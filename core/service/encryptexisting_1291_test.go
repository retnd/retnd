package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// #1291 at the service layer, where the verb and the route meet: encrypting a
// retained copy that already sits on a medium in the clear. The verb
// (`retnd encryption encrypt-existing`) and the routes call the methods tested
// here, and cancelling goes through the operation registry the assurance
// operations already use, so one cancel path serves the terminal and the
// browser.
//
// They were written by reading the service and the fixtures and have not been
// run. Each one names the defect that turns it red in its own comment.

// requireEncryption turns the fixture's one backup set to "required" under the
// managed key, which is the state an operator is in the moment after flipping
// the policy with plaintext copies already on a medium.
func requireEncryption(cfg *config.Config) {
	cfg.Sources[0].BackupSets[0].RetainedCopyEncryption = config.RetainedCopyEncryption{
		Mode:        config.RetainedCopyEncryptionRequired,
		KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
	}
}

// retroFixture is a deployment whose set requires encryption, with a managed
// key, and a plaintext artifact on "warm".
func retroFixture(t *testing.T, name string) (*recoveryFixture, model.ArtifactID) {
	t.Helper()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireEncryption})
	fileBacked(t, f)
	if _, err := f.b.CreateEncryptionKey(context.Background()); err != nil {
		t.Fatalf("CreateEncryptionKey: %v", err)
	}
	return f, seedComplete(t, f, name, "warm")
}

func retroReq(a model.ArtifactID) apicontract.StartEncryptExistingRequest {
	return apicontract.StartEncryptExistingRequest{BackupSetID: a.Set.String(), ArtifactID: a.String(), DestinationMedium: "other"}
}

// Red if a set that does not require encryption can be asked to encrypt
// existing copies: the verb is for a set whose policy already says required,
// and running it on any other would be an encryption nobody configured.
func TestEncryptExistingIsRefusedForASetThatDoesNotRequireEncryption_1291(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	a := seedComplete(t, f, "plain.dump", "warm")

	_, err := f.b.StartEncryptExisting(context.Background(), retroReq(a))
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("StartEncryptExisting = %v, want ErrInvalidRequest", err)
	}
	if len(f.store.objects) != 1 {
		t.Errorf("%d objects on the media after a refusal, want only the seeded plaintext copy", len(f.store.objects))
	}
}

