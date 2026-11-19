package app

import (
	"bytes"
	"context"
	"testing"

	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// #1200 (review): what a scrub now and a recovery test write to the feed so
// the Operation page and `retnd assurance show` can read them back by id.

// A scrub now is on the record the moment it starts, and it ends on the
// record even when it selected nothing, so "running" and "finished" are both
// answerable by its operation id.
func TestScrub_AScrubNowWritesAStartLineAndAnEndLineEvenWhenItSelectsNothing(t *testing.T) {
	ctx := context.Background()
	var stream bytes.Buffer
	bs := testBackupSet(t, t.TempDir())
	svc, _ := scrubService(t, scrubReval, bs)
	svc.Logger = obs.New(&stream, obs.LevelInfo)

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubByManual, Set: &bs.ID, OpID: "op-empty"})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Selected != 0 {
		t.Fatalf("selected = %d, want a set with nothing to scrub", sum.Selected)
	}
	start := eventsOf(t, &stream, obs.EventScrubStart)
	if len(start) != 1 || start[0]["op_id"] != "op-empty" {
		t.Fatalf("scrub_start lines = %v, want one carrying op-empty\n%s", start, stream.String())
	}
	pass := eventsOf(t, &stream, obs.EventScrubPass)
	if len(pass) != 1 || pass[0]["op_id"] != "op-empty" {
		t.Fatalf("scrub_pass lines = %v, want one carrying op-empty\n%s", pass, stream.String())
	}
}

// A scheduled pass has no operation, so it writes no start line, and a quiet
// tick still writes no pass line (the feed would otherwise fill with them).
func TestScrub_AScheduledQuietPassWritesNothingToTheFeed(t *testing.T) {
	var stream bytes.Buffer
	bs := testBackupSet(t, t.TempDir())
	svc, _ := scrubService(t, scrubReval, bs)
	svc.Logger = obs.New(&stream, obs.LevelInfo)
	if _, err := svc.Scrub(context.Background(), ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
		t.Fatal(err)
	}
	if n := len(eventsOf(t, &stream, obs.EventScrubStart)) + len(eventsOf(t, &stream, obs.EventScrubPass)); n != 0 {
		t.Fatalf("a quiet scheduled pass wrote %d lines, want none\n%s", n, stream.String())
	}
}

// Only a cancel request is a cancellation. A context that ends for any other
// reason (the service shutting down) is not, and says nothing of the kind.
func TestScrub_OnlyACancelRequestIsCancelled(t *testing.T) {
	for _, c := range []struct {
		name  string
		cause error
		want  bool
	}{
		{"cancel request", ErrOperationCancelled, true},
		{"shutdown", context.Canceled, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stream bytes.Buffer
			bs := testBackupSet(t, t.TempDir())
			svc, _ := scrubService(t, scrubReval, bs)
			svc.Logger = obs.New(&stream, obs.LevelInfo)
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(c.cause)

			if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubByManual, Set: &bs.ID, OpID: "op-c"}); err == nil {
				t.Fatal("a scrub whose context ended returned no error")
			}
			pass := eventsOf(t, &stream, obs.EventScrubPass)
			if len(pass) != 1 {
				t.Fatalf("scrub_pass lines = %v\n%s", pass, stream.String())
			}
			got, _ := pass[0]["cancelled"].(bool)
			if got != c.want {
				t.Errorf("cancelled = %v, want %v (line %v)", got, c.want, pass[0])
			}
		})
	}
}

// The same for a recovery test: its end line says cancelled=true for a
// cancel request and not for shutdown.
func TestRecoveryTest_OnlyACancelRequestIsCancelled(t *testing.T) {
	for _, c := range []struct {
		name  string
		cause error
		want  bool
	}{
		{"cancel request", ErrOperationCancelled, true},
		{"shutdown", context.Canceled, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stream bytes.Buffer
			svc, journal, bs := recoveryService(t, accepts())
			svc.Logger = obs.New(&stream, obs.LevelInfo)
			seedMovableArtifact(t, context.Background(), journal, bs, "rt.dump", retentionTestNow)
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(c.cause)

			_, _ = svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
			end := eventsOf(t, &stream, obs.EventRecoveryTestEnd)
			if len(end) != 1 {
				t.Fatalf("recovery_test_end lines = %v\n%s", end, stream.String())
			}
			got, _ := end[0]["cancelled"].(bool)
			if got != c.want {
				t.Errorf("cancelled = %v, want %v (line %v)", got, c.want, end[0])
			}
		})
	}
}
