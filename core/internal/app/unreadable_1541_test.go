package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1541 through the whole cycle: the cicd-pipeline/forge set, a good
// backup every morning and one gitea dump the producer has not published.
// The dump waits, the set stays HEALTHY, a warning naming the dump appears
// once it has waited longer than stale_after, and it clears when the dump
// is published and admitted.

// unpublishedTransport is the package's fakeTransport plus the readability
// probe's open, which refuses every path in denied the way a root-only file
// refuses the backup account.
type unpublishedTransport struct {
	*fakeTransport
	denied map[string]bool
}

func (u *unpublishedTransport) OpenSourceStream(_ context.Context, _ transport.Source, remotePath string) (io.ReadCloser, error) {
	if u.denied[remotePath] {
		return nil, transport.NewError(transport.PermissionDenied, "open_source_stream", os.ErrPermission)
	}
	obj, ok := u.objects[remotePath]
	if !ok {
		return nil, transport.NewError(transport.NotFound, "open_source_stream", errors.New("not found"))
	}
	return io.NopCloser(bytes.NewReader(obj.data)), nil
}

// CopyToLocal is refused for a denied path too, because the transfer's open
// is the same open. Without this the fake would copy a file the probe was
// told it cannot read, and the pre-fix FAILED would never be reproduced.
func (u *unpublishedTransport) CopyToLocal(ctx context.Context, source transport.Source, remotePath, localPartialPath string) (transport.TransferResult, error) {
	if u.denied[remotePath] {
		return transport.TransferResult{}, transport.NewError(transport.PermissionDenied, "copy_to_local", os.ErrPermission)
	}
	return u.fakeTransport.CopyToLocal(ctx, source, remotePath, localPartialPath)
}

// The #1541 fields are read by name, the way #1528's retire tests read
// RetiredCount, so this file compiles on the tree before the fix and goes red
// there on the behaviour rather than on a build error.
func pendingCount(t *testing.T, h health.BackupSetHealth) int {
	t.Helper()
	f := reflect.ValueOf(h).FieldByName("PendingUnreadableCount")
	if !f.IsValid() {
		t.Fatalf("BackupSetHealth has no PendingUnreadableCount: a complete but unreadable source file is invisible to health")
	}
	return int(f.Int())
}

func pendingString(t *testing.T, h health.BackupSetHealth, name string) string {
	t.Helper()
	f := reflect.ValueOf(h).FieldByName(name)
	if !f.IsValid() {
		t.Fatalf("BackupSetHealth has no %s", name)
	}
	return f.String()
}

func pendingWarning(t *testing.T, h health.BackupSetHealth) string {
	t.Helper()
	return pendingString(t, h, "PendingUnreadableWarning")
}

func pendingOldestPath(t *testing.T, h health.BackupSetHealth) string {
	t.Helper()
	return pendingString(t, h, "PendingUnreadableOldestPath")
}

// adoptUnreadable calls Service.AdoptUnreadableSources by name, for the same
// reason.
func adoptUnreadable(t *testing.T, next, prev *Service) {
	t.Helper()
	m := reflect.ValueOf(next).MethodByName("AdoptUnreadableSources")
	if !m.IsValid() {
		t.Fatalf("Service has no AdoptUnreadableSources: a configuration reload would restart the clock an unreadable file's warning runs on")
	}
	m.Call([]reflect.Value{reflect.ValueOf(prev)})
}

func onlySet(t *testing.T, svc *Service) health.BackupSetHealth {
	t.Helper()
	report, err := svc.BuildHealthReport(context.Background(), VersionInfo{})
	if err != nil {
		t.Fatalf("BuildHealthReport: %v", err)
	}
	if len(report.BackupSets) != 1 {
		t.Fatalf("BackupSets = %+v, want exactly one", report.BackupSets)
	}
	return report.BackupSets[0]
}

