package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// #1220: the list's last result for a validator is found under its name, the
// canonical id a test now records, and still under the script file name that
// every row written before this change carries. The newer of the two wins.

func recordValidatorTest(t *testing.T, svc *BackupService, name, validatorID string, st string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	bs := svc.state.Load().inner.Config.Sources[0].BackupSets[0]
	a := seedCompleteArtifactAt(t, ctx, svc.journal, bs, name, at.Add(-time.Hour), "/backups/"+name)
	rt := state.RecoveryTest{
		Copy: state.CopyRef{Artifact: a, Medium: state.MediumLocal}, State: st, At: at, NextDue: at.Add(time.Hour),
		ValidatorID: validatorID, ValidatorHash: strings.Repeat("ab", 32),
		Termination: state.RecoveryTerminationExited, CleanupState: state.RecoveryCleanupRemoved,
	}
	if st == state.RecoveryPassed {
		yes := true
		rt.DigestMatched = &yes
	} else {
		yes := true
		rt.DigestMatched, rt.Reason = &yes, "exit 1"
	}
	if err := svc.journal.RecordRecoveryTest(ctx, rt); err != nil {
		t.Fatalf("RecordRecoveryTest(%s): %v", name, err)
	}
}

func TestListCustomValidators_TheLastResultIsFoundUnderTheNameAndUnderTheOldFileName(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "manifest", Pattern: "^MANIFEST", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	// Only the old spelling on record: a row from before the change.
	recordValidatorTest(t, svc, "MANIFEST-1.txt", "manifest.local.sh", state.RecoveryPassed, t0)
	l, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if r := l.Validators[0].LastResult; r == nil || r.State != state.RecoveryPassed || !r.At.Equal(t0) {
		t.Fatalf("the old spelling was not read: %+v", r)
	}

	// A newer one under the name: it wins.
	recordValidatorTest(t, svc, "MANIFEST-2.txt", "manifest", state.RecoveryFailed, t0.Add(time.Hour))
	if l, err = svc.ListCustomValidators(ctx, CustomValidatorsFilter{}); err != nil {
		t.Fatal(err)
	}
	if r := l.Validators[0].LastResult; r == nil || r.State != state.RecoveryFailed || r.Artifact != "production/alpha/MANIFEST-2.txt" {
		t.Errorf("the newer result under the name did not win: %+v", r)
	}

	// An older one under the name does not displace a newer old-spelling row.
	recordValidatorTest(t, svc, "MANIFEST-3.txt", "manifest.local.sh", state.RecoveryPassed, t0.Add(2*time.Hour))
	if l, err = svc.ListCustomValidators(ctx, CustomValidatorsFilter{}); err != nil {
		t.Fatal(err)
	}
	if r := l.Validators[0].LastResult; r == nil || r.Artifact != "production/alpha/MANIFEST-3.txt" {
		t.Errorf("the newest row, whichever spelling it uses, should be the last result: %+v", r)
	}
}
