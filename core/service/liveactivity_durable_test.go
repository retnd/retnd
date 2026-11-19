package service

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// The activity feed's events are durable (migration 0018, the owner's
// decision). These cases hold what that promises: a restart keeps the feed
// and the numbering, the write volume is bounded by what is stored and how
// long, the cycle is never slowed by the writer, and a client can page back
// past the ring.

type durableRig struct {
	t       *testing.T
	journal *state.Journal
	cfg     *config.Config
	svc     *BackupService
}

func newDurableRig(t *testing.T) *durableRig {
	t.Helper()
	cfg := testConfig(config.Source{
		Name:       "alpha",
		BackupSets: []config.BackupSet{{Name: "nightly", ID: mustBackupSetID(t, "alpha", "nightly")}},
	})
	r := &durableRig{t: t, journal: openTestJournal(t), cfg: cfg}
	r.svc = New(cfg, r.journal, nil, nil)
	return r
}

// restart stops the running service the way Close does (drain the writer)
// but keeps the journal open, then starts a new one on it.
func (r *durableRig) restart() {
	r.t.Helper()
	r.svc.cancel()
	<-r.svc.activityDone
	r.svc = New(r.cfg, r.journal, nil, nil)
}

func (r *durableRig) record(event, setID string, fields ...obs.Field) {
	r.svc.activity.RecordEvent(obs.Record{
		At: time.Now().UTC(), Level: obs.LevelInfo, Event: event, Message: event,
		Fields: append([]obs.Field{{Key: "backup_set", Value: setID}}, fields...),
	})
}

func (r *durableRig) recordAction(event, actionID string, fields ...obs.Field) {
	r.svc.activity.RecordEvent(obs.Record{
		At: time.Now().UTC(), Level: obs.LevelInfo, Event: event, Message: event,
		Fields: append([]obs.Field{{Key: "backup_set", Value: "alpha/nightly"}, {Key: "op_id", Value: actionID}}, fields...),
		Action: "recovery", ActionID: actionID,
	})
}

// flush stops the writer, which drains and writes what it holds.
func (r *durableRig) flush() {
	r.t.Helper()
	r.svc.cancel()
	<-r.svc.activityDone
}

func (r *durableRig) set() LiveActivitySet {
	r.t.Helper()
	live, err := r.svc.LiveActivity(context.Background(), LiveActivityRequest{BackupSetID: "alpha/nightly", Limit: liveActivityMaxLimit})
	if err != nil {
		r.t.Fatal(err)
	}
	return live.Sets[0]
}

func TestTheFeedSurvivesARestartAndTheNumberingContinues(t *testing.T) {
	r := newDurableRig(t)
	r.record(obs.EventScrubPass, "alpha/nightly", obs.Field{Key: "passed", Value: "3"})
	r.record(obs.EventScrubFailure, "alpha/nightly", obs.Field{Key: "artifact", Value: "alpha/nightly/a.zst"})
	before := r.set()
	if len(before.Events) != 2 {
		t.Fatalf("setup: the live feed holds %d events", len(before.Events))
	}
	last := before.LatestSequence
	r.restart()

	after := r.set()
	names := eventNames(after)
	if len(names) != 2 || names[0] != obs.EventScrubPass || names[1] != obs.EventScrubFailure {
		t.Fatalf("after a restart the feed holds %v, so an operator opening it sees nothing of what happened before", names)
	}
	if after.Events[1].Fields[0].Key != "backup_set" && len(after.Events[1].Fields) == 0 {
		t.Fatal("the stored events lost their fields")
	}
	r.record(obs.EventScrubPass, "alpha/nightly")
	got := r.set()
	if got.LatestSequence <= last {
		t.Fatalf("the first event after the restart is numbered %d, not above %d: a cursor would skip or repeat it", got.LatestSequence, last)
	}
}

