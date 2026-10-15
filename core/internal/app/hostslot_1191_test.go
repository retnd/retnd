package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1191: a backup cycle's pass over an SFTP set takes the source host's
// write slot, so a recovery uploading onto that same machine never adds a
// third connection to the two a pass already holds.

type listCounting struct {
	*fakeTransport
	lists atomic.Int32
}

func (l *listCounting) List(ctx context.Context, src transport.Source) ([]transport.RemoteArtifact, error) {
	l.lists.Add(1)
	return l.fakeTransport.List(ctx, src)
}

func slotTestService(t *testing.T) (*Service, *listCounting, transport.Source) {
	t.Helper()
	bs := testBackupSet(t, t.TempDir())
	bs.Remote = config.Remote{Type: "sftp", Host: "slot-1191.example", Port: 22, User: "backupuser"}
	tr := &listCounting{fakeTransport: newFakeTransport()}
	tr.put("backup.dump", "payload", epoch.Unix())
	cfg := testConfig(t, testSource("production", bs))
	svc := New(cfg, openJournal(t), tr, nil)
	svc.Now = fixedNow(epoch)
	return svc, tr, sourceFor(cfg, cfg.Sources[0], bs)
}

func TestRunCycle_AnSFTPSetWaitsForItsHostsWriteSlot(t *testing.T) {
	svc, tr, src := slotTestService(t)
	release, err := transport.AcquireSourceHost(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan CycleReport, 1)
	go func() { done <- svc.RunCycle(context.Background()) }()

	time.Sleep(150 * time.Millisecond)
	if n := tr.lists.Load(); n != 0 {
		release()
		t.Fatalf("the pass listed the source %d times while a recovery held its host", n)
	}
	release()
	select {
	case rep := <-done:
		if tr.lists.Load() == 0 {
			t.Fatal("the pass never reached the source once the slot was free")
		}
		if len(rep.Sets) != 1 || rep.Sets[0].Err != nil {
			t.Fatalf("the pass after the wait: %+v", rep.Sets)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cycle never ran after the slot was given back")
	}
}

func TestRunCycle_AStoppedWaitForTheSlotTouchesNothing(t *testing.T) {
	svc, tr, src := slotTestService(t)
	release, err := transport.AcquireSourceHost(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	rep := svc.RunCycle(ctx)
	if tr.lists.Load() != 0 {
		t.Fatal("a pass that never got the slot listed the source")
	}
	if len(rep.Sets) == 1 && !errors.Is(rep.Sets[0].Err, context.DeadlineExceeded) {
		t.Fatalf("the set's error = %v, want the context's", rep.Sets[0].Err)
	}
}
