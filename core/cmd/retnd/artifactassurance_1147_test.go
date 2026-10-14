package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// #1147: what a scrub and a recovery test recorded on a copy has to reach a
// terminal. The API and the browser already show it; `artifacts <id>` printed
// none of it, `artifacts` had no --json, and there was no verb that lists the
// backups held in quarantine.
//
// Every cell drives the real binary entry point (run) over a real journal.
// The scrub cells corrupt a real file and let `scrub` find it, so the failure
// being read back is one the product wrote itself.

var yes = true

const assuranceID = "production/postgres-primary/backup.dump"

// scrubbedRottedCopy returns a deployment whose only copy has been found
// corrupt by a real scrub.
func scrubbedRottedCopy(t *testing.T) string {
	t.Helper()
	configPath, localFinal := oneCommittedArtifact(t)
	writeFileT(t, localFinal, "tampered bytes that do not match the recorded hash")
	if code := run([]string{"scrub", "--config", configPath, scrubSet}); code != 1 {
		t.Fatalf("scrub of a rotted copy = %d, want 1 (a copy failed)", code)
	}
	return configPath
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// recordRecovery writes a recovery-test verdict onto the one local copy of
// the committed artifact, straight into the journal.
func recordRecovery(t *testing.T, configPath string, rt state.RecoveryTest) {
	t.Helper()
	ctx := context.Background()
	j, err := state.Open(ctx, filepath.Join(filepath.Dir(configPath), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer func() { _ = j.Close() }()
	set, err := model.NewBackupSetID("production", "postgres-primary")
	if err != nil {
		t.Fatal(err)
	}
	id, err := model.NewArtifactID(set, "backup.dump")
	if err != nil {
		t.Fatal(err)
	}
	rt.Copy = state.CopyRef{Artifact: id, Medium: state.MediumLocal}
	if err := j.RecordRecoveryTest(ctx, rt); err != nil {
		t.Fatalf("RecordRecoveryTest: %v", err)
	}
}

func artifactsOut(t *testing.T, want int, args ...string) string {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = run(args) })
	if code != want {
		t.Fatalf("run(%v) = %d, want %d\nstdout: %s", args, code, want, out)
	}
	return out
}

func strictDecode(t *testing.T, raw string, into any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("not the contract's own type: %v\n%s", err, raw)
	}
	if dec.More() {
		t.Fatalf("more than one JSON value on stdout:\n%s", raw)
	}
}

// TestIssue1147_ArtifactDetailPrintsAFailedScrubOnTheCopy is P0-2's headline:
// a copy a scheduled scrub found corrupt says so, with the reason and the
// streak, in the detail an operator opens to ask "is this backup good".
func TestIssue1147_ArtifactDetailPrintsAFailedScrubOnTheCopy(t *testing.T) {
	configPath := scrubbedRottedCopy(t)
	out := artifactsOut(t, 0, "artifacts", "--config", configPath, assuranceID)
	for _, want := range []string{
		"assurance_state:",
		"failed",
		"last_checked_at:",
		"last_failure_at:",
		"last_failure_reason:",
		"consecutive_failures:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the detail lacks %q:\n%s", want, out)
		}
	}
	// The streak is a number, not just a label.
	if !regexpFind(`consecutive_failures:\s+1\b`, out) {
		t.Errorf("consecutive_failures is not 1:\n%s", out)
	}
}

// TestIssue1147_ACopyNobodyHasCheckedStillPrintsNoCopyBlock is the control for
// the cell above and FR-35's promise: the unknown state is the absence of
// lines, so a plain local artifact reads exactly as it did before.
func TestIssue1147_ACopyNobodyHasCheckedStillPrintsNoCopyBlock(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	out := artifactsOut(t, 0, "artifacts", "--config", configPath, assuranceID)
	for _, banned := range []string{"copy:", "assurance_state", "recovery_state", "last_failure"} {
		if strings.Contains(out, banned) {
			t.Errorf("an unchecked local copy printed %q:\n%s", banned, out)
		}
	}
}

// TestIssue1147_ARecoveryTestPassShowsAsReadableWithItsCheck pins the wire
// spelling: a built-in structure pass is stored as "passed" under the
// reserved validator id and is served as "readable". A terminal that printed
// "passed" would tell an operator a validator accepted a copy it only
// structure-checked.
func TestIssue1147_ARecoveryTestPassShowsAsReadableWithItsCheck(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	now := time.Now().UTC().Truncate(time.Second)
	recordRecovery(t, configPath, state.RecoveryTest{
		State: state.RecoveryPassed, At: now, NextDue: now.Add(24 * time.Hour),
		ValidatorID: "retnd-builtin-structure", ValidatorHash: strings.Repeat("a", 64),
		DigestMatched: &yes, Termination: state.RecoveryTerminationExited, CleanupState: "removed",
	})
	out := artifactsOut(t, 0, "artifacts", "--config", configPath, assuranceID)
	for _, want := range []string{"recovery_state:", "readable", "recovery_tested_at:", "recovery_check:", "builtin"} {
		if !strings.Contains(out, want) {
			t.Errorf("the detail lacks %q:\n%s", want, out)
		}
	}
	if regexpFind(`recovery_state:\s+passed`, out) {
		t.Errorf("a built-in pass was printed as passed:\n%s", out)
	}
}

