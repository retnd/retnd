package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/service"
)

// Artifact recovery over HTTP (EPIC Q, #976, FR-67 and FR-68): a POST that
// mints a short-lived, single-use grant and a GET that redeems it.
//
// # Why a pair, and why not POST /operations
//
// POST /api/v1/operations is wrapped in requireCSRF and
// requireDestructiveGate, and the only gate this repository ships
// (NotYetImplementedGate) never passes. A recovery submitted there would
// be refused on every deployment this repository can build. A recovery
// reads: it deletes nothing and overwrites nothing. So the mint carries
// requireCSRF and NOT requireDestructiveGate, in the tier
// handlers_artifacts.go already uses for actions with no effect on backup
// data, and the redeem is a GET, which is why it carries neither.
//
// # Who may read
//
// There is no role on capabilities.AuthContext, so any authenticated
// caller can recover any artifact, as any authenticated caller can hold
// any backup set. The answer is audit and a bound grant, not a role model:
// the grant belongs to the session that minted it and the service writes a
// row for every mint and every served range.
//
// # The streaming response
//
// 200 or 206 with application/octet-stream, an attachment disposition,
// nosniff and no-store. One Range per request; several in one are refused,
// because the audit row names a range and a multipart answer has none.

// RecoveryClient is what the two recovery routes need from the backend.
// It is separate from BackupServiceClient so that a backend without
// recovery is a 501 on these two routes and not a compile error in every
// fake.
type RecoveryClient interface {
	MintRecovery(ctx context.Context, sess service.RecoverySession, req service.RecoveryRequest) (service.RecoveryGrant, error)
	RedeemRecovery(ctx context.Context, sess service.RecoverySession, token string) (service.RecoveryStream, *service.RecoveryResult, error)
}

var _ RecoveryClient = (*service.BackupService)(nil)

// maxRecoveryMintBodyBytes bounds the mint body: a copy id, a target and a
// flag, nothing that needs more than a few hundred bytes.
const maxRecoveryMintBodyBytes = 8 << 10

type recoveryTargetBody struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Medium string `json:"medium,omitempty"`
	Key    string `json:"key,omitempty"`
}

type recoveryMintRequest struct {
	Copy            string             `json:"copy,omitempty"`
	AcknowledgeCost bool               `json:"acknowledge_cost,omitempty"`
	Target          recoveryTargetBody `json:"target"`
}

// recoveryMintResponse is both answers to a mint: a grant (201) or, when
// the only copy is archive-class and the request acknowledged the cost, the
// provider restore that was started in its place (202). One shape, with the
// half that does not apply absent.
type recoveryMintResponse struct {
	ArtifactID  string                 `json:"artifact_id"`
	Copy        string                 `json:"copy"`
	Grant       string                 `json:"grant,omitempty"`
	DownloadURL string                 `json:"download_url,omitempty"`
	ExpiresAt   string                 `json:"expires_at,omitempty"`
	Target      *recoveryTargetBody    `json:"target,omitempty"`
	SizeBytes   *int64                 `json:"size_bytes,omitempty"`
	Restore     *recoveryRestoreStatus `json:"restore,omitempty"`
}

type recoveryRestoreStatus struct {
	OperationID string `json:"operation_id"`
	Wait        string `json:"wait,omitempty"`
	Billing     string `json:"billing,omitempty"`
}

type recoveryResultResponse struct {
	ArtifactID string             `json:"artifact_id"`
	Copy       string             `json:"copy"`
	Target     recoveryTargetBody `json:"target"`
	Path       string             `json:"path,omitempty"`
	Key        string             `json:"key,omitempty"`
	Bytes      int64              `json:"bytes"`
	SHA256     string             `json:"sha256"`
}

func (h *handlers) recoveryBackend(w http.ResponseWriter) (RecoveryClient, bool) {
	rc, ok := h.backend.(RecoveryClient)
	if !ok {
		writeError(w, http.StatusNotImplemented, "RECOVERY_UNAVAILABLE", "this deployment cannot recover backups")
		return nil, false
	}
	return rc, true
}

