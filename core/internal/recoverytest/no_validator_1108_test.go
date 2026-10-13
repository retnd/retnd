package recoverytest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// #1108: a recovery test on a set with no validator printed "recovery stage:
// Complete" for a run that tested nothing. Complete is the last stage of a run
// that reached a verdict; a run that stopped at the door never got there, and
// a failed recovery already never prints it.

func stagesOf(t *testing.T, j *state.Journal, rc *fileRecoverer, v Validator) ([]string, Result) {
	t.Helper()
	var got []string
	res, err := Run(context.Background(), Deps{
		Journal: j, Recoverer: rc, Validator: v, Now: func() time.Time { return testNow },
		Stage: func(id string) { got = append(got, id) },
	}, setID(t), cfgFor(), Request{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return got, res
}

func TestIssue1108_ARunThatStoppedAtTheDoorNeverReportsTheCompleteStage(t *testing.T) {
	for name, tc := range map[string]struct {
		validator Validator
		wantState string
	}{
		"no validator declared":       {nil, state.RecoveryNotConfigured},
		"validator directory absent":  {&fakeValidator{configured: false}, state.RecoveryNotConfigured},
		"validator could not be read": {&fakeValidator{cerr: errors.New("the script directory is unreadable")}, state.RecoveryCouldNotComplete},
	} {
		t.Run(name, func(t *testing.T) {
			j, _, rc := newEnv(t)
			got, res := stagesOf(t, j, rc, tc.validator)
			if res.State != tc.wantState {
				t.Fatalf("state = %q, want %q (the refusal itself must stay)", res.State, tc.wantState)
			}
			for _, s := range got {
				if s == "complete" {
					t.Errorf("a run that ended %q reported the complete stage; stages = %v", res.State, got)
				}
			}
			if strings.Join(got, ",") != "resolving_copy" {
				t.Errorf("stages = %v, want only [resolving_copy]: the copy was resolved and then nothing ran", got)
			}
		})
	}
}

// The control: a pass still ends on Complete, so the fix cannot be "never
// report it".
func TestIssue1108_AControlAPassStillEndsOnTheCompleteStage(t *testing.T) {
	j, _, rc := newEnv(t)
	got, res := stagesOf(t, j, rc, accepting())
	if res.State != state.RecoveryPassed {
		t.Fatalf("state = %q", res.State)
	}
	if len(got) == 0 || got[len(got)-1] != "complete" {
		t.Errorf("stages = %v, want a pass to end on complete", got)
	}
}
