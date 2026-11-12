package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// The agent audit trail (EPIC U, U1.5, #1348; FR-97 and FR-98; ADR 0034
// decision 6).
//
// The action log records what changed and skips every GET, which is right for
// a dashboard and wrong for an agent, whose reads are the question. So every
// tool call is a row in agent_audit, reads included, and only the calls an
// operator would want on the feed (a mutation, a refusal, a confirmation
// changing state) also emit EventAgentAction. The feed is bounded to 50,000
// rows; one agent at the default rate limit could write 86,000 reads a day.
//
// # The writer
//
// Rows go through a queue to one goroutine that writes them in batches, the
// pattern the durable activity feed uses, so a call does not wait on a journal
// write. The difference is what happens when it cannot keep up: the activity
// feed drops and counts, because a feed line is a note about something that
// happened anyway. An audit row is the record that it happened, so a full
// queue makes the caller wait instead. The limiter is upstream of the writer,
// which keeps that wait short; a writer that is behind slows the agent and
// loses nothing. That holds for a call that names a token, which waits up to
// agentAuditEnqueueWait and is then reported lost (ErrAgentAuditStalled and an
// agent_audit_lost log line). A refusal that names no token is anyone's to
// cause, so it never waits: a full queue drops it and the feed's summary line
// counts it.
//
// # The bound
//
// Rows older than the retention are pruned, and so are rows beyond a row cap,
// because a refusal for a token that matched nothing is written for a caller
// with no credential at all and a retention in days does not stop a flood
// filling a disk inside one day. Both prunes delete in small batches and
// yield between them, so neither holds the journal across a cycle.

const (
	// DefaultAgentAuditRetentionDays is agents.audit_retention_days when the
	// configuration does not say.
	DefaultAgentAuditRetentionDays = 90

	// DefaultAgentAuditLimit and MaxAgentAuditLimit are the page size for
	// ListAgentAudit, clamped rather than refused like the activity feed's.
	DefaultAgentAuditLimit = 100
	MaxAgentAuditLimit     = 1000

	agentAuditQueueSize  = 1024
	agentAuditBatchMax   = 128
	agentAuditFlushEvery = 500 * time.Millisecond
	// agentAuditPruneEvery is short on purpose: three tokens at the default 60
	// calls a minute write 10,800 rows an hour, and a prune that comes round
	// hourly cannot hold a cap that insertion outruns.
	agentAuditPruneEvery = 5 * time.Minute
	agentAuditPruneBatch = 500
	// agentAuditPruneBatches bounds the age step of one pass, so a backlog of
	// old rows is worked off over several passes rather than in one long hold
	// of the journal. The over-count step has no ceiling: it loops until the
	// table is at its cap, yielding between batches.
	agentAuditPruneBatches = 20
	agentAuditPruneYield   = 10 * time.Millisecond
	// agentAuditEnqueueWait is how long a call that names a token waits for
	// room in the queue before the row is reported lost.
	agentAuditEnqueueWait = 5 * time.Second
	agentAuditMaxRows     = 1_000_000
	agentAuditStatement   = 5 * time.Second
	// agentAuditRetainFailed is how many rows a writer whose journal is failing
	// holds for the retry before it reports the rest lost.
	agentAuditRetainFailed = 10_000

	// maxAgentAuditArguments is the stored arguments' size cap. A tool's
	// arguments are small; a row per call must not be a row of whatever size
	// the caller sent.
	maxAgentAuditArguments = 4096

	// agentRefusalFeedEvery is how often a refusal that names no token may
	// reach the feed. Those are written for a caller with no credential, so
	// the feed gets one line that says how many there were, and the table
	// keeps every one.
	agentRefusalFeedEvery = time.Minute
)

// The headers a transport puts on the /api/v1 request it makes for a tool
// call, so the audit row for that request can name the tool and the transport.
// They are the transport's own statement and are checked on the way in
// (AgentCallHeaders): a tool name that is not a plain identifier is dropped,
// and a transport is stdio or http and otherwise api. The headers name the
// audit row and, on a mutating route, select the tool to propose; they never
// widen scope.
const (
	AgentToolHeader      = "X-Retnd-Agent-Tool"
	AgentTransportHeader = "X-Retnd-Agent-Transport"
)