// mintRecovery is POST /api/v1/backups/{source}/{set}/{name}/recovery.
func (h *handlers) mintRecovery(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.recoveryBackend(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRecoveryMintBodyBytes)
	var body recoveryMintRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeDecodeError(w, err, maxRecoveryMintBodyBytes)
		return
	}

	id := artifactIDFrom(r)
	// MintRecovery writes the action-log row itself (it knows the copy and
	// the code), so the generic one is not written as well.
	markSelfAudited(r.Context())
	grant, err := rc.MintRecovery(r.Context(), recoverySessionFrom(r), service.RecoveryRequest{
		ArtifactID:      id,
		Copy:            body.Copy,
		AcknowledgeCost: body.AcknowledgeCost,
		Target: service.RecoveryTarget{
			Kind: body.Target.Kind, Path: body.Target.Path, Medium: body.Target.Medium, Key: body.Target.Key,
		},
	})
	if err != nil {
		h.writeRecoveryError(w, r, err)
		return
	}
	if grant.Thaw != nil {
		writeJSON(w, http.StatusAccepted, recoveryMintResponse{
			ArtifactID: grant.ArtifactID, Copy: grant.Copy,
			Restore: &recoveryRestoreStatus{OperationID: grant.Thaw.OperationID, Wait: grant.Thaw.Wait, Billing: grant.Thaw.Billing},
		})
		return
	}
	writeJSON(w, http.StatusCreated, recoveryMintResponse{
		ArtifactID:  grant.ArtifactID,
		Copy:        grant.Copy,
		Grant:       grant.Token,
		DownloadURL: "/api/v1/recoveries/" + url.PathEscape(grant.Token),
		ExpiresAt:   grant.ExpiresAt.UTC().Format(time.RFC3339),
		Target:      &recoveryTargetBody{Kind: grant.Target.Kind, Path: grant.Target.Path, Medium: grant.Target.Medium, Key: grant.Target.Key},
		SizeBytes:   grant.SizeBytes,
	})
}

// redeemRecovery is GET /api/v1/recoveries/{grant}.
func (h *handlers) redeemRecovery(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.recoveryBackend(w)
	if !ok {
		return
	}
	// One range per request. Checked before the grant is touched, so a
	// request the audit row could not describe does not spend anything.
	if strings.Contains(r.Header.Get("Range"), ",") {
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "INVALID_REQUEST", "ask for one byte range at a time")
		return
	}

	stream, result, err := rc.RedeemRecovery(r.Context(), recoverySessionFrom(r), chi.URLParam(r, "grant"))
	if err != nil {
		h.writeRecoveryError(w, r, err)
		return
	}
	if result != nil {
		writeJSON(w, http.StatusOK, recoveryResultResponse{
			ArtifactID: result.ArtifactID, Copy: result.Copy,
			Target: recoveryTargetBody{Kind: result.Target.Kind, Path: result.Target.Path, Medium: result.Target.Medium, Key: result.Target.Key},
			Path:   result.Path, Key: result.Key, Bytes: result.Bytes, SHA256: result.SHA256,
		})
		return
	}
	defer stream.Close()

	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", contentDisposition(stream.FileName()))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "no-store")

	cw := &recoveryResponseWriter{ResponseWriter: w, status: http.StatusOK}
	http.ServeContent(cw, r, "", time.Time{}, stream.Reader())

	size := stream.Length()
	start, end := int64(0), size-1
	if cw.status == http.StatusPartialContent {
		start, end = parseContentRange(cw.Header().Get("Content-Range"), start, end)
	}
	var serr error
	switch {
	case cw.err != nil:
		serr = cw.err
	case r.Context().Err() != nil:
		serr = r.Context().Err()
	case cw.status >= 400:
		serr = errors.New("the range could not be served")
	case cw.status != http.StatusNotModified && cw.written != end-start+1:
		serr = io.ErrShortWrite
	}
	stream.Served(start, end, cw.written, serr)
}

