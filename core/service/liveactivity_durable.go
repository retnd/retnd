package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// The durable half of the activity feed (migration 0018).
//
// # What changed
//
// liveactivity.go keeps a bounded ring per backup set and says, in its own
// header, why it did not write those events to the journal: a tick-rate write
// path on the one table whose guarantees exist to avoid exactly that. That
// reasoning holds for the NUMBERS the feed carries (a transfer's rate, a bar's
// position), which are true for an instant and stay in memory. It never held
// for the EVENTS, which are few, are facts about what happened, and are what an
// operator opens the feed to read after a restart. The owner decided they are
// durable; this file is how, without putting a write on the cycle's path.
//
// # The write path
//
// RecordEvent still does one lock, one append and a return. It also offers the
// event to a bounded channel with a non-blocking send. One goroutine drains
// the channel, batches what it finds (up to activityBatchMax events or
// activityFlushEvery, whichever is first) and writes the batch in a single
// transaction, so a cycle that logs a hundred lines costs the journal a few
// transactions and the cycle nothing. If the writer cannot keep up the channel
// fills and further events are DROPPED from the durable record (counted and
// reported once per flush); they are never dropped from the ring, and the
// cycle is never slowed by them.
//
// What a crash loses: whatever was in the channel or the open batch, at most
// activityFlushEvery of events. A clean Close drains and flushes first.
//
// # Write volume is a decision
//
//   - Periodic samples (transfer_stats, snapshot_stats) are not stored; the
//     next sample supersedes them and they are what makes an event stream
//     tick-rate.
//   - A streamed download writes one "recovery range" audit line per HTTP
//     range, and a browser can ask for hundreds. Those are not stored. The
//     download's start, its refusals and its final verification are.
//   - recovery_stage lines are coalesced: one row per operation, replaced by
//     each newer stage. A recovery costs a start row, one stage row naming the
//     last stage it reached and an end row, whatever number of stages it
//     passes through, and one that died shows where it stopped.
//
// # Retention
//
// 30 days and 50,000 rows, whichever bites first, oldest first. Pruned at
// start, then hourly, then after each activityPruneAfter inserts. The numbers
// are constants here and not configuration: the table holds a fact about what
// happened, not a setting an operator tunes, and a longer history is the
// journal's transition log, which is not pruned.
//
// # A prune waits its turn (#1247)
//
// The journal has one connection, and a scrub or a recovery test can keep it
// busy, or evict the page cache from under it, for longer than a prune
// should wait. So a prune is a run of single statements, each removing at
// most activityPruneBatch rows under activityPruneStatementTimeout of its
// own, and the connection is free between them. One that misses its
// deadline has lost a race, not found a fault: it is tried again
// activityPruneRetry later and logged at debug to the process log alone,
// not to the feed. Only a prune that has gone on failing for
// activityPruneComplainAfter is an error, once per window: by then the
// table is growing past its bound and somebody should know.
//
// # Reading it back
//
// At start the rings are refilled from the newest rows of each bucket and the
// sequence counter continues from the highest stored one, so a restart no
// longer empties the feed or reuses a number. A client that asks for events
// older than the ring holds passes before=<sequence> to GET /activity/live
// and pages backwards through the table.

const (
	activityRetention  = 30 * 24 * time.Hour
	activityMaxRows    = 50_000
	activityBatchMax   = 200
	activityFlushEvery = 250 * time.Millisecond
	activityQueueSize  = 4096
	activityPruneEvery = time.Hour
	activityPruneAfter = 5_000
	activityPruneBatch = 1_000
	activityMessageMax = 2 << 10
	activityFieldsMax  = 8 << 10
)

const (
	activityPruneStatementTimeout = 5 * time.Second
	activityPruneRetry            = time.Minute
	activityPruneComplainAfter    = 6 * time.Hour

	activityPruneStatementsPerPass = 10
	activityPruneContinue          = 5 * time.Second
)

// activityNotStored are the events never written: periodic samples.
var activityNotStored = map[string]bool{
	obs.EventTransferStats: true,
	obs.EventSnapshotStats: true,
}

