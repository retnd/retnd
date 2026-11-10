package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// #1317 at the service layer, where `retnd encryption reencrypt` and GET/POST
// /api/v1/encryption/reencrypt meet: selecting encrypted copies by suite, key
// or deprecation and asking the engine, per copy, whether a run would rewrite
// it under the active suite and key.
//
// They were written by reading the service and the fixtures and have not been
// run. Each one names the defect that turns it red in its own comment.

// reencryptFixture is a deployment that requires encryption with two managed
// keys (the first superseded by a rotation, the second active) and two
// encrypted copies on "warm": one under the old key, one under the active
// one. The records are the per-copy records the engine would have written;
// the dry run reads records and resolves keys, never objects.
func reencryptFixture(t *testing.T) (f *recoveryFixture, old, current model.ArtifactID, oldKey, activeKey string) {
	t.Helper()
	f, old = retroFixture(t, "old.dump")
	ctx := context.Background()
	keys, err := f.b.ListEncryptionKeys(ctx)
	if err != nil || len(keys.Keys) != 1 {
		t.Fatalf("control: want one key before the rotation, got %+v (err=%v)", keys, err)
	}
	oldKey = keys.Keys[0].ID
	rotated, err := f.b.RotateEncryptionKey(ctx)
	if err != nil {
		t.Fatalf("RotateEncryptionKey: %v", err)
	}
	activeKey = rotated.ID
	if activeKey == oldKey {
		t.Fatal("control: the rotation did not mint a new key")
	}
	current = seedComplete(t, f, "current.dump", "warm")
	for a, key := range map[model.ArtifactID]string{old: oldKey, current: activeKey} {
		if err := f.b.journal.RecordPlacementEncryption(ctx, a, "warm", state.PlacementEncryption{
			FormatID: "age/v1", FormatVersion: 1, KeyID: key,
			StoredHash: strings.Repeat("e", 64), StoredHashAlg: "sha256", StoredSize: int64(len(f.data)) + 200,
		}); err != nil {
			t.Fatalf("seeding the encrypted copy of %s: %v", a, err)
		}
	}
	return f, old, current, oldKey, activeKey
}

// The acceptance line: `reencrypt --suite age/v1 --dry-run` lists every active
// age/v1 copy with its key and destination, grouped by suite and key, and
// refuses per copy the one it cannot do with the reason named. Red if the
// copies are not grouped by key, if the copy under the active key is offered,
// if the one under the superseded key is refused, if a destination is missing,
// or if the dry run plans a move or reads an object.
func TestReencryptDryRunGroupsBySuiteAndKey_1317(t *testing.T) {
	f, old, current, oldKey, activeKey := reencryptFixture(t)
	ctx := context.Background()
	opensBefore := f.store.opens.Load()

	plan, err := f.b.PlanReencrypt(ctx, apicontract.StartReencryptRequest{SuiteID: "age/v1", DestinationMedium: "other"})
	if err != nil {
		t.Fatalf("PlanReencrypt: %v", err)
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("%d groups, want one per key under age/v1: %+v", len(plan.Groups), plan.Groups)
	}
	byKey := map[string]apicontract.ReencryptGroup{}
	for _, g := range plan.Groups {
		if g.SuiteID != "age/v1" {
			t.Errorf("a group under suite %q, want only age/v1", g.SuiteID)
		}
		if len(g.Candidates) != 1 {
			t.Fatalf("group %s/%s holds %d copies, want one", g.SuiteID, g.KeyID, len(g.Candidates))
		}
		byKey[g.KeyID] = g
	}
	o, c := byKey[oldKey].Candidates, byKey[activeKey].Candidates
	if len(o) != 1 || o[0].ArtifactID != old.String() || o[0].Verdict != "would_reencrypt" || o[0].SourceMedium != "warm" || o[0].DestinationMedium != "other" {
		t.Fatalf("the copy under the superseded key is %+v, want it offered from warm onto other", o)
	}
	if len(c) != 1 || c[0].ArtifactID != current.String() || c[0].Verdict != "refused" || c[0].Refusal != "already_current" || c[0].Reason == "" {
		t.Fatalf("the copy under the active key is %+v, want it refused as already_current with a reason", c)
	}
	if plan.WouldReencrypt != 1 || plan.Refused != 1 {
		t.Fatalf("totals %d/%d, want 1 would_reencrypt and 1 refused", plan.WouldReencrypt, plan.Refused)
	}
	if moves, err := f.b.journal.ListMoves(ctx); err != nil || len(moves) != 0 {
		t.Fatalf("the dry run planned %d move(s) (err=%v)", len(moves), err)
	}
	if got := f.store.opens.Load() - opensBefore; got != 0 {
		t.Errorf("the dry run read %d object(s)", got)
	}
}

