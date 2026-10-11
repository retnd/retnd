package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// The panel's findings on the recovery side of EPIC Q, driven through the
// real service the way `retnd serve` builds it: the host knobs reach the
// engine, a recovery test reads from a storage medium through the same staged
// path an operator's recovery uses, a real recovery says which stage it is in,
// and the serving scheduler runs every assurance tick without a script's help.

// stubStages answers every stage request as a validator that accepts, and
// records what it was asked.
type stubStages struct {
	mu      sync.Mutex
	script  string
	exit    int
	reqs    []app.StageRequest
	notices []app.StageRequest
}

func (s *stubStages) RunStage(_ context.Context, req app.StageRequest) (app.StageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(req.SetDir, "assurance-after") {
		s.notices = append(s.notices, req)
		return app.StageResult{Configured: true, Scripts: []app.StageScript{{Name: "10-notify.local.sh", State: "success"}}}, nil
	}
	if req.Only != "" && req.Only != s.script {
		return app.StageResult{}, app.ErrStageScriptMissing
	}
	res := app.StageResult{Configured: true, Scripts: []app.StageScript{{
		Name: s.script, SHA256: strings.Repeat("ab", 32), Target: "local", State: "success", Disposition: "exited", ExitCode: &s.exit,
	}}}
	if req.DryRun {
		res.Scripts[0].State = ""
		res.Scripts[0].ExitCode = nil
		res.Scripts[0].Disposition = ""
		return res, nil
	}
	s.reqs = append(s.reqs, req)
	return res, nil
}

func (s *stubStages) ran() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// withStages replaces the service's stage runner, as a deployment with a
// workflow root would have it.
func withStages(f *recoveryFixture, s *stubStages) {
	f.b.state.Load().inner.Stages = s
}

func requireRecoveryTest(cfg *config.Config) {
	cfg.Sources[0].BackupSets[0].Assurance.RecoveryTest.Mode = config.RecoveryTestRequired
}

