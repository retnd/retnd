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
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
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

// fakeValidator stands in for the workflow machinery the application puts
// behind Validator. What it returns is scripted per test; what it was asked
// is recorded.
type fakeValidator struct {
	configured bool
	cerr       error
	outcome    ValidatorOutcome
	verr       error
	calls      []ValidatorInput
}

func (f *fakeValidator) Configured(context.Context, model.BackupSetID) (bool, error) {
	return f.configured, f.cerr
}

func (f *fakeValidator) Validate(_ context.Context, in ValidatorInput) (ValidatorOutcome, error) {
	f.calls = append(f.calls, in)
	return f.outcome, f.verr
}

func exitedWith(code int) ValidatorOutcome {
	return ValidatorOutcome{ID: "10-check.local.sh", Hash: strings.Repeat("ab", 32), Disposition: "exited", ExitCode: &code}
}

func accepting() *fakeValidator { return &fakeValidator{configured: true, outcome: exitedWith(0)} }

func cfgFor() Config { return Config{Cadence: 24 * time.Hour} }

var content = []byte("the bytes of a database dump")

func newEnv(t *testing.T) (*state.Journal, model.ArtifactID, *fileRecoverer) {
	t.Helper()
	j := openJournal(t)
	a := seed(t, j, "dump.sql", state.MediumLocal, content, testNow.Add(-time.Hour))
	return j, a, &fileRecoverer{dir: t.TempDir(), served: content}
}

