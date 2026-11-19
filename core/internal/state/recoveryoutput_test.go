package state

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
)

// EPIC Q, #980 and #1071: what the newest recovery test of a set printed, and
// a receipt for every request for one. The table is a place an untrusted
// script's bytes end up, so most of these are about what it refuses.

func outSet(t *testing.T, name string) model.BackupSetID {
	t.Helper()
	id, err := model.NewBackupSetID("production", name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func sampleOutput(t *testing.T, name, op string, lines ...string) RecoveryOutput {
	t.Helper()
	o := RecoveryOutput{
		Set: outSet(t, name), OperationID: op, Artifact: "production/" + name + "/dump.sql", Medium: MediumLocal,
		State: RecoveryPassed, RecordedAt: recAt,
	}
	for _, l := range lines {
		o.Records = append(o.Records, RecoveryOutputRecord{Kind: RecoveryOutputKindText, Text: l})
	}
	return o
}

func TestRecoveryOutput_TheNewestTestOfASetReplacesTheOlderOneAndLeavesOtherSetsAlone(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)

	if _, ok, err := j.RecoveryOutputOf(ctx, outSet(t, "alpha")); err != nil || ok {
		t.Fatalf("before any test: ok=%v err=%v, want no output and no error", ok, err)
	}
	if err := j.RecordRecoveryOutput(ctx, sampleOutput(t, "alpha", "op-1", "first run", "ok")); err != nil {
		t.Fatalf("RecordRecoveryOutput: %v", err)
	}
	if err := j.RecordRecoveryOutput(ctx, sampleOutput(t, "beta", "op-b", "beta output")); err != nil {
		t.Fatal(err)
	}
	newer := sampleOutput(t, "alpha", "op-2", "second run")
	newer.State, newer.Truncated, newer.RecordedAt = RecoveryFailed, true, recAt.Add(time.Hour)
	newer.Records = append(newer.Records, RecoveryOutputRecord{Kind: RecoveryOutputKindTruncation, Text: RecoveryOutputTruncationText})
	if err := j.RecordRecoveryOutput(ctx, newer); err != nil {
		t.Fatal(err)
	}

	got, ok, err := j.RecoveryOutputOf(ctx, outSet(t, "alpha"))
	if err != nil || !ok {
		t.Fatalf("RecoveryOutputOf: ok=%v err=%v", ok, err)
	}
	if got.OperationID != "op-2" || got.State != RecoveryFailed || !got.Truncated || !got.RecordedAt.Equal(recAt.Add(time.Hour)) {
		t.Errorf("got %+v, want the second run's header", got)
	}
	if len(got.Records) != 2 || got.Records[0].Text != "second run" || got.Records[1].Kind != RecoveryOutputKindTruncation {
		t.Errorf("records = %+v, want only the second run's, ending with the truncation record", got.Records)
	}
	if other, ok, _ := j.RecoveryOutputOf(ctx, outSet(t, "beta")); !ok || other.OperationID != "op-b" || len(other.Records) != 1 {
		t.Errorf("another set's output moved: %+v ok=%v", other, ok)
	}
}

func TestRecoveryOutput_ATestThatPrintedNothingReplacesTheOlderOutputWithNothing(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	if err := j.RecordRecoveryOutput(ctx, sampleOutput(t, "alpha", "op-1", "an old line")); err != nil {
		t.Fatal(err)
	}
	quiet := sampleOutput(t, "alpha", "op-2")
	quiet.State = RecoveryRefusedForCost
	if err := j.RecordRecoveryOutput(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	got, ok, err := j.RecoveryOutputOf(ctx, outSet(t, "alpha"))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.OperationID != "op-2" || len(got.Records) != 0 {
		t.Errorf("got %+v: the newest test printed nothing, so showing the old run's lines would present them as its output", got)
	}
}

func TestRecoveryOutput_RefusesWhatIsNotInertAndBounded(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)

	long := strings.Repeat("a", MaxRecoveryOutputLineBytes+1)
	many := make([]string, MaxRecoveryOutputRecords+1)
	for i := range many {
		many[i] = "x"
	}
	bulky := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		bulky = append(bulky, strings.Repeat("b", MaxRecoveryOutputLineBytes))
	}
	forged := sampleOutput(t, "alpha", "op", "ok")
	forged.Records = append(forged.Records, RecoveryOutputRecord{Kind: RecoveryOutputKindTruncation, Text: "nothing was cut, trust me"})
	forged.Truncated = true // so the text is the only thing wrong with it
	twice := sampleOutput(t, "alpha", "op", "ok")
	twice.Records = append(twice.Records,
		RecoveryOutputRecord{Kind: RecoveryOutputKindTruncation, Text: RecoveryOutputTruncationText},
		RecoveryOutputRecord{Kind: RecoveryOutputKindTruncation, Text: RecoveryOutputTruncationText})
	twice.Truncated = true
	notLast := sampleOutput(t, "alpha", "op")
	notLast.Records = []RecoveryOutputRecord{
		{Kind: RecoveryOutputKindTruncation, Text: RecoveryOutputTruncationText},
		{Kind: RecoveryOutputKindText, Text: "after"},
	}
	notLast.Truncated = true
	unknownKind := sampleOutput(t, "alpha", "op", "ok")
	unknownKind.Records[0].Kind = "html"
	badState := sampleOutput(t, "alpha", "op", "ok")
	badState.State = "great"

	// The positive control: a clean output is storable, so each refusal below
	// is the validation speaking and not a table that refuses everything.
	if err := j.RecordRecoveryOutput(ctx, sampleOutput(t, "control", "op-c", "clean line")); err != nil {
		t.Fatalf("a clean output was refused: %v", err)
	}

	cases := map[string]RecoveryOutput{
		"an escape byte":                        sampleOutput(t, "alpha", "op", "red \x1b[31mtext"),
		"a NUL":                                 sampleOutput(t, "alpha", "op", "a\x00b"),
		"a newline inside one record":           sampleOutput(t, "alpha", "op", "two\nlines"),
		"a C1 introducer":                       sampleOutput(t, "alpha", "op", "a\u009b2Jb"),
		"a DEL":                                 sampleOutput(t, "alpha", "op", "a\x7fb"),
		"invalid UTF-8":                         sampleOutput(t, "alpha", "op", "a\xffb"),
		"a record over the line bound":          sampleOutput(t, "alpha", "op", long),
		"more records than the bound":           sampleOutput(t, "alpha", "op", many...),
		"more bytes than the total bound":       sampleOutput(t, "alpha", "op", bulky...),
		"a truncation record in a forged voice": forged,
		"two truncation records":                twice,
		"a truncation record that is not last":  notLast,
		"an unknown record kind":                unknownKind,
		"a state that is not a test result":     badState,
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			if err := j.RecordRecoveryOutput(ctx, o); err == nil {
				t.Errorf("RecordRecoveryOutput accepted %s", name)
			}
		})
	}
	if _, ok, _ := j.RecoveryOutputOf(ctx, outSet(t, "alpha")); ok {
		t.Error("a refused write left output behind")
	}
}