// seedComplete journals a COMPLETE artifact with a copy on each medium named,
// "local" meaning the fixture's local directory.
func seedComplete(t *testing.T, f *recoveryFixture, name string, mediums ...string) model.ArtifactID {
	t.Helper()
	ctx := context.Background()
	set, _ := model.NewBackupSetID("production", "postgres")
	a, err := model.NewArtifactID(set, name)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	if _, err := f.b.journal.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "d-" + name, To: string(lifecycle.Discovered), OccurredAt: at, RemotePath: "/r/" + name,
	}); err != nil {
		t.Fatal(err)
	}
	size := int64(len(f.data))
	verified := at
	tr := state.Transition{
		Artifact: a, Key: "c-" + name, From: string(lifecycle.Discovered), To: string(lifecycle.Complete), OccurredAt: at,
		Hashes:   &state.HashUpdate{Hash: sha(f.data), Alg: "sha256"},
		Transfer: &state.TransferResult{BytesTransferred: size, Checksummed: true},
	}
	for i, m := range mediums {
		var pu state.PlacementUpdate
		if m == state.MediumLocal {
			if err := os.MkdirAll(f.localDir, 0o755); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(f.localDir, name)
			if err := os.WriteFile(p, f.data, 0o600); err != nil {
				t.Fatal(err)
			}
			pu = state.PlacementUpdate{Medium: m, Location: p, Size: &size, Hash: sha(f.data), HashAlg: "sha256", VerificationClass: state.VerificationContent, VerifiedAt: &verified, Status: state.PlacementActive}
			tr.LocalPath = &p
		} else {
			key := "prefix/" + name
			f.store.mu.Lock()
			f.store.objects[key] = f.data
			f.store.mu.Unlock()
			pu = state.PlacementUpdate{Medium: m, Location: key, Size: &size, Hash: sha(f.data), HashAlg: "sha256", VerificationClass: state.VerificationContent, VerifiedAt: &verified, Status: state.PlacementActive}
		}
		if i == 0 {
			tr.Placement = &pu
			if _, err := f.b.journal.RecordTransition(ctx, tr); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := f.b.journal.RecordTransition(ctx, state.Transition{
			Artifact: a, Key: "p" + m + name, From: string(lifecycle.Complete), To: string(lifecycle.Complete), OccurredAt: at, Placement: &pu,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func claimOf(t *testing.T, f *recoveryFixture, a model.ArtifactID, medium string) state.Placement {
	t.Helper()
	rec, err := f.b.journal.Get(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rec.Placements {
		if p.Medium == medium {
			return p
		}
	}
	t.Fatalf("no copy on %s", medium)
	return state.Placement{}
}

func setID(t *testing.T) model.BackupSetID {
	t.Helper()
	id, _ := model.NewBackupSetID("production", "postgres")
	return id
}

// --- C3: the host knobs reach the engine ---

func TestAssuranceHostKnobsReachTheEngine(t *testing.T) {
	root := filepath.Join(t.TempDir(), "my-staging")
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: func(c *config.Config) {
		c.Assurance.StagingRoot = root
		c.Assurance.StagingTTL = 2 * time.Hour
		c.Assurance.MaxConcurrentFullReads = 3
		c.Assurance.MaxPerDestination = 2
	}})
	h := f.b.recoveryHost()
	if h.unavailable {
		t.Fatalf("recovery is unavailable: %s", h.unavailableWhy)
	}
	if h.staging.Root != root {
		t.Errorf("staging root = %q, want the configured %q", h.staging.Root, root)
	}
	if h.staging.OutputTTL != 2*time.Hour {
		t.Errorf("output TTL = %s, want the configured staging_ttl of 2h", h.staging.OutputTTL)
	}
	if h.staging.PartialTTL > 2*time.Hour {
		t.Errorf("partial TTL = %s, which outlives the staging_ttl", h.staging.PartialTTL)
	}
	got := f.b.RecoveryLimits()
	if got.MaxConcurrentReads != 3 || got.MaxReadsPerDestination != 2 {
		t.Errorf("effective read ceilings = %d / %d, want the configured 3 / 2", got.MaxConcurrentReads, got.MaxReadsPerDestination)
	}
	var releases []func()
	for i := 0; i < 2; i++ {
		r, err := h.reads.Acquire("d")
		if err != nil {
			t.Fatalf("read %d on one destination was refused under a per-destination ceiling of 2: %v", i+1, err)
		}
		releases = append(releases, r)
	}
	if _, err := h.reads.Acquire("d"); err == nil {
		t.Error("a third read of one destination was admitted: the per-destination ceiling is not enforced")
	}
	for _, r := range releases {
		r()
	}

	// And a recovery really writes there.
	res, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Path, root+string(filepath.Separator)) {
		t.Errorf("the recovered file landed at %s, not under the configured root %s", res.Path, root)
	}
}

func TestADefaultDeploymentStagesBesideTheStateDatabaseInOneDirectory(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	h := f.b.recoveryHost()
	if want := filepath.Join(f.dir, "state", config.DefaultStagingDirName); h.staging.Root != want {
		t.Errorf("staging root = %q, want %q (assurance.staging_root's default)", h.staging.Root, want)
	}
	if got := f.b.RecoveryLimits(); got.MaxConcurrentReads != config.DefaultMaxConcurrentReads || got.MaxReadsPerDestination != config.DefaultMaxPerDestination {
		t.Errorf("default ceilings = %d / %d, want the settings' %d / %d", got.MaxConcurrentReads, got.MaxReadsPerDestination, config.DefaultMaxConcurrentReads, config.DefaultMaxPerDestination)
	}
}

func TestAStagingRootThatFailsCustodyMakesRecoveryUnavailableRatherThanStagingThere(t *testing.T) {
	bad := t.TempDir()
	if err := os.Chmod(bad, 0o777); err != nil {
		t.Fatal(err)
	}
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: func(c *config.Config) { c.Assurance.StagingRoot = bad }})
	_, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")}, nil)
	if err == nil || codeOf(t, err) != RecoveryUnavailable {
		t.Fatalf("a world-writable staging root: want %s, got %v", RecoveryUnavailable, err)
	}
	if entries, _ := os.ReadDir(bad); len(entries) != 0 {
		t.Errorf("%d entries were staged in a directory that failed custody", len(entries))
	}
}

// --- C14: a recovery test of a copy on a storage medium ---

