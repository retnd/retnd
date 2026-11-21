package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// #1545 at the service layer: `reencrypt --from-local` and the matching field
// on GET/POST /encryption/reencrypt. An artifact that still has an ACTIVE local
// copy is refused by default and, only when the operator asks, is written anew
// from that copy; the dry run says per copy which path it takes and what each
// medium would read and write.

// fromLocalFixture is reencryptFixture plus one artifact that sits on local
// disk and on "warm" under the superseded key, which is the artifact of the
// issue, and a second one (the fixture's old.dump) with no local copy.
func fromLocalFixture(t *testing.T) (f *recoveryFixture, kept, plain model.ArtifactID, oldKey string) {
	t.Helper()
	f, plain, _, oldKey, _ = reencryptFixture(t)
	kept = seedComplete(t, f, "kept.dump", state.MediumLocal, "warm")
	// What stands for the old ciphertext on warm: bytes that are not the
	// artifact's, so the record can describe them honestly.
	blob := append([]byte("not-the-plaintext:"), f.data...)
	f.store.mu.Lock()
	f.store.objects["prefix/kept.dump"] = blob
	f.store.mu.Unlock()
	if err := f.b.journal.RecordPlacementEncryption(context.Background(), kept, "warm", state.PlacementEncryption{
		FormatID: fixtureSuite, FormatVersion: 1, KeyID: oldKey,
		// The retire of the old copy checks the object against this record, so
		// it describes the bytes on warm.
		StoredHash: sha(blob), StoredHashAlg: "sha256", StoredSize: int64(len(blob)),
	}); err != nil {
		t.Fatalf("seeding the encrypted copy of %s: %v", kept, err)
	}
	return f, kept, plain, oldKey
}

func candidate(t *testing.T, plan apicontract.ReencryptPlan, a model.ArtifactID) apicontract.ReencryptCandidate {
	t.Helper()
	for _, g := range plan.Groups {
		for _, c := range g.Candidates {
			if c.ArtifactID == a.String() {
				return c
			}
		}
	}
	t.Fatalf("%s is not in the plan %+v", a, plan)
	return apicontract.ReencryptCandidate{}
}

// Without the field nothing changes: the artifact with a local copy is refused
// as not eligible and the other one is a move. Green today; it has to stay so.
func TestTheDryRunRefusesAnArtifactWithALocalCopyUnlessAskedNotTo_1545(t *testing.T) {
	f, kept, plain, oldKey := fromLocalFixture(t)
	plan, err := f.b.PlanReencrypt(context.Background(), apicontract.StartReencryptRequest{KeyID: oldKey, DestinationMedium: "other"})
	if err != nil {
		t.Fatalf("PlanReencrypt: %v", err)
	}
	if c := candidate(t, plan, kept); c.Verdict != "refused" || c.Refusal != "not_eligible" {
		t.Fatalf("the artifact with a local copy is %+v, want refused as not_eligible", c)
	}
	if c := candidate(t, plan, plain); c.Verdict != "would_reencrypt" || c.Path != "move" {
		t.Fatalf("the artifact with no local copy is %+v, want would_reencrypt by move", c)
	}
}

// Red until the field exists. With it, the artifact with a local copy is
// offered by upload_from_local and the other one stays a move, and the dry run
// still plans nothing and reads no object.
func TestTheDryRunSaysWhichPathEachCopyTakes_1545(t *testing.T) {
	f, kept, plain, oldKey := fromLocalFixture(t)
	opensBefore := f.store.opens.Load()
	plan, err := f.b.PlanReencrypt(context.Background(), apicontract.StartReencryptRequest{KeyID: oldKey, DestinationMedium: "other", FromLocal: true})
	if err != nil {
		t.Fatalf("PlanReencrypt: %v", err)
	}
	if c := candidate(t, plan, kept); c.Verdict != "would_reencrypt" || c.Path != "upload_from_local" || c.SourceMedium != "warm" || c.DestinationMedium != "other" {
		t.Fatalf("the artifact with a local copy is %+v, want would_reencrypt by upload_from_local from warm onto other", c)
	}
	if c := candidate(t, plan, plain); c.Verdict != "would_reencrypt" || c.Path != "move" {
		t.Fatalf("the artifact with no local copy is %+v, want would_reencrypt by move", c)
	}
	if plan.WouldReencrypt != 2 || plan.Refused != 0 {
		t.Fatalf("totals %d/%d, want 2 would_reencrypt and 0 refused", plan.WouldReencrypt, plan.Refused)
	}
	if moves, err := f.b.journal.ListMoves(context.Background()); err != nil || len(moves) != 0 {
		t.Fatalf("the dry run planned %d move(s) (err=%v)", len(moves), err)
	}
	if got := f.store.opens.Load() - opensBefore; got != 0 {
		t.Errorf("the dry run read %d object(s)", got)
	}
}

