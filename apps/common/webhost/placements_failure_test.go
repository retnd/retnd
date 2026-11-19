package webhost

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// Panel finding C8 (#985): the wire has to say that a copy's scrub failed,
// why, how often, and what its recovery test last concluded. These are the
// names the contract declares for them (api/v1/openapi.json, schema
// Placement), and the unknown state is ABSENT, never an empty string.
func TestAPlacementOnTheWireCarriesTheFailureOfItsOwnScrub(t *testing.T) {
	failed := time.Date(2026, 10, 9, 3, 0, 5, 0, time.UTC)
	out := toPlacementResponses([]service.Placement{{
		Medium: "local", MediumType: "local", Location: "/b/a.dump", Access: "immediate", Status: "ACTIVE",
		ScrubState:               "failed",
		ScrubLastCheckedAt:       time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC),
		ScrubLastFailureAt:       failed,
		ScrubLastFailureReason:   "sha256 differs from the one recorded at ingest",
		ScrubConsecutiveFailures: 3,
		RecoveryState:            "failed",
		RecoveryTestedAt:         time.Date(2026, 10, 8, 4, 0, 0, 0, time.UTC),
		RecoveryFailureReason:    "the validator rejected the bytes",
	}})
	raw, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"assurance_state":         "failed",
		"last_failure_reason":     "sha256 differs from the one recorded at ingest",
		"consecutive_failures":    float64(3),
		"recovery_state":          "failed",
		"recovery_failure_reason": "the validator rejected the bytes",
		"last_failure_at":         "2026-10-09T03:00:05Z",
		"last_checked_at":         "2026-10-09T03:00:00Z",
		"recovery_tested_at":      "2026-10-08T04:00:00Z",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("wire %s = %v, want %v (body %s)", k, m[k], v, raw)
		}
	}
}

func TestAPlacementNobodyHasCheckedAddsNothingToTheWire(t *testing.T) {
	out := toPlacementResponses([]service.Placement{{Medium: "local", MediumType: "local", Location: "/b/a.dump", Access: "immediate", Status: "ACTIVE", ScrubState: "unknown", RecoveryState: "unknown"}})
	raw, _ := json.Marshal(out[0])
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"assurance_state", "last_failure_at", "last_failure_reason", "consecutive_failures", "recovery_state", "recovery_tested_at", "recovery_failure_reason", "last_checked_at"} {
		if _, there := m[k]; there {
			t.Errorf("a copy nobody checked serves %s: %s", k, raw)
		}
	}
}
