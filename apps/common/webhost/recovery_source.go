package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/service"
)

// The "Source host (SFTP)" recovery destination over HTTP (issues #1191 and
// #1193).
//
// # Why the source target is gated and the others are not
//
// handlers_recovery.go keeps recovery off the destructive gate because a
// recovery reads and overwrites nothing. That stays true of a download, a
// staging file and another storage destination. It is not true of the source
// target: it writes onto the production host the backup came from. So that
// one kind goes through what #1163 asks of a restore, on every route that can
// carry it (the mint, POST /recoveries, and the redeem that does the write),
// through the same decide every other gated act uses: the gate, the transport
// rule, an acknowledgement of the exact destination in the request
// (target.acknowledged_destination, refused 409
// RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED like every other consent) and the
// password again. Every other target is let through untouched, which is what
// keeps a recovery to staging working on a shut gate. openapi.json says this
// with x-destructive-gate-target-kinds, and router_test.go holds the two to
// each other.
//
// Which bodies write onto a source host is not decided here: the service
// says (service.RecoveryTargetWritesToSourceHost), so a new kind that writes
// there cannot slip past the gate by having another name.
//
// # Describe on open, check on intent
//
// GET .../source-host reads configuration and the cached write check, so a
// dialog may call it on open. POST .../source-host/write-check writes and
// removes one probe file on the host, so it runs only when the operator asks,
// and it is refused on a shut gate before anything is written.
//
// # One decode
//
// The body is decoded once, by recoveryRequest, and the gate and the handler
// both read that one value, the same way POST /operations does since #1167's
// review. There is no peek for a crafted body to make disagree with what
// runs.

// sourceHostRetryAfterSeconds is what a 503 RECOVERY_SOURCE_HOST_UNREACHABLE
// asks a client to wait before trying again.
const sourceHostRetryAfterSeconds = 30

// parsedRecovery is a recovery body, decoded once for the whole request.
type parsedRecovery[T any] struct {
	body T
	// err is the decode's own error, reported by the handler in its words.
	err error
}

// parsedRecoveryKey is typed per body, so one request can never hand a body
// decoded as one type to a reader asking for another.
type parsedRecoveryKey[T any] struct{}

// consumedRecoveryBody marks a request whose body one recoveryRequest has
// already read, whatever the type.
type consumedRecoveryBody struct{}

var errRecoveryBodyAlreadyRead = errors.New("webhost: this request's body was already decoded as another type")

// recoveryRequest is the decoded body of this recovery request, decoding it
// the first time it is asked for (with the route's own limit, and refusing
// unknown fields as the handlers always have) and handing back a request
// that carries it. An empty body decodes to the zero value. Asking again for
// another type is an error, never a zero value: the body is gone.
func recoveryRequest[T any](w http.ResponseWriter, r *http.Request, limit int64) (*parsedRecovery[T], *http.Request) {
	if p, ok := r.Context().Value(parsedRecoveryKey[T]{}).(*parsedRecovery[T]); ok {
		return p, r
	}
	p := &parsedRecovery[T]{}
	if r.Context().Value(consumedRecoveryBody{}) != nil {
		p.err = errRecoveryBodyAlreadyRead
		return p, r
	}
	src := r.Body
	if src == nil {
		src = http.NoBody
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, src, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p.body); err != nil && !errors.Is(err, io.EOF) {
		p.err = err
	}
	ctx := context.WithValue(r.Context(), parsedRecoveryKey[T]{}, p)
	ctx = context.WithValue(ctx, consumedRecoveryBody{}, true)
	return p, r.WithContext(ctx)
}

// sourceTargetOf reads what the gate needs off a decoded body: the target,
// the set it writes for, and whether this is a pre-flight that writes
// nothing.
type sourceTargetOf[T any] func(r *http.Request, body T) (target recoveryTargetBody, setID string, preflight bool)

// SourceHostDescriber is what the gate and the two source-host routes need
// from the backend.
type SourceHostDescriber interface {
	DescribeSourceHost(setID, folder string) (service.SourceHostDescription, error)
}

