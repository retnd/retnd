package agenttools

import (
	"slices"
	"testing"
)

// Issue #1594: accept_unverified_copy is an acknowledgement a person gives,
// like the two #1528 added, so the retire_backup row has to name it among the
// fields an agent may not send. The proposer and the replay guard both read
// this row.
func TestAnAgentMayNotAcknowledgeAnUnverifiedCopy_1594(t *testing.T) {
	row, ok := Lookup("retire_backup")
	if !ok {
		t.Fatal("there is no retire_backup tool")
	}
	for _, f := range []string{"accept_source_present", "no_successor", "accept_unverified_copy"} {
		if !slices.Contains(row.AgentMayNotSet, f) {
			t.Errorf("retire_backup's AgentMayNotSet is %v, missing %q", row.AgentMayNotSet, f)
		}
	}
}
