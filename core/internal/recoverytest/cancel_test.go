package recoverytest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// cancellingValidator is the host runner as it behaves when the context ends
// under a running script: it kills the group and reports the script as
// canceled, with no error. The runner answers; it does not fail.
type cancellingValidator struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancellingValidator) Configured(context.Context, model.BackupSetID) (bool, error) {
	return true, nil
}

func (c *cancellingValidator) Validate(context.Context, ValidatorInput) (ValidatorOutcome, error) {
	c.calls++
	c.cancel()
	zero := 0
	return ValidatorOutcome{ID: "10-check.local.sh", Hash: strings.Repeat("ab", 32), Disposition: "canceled", Termination: "confirmed", ExitCode: &zero}, nil
}

// EPIC Q, #980: an operator's cancel is not a verdict. The runner reports a
// cancelled script as an outcome and not as an error, so unless the test
// itself looks at its context the copy would be recorded as FAILED, with
// "the validator exited 0" as the reason: a cancel turned into an accusation
// against a copy nobody finished testing.
func TestACancelledValidatorIsNotRecordedAsAFailureOfTheCopy(t *testing.T) {
	j, a, rc := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	v := &cancellingValidator{cancel: cancel}

	_, err := Run(ctx, Deps{Journal: j, Recoverer: rc, Validator: v, Now: func() time.Time { return testNow }}, setID(t), cfgFor(), Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want an error wrapping context.Canceled: a cancelled test has no result", err)
	}
	if v.calls != 1 {
		t.Fatalf("the validator was called %d times", v.calls)
	}
	if p := copyOf(t, j, a, state.MediumLocal); p.RecoveryState == state.RecoveryFailed || p.RecoveryState == state.RecoveryPassed {
		t.Errorf("the copy was recorded %q by a test that was cancelled", p.RecoveryState)
	}
	if rc.removed != 1 {
		t.Errorf("the staged copy was removed %d times, want 1", rc.removed)
	}
}