func run(t *testing.T, j *state.Journal, rc *fileRecoverer, v Validator, req Request) Result {
	t.Helper()
	res, err := Run(context.Background(), Deps{
		Journal: j, Recoverer: rc, Validator: v, Now: func() time.Time { return testNow },
	}, setID(t), cfgFor(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// --- the happy path, and what it records ---

func TestAMatchingDigestAndAnAcceptingValidatorIsARecordedPass(t *testing.T) {
	j, a, rc := newEnv(t)
	v := accepting()

	res := run(t, j, rc, v, Request{})
	if res.State != state.RecoveryPassed {
		t.Fatalf("State = %q (%s)", res.State, res.Reason)
	}
	p := copyOf(t, j, a, state.MediumLocal)
	if p.RecoveryState != state.RecoveryPassed || p.RecoveryDigestMatched == nil || !*p.RecoveryDigestMatched {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryDigestMatched)
	}
	if p.RecoveryValidatorID != "10-check.local.sh" || p.RecoveryValidatorHash != strings.Repeat("ab", 32) {
		t.Errorf("validator = %q / %q: the identity is the script's name and the hash of the bytes that ran, reported by the plan", p.RecoveryValidatorID, p.RecoveryValidatorHash)
	}
	if p.RecoveryTermination != state.RecoveryTerminationExited || p.RecoveryCleanupState != state.RecoveryCleanupRemoved {
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

func TestAChangedValidatorIsADifferentRecordedIdentity(t *testing.T) {
	j, a, rc := newEnv(t)
	v := accepting()
	run(t, j, rc, v, Request{})
	first := copyOf(t, j, a, state.MediumLocal).RecoveryValidatorHash

	v.outcome.Hash = strings.Repeat("cd", 32)
	run(t, j, rc, v, Request{})
	if second := copyOf(t, j, a, state.MediumLocal).RecoveryValidatorHash; first == second {
		t.Error("an edited validator recorded the same hash: an old pass would be silently inherited by new code")
	}
}

func TestTheValidatorIsHandedTheRecoveredFileAndTheDigestThatPassedTheGate(t *testing.T) {
	j, a, rc := newEnv(t)
	v := accepting()
	run(t, j, rc, v, Request{})
	if len(v.calls) != 1 {
		t.Fatalf("validator calls = %d", len(v.calls))
	}
	in := v.calls[0]
	if in.Artifact != a || in.Digest != sha(content) || in.Size != int64(len(content)) || in.File == "" {
		t.Errorf("input = %+v", in)
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
	v := accepting() // exits 0 having read nothing

	res, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Validator: v, Now: func() time.Time { return testNow }},
		setID(t), cfgFor(), Request{})
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
	v := accepting()

	res := run(t, j, rc, v, Request{})
	if res.State != state.RecoveryFailed || res.DigestMatched == nil || *res.DigestMatched {
		t.Fatalf("result = %q / %v", res.State, res.DigestMatched)
	}
	if len(v.calls) != 0 {
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
	res := run(t, j, rc, accepting(), Request{})
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
	if err := j.RecordCopyScrub(context.Background(), state.CopyScrub{
		Artifact: a, Medium: state.MediumLocal, Verdict: state.ScrubPassed, Class: state.VerificationContent,
		At: testNow.Add(-time.Minute), NextDue: testNow.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	v := accepting()
	v.outcome = exitedWith(3)
	v.outcome.Output = []byte("pg_restore: unexpected EOF\n")

	res := run(t, j, rc, v, Request{})
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
	if !strings.Contains(strings.Join(Render(res.Output), "\n"), "unexpected EOF") {
		t.Errorf("the validator's output did not reach the result: %+v", res.Output)
	}
}

func TestAValidatorThatNeverReturnsIsFailClosedAndTheTerminationIsRecorded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		proved   string
		wantTerm string
	}{
		{"killed and proved gone", "confirmed", state.RecoveryTerminationKilledGroup},
		{"killed and not proved", "unconfirmed", state.RecoveryTerminationUnconfirmed},
		{"killed and nothing said", "", state.RecoveryTerminationUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, a, rc := newEnv(t)
			v := accepting()
			v.outcome = ValidatorOutcome{ID: "10-check.local.sh", Hash: strings.Repeat("ab", 32), Disposition: "timed_out", Termination: tc.proved}
			res := run(t, j, rc, v, Request{})
			if res.State != state.RecoveryFailed {
				t.Fatalf("State = %q, want a fail-closed failure", res.State)
			}
			p := copyOf(t, j, a, state.MediumLocal)
			if p.RecoveryTermination != tc.wantTerm {
				t.Errorf("recorded termination = %q, want %q: certainty is recorded, not assumed", p.RecoveryTermination, tc.wantTerm)
			}
			if !strings.Contains(p.RecoveryFailureReason, "timeout") {
				t.Errorf("reason = %q", p.RecoveryFailureReason)
			}
		})
	}
}

func TestAnUnconfirmedTerminationIsNeverAPass(t *testing.T) {
	j, a, rc := newEnv(t)
	v := accepting()
	v.outcome.Termination = "unconfirmed" // exit 0, but the executor could not confirm the process is gone
	res := run(t, j, rc, v, Request{})
	if res.State != state.RecoveryFailed {
		t.Errorf("State = %q: a process nobody saw die gave no verdict", res.State)
	}
	if p := copyOf(t, j, a, state.MediumLocal); p.RecoveryTermination != state.RecoveryTerminationUnconfirmed {
		t.Errorf("termination = %q", p.RecoveryTermination)
	}
}

func TestAValidatorTheRunnerRefusedOrLostIsCouldNotCompleteNotAFailureOfTheCopy(t *testing.T) {
	for _, d := range []string{"not_attempted", "transport_lost", "weird"} {
		t.Run(d, func(t *testing.T) {
			j, a, rc := newEnv(t)
			v := accepting()
			v.outcome = ValidatorOutcome{Disposition: d, Detail: "the runner socket went away"}
			res := run(t, j, rc, v, Request{})
			if res.State != state.RecoveryCouldNotComplete {
				t.Errorf("State = %q", res.State)
			}
			p := copyOf(t, j, a, state.MediumLocal)
			if p.RecoveryState != state.RecoveryCouldNotComplete || p.RecoveryValidatorID != "" {
				t.Errorf("record = %q / validator %q", p.RecoveryState, p.RecoveryValidatorID)
			}
		})
	}
}

func TestAnOutcomeWithNoIdentityCannotBeAttributedAndIsNotAPass(t *testing.T) {
	j, _, rc := newEnv(t)
	v := accepting()
	v.outcome.ID, v.outcome.Hash = "", ""
	if res := run(t, j, rc, v, Request{}); res.State != state.RecoveryCouldNotComplete {
		t.Errorf("State = %q", res.State)
	}
}

func TestAValidatorThatErrorsIsCouldNotComplete(t *testing.T) {
	j, _, rc := newEnv(t)
	v := accepting()
	v.verr = errors.New("the plan could not be built: a script is group writable")
	res := run(t, j, rc, v, Request{})
	if res.State != state.RecoveryCouldNotComplete || rc.removed != 1 {
		t.Errorf("State = %q, removed = %d", res.State, rc.removed)
	}
}

func TestAValidatorWhoseDirectoryCannotBeExaminedIsCouldNotCompleteAndNothingIsRecovered(t *testing.T) {
	j, _, rc := newEnv(t)
	v := accepting()
	v.cerr = errors.New("the stage directory is a symbolic link")
	res := run(t, j, rc, v, Request{})
	if res.State != state.RecoveryCouldNotComplete || rc.recovered != 0 {
		t.Errorf("State = %q, recovered = %d", res.State, rc.recovered)
	}
}

// --- hostile output ---

func TestHostileOutputLeavesAsSanitisedRecordsWithTheTruncationFlagged(t *testing.T) {
	j, a, rc := newEnv(t)
	hostile := strings.Repeat("\x1b]0;owned\x07\x1b[31mred\x1b[0m\x07\x1b[?1049h a line of padding\n", 400)
	v := accepting()
	v.outcome = exitedWith(4)
	v.outcome.Output = []byte(hostile)
	v.outcome.Truncated = true

	res := run(t, j, rc, v, Request{})
	if !res.OutputTruncated {
		t.Fatal("OutputTruncated = false")
	}
	for _, r := range res.Output {
		for _, c := range r.Text {
			if c < 0x20 && c != '\n' || c == 0x7f || c >= 0x80 && c < 0xa0 {
				t.Fatalf("raw control character %#x in a %s record: %q", c, r.Kind, r.Text)
			}
		}
	}
	if last := res.Output[len(res.Output)-1]; last.Kind != OutputTruncation {
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

	res, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Mediums: fixedMediums{}, Validator: accepting(), Now: func() time.Time { return testNow }},
		setID(t), cfgFor(), Request{Scheduled: true, AcknowledgeCost: true})
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
	res, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Mediums: fixedMediums{}, Validator: accepting(), Now: func() time.Time { return testNow }},
		setID(t), cfgFor(), Request{AcknowledgeCost: true})
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
	res, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Mediums: fixedMediums{class: config.StorageClassDeepArchive}, Validator: accepting(), Now: func() time.Time { return testNow }},
		setID(t), cfgFor(), Request{})
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
	res := run(t, j, rc, accepting(), Request{})
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
	res := run(t, j, rc, accepting(), Request{})
	if res.State != state.RecoveryCouldNotComplete {
		t.Errorf("State = %q", res.State)
	}
	if p := copyOf(t, j, a, state.MediumLocal); p.RecoveryState != state.RecoveryCouldNotComplete || p.RecoveryDigestMatched != nil {
		t.Errorf("record = %q / %v", p.RecoveryState, p.RecoveryDigestMatched)
	}
}

