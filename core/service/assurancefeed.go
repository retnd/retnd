package service

import (
	"context"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
)

// Scrub outcomes for one copy.
const (
	ScrubPass    = "pass"
	ScrubFail    = "fail"
	ScrubRefused = "refused"
	ScrubUnread  = "unread"
)

// ScrubCopyResult is what a scrub learned about one copy.
type ScrubCopyResult struct {
	Artifact  string
	Medium    string
	Outcome   string
	Reason    string
	BytesRead int64
}

// EmitScrubPass writes one scrub pass to the activity feed under the volume
// policy in assurance.go: a start line, one line per non-passing copy up to
// MaxScrubCopyRows, and an end line that states a Result and counts every
// copy, including the ones that were not given a line of their own.
//
// The Result is success only when every copy passed. A copy refused for cost
// or unreadable makes the pass a warn, and a failed copy makes it an error,
// so no surface can draw a pass with a hole in it as a tick.
func EmitScrubPass(ctx context.Context, logger *obs.Logger, runID, backupSet string, results []ScrubCopyResult, took time.Duration) obs.ScrubTotals {
	logger.ScrubStart(ctx, runID, backupSet, len(results))

	var t obs.ScrubTotals
	t.Checked = len(results)
	t.Duration = took
	rows := 0
	for _, r := range results {
		t.BytesRead += r.BytesRead
		switch r.Outcome {
		case ScrubPass:
			t.Passed++
			continue
		case ScrubFail:
			t.Failed++
		case ScrubRefused:
			t.Refused++
		default:
			t.Unread++
		}
		if rows >= MaxScrubCopyRows {
			t.Truncated++
			continue
		}
		rows++
		logger.ScrubCopy(ctx, runID, backupSet, r.Artifact, r.Medium, r.Outcome, r.Reason)
	}

	// A pass that looked at nothing states no verdict: nothing was checked,
	// so nothing passed, and a tick would be the "green because nobody
	// looked" this epic exists to prevent.
	result := obs.ResultSuccess
	switch {
	case t.Checked == 0:
		result = obs.ResultInfo
	case t.Failed > 0:
		result = obs.ResultError
	case t.Refused > 0 || t.Unread > 0:
		result = obs.ResultWarn
	}
	logger.ScrubEnd(ctx, runID, backupSet, t, result)
	return t
}