func TestRecoveryTestOfAMediumCopyStagesAndPassesWhenAcknowledged(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	stages := &stubStages{script: "pg-check.local.sh"}
	withStages(f, stages)
	a := seedComplete(t, f, "away.dump", "warm")

	res, err := f.b.RunRecoveryTest(ctx, setID(t), RecoveryTestOptions{Artifact: &a, AcknowledgeCost: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != state.RecoveryPassed {
		t.Fatalf("State = %q (%s), want a pass: the copy was read from the medium, verified, and handed to the validator", res.State, res.Reason)
	}
	if got := claimOf(t, f, a, "warm"); got.RecoveryState != state.RecoveryPassed || got.RecoveryDigestMatched == nil || !*got.RecoveryDigestMatched {
		t.Errorf("recorded %q / %v", got.RecoveryState, got.RecoveryDigestMatched)
	}
	if stages.ran() != 1 {
		t.Errorf("validator runs = %d", stages.ran())
	}
	if f.store.opens.Load() != 1 {
		t.Errorf("the medium was opened %d times, want exactly one read", f.store.opens.Load())
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Errorf("staging not cleaned: %v", e)
	}
}

func TestAScheduledRecoveryTestNeverPaysForAChargeableMedium(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	withStages(f, &stubStages{script: "pg-check.local.sh"})
	a := seedComplete(t, f, "away.dump", "warm")

	f.b.state.Load().inner.RecoveryTestTick(ctx)

	if got := claimOf(t, f, a, "warm"); got.RecoveryState != state.RecoveryRefusedForCost {
		t.Errorf("a scheduled test of an s3 copy recorded %q, want refused_for_cost", got.RecoveryState)
	}
	if f.store.opens.Load() != 0 {
		t.Errorf("a scheduled test opened a chargeable object %d times", f.store.opens.Load())
	}
}

func TestAScheduledRecoveryTestOfAFreeLocalDirMediumRuns(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: func(c *config.Config) {
		requireRecoveryTest(c)
		c.StorageMediums = append(c.StorageMediums, config.StorageMedium{ID: "freevol", Type: config.StorageMediumTypeLocalVolume, Path: "/mnt/free"})
	}})
	stages := &stubStages{script: "pg-check.local.sh"}
	withStages(f, stages)
	a := seedComplete(t, f, "free.dump", "freevol")

	f.b.state.Load().inner.RecoveryTestTick(ctx)

	if got := claimOf(t, f, a, "freevol"); got.RecoveryState != state.RecoveryPassed {
		t.Errorf("a scheduled test of a free medium recorded %q (%s), want passed: nothing is charged for reading it", got.RecoveryState, got.RecoveryFailureReason)
	}
}