// Red if the inputs are not checked before anything is planned: an unknown set,
// an unknown destination, a destination that is the copy's own medium, and an
// artifact from another set each have to be refused with a request error.
func TestEncryptExistingRefusesBadInputsBeforePlanningAnything_1291(t *testing.T) {
	f, a := retroFixture(t, "a.dump")
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		req  apicontract.StartEncryptExistingRequest
		want error
	}{
		{"an unknown set", apicontract.StartEncryptExistingRequest{BackupSetID: "nosuch/set", DestinationMedium: "other"}, ErrBackupSetNotFound},
		{"no destination", apicontract.StartEncryptExistingRequest{BackupSetID: a.Set.String()}, ErrInvalidRequest},
		{"a destination that is not configured", apicontract.StartEncryptExistingRequest{BackupSetID: a.Set.String(), DestinationMedium: "nowhere"}, ErrInvalidRequest},
		{"an artifact of another set", apicontract.StartEncryptExistingRequest{BackupSetID: a.Set.String(), ArtifactID: "other/set/x.dump", DestinationMedium: "other"}, ErrInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.b.StartEncryptExisting(ctx, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	moves, err := f.b.journal.ListMoves(ctx)
	if err != nil || len(moves) != 0 {
		t.Fatalf("%d move(s) after refused requests (err=%v)", len(moves), err)
	}
}

// The dry run is the same decision the run makes, per copy, with a named
// reason. Red if it plans anything, reads any object, or reports a copy the
// run would refuse as one it would encrypt.
func TestThePlanSaysPerCopyWhatWouldHappenAndWhyAndTouchesNothing_1291(t *testing.T) {
	f, a := retroFixture(t, "a.dump")
	ctx := context.Background()
	opensBefore := f.store.opens.Load()

	plan, err := f.b.PlanEncryptExisting(ctx, a.Set.String(), "", "other")
	if err != nil {
		t.Fatalf("PlanEncryptExisting: %v", err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].ArtifactID != a.String() || plan.Candidates[0].SourceMedium != "warm" {
		t.Fatalf("candidates %+v, want the one plaintext copy on warm", plan.Candidates)
	}
	if plan.Candidates[0].Verdict != "would_encrypt" || plan.WouldEncrypt != 1 || plan.Refused != 0 {
		t.Fatalf("plan %+v, want the copy listed as would_encrypt", plan)
	}
	if moves, err := f.b.journal.ListMoves(ctx); err != nil || len(moves) != 0 {
		t.Fatalf("the dry run planned %d move(s) (err=%v)", len(moves), err)
	}
	if got := f.store.opens.Load() - opensBefore; got != 0 {
		t.Errorf("the dry run read %d object(s)", got)
	}

}

// A copy that is already encrypted is listed as refused with its own code,
// and the rest of the plan is still answered. Red if one refusal fails the
// whole request, or reports the encrypted copy as a candidate.
func TestThePlanListsAnAlreadyEncryptedCopyAsRefusedWithItsOwnCode_1291(t *testing.T) {
	f, a := retroFixture(t, "a.dump")
	ctx := context.Background()
	key, err := f.b.ListEncryptionKeys(ctx)
	if err != nil || len(key.Keys) == 0 {
		t.Fatalf("keys: %+v err=%v", key, err)
	}
	if err := f.b.journal.RecordPlacementEncryption(ctx, a, "warm", state.PlacementEncryption{
		FormatID: "age", FormatVersion: 1, KeyID: key.Keys[0].ID,
		StoredHash: strings.Repeat("e", 64), StoredHashAlg: "sha256", StoredSize: int64(len(f.data)) + 200,
	}); err != nil {
		t.Fatalf("seeding the encrypted copy: %v", err)
	}

	plan, err := f.b.PlanEncryptExisting(ctx, a.Set.String(), "", "other")
	if err != nil {
		t.Fatalf("PlanEncryptExisting: %v", err)
	}
	if plan.WouldEncrypt != 0 || plan.Refused != 1 || plan.Candidates[0].Refusal != "already_encrypted" {
		t.Fatalf("plan %+v, want the one copy refused as already_encrypted", plan)
	}
}

// The browser's cancel and the terminal's cancel leave the same durable state.
// The browser starts the operation (StartEncryptExisting) and cancels it by
// operation id; the terminal runs it in its own process (RunEncryptExisting)
// and cancels by ending the context. Both end in a cancelled context handed to
// the engine's EncryptExisting, so the move is abandoned, the plaintext object
// and its ACTIVE placement are untouched, nothing is on the destination, and
// no move is left live. Red if the two differ in any of those rows.
func TestACancelFromTheBrowserAndFromTheTerminalLeaveTheSameState_1291(t *testing.T) {
	ctx := context.Background()
	f, a := retroFixture(t, "a.dump")
	stall := &stallOpenStore{recoveryStore: f.store, opened: make(chan struct{})}
	f.b.state.Load().inner.MediumStore = stall

	started, err := f.b.StartEncryptExisting(ctx, retroReq(a))
	if err != nil {
		t.Fatalf("StartEncryptExisting: %v", err)
	}
	select {
	case <-stall.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never reached the plaintext copy")
	}
	if _, err := f.b.CancelAssuranceOperation(ctx, started.OperationID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	idle(t, f)
	browser := retroState(t, f, a)

	// The terminal's run, on a second plaintext copy in the same deployment.
	b2 := seedComplete(t, f, "b.dump", "warm")
	stall2 := &stallOpenStore{recoveryStore: f.store, opened: make(chan struct{})}
	f.b.state.Load().inner.MediumStore = stall2
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.b.RunEncryptExisting(runCtx, retroReq(b2))
		done <- err
	}()
	select {
	case <-stall2.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the terminal's run never reached the plaintext copy")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("RunEncryptExisting after a cancel: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the terminal's run did not stop after its context ended")
	}
	terminal := retroState(t, f, b2)

	if browser != terminal {
		t.Fatalf("a cancel left different state from the two surfaces:\n browser:  %s\n terminal: %s", browser, terminal)
	}
	if want := "phase=ABANDONED plaintext=present placement=ACTIVE destination=empty record=none live=0"; browser != want {
		t.Fatalf("state after a cancel is %q, want %q", browser, want)
	}
}

// retroState describes what a cancelled run leaves for one artifact, as one
// comparable line. The plaintext object is on "warm" under the seeded key.
func retroState(t *testing.T, f *recoveryFixture, a model.ArtifactID) string {
	t.Helper()
	ctx := context.Background()
	moves, err := f.b.journal.MovesForArtifact(ctx, a)
	if err != nil || len(moves) == 0 {
		t.Fatalf("no move for %s (err=%v)", a, err)
	}
	phase := moves[len(moves)-1].Phase
	f.store.mu.Lock()
	_, plaintext := f.store.objects["prefix/"+a.Name]
	var destination int
	for k := range f.store.objects {
		if k != "prefix/"+a.Name && len(k) >= len(a.Name) && k[len(k)-len(a.Name):] == a.Name {
			destination++
		}
	}
	f.store.mu.Unlock()
	rec, err := f.b.journal.Get(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	status := "none"
	for _, p := range rec.Placements {
		if p.Medium == "warm" {
			status = p.Status
		}
	}
	_, hasRecord, err := f.b.journal.PlacementEncryption(ctx, a, "other")
	if err != nil {
		t.Fatal(err)
	}
	live, err := f.b.journal.ListMoves(ctx, "PLANNED", "COPYING", "COPIED", "VERIFYING", "VERIFIED", "SOURCE_DELETE_PENDING")
	if err != nil {
		t.Fatal(err)
	}
	present := "gone"
	if plaintext {
		present = "present"
	}
	dest := "empty"
	if destination > 0 {
		dest = fmt.Sprintf("%d object(s)", destination)
	}
	record := "none"
	if hasRecord {
		record = "present"
	}
	return fmt.Sprintf("phase=%s plaintext=%s placement=%s destination=%s record=%s live=%d", phase, present, status, dest, record, len(live))
}

// Only one run per backup set at a time, so a double click cannot become two
// passes over the same copies. Red if a second start is accepted while one is
// running.
func TestASecondRunForTheSameSetIsRefusedWhileOneIsRunning_1291(t *testing.T) {
	ctx := context.Background()
	f, a := retroFixture(t, "a.dump")
	stall := &stallOpenStore{recoveryStore: f.store, opened: make(chan struct{})}
	f.b.state.Load().inner.MediumStore = stall

	started, err := f.b.StartEncryptExisting(ctx, retroReq(a))
	if err != nil {
		t.Fatalf("StartEncryptExisting: %v", err)
	}
	<-stall.opened
	if _, err := f.b.StartEncryptExisting(ctx, retroReq(a)); !errors.Is(err, ErrEncryptExistingRunning) {
		t.Fatalf("a second start while one runs: err = %v, want ErrEncryptExistingRunning", err)
	}
	if _, err := f.b.CancelAssuranceOperation(ctx, started.OperationID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	idle(t, f)
}

// A normal move cycle and an encrypt-existing run must not drive moves at the
// same time, so both take the single-flight guard the cycle uses (runOnce).
// Red if either entry point starts while a cycle holds it, or keeps it after
// it ends.
func TestEncryptExistingTakesTheCycleSingleFlightGuard_1291(t *testing.T) {
	ctx := context.Background()
	f, a := retroFixture(t, "a.dump")

	f.b.runOnce.Lock()
	if _, err := f.b.StartEncryptExisting(ctx, retroReq(a)); !errors.Is(err, ErrOperationAlreadyRunning) {
		t.Errorf("StartEncryptExisting while a cycle runs: err = %v, want ErrOperationAlreadyRunning", err)
	}
	if _, err := f.b.RunEncryptExisting(ctx, retroReq(a)); !errors.Is(err, ErrOperationAlreadyRunning) {
		t.Errorf("RunEncryptExisting while a cycle runs: err = %v, want ErrOperationAlreadyRunning", err)
	}
	f.b.runOnce.Unlock()

	stall := &stallOpenStore{recoveryStore: f.store, opened: make(chan struct{})}
	f.b.state.Load().inner.MediumStore = stall
	started, err := f.b.StartEncryptExisting(ctx, retroReq(a))
	if err != nil {
		t.Fatalf("StartEncryptExisting: %v", err)
	}
	<-stall.opened
	if f.b.runOnce.TryLock() {
		f.b.runOnce.Unlock()
		t.Error("a running encrypt-existing operation does not hold the guard")
	}
	if _, err := f.b.CancelAssuranceOperation(ctx, started.OperationID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	idle(t, f)
	if !f.b.runOnce.TryLock() {
		t.Fatal("the guard is still held after the operation ended")
	}
	f.b.runOnce.Unlock()
}