// TestIssue1147_AFailedRecoveryTestPrintsItsReasonAndValidator is the failing
// half: the reason is the thing an operator acts on.
func TestIssue1147_AFailedRecoveryTestPrintsItsReasonAndValidator(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	now := time.Now().UTC().Truncate(time.Second)
	recordRecovery(t, configPath, state.RecoveryTest{
		State: state.RecoveryFailed, At: now, NextDue: now.Add(24 * time.Hour),
		Reason: "the restored dump would not load: unexpected EOF", ValidatorID: "10-load.local.sh", ValidatorHash: strings.Repeat("b", 64),
		DigestMatched: &yes, Termination: state.RecoveryTerminationExited, CleanupState: "removed",
	})
	out := artifactsOut(t, 0, "artifacts", "--config", configPath, assuranceID)
	for _, want := range []string{"recovery_state:", "failed", "recovery_failure_reason:", "unexpected EOF", "recovery_check:", "validator"} {
		if !strings.Contains(out, want) {
			t.Errorf("the detail lacks %q:\n%s", want, out)
		}
	}
}

// TestIssue1147_ArtifactDetailJSONIsTheArtifactWireObject: strict decode into
// the contract type, so a field the contract does not name (or names with
// another type) is a --json that has drifted from the API.
func TestIssue1147_ArtifactDetailJSONIsTheArtifactWireObject(t *testing.T) {
	configPath := scrubbedRottedCopy(t)
	now := time.Now().UTC().Truncate(time.Second)
	recordRecovery(t, configPath, state.RecoveryTest{
		State: state.RecoveryFailed, At: now, NextDue: now.Add(time.Hour),
		Reason: "boom", ValidatorID: "10-load.local.sh", ValidatorHash: strings.Repeat("b", 64),
		DigestMatched: &yes, Termination: state.RecoveryTerminationExited, CleanupState: "removed",
	})
	out := artifactsOut(t, 0, "artifacts", "--json", "--config", configPath, assuranceID)

	var got apicontract.Artifact
	strictDecode(t, out, &got)
	if got.ID != assuranceID || got.BackupSetID != scrubSet || got.State == "" {
		t.Errorf("identity fields wrong: %+v", got)
	}
	if got.RetentionPolicy == "" || got.Validation == "" {
		t.Errorf("required fields missing (retention_policy %q, validation %q)", got.RetentionPolicy, got.Validation)
	}
	if len(got.Placements) != 1 {
		t.Fatalf("placements = %+v, want the one local copy", got.Placements)
	}
	p := got.Placements[0]
	if p.AssuranceState != "failed" || p.LastFailureReason == "" || p.ConsecutiveFailures != 1 ||
		p.LastCheckedAt == "" || p.LastFailureAt == "" {
		t.Errorf("scrub record not on the wire placement: %+v", p)
	}
	if p.RecoveryState != "failed" || p.RecoveryFailureReason != "boom" || p.RecoveryTestedAt == "" || p.RecoveryCheck != "validator" {
		t.Errorf("recovery record not on the wire placement: %+v", p)
	}
	if strings.Contains(out, "assurance_state:") {
		t.Errorf("the human lines leaked into --json:\n%s", out)
	}
}

// TestIssue1147_ArtifactListJSONIsListArtifactsResponse, including the empty
// list being [] and not null.
func TestIssue1147_ArtifactListJSONIsListArtifactsResponse(t *testing.T) {
	configPath := scrubbedRottedCopy(t)
	out := artifactsOut(t, 0, "artifacts", "--json", "--config", configPath)
	var got apicontract.ListArtifactsResponse
	strictDecode(t, out, &got)
	if len(got.Artifacts) != 1 || got.Artifacts[0].ID != assuranceID {
		t.Fatalf("artifacts = %+v, want the one backup", got.Artifacts)
	}
	if len(got.Artifacts[0].Placements) != 1 || got.Artifacts[0].Placements[0].AssuranceState != "failed" {
		t.Errorf("the list lost the per-copy assurance: %+v", got.Artifacts[0].Placements)
	}

	// A narrowing that matches nothing is an empty array, never null.
	out = artifactsOut(t, 0, "artifacts", "--json", "--config", configPath, "--state", "FAILED")
	if !strings.Contains(out, `"artifacts": []`) {
		t.Errorf("an empty list must be [] and not null:\n%s", out)
	}
	// The scrub found the sole copy rotten, so the backup is held as lost.
	out = artifactsOut(t, 0, "artifacts", "--json", "--config", configPath, "--state", "QUARANTINED_LOST")
	strictDecode(t, out, &got)
	if len(got.Artifacts) != 1 {
		t.Errorf("--state QUARANTINED_LOST dropped the backup the scrub quarantined: %s", out)
	}
}

