package app

import (
	"sort"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/discovery"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/model"
)

// Issue #1541: a source file that is complete but not yet readable waits in
// discovery's Pending instead of being journaled and failed. Waiting has no
// deadline of its own, so this is what keeps it from hiding: the first time
// each such file was seen, per set, so health can say how long a producer
// has been sitting on a finished file and warn once that passes the set's
// stale_after.
//
// It lives in memory rather than in a table, like lastPoll. A process
// restart starts the clock again, which delays a warning by at most one
// stale window and never invents one. A `retnd status` attached to a
// running engine reads the engine's numbers over the API (cmd/retnd's
// status), so the short-lived process not having run discovery itself does
// not blank the line.

// unreadableSources is the per-set first-seen record. It has its own lock
// because the cycle writes it while the alerting goroutine's health report
// reads it, and a reload shares the pointer between two Services.
type unreadableSources struct {
	mu    sync.Mutex
	bySet map[model.BackupSetID]map[string]time.Time
}

// observe replaces set's record with what this discovery pass found
// unreadable, keeping the earlier first-seen time for a file still there.
// A file the pass admitted, or that has gone, drops out, which is the
// whole of how the warning clears.
func (u *unreadableSources) observe(set model.BackupSetID, res discovery.Result, now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	prev := u.bySet[set]
	next := make(map[string]time.Time)
	for _, p := range res.Pending {
		if !p.Unreadable {
			continue
		}
		if seen, ok := prev[p.RemotePath]; ok {
			next[p.RemotePath] = seen
		} else {
			next[p.RemotePath] = now
		}
	}
	if u.bySet == nil {
		u.bySet = make(map[model.BackupSetID]map[string]time.Time)
	}
	if len(next) == 0 {
		delete(u.bySet, set)
		return
	}
	u.bySet[set] = next
}

// pending is set's record as health reads it, oldest first.
func (u *unreadableSources) pending(set model.BackupSetID) []health.PendingUnreadable {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]health.PendingUnreadable, 0, len(u.bySet[set]))
	for path, seen := range u.bySet[set] {
		out = append(out, health.PendingUnreadable{RemotePath: path, FirstSeen: seen})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.Before(out[j].FirstSeen)
		}
		return out[i].RemotePath < out[j].RemotePath
	})
	return out
}

// unreadableState is this Service's record, created on first use, for the
// reason pollScheduleState gives: every test double is a struct literal.
func (s *Service) unreadableState() *unreadableSources {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unreadable == nil {
		s.unreadable = &unreadableSources{}
	}
	return s.unreadable
}

// AdoptUnreadableSources carries the first-seen record from the Service a
// configuration reload is replacing, sharing it rather than copying it for
// the reason AdoptPollSchedule shares the schedule: the old Service's cycle
// keeps running until the swap, and what it observes must not be lost.
func (s *Service) AdoptUnreadableSources(prev *Service) {
	if prev == nil {
		return
	}
	shared := prev.unreadableState()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unreadable = shared
}
