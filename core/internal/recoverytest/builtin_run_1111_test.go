package recoverytest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// #1111: Run's branch for a set that declares no validator.

type spyChecker struct {
	inner   BuiltinChecker
	applies int
	checks  int
}

func (s *spyChecker) Applies(name string) (string, bool) {
	s.applies++
	return s.inner.Applies(name)
}

func (s *spyChecker) Check(ctx context.Context, in BuiltinInput) BuiltinReport {
	s.checks++
	return s.inner.Check(ctx, in)
}

func newSpy() *spyChecker { return &spyChecker{inner: StructureCheck{}} }

type builtinEnv struct {
	j   *state.Journal
	a   model.ArtifactID
	rc  *fileRecoverer
	spy *spyChecker
}

// builtinEnvFor journals an artifact called name whose bytes are the fixture.
func builtinEnvFor(t *testing.T, name, fixtureFile string) builtinEnv {
	t.Helper()
	b := fixtureBytes(t, fixtureFile)
	j := openJournal(t)
	a := seed(t, j, name, state.MediumLocal, b, testNow.Add(-time.Hour))
	return builtinEnv{j: j, a: a, rc: &fileRecoverer{dir: t.TempDir(), served: b}, spy: newSpy()}
}

func (e builtinEnv) run(t *testing.T, v Validator, cfg Config, req Request, stages *[]string) Result {
	t.Helper()
	d := Deps{Journal: e.j, Recoverer: e.rc, Validator: v, Builtin: e.spy, Now: func() time.Time { return testNow }}
	if stages != nil {
		d.Stage = func(id string) { *stages = append(*stages, id) }
	}
	res, err := Run(context.Background(), d, setID(t), cfg, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestIssue1111_TheIdentityIsReservedAndHashedFromItsDescriptor(t *testing.T) {
	if BuiltinValidatorID != "retnd-builtin-structure" {
		t.Errorf("id = %q", BuiltinValidatorID)
	}
	if BuiltinDescriptor != "retnd-builtin-structure v1: gzip zstd tar tar+gzip tar+zstd" {
		t.Errorf("descriptor = %q", BuiltinDescriptor)
	}
	if BuiltinValidatorHash() != sha([]byte(BuiltinDescriptor)) {
		t.Errorf("hash = %s", BuiltinValidatorHash())
	}
	for _, c := range BuiltinValidatorID {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			t.Errorf("the id uses %q, outside [a-z0-9-]", c)
		}
	}
	for id, want := range map[string]bool{
		"retnd-builtin-structure": true, "retnd-foo": true, "retnd-": true,
		"10-check": false, "pg-restore-check": false, "retnd": false, "xretnd-foo": false, "Retnd-foo": false, "": false,
	} {
		if IsBuiltin(id) != want {
			t.Errorf("IsBuiltin(%q) = %v, want %v", id, !want, want)
		}
	}
}

func TestIssue1111_AWellFormedFileIsARecordedPassUnderTheReservedIdentity(t *testing.T) {
	for _, tc := range []struct{ name, fixture, magic string }{
		{"dump.tar.zst", "tarzst-good.tar.zst", "28 b5 2f fd"},
		{"dump.tar.gz", "targz-good.tar.gz", "1f 8b"},
		{"dump.tgz", "targz-good.tar.gz", "1f 8b"},
		{"dump.zst", "zst-checksum.zst", "28 b5 2f fd"},
		{"dump.gz", "gz-single.gz", "1f 8b"},
		{"dump.tar", "tar-plain.tar", "ustar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := builtinEnvFor(t, tc.name, tc.fixture)
			var stages []string
			res := e.run(t, nil, cfgFor(), Request{}, &stages)
			if res.State != state.RecoveryPassed {
				t.Fatalf("state = %q (%s)", res.State, res.Reason)
			}
			if res.Check != "builtin" || res.ValidatorID != BuiltinValidatorID || res.ValidatorHash != BuiltinValidatorHash() {
				t.Errorf("check/id/hash = %q/%q/%q", res.Check, res.ValidatorID, res.ValidatorHash)
			}
			if res.CheckFormat == "" {
				t.Error("no format line")
			}
			p := copyOf(t, e.j, e.a, state.MediumLocal)
			if p.RecoveryState != state.RecoveryPassed || p.RecoveryValidatorID != BuiltinValidatorID || p.RecoveryValidatorHash != BuiltinValidatorHash() {
				t.Errorf("record = %q / %q / %q", p.RecoveryState, p.RecoveryValidatorID, p.RecoveryValidatorHash)
			}
			if p.RecoveryDigestMatched == nil || !*p.RecoveryDigestMatched || p.RecoveryTermination != state.RecoveryTerminationExited {
				t.Errorf("digest/termination = %v/%q", p.RecoveryDigestMatched, p.RecoveryTermination)
			}
			if p.RecoveryCleanupState != state.RecoveryCleanupRemoved || e.rc.removed != 1 {
				t.Errorf("the staged copy was not removed: %q, removed %d", p.RecoveryCleanupState, e.rc.removed)
			}
			if got := strings.Join(stages, ","); got != "resolving_copy,reading,verifying,validating,cleaning_temporary_files,complete" {
				t.Errorf("stages = %s", got)
			}
			if e.spy.checks != 1 {
				t.Errorf("checks = %d", e.spy.checks)
			}
		})
	}
}