// Selecting by key picks exactly that key's copies. Red if the selector leaks
// copies under another key.
func TestReencryptByKeySelectsOnlyThatKeysCopies_1317(t *testing.T) {
	f, old, _, oldKey, _ := reencryptFixture(t)
	plan, err := f.b.PlanReencrypt(context.Background(), apicontract.StartReencryptRequest{KeyID: oldKey, DestinationMedium: "other"})
	if err != nil {
		t.Fatalf("PlanReencrypt: %v", err)
	}
	if len(plan.Groups) != 1 || plan.Groups[0].KeyID != oldKey || len(plan.Groups[0].Candidates) != 1 || plan.Groups[0].Candidates[0].ArtifactID != old.String() {
		t.Fatalf("plan %+v, want only the copy under %s", plan, oldKey)
	}
}

// With no destination the dry run still names one per copy (the first other
// configured medium), and the run refuses to choose. Red if the dry run leaves
// the destination empty, or a run starts with no destination.
func TestTheDryRunProposesADestinationAndARunNeedsOne_1317(t *testing.T) {
	f, _, _, oldKey, _ := reencryptFixture(t)
	ctx := context.Background()
	plan, err := f.b.PlanReencrypt(ctx, apicontract.StartReencryptRequest{KeyID: oldKey})
	if err != nil {
		t.Fatalf("PlanReencrypt: %v", err)
	}
	if len(plan.Groups) != 1 || plan.Groups[0].Candidates[0].DestinationMedium == "" || plan.Groups[0].Candidates[0].DestinationMedium == "warm" {
		t.Fatalf("plan %+v, want a proposed destination other than the copy's own medium", plan)
	}
	if _, err := f.b.StartReencrypt(ctx, apicontract.StartReencryptRequest{KeyID: oldKey}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("StartReencrypt with no destination = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.b.RunReencrypt(ctx, apicontract.StartReencryptRequest{KeyID: oldKey}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("RunReencrypt with no destination = %v, want ErrInvalidRequest", err)
	}
}

// A copy whose old key cannot be resolved is listed, named, and refused with
// its own code; the rest of the plan is still answered. Red if the missing key
// fails the whole request or the copy is offered.
func TestACopyWhoseKeyIsUnavailableIsNamedAndRefused_1317(t *testing.T) {
	f, old, _, _, _ := reencryptFixture(t)
	ctx := context.Background()
	if err := f.b.journal.RecordPlacementEncryption(ctx, old, "warm", state.PlacementEncryption{
		FormatID: "age/v1", FormatVersion: 1, KeyID: "knosuchkey000000",
		StoredHash: strings.Repeat("e", 64), StoredHashAlg: "sha256", StoredSize: int64(len(f.data)) + 200,
	}); err != nil {
		// A journal that refuses a record naming an unregistered key cannot
		// hold this state at all, which is the stronger answer.
		t.Skipf("the journal refuses a record under an unregistered key: %v", err)
	}
	plan, err := f.b.PlanReencrypt(ctx, apicontract.StartReencryptRequest{SuiteID: "age/v1", DestinationMedium: "other"})
	if err != nil {
		t.Fatalf("PlanReencrypt: %v", err)
	}
	var found bool
	for _, g := range plan.Groups {
		for _, c := range g.Candidates {
			if c.ArtifactID == old.String() {
				found = true
				if c.Refusal != "source_key_unavailable" || !strings.Contains(c.Reason, "knosuchkey000000") {
					t.Errorf("the copy under the missing key is %+v, want source_key_unavailable naming the key", c)
				}
			}
		}
	}
	if !found {
		t.Fatal("the copy under the missing key is not listed, so the operator is never told about it")
	}
}

// The request is checked before anything is planned: no selector, two
// selectors, a suite this build does not know, a destination that is not
// configured. Red if any of them reaches the engine.
func TestReencryptRefusesBadInputsBeforePlanningAnything_1317(t *testing.T) {
	f, _, _, oldKey, _ := reencryptFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		req  apicontract.StartReencryptRequest
	}{
		{"no selector", apicontract.StartReencryptRequest{DestinationMedium: "other"}},
		{"two selectors", apicontract.StartReencryptRequest{SuiteID: "age/v1", KeyID: oldKey, DestinationMedium: "other"}},
		{"an unknown suite", apicontract.StartReencryptRequest{SuiteID: "nosuch/v9", DestinationMedium: "other"}},
		{"a destination that is not configured", apicontract.StartReencryptRequest{SuiteID: "age/v1", DestinationMedium: "nowhere"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.b.PlanReencrypt(ctx, tc.req); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("PlanReencrypt = %v, want ErrInvalidRequest", err)
			}
			if _, err := f.b.StartReencrypt(ctx, tc.req); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("StartReencrypt = %v, want ErrInvalidRequest", err)
			}
		})
	}
	if moves, err := f.b.journal.ListMoves(ctx); err != nil || len(moves) != 0 {
		t.Fatalf("%d move(s) after refused requests (err=%v)", len(moves), err)
	}
}