// TestIssue1147_ArtifactsStateNarrowsTheListAndRefusesANonState covers the
// optional --state, with a positive control so a flag that filtered
// everything away would not pass.
func TestIssue1147_ArtifactsStateNarrowsTheListAndRefusesANonState(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	var committed string
	for _, st := range []string{"COMMITTED", "COMPLETE"} {
		if out := artifactsOut(t, 0, "artifacts", "--config", configPath, "--state", st); strings.Contains(out, assuranceID) {
			committed = st
		}
	}
	if committed == "" {
		t.Fatalf("--state never matched the one backup in either of its settled states")
	}
	if out := artifactsOut(t, 0, "artifacts", "--config", configPath, "--state", "QUARANTINED"); strings.Contains(out, assuranceID) || !strings.Contains(out, "0 artifact(s)") {
		t.Errorf("--state QUARANTINED listed a healthy backup:\n%s", out)
	}
	checkExitAndStderr(t, []string{"artifacts", "--config", configPath, "--state", "NOT_A_STATE"}, 2, "--state")
}

// ---------------------------------------------------------------- quarantine

func stagedQuarantineDeployment(t *testing.T) (configPath string, held, healthy model.ArtifactID) {
	t.Helper()
	configPath = writeQuarantineTestConfig(t)
	held = stageQuarantinedArtifact(t, configPath, "held.dump", false)
	healthy = stageQuarantinedArtifact(t, configPath, "healed.dump", false)
	if code := run([]string{"quarantine", "reinstate", "--config", configPath, healthy.String()}); code != 0 {
		t.Fatalf("reinstate of an intact copy = %d, want 0 (this puts one backup back in service)", code)
	}
	return configPath, held, healthy
}

// TestIssue1147_QuarantineListPrintsTheHeldBackupsAndOnlyThose is P1-5.
func TestIssue1147_QuarantineListPrintsTheHeldBackupsAndOnlyThose(t *testing.T) {
	configPath, held, healthy := stagedQuarantineDeployment(t)
	out := artifactsOut(t, 0, "quarantine", "list", "--config", configPath)
	if !strings.Contains(out, held.String()) {
		t.Errorf("the held backup is not listed:\n%s", out)
	}
	if strings.Contains(out, healthy.String()) {
		t.Errorf("a reinstated backup is listed as held:\n%s", out)
	}
	for _, want := range []string{"test fixture", "irrecoverable", "updated"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing lacks %q (id, reason, irrecoverable, updated_at):\n%s", want, out)
		}
	}
}

func TestIssue1147_QuarantineListJSONIsListArtifactsResponse(t *testing.T) {
	configPath, held, healthy := stagedQuarantineDeployment(t)
	out := artifactsOut(t, 0, "quarantine", "list", "--json", "--config", configPath)
	var got apicontract.ListArtifactsResponse
	strictDecode(t, out, &got)
	if len(got.Artifacts) != 1 || got.Artifacts[0].ID != held.String() {
		t.Fatalf("artifacts = %+v, want only %s (not %s)", got.Artifacts, held, healthy)
	}
	a := got.Artifacts[0]
	if !a.Quarantined || a.QuarantineIrrecoverable || a.QuarantineReason != "test fixture" {
		t.Errorf("quarantine fields wrong: quarantined=%v irrecoverable=%v reason=%q", a.Quarantined, a.QuarantineIrrecoverable, a.QuarantineReason)
	}
}

func TestIssue1147_QuarantineListNarrowsToABackupSetAndRefusesAnUnknownOne(t *testing.T) {
	configPath, held, _ := stagedQuarantineDeployment(t)
	if out := artifactsOut(t, 0, "quarantine", "list", "--config", configPath, "--backup-set", "production/postgres-primary"); !strings.Contains(out, held.String()) {
		t.Errorf("the set's own held backup is missing:\n%s", out)
	}
	// An unknown set is a refusal and not an empty list: an empty list has to
	// keep meaning "nothing is held".
	var code int
	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			code = run([]string{"quarantine", "list", "--config", configPath, "--backup-set", "production/nope"})
		})
	})
	if code == 0 || !strings.Contains(stderr, "production/nope") {
		t.Errorf("an unknown backup set exited %d, stderr %q; want a refusal naming it", code, stderr)
	}
}

func TestIssue1147_QuarantineListIsUsageCheckedAndJSONIsListOnly(t *testing.T) {
	configPath := writeQuarantineTestConfig(t)
	checkExitAndStderr(t, []string{"quarantine", "list", "extra", "--config", configPath}, 2, "quarantine")
	// --json and --backup-set mean nothing to the three actions.
	id := stageQuarantinedArtifact(t, configPath, "x.dump", false).String()
	checkExitAndStderr(t, []string{"quarantine", "revalidate", id, "--json", "--config", configPath}, 2, "--json")
	// An empty hold is a clean, empty array.
	empty := writeQuarantineTestConfig(t)
	out := artifactsOut(t, 0, "quarantine", "list", "--json", "--config", empty)
	if !strings.Contains(out, `"artifacts": []`) {
		t.Errorf("nothing held must print [] and not null:\n%s", out)
	}
}

func regexpFind(pattern, s string) bool { return regexp.MustCompile(pattern).MatchString(s) }
