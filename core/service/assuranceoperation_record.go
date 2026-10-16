package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/obs"
)

// One assurance operation, read back by its id (#1200).
//
// The Operation page and `retnd assurance show <op>` both answer "what did
// this scrub or recovery test do", for a run that is still going and for one
// that ended an hour ago. The live feed can't answer the second: it is a
// bounded ring, and a run's lines fall off it. The durable activity record
// (migration 0018) can, because every line a recovery test writes carries its
// op_id, and since #1200 so does a scrub started with "scrub now". That
// record keeps a run's start, the LAST stage it reached (stage lines are
// coalesced) and its end, for 30 days.
//
// So this reads the record by op_id and adds whatever of the run the ring
// still holds, merged by sequence, because the writer batches (a line can be
// a quarter of a second from the journal) and the ring has every stage line
// rather than the last one. Then it says, from those lines and nothing else,
// what kind of run it was, where it got to and how it ended. The one fact
// the lines can't carry is "still running in this process", and the
// registry of running operations answers that.
//
// Nothing on record is ErrOperationNotFound: an id that never existed, and
// one whose lines were pruned, look the same from here, and a caller says so
// in those words.

// The states an assurance operation can be in, as the record says.
const (
	AssuranceOpRunning        = "running"
	AssuranceOpPassed         = "passed"
	AssuranceOpFailed         = "failed"
	AssuranceOpDidNotComplete = "did_not_complete"
	AssuranceOpCancelled      = "cancelled"
	// AssuranceOpUnfinished is a run with no end line that this process is
	// not running: it is still going in another process, or the process
	// running it stopped before it could write one.
	AssuranceOpUnfinished = "unfinished"
)

// AssuranceOperationRecord is one scrub's or recovery test's lines and what
// they add up to.
type AssuranceOperationRecord struct {
	OperationID string
	// Kind is scrub, recovery_test or recovery.
	Kind        string
	BackupSetID string
	State       string
	// Stage is the last named stage the run reached ("complete" for a
	// recovery or recovery test that passed). Empty for a scrub, which
	// names none.
	Stage string
	// Reason is the end line's own words for a run that did not pass.
	Reason    string
	StartedAt time.Time
	EndedAt   time.Time
	// Events are the run's lines, oldest first, as the feed carries them.
	Events []LiveActivityEvent
}

// AssuranceOperation reads one scrub or recovery test back by its operation
// id. ErrOperationNotFound when nothing of it is on record.
func (b *BackupService) AssuranceOperation(ctx context.Context, id string) (AssuranceOperationRecord, error) {
	if id == "" {
		return AssuranceOperationRecord{}, fmt.Errorf("%w: an operation id is required", ErrInvalidRequest)
	}
	var events []LiveActivityEvent
	setID := ""
	if b.journal != nil {
		rows, err := b.journal.ActivityEventsOfOperation(ctx, id)
		if err != nil {
			return AssuranceOperationRecord{}, err
		}
		for _, r := range rows {
			events = append(events, eventOf(r))
			if setID == "" {
				setID = r.SetID
			}
		}
	}
	events = append(events, b.activity.linesOf(id)...)
	events = uniqueBySequence(events)
	if len(events) == 0 {
		return AssuranceOperationRecord{}, fmt.Errorf("%w: nothing is on record for %q; a run's lines are kept for 30 days", ErrOperationNotFound, id)
	}
	b.ops.mu.Lock()
	running := b.ops.byID[id] != nil
	b.ops.mu.Unlock()
	rec := summariseAssuranceOperation(id, events, running)
	if rec.BackupSetID == "" {
		rec.BackupSetID = setID
	}
	return rec, nil
}

