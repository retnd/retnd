package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/service"
)

// The "Source host (SFTP)" recovery destination over HTTP (issue #1191).
//
// # Why the source target is gated and the others are not
//
// handlers_recovery.go keeps recovery off the destructive gate because a
// recovery reads and overwrites nothing. That stays true of a download, a
// staging file and another storage destination. It is not true of the source
// target: it writes onto the production host the backup came from. So that
// one kind, on the two routes that can carry it, goes through what #1163 asks
// of a restore, through the same decide every other gated act uses: the gate,
// the transport rule (which reads serve-ui's verdict about the browser), an
// explicit acknowledgement in the request (target.acknowledged, refused 409
// RECOVERY_SOURCE_NOT_ACKNOWLEDGED like every other consent) and the password
// again (403, 429 RATE_LIMITED when the check is throttled, 503 when there is
// nothing to check it with). Every other target is let through untouched,
// which is what keeps a recovery to staging working on a shut gate.
//
// A pre-flight of a set (preflight: true) writes nothing and is not gated, so
// the dialog can show the plan before anyone types a password.
//
// # One decode
//
// The body is decoded once, by recoveryRequest, and the gate and the handler
// both read that one value, the same way POST /operations does since #1167's
// review. There is no peek for a crafted body to make disagree with what
// runs.

// parsedRecovery is a recovery body, decoded once for the whole request.
type parsedRecovery[T any] struct {
	body T
	// err is the decode's own error, reported by the handler in its words.
	err error
}

type parsedRecoveryKey struct{}

// recoveryRequest is the decoded body of this recovery request, decoding it
// the first time it is asked for (with the route's own limit, and refusing
// unknown fields as the handlers always have) and handing back a request
// that carries it. An empty body decodes to the zero value.
func recoveryRequest[T any](w http.ResponseWriter, r *http.Request, limit int64) (*parsedRecovery[T], *http.Request) {
	if p, ok := r.Context().Value(parsedRecoveryKey{}).(*parsedRecovery[T]); ok {
		return p, r
	}
	p := &parsedRecovery[T]{}
	src := r.Body
	if src == nil {
		src = http.NoBody
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, src, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p.body); err != nil && !errors.Is(err, io.EOF) {
		p.err = err
	}
	return p, r.WithContext(context.WithValue(r.Context(), parsedRecoveryKey{}, p))
}

// sourceTargetOf reads what the gate needs off a decoded body: the target,
// and whether this is a pre-flight that writes nothing.
type sourceTargetOf[T any] func(T) (target recoveryTargetBody, preflight bool)

// requireSourceRecoveryGate gates a recovery whose target is the source host,
// against the body the handler then runs. limit is the route's own body
// limit. A body that does not decode goes to the handler, which refuses it.
func requireSourceRecoveryGate[T any](e gateEnforcement, limit int64, of sourceTargetOf[T]) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, r := recoveryRequest[T](w, r, limit)
			if p.err != nil {
				next.ServeHTTP(w, r)
				return
			}
			target, preflight := of(p.body)
			if target.Kind != "source" || preflight {
				next.ServeHTTP(w, r)
				return
			}
			act := gatedAct{sourceRecovery: &sourceRecoveryAct{acknowledged: target.Acknowledged}}
			if ref, refused := e.decide(r, act); refused {
				ref.write(w, r, "recover to source host")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func mintSourceTarget(b recoveryMintRequest) (recoveryTargetBody, bool) { return b.Target, false }

func recoverManySourceTarget(b recoverManyRequestBody) (recoveryTargetBody, bool) {
	return b.Target, b.Preflight
}

// RecoverySourceClient is what the preflight route needs from the backend.
type RecoverySourceClient interface {
	PreflightRecoverySource(ctx context.Context, setID string) (service.RecoverySourceDestination, error)
}

var _ RecoverySourceClient = (*service.BackupService)(nil)

type recoverySourceBody struct {
	SetID      string `json:"set_id"`
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`
	Folder     string `json:"folder,omitempty"`
	URL        string `json:"url,omitempty"`
	Writable   string `json:"writable,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// preflightRecoverySource is POST /api/v1/backup-sets/{source}/{set}/
// recovery-source/preflight. A POST and not a GET because it writes and
// removes one probe file on the source (#852's write probe); CSRF, and not
// the destructive gate, the tier the connection test that runs the same
// probe already sits in.
func (h *handlers) preflightRecoverySource(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.backend.(RecoverySourceClient)
	if !ok {
		writeError(w, http.StatusNotImplemented, "RECOVERY_UNAVAILABLE", "this deployment cannot recover backups")
		return
	}
	setID := chi.URLParam(r, "source") + "/" + chi.URLParam(r, "set")
	d, err := rc.PreflightRecoverySource(r.Context(), setID)
	if err != nil {
		h.writeRecoveryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recoverySourceBody{
		SetID: d.SetID, Available: d.Available, Reason: d.Reason, Host: d.Host, Port: d.Port, User: d.User,
		RemotePath: d.RemotePath, Folder: d.Folder, URL: d.URL(), Writable: d.Writable, Detail: d.Detail,
	})
}
