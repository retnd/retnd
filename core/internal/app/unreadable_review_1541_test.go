package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
)

// errListingRefused is a listing failure that is not retried, so each pass
// fails once, before discovery, and moves on.
var errListingRefused = errors.New("the source refused the listing")

// The review of #1551: the first-seen record has to describe sets that are
// configured and being looked at. A removed set must not leave a clock
// behind for a set re-added under the same id, and a set nobody is
// discovering (disabled, or whose passes fail before discovery) must not
// keep ageing a warning that blames its producer for something no pass has
// checked.

func unpublishedForge(t *testing.T) (*unpublishedTransport, func(disabled bool) *Service) {
	t.Helper()
	tr := &unpublishedTransport{fakeTransport: newFakeTransport(), denied: map[string]bool{"gitea-dump.dump": true}}
	tr.put("gitea-dump.dump", "root-only", epoch.Unix())
	journal := openJournal(t)
	local := t.TempDir()
	build := func(disabled bool) *Service {
		bs := testBackupSet(t, local)
		bs.StaleAfter = mustParseDuration(t, "24h")
		bs.Disabled = disabled
		// Polled hourly, so passes an hour apart carry a file's first-seen
		// time and a longer silence does not.
		cfg := testConfig(t, testSource("production", bs))
		cfg.PollInterval = config.Duration(time.Hour)
		return New(cfg, journal, tr, nil)
	}
	return tr, build
}

func TestARemovedSetComesBackWithNoUnreadableRecord_1541(t *testing.T) {
	_, build := unpublishedForge(t)
	before := build(false)
	before.Now = fixedNow(epoch)
	before.RunCycle(context.Background())
	if n := pendingCount(t, onlySet(t, before)); n != 1 {
		t.Fatalf("setup: %d unreadable files recorded, want 1", n)
	}

	// The set is removed from the configuration...
	removed := New(testConfig(t), before.Journal, before.Transport, nil)
	adoptUnreadable(t, removed, before)
	removed.Now = fixedNow(epoch.Add(time.Hour))

	// ...and three weeks later added back under the same id. Nothing has
	// looked at its source since, so it has no record to report.
	back := build(false)
	adoptUnreadable(t, back, removed)
	back.Now = fixedNow(epoch.Add(21 * 24 * time.Hour))
	got := onlySet(t, back)
	if n := pendingCount(t, got); n != 0 {
		t.Errorf("a re-added set reports %d unreadable files from before it was removed (warning %q), want none until a pass looks", n, pendingWarning(t, got))
	}
}

func TestADisabledSetDoesNotWarnAboutAnOldRecord_1541(t *testing.T) {
	_, build := unpublishedForge(t)
	enabled := build(false)
	enabled.Now = fixedNow(epoch)
	enabled.RunCycle(context.Background())

	disabled := build(true)
	adoptUnreadable(t, disabled, enabled)
	disabled.Now = fixedNow(epoch.Add(30 * time.Hour))
	disabled.RunCycle(context.Background())

	got := onlySet(t, disabled)
	if w := pendingWarning(t, got); w != "" {
		t.Errorf("a disabled set warns %q; no pass has looked at its source in 30h, so the producer cannot be blamed", w)
	}
	if n := pendingCount(t, got); n != 0 {
		t.Errorf("a disabled set reports %d unreadable files, want none: nothing is discovering it", n)
	}
}

func TestASetWhosePassesNoLongerReachDiscoveryDoesNotWarn_1541(t *testing.T) {
	// The control first: the same 30 hours of hourly passes that DO reach
	// discovery warn, so the silence below is the failing passes' doing.
	_, buildControl := unpublishedForge(t)
	control := buildControl(false)
	for h := 0; h <= 30; h++ {
		control.Now = fixedNow(epoch.Add(time.Duration(h) * time.Hour))
		control.RunCycle(context.Background())
	}
	if w := pendingWarning(t, onlySet(t, control)); w == "" {
		t.Fatalf("control: 30 hours of passes that reach discovery did not warn, so this test cannot tell anything apart")
	}

	tr, build := unpublishedForge(t)
	svc := build(false)
	svc.Now = fixedNow(epoch)
	svc.RunCycle(context.Background())

	// From here on every pass fails before discovery (the listing is
	// refused), for 30 hours.
	tr.failForSourceID = svc.Config.Sources[0].BackupSets[0].ID.String()
	tr.failErr = errListingRefused
	for h := 1; h <= 30; h++ {
		svc.Now = fixedNow(epoch.Add(time.Duration(h) * time.Hour))
		svc.RunCycle(context.Background())
	}

	got := onlySet(t, svc)
	if w := pendingWarning(t, got); w != "" {
		t.Errorf("a set whose passes have not reached discovery for 30h warns %q; that blames the producer for a connectivity failure", w)
	}
}

// A set disabled for three weeks and enabled again, with the file still
// unreadable, counts from the pass that looked again, not from before the
// gap nobody was watching.
func TestASetEnabledAgainCountsFromThePassThatLookedAgain_1541(t *testing.T) {
	_, build := unpublishedForge(t)
	first := build(false)
	first.Now = fixedNow(epoch)
	first.RunCycle(context.Background())

	off := build(true)
	adoptUnreadable(t, off, first)
	off.Now = fixedNow(epoch.Add(time.Hour))
	off.RunCycle(context.Background())

	on := build(false)
	adoptUnreadable(t, on, off)
	on.Now = fixedNow(epoch.Add(21 * 24 * time.Hour))
	on.RunCycle(context.Background())

	got := onlySet(t, on)
	if n := pendingCount(t, got); n != 1 {
		t.Fatalf("after enabling again: %d unreadable files, want the one still unpublished", n)
	}
	if w := pendingWarning(t, got); w != "" {
		t.Errorf("after three weeks disabled the warning is %q; it counts time nobody was looking", w)
	}
}