func TestASetWithNoValidatorIsNotConfiguredNotAPassAndNotAFailure(t *testing.T) {
	for name, v := range map[string]Validator{"none declared": nil, "directory absent": &fakeValidator{configured: false}} {
		t.Run(name, func(t *testing.T) {
			j, a, rc := newEnv(t)
			res := run(t, j, rc, v, Request{})
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
		})
	}
}

func TestACleanupFailureIsItsOwnStateAndChangesNeitherVerdict(t *testing.T) {
	for _, tc := range []struct {
		name   string
		served []byte
		want   string
	}{
		{"pass", content, state.RecoveryPassed},
		{"fail", []byte("wrong"), state.RecoveryFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, a, rc := newEnv(t)
			rc.served = tc.served
			rc.removeErr = errors.New("device or resource busy")
			res := run(t, j, rc, accepting(), Request{})
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
	_, err := Run(context.Background(), Deps{Journal: j, Recoverer: rc, Validator: accepting()}, setID(t), cfgFor(), Request{})
	if !errors.Is(err, ErrNoKnownGoodCopy) {
		t.Errorf("err = %v, want ErrNoKnownGoodCopy", err)
	}
}

func TestTheNewestKnownGoodCopyIsTheOneTested(t *testing.T) {
	j := openJournal(t)
	seed(t, j, "old.dump", state.MediumLocal, content, testNow.Add(-72*time.Hour))
	newer := seed(t, j, "new.dump", state.MediumLocal, content, testNow.Add(-time.Hour))
	rc := &fileRecoverer{dir: t.TempDir(), served: content}
	res := run(t, j, rc, accepting(), Request{})
	if res.Artifact != newer {
		t.Errorf("tested %s, want %s", res.Artifact, newer)
	}
	if rc.recovered != 1 {
		t.Errorf("recovered %d copies: only the newest is tested", rc.recovered)
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
	_, err = Run(ctx, Deps{Journal: j, Recoverer: rc, Limits: limits, Validator: accepting(), Now: func() time.Time { return testNow }},
		setID(t), cfgFor(), Request{})
	if err == nil {
		t.Fatal("a recovery test ran while the only read slot was taken")
	}
	if rc.recovered != 0 {
		t.Errorf("recovered %d copies without a slot", rc.recovered)
	}
}