var agentToolName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// AgentCallHeaders reads the tool and the transport a request says it is for.
func AgentCallHeaders(h http.Header) (tool, transport string) {
	tool = h.Get(AgentToolHeader)
	if !agentToolName.MatchString(tool) {
		tool = ""
	}
	switch t := h.Get(AgentTransportHeader); t {
	case "stdio", "http":
		return tool, t
	}
	return tool, "api"
}

// ErrAgentAuditClosed is returned when a call is recorded after the service
// has shut down its writer.
var ErrAgentAuditClosed = errors.New("service: the agent audit writer has stopped")

// ErrAgentAuditStalled is returned when the writer stayed full for
// agentAuditEnqueueWait, so the row was not stored.
var ErrAgentAuditStalled = errors.New("service: the audit could not be written")

// ErrAgentAuditFailing is returned by a reservation while the journal is
// refusing the audit writer's rows (a full or read-only disk, a schema error).
// It clears when a write lands.
var ErrAgentAuditFailing = errors.New("service: the audit trail cannot be written to the journal")

// AgentCall is one tool call, or one refusal of a call, for the audit trail.
type AgentCall struct {
	TokenID   string
	TokenName string
	// Tool is the catalogue name. Empty for a call that never reached a tool.
	Tool string
	// Transport is stdio, http or api.
	Transport string
	// Arguments is what the caller asked with. It is stored as JSON after
	// agenttools.Project and cut at a size cap, whatever the caller did.
	Arguments any
	// BackupSetID is "source/set" when the call concerns one set, which puts
	// its feed line on that set's strip. A set the configuration does not
	// have is dropped, as RecordAPIAction does.
	BackupSetID string
	// Mutating is true for a call that changes or starts something.
	Mutating bool
	// Refused is true when the call was turned away rather than served.
	Refused        bool
	Status         int
	Code           string
	CorrelationID  string
	ConfirmationID string
	// ConfirmationState is pending, approved, denied, expired or interrupted when this
	// record is a confirmation changing state, and empty for a call.
	ConfirmationState string

	// held is how many feed lines for refusals that named no token were held
	// back since the last one; only RecordAgentRefusal sets it.
	held int
}

type agentAuditItem struct {
	row state.AgentAudit
	// barrier, when set, is closed once everything queued before it is stored.
	barrier chan struct{}
	// probe marks an item that only proves the queue has room: it is dropped
	// by the writer and stores nothing.
	probe bool
}

type agentAuditWriter struct {
	journal *state.Journal
	// insert stores a batch; the journal's own InsertAgentAudits, a field so a
	// test can stand a failing disk in front of it.
	insert func(context.Context, []state.AgentAudit) error
	logger *obs.Logger
	// failing is set when a write was refused and cleared when one lands.
	failing atomic.Bool
	queue   chan agentAuditItem
	// stopped is closed when run has drained and returned.
	stopped chan struct{}

	mu        sync.Mutex
	retention time.Duration
	maxRows   int
	now       func() time.Time
	// wait is how long a row that names a token waits for room. A field so a
	// test can shorten it.
	wait time.Duration

	// The rest belongs to run's goroutine.
	batch    []state.AgentAudit
	barriers []chan struct{}
	pruneAt  time.Time
}

func newAgentAuditWriter(j *state.Journal, logger *obs.Logger) *agentAuditWriter {
	return &agentAuditWriter{
		journal: j, insert: j.InsertAgentAudits, logger: logger, now: time.Now,
		queue:     make(chan agentAuditItem, agentAuditQueueSize),
		stopped:   make(chan struct{}),
		retention: DefaultAgentAuditRetentionDays * 24 * time.Hour,
		maxRows:   agentAuditMaxRows,
		wait:      agentAuditEnqueueWait,
	}
}

