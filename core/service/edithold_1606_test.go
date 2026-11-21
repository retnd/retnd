// Issue #1606: a heartbeat renewal that lands after an edit-hold release
// put the hold back, for another full lease, and stopped any backup run for
// that set that had started in the meantime. Taking and renewing were the
// same unconditional call, so the engine could not tell "keep MY hold" from
// "hold this set again".
//
// Every case here goes through the real BackupService and, where the
// question is whether backups actually resume, through a real run cycle,
// for the reason edithold_test.go gives: a registry that answers right
// while the wiring around it does not would pass a test that only read the
// registry back.
package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// takeHold takes a hold and fails the test if it could not, or if it came
// back without the token every later call needs.
func takeHold(t *testing.T, svc *BackupService) EditHold {
	t.Helper()
	hold, err := svc.BeginBackupSetEdit(context.Background(), fixtureSetID)
	if err != nil {
		t.Fatalf("BeginBackupSetEdit: %v", err)
	}
	if hold.Token == "" {
		t.Fatal("BeginBackupSetEdit returned no holder token, so nothing can renew or release this hold and only this hold")
	}
	return hold
}

// The issue's own sequence: take, release, and only then does the renewal
// that was already in flight arrive. It must be refused, must leave the set
// unheld, and a cycle that runs afterwards must process the set.
func TestEditHold1606_ARenewalThatLandsAfterTheReleaseDoesNotPutTheHoldBack(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()

	hold := takeHold(t, svc)
	if err := svc.EndBackupSetEdit(ctx, fixtureSetID, hold.Token); err != nil {
		t.Fatalf("EndBackupSetEdit: %v", err)
	}

	_, err := svc.RenewBackupSetEdit(ctx, fixtureSetID, hold.Token)
	if !errors.Is(err, ErrEditHoldGone) {
		t.Fatalf("a renewal after the release answered err = %v, want ErrEditHoldGone", err)
	}
	if svc.holds.Held(fixtureSetID) {
		t.Fatal("the renewal that landed after the release put the hold back")
	}

	runOneCycle(t, svc)
	artifacts, err := svc.ListArtifacts(ctx, ArtifactFilter{})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(artifacts) == 0 {
		t.Error("no artifact was processed after the release, so the late renewal kept the set paused")
	}
}

// The other half of the damage the issue names: a late renewal also
// stopped a run that had started after the release. What stops a cycle
// already inside a set is the registry's broadcast, so a refused renewal
// must not broadcast.
func TestEditHold1606_ARefusedRenewalDoesNotStopACycleThatStartedAfterTheRelease(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()

	hold := takeHold(t, svc)
	if err := svc.EndBackupSetEdit(ctx, fixtureSetID, hold.Token); err != nil {
		t.Fatalf("EndBackupSetEdit: %v", err)
	}

	changed := svc.holds.Changed()
	_, _ = svc.RenewBackupSetEdit(ctx, fixtureSetID, hold.Token)
	select {
	case <-changed:
		t.Error("the renewal that landed after the release broadcast a hold change, which is what tells a cycle already inside this set to stop")
	default:
	}
}

// A release with no token is what a client from before tokens sends, and
// what an operator clearing a hold somebody left behind sends. It gives
// back every hold, so a renewal by a holder it released is refused too.
func TestEditHold1606_ARenewalAfterAReleaseWithNoTokenIsRefused(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()

	hold := takeHold(t, svc)
	if err := svc.EndBackupSetEdit(ctx, fixtureSetID, ""); err != nil {
		t.Fatalf("EndBackupSetEdit with no token: %v", err)
	}
	if _, err := svc.RenewBackupSetEdit(ctx, fixtureSetID, hold.Token); !errors.Is(err, ErrEditHoldGone) {
		t.Fatalf("a renewal after a release of every hold answered err = %v, want ErrEditHoldGone", err)
	}
	if svc.holds.Held(fixtureSetID) {
		t.Error("the set is held after every hold on it was released and the only holder's renewal was refused")
	}
}

// A lease that lapsed is gone the same way a released one is. Refusing it
// is what lets the client tell "my hold is gone" apart from "renewed", and
// take a fresh hold that reports what it stopped, rather than a renewal
// silently re-creating a hold under a token nobody holds any more.
func TestEditHold1606_ARenewalAfterTheLeaseLapsedIsRefused(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()
	clock := time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)
	svc.holds.now = func() time.Time { return clock }

	hold := takeHold(t, svc)
	clock = clock.Add(editHoldLease + time.Second)

	if _, err := svc.RenewBackupSetEdit(ctx, fixtureSetID, hold.Token); !errors.Is(err, ErrEditHoldGone) {
		t.Fatalf("a renewal after the lease lapsed answered err = %v, want ErrEditHoldGone", err)
	}
	if svc.holds.Held(fixtureSetID) {
		t.Error("a refused renewal of a lapsed lease left the set held")
	}
}

// A token nobody was ever given is refused the same way, so the answer
// cannot be used to tell a forged token from a stale one.
func TestEditHold1606_ARenewalWithATokenNobodyHoldsIsRefused(t *testing.T) {
	svc, _ := openTestService(t)
	if _, err := svc.RenewBackupSetEdit(context.Background(), fixtureSetID, "not-a-token"); !errors.Is(err, ErrEditHoldGone) {
		t.Fatalf("a renewal with a token nobody holds answered err = %v, want ErrEditHoldGone", err)
	}
	if svc.holds.Held(fixtureSetID) {
		t.Error("a renewal with a token nobody holds created a hold")
	}
}

