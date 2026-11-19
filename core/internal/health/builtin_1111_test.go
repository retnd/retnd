package health

import (
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// #1111: a built-in structure pass proves a file decodes. It is never the
// operator's validator accepting it, so mode required stays unmet.

func builtinPassRecord(now time.Time, id string) state.Record {
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	r := recoveryRecord(now, state.RecoveryPassed, &past, &future)
	r.Placements[0].RecoveryValidatorID = id
	return r
}

func TestIssue1111_ABuiltinPassNeverSatisfiesARequiredRecoveryTest(t *testing.T) {
	now := time.Now().UTC()
	for name, tc := range map[string]struct {
		id   string
		want State
	}{
		"the operator's validator passed":           {"10-check.local.sh", Healthy},
		"no id recorded (an older pass)":            {"", Healthy},
		"the built-in structure check passed":       {"retnd-builtin-structure", Degraded},
		"any id in the reserved prefix is the same": {"retnd-future-check", Degraded},
	} {
		t.Run(name, func(t *testing.T) {
			got := ComputeBackupSetHealth(testSet, []state.Record{builtinPassRecord(now, tc.id)}, nil, PlacementEvidence{}, day, BackupSetInputs{RecoveryTestRequired: true}, now)
			if got.State != tc.want {
				t.Errorf("State = %s (%s), want %s", got.State, got.Reason, tc.want)
			}
		})
	}
}

func TestIssue1111_ABuiltinPassDegradesNothingWhenNoRecoveryTestIsRequired(t *testing.T) {
	now := time.Now().UTC()
	got := ComputeBackupSetHealth(testSet, []state.Record{builtinPassRecord(now, "retnd-builtin-structure")}, nil, PlacementEvidence{}, day, BackupSetInputs{}, now)
	if got.State != Healthy {
		t.Errorf("State = %s (%s): a built-in pass on a set that requires nothing changed its verdict", got.State, got.Reason)
	}
}

// The existing path for a set with nothing declared is untouched.
func TestIssue1111_NotConfiguredStillReadsAsNoValidatorIsConfigured(t *testing.T) {
	now := time.Now().UTC()
	got := ComputeBackupSetHealth(testSet, []state.Record{recoveryRecord(now, state.RecoveryNotConfigured, nil, nil)}, nil, PlacementEvidence{}, day, BackupSetInputs{RecoveryTestRequired: true}, now)
	if got.State != Degraded || !strings.Contains(got.Reason, "no validator is configured") {
		t.Errorf("State = %s, reason = %q", got.State, got.Reason)
	}
}