// enqueue waits for room rather than dropping, and gives up only when the
// caller's context ends or the writer has stopped.
func (w *agentAuditWriter) enqueue(ctx context.Context, it agentAuditItem) error {
	select {
	case <-w.stopped:
		return ErrAgentAuditClosed
	default:
	}
	select {
	case w.queue <- it:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("service: record agent audit: %w", ctx.Err())
	case <-w.stopped:
		return ErrAgentAuditClosed
	}
}

// enqueueNamed is enqueue for a row that names a token: it waits for room, but
// only agentAuditEnqueueWait, and a row that did not fit is logged as lost.
func (w *agentAuditWriter) enqueueNamed(ctx context.Context, it agentAuditItem) error {
	wctx, cancel := context.WithTimeout(ctx, w.wait)
	defer cancel()
	err := w.enqueue(wctx, it)
	if err != nil && ctx.Err() == nil && wctx.Err() != nil {
		err = fmt.Errorf("%w: the writer stayed full for %s", ErrAgentAuditStalled, w.wait)
		w.logger.Error(ctx, "agent_audit_lost", err)
	}
	return err
}

// enqueueOrDrop is enqueue for a row no credential stands behind. It never
// waits, and reports false when the queue had no room.
func (w *agentAuditWriter) enqueueOrDrop(it agentAuditItem) (stored bool, err error) {
	select {
	case <-w.stopped:
		return false, ErrAgentAuditClosed
	default:
	}
	select {
	case w.queue <- it:
		return true, nil
	default:
		return false, nil
	}
}

func (w *agentAuditWriter) run(ctx context.Context) {
	defer close(w.stopped)
	tick := time.NewTicker(agentAuditFlushEvery)
	defer tick.Stop()
	for {
		select {
		case it := <-w.queue:
			w.add(it)
			if len(w.batch) >= agentAuditBatchMax || len(w.barriers) > 0 {
				w.write()
			}
		case <-tick.C:
			w.write()
			w.pruneIfDue(ctx)
		case <-ctx.Done():
			for {
				select {
				case it := <-w.queue:
					w.add(it)
				default:
					w.write()
					return
				}
			}
		}
	}
}

func (w *agentAuditWriter) add(it agentAuditItem) {
	if it.probe {
		return
	}
	if it.barrier != nil {
		w.barriers = append(w.barriers, it.barrier)
		return
	}
	w.batch = append(w.batch, it.row)
}

// write stores the open batch in one transaction and releases the barriers
// that were waiting behind it. A batch the journal refused is kept for the
// next try, up to agentAuditRetainFailed rows.
func (w *agentAuditWriter) write() {
	if len(w.batch) > 0 {
		wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := w.insert(wctx, w.batch)
		if err != nil {
			w.failing.Store(true)
			w.logger.Error(wctx, "agent_audit_write", err)
			if over := len(w.batch) - agentAuditRetainFailed; over > 0 {
				w.logger.Error(wctx, "agent_audit_lost",
					fmt.Errorf("%d agent audit rows were lost because the journal kept refusing them", over))
				w.batch = w.batch[over:]
			}
		} else {
			w.failing.Store(false)
			w.batch = w.batch[:0]
		}
		cancel()
	}
	for _, c := range w.barriers {
		close(c)
	}
	w.barriers = w.barriers[:0]
}

func (w *agentAuditWriter) pruneIfDue(ctx context.Context) {
	if w.now().Before(w.pruneAt) {
		return
	}
	w.pruneAt = w.now().Add(agentAuditPruneEvery)
	if _, err := w.prune(ctx); err != nil {
		w.logger.Error(ctx, "agent_audit_prune", err)
		// Try again at the next flush rather than in an hour.
		w.pruneAt = time.Time{}
	}
}