func TestARecoveryTestOfAMediumCopyWithWrongBytesFailsAtTheDigestGate(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	stages := &stubStages{script: "pg-check.local.sh"}
	withStages(f, stages)
	a := seedComplete(t, f, "rot.dump", "warm")
	f.store.flipAt = 7

	res, err := f.b.RunRecoveryTest(ctx, setID(t), RecoveryTestOptions{Artifact: &a, AcknowledgeCost: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != state.RecoveryFailed || res.DigestMatched == nil || *res.DigestMatched {
		t.Fatalf("result = %q / %v, want a failure at the digest gate", res.State, res.DigestMatched)
	}
	if stages.ran() != 0 {
		t.Errorf("the validator ran against bytes that did not match: %d runs", stages.ran())
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Errorf("a failed test left %v behind", e)
	}
}

func TestARecoveryTestOfAnArchiveCopyIsRefusedAndNeverAPass(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	stages := &stubStages{script: "pg-check.local.sh"}
	withStages(f, stages)
	a := seedComplete(t, f, "cold.dump", "cold")

	res, err := f.b.RunRecoveryTest(ctx, setID(t), RecoveryTestOptions{Artifact: &a, AcknowledgeCost: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.State == state.RecoveryPassed {
		t.Fatalf("an archive-class copy that needs a restore passed")
	}
	if res.State != state.RecoveryCouldNotComplete {
		t.Errorf("State = %q, want could_not_complete (a test never starts a provider restore)", res.State)
	}
	if f.store.restores.Load() != 0 {
		t.Errorf("a recovery test started %d provider restores", f.store.restores.Load())
	}
}

// --- C2: a real recovery says which stage it is in ---

func (f *recoveryFixture) eventsNamed(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(f.log.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if m["event"] == name {
			out = append(out, m)
		}
	}
	return out
}

func TestARealRecoveryEmitsTheStageEventsTheUIRenders(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	if _, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil); err != nil {
		t.Fatal(err)
	}

	starts := f.eventsNamed(t, obs.EventRecoveryStart)
	if len(starts) != 1 {
		t.Fatalf("recovery_start events = %d, want 1\n%s", len(starts), f.log.String())
	}
	opID := starts[0]["op_id"]
	var got []string
	for _, ev := range f.eventsNamed(t, obs.EventRecoveryStage) {
		if ev["op_id"] != opID {
			t.Errorf("a stage line carries operation %v, want %v", ev["op_id"], opID)
		}
		id := ev["stage"].(string)
		if obs.StageLabel(id) == "" || ev["stage_label"] != obs.StageLabel(id) {
			t.Errorf("stage %q / label %q is not in obs.RecoveryStages", id, ev["stage_label"])
		}
		got = append(got, id)
	}
	want := []string{"resolving_copy", "reading", "verifying", "writing_target", "cleaning_temporary_files", "complete"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stages = %v, want %v", got, want)
	}
	ends := f.eventsNamed(t, obs.EventRecoveryEnd)
	if len(ends) != 1 || ends[0]["result"] != "success" || ends[0]["op_id"] != opID {
		t.Errorf("recovery_end = %v", ends)
	}
}

func TestAFailedRecoveryEndsWithAnErrorAndNeverSaysComplete(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.store.flipAt = 3
	if _, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil); err == nil {
		t.Fatal("a corrupt copy was recovered")
	}
	for _, ev := range f.eventsNamed(t, obs.EventRecoveryStage) {
		if ev["stage"] == "complete" {
			t.Errorf("a failed recovery reported the Complete stage")
		}
	}
	ends := f.eventsNamed(t, obs.EventRecoveryEnd)
	if len(ends) != 1 || ends[0]["result"] == "success" {
		t.Errorf("recovery_end = %v, want one non-success line", ends)
	}
}

// --- C2/C10: under `serve`, with a default configuration and no script's help ---

func TestServeSchedulerScrubsADefaultConfigurationWithNoRevalidationBlock(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{})
	if err := f.b.state.Load().inner.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	a := seedComplete(t, f, "default.dump", state.MediumLocal)
	if got := claimOf(t, f, a, state.MediumLocal).AssuranceState; got != state.AssuranceUnknown {
		t.Fatalf("precondition: %q", got)
	}

	f.b.tickScrub(ctx)

	if got := claimOf(t, f, a, state.MediumLocal).AssuranceState; got != state.AssurancePassed {
		t.Errorf("after one serve tick on a default install the copy is %q, want passed: the default install scrubs", got)
	}
}

func TestServeSchedulerRunsADueRecoveryTestWithNoScriptOtherThanTheValidator(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	if err := f.b.state.Load().inner.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	requireRecoveryTest(f.b.state.Load().inner.Config)
	stages := &stubStages{script: "pg-check.local.sh"}
	withStages(f, stages)
	a := seedComplete(t, f, "due.dump", state.MediumLocal)

	f.b.tickScrub(ctx)

	if got := claimOf(t, f, a, state.MediumLocal).RecoveryState; got != state.RecoveryPassed {
		t.Errorf("after one serve tick a due recovery test is %q, want passed (RecoveryTestTick had no caller under serve)", got)
	}
	if len(stages.notices) != 0 {
		t.Errorf("a passing run told the operator: %v", stages.notices)
	}
}

func TestAServeTickLetsAFailingRecoveryTestReachTheAlertDispatcher(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: func(c *config.Config) {
		requireRecoveryTest(c)
		c.Alerts = config.Alerts{Enabled: true, RepeatedFailureThreshold: 3}
	}})
	requireRecoveryTest(f.b.state.Load().inner.Config)
	stages := &stubStages{script: "pg-check.local.sh", exit: 7}
	withStages(f, stages)
	var delivered bytes.Buffer
	sink := alertSinkFunc(func(kind, scope string) { delivered.WriteString(kind + " " + scope + "\n") })
	if !f.b.state.Load().inner.EnableAlerts(sink) {
		t.Fatal("alerts did not enable")
	}
	seedComplete(t, f, "bad.dump", state.MediumLocal)

	f.b.tickScrub(ctx)
	f.b.state.Load().inner.AlertTick(ctx)

	if !strings.Contains(delivered.String(), "RECOVERY_TEST_FAILING production/postgres") {
		t.Errorf("delivered = %q, want RECOVERY_TEST_FAILING for the set with no assurance-after script anywhere", delivered.String())
	}
}

// alertSinkFunc is an alert.Sink that reports the kind and scope it was given.
type alertSinkFunc func(kind, scope string)

func (f alertSinkFunc) Deliver(_ context.Context, a alert.Alert) error {
	f(string(a.Kind), a.Scope)
	return nil
}
