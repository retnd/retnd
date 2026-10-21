package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/retnd/retnd/core/service"
)

// Recovering several backups, or a whole backup set, to one destination over
// HTTP (#1106, #1107): POST /api/v1/recoveries.
//
// It is the same recovery as the single-backup pair, many times over: the
// service plans the whole request first (cost, space, what cannot be
// recovered), refuses early with the numbers, and then recovers file by file
// through the code a single recovery uses. This file only carries it across
// the boundary.
//
// # Why a stream, and why no browser download
//
// A run is minutes long for a large set, so the answer is a stream of JSON
// lines (application/x-ndjson): one `plan` line once the pre-flight passed,
// a `start` and an `item` line per file with a running total, and one
// `result` line last. A refusal from the pre-flight comes before the first
// line and is an ordinary error response, so a client never has to look for
// an error inside a 200. Closing the connection cancels the run: the request
// context is the service's context, the file in flight is discarded, and the
// manifest still records what happened.
//
// Many files do not fit one browser download without an archive that the
// server would have to build and keep, so this route does not offer a
// download target: a set goes to a folder under the staging root or to another
// storage destination, and the refusal says so.
//
// The route carries requireCSRF and not the destructive gate, for the reason
// handlers_recovery.go gives: it reads, it deletes and overwrites nothing, and
// it is audited by the service instead.

// RecoverManyClient is what POST /api/v1/recoveries needs from the backend.
// Separate from RecoveryClient so a backend that cannot recover answers 501.
type RecoverManyClient interface {
	PlanRecoverMany(ctx context.Context, req service.RecoverManyRequest) (service.RecoverManyPlan, error)
	RecoverMany(ctx context.Context, sess service.RecoverySession, req service.RecoverManyRequest, progress func(service.RecoverManyProgress)) (service.RecoverManyResult, error)
}

var _ RecoverManyClient = (*service.BackupService)(nil)

// maxRecoverManyBodyBytes bounds the body: a few thousand ids at most.
const maxRecoverManyBodyBytes = 1 << 20

type recoverManyRequestBody struct {
	Set             string             `json:"set,omitempty"`
	Artifacts       []string           `json:"artifacts,omitempty"`
	NewestOnly      bool               `json:"newest_only,omitempty"`
	Since           string             `json:"since,omitempty"`
	Until           string             `json:"until,omitempty"`
	All             bool               `json:"all,omitempty"`
	Copy            string             `json:"copy,omitempty"`
	AcknowledgeCost bool               `json:"acknowledged,omitempty"`
	Preflight       bool               `json:"preflight,omitempty"`
	Target          recoveryTargetBody `json:"target"`
}