// prune removes what is past the retention and what is over the row cap, in
// batches. The age step is bounded per pass; the over-count step runs until
// the table is at its cap. It reports how many rows went.
func (w *agentAuditWriter) prune(ctx context.Context) (int64, error) {
	w.mu.Lock()
	cutoff, keep := w.now().Add(-w.retention), w.maxRows
	w.mu.Unlock()
	var total int64
	for _, step := range []struct {
		run     func(context.Context) (int64, error)
		ceiling int
	}{
		{func(c context.Context) (int64, error) {
			return w.journal.PruneAgentAuditByAge(c, cutoff, agentAuditPruneBatch)
		}, agentAuditPruneBatches},
		{func(c context.Context) (int64, error) {
			return w.journal.PruneAgentAuditOverCount(c, keep, agentAuditPruneBatch)
		}, 0},
	} {
		for i := 0; step.ceiling == 0 || i < step.ceiling; i++ {
			sctx, cancel := context.WithTimeout(ctx, agentAuditStatement)
			n, err := step.run(sctx)
			cancel()
			total += n
			if err != nil {
				return total, err
			}
			if n < agentAuditPruneBatch {
				break
			}
			// Yield, so the journal is free for the writer between batches.
			select {
			case <-time.After(agentAuditPruneYield):
			case <-ctx.Done():
				return total, fmt.Errorf("service: prune agent audit: %w", ctx.Err())
			}
		}
	}
	return total, nil
}

// flush returns once every row queued before it is stored.
func (w *agentAuditWriter) flush(ctx context.Context) error {
	barrier := make(chan struct{})
	if err := w.enqueue(ctx, agentAuditItem{barrier: barrier}); err != nil {
		return err
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("service: flush agent audit: %w", ctx.Err())
	case <-w.stopped:
		return nil
	}
}

// PruneAgentAudit runs one prune pass now, for the cases a caller wants the
// bound enforced without waiting for the writer's timer.
func (b *BackupService) PruneAgentAudit(ctx context.Context) (int64, error) {
	if err := b.audit.flush(ctx); err != nil && !errors.Is(err, ErrAgentAuditClosed) {
		return 0, err
	}
	return b.audit.prune(ctx)
}

// RecordAgentCall writes one row for a tool call, reads included, and puts a
// mutation, a refusal or a confirmation state change on the feed as
// EventAgentAction with a Result. A read that was served is a row and nothing
// else.
//
// The feed line is emitted whether or not the row is stored, and the error
// says only that the row was not: describing a call is not a reason for it to
// fail, but the caller learns the trail has a hole.
// ReserveAgentAudit is asked before an agent's tool call is dispatched. It
// proves the audit writer can take the row the call will leave, waiting up to
// five seconds for room, and returns ErrAgentAuditStalled when it cannot, or
// ErrAgentAuditFailing at once while the journal is refusing the writer's rows,
// so the caller refuses the call: an unaudited call is the worse failure. It is
// for every call that carries a token, a raw bearer call included; a row with
// no token never waits.
func (b *BackupService) ReserveAgentAudit(ctx context.Context) error {
	if b == nil {
		return nil
	}
	// A journal that refuses writes still drains the queue, so a reservation
	// that only asked for room would succeed while the rows were lost. The
	// writer says so, and the call is refused until a write lands.
	if b.audit.failing.Load() {
		b.audit.logger.Error(ctx, "agent_audit_unavailable", ErrAgentAuditFailing)
		return ErrAgentAuditFailing
	}
	return b.audit.enqueueNamed(ctx, agentAuditItem{probe: true})
}

func (b *BackupService) RecordAgentCall(ctx context.Context, c AgentCall) error {
	if b == nil {
		return nil
	}
	if c.Mutating || c.Refused || c.ConfirmationState != "" {
		b.emitAgentAction(ctx, c)
	}
	return b.audit.enqueueNamed(ctx, agentAuditItem{row: state.AgentAudit{
		At: now(), TokenID: c.TokenID, Tool: c.Tool, Transport: c.Transport,
		Arguments: agentAuditArguments(c.Arguments), Status: c.Status, Code: c.Code,
		CorrelationID: c.CorrelationID, ConfirmationID: c.ConfirmationID,
	}})
}