// activityJournal is the slice of state.Journal this file needs.
type activityJournal interface {
	AppendActivityEvents(ctx context.Context, rows []state.ActivityEventRow) error
	MaxActivityEventSeq(ctx context.Context) (int64, error)
	RecentActivityEvents(ctx context.Context, setID string, limit int) ([]state.ActivityEventRow, error)
	ActivityEventsBefore(ctx context.Context, setID string, before int64, limit int) ([]state.ActivityEventRow, bool, error)
	PruneActivityEventsByAge(ctx context.Context, olderThan time.Time, limit int) (int64, error)
	PruneActivityEventsOverCount(ctx context.Context, maxRows, limit int) (int64, error)
}

var _ activityJournal = (*state.Journal)(nil)

// durableItem is what RecordEvent hands the writer: the event as the ring
// holds it and the bucket it landed in.
type durableItem struct {
	e     LiveActivityEvent
	setID string
}

// activityDurable is the writer.
type activityDurable struct {
	journal activityJournal
	logger  *obs.Logger
	now     func() time.Time

	// statementTimeout bounds each prune statement on its own. It is a
	// field so a test can make a held connection cost milliseconds.
	statementTimeout time.Duration

	queue   chan durableItem
	dropped atomic.Int64

	// parent is the writer's own context, set by run.
	parent context.Context

	// The rest belongs to run's goroutine and nothing else touches it.
	// pruneAt is when the next prune is owed (zero: now), and
	// pruneFailingSince is when the run of failed prunes that has not
	// ended yet began (zero: the last one worked).
	batch             []durableItem
	inserted          int
	pruneAt           time.Time
	pruneFailingSince time.Time
}

func newActivityDurable(j activityJournal, logger *obs.Logger) *activityDurable {
	return &activityDurable{
		journal: j, logger: logger, now: time.Now, statementTimeout: activityPruneStatementTimeout,
		queue: make(chan durableItem, activityQueueSize),
	}
}

// offer is called under the feed's lock and must not block.
func (d *activityDurable) offer(setID string, e LiveActivityEvent) {
	if d == nil || activityNotStored[e.Event] {
		return
	}
	select {
	case d.queue <- durableItem{e: e, setID: setID}:
	default:
		d.dropped.Add(1)
	}
}