// The browser's cancel and the terminal's cancel leave the same durable state,
// as for #1291: both end in a cancelled context handed to the engine, so the
// move is abandoned and the old copy and its placement are untouched. Each
// surface gets its own deployment, because the selector is deployment-wide
// and a second run would pick the first one's copy up again. Red if the two
// surfaces differ, or a cancel leaves a live move.
func TestACancelOfAReencryptFromTheBrowserAndFromTheTerminalLeaveTheSameState_1317(t *testing.T) {
	ctx := context.Background()

	f1, old1, _, oldKey1, _ := reencryptFixture(t)
	stall := &stallOpenStore{recoveryStore: f1.store, opened: make(chan struct{})}
	f1.b.state.Load().inner.MediumStore = stall
	started, err := f1.b.StartReencrypt(ctx, apicontract.StartReencryptRequest{KeyID: oldKey1, DestinationMedium: "other"})
	if err != nil {
		t.Fatalf("StartReencrypt: %v", err)
	}
	waitOpened(t, stall.opened, "the browser's run never reached the old copy")
	if _, err := f1.b.CancelAssuranceOperation(ctx, started.OperationID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	idle(t, f1)
	browser := retroState(t, f1, old1)

	f2, old2, _, oldKey2, _ := reencryptFixture(t)
	stall2 := &stallOpenStore{recoveryStore: f2.store, opened: make(chan struct{})}
	f2.b.state.Load().inner.MediumStore = stall2
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f2.b.RunReencrypt(runCtx, apicontract.StartReencryptRequest{KeyID: oldKey2, DestinationMedium: "other"})
		done <- err
	}()
	waitOpened(t, stall2.opened, "the terminal's run never reached the old copy")
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("RunReencrypt after a cancel: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the terminal's run did not stop after its context ended")
	}
	terminal := retroState(t, f2, old2)

	if browser != terminal {
		t.Fatalf("a cancel left different state from the two surfaces:\n browser:  %s\n terminal: %s", browser, terminal)
	}
	// retroState calls the source object "plaintext"; here it is the old
	// ciphertext, and "present" is the claim either way.
	if want := "phase=ABANDONED plaintext=present placement=ACTIVE destination=empty record=none live=0"; browser != want {
		t.Fatalf("state after a cancel is %q, want %q", browser, want)
	}
}

func waitOpened(t *testing.T, opened <-chan struct{}, why string) {
	t.Helper()
	select {
	case <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal(why)
	}
}
