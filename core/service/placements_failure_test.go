package service

import (
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/state"
)

// Panel finding C8 (#985): the Backups page rendered a copy whose scrub had
// FAILED as "Never run", because a failed scrub clears the class and the
// time and the boundary carried nothing else. The failure has to cross the
// boundary on the copy it is about: the state, when it last ran, the reason
// the journal stored, how many times in a row it has failed, and the
// recovery claim beside it.
func TestAPlacementCarriesItsOwnScrubFailureAndRecoveryClaim(t *testing.T) {
	checked := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	failed := time.Date(2026, 10, 9, 3, 0, 5, 0, time.UTC)
	tested := time.Date(2026, 10, 8, 4, 0, 0, 0, time.UTC)

	got := toServicePlacement(state.Placement{
		Medium: state.MediumLocal, Location: "/backups/a.dump", Status: state.PlacementActive,
		Assurance: state.Assurance{
			AssuranceState:        state.AssuranceFailed,
			LastCheckedAt:         &checked,
			LastFailureAt:         &failed,
			LastFailureReason:     "sha256 differs from the one recorded at ingest",
			ConsecutiveFailures:   3,
			RecoveryState:         state.RecoveryFailed,
			RecoveryTestedAt:      &tested,
			RecoveryFailureReason: "the validator rejected the bytes",
		},
	}, nil)

	if got.ScrubState != state.AssuranceFailed {
		t.Errorf("ScrubState = %q, want failed", got.ScrubState)
	}
	if !got.ScrubLastFailureAt.Equal(failed) {
		t.Errorf("ScrubLastFailureAt = %v, want %v", got.ScrubLastFailureAt, failed)
	}
	if got.ScrubLastFailureReason != "sha256 differs from the one recorded at ingest" {
		t.Errorf("ScrubLastFailureReason = %q", got.ScrubLastFailureReason)
	}
	if got.ScrubConsecutiveFailures != 3 {
		t.Errorf("ScrubConsecutiveFailures = %d, want 3", got.ScrubConsecutiveFailures)
	}
	if got.RecoveryState != state.RecoveryFailed {
		t.Errorf("RecoveryState = %q, want failed", got.RecoveryState)
	}
	if !got.RecoveryTestedAt.Equal(tested) {
		t.Errorf("RecoveryTestedAt = %v, want %v", got.RecoveryTestedAt, tested)
	}
	if got.RecoveryFailureReason != "the validator rejected the bytes" {
		t.Errorf("RecoveryFailureReason = %q", got.RecoveryFailureReason)
	}
}

// The zero record stays the zero value that cannot look good: a copy nobody
// has scrubbed or tested reports "unknown" for both, with no time and no
// reason, rather than an empty string a surface might read as fine.
func TestACopyNobodyHasCheckedReportsUnknownForBothClaims(t *testing.T) {
	got := toServicePlacement(state.Placement{Medium: state.MediumLocal, Status: state.PlacementActive}, nil)
	if got.ScrubState != state.AssuranceUnknown && got.ScrubState != "" {
		t.Errorf("ScrubState = %q, want unknown (or absent)", got.ScrubState)
	}
	if got.RecoveryState != state.RecoveryUnknown && got.RecoveryState != "" {
		t.Errorf("RecoveryState = %q, want unknown (or absent)", got.RecoveryState)
	}
	if !got.ScrubLastFailureAt.IsZero() || got.ScrubLastFailureReason != "" || got.ScrubConsecutiveFailures != 0 {
		t.Errorf("a copy that never failed carries failure fields: %+v", got)
	}
}