// storable reports whether an event passes the per-event filter that needs
// its fields.
func storable(e LiveActivityEvent) bool {
	if e.Event != obs.EventAPIAction {
		return true
	}
	for _, f := range e.Fields {
		if f.Key == "operation" && f.Value == "recovery range" {
			return false
		}
	}
	return true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type fieldJSON struct {
	K string `json:"k"`
	V string `json:"v"`
}

func rowOf(it durableItem) state.ActivityEventRow {
	fields := make([]fieldJSON, 0, len(it.e.Fields))
	opID := ""
	for _, f := range it.e.Fields {
		fields = append(fields, fieldJSON{f.Key, f.Value})
		if f.Key == "op_id" {
			opID = f.Value
		}
	}
	raw, _ := json.Marshal(fields)
	if len(raw) > activityFieldsMax {
		// Keep the front of the list (the fields are in the order the
		// emitter logged them, identity first) and say it was cut.
		for len(fields) > 0 {
			fields = fields[:len(fields)-1]
			raw, _ = json.Marshal(append(append([]fieldJSON(nil), fields...), fieldJSON{"truncated", "true"}))
			if len(raw) <= activityFieldsMax {
				break
			}
		}
	}
	return state.ActivityEventRow{
		Seq: it.e.Sequence, At: it.e.At, Level: it.e.Level, Event: it.e.Event, Scope: it.e.Scope,
		SetID: it.setID, OpID: opID, Message: clip(it.e.Message, activityMessageMax),
		Result: it.e.Result, Action: it.e.Action, ActionID: it.e.ActionID, Fields: string(raw),
		Coalesce: it.e.Event == obs.EventRecoveryStage,
	}
}

// run drains the queue until ctx ends, then flushes what is left.
func (d *activityDurable) run(ctx context.Context) {
	tick := time.NewTicker(activityFlushEvery)
	defer tick.Stop()
	for {
		select {
		case it := <-d.queue:
			d.add(it)
			if len(d.batch) >= activityBatchMax {
				d.flush()
			}
		case <-tick.C:
			d.tick()
		case <-ctx.Done():
			for {
				select {
				case it := <-d.queue:
					d.add(it)
				default:
					d.flush()
					return
				}
			}
		}
	}
}

// add files one queued event into the open batch, if it is stored at all.
func (d *activityDurable) add(it durableItem) {
	if storable(it.e) {
		d.batch = append(d.batch, it)
	}
}

// tick is the flush timer: write the open batch, then prune if one is owed.
func (d *activityDurable) tick() {
	d.flush()
	if d.pruneDue() {
		d.prune()
	}
}

// flush writes the open batch in one transaction.
func (d *activityDurable) flush() {
	if len(d.batch) == 0 && d.dropped.Load() == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows := coalesceStages(d.batch)
	if len(rows) > 0 {
		if err := d.journal.AppendActivityEvents(wctx, rows); err != nil {
			d.logger.Error(wctx, "activity_events_write", err)
		} else {
			d.inserted += len(rows)
		}
	}
	d.batch = d.batch[:0]
	if n := d.dropped.Swap(0); n > 0 {
		d.logger.Error(wctx, "activity_events_dropped",
			fmt.Errorf("%d activity events were not stored because the writer fell behind", n))
	}
	if d.inserted > 0 && d.pruneDue() {
		d.prune()
	}
}

// pruneDue reports whether a prune is owed: its time has come, or enough
// rows went in since the last one. The second does not apply while prunes
// are failing, or a busy feed would retry one on every flush.
func (d *activityDurable) pruneDue() bool {
	if d.inserted >= activityPruneAfter && d.pruneFailingSince.IsZero() {
		return true
	}
	return !d.now().Before(d.pruneAt)
}

// prune applies the retention rule and decides what its outcome is worth
// saying. See "A prune waits its turn" above.
func (d *activityDurable) prune() {
	err := d.pruneBatches()
	now := d.now()
	if err == nil {
		d.inserted, d.pruneAt, d.pruneFailingSince = 0, now.Add(activityPruneEvery), time.Time{}
		return
	}
	d.pruneAt = now.Add(activityPruneRetry)
	if d.pruneFailingSince.IsZero() {
		d.pruneFailingSince = now
	}
	ctx := context.Background()
	if since := d.pruneFailingSince; now.Sub(since) >= activityPruneComplainAfter {
		d.logger.Error(ctx, "activity_events_prune",
			fmt.Errorf("the activity events table has not been pruned since %s: %w", since.Format(time.RFC3339), err))
		d.pruneFailingSince = now
		return
	}
	// The process log only, with the tap off: the feed is the operator's
	// terminal, and a line in it at any level is a line somebody reads.
	d.logger.WithSink(nil).Event(ctx, obs.LevelDebug, obs.EventError, "activity events prune deferred",
		slog.String("op", "activity_events_prune"),
		slog.String("error", err.Error()),
		slog.Duration("retry_in", activityPruneRetry),
	)
}

// pruneBatches runs the age rule and then the row bound, one statement at a
// time, each under a deadline of its own, until a statement removes less
// than a whole batch. It stops at the first failure: what is left is
// removed by the retry.
func (d *activityDurable) pruneBatches() error {
	steps := []func(context.Context) (int64, error){
		func(ctx context.Context) (int64, error) {
			return d.journal.PruneActivityEventsByAge(ctx, d.now().Add(-activityRetention), activityPruneBatch)
		},
		func(ctx context.Context) (int64, error) {
			return d.journal.PruneActivityEventsOverCount(ctx, activityMaxRows, activityPruneBatch)
		},
	}
	for _, step := range steps {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), d.statementTimeout)
			n, err := step(ctx)
			cancel()
			if err != nil {
				return err
			}
			if n < activityPruneBatch {
				break
			}
		}
	}
	return nil
}

