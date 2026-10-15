package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/cliecho"
)

// Every action taken through this API, recorded where an operator can
// read it (issue #599).
//
// `grep RecordEvent apps/common/webhost` used to find nothing. The engine
// has emitted a structured event for every step of a cycle since FR-23,
// but nothing the Web UI did emitted anything at all: a connection test
// returned a sentence to one caller and vanished, a settings patch left no
// trace, a refusal reached a dialog that closed. So an operator watching a
// button do nothing had no way to tell "it refused", "it failed" and "it
// was never wired up" apart, which is the failure the global terminal
// exists to end.
//
// # Why this is middleware and not a line in every handler
//
// Because "every action, refusals included" is a claim about ALL of them,
// and a claim like that held up by forty call sites is a claim that is
// already false somewhere. One middleware over the whole /api/v1 group
// covers every route that exists and every route added later, including
// the ones that refuse before reaching a handler at all: an unauthorised
// request, a CSRF failure, a destructive-operations gate denial. Those are
// exactly the refusals an operator most needs to see, and not one of them
// reaches a handler body.
//
// It also settles where the echoed `retnd` command comes from.
// The command is a function of the route and the request, both of which
// are right here, so it is built once, in Go, in core/cliecho, and never
// composed in a browser (see that package for why that matters).
//
// # Why only what changes something
//
// A GET is not an action. A dashboard polling five read routes every
// second would bury the one line an operator is looking for under
// thousands of its own reads, and the durable record of what was read is
// nobody's question. So this records every request that is not a GET, and
// the reads stay out of it.

// ActionRecorder is what this package records API actions through.
//
// It is deliberately narrow, and the action it carries is
// core/cliecho.APIAction rather than a type declared here, for a reason
// the dependency rule settles: core may not import apps, so a recorder
// implemented in core/service could not name a type that lives in this
// package. The vocabulary of "an action somebody took" belongs with the
// package that already owns the vocabulary of what an action is
// equivalent to.
//
// core/service.BackupService implements this.
type ActionRecorder interface {
	RecordAPIAction(ctx context.Context, action cliecho.APIAction)
}

// selfAuditedKey carries a flag a handler raises when it has written its own
// action-log row, naming things this middleware cannot see (the copy
// chosen, the target, the code of a refusal). Recording the request here
// too would write two rows for one act, and "one mint writes one row" is a
// claim somebody counts.
//
// A flag the handler raises, rather than a list of routes, because a
// request can be refused before its handler runs (no session, no CSRF
// pair) and those are exactly the refusals that must still be recorded.
type selfAuditedKey struct{}

// markSelfAudited tells recordActions this request has its own row.
func markSelfAudited(ctx context.Context) {
	if f, ok := ctx.Value(selfAuditedKey{}).(*atomic.Bool); ok {
		f.Store(true)
	}
}

// gateNoteKey carries a note a gate refusal leaves for recordActions
// (issue #1165).
//
// A refusal by the destructive gate is an audit entry in its own right: who
// asked, which act, which set or snapshot, what state the gate was in. The
// first two are in the request and the last is in the gate, and the gate is
// consulted in two places that do not share a code path: requireDestructiveGate
// for the routes behind it, and createBackupSet for the run a create asks
// for, which is a 201 and so is not a refusal by status. Both leave this note,
// and recordActions turns it into the fields of the one entry it was already
// going to write. A second entry would be a second line in the feed for one
// refusal, and "exactly one" is a claim somebody counts.
type gateNoteKey struct{}

type gateNote struct {
	refused     bool
	state       string
	reason      string
	operation   string // set by the caller when the route and body cannot say
	backupSetID string // likewise
}

// noteGateRefusal records that the gate refused this request, and the state
// it was in when it did. operation is "" when the route and the body say
// which act it was (the middleware case), and a name when only the handler
// knows (run_immediately).
func noteGateRefusal(ctx context.Context, gate DestructiveGate, operation string) {
	n, ok := ctx.Value(gateNoteKey{}).(*gateNote)
	if !ok {
		return
	}
	n.refused = true
	n.state, n.reason = gateStatus(gate)
	n.operation = operation
}

// noteGateRefusedSet names the set a refused create made, which exists only
// after the create has run.
func noteGateRefusedSet(ctx context.Context, backupSetID string) {
	if n, ok := ctx.Value(gateNoteKey{}).(*gateNote); ok {
		n.backupSetID = backupSetID
	}
}