// agentAuditArguments renders arguments as stored: JSON that has been through
// agenttools.Project, bounded. The projection is applied here, to every row,
// rather than left to each transport, so a call that reaches the table by any
// door has its arguments cleaned by the one thing that knows how (FR-98): a
// denied field name is dropped at every depth and a string that carries a
// canary shape is replaced by a marker. Arguments that do not render as JSON
// are not stored at all.
func agentAuditArguments(v any) string {
	const unrecorded = `{"unrecorded":"the arguments could not be rendered"}`
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return unrecorded
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return unrecorded
	}
	projected := agenttools.Project(decoded)
	s, ok := projected.(string)
	if !ok {
		out, err := json.Marshal(projected)
		if err != nil {
			return unrecorded
		}
		s = string(out)
	}
	if len(s) > maxAgentAuditArguments {
		s = strings.ToValidUTF8(s[:maxAgentAuditArguments], "") + "..."
	}
	return s
}

// emitAgentAction is the feed half of RecordAgentCall.
func (b *BackupService) emitAgentAction(ctx context.Context, c AgentCall) {
	result, message := agentActionResultAndMessage(c)
	attrs := []slog.Attr{
		slog.String("actor", "agent:"+c.TokenName),
		slog.String("token_id", c.TokenID),
		slog.String("transport", c.Transport),
		slog.Int("status", c.Status),
	}
	if c.Tool != "" {
		attrs = append(attrs, slog.String("tool", c.Tool))
	}
	if c.Code != "" {
		attrs = append(attrs, slog.String("error_code", c.Code))
	}
	if c.CorrelationID != "" {
		attrs = append(attrs, slog.String("correlation_id", c.CorrelationID))
	}
	if c.ConfirmationID != "" {
		attrs = append(attrs, slog.String("confirmation_id", c.ConfirmationID))
	}
	if c.ConfirmationState != "" {
		attrs = append(attrs, slog.String("state", c.ConfirmationState))
	}
	// Only a set the configuration names, for the reason RecordAPIAction
	// gives: the id came from a request.
	if c.BackupSetID != "" && b.configuresBackupSetID(c.BackupSetID) {
		attrs = append(attrs, slog.String("backup_set", c.BackupSetID))
	}
	if c.held > 0 {
		attrs = append(attrs, slog.Int("suppressed", c.held))
		message += " (" + strconv.Itoa(c.held) + " more since the last line)"
	}
	b.logger.Completed(ctx, result, obs.EventAgentAction, message, attrs...)
}

// agentActionResultAndMessage decides how the call went. A confirmation's
// state is its outcome: pending is information, approved is success, and a
// denial or an expiry is a warning, because the change did not happen.
func agentActionResultAndMessage(c AgentCall) (obs.Result, string) {
	who := "agent:" + c.TokenName
	if c.TokenName == "" {
		who = "an unknown agent token"
	}
	what := c.Tool
	if what == "" {
		what = "a call"
	}
	switch c.ConfirmationState {
	case "pending":
		return obs.ResultInfo, who + " asked to " + what + ", waiting for a person"
	case "approved":
		return obs.ResultSuccess, "a person approved " + what + " for " + who
	case "denied", "expired":
		return obs.ResultWarn, what + " for " + who + " was " + c.ConfirmationState
	case "interrupted":
		return obs.ResultWarn, what + " for " + who + " was approved but the engine stopped before it finished; check this feed before running it again"
	}
	switch {
	case c.Refused:
		code := c.Code
		if code == "" {
			code = strconv.Itoa(c.Status)
		}
		return obs.ResultWarn, who + " was refused " + what + ": " + code
	case c.Status >= 500:
		return obs.ResultError, who + " " + what + " failed: " + strconv.Itoa(c.Status)
	case c.Status >= 400:
		return obs.ResultWarn, who + " " + what + " refused: " + strconv.Itoa(c.Status)
	default:
		return obs.ResultSuccess, who + " " + what
	}
}

// refusalFeed lets one refusal that names no token through per
// agentRefusalFeedEvery and counts the rest, so a caller with no credential
// cannot fill the feed that an operator reads.
type refusalFeed struct {
	mu         sync.Mutex
	last       time.Time
	suppressed int
}

// hold counts a refusal whose audit row was dropped, so the next line that
// reaches the feed says so with the others.
func (f *refusalFeed) hold() {
	f.mu.Lock()
	f.suppressed++
	f.mu.Unlock()
}

