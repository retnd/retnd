package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
)

// The third review of #1551 (B4): the workflow lifecycle can refuse a set's
// pass before the pass runs at all (a plan that does not build, a bypass
// nobody authorised, a before hook that says no). Such a pass never reaches
// discovery, so it says nothing current about the source, and 30 hours of
// them must not grow a warning that blames the producer.

// refusingWorkflow is a workflow lifecycle that, while refuse is set, turns
// every pass away without calling it, the way a before hook on a runner
// whose socket has gone does.
type refusingWorkflow struct{ refuse bool }

func (w *refusingWorkflow) AroundBackupSet(ctx context.Context, _ config.BackupSet, backup func(context.Context) error) error {
	if w.refuse {
		return errors.New("before hook: the runner is not answering")
	}
	return backup(ctx)
}

func forgeBehindAWorkflow(t *testing.T) (*Service, *refusingWorkflow) {
	t.Helper()
	tr := &unpublishedTransport{fakeTransport: newFakeTransport(), denied: map[string]bool{"gitea-dump.dump": true}}
	tr.put("gitea-dump.dump", "root-only", epoch.Unix())
	bs := testBackupSet(t, t.TempDir())
	bs.StaleAfter = mustParseDuration(t, "24h")
	cfg := testConfig(t, testSource("production", bs))
	cfg.PollInterval = config.Duration(time.Hour)
	svc := New(cfg, openJournal(t), tr, nil)
	wf := &refusingWorkflow{}
	svc.Workflow = wf
	return svc, wf
}

func TestAPassTheWorkflowRefusesDoesNotAgeAWarning_1541(t *testing.T) {
	// Control: the same 30 hours with the workflow letting every pass run
	// does warn, so the silence below is the refusals' doing.
	control, _ := forgeBehindAWorkflow(t)
	for h := 0; h <= 30; h++ {
		control.Now = fixedNow(epoch.Add(time.Duration(h) * time.Hour))
		control.RunCycle(context.Background())
	}
	if w := pendingWarning(t, onlySet(t, control)); w == "" {
		t.Fatalf("control: 30 hours of passes the workflow let run did not warn, so this test cannot tell anything apart")
	}

	svc, wf := forgeBehindAWorkflow(t)
	svc.Now = fixedNow(epoch)
	svc.RunCycle(context.Background())
	if n := pendingCount(t, onlySet(t, svc)); n != 1 {
		t.Fatalf("setup: the first pass recorded %d unreadable files, want 1", n)
	}

	wf.refuse = true
	for h := 1; h <= 30; h++ {
		svc.Now = fixedNow(epoch.Add(time.Duration(h) * time.Hour))
		svc.RunCycle(context.Background())
	}

	got := onlySet(t, svc)
	if w := pendingWarning(t, got); w != "" {
		t.Errorf("30 hours of passes the workflow refused before they ran still warn %q; no pass has looked at the source", w)
	}
	if n := pendingCount(t, got); n != 0 {
		t.Errorf("after passes the workflow refused, %d unreadable files are still reported, want none", n)
	}
}
