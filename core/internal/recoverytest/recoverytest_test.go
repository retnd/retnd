package recoverytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/revalidate"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q, #980. Every case runs a real journal and, where the claim is about
// a process, a real process. The recurring assertion is on the durable
// record of the copy, because that is what an operator reads later.

var testNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func openJournal(t *testing.T) *state.Journal {
	t.Helper()
	j, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func setID(t *testing.T) model.BackupSetID {
	t.Helper()
	id, err := model.NewBackupSetID("production", "postgres-primary")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seed journals one COMPLETE artifact whose single copy is on `medium`
// ("local" or a medium id) and records the digest of content.
func seed(t *testing.T, j *state.Journal, name, medium string, content []byte, at time.Time) model.ArtifactID {
	t.Helper()
	ctx := context.Background()
	a, err := model.NewArtifactID(setID(t), name)
	if err != nil {
		t.Fatal(err)
	}
	path := "/backups/" + name
	if _, err := j.RecordTransition(ctx, state.Transition{Artifact: a, Key: "d-" + name, From: "", To: "DISCOVERED", OccurredAt: at, RemotePath: "/r/" + name}); err != nil {
		t.Fatal(err)
	}
	size := int64(len(content))
	if _, err := j.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "c-" + name, From: "DISCOVERED", To: "COMPLETE", OccurredAt: at, LocalPath: &path,
		Hashes:   &state.HashUpdate{Hash: sha(content), Alg: "sha256"},
		Transfer: &state.TransferResult{BytesTransferred: size, Checksummed: true},
		Placement: &state.PlacementUpdate{
			Medium: medium, Location: path, Size: &size, Hash: sha(content), HashAlg: "sha256",
			VerificationClass: state.VerificationContent, Status: state.PlacementActive,
		},
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

func copyOf(t *testing.T, j *state.Journal, a model.ArtifactID, medium string) state.Placement {
	t.Helper()
	rec, err := j.Get(context.Background(), a)
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

// fileRecoverer stages bytes into a temp directory. served is what it writes,
// which a test sets to something other than the recorded content to stage a
// recovery that produced the wrong bytes.
type fileRecoverer struct {
	dir        string
	served     []byte
	recoverErr error
	removeErr  error
	recovered  int
	removed    int
	lastAck    bool
}

func (r *fileRecoverer) Recover(_ context.Context, req RecoverRequest) (Staged, error) {
	r.recovered++
	r.lastAck = req.AcknowledgeCost
	if r.recoverErr != nil {
		return Staged{}, r.recoverErr
	}
	p := filepath.Join(r.dir, fmt.Sprintf("staged-%d", r.recovered))
	if err := os.WriteFile(p, r.served, 0o600); err != nil {
		return Staged{}, err
	}
	return Staged{Path: p}, nil
}

func (r *fileRecoverer) Remove(_ context.Context, s Staged) error {
	r.removed++
	if r.removeErr != nil {
		return r.removeErr
	}
	return os.Remove(s.Path)
}

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "check-restore.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func cfgFor(exe string, timeout time.Duration) Config {
	return Config{Command: &config.Command{Executable: exe, Timeout: config.Duration(timeout)}, Cadence: 24 * time.Hour}
}

var content = []byte("the bytes of a database dump")

func newEnv(t *testing.T) (*state.Journal, model.ArtifactID, *fileRecoverer) {
	t.Helper()
	j := openJournal(t)
	a := seed(t, j, "dump.sql", state.MediumLocal, content, testNow.Add(-time.Hour))
	return j, a, &fileRecoverer{dir: t.TempDir(), served: content}
}

func run(t *testing.T, j *state.Journal, rc *fileRecoverer, cfg Config, req Request) Result {
	t.Helper()
	res, err := Run(context.Background(), Deps{
		Journal: j, Recoverer: rc, Now: func() time.Time { return testNow },
	}, setID(t), cfg, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// --- the happy path, and what it records ---

func TestAMatchingDigestAndAnAcceptingValidatorIsARecordedPass(t *testing.T) {
	j, a, rc := newEnv(t)
	exe := script(t, "test -s \"$1\" || exit 1\nexit 0\n")

	res := run(t, j, rc, cfgFor(exe, 5*time.Second), Request{})
	if res.State != state.RecoveryPassed {
		t.Fatalf("State = %q (%s)", res.State, res.Reason)
	}
	p := copyOf(t, j, a, state.MediumLocal)
	if p.RecoveryState != state.RecoveryPassed || p.RecoveryDigestMatched == nil || !*p.RecoveryDigestMatched {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryDigestMatched)
	}
	if p.RecoveryValidatorID != "check-restore.sh" {
		t.Errorf("validator id = %q, want the base name and never a path", p.RecoveryValidatorID)
	}
	if want := hashOfFile(t, exe); p.RecoveryValidatorHash != want {
		t.Errorf("validator hash = %q, want %q", p.RecoveryValidatorHash, want)
	}
	if p.RecoveryTermination != lifecycle.TerminationExited || p.RecoveryCleanupState != state.RecoveryCleanupRemoved {
		t.Errorf("termination/cleanup = %q/%q", p.RecoveryTermination, p.RecoveryCleanupState)
	}
	if p.RecoveryNextDueAt == nil || !p.RecoveryNextDueAt.Equal(testNow.Add(24*time.Hour)) {
		t.Errorf("RecoveryNextDueAt = %v", p.RecoveryNextDueAt)
	}
	if p.RecoveryBytesRead == nil || *p.RecoveryBytesRead != int64(len(content)) {
		t.Errorf("RecoveryBytesRead = %v", p.RecoveryBytesRead)
	}
	if rc.removed != 1 {
		t.Errorf("recovered copy removed %d times, want 1", rc.removed)
	}
	if res.Artifact != a || res.Medium != state.MediumLocal {
		t.Errorf("result names %s on %s", res.Artifact, res.Medium)
	}
	if res.PlacementID <= 0 || res.PlacementID != p.ID {
		t.Errorf("result names placement %d, the copy is placement %d", res.PlacementID, p.ID)
	}
}

func hashOfFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha(b)
}

func TestAChangedValidatorIsADifferentRecordedIdentity(t *testing.T) {
	j, a, rc := newEnv(t)
	exe := script(t, "exit 0\n")
	run(t, j, rc, cfgFor(exe, 5*time.Second), Request{})
	first := copyOf(t, j, a, state.MediumLocal).RecoveryValidatorHash

	if err := os.WriteFile(exe, []byte("#!/bin/sh\n# edited\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, j, rc, cfgFor(exe, 5*time.Second), Request{})
	second := copyOf(t, j, a, state.MediumLocal).RecoveryValidatorHash
	if first == second {
		t.Error("an edited validator recorded the same hash: an old pass would be silently inherited by new code")
	}
}

// --- the digest gate ---

// badBytesAreNeverAPass is the assertion the digest-gate mutant must turn red.
// It stages a recovery whose bytes are wrong, runs a validator that exits 0
// without reading them, and requires that the outcome is not a pass and that
// the journal does not say one.
func badBytesAreNeverAPass(t *testing.T) error {
	t.Helper()
	j, a, rc := newEnv(t)
	rc.served = []byte("a different set of bytes entirely")
	exe := script(t, "exit 0\n") // exits 0 having read nothing

	res, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Now: func() time.Time { return testNow }},
		setID(t), cfgFor(exe, 5*time.Second), Request{})
	if err != nil {
		return err
	}
	p := copyOf(t, j, a, state.MediumLocal)
	if res.State == state.RecoveryPassed || p.RecoveryState == state.RecoveryPassed {
		return fmt.Errorf("a validator that exited 0 without opening wrong bytes produced a pass (result %q, journal %q)", res.State, p.RecoveryState)
	}
	return nil
}

func TestAValidatorThatExitsZeroWithoutReadingDoesNotProduceAPassOverWrongBytes(t *testing.T) {
	if err := badBytesAreNeverAPass(t); err != nil {
		t.Fatal(err)
	}
}

func TestADigestMismatchIsAFailureAndTheValidatorNeverRuns(t *testing.T) {
	j, a, rc := newEnv(t)
	rc.served = []byte("flipped")
	marker := filepath.Join(t.TempDir(), "validator-ran")
	exe := script(t, fmt.Sprintf("touch %s\nexit 0\n", marker))

	res := run(t, j, rc, cfgFor(exe, 5*time.Second), Request{})
	if res.State != state.RecoveryFailed || res.DigestMatched == nil || *res.DigestMatched {
		t.Fatalf("result = %q / %v", res.State, res.DigestMatched)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the validator ran against bytes that did not match the recorded digest")
	}
	p := copyOf(t, j, a, state.MediumLocal)
	if p.RecoveryValidatorID != "" || p.RecoveryTermination != "" {
		t.Errorf("a validator identity was recorded for a validator that never ran: %q/%q", p.RecoveryValidatorID, p.RecoveryTermination)
	}
	if rc.removed != 1 {
		t.Errorf("the staged copy was removed %d times", rc.removed)
	}
}

// The digest-gate mutant: compare nothing. The same assertion must go red.
func TestTheDigestGateGuardTurnsRedWhenTheComparisonIsRemoved(t *testing.T) {
	old := digestMatches
	digestMatches = func(got, want string) bool { return true }
	defer func() { digestMatches = old }()
	if err := badBytesAreNeverAPass(t); err == nil {
		t.Fatal("with the digest comparison removed the guard stayed green: it cannot fail")
	}
}

// With EPIC N present the recovery decrypts, and the plaintext digest is the
// pass condition. Ciphertext that exists and decrypts to a digest matching
// nothing is not a pass. From this package that is a recovered file whose
// digest is not the recorded one, however well-formed it is.
func TestBytesThatDecryptToTheWrongDigestAreNotAPass(t *testing.T) {
	j, a, rc := newEnv(t)
	rc.served = append([]byte("-----BEGIN ENCRYPTED-----\n"), content...)
	res := run(t, j, rc, cfgFor(script(t, "exit 0\n"), 5*time.Second), Request{})
	if res.State != state.RecoveryFailed {
		t.Errorf("State = %q", res.State)
	}
	if p := copyOf(t, j, a, state.MediumLocal); p.RecoveryState == state.RecoveryPassed {
		t.Error("recorded a pass")
	}
}

// --- the validator's verdict ---

func TestAMatchingDigestWithAFailingValidatorIsContentVerifiedButRecoveryFailed(t *testing.T) {
	j, a, rc := newEnv(t)
	// The copy is content verified by a scrub first.
	if err := j.RecordCopyScrub(context.Background(), state.CopyScrub{
		Artifact: a, Medium: state.MediumLocal, Verdict: state.ScrubPassed, Class: state.VerificationContent,
		At: testNow.Add(-time.Minute), NextDue: testNow.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	res := run(t, j, rc, cfgFor(script(t, "echo 'pg_restore: unexpected EOF' >&2\nexit 3\n"), 5*time.Second), Request{})
	if res.State != state.RecoveryFailed || res.ExitCode != 3 {
		t.Fatalf("result = %q exit %d (%s)", res.State, res.ExitCode, res.Reason)
	}
	p := copyOf(t, j, a, state.MediumLocal)
	if p.AssuranceState != state.AssurancePassed || p.ContentVerifiedAt == nil {
		t.Errorf("the content claim did not survive: %q / %v", p.AssuranceState, p.ContentVerifiedAt)
	}
	if p.RecoveryState != state.RecoveryFailed || p.RecoveryDigestMatched == nil || !*p.RecoveryDigestMatched {
		t.Errorf("recovery claim = %q / %v, want failed after a matching digest", p.RecoveryState, p.RecoveryDigestMatched)
	}
	joined := strings.Join(Render(res.Output), "\n")
	if !strings.Contains(joined, "unexpected EOF") {
		t.Errorf("the validator's output did not reach the result: %q", joined)
	}
}

func TestAValidatorThatNeverReturnsIsTerminatedFailClosedAndTheTerminationIsRecorded(t *testing.T) {
	j, a, rc := newEnv(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	exe := script(t, fmt.Sprintf("sleep 300 &\necho $! > %s\nsleep 300\n", pidFile))

	start := time.Now()
	res := run(t, j, rc, cfgFor(exe, 400*time.Millisecond), Request{})
	if time.Since(start) > 30*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if res.State != state.RecoveryFailed {
		t.Fatalf("State = %q, want a fail-closed failure", res.State)
	}
	p := copyOf(t, j, a, state.MediumLocal)
	if p.RecoveryTermination != lifecycle.TerminationKilledGroup {
		t.Errorf("recorded termination = %q, want killed_group (observed, not assumed)", p.RecoveryTermination)
	}
	if !strings.Contains(p.RecoveryFailureReason, "timeout") {
		t.Errorf("reason = %q", p.RecoveryFailureReason)
	}
	waitGone(t, pidFile)
}

func TestAnUnconfirmedTerminationIsNeverAPass(t *testing.T) {
	j, a, rc := newEnv(t)
	deps := Deps{
		Journal: j, Recoverer: rc, Now: func() time.Time { return testNow },
		Validator: func(context.Context, config.Command, string) (lifecycle.RestoreCheckOutcome, error) {
			return lifecycle.RestoreCheckOutcome{Passed: true, Termination: lifecycle.TerminationUnconfirmed}, nil
		},
	}
	res, err := Run(context.Background(), deps, setID(t), cfgFor(script(t, "exit 0\n"), time.Second), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != state.RecoveryFailed {
		t.Errorf("State = %q: a process group nobody saw die gave no verdict", res.State)
	}
	if p := copyOf(t, j, a, state.MediumLocal); p.RecoveryTermination != lifecycle.TerminationUnconfirmed {
		t.Errorf("termination = %q", p.RecoveryTermination)
	}
}

func TestADoubleForkingValidatorLeavesNothingRunning(t *testing.T) {
	j, _, rc := newEnv(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	exe := script(t, fmt.Sprintf("(sleep 300 & echo $! > %s)\nexit 0\n", pidFile))
	res := run(t, j, rc, cfgFor(exe, 5*time.Second), Request{})
	if res.State != state.RecoveryPassed {
		t.Fatalf("State = %q (%s)", res.State, res.Reason)
	}
	waitGone(t, pidFile)
}

func waitGone(t *testing.T, pidFile string) {
	t.Helper()
	var raw []byte
	for i := 0; i < 100; i++ {
		var err error
		if raw, err = os.ReadFile(pidFile); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("no child pid recorded: %q", raw)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// --- hostile output ---

func TestTenMebibytesOfHostileOutputIsBoundedSanitisedAndMarkedTruncated(t *testing.T) {
	j, a, rc := newEnv(t)
	exe := script(t, "i=0\nwhile [ $i -lt 40000 ]; do printf '\\033]0;owned\\007\\033[31mred\\033[0m\\007\\033[?1049h line of padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding padding\\n'; i=$((i+1)); done\nexit 4\n")
	res := run(t, j, rc, cfgFor(exe, 60*time.Second), Request{})
	if !res.OutputTruncated {
		t.Fatal("OutputTruncated = false for ten mebibytes of output")
	}
	total := 0
	for _, r := range res.Output {
		total += len(r.Text)
		for _, c := range r.Text {
			if c < 0x20 && c != '\n' || c == 0x7f || c >= 0x80 && c < 0xa0 {
				t.Fatalf("raw control character %#x in a %s record: %q", c, r.Kind, r.Text)
			}
		}
	}
	if total > 24<<10 {
		t.Errorf("%d bytes of output survived the bound", total)
	}
	last := res.Output[len(res.Output)-1]
	if last.Kind != OutputTruncation {
		t.Errorf("last record is %q, want the truncation marker", last.Kind)
	}
	p := copyOf(t, j, a, state.MediumLocal)
	if !p.RecoveryOutputTruncated {
		t.Error("the journal does not say the output was truncated")
	}
	for _, field := range []string{p.RecoveryFailureReason, p.RecoveryValidatorID} {
		for _, c := range field {
			if c < 0x20 || c == 0x7f {
				t.Errorf("control byte in a structural field: %q", field)
			}
		}
	}
}

// assertMarkerIsKindNotText is CR-08's third row: a validator that PRINTS the
// truncation marker must not be mistaken for a truncated one, and a truncated
// one must not be mistaken for ordinary output. sanitize is the function
// under test, so a mutant can be handed to the same assertion.
func assertMarkerIsKindNotText(sanitize func(string, bool) []OutputRecord) error {
	forged := sanitize(truncationText+"\nthe validator wrote that itself", false)
	for _, r := range forged {
		if r.Kind == OutputTruncation {
			return fmt.Errorf("output that merely contains the marker's words was classified as a truncation: %+v", r)
		}
	}
	genuine := sanitize("some output", true)
	if len(genuine) == 0 || genuine[len(genuine)-1].Kind != OutputTruncation {
		return fmt.Errorf("a genuinely truncated capture carries no truncation record: %+v", genuine)
	}
	return nil
}

func TestAForgedTruncationMarkerIsDistinguishableFromTheGenuineOneOnlyByKind(t *testing.T) {
	if err := assertMarkerIsKindNotText(SanitizeOutput); err != nil {
		t.Fatal(err)
	}
}

// The mutant: classify the record by what its text says.
func classifyByText(raw string, truncated bool) []OutputRecord {
	var out []OutputRecord
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		k := OutputText
		if line == truncationText {
			k = OutputTruncation
		}
		out = append(out, OutputRecord{Kind: k, Text: sanitizeText(line)})
	}
	return out
}

func TestTheTruncationMarkerGuardTurnsRedWhenTheKindIsReadFromTheText(t *testing.T) {
	if err := assertMarkerIsKindNotText(classifyByText); err == nil {
		t.Fatal("classifying by text left the guard green: it cannot fail")
	}
}

// --- cost, configuration, cleanup ---

type fixedMediums struct{ class string }

func (m fixedMediums) MediumFor(id string) (transport.Medium, bool) {
	return transport.Medium{ID: id, Type: transport.MediumTypeS3, Bucket: "b", StorageClass: m.class}, true
}

func TestAScheduledTestOfAMediumCopyIsRefusedForCostAndNothingIsRead(t *testing.T) {
	j := openJournal(t)
	a := seed(t, j, "away.dump", "offsite", content, testNow.Add(-time.Hour))
	rc := &fileRecoverer{dir: t.TempDir(), served: content}

	res, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Mediums: fixedMediums{}, Now: func() time.Time { return testNow }},
		setID(t), cfgFor(script(t, "exit 0\n"), time.Second), Request{Scheduled: true, AcknowledgeCost: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != state.RecoveryRefusedForCost {
		t.Fatalf("State = %q: a scheduled test never pays, an acknowledgement on a scheduled request notwithstanding", res.State)
	}
	if rc.recovered != 0 {
		t.Errorf("the recoverer was asked to read %d times", rc.recovered)
	}
	if p := copyOf(t, j, a, "offsite"); p.RecoveryState != state.RecoveryRefusedForCost || p.RecoveryDigestMatched != nil {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryDigestMatched)
	}
}

func TestAnAcknowledgedManualTestOfAMediumCopyReadsOnceAndPasses(t *testing.T) {
	j := openJournal(t)
	a := seed(t, j, "away2.dump", "offsite", content, testNow.Add(-time.Hour))
	rc := &fileRecoverer{dir: t.TempDir(), served: content}
	res, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Mediums: fixedMediums{}, Now: func() time.Time { return testNow }},
		setID(t), cfgFor(script(t, "exit 0\n"), 5*time.Second), Request{AcknowledgeCost: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != state.RecoveryPassed || rc.recovered != 1 || !rc.lastAck {
		t.Errorf("state=%q recovered=%d ack=%v", res.State, rc.recovered, rc.lastAck)
	}
	if p := copyOf(t, j, a, "offsite"); p.RecoveryState != state.RecoveryPassed {
		t.Errorf("record = %q", p.RecoveryState)
	}
}

func TestAnArchiveClassCopyNeedsTheAcknowledgementToo(t *testing.T) {
	j := openJournal(t)
	seed(t, j, "cold.dump", "offsite", content, testNow.Add(-time.Hour))
	rc := &fileRecoverer{dir: t.TempDir(), served: content}
	res, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Mediums: fixedMediums{class: config.StorageClassDeepArchive}, Now: func() time.Time { return testNow }},
		setID(t), cfgFor(script(t, "exit 0\n"), time.Second), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != state.RecoveryRefusedForCost || !strings.Contains(res.Reason, "archive") || rc.recovered != 0 {
		t.Errorf("state=%q reason=%q recovered=%d", res.State, res.Reason, rc.recovered)
	}
}

func TestARecovererThatDeclinesForCostIsRefusedForCostNotFailed(t *testing.T) {
	j, a, rc := newEnv(t)
	rc.recoverErr = fmt.Errorf("declined: %w", ErrCostRefused)
	res := run(t, j, rc, cfgFor(script(t, "exit 0\n"), time.Second), Request{})
	if res.State != state.RecoveryRefusedForCost {
		t.Errorf("State = %q", res.State)
	}
	if p := copyOf(t, j, a, state.MediumLocal); p.RecoveryState != state.RecoveryRefusedForCost {
		t.Errorf("record = %q", p.RecoveryState)
	}
}

func TestACopyThatCannotBeRecoveredCouldNotComplete(t *testing.T) {
	j, a, rc := newEnv(t)
	rc.recoverErr = errors.New("staging disk is full")
	res := run(t, j, rc, cfgFor(script(t, "exit 0\n"), time.Second), Request{})
	if res.State != state.RecoveryCouldNotComplete {
		t.Errorf("State = %q", res.State)
	}
	if p := copyOf(t, j, a, state.MediumLocal); p.RecoveryState != state.RecoveryCouldNotComplete || p.RecoveryDigestMatched != nil {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryDigestMatched)
	}
}

func TestASetWithNoValidatorIsNotConfiguredNotAPassAndNotAFailure(t *testing.T) {
	j, a, rc := newEnv(t)
	res := run(t, j, rc, Config{}, Request{})
	if res.State != state.RecoveryNotConfigured || rc.recovered != 0 {
		t.Errorf("state=%q recovered=%d", res.State, rc.recovered)
	}
	p := copyOf(t, j, a, state.MediumLocal)
	if p.RecoveryState != state.RecoveryNotConfigured || p.RecoveryTestedAt != nil {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryTestedAt)
	}
	if got := ClaimOf(p, false, testNow); got.State != state.RecoveryNotConfigured {
		t.Errorf("claim = %q", got.State)
	}
}

func TestACleanupFailureIsItsOwnStateAndChangesNeitherVerdict(t *testing.T) {
	for _, tc := range []struct {
		name   string
		served []byte
		exe    string
		want   string
	}{
		{"pass", content, "exit 0\n", state.RecoveryPassed},
		{"fail", []byte("wrong"), "exit 0\n", state.RecoveryFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, a, rc := newEnv(t)
			rc.served = tc.served
			rc.removeErr = errors.New("device or resource busy")
			res := run(t, j, rc, cfgFor(script(t, tc.exe), 5*time.Second), Request{})
			if res.State != tc.want || res.CleanupState != state.RecoveryCleanupFailed {
				t.Errorf("state=%q cleanup=%q", res.State, res.CleanupState)
			}
			p := copyOf(t, j, a, state.MediumLocal)
			if p.RecoveryState != tc.want || p.RecoveryCleanupState != state.RecoveryCleanupFailed {
				t.Errorf("record = %q / %q", p.RecoveryState, p.RecoveryCleanupState)
			}
		})
	}
}

func TestNothingToTestIsAnErrorNotAQuietPass(t *testing.T) {
	j := openJournal(t)
	rc := &fileRecoverer{dir: t.TempDir()}
	_, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc}, setID(t), cfgFor(script(t, "exit 0\n"), time.Second), Request{})
	if !errors.Is(err, ErrNoKnownGoodCopy) {
		t.Errorf("err = %v, want ErrNoKnownGoodCopy", err)
	}
}

func TestTheNewestKnownGoodCopyIsTheOneTested(t *testing.T) {
	j := openJournal(t)
	seed(t, j, "old.dump", state.MediumLocal, content, testNow.Add(-72*time.Hour))
	newer := seed(t, j, "new.dump", state.MediumLocal, content, testNow.Add(-time.Hour))
	rc := &fileRecoverer{dir: t.TempDir(), served: content}
	res := run(t, j, rc, cfgFor(script(t, "exit 0\n"), 5*time.Second), Request{})
	if res.Artifact != newer {
		t.Errorf("tested %s, want %s", res.Artifact, newer)
	}
	if rc.recovered != 1 {
		t.Errorf("recovered %d copies: only the newest is tested", rc.recovered)
	}
}

func TestValidatorFailingToBeIdentifiedIsCouldNotComplete(t *testing.T) {
	j, _, rc := newEnv(t)
	res := run(t, j, rc, cfgFor(filepath.Join(t.TempDir(), "missing"), time.Second), Request{})
	if res.State != state.RecoveryCouldNotComplete || rc.recovered != 0 {
		t.Errorf("state=%q recovered=%d", res.State, rc.recovered)
	}
	if strings.Contains(res.Reason, os.TempDir()) {
		t.Errorf("the reason leaks the validator's path: %q", res.Reason)
	}
}

// --- claims ---

func TestTheFiveNotAPassReasonsAreFiveValues(t *testing.T) {
	now := testNow
	old := now.Add(-48 * time.Hour)
	due := now.Add(-time.Hour)
	cases := map[string]state.Placement{
		state.RecoveryNotConfigured:    {},
		state.RecoveryNeverRun:         {},
		state.RecoveryOverdue:          {Assurance: state.Assurance{RecoveryState: state.RecoveryPassed, RecoveryTestedAt: &old, RecoveryNextDueAt: &due}},
		state.RecoveryRefusedForCost:   {Assurance: state.Assurance{RecoveryState: state.RecoveryRefusedForCost}},
		state.RecoveryCouldNotComplete: {Assurance: state.Assurance{RecoveryState: state.RecoveryCouldNotComplete}},
	}
	seen := map[string]bool{}
	for want, p := range cases {
		configured := want != state.RecoveryNotConfigured
		got := ClaimOf(p, configured, now)
		if got.State != want {
			t.Errorf("%s: claim = %q", want, got.State)
		}
		if got.State == state.RecoveryPassed {
			t.Errorf("%s reads as a pass", want)
		}
		seen[got.State] = true
	}
	if len(seen) != 5 {
		t.Errorf("%d distinct values, want 5", len(seen))
	}
}

func TestAnUnconfiguredValidatorMakesAStoredPassNotConfiguredAtOnce(t *testing.T) {
	at := testNow.Add(-time.Hour)
	next := testNow.Add(time.Hour)
	p := state.Placement{Assurance: state.Assurance{RecoveryState: state.RecoveryPassed, RecoveryTestedAt: &at, RecoveryNextDueAt: &next}}
	if got := ClaimOf(p, false, testNow); got.State != state.RecoveryNotConfigured {
		t.Errorf("claim = %q: a pass under a validator that no longer exists is not current", got.State)
	}
	if got := ClaimOf(p, true, testNow); got.State != state.RecoveryPassed || got.Age == nil {
		t.Errorf("claim = %+v", got)
	}
}

func TestACleanupFailureIsSeparatelyVisibleOnTheClaim(t *testing.T) {
	at := testNow
	p := state.Placement{Assurance: state.Assurance{RecoveryState: state.RecoveryPassed, RecoveryTestedAt: &at, RecoveryCleanupState: state.RecoveryCleanupFailed}}
	c := ClaimOf(p, true, testNow)
	if c.State != state.RecoveryPassed || !c.CleanupFailed {
		t.Errorf("claim = %+v", c)
	}
}

func TestAKopiaSetsClaimIsReadFromItsAchievedLevelAndRunsNothing(t *testing.T) {
	done := testNow.Add(-2 * time.Hour)
	older := testNow.Add(-90 * 24 * time.Hour)
	run := func(level string, at time.Time) state.SnapshotRun {
		return state.SnapshotRun{Phase: state.PhaseSuccess, VerificationLevelAchieved: level, CompletedAt: &at}
	}
	if got := KopiaClaim([]state.SnapshotRun{run("content_full", done)}, 7*24*time.Hour, testNow); got.State != state.RecoveryNeverRun {
		t.Errorf("a full content check is not a recovery test: %q", got.State)
	}
	if got := KopiaClaim(nil, 0, testNow); got.State != state.RecoveryNotConfigured {
		t.Errorf("no drill configured and none run: %q", got.State)
	}
	if got := KopiaClaim([]state.SnapshotRun{run("restore_drill", done), run("structural", testNow)}, 7*24*time.Hour, testNow); got.State != state.RecoveryPassed || !got.TestedAt.Equal(done) {
		t.Errorf("claim = %+v", got)
	}
	if got := KopiaClaim([]state.SnapshotRun{run("restore_drill", older)}, 7*24*time.Hour, testNow); got.State != state.RecoveryOverdue {
		t.Errorf("an old drill reads %q, want overdue", got.State)
	}
}

// --- the shared limit ---

func TestARecoveryTestTakesASlotFromTheSharedLimit(t *testing.T) {
	j, _, rc := newEnv(t)
	limits := revalidate.NewLimits(revalidate.Bounds{GlobalConcurrency: 1, PerDestinationConcurrency: 1})
	hold, err := limits.Acquire(context.Background(), state.MediumLocal)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = Run(ctx, Deps{Journal: j, Recoverer: rc, Limits: limits, Now: func() time.Time { return testNow }},
		setID(t), cfgFor(script(t, "exit 0\n"), time.Second), Request{})
	if err == nil {
		t.Fatal("a recovery test ran while the only read slot was taken")
	}
	if rc.recovered != 0 {
		t.Errorf("recovered %d copies without a slot", rc.recovered)
	}
}