// requireSourceRecoveryGate gates a recovery that writes onto a source host,
// against the body the handler then runs. limit is the route's own body
// limit. A body that does not decode goes to the handler, which refuses it.
func requireSourceRecoveryGate[T any](e gateEnforcement, backend any, limit int64, of sourceTargetOf[T]) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, r := recoveryRequest[T](w, r, limit)
			if p.err != nil {
				next.ServeHTTP(w, r)
				return
			}
			target, setID, preflight := of(r, p.body)
			if preflight || !service.RecoveryTargetWritesToSourceHost(target.service()) {
				next.ServeHTTP(w, r)
				return
			}
			act := gatedAct{sourceRecovery: &sourceRecoveryAct{acknowledged: target.AcknowledgedDestination}}
			if d, ok := backend.(SourceHostDescriber); ok && setID != "" {
				// The gate compares the acknowledgement too, before it asks
				// for the password: a request for a destination the
				// configuration no longer resolves to is refused without
				// spending a step-up check. The service compares again at
				// mint and before every upload.
				if desc, err := d.DescribeSourceHost(setID, target.Path); err == nil && desc.Available && target.Path != "" {
					act.sourceRecovery.current = desc.DestinationID()
					act.sourceRecovery.url = desc.URL()
				}
			}
			if ref, refused := e.decide(r, act); refused {
				ref.write(w, r, "recover to source host")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func mintSourceTarget(r *http.Request, b recoveryMintRequest) (recoveryTargetBody, string, bool) {
	return b.Target, chi.URLParam(r, "source") + "/" + chi.URLParam(r, "set"), false
}

func recoverManySourceTarget(_ *http.Request, b recoverManyRequestBody) (recoveryTargetBody, string, bool) {
	setID := b.Set
	if setID == "" && len(b.Artifacts) > 0 {
		if parts := strings.SplitN(b.Artifacts[0], "/", 3); len(parts) == 3 {
			setID = parts[0] + "/" + parts[1]
		}
	}
	return b.Target, setID, b.Preflight
}

// SourceGrantInspector says whether a grant writes onto a source host.
type SourceGrantInspector interface {
	RecoveryGrantWritesToSourceHost(token string) bool
}

// requireSourceGrantGate puts the redeem of a source-host grant behind the
// shut gate again (#1193): the redeem is where the upload happens, and a
// grant minted while the gate was open must not write once it has shut.
// Every other grant reads and overwrites nothing and is let through.
func requireSourceGrantGate(e gateEnforcement, backend any) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			insp, ok := backend.(SourceGrantInspector)
			if ok && insp.RecoveryGrantWritesToSourceHost(chi.URLParam(r, "grant")) && !destructiveGatePassed(e.gate) {
				noteGateRefusal(r.Context(), e.gate, "")
				writeDestructiveGateDenied(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SourceHostChecker is what the write-check route needs from the backend.
type SourceHostChecker interface {
	CheckSourceHostWrite(ctx context.Context, actor, setID, folder string) (service.SourceHostDescription, error)
}

var (
	_ SourceHostDescriber  = (*service.BackupService)(nil)
	_ SourceHostChecker    = (*service.BackupService)(nil)
	_ SourceGrantInspector = (*service.BackupService)(nil)
)

type sourceHostBody struct {
	SetID         string `json:"set_id"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
	Host          string `json:"host,omitempty"`
	Port          int    `json:"port,omitempty"`
	User          string `json:"user,omitempty"`
	RemotePath    string `json:"remote_path,omitempty"`
	Folder        string `json:"folder,omitempty"`
	URL           string `json:"url,omitempty"`
	DestinationID string `json:"destination_id,omitempty"`
	ReadOnly      bool   `json:"read_only,omitempty"`
	WriteAccess   string `json:"write_access"`
	Code          string `json:"code,omitempty"`
	Detail        string `json:"detail,omitempty"`
	CheckedAt     string `json:"checked_at,omitempty"`
	LeftFile      string `json:"left_file,omitempty"`
	FreeBytes     *int64 `json:"free_bytes,omitempty"`
}

func sourceHostBodyOf(d service.SourceHostDescription) sourceHostBody {
	b := sourceHostBody{
		SetID: d.SetID, Available: d.Available, Reason: d.Reason, Host: d.Host, Port: d.Port, User: d.User,
		RemotePath: d.RemotePath, Folder: d.Folder, URL: d.URL(), DestinationID: d.DestinationID(),
		ReadOnly: d.ReadOnly, WriteAccess: d.WriteAccess, Code: d.Code, Detail: d.Detail, LeftFile: d.LeftFile,
		FreeBytes: d.FreeBytes,
	}
	if !d.CheckedAt.IsZero() {
		b.CheckedAt = d.CheckedAt.UTC().Format(time.RFC3339)
	}
	if b.WriteAccess == "" {
		b.WriteAccess = service.SourceWriteNotChecked
	}
	return b
}

// writeSourceHostError answers a describe or a write check that failed.
func (h *handlers) writeSourceHostError(w http.ResponseWriter, r *http.Request, err error) {
	var limited *service.SourceWriteCheckRateLimited
	switch {
	case errors.Is(err, service.ErrBackupSetNotFound):
		writeError(w, http.StatusNotFound, "BACKUP_SET_NOT_FOUND", "no such backup set")
	case errors.As(err, &limited):
		secs := int(limited.RetryAfter.Round(time.Second) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many write checks ran in the last minute, so this one did not run and nothing was written; try again in "+strconv.Itoa(secs)+" seconds")
	default:
		h.writeRecoveryError(w, r, err)
	}
}

// describeSourceHost is GET /api/v1/backup-sets/{source}/{set}/source-host:
// configuration and the cached write check, never the network.
func (h *handlers) describeSourceHost(w http.ResponseWriter, r *http.Request) {
	d, ok := h.backend.(SourceHostDescriber)
	if !ok {
		writeError(w, http.StatusNotImplemented, "RECOVERY_UNAVAILABLE", "this deployment cannot recover backups")
		return
	}
	desc, err := d.DescribeSourceHost(chi.URLParam(r, "source")+"/"+chi.URLParam(r, "set"), r.URL.Query().Get("folder"))
	if err != nil {
		h.writeSourceHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sourceHostBodyOf(desc))
}

// checkSourceHostWrite is POST /api/v1/backup-sets/{source}/{set}/
// source-host/write-check: #852's write probe, on the operator's say-so,
// behind CSRF and refused by a shut gate before anything is written
// (requireDestructiveGate, router.go). The service caches, shares and rate
// limits it, and audits every probe.
func (h *handlers) checkSourceHostWrite(w http.ResponseWriter, r *http.Request) {
	c, ok := h.backend.(SourceHostChecker)
	if !ok {
		writeError(w, http.StatusNotImplemented, "RECOVERY_UNAVAILABLE", "this deployment cannot recover backups")
		return
	}
	desc, err := c.CheckSourceHostWrite(r.Context(), actorFromContext(r.Context()), chi.URLParam(r, "source")+"/"+chi.URLParam(r, "set"), r.URL.Query().Get("folder"))
	if err != nil {
		h.writeSourceHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sourceHostBodyOf(desc))
}