func TestIssue1111_TheStoredOutputIsTheFourRecords(t *testing.T) {
	e := builtinEnvFor(t, "dump.zst", "zst-checksum.zst")
	res := e.run(t, nil, cfgFor(), Request{}, nil)
	var lines []string
	for _, r := range res.Output {
		lines = append(lines, r.Text)
	}
	want := []string{
		"built-in structure check v1; no validator is declared for this backup set",
		"recognised: zstd (magic 28 b5 2f fd; the name ends .zst)",
		"read 1 frame, 300,000 bytes decoded and discarded, content checksum present and verified",
		"this proves the file decodes end to end; it does not prove an application can use it",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("output =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if !strings.HasPrefix(res.CheckFormat, "zstd, 1 frame, content checksum verified, ") || !strings.HasSuffix(res.CheckFormat, "decoded and discarded") {
		t.Errorf("format = %q", res.CheckFormat)
	}
}

func TestIssue1111_AFileThatIsNotWhatItsNameSaysIsAFailedVerdictOfTheCopyAsIngested(t *testing.T) {
	for _, tc := range []struct{ name, fixture, wantIn string }{
		{"dump.gz", "gz-truncated.gz", "does not read as a complete gzip stream"},
		{"dump.gz", "gz-random.gz", "not"},
		{"dump.tar.zst", "tarzst-truncated-inner.tar.zst", "tar"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			e := builtinEnvFor(t, tc.name, tc.fixture)
			res := e.run(t, nil, cfgFor(), Request{}, nil)
			if res.State != state.RecoveryFailed {
				t.Fatalf("state = %q (%s)", res.State, res.Reason)
			}
			if !strings.Contains(res.Reason, "matched its recorded digest") || !strings.Contains(res.Reason, tc.wantIn) {
				t.Errorf("reason = %q", res.Reason)
			}
			if res.ValidatorID != BuiltinValidatorID || res.Check != "builtin" {
				t.Errorf("id/check = %q/%q", res.ValidatorID, res.Check)
			}
			p := copyOf(t, e.j, e.a, state.MediumLocal)
			if p.RecoveryState != state.RecoveryFailed || p.RecoveryValidatorID != BuiltinValidatorID {
				t.Errorf("record = %q / %q", p.RecoveryState, p.RecoveryValidatorID)
			}
			if e.rc.removed != 1 {
				t.Errorf("removed = %d", e.rc.removed)
			}
		})
	}
}

func TestIssue1111_AStoppedCheckCouldNotComplete_AndCarriesNoValidatorIdentity(t *testing.T) {
	e := builtinEnvFor(t, "dump.zst", "zst-window2g.zst")
	res := e.run(t, nil, cfgFor(), Request{}, nil)
	if res.State != state.RecoveryCouldNotComplete {
		t.Fatalf("state = %q (%s)", res.State, res.Reason)
	}
	if res.ValidatorID != "" || res.Check != "builtin" {
		t.Errorf("id/check = %q/%q", res.ValidatorID, res.Check)
	}
	if !strings.Contains(res.Reason, "Declare a validator") {
		t.Errorf("reason = %q", res.Reason)
	}
	p := copyOf(t, e.j, e.a, state.MediumLocal)
	if p.RecoveryState != state.RecoveryCouldNotComplete || p.RecoveryValidatorID != "" {
		t.Errorf("record = %q / %q", p.RecoveryState, p.RecoveryValidatorID)
	}
	if e.rc.removed != 1 {
		t.Errorf("removed = %d", e.rc.removed)
	}
}

func TestIssue1111_AnUnrecognisedTypeIsRefusedBeforeAnythingIsStaged(t *testing.T) {
	e := builtinEnvFor(t, "backup.dump", "gz-single.gz")
	res := e.run(t, nil, cfgFor(), Request{}, nil)
	if res.State != state.RecoveryNotConfigured {
		t.Fatalf("state = %q", res.State)
	}
	for _, want := range []string{"(.dump)", "nothing was read", "NAME.local.sh", "no recovery-test validator is declared for this backup set"} {
		if !strings.Contains(res.Reason, want) {
			t.Errorf("reason %q lacks %q", res.Reason, want)
		}
	}
	if e.rc.recovered != 0 {
		t.Errorf("Recoverer.Recover was called %d times: a billed copy must never be paid for to learn the check cannot apply", e.rc.recovered)
	}
	if e.spy.checks != 0 {
		t.Errorf("checks = %d", e.spy.checks)
	}
}

