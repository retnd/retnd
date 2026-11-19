package service

import (
	"context"
	"testing"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
)

// Issue #1108: the per-set assurance answer says whether a recovery test of
// the set would find a validator, from the same dry run the test makes first,
// so the page can disable a button that could only be refused.

// noValidatorStages is a stage runner for a set whose recovery-test directory
// holds nothing: the dry run finds no script.
type noValidatorStages struct{}

func (noValidatorStages) RunStage(context.Context, app.StageRequest) (app.StageResult, error) {
	return app.StageResult{Configured: false}, nil
}

func TestIssue1108_TheSetsAssuranceSaysWhetherAValidatorIsDeclared(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})

	f.b.state.Load().inner.Stages = noValidatorStages{}
	got, err := f.b.BackupSetAssurance(ctx, "production/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidatorDeclared {
		t.Error("a set whose recovery-test directory holds nothing reported a validator as declared")
	}

	// The control: the same set with a script in place says so.
	f.b.state.Load().inner.Stages = &stubStages{script: "pg-check.local.sh"}
	got, err = f.b.BackupSetAssurance(ctx, "production/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ValidatorDeclared {
		t.Error("a set with a validator script in place reported none declared")
	}
}

func TestIssue1108_ADeclaredValidatorThatIsMissingStillCountsAsDeclared(t *testing.T) {
	// The test then ends could_not_complete with its reason, not not_configured,
	// so the page must not call it undeclared.
	f := newRecoveryFixture(t, fixtureOpts{cfg: func(c *config.Config) {
		requireRecoveryTest(c)
		c.Sources[0].BackupSets[0].Assurance.RecoveryTest.ValidatorID = "pg-check"
	}})
	f.b.state.Load().inner.Stages = &stubStages{script: "some-other.local.sh"}
	got, err := f.b.BackupSetAssurance(context.Background(), "production/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ValidatorDeclared {
		t.Error("a validator_id that names a missing script reported none declared")
	}
}
