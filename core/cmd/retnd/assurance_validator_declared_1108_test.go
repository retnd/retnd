package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1108: a set's assurance answer always says whether a validator is
// declared, false included, and the deployment-wide answer, which has no set
// to look in, never says it.
func TestIssue1108_ValidatorDeclaredIsOnASetsAnswerEvenWhenFalseAndNeverOnTheDeploymentsOwn(t *testing.T) {
	for _, declared := range []bool{false, true} {
		b, err := json.Marshal(backupSetAssuranceWire(service.BackupSetAssurance{BackupSetID: "a/b", ValidatorDeclared: declared}))
		if err != nil {
			t.Fatal(err)
		}
		want := `"validator_declared":false`
		if declared {
			want = `"validator_declared":true`
		}
		if !strings.Contains(string(b), want) {
			t.Errorf("a set with ValidatorDeclared=%v answered %s, want it to contain %s", declared, b, want)
		}
		// and it survives the trip back through the CLI's own decoder
		var back struct {
			RecoveryTest struct {
				ValidatorDeclared *bool `json:"validator_declared"`
			} `json:"recovery_test"`
		}
		if err := json.Unmarshal(b, &back); err != nil || back.RecoveryTest.ValidatorDeclared == nil || *back.RecoveryTest.ValidatorDeclared != declared {
			t.Errorf("round trip lost validator_declared=%v: %s (%v)", declared, b, err)
		}
	}

	b, err := json.Marshal(assuranceSettingsWire(service.AssuranceSettings{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "validator_declared") {
		t.Errorf("the deployment-wide policy has no set to look in but answered %s", b)
	}
}