// Two tabs, or a tab and a script, holding the same set are two holders.
// Each take answers its own token.
func TestEditHold1606_TwoTakesAreTwoHolders(t *testing.T) {
	svc, _ := openTestService(t)
	a, b := takeHold(t, svc), takeHold(t, svc)
	if a.Token == b.Token {
		t.Fatalf("two takes answered the same token %q, so neither can renew or release without touching the other", a.Token)
	}
}

// Holder A renewing does not keep holder B's lease alive. B's tab closed
// without a release; A keeps renewing; B's lease lapses on its own time
// and a renewal under B's token is refused, while A's hold stays in force.
func TestEditHold1606_TwoHoldersDoNotRenewEachOthersHold(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()
	clock := time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)
	svc.holds.now = func() time.Time { return clock }

	a, b := takeHold(t, svc), takeHold(t, svc)

	clock = clock.Add(editHoldLease / 2)
	if _, err := svc.RenewBackupSetEdit(ctx, fixtureSetID, a.Token); err != nil {
		t.Fatalf("holder A's renewal: %v", err)
	}
	clock = clock.Add(editHoldLease/2 + time.Second)

	if _, err := svc.RenewBackupSetEdit(ctx, fixtureSetID, b.Token); !errors.Is(err, ErrEditHoldGone) {
		t.Errorf("holder B's renewal after its own lease lapsed answered err = %v, want ErrEditHoldGone: A's renewal kept B's hold alive", err)
	}
	if !svc.holds.Held(fixtureSetID) {
		t.Error("the set is not held although holder A renewed within its lease")
	}
	if _, err := svc.RenewBackupSetEdit(ctx, fixtureSetID, a.Token); err != nil {
		t.Errorf("holder A's renewal after B's lease lapsed: %v", err)
	}
}

// Holder A leaving edit mode gives back A's hold, not B's: the set stays
// held while B is still editing, B can keep renewing, and the set is only
// free once B leaves too. Before tokens, A's release dropped the hold
// from under B until B's next heartbeat took it again, and a cycle could
// start in between.
func TestEditHold1606_ReleasingOneHolderLeavesTheOtherInForce(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()

	a, b := takeHold(t, svc), takeHold(t, svc)
	if err := svc.EndBackupSetEdit(ctx, fixtureSetID, a.Token); err != nil {
		t.Fatalf("releasing holder A: %v", err)
	}
	if !svc.holds.Held(fixtureSetID) {
		t.Fatal("releasing holder A released holder B's hold as well")
	}
	if _, err := svc.RenewBackupSetEdit(ctx, fixtureSetID, b.Token); err != nil {
		t.Errorf("holder B's renewal after A left: %v", err)
	}
	if _, err := svc.RenewBackupSetEdit(ctx, fixtureSetID, a.Token); !errors.Is(err, ErrEditHoldGone) {
		t.Errorf("holder A's renewal after A released answered err = %v, want ErrEditHoldGone", err)
	}

	if err := svc.EndBackupSetEdit(ctx, fixtureSetID, b.Token); err != nil {
		t.Fatalf("releasing holder B: %v", err)
	}
	if svc.holds.Held(fixtureSetID) {
		t.Fatal("the set is still held after both holders released")
	}
	runOneCycle(t, svc)
	artifacts, err := svc.ListArtifacts(ctx, ArtifactFilter{})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(artifacts) == 0 {
		t.Error("no artifact was processed after both holders released")
	}
}

// A renewal answers the token it renewed, and a renewal of a live lease
// claims to have stopped nothing: the set was already held, so whatever a
// cycle was doing when the hold was taken was reported by that take.
func TestEditHold1606_ARenewalKeepsItsTokenAndStopsNothing(t *testing.T) {
	svc, _ := openTestService(t)
	hold := takeHold(t, svc)
	renewed, err := svc.RenewBackupSetEdit(context.Background(), fixtureSetID, hold.Token)
	if err != nil {
		t.Fatalf("RenewBackupSetEdit: %v", err)
	}
	if renewed.Token != hold.Token {
		t.Errorf("the renewal answered token %q, want the one it renewed (%q)", renewed.Token, hold.Token)
	}
	if renewed.Stopped != nil {
		t.Errorf("a renewal of a live lease reported Stopped = %+v, want nil", renewed.Stopped)
	}
}

// Releasing a token that is already gone is a success, like releasing a
// set that is not held: several routes out of edit mode fire for one edit.
func TestEditHold1606_ReleasingAStaleTokenIsASuccess(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()
	hold := takeHold(t, svc)
	for i := 0; i < 2; i++ {
		if err := svc.EndBackupSetEdit(ctx, fixtureSetID, hold.Token); err != nil {
			t.Fatalf("release %d: %v", i+1, err)
		}
	}
}

// A renewal still names a set the deployment configures, and an unknown
// one is the same not-found every other edit-hold call answers.
func TestEditHold1606_RenewingAnUnknownSetIsNotFound(t *testing.T) {
	svc, _ := openTestService(t)
	if _, err := svc.RenewBackupSetEdit(context.Background(), "production/nope", "x"); !errors.Is(err, ErrBackupSetNotFound) {
		t.Errorf("RenewBackupSetEdit on an unknown set: err = %v, want ErrBackupSetNotFound", err)
	}
}