func TestAnUnreadableSourceFileWarnsAfterStaleAfterAndNeverFailsTheSet_1541(t *testing.T) {
	bs := testBackupSet(t, t.TempDir())
	bs.StaleAfter = mustParseDuration(t, "24h")

	tr := &unpublishedTransport{fakeTransport: newFakeTransport(), denied: map[string]bool{"gitea-dump.dump": true}}
	tr.put("morning-1.dump", "a good backup", epoch.Unix())
	tr.put("gitea-dump.dump", "root-only until published", epoch.Unix())

	journal := openJournal(t)
	svc := New(testConfig(t, testSource("production", bs)), journal, tr, nil)
	svc.Now = fixedNow(epoch)
	svc.RunCycle(context.Background())

	got := onlySet(t, svc)
	if got.Failures != 0 {
		t.Errorf("Failures = %d (%s), want 0: an unpublished file must never be journaled, so it can never be FAILED", got.Failures, got.Reason)
	}
	if pendingCount(t, got) != 1 || pendingOldestPath(t, got) != "gitea-dump.dump" {
		t.Fatalf("after the first cycle: count %d, oldest %q, want the one unpublished dump", pendingCount(t, got), pendingOldestPath(t, got))
	}
	if w := pendingWarning(t, got); w != "" {
		t.Errorf("a dump unreadable for no time at all warned: %q", w)
	}

	// Thirty hours on, the next morning's backup lands and the dump is
	// still root-only.
	later := epoch.Add(30 * time.Hour)
	tr.put("morning-2.dump", "the next good backup", later.Unix())
	svc.Now = fixedNow(later)
	svc.RunCycle(context.Background())

	got = onlySet(t, svc)
	if got.State != health.Healthy {
		t.Errorf("State = %s (%s), want %s: the set has a fresh good backup, and a dump waiting for its producer is not a failed backup", got.State, got.Reason, health.Healthy)
	}
	want := "1 source file complete but unreadable for 30h, oldest gitea-dump.dump; the producer has not published it"
	if w := pendingWarning(t, got); w != want {
		t.Errorf("PendingUnreadableWarning = %q, want %q", w, want)
	}
	if got.Failures != 0 {
		t.Errorf("Failures = %d after 30h, want 0", got.Failures)
	}

	// The producer publishes. The dump is admitted and the warning clears.
	delete(tr.denied, "gitea-dump.dump")
	svc.Now = fixedNow(later.Add(time.Hour))
	svc.RunCycle(context.Background())

	got = onlySet(t, svc)
	if pendingCount(t, got) != 0 || pendingWarning(t, got) != "" {
		t.Errorf("after publish: count %d, warning %q, want both cleared", pendingCount(t, got), pendingWarning(t, got))
	}
	records, err := journal.ListByBackupSet(context.Background(), bs.ID)
	if err != nil {
		t.Fatalf("ListByBackupSet: %v", err)
	}
	var sawDump bool
	for _, r := range records {
		if r.Artifact.Name == "gitea-dump.dump" {
			sawDump = true
		}
		if r.State == "FAILED" {
			t.Errorf("%s is FAILED; nothing in this scenario was ever unreadable after admission", r.Artifact)
		}
	}
	if !sawDump {
		t.Error("the published dump was never journaled")
	}
}

// A configuration reload builds a new Service. It has to keep the first-seen
// time, or every settings save would restart the clock the warning runs on.
func TestAReloadKeepsHowLongAFileHasBeenUnreadable_1541(t *testing.T) {
	bs := testBackupSet(t, t.TempDir())
	bs.StaleAfter = mustParseDuration(t, "24h")
	tr := &unpublishedTransport{fakeTransport: newFakeTransport(), denied: map[string]bool{"gitea-dump.dump": true}}
	tr.put("gitea-dump.dump", "root-only", epoch.Unix())

	cfg := testConfig(t, testSource("production", bs))
	journal := openJournal(t)
	before := New(cfg, journal, tr, nil)
	before.Now = fixedNow(epoch)
	before.RunCycle(context.Background())

	after := New(cfg, journal, tr, nil)
	adoptUnreadable(t, after, before)
	after.Now = fixedNow(epoch.Add(25 * time.Hour))
	after.RunCycle(context.Background())

	got := onlySet(t, after)
	if w := pendingWarning(t, got); !strings.Contains(w, "unreadable for 25h") {
		t.Errorf("after a reload: warning %q, want it to count from the first sighting 25h ago", w)
	}
}
