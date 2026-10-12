package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #980 and #1071: the newest recovery test's output is kept per set,
// as the runner's sanitised, bounded records, and nothing else is.

func TestRecoveryTest_PersistsTheValidatorsSanitisedBoundedOutputForTheSet(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	stages.outcome.Output = []byte("checking \x1b[31mrestore\x1b[0m\n" + strings.Repeat("noise \x07\n", 5000))
	stages.outcome.OutputTruncated = true
	svc, journal, bs := recoveryService(t, stages)
	seedMovableArtifact(t, ctx, journal, bs, "out.dump", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID, OperationID: "op-out-1"}); err != nil {
		t.Fatalf("RecoveryTest: %v", err)
	}

	got, ok, err := journal.RecoveryOutputOf(ctx, bs.ID)
	if err != nil || !ok {
		t.Fatalf("RecoveryOutputOf: ok=%v err=%v: the test's output was not kept", ok, err)
	}
	if got.OperationID != "op-out-1" || got.State != state.RecoveryPassed || got.Medium != state.MediumLocal || !strings.HasSuffix(got.Artifact, "/out.dump") {
		t.Errorf("header = %+v", got)
	}
	if !got.Truncated {
		t.Error("Truncated = false for a capture that was cut")
	}
	if len(got.Records) == 0 || got.Records[0].Text != "checking �[31mrestore�[0m" {
		t.Fatalf("first record = %+v, want the line with its escape bytes made inert", got.Records)
	}
	if len(got.Records) > state.MaxRecoveryOutputRecords {
		t.Errorf("%d records stored, the bound is %d", len(got.Records), state.MaxRecoveryOutputRecords)
	}
	total, truncations := 0, 0
	for _, r := range got.Records {
		total += len(r.Text)
		for _, c := range r.Text {
			if c < 0x20 || c == 0x7f || c >= 0x80 && c < 0xa0 {
				t.Fatalf("raw control byte %#x stored in %q", c, r.Text)
			}
		}
		if r.Kind == state.RecoveryOutputKindTruncation {
			truncations++
		}
	}
	if total > state.MaxRecoveryOutputBytes {
		t.Errorf("%d bytes stored, the bound is %d", total, state.MaxRecoveryOutputBytes)
	}
	if truncations != 1 || got.Records[len(got.Records)-1].Kind != state.RecoveryOutputKindTruncation {
		t.Errorf("%d truncation records, last is %q: want exactly one, last, written by the runner", truncations, got.Records[len(got.Records)-1].Kind)
	}
}

func TestRecoveryTest_AQuotedTruncationMarkerIsStoredAsOutputAndNotAsATruncation(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	stages.outcome.Output = []byte(state.RecoveryOutputTruncationText + "\n")
	svc, journal, bs := recoveryService(t, stages)
	seedMovableArtifact(t, ctx, journal, bs, "quote.dump", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := journal.RecoveryOutputOf(ctx, bs.ID)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Truncated || len(got.Records) != 1 || got.Records[0].Kind != state.RecoveryOutputKindText {
		t.Errorf("got %+v: only the runner may write a truncation record", got)
	}
}

func TestRecoveryTest_ANewerTestThatNeverReachedTheValidatorDoesNotLeaveTheOldOutputStanding(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	stages.outcome.Output = []byte("the first run's line\n")
	svc, journal, bs := recoveryService(t, stages)
	seedMovableArtifact(t, ctx, journal, bs, "again.dump", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID, OperationID: "op-1"}); err != nil {
		t.Fatal(err)
	}
	// The copy rots, so the second test fails at the digest gate and the
	// validator never runs.
	if err := os.WriteFile(filepath.Join(bs.LocalPath, "again.dump"), []byte("rotted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID, OperationID: "op-2"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := journal.RecoveryOutputOf(ctx, bs.ID)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.OperationID != "op-2" || got.State != state.RecoveryFailed || len(got.Records) != 0 {
		t.Errorf("got %+v: the newest test printed nothing, and the first run's line must not be shown as its output", got)
	}
}