// The bytes, per medium, in the shape EPIC V's V1.3 gives. The upload path
// reads the local copy (its plaintext size) and nothing from "warm"; the move
// path reads warm's stored size; both write the destination an upper bound and
// read it back. Red if the old copy is charged for the upload path, if the
// local read is missing, or if the verification read of the destination is.
func TestTheDryRunStatesBytesPerMediumForBothPaths_1545(t *testing.T) {
	f, _, _, oldKey := fromLocalFixture(t)
	plan, err := f.b.PlanReencrypt(context.Background(), apicontract.StartReencryptRequest{KeyID: oldKey, DestinationMedium: "other", FromLocal: true})
	if err != nil {
		t.Fatalf("PlanReencrypt: %v", err)
	}
	p := int64(len(f.data))
	bound := p + 16*max(1, (p+65535)/65536) + 16 + 4096
	got := map[string]apicontract.ReencryptMedium{}
	for _, m := range plan.Media {
		got[m.Medium] = m
	}
	want := map[string]apicontract.ReencryptMedium{
		"local": {Medium: "local", BytesRead: p},
		"warm":  {Medium: "warm", BytesRead: p + 200}, // only the move path reads it
		"other": {Medium: "other", BytesRead: 2 * bound, BytesWritten: 2 * bound},
	}
	if len(got) != len(want) {
		t.Fatalf("media %+v, want exactly %+v", plan.Media, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("medium %s = %+v, want %+v", k, got[k], w)
		}
	}
	if plan.SizesUnknown != 0 {
		t.Errorf("sizes_unknown = %d, want 0", plan.SizesUnknown)
	}
}

// A run with the field writes the new copy for the artifact that has a local
// copy: a fresh copy on "other" under the active key, decrypt-verified and
// recorded, the local file and its placement exactly as they were, the copy
// under the old key waiting to be retired. This fixture's store refuses every
// delete (it is the recovery double), so the run stops where the retire would
// begin; the retire itself, and the end state, are the engine's tests and the
// crash matrix. Red until the field is honoured.
func TestARunWithFromLocalWritesTheNewCopyFromTheLocalCopy_1545(t *testing.T) {
	f, kept, _, oldKey := fromLocalFixture(t)
	ctx := context.Background()
	local := filepath.Join(f.localDir, "kept.dump")

	got, err := f.b.RunReencrypt(ctx, apicontract.StartReencryptRequest{KeyID: oldKey, DestinationMedium: "other", FromLocal: true})
	if err != nil {
		t.Fatalf("RunReencrypt: %v", err)
	}
	var line *EncryptExistingOutcome
	for i := range got.Outcomes {
		if got.Outcomes[i].ArtifactID == kept.String() {
			line = &got.Outcomes[i]
		}
	}
	if line == nil || line.Phase != "SOURCE_DELETE_PENDING" || line.Refusal == "not_eligible" {
		t.Fatalf("the artifact with a local copy ended %+v in %+v, want it written and waiting to retire the old copy", line, got)
	}
	if b, err := os.ReadFile(local); err != nil || string(b) != string(f.data) {
		t.Fatalf("the local copy is gone or changed (err=%v)", err)
	}
	rec, err := f.b.journal.Get(ctx, kept)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, p := range rec.Placements {
		status[p.Medium] = p.Status
	}
	if status[state.MediumLocal] != state.PlacementActive || status["other"] != state.PlacementActive || status["warm"] != state.PlacementDeletePending {
		t.Fatalf("placements %v, want local ACTIVE, other ACTIVE and warm DELETE_PENDING", status)
	}
	enc, ok, err := f.b.journal.PlacementEncryption(ctx, kept, "other")
	if err != nil || !ok || enc.KeyID == oldKey {
		t.Fatalf("the new copy's record is %+v (ok=%v err=%v), want it under the active key", enc, ok, err)
	}
}
