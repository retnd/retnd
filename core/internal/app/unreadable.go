package app

import (
	"sort"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/config"
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
//
// The record only ever describes a set that is configured and being looked
// at (the review of #1551). A reload drops the sets the new configuration
// no longer names, so a set re-added under the same id starts clean. And
// each set remembers when a pass last ran discovery on it: once that is
// older than unreadableFreshFor, nothing is reported, and the next pass
// that does look starts the clock again. Otherwise a disabled set, or one
// whose passes fail before discovery, would keep ageing a warning that
// blames the producer for something no pass has checked.

// unreadableSet is one set's record: the unreadable files with when each
// was first seen, and when a pass last ran discovery on the set.
type unreadableSet struct {
	files   map[string]time.Time
	checked time.Time
}

// unreadableSources is the per-set record. It has its own lock because the
// cycle writes it while the alerting goroutine's health report reads it,
// and a reload shares the pointer between two Services.
type unreadableSources struct {
	mu    sync.Mutex
	bySet map[model.BackupSetID]*unreadableSet
}

// unreadableFreshFor is how long after the last pass that ran discovery a
// set's record is still reported: two of its poll intervals, so one late
// or failed pass does not blank it, with a floor of two of the shortest
// interval a configuration can set, for a Service built without one.
func (s *Service) unreadableFreshFor(bs config.BackupSet) time.Duration {
	return max(2*s.Config.EffectivePollInterval(bs), 2*config.MinPollInterval)
}

// observe replaces set's record with what this discovery pass found
// unreadable. A file still there keeps its first-seen time, but only when
// the previous pass that looked is itself within fresh: after a gap nobody
// was watching, the clock starts again at this pass. A file the pass
// admitted, or that has gone, drops out, which is the whole of how the
// warning clears.
func (u *unreadableSources) observe(set model.BackupSetID, res discovery.Result, now time.Time, fresh time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	var carried map[string]time.Time
	if prev := u.bySet[set]; prev != nil && now.Sub(prev.checked) <= fresh {
		carried = prev.files
	}
	next := make(map[string]time.Time)
	for _, p := range res.Pending {
		if !p.Unreadable {
			continue
		}
		if seen, ok := carried[p.RemotePath]; ok {
			next[p.RemotePath] = seen
		} else {
			next[p.RemotePath] = now
		}
	}
	if u.bySet == nil {
		u.bySet = make(map[model.BackupSetID]*unreadableSet)
	}
	u.bySet[set] = &unreadableSet{files: next, checked: now}
}

// pending is set's record as health reads it, oldest first, or nothing
// when no pass has run discovery on the set within fresh.
func (u *unreadableSources) pending(set model.BackupSetID, now time.Time, fresh time.Duration) []health.PendingUnreadable {
	u.mu.Lock()
	defer u.mu.Unlock()
	rec := u.bySet[set]
	if rec == nil || now.Sub(rec.checked) > fresh {
		return nil
	}
	out := make([]health.PendingUnreadable, 0, len(rec.files))
	for path, seen := range rec.files {
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

// keepOnly drops every set the configuration in force no longer names.
func (u *unreadableSources) keepOnly(cfg *config.Config) {
	u.mu.Lock()
	defer u.mu.Unlock()
	configured := make(map[model.BackupSetID]bool)
	if cfg != nil {
		for _, src := range cfg.Sources {
			for _, bs := range src.BackupSets {
				configured[bs.ID] = true
			}
		}
	}
	for set := range u.bySet {
		if !configured[set] {
			delete(u.bySet, set)
		}
	}
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
// keeps running until the swap, and what it observes must not be lost. The
// sets this Service's configuration no longer names are dropped from it.
func (s *Service) AdoptUnreadableSources(prev *Service) {
	if prev == nil {
		return
	}
	shared := prev.unreadableState()
	shared.keepOnly(s.Config)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unreadable = shared
}