type recoverManyItemBody struct {
	ArtifactID string                 `json:"artifact_id"`
	Name       string                 `json:"name"`
	Status     string                 `json:"status"`
	Copy       string                 `json:"copy,omitempty"`
	SizeBytes  *int64                 `json:"size_bytes,omitempty"`
	Chargeable bool                   `json:"chargeable,omitempty"`
	Bytes      int64                  `json:"bytes,omitempty"`
	SHA256     string                 `json:"sha256,omitempty"`
	Path       string                 `json:"path,omitempty"`
	Key        string                 `json:"key,omitempty"`
	File       string                 `json:"file,omitempty"`
	Code       string                 `json:"code,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Restore    *recoveryRestoreStatus `json:"restore,omitempty"`
	Verified   string                 `json:"verified,omitempty"`
}

type recoverManyTotalsBody struct {
	Count         int   `json:"count"`
	Ready         int   `json:"ready"`
	Recovered     int   `json:"recovered"`
	Failed        int   `json:"failed"`
	Skipped       int   `json:"skipped"`
	NeedsThaw     int   `json:"needs_thaw"`
	Quarantined   int   `json:"quarantined"`
	Unrecoverable int   `json:"unrecoverable"`
	Bytes         int64 `json:"bytes"`
}

type recoverManyPlanBody struct {
	Set             string                `json:"set,omitempty"`
	Target          recoveryTargetBody    `json:"target"`
	Folder          string                `json:"folder"`
	Items           []recoverManyItemBody `json:"items"`
	Totals          recoverManyTotalsBody `json:"totals"`
	ChargeableCount int                   `json:"chargeable_count"`
	ChargeableBytes int64                 `json:"chargeable_bytes"`
	UnknownSizes    int                   `json:"unknown_sizes"`
	FreeBytes       *int64                `json:"free_bytes,omitempty"`
	ReserveBytes    int64                 `json:"reserve_bytes"`
	// For a source target (#1193).
	DestinationID      string `json:"destination_id,omitempty"`
	URL                string `json:"url,omitempty"`
	SourceFreeBytes    *int64 `json:"source_free_bytes,omitempty"`
	SourceReserveBytes int64  `json:"source_reserve_bytes,omitempty"`
	// Refusal is set on a pre-flight answer when the run itself would be
	// refused; the plan beside it is what the refusal is about.
	Refusal *recoverManyErrorBody `json:"refusal,omitempty"`
}

type recoverManyResultBody struct {
	OperationID    string                `json:"operation_id"`
	Set            string                `json:"set,omitempty"`
	Target         recoveryTargetBody    `json:"target"`
	Folder         string                `json:"folder"`
	Items          []recoverManyItemBody `json:"items"`
	Totals         recoverManyTotalsBody `json:"totals"`
	Cancelled      bool                  `json:"cancelled"`
	ManifestPath   string                `json:"manifest_path,omitempty"`
	ManifestKey    string                `json:"manifest_key,omitempty"`
	ManifestSHA256 string                `json:"manifest_sha256,omitempty"`
	DestinationID  string                `json:"destination_id,omitempty"`
	URL            string                `json:"url,omitempty"`
}

// recoverManyEvent is one line of the stream.
type recoverManyEvent struct {
	Event      string                 `json:"event"`
	Index      int                    `json:"index,omitempty"`
	Total      int                    `json:"total,omitempty"`
	Item       *recoverManyItemBody   `json:"item,omitempty"`
	Plan       *recoverManyPlanBody   `json:"plan,omitempty"`
	Result     *recoverManyResultBody `json:"result,omitempty"`
	DoneBytes  *int64                 `json:"done_bytes,omitempty"`
	TotalBytes *int64                 `json:"total_bytes,omitempty"`
	Error      *recoverManyErrorBody  `json:"error,omitempty"`
}

type recoverManyErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func itemBody(it service.RecoverManyItem) recoverManyItemBody {
	b := recoverManyItemBody{
		ArtifactID: it.ArtifactID, Name: it.Name, Status: it.Status, Copy: it.Copy, SizeBytes: it.SizeBytes,
		Chargeable: it.Chargeable, Bytes: it.Bytes, SHA256: it.SHA256, Path: it.Path, Key: it.Key, File: it.Rel,
		Code: it.Code, Message: it.Message, Verified: it.Verified,
	}
	if it.Thaw != nil {
		b.Restore = &recoveryRestoreStatus{OperationID: it.Thaw.OperationID, Wait: it.Thaw.Wait, Billing: it.Thaw.Billing}
	}
	return b
}

func itemsBody(items []service.RecoverManyItem) []recoverManyItemBody {
	out := make([]recoverManyItemBody, 0, len(items))
	for _, it := range items {
		out = append(out, itemBody(it))
	}
	return out
}

func totalsBody(t service.RecoverManyTotals) recoverManyTotalsBody {
	return recoverManyTotalsBody{
		Count: t.Count, Ready: t.Ready, Recovered: t.Recovered, Failed: t.Failed, Skipped: t.Skipped,
		NeedsThaw: t.NeedsThaw, Quarantined: t.Quarantined, Unrecoverable: t.Unrecoverable, Bytes: t.Bytes,
	}
}

func targetBody(t service.RecoveryTarget) recoveryTargetBody {
	return recoveryTargetBody{Kind: t.Kind, Path: t.Path, Medium: t.Medium, Key: t.Key}
}

func planBody(p service.RecoverManyPlan) *recoverManyPlanBody {
	return &recoverManyPlanBody{
		Set: p.Set, Target: targetBody(p.Target), Folder: p.Folder, Items: itemsBody(p.Items), Totals: totalsBody(p.Totals),
		ChargeableCount: p.ChargeableCount, ChargeableBytes: p.ChargeableBytes, UnknownSizes: p.UnknownSizes,
		FreeBytes: p.FreeBytes, ReserveBytes: p.ReserveBytes,
		DestinationID: p.DestinationID, URL: p.URL, SourceFreeBytes: p.SourceFreeBytes, SourceReserveBytes: p.SourceReserveBytes,
	}
}

func resultBody(r service.RecoverManyResult) *recoverManyResultBody {
	return &recoverManyResultBody{
		OperationID: r.OperationID, Set: r.Set, Target: targetBody(r.Target), Folder: r.Folder, Items: itemsBody(r.Items),
		Totals: totalsBody(r.Totals), Cancelled: r.Cancelled, ManifestPath: r.ManifestPath, ManifestKey: r.ManifestKey,
		ManifestSHA256: r.ManifestSHA256, DestinationID: r.DestinationID, URL: r.URL,
	}
}

// recoverMany is POST /api/v1/recoveries.
func (h *handlers) recoverMany(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.backend.(RecoverManyClient)
	if !ok {
		writeError(w, http.StatusNotImplemented, "RECOVERY_UNAVAILABLE", "this deployment cannot recover backups")
		return
	}
	// Decoded once: the source-host gate in front of this handler read this
	// same value (recovery_source.go).
	parsed, r := recoveryRequest[recoverManyRequestBody](w, r, maxRecoverManyBodyBytes)
	if parsed.err != nil {
		writeDecodeError(w, parsed.err, maxRecoverManyBodyBytes)
		return
	}
	body := parsed.body
	if (body.Set == "") == (len(body.Artifacts) == 0) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "name either one backup set (set) or a list of backups (artifacts), not both and not neither")
		return
	}
	req := service.RecoverManyRequest{
		Artifacts: body.Artifacts, Set: body.Set, NewestOnly: body.NewestOnly, All: body.All, Copy: body.Copy,
		Target:          body.Target.service(),
		AcknowledgeCost: body.AcknowledgeCost,
	}
	if body.Since != "" {
		t, err := time.Parse(time.RFC3339, body.Since)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "since must be an RFC 3339 time")
			return
		}
		req.Since = t
	}
	if body.Until != "" {
		t, err := time.Parse(time.RFC3339, body.Until)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "until must be an RFC 3339 time")
			return
		}
		req.Until = t
	}
	// The terminal refuses an inverted range as a typo, and so does the
	// route, rather than answer it with nothing (#1251).
	if !req.Since.IsZero() && !req.Until.IsZero() && req.Until.Before(req.Since) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "until is before since, so nothing can be in that range")
		return
	}
	// Not self-audited: the action-log middleware records the request with
	// the `retnd recover ...` command it is equivalent to, which is what the
	// terminal prints, and the service writes its own row with the totals.

	if body.Preflight {
		plan, err := rc.PlanRecoverMany(r.Context(), req)
		if err != nil {
			// A pre-flight that can say what it found and why the run would
			// be refused answers with both: the plan is the point of asking,
			// and the numbers in a refusal mean little without the rows they
			// count. Anything it could not plan at all is an error response.
			if re, ok := service.AsRecoveryError(err); ok && len(plan.Items) > 0 {
				body := planBody(plan)
				body.Refusal = &recoverManyErrorBody{Code: re.Code, Message: re.Message}
				writeJSON(w, http.StatusOK, body)
				return
			}
			h.writeRecoverManyError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, planBody(plan))
		return
	}

	rcn := http.NewResponseController(w)
	var enc *json.Encoder
	started := false
	write := func(ev recoverManyEvent) {
		if !started {
			hd := w.Header()
			hd.Set("Content-Type", "application/x-ndjson")
			hd.Set("Cache-Control", "no-store")
			hd.Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			enc = json.NewEncoder(w)
			started = true
		}
		// A run can outlast the server's write timeout; each line pushes the
		// idle deadline out, as a download does.
		_ = rcn.SetWriteDeadline(time.Now().Add(recoveryWriteIdle))
		_ = enc.Encode(ev)
		_ = rcn.Flush()
	}
	res, err := rc.RecoverMany(r.Context(), recoverySessionFrom(r), req, func(p service.RecoverManyProgress) {
		ev := recoverManyEvent{Event: p.Kind, Index: p.Index, Total: p.Total}
		done, total := p.DoneBytes, p.TotalBytes
		switch p.Kind {
		case "plan":
			ev.Plan = planBody(*p.Plan)
			ev.TotalBytes = &total
		default:
			it := itemBody(p.Item)
			ev.Item = &it
			ev.DoneBytes, ev.TotalBytes = &done, &total
		}
		write(ev)
	})
	if !started {
		// Refused before anything ran: an ordinary error response.
		h.writeRecoverManyError(w, r, err)
		return
	}
	last := recoverManyEvent{Event: "result", Result: resultBody(res)}
	if err != nil {
		if re, ok := service.AsRecoveryError(err); ok && !res.Cancelled {
			last.Error = &recoverManyErrorBody{Code: re.Code, Message: re.Message}
		}
	}
	write(last)
	_ = rcn.SetWriteDeadline(time.Time{})
}

func (h *handlers) writeRecoverManyError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		h.internalError(w, r, "INTERNAL", "the recovery failed", errors.New("recovery produced neither a plan nor an error"))
	case errors.Is(err, service.ErrRecoverManyInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, service.ErrBackupSetNotFound):
		writeError(w, http.StatusNotFound, "BACKUP_SET_NOT_FOUND", "no such backup set")
	default:
		h.writeRecoveryError(w, r, err)
	}
}
