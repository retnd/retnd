package webhost

import (
	"bytes"
	"context"
	"encoding/json"
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
// of a restore: the gate, the insecure-transport acknowledgement, an explicit
// acknowledgement in the request (target.acknowledged) and the password again.
// Every other target is let through untouched, which is what keeps a recovery
// to staging working on a shut gate.
//
// A pre-flight of a set (preflight: true) writes nothing and is not gated, so
// the dialog can show the plan before anyone types a password.
//
// The target is read out of the body before the handler decodes it, so the
// two must agree on what the target is. Both use encoding/json on the first
// JSON value of the body, with the same field names, so case-insensitive keys
// and a later duplicate read the same way in both. A body over the route's
// own limit is passed to the handler, which refuses it as too large.

// sourceTargetPeek is the part of a mint or a set-recovery body the gate
// reads.
type sourceTargetPeek struct {
	Preflight bool `json:"preflight"`
	Target    struct {
		Kind         string `json:"kind"`
		Acknowledged bool   `json:"acknowledged"`
	} `json:"target"`
}

// requireSourceTargetGate gates a request whose target is the source host.
// limit is the route's own body limit.
func requireSourceTargetGate(e gateEnforcement, limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil {
				next.ServeHTTP(w, r)
				return
			}
			buf, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
			if err != nil || int64(len(buf)) > limit {
				next.ServeHTTP(w, r)
				return
			}
			var peek sourceTargetPeek
			if derr := json.NewDecoder(bytes.NewReader(buf)).Decode(&peek); derr != nil || peek.Target.Kind != "source" || peek.Preflight {
				next.ServeHTTP(w, r)
				return
			}
			if !destructiveGatePassed(e.gate) {
				noteGateRefusal(r.Context(), e.gate, "recover to source host")
				writeDestructiveGateDenied(w)
				return
			}
			if e.insecureTransportAckNeeded(r) && !insecureTransportAcknowledged(r) {
				refuseInsecureTransport(w)
				return
			}
			if _, stepUp := conditionsOf(e.gate); stepUp {
				if !peek.Target.Acknowledged {
					writeError(w, http.StatusBadRequest, "ACKNOWLEDGEMENT_REQUIRED",
						"recovering to the source host writes onto the machine this backup came from, so the request has to say you understand that: set acknowledged to true in its target")
					return
				}
				if !e.checkStepUp(w, r, "a recovery onto the source host", "retnd recover --acknowledge-source-write") {
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
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