func TestIssue1111_BuiltinCheckOffRefusesAndStagesNothing(t *testing.T) {
	e := builtinEnvFor(t, "dump.tar.gz", "targz-good.tar.gz")
	cfg := cfgFor()
	cfg.BuiltinCheck = "off"
	res := e.run(t, nil, cfg, Request{}, nil)
	if res.State != state.RecoveryNotConfigured {
		t.Fatalf("state = %q", res.State)
	}
	if !strings.Contains(res.Reason, "the built-in check is off for this backup set and no validator is declared") {
		t.Errorf("reason = %q", res.Reason)
	}
	if e.rc.recovered != 0 || e.spy.applies != 0 || e.spy.checks != 0 {
		t.Errorf("recovered/applies/checks = %d/%d/%d", e.rc.recovered, e.spy.applies, e.spy.checks)
	}
}

func TestIssue1111_ADeclaredValidatorRunsAndTheBuiltinIsNeverConsulted(t *testing.T) {
	e := builtinEnvFor(t, "dump.tar.gz", "targz-good.tar.gz")
	v := accepting()
	res := e.run(t, v, cfgFor(), Request{}, nil)
	if res.State != state.RecoveryPassed || res.ValidatorID != "10-check.local.sh" || res.Check != "" {
		t.Fatalf("state/id/check = %q/%q/%q", res.State, res.ValidatorID, res.Check)
	}
	if len(v.calls) != 1 || e.spy.applies != 0 || e.spy.checks != 0 {
		t.Errorf("validator calls %d, builtin applies/checks %d/%d", len(v.calls), e.spy.applies, e.spy.checks)
	}
}

func TestIssue1111_ADeclaredValidatorThatCannotBeExaminedNeverFallsBackToTheBuiltin(t *testing.T) {
	e := builtinEnvFor(t, "dump.tar.gz", "targz-good.tar.gz")
	res := e.run(t, &fakeValidator{cerr: errors.New("the declared validator pg.local.sh was not found")}, cfgFor(), Request{}, nil)
	if res.State != state.RecoveryCouldNotComplete || !strings.Contains(res.Reason, "was not found") {
		t.Fatalf("state/reason = %q / %q", res.State, res.Reason)
	}
	if e.spy.applies != 0 || e.spy.checks != 0 || e.rc.recovered != 0 {
		t.Errorf("builtin applies/checks %d/%d, recovered %d", e.spy.applies, e.spy.checks, e.rc.recovered)
	}
}

func TestIssue1111_AnEmptyValidatorDirectoryReachesTheBuiltin(t *testing.T) {
	e := builtinEnvFor(t, "dump.tar.gz", "targz-good.tar.gz")
	res := e.run(t, &fakeValidator{configured: false}, cfgFor(), Request{}, nil)
	if res.State != state.RecoveryPassed || res.Check != "builtin" {
		t.Errorf("state/check = %q/%q (%s)", res.State, res.Check, res.Reason)
	}
}

func TestIssue1111_TheCostGateAndTheDigestGateStillComeFirst(t *testing.T) {
	t.Run("a billed copy is refused for cost and nothing is staged", func(t *testing.T) {
		b := fixtureBytes(t, "targz-good.tar.gz")
		j := openJournal(t)
		a := seed(t, j, "dump.tar.gz", "offsite", b, testNow.Add(-time.Hour))
		e := builtinEnv{j: j, a: a, rc: &fileRecoverer{dir: t.TempDir(), served: b}, spy: newSpy()}
		res := e.run(t, nil, cfgFor(), Request{}, nil)
		if res.State != state.RecoveryRefusedForCost || e.rc.recovered != 0 || e.spy.checks != 0 {
			t.Errorf("state %q, recovered %d, checks %d", res.State, e.rc.recovered, e.spy.checks)
		}
	})
	t.Run("bytes that do not match the digest are never read as a format", func(t *testing.T) {
		e := builtinEnvFor(t, "dump.tar.gz", "targz-good.tar.gz")
		e.rc.served = fixtureBytes(t, "gz-single.gz")
		res := e.run(t, nil, cfgFor(), Request{}, nil)
		if res.State != state.RecoveryFailed || res.ValidatorID != "" || e.spy.checks != 0 {
			t.Errorf("state %q id %q checks %d", res.State, res.ValidatorID, e.spy.checks)
		}
		if res.DigestMatched == nil || *res.DigestMatched {
			t.Errorf("digest matched = %v", res.DigestMatched)
		}
	})
}

func TestIssue1111_AScheduledRunUsesTheBuiltinAndStillNeverPays(t *testing.T) {
	e := builtinEnvFor(t, "dump.tar.gz", "targz-good.tar.gz")
	res := e.run(t, nil, cfgFor(), Request{Scheduled: true}, nil)
	if res.State != state.RecoveryPassed || res.Check != "builtin" {
		t.Errorf("state/check = %q/%q", res.State, res.Check)
	}
	p := copyOf(t, e.j, e.a, state.MediumLocal)
	if p.RecoveryNextDueAt == nil || !p.RecoveryNextDueAt.Equal(testNow.Add(cfgFor().Cadence)) {
		t.Errorf("next due = %v: a pass is due again on the cadence", p.RecoveryNextDueAt)
	}
}