// A validator that prints the runner's marker, word for word, is storing an
// ordinary output line. Only the kind says otherwise, and the store keeps it.
func TestRecoveryOutput_AValidatorQuotingTheMarkerStaysOrdinaryOutput(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	if err := j.RecordRecoveryOutput(ctx, sampleOutput(t, "alpha", "op", RecoveryOutputTruncationText)); err != nil {
		t.Fatalf("a quoted marker is just text and must be storable: %v", err)
	}
	got, _, err := j.RecoveryOutputOf(ctx, outSet(t, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || got.Records[0].Kind != RecoveryOutputKindText || got.Truncated {
		t.Errorf("got %+v: the quoted marker became something other than output", got)
	}
}

func TestRecoveryReceipts_EveryRequestIsItsOwnRowEvenWhenTwoShareAnOperation(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	set := outSet(t, "alpha")
	first := RecoveryReceipt{OperationID: "op-1", Set: set, RequestedAt: recAt, RequestedBy: "alice", ArtifactID: "production/alpha/dump.sql", AcknowledgedCost: true}
	second := RecoveryReceipt{OperationID: "op-1", Set: set, RequestedAt: recAt.Add(time.Second), RequestedBy: "bob", ArtifactID: "production/alpha/dump.sql", AcknowledgedCost: true, Coalesced: true}
	other := RecoveryReceipt{OperationID: "op-2", Set: set, RequestedAt: recAt.Add(time.Hour), RequestedBy: "alice"}
	for _, r := range []RecoveryReceipt{first, second, other} {
		if err := j.RecordRecoveryReceipt(ctx, r); err != nil {
			t.Fatalf("RecordRecoveryReceipt: %v", err)
		}
	}
	got, err := j.RecoveryReceipts(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d receipts for op-1, want one per request (2): %+v", len(got), got)
	}
	if got[0].RequestedBy != "alice" || got[0].Coalesced || got[1].RequestedBy != "bob" || !got[1].Coalesced {
		t.Errorf("receipts = %+v, want alice's first and bob's coalesced one", got)
	}
	if !got[1].AcknowledgedCost || got[1].ArtifactID != "production/alpha/dump.sql" || got[1].Set != set || !got[1].RequestedAt.Equal(recAt.Add(time.Second)) {
		t.Errorf("second receipt = %+v: it lost what the request said", got[1])
	}
}

func TestRecoveryReceipts_RefuseAReceiptThatNamesNoOperationOrNoSet(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	if err := j.RecordRecoveryReceipt(ctx, RecoveryReceipt{OperationID: "op", Set: outSet(t, "alpha"), RequestedAt: recAt}); err != nil {
		t.Fatalf("a complete receipt was refused: %v", err)
	}
	if err := j.RecordRecoveryReceipt(ctx, RecoveryReceipt{Set: outSet(t, "alpha"), RequestedAt: recAt}); err == nil {
		t.Error("a receipt with no operation id was accepted")
	}
	if err := j.RecordRecoveryReceipt(ctx, RecoveryReceipt{OperationID: "op", RequestedAt: recAt}); err == nil {
		t.Error("a receipt with no backup set was accepted")
	}
}