// gateTarget reads what a refused gated request asked for. Nothing in it is
// taken from anywhere but the route and the ids in the body: the entry is
// readable by anyone who can read the feed, so it names the act and its
// target and carries no credential, no header and no free text.
func gateTarget(route string, params map[string]string, body []byte) (operation, set, run, snapshot, hold string) {
	switch {
	case route == "/operations":
		var req struct {
			Action      string `json:"action"`
			BackupSetID string `json:"backup_set_id"`
			Restore     struct {
				BackupSetID string `json:"backup_set_id"`
				SnapshotID  string `json:"snapshot_id"`
			} `json:"snapshot_restore"`
			Verify struct {
				BackupSetID string `json:"backup_set_id"`
				RunID       string `json:"run_id"`
			} `json:"snapshot_verify"`
			Hold struct {
				BackupSetID string `json:"backup_set_id"`
				RunID       string `json:"run_id"`
			} `json:"snapshot_hold"`
			Release struct {
				BackupSetID string `json:"backup_set_id"`
				HoldID      string `json:"hold_id"`
			} `json:"snapshot_hold_release"`
		}
		_ = json.Unmarshal(body, &req)
		operation = req.Action
		switch req.Action {
		case "run_backup_set":
			set = req.BackupSetID
		case "restore_snapshot":
			set, snapshot = req.Restore.BackupSetID, req.Restore.SnapshotID
		case "verify_snapshot":
			set, run = req.Verify.BackupSetID, req.Verify.RunID
		case "hold_snapshot":
			set, run = req.Hold.BackupSetID, req.Hold.RunID
		case "release_snapshot_hold":
			set, hold = req.Release.BackupSetID, req.Release.HoldID
		}
	case strings.HasSuffix(route, "/retention/apply"):
		operation = "retention_apply"
		set = backupSetOfRoute(params)
	}
	return operation, set, run, snapshot, hold
}

// maxRecordedBodyBytes bounds what this middleware buffers in order to
// build a command line from a request.
//
// The routes that carry a body all cap their own already
// (maxCreateBackupSetBodyBytes and friends), and this is the same order of
// size. It is a second, independent bound because this reads the body
// BEFORE any handler has decided anything about it: a route that forgot
// its own cap must not become a way to make this middleware hold an
// unbounded request in memory.
const maxRecordedBodyBytes = 1 << 20

// recordActions is the middleware. It runs the request, then records what
// happened.
//
// After, not before, deliberately: the interesting half of an action is
// how it ended, and a line written before the handler ran could only say
// what was asked. The route pattern is also only known afterwards, because
// chi fills it in while routing.
func recordActions(recorder ActionRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if recorder == nil || r.Method == http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}

			body := drainBody(r)
			selfAudited := &atomic.Bool{}
			note := &gateNote{}
			r = r.WithContext(context.WithValue(context.WithValue(r.Context(), selfAuditedKey{}, selfAudited), gateNoteKey{}, note))
			capture := &capturingWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(capture, r)

			route := routePatternOf(r)
			if selfAudited.Load() {
				return
			}
			if route == "" {
				// Nothing matched, so there is no action to name: a 404
				// on a path this API does not have is not an operator
				// doing something, it is a client that is lost.
				return
			}
			line := cliecho.Echo(cliecho.Action{
				Method: r.Method,
				Route:  route,
				Params: routeParamsOf(r),
				Query:  r.URL.Query(),
				Body:   body,
			})
			code, message := capture.refusal()
			setID := backupSetOfRoute(routeParamsOf(r))
			var operation, runID, snapshotID, holdID string
			if note.refused {
				var targetSet string
				operation, targetSet, runID, snapshotID, holdID = gateTarget(route, routeParamsOf(r), body)
				if note.operation != "" {
					operation = note.operation
				}
				if targetSet == "" {
					targetSet = note.backupSetID
				}
				if targetSet != "" {
					setID = targetSet
				}
				// A create whose run was refused answers 201, so no error
				// envelope was written and the status does not say it was
				// refused. The note does.
				if code == "" {
					code, message = "DESTRUCTIVE_OPERATIONS_DISABLED", destructiveGateRefusal
				}
			}
			action := cliecho.APIAction{
				Actor:       actorFromContext(r.Context()),
				Method:      r.Method,
				Route:       route,
				Status:      capture.status,
				ErrorCode:   code,
				Message:     message,
				BackupSetID: setID,
				Gap:         line.Gap,
				GapDetail:   line.GapDetail,
				Placeholder: line.Placeholder,
			}
			if note.refused {
				action.Gate, action.GateReason = note.state, note.reason
				action.Operation, action.RunID, action.SnapshotID, action.HoldID = operation, runID, snapshotID, holdID
			}
			// Releasing a hold lifts a protection, and it is off the
			// destructive gate (issue #1164), so no refusal note names it.
			// The feed is the record of who released which hold: the actor is
			// already on the entry, the line carries its own time, and this
			// adds the act and the hold, on success and on refusal alike.
			if !note.refused && route == "/operations" {
				if op, set, _, _, hold := gateTarget(route, routeParamsOf(r), body); op == "release_snapshot_hold" {
					action.Operation, action.HoldID = op, hold
					if set != "" {
						action.BackupSetID = set
					}
				}
			}
			// Bare: the "$ " a terminal draws in front of it is the
			// terminal's, and a script reading the journal wants a
			// command it can run, not a screen (see cliecho.Line.Shell).
			action.Command = line.Shell()
			recorder.RecordAPIAction(r.Context(), action)
		})
	}
}