// allow reports whether a line may be emitted at t and how many were held
// back since the last one.
func (f *refusalFeed) allow(t time.Time) (bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.last.IsZero() && t.Sub(f.last) < agentRefusalFeedEvery {
		f.suppressed++
		return false, 0
	}
	held := f.suppressed
	f.last, f.suppressed = t, 0
	return true, held
}

// AgentAuditEntry is one row of the trail.
type AgentAuditEntry struct {
	Seq     int64
	At      time.Time
	TokenID string
	// TokenName is the token's name, joined when the page is read. Empty for a
	// row whose token matched nothing.
	TokenName      string
	Tool           string
	Transport      string
	Arguments      string
	Status         int
	Code           string
	CorrelationID  string
	ConfirmationID string
}

// Contract is the entry as /api/v1 shows it, for the web host and the
// terminal's direct door alike, so the two cannot format a time differently.
func (e AgentAuditEntry) Contract() apicontract.AgentAuditEntry {
	var name *string
	if e.TokenName != "" {
		name = &e.TokenName
	}
	return apicontract.AgentAuditEntry{
		Seq: e.Seq, At: e.At.UTC().Format(time.RFC3339),
		TokenID: e.TokenID, TokenName: name, Tool: e.Tool, Transport: e.Transport, Arguments: e.Arguments,
		Status: e.Status, Code: e.Code, CorrelationID: e.CorrelationID, ConfirmationID: e.ConfirmationID,
	}
}

// AgentAuditQuery selects a page of the trail.
type AgentAuditQuery struct {
	TokenID string
	// Since keeps entries at or after a moment: an RFC 3339 time, or how far
	// back to look as days ("7d") or a duration ("36h"). Empty keeps all.
	Since string
	// Before is a cursor an earlier page returned; anything that is not a
	// positive number starts at the newest, as the activity feed's does.
	Before string
	Limit  int
}

// ListAgentAudit returns a page of the trail, newest first, and the cursor
// for the page behind it, non-empty only when this page came back full.
// Everything recorded before the call is in it, queued or not.
func (b *BackupService) ListAgentAudit(ctx context.Context, q AgentAuditQuery) ([]AgentAuditEntry, string, error) {
	if q.Limit <= 0 {
		q.Limit = DefaultAgentAuditLimit
	}
	if q.Limit > MaxAgentAuditLimit {
		q.Limit = MaxAgentAuditLimit
	}
	since, err := parseAgentAuditSince(q.Since, now())
	if err != nil {
		return nil, "", err
	}
	if err := b.audit.flush(ctx); err != nil && !errors.Is(err, ErrAgentAuditClosed) {
		return nil, "", err
	}
	rows, err := b.journal.AgentAuditPage(ctx, state.AgentAuditFilter{
		TokenID: q.TokenID, Since: since, Before: activityCursor(q.Before), Limit: q.Limit,
	})
	if err != nil {
		return nil, "", fmt.Errorf("service: list agent audit: %w", err)
	}
	out := make([]AgentAuditEntry, 0, len(rows))
	for _, a := range rows {
		out = append(out, AgentAuditEntry{
			Seq: a.Seq, At: a.At, TokenID: a.TokenID, TokenName: a.TokenName, Tool: a.Tool, Transport: a.Transport,
			Arguments: a.Arguments, Status: a.Status, Code: a.Code,
			CorrelationID: a.CorrelationID, ConfirmationID: a.ConfirmationID,
		})
	}
	next := ""
	if len(rows) == q.Limit {
		next = strconv.FormatInt(rows[len(rows)-1].Seq, 10)
	}
	return out, next, nil
}

// parseAgentAuditSince reads a since: empty, an RFC 3339 time, "7d" or a Go
// duration. A duration is how far back from at.
func parseAgentAuditSince(s string, at time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("%w: %q is not a time or a duration; use 2026-11-01T00:00:00Z, 7d or 36h", ErrInvalidRequest, s)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		parsed, err := time.ParseDuration(s)
		if err != nil || parsed < 0 {
			return time.Time{}, fmt.Errorf("%w: %q is not a time or a duration; use 2026-11-01T00:00:00Z, 7d or 36h", ErrInvalidRequest, s)
		}
		d = parsed
	}
	return at.Add(-d), nil
}