// linesOf is every line the rings still hold that carries op_id id.
func (l *liveActivity) linesOf(id string) []LiveActivityEvent {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []LiveActivityEvent
	take := func(r *liveActivityRing) {
		if r == nil {
			return
		}
		for i := 0; i < r.len(); i++ {
			if e := r.at(i); fieldValue(e.Fields, "op_id") == id {
				out = append(out, e)
			}
		}
	}
	// A scrub pass names its set in words rather than in a backup_set
	// field, so it lands in the deployment's bucket, not the set's.
	take(l.deployment)
	for _, st := range l.sets {
		if st != nil {
			take(st.ring)
		}
	}
	return out
}

func uniqueBySequence(in []LiveActivityEvent) []LiveActivityEvent {
	sort.SliceStable(in, func(i, j int) bool { return in[i].Sequence < in[j].Sequence })
	out := in[:0]
	for i, e := range in {
		if i > 0 && e.Sequence == in[i-1].Sequence {
			continue
		}
		out = append(out, e)
	}
	return out
}

func fieldValue(fields []LiveActivityField, key string) string {
	for _, f := range fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// summariseAssuranceOperation says what a run's lines add up to. It is pure,
// so the tests can hold it to every shape of record.
func summariseAssuranceOperation(id string, events []LiveActivityEvent, running bool) AssuranceOperationRecord {
	rec := AssuranceOperationRecord{OperationID: id, Events: events}
	var end, pass *LiveActivityEvent
	for i := range events {
		e := &events[i]
		if rec.BackupSetID == "" {
			rec.BackupSetID = fieldValue(e.Fields, "backup_set")
		}
		if rec.StartedAt.IsZero() || e.At.Before(rec.StartedAt) {
			rec.StartedAt = e.At
		}
		switch e.Event {
		case obs.EventRecoveryTestStart, obs.EventRecoveryTestEnd:
			rec.Kind = opKindRecoveryTest
		case obs.EventRecoveryStart, obs.EventRecoveryEnd:
			rec.Kind = obs.ActionRecovery
		case obs.EventRecoveryStage:
			if rec.Kind == "" {
				rec.Kind = e.Action
			}
			if s := fieldValue(e.Fields, "stage"); s != "" {
				rec.Stage = s
			}
		case obs.EventScrubPass, obs.EventScrubFailure:
			rec.Kind = opKindScrub
		}
		switch e.Event {
		case obs.EventRecoveryTestEnd, obs.EventRecoveryEnd:
			end = e
		case obs.EventScrubPass:
			pass = e
		}
	}
	if rec.Kind == opKindScrub && rec.BackupSetID == "" {
		rec.BackupSetID = scrubSetOf(events)
	}
	switch {
	case end != nil:
		rec.EndedAt = end.At
		rec.Reason = fieldValue(end.Fields, "reason")
		switch {
		case end.Result == string(obs.ResultSuccess):
			rec.State, rec.Stage = AssuranceOpPassed, "complete"
		case end.Result == string(obs.ResultError):
			rec.State = AssuranceOpFailed
		case rec.Reason == app.RecoveryTestCancelledReason:
			rec.State = AssuranceOpCancelled
		default:
			rec.State = AssuranceOpDidNotComplete
		}
	case pass != nil:
		rec.EndedAt = pass.At
		failed, _ := strconv.Atoi(fieldValue(pass.Fields, "failed"))
		incomplete, _ := strconv.Atoi(fieldValue(pass.Fields, "could_not_complete"))
		switch {
		case fieldValue(pass.Fields, "cancelled") == "true":
			rec.State = AssuranceOpCancelled
		case failed > 0:
			rec.State = AssuranceOpFailed
		case incomplete > 0:
			rec.State = AssuranceOpDidNotComplete
		default:
			rec.State = AssuranceOpPassed
		}
	case running:
		rec.State = AssuranceOpRunning
	default:
		rec.State = AssuranceOpUnfinished
	}
	return rec
}

// scrubSetOf is the set a scrub pass names in its scope ("set source/name").
func scrubSetOf(events []LiveActivityEvent) string {
	for _, e := range events {
		if s := fieldValue(e.Fields, "scope"); len(s) > 4 && s[:4] == "set " {
			return s[4:]
		}
	}
	return ""
}