// drainBody reads the request body so a command can be built from it, and
// puts it back so the handler still sees it.
//
// Bounded, and a body that will not read is simply not available to the
// command builder: this must never be the reason a request fails, because
// a request that succeeded and was not described is far better than one
// that was refused in order to describe it.
func drainBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxRecordedBodyBytes))
	if err != nil {
		// Hand the handler back what is left rather than what was read:
		// it will meet the same error and refuse with its own words.
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r.Body))
		return nil
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
	return buf
}

// routePatternOf is the pattern the request matched, with the /api/v1
// prefix off, which is the form core/cliecho's table is keyed by and the
// form api/v1/openapi.json names an operation with.
func routePatternOf(r *http.Request) string {
	ctx := chi.RouteContext(r.Context())
	if ctx == nil {
		return ""
	}
	return strings.TrimPrefix(ctx.RoutePattern(), "/api/v1")
}

func routeParamsOf(r *http.Request) map[string]string {
	ctx := chi.RouteContext(r.Context())
	if ctx == nil {
		return nil
	}
	out := make(map[string]string, len(ctx.URLParams.Keys))
	for i, k := range ctx.URLParams.Keys {
		if i < len(ctx.URLParams.Values) {
			out[k] = ctx.URLParams.Values[i]
		}
	}
	return out
}

// backupSetOfRoute reports the backup set an action was about, from the
// route's own parameters.
//
// It is what decides whether a line lands on a set's strip or on the
// deployment's feed, and it reads the route rather than the body on
// purpose: a route that names {source} and {set} is about that set
// whatever its body says, and a route that does not is not about a set at
// all.
func backupSetOfRoute(params map[string]string) string {
	source, set := params["source"], params["set"]
	if source == "" || set == "" {
		return ""
	}
	return source + "/" + set
}

// capturingWriter watches the status and, for a refusal, the error
// envelope this package writes, so the recorded line can carry the reason
// rather than only the number.
type capturingWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

// Unwrap lets http.ResponseController reach the real writer. Without it a
// handler behind this middleware cannot flush or move its write deadline,
// which a streamed answer (POST /recoveries) needs: the first line would sit
// in the server's buffer until the whole run had ended.
func (c *capturingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *capturingWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *capturingWriter) Write(p []byte) (int, error) {
	// Only a refusal's body is worth keeping, and only as much of it as
	// an error envelope can be. A successful response is the caller's
	// business and may be large.
	if c.status >= http.StatusBadRequest && c.body.Len() < 4096 {
		c.body.Write(p)
	}
	return c.ResponseWriter.Write(p)
}

// refusal reads the code and message out of the error envelope, or
// returns empty for a response that is not one.
func (c *capturingWriter) refusal() (string, string) {
	if c.status < http.StatusBadRequest {
		return "", ""
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(c.body.Bytes(), &envelope); err != nil || envelope.Error.Code == "" {
		return "HTTP_" + strconv.Itoa(c.status), http.StatusText(c.status)
	}
	return envelope.Error.Code, envelope.Error.Message
}