// coalesceStages turns a batch into rows, keeping only the last
// recovery_stage line of each operation within it (the journal then replaces
// any stored one).
func coalesceStages(batch []durableItem) []state.ActivityEventRow {
	lastStage := map[string]int{}
	for i, it := range batch {
		if it.e.Event == obs.EventRecoveryStage && it.e.ActionID != "" {
			lastStage[it.e.ActionID] = i
		}
	}
	rows := make([]state.ActivityEventRow, 0, len(batch))
	for i, it := range batch {
		if it.e.Event == obs.EventRecoveryStage && it.e.ActionID != "" && lastStage[it.e.ActionID] != i {
			continue
		}
		rows = append(rows, rowOf(it))
	}
	return rows
}

func eventOf(r state.ActivityEventRow) LiveActivityEvent {
	var fields []fieldJSON
	_ = json.Unmarshal([]byte(r.Fields), &fields)
	e := LiveActivityEvent{
		Sequence: r.Seq, At: r.At, Level: r.Level, Event: r.Event, Scope: r.Scope,
		Message: r.Message, Result: r.Result, Action: r.Action, ActionID: r.ActionID,
		Fields: make([]LiveActivityField, 0, len(fields)),
	}
	for _, f := range fields {
		e.Fields = append(e.Fields, LiveActivityField{Key: f.K, Value: f.V})
	}
	return e
}

// hydrate refills the rings from the journal and moves the sequence counter
// past everything stored. It runs once, before the feed is reachable.
func (l *liveActivity) hydrate(ctx context.Context, j activityJournal, setIDs []string) {
	if l == nil || j == nil {
		return
	}
	max, err := j.MaxActivityEventSeq(ctx)
	if err != nil || max == 0 {
		return
	}
	buckets := map[string][]LiveActivityEvent{}
	for _, id := range append([]string{""}, setIDs...) {
		rows, err := j.RecentActivityEvents(ctx, id, liveActivityBufferSize)
		if err != nil {
			continue
		}
		evs := make([]LiveActivityEvent, 0, len(rows))
		for _, r := range rows {
			evs = append(evs, eventOf(r))
		}
		buckets[id] = evs
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if max > l.seq {
		l.seq = max
	}
	for _, e := range buckets[""] {
		l.deployment.add(e)
	}
	for _, id := range setIDs {
		st := l.setLocked(id)
		if st == nil {
			continue
		}
		for _, e := range buckets[id] {
			st.ring.add(e)
		}
	}
}

// history serves the part of a reading older than the ring: the events of
// each named bucket with a sequence below before, from the journal.
func (b *BackupService) activityHistory(ctx context.Context, ids []string, wantDeployment bool, before int64, limit int) ([]LiveActivitySet, *LiveActivityDeployment, error) {
	// The live counters (is the set active, its stage) are the live ones;
	// only the events are replaced by the page.
	sets, deployment := b.activity.read(ids, wantDeployment, 1<<62, 1)
	for i := range sets {
		rows, more, err := b.journal.ActivityEventsBefore(ctx, sets[i].BackupSetID, before, limit)
		if err != nil {
			return nil, nil, err
		}
		sets[i].Events = historyEvents(rows)
		sets[i].Truncated = more
		sets[i].Dropped = false
		sets[i].Unfinished = nil
		if len(rows) > 0 {
			sets[i].OldestSequence, sets[i].LatestSequence = rows[0].Seq, rows[len(rows)-1].Seq
		}
	}
	if deployment != nil {
		rows, more, err := b.journal.ActivityEventsBefore(ctx, "", before, limit)
		if err != nil {
			return nil, nil, err
		}
		deployment.Events = historyEvents(rows)
		deployment.Truncated = more
		deployment.Dropped = false
		deployment.Unfinished = nil
		if len(rows) > 0 {
			deployment.OldestSequence, deployment.LatestSequence = rows[0].Seq, rows[len(rows)-1].Seq
		}
	}
	return sets, deployment, nil
}

func historyEvents(rows []state.ActivityEventRow) []LiveActivityEvent {
	out := make([]LiveActivityEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventOf(r))
	}
	return out
}

// configuredSetIDs lists the backup sets a configuration names, as the
// source/name ids the feed's buckets use.
func configuredSetIDs(cfg *config.Config) []string {
	var ids []string
	for _, src := range cfg.Sources {
		for _, bs := range src.BackupSets {
			ids = append(ids, src.Name+"/"+bs.Name)
		}
	}
	return ids
}