// recoveryResponseWriter counts what was written and keeps the first write
// error, which is how a client that went away is told apart from one that
// received everything. http.ServeContent swallows write errors.
type recoveryResponseWriter struct {
	http.ResponseWriter
	status  int
	written int64
	err     error
}

func (c *recoveryResponseWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *recoveryResponseWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.written += int64(n)
	if err != nil && c.err == nil {
		c.err = err
	}
	return n, err
}

// parseContentRange reads "bytes a-b/n" and falls back to the given
// values when it cannot.
func parseContentRange(v string, fallbackStart, fallbackEnd int64) (int64, int64) {
	v = strings.TrimPrefix(v, "bytes ")
	span, _, _ := strings.Cut(v, "/")
	a, b, ok := strings.Cut(span, "-")
	if !ok {
		return fallbackStart, fallbackEnd
	}
	s, err1 := strconv.ParseInt(a, 10, 64)
	e, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil {
		return fallbackStart, fallbackEnd
	}
	return s, e
}

// contentDisposition builds an attachment header from an artifact's name
// without ever letting that name reach the header unescaped: an ASCII
// fallback with anything unusual replaced, and the exact name as RFC 5987
// percent-encoding.
func contentDisposition(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' && r != ';' && r != '%' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return `attachment; filename="` + b.String() + `"; filename*=UTF-8''` + url.PathEscape(name)
}

// recoverySessionFrom is who is asking, from the authentication the
// request already passed.
func recoverySessionFrom(r *http.Request) service.RecoverySession {
	auth := authContextFrom(r.Context())
	sess := service.RecoverySession{Actor: auth.Username}
	if auth.SessionExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, auth.SessionExpiresAt); err == nil {
			sess.Expires = t
		}
	}
	return sess
}

// recoveryStatuses maps each code the service can refuse with to its HTTP
// status. A code absent here is a 500, and a test holds this table to
// service.RecoveryCodes so one cannot be added without a decision.
var recoveryStatuses = map[string]int{
	"RECOVERY_COST_NOT_ACKNOWLEDGED": http.StatusConflict,
	"RECOVERY_NO_COPY":               http.StatusConflict,
	"RECOVERY_COPY_NOT_FOUND":        http.StatusNotFound,
	"RECOVERY_NOT_READABLE":          http.StatusConflict,
	"RECOVERY_LOCAL_COPY_CORRUPT":    http.StatusInternalServerError,
	"RECOVERY_REMOTE_COPY_CORRUPT":   http.StatusBadGateway,
	"RECOVERY_NO_DIGEST":             http.StatusConflict,
	"RECOVERY_NO_SPACE":              http.StatusInsufficientStorage,
	"RECOVERY_BELOW_RESERVE":         http.StatusInsufficientStorage,
	"RECOVERY_GRANT_UNKNOWN":         http.StatusNotFound,
	"RECOVERY_GRANT_USED":            http.StatusGone,
	"RECOVERY_GRANT_EXPIRED":         http.StatusGone,
	"RECOVERY_GRANT_SESSION_ENDED":   http.StatusForbidden,
	"RECOVERY_TOO_MANY_GRANTS":       http.StatusTooManyRequests,
	"RECOVERY_TARGET_INVALID":        http.StatusBadRequest,
	"RECOVERY_TARGET_EXISTS":         http.StatusConflict,
	"RECOVERY_USE_SNAPSHOT_RESTORE":  http.StatusConflict,
	"RECOVERY_UNAVAILABLE":           http.StatusServiceUnavailable,
	"RECOVERY_BUSY":                  http.StatusConflict,
	"RECOVERY_INTERRUPTED":           http.StatusServiceUnavailable,
	"RECOVERY_FAILED":                http.StatusInternalServerError,
}

func (h *handlers) writeRecoveryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, service.ErrArtifactNotFound) {
		writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "no such backup")
		return
	}
	if re, ok := service.AsRecoveryError(err); ok {
		status, known := recoveryStatuses[re.Code]
		if !known {
			status = http.StatusInternalServerError
		}
		writeError(w, status, re.Code, re.Message)
		return
	}
	h.internalError(w, r, "INTERNAL", "the recovery failed", err)
}
