package app

import (
	"context"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
)

// The re-review of #1551 (B3): sets run one after another in one cycle,
// with their transfers inside each pass, so a set polled every 15 minutes
// can easily be reached every 40 when another set is pulling something
// big. Every one of those passes runs discovery, so the file has been
// watched the whole time, and 30 hours of it still being denied has to
// warn. A record that went stale on wall-clock time would reset on every
// slow cycle and never warn at all.
func TestASetReachedLaterThanItsPollIntervalStillWarns_1541(t *testing.T) {
	tr := &unpublishedTransport{fakeTransport: newFakeTransport(), denied: map[string]bool{"gitea-dump.dump": true}}
	tr.put("gitea-dump.dump", "root-only", epoch.Unix())

	bs := testBackupSet(t, t.TempDir())
	bs.StaleAfter = mustParseDuration(t, "24h")
	cfg := testConfig(t, testSource("production", bs))
	cfg.PollInterval = config.Duration(15 * time.Minute)
	svc := New(cfg, openJournal(t), tr, nil)

	for at := time.Duration(0); at <= 30*time.Hour; at += 40 * time.Minute {
		svc.Now = fixedNow(epoch.Add(at))
		svc.RunCycle(context.Background())
	}

	got := onlySet(t, svc)
	if w := pendingWarning(t, got); w == "" {
		t.Errorf("30 hours of passes 40 minutes apart, every one of them reaching discovery and finding the dump denied, did not warn (count %d)", pendingCount(t, got))
	}
}