func TestRecoveryAndRecoveryTestEventsAndTheirOpIDAreKept(t *testing.T) {
	r := newDurableRig(t)
	r.recordAction(obs.EventRecoveryStart, "op-1")
	r.recordAction(obs.EventRecoveryEnd, "op-1")
	r.recordAction(obs.EventRecoveryTestStart, "op-2")
	r.recordAction(obs.EventRecoveryTestEnd, "op-2")
	r.flush()
	rows, err := r.journal.ActivityEventsOfOperation(context.Background(), "op-1")
	if err != nil || len(rows) != 2 {
		t.Fatalf("op-1 has %d stored rows (%v); its start and its end are owed", len(rows), err)
	}
	if rows[0].OpID != "op-1" || rows[0].Event != obs.EventRecoveryStart {
		t.Fatalf("row = %+v", rows[0])
	}
	if rows, _ := r.journal.ActivityEventsOfOperation(context.Background(), "op-2"); len(rows) != 2 {
		t.Fatalf("the recovery test's events were not kept: %d rows", len(rows))
	}
}

func TestPeriodicSamplesAndPerRangeDownloadLinesAreNotStored(t *testing.T) {
	r := newDurableRig(t)
	for i := 0; i < 50; i++ {
		r.record(obs.EventTransferStats, "alpha/nightly")
		r.record(obs.EventSnapshotStats, "alpha/nightly")
		r.record(obs.EventAPIAction, "alpha/nightly", obs.Field{Key: "operation", Value: "recovery range"})
	}
	r.record(obs.EventAPIAction, "alpha/nightly", obs.Field{Key: "operation", Value: "recovery verified"})
	r.flush()
	rows, err := r.journal.RecentActivityEvents(context.Background(), "alpha/nightly", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Event != obs.EventAPIAction {
		t.Fatalf("%d rows stored for 150 samples and range lines plus one verification: %+v", len(rows), rows)
	}
}

func TestRecoveryStagesAreCoalescedToTheLastOneReached(t *testing.T) {
	r := newDurableRig(t)
	r.recordAction(obs.EventRecoveryStart, "op-9")
	for _, st := range []string{"resolving_copy", "reading", "verifying", "writing_target"} {
		r.recordAction(obs.EventRecoveryStage, "op-9", obs.Field{Key: "stage", Value: st})
		time.Sleep(2 * time.Millisecond)
	}
	r.flush()
	rows, _ := r.journal.ActivityEventsOfOperation(context.Background(), "op-9")
	var stages []state.ActivityEventRow
	for _, row := range rows {
		if row.Event == obs.EventRecoveryStage {
			stages = append(stages, row)
		}
	}
	if len(stages) != 1 || len(rows) != 2 {
		t.Fatalf("a recovery that passed 4 stages stored %d stage rows in %d rows; one stage row and the start are owed", len(stages), len(rows))
	}
	if got := stages[0].Fields; got == "" || !strings.Contains(got, "writing_target") {
		t.Fatalf("the stored stage is not the last one reached: %s", got)
	}
}

func TestACoalescedStageReplacesTheOneAnEarlierBatchStored(t *testing.T) {
	r := newDurableRig(t)
	r.recordAction(obs.EventRecoveryStage, "op-7", obs.Field{Key: "stage", Value: "reading"})
	time.Sleep(3 * activityFlushEvery) // the first batch is written
	r.recordAction(obs.EventRecoveryStage, "op-7", obs.Field{Key: "stage", Value: "verifying"})
	r.flush()
	rows, _ := r.journal.ActivityEventsOfOperation(context.Background(), "op-7")
	if len(rows) != 1 || !strings.Contains(rows[0].Fields, "verifying") {
		t.Fatalf("stored %d rows for one operation's stages: %+v", len(rows), rows)
	}
}

func TestRetentionDropsWhatIsOldAndWhatIsOverTheRowBound(t *testing.T) {
	j := openTestJournal(t)
	ctx := context.Background()
	prune := func(olderThan time.Time, maxRows int) (int64, error) {
		age, err := j.PruneActivityEventsByAge(ctx, olderThan, activityPruneBatch)
		if err != nil {
			return age, err
		}
		count, err := j.PruneActivityEventsOverCount(ctx, maxRows, activityPruneBatch)
		return age + count, err
	}
	now := time.Now().UTC()
	var rows []state.ActivityEventRow
	for i := 1; i <= 120; i++ {
		at := now
		// The old rows are in the MIDDLE of the sequence, so the row bound
		// alone would keep them: only the age rule can remove them.
		if i >= 61 && i <= 80 {
			at = now.Add(-activityRetention - time.Hour)
		}
		rows = append(rows, state.ActivityEventRow{Seq: int64(i), At: at, Level: "info", Event: "e", Scope: "set", SetID: "alpha/nightly", Fields: "[]"})
	}
	if err := j.AppendActivityEvents(ctx, rows); err != nil {
		t.Fatal(err)
	}
	n, err := prune(now.Add(-activityRetention), 50)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := j.RecentActivityEvents(ctx, "alpha/nightly", 1000)
	for _, r := range left {
		if r.Seq >= 61 && r.Seq <= 80 {
			t.Fatalf("a row older than the retention survived (seq %d): want 20 by age", r.Seq)
		}
	}
	if len(left) != 50 || left[0].Seq != 51 || n != 70 {
		t.Fatalf("pruned %d, kept %d starting at %d; want 20 by age and 50 by count, the newest 50 of what is left", n, len(left), left[0].Seq)
	}
}

func TestTheWriterFallingBehindDropsFromTheRecordAndNeverBlocksTheFeed(t *testing.T) {
	r := newDurableRig(t)
	// Stop the writer so the queue cannot drain, then overfill it.
	r.flush()
	done := make(chan struct{})
	go func() {
		for i := 0; i < activityQueueSize+500; i++ {
			r.record(obs.EventScrubPass, "alpha/nightly")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RecordEvent blocked on a full durable queue: a cycle would stall behind the journal")
	}
	if r.svc.activity.durable.dropped.Load() < 500 {
		t.Fatalf("dropped = %d; the overflow must be counted", r.svc.activity.durable.dropped.Load())
	}
	if got := len(r.set().Events); got == 0 {
		t.Fatal("the ring lost events because the durable queue was full")
	}
}

func TestBeforePagesBackwardsPastWhatTheRingHolds(t *testing.T) {
	r := newDurableRig(t)
	total := liveActivityBufferSize + 150
	for i := 0; i < total; i++ {
		r.record(obs.EventScrubPass, "alpha/nightly", obs.Field{Key: "n", Value: strconv.Itoa(i)})
	}
	r.flush()
	r.restart() // the ring is refilled with its newest 200 only
	live := r.set()
	if len(live.Events) != liveActivityBufferSize {
		t.Fatalf("ring holds %d", len(live.Events))
	}
	oldest := live.OldestSequence
	page, err := r.svc.LiveActivity(context.Background(), LiveActivityRequest{BackupSetID: "alpha/nightly", Before: oldest, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	p := page.Sets[0]
	if len(p.Events) != 100 || !p.Truncated {
		t.Fatalf("first page: %d events, truncated=%v; 150 older events exist", len(p.Events), p.Truncated)
	}
	if p.Events[len(p.Events)-1].Sequence >= oldest {
		t.Fatal("a page 'before' a sequence returned events at or after it")
	}
	for i := 1; i < len(p.Events); i++ {
		if p.Events[i].Sequence <= p.Events[i-1].Sequence {
			t.Fatal("a page is not oldest first")
		}
	}
	next, _ := r.svc.LiveActivity(context.Background(), LiveActivityRequest{BackupSetID: "alpha/nightly", Before: p.Events[0].Sequence, Limit: 100})
	if got := next.Sets[0]; len(got.Events) != 50 || got.Truncated {
		t.Fatalf("second page: %d events, truncated=%v; 50 remain", len(got.Events), got.Truncated)
	}
}

func TestAnOverlongMessageAndFieldListAreBounded(t *testing.T) {
	long := make([]byte, 100_000)
	for i := range long {
		long[i] = 'x'
	}
	row := rowOf(durableItem{e: LiveActivityEvent{
		Sequence: 1, At: time.Now(), Event: "e", Message: string(long),
		Fields: []LiveActivityField{{Key: "a", Value: string(long)}, {Key: "b", Value: "kept?"}},
	}})
	if len(row.Message) > activityMessageMax || len(row.Fields) > activityFieldsMax {
		t.Fatalf("message %d bytes, fields %d bytes", len(row.Message), len(row.Fields))
	}
}
