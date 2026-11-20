package webhost

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/service"
)

// The edit hold: a short lease that stops a run cycle from working on a
// backup set while somebody is changing it.
//
// The interesting design decision here is the null. The state response
// carries what taking a hold WOULD interrupt as a pointer, so "nothing is
// running" is a null rather than a zero-valued object, and a client that
// gets null opens edit mode with no prompt at all. A zero object would
// render as a warning about a risk that does not exist, and an operator
// who is warned about nothing stops reading warnings.
//
// The same reasoning runs through the take response: it reports what it
// actually interrupted, so a client never claims to have stopped something
// that was not happening. And the warning distinguishes a named artifact
// mid-transfer from a cycle that has only reached discovery, because those
// two cost an operator very different amounts to discard and only the
// specific message lets them choose.

// runningWorkResponse names what a run cycle is doing for one backup set
// right now. It is the content of the warning an operator sees before
// entering edit mode, and the reason that warning is worth showing at
// all: discarding a partial transfer of a named artifact is a materially
// different cost from cancelling a scheduler tick that has not started
// work, and only a message that says which one it is lets an operator
// decide (issue #350).
type runningWorkResponse struct {
	// Artifact is the artifact being worked on, or "" during discovery,
	// when the cycle has reached this set but not yet picked one.
	Artifact string `json:"artifact"`
	// Stage is one of OperationProgress.stage's values.
	Stage string `json:"stage"`
}

// editHoldStateResponse is GET /api/v1/backup-sets/{source}/{set}/edit-hold's
// body: what entering edit mode would interrupt, and whether a hold is
// already in force.
//
// Running is a pointer so it can be null, and that is the whole shape of
// "the warning appears only when something is actually running": a client
// that gets null opens edit mode with no prompt at all. A zero-valued
// object would be rendered as a prompt for a risk that does not exist.
type editHoldStateResponse struct {
	BackupSetID string               `json:"backup_set_id"`
	Held        bool                 `json:"held"`
	ExpiresAt   string               `json:"expires_at,omitempty"`
	Running     *runningWorkResponse `json:"running"`
}

// editHoldResponse is POST /edit-hold's body: the lease, the holder token
// that names it, plus what taking it interrupted (null when nothing was
// running, so a client never claims to have stopped something it did not).
type editHoldResponse struct {
	BackupSetID string               `json:"backup_set_id"`
	ExpiresAt   string               `json:"expires_at"`
	Token       string               `json:"token"`
	Stopped     *runningWorkResponse `json:"stopped"`
}

// editHoldBody is the optional body of POST /edit-hold and of
// /edit-hold/release (issue #1606): the holder the call is about.
//
// Optional on both, and that is the backwards compatibility. A client from
// before tokens sends no body: on the take route that takes a new hold,
// exactly as it always did, and on /release it gives back every hold on
// the set, exactly as it always did. Only a client that sends a token gets
// the new guarantees, and the web UI and the CLI both do.
type editHoldBody struct {
	Token string `json:"token,omitempty"`
}

// maxEditHoldBodyBytes bounds the body above. It carries one opaque token,
// so anything near this size is not a request this route understands.
const maxEditHoldBodyBytes = 4 << 10

// decodeEditHoldRequest reads the optional body. An absent body is
// legitimate and is what a client from before tokens sends, so io.EOF is
// not an error; decoding unconditionally rather than gating on
// ContentLength is retryFailedIngestion's reason (a chunked body has no
// length and would otherwise be read as absent). A body that is not JSON
// is refused rather than read as "no token": a client that meant to renew
// and had its renewal turned into a fresh take would be this issue again.
func decodeEditHoldRequest(w http.ResponseWriter, r *http.Request) (editHoldBody, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxEditHoldBodyBytes)
	var req editHoldBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeDecodeError(w, err, maxEditHoldBodyBytes)
		return editHoldBody{}, false
	}
	return req, true
}

func toRunningWorkResponse(w *service.RunningWork) *runningWorkResponse {
	if w == nil {
		return nil
	}
	return &runningWorkResponse{Artifact: w.Artifact, Stage: w.Stage}
}

// getBackupSetEditHold is GET
// /api/v1/backup-sets/{source}/{set}/edit-hold: what a cycle is currently
// doing for this set, and whether it is already held.
//
// Read-only (§50), so no CSRF and no destructive gate, exactly like the
// other GETs in this package. It deliberately takes no hold: a read that
// silently held would pause backups for a set an operator merely looked
// at, and the whole point of splitting the read from the write is that an
// operator can be shown what they are about to stop and then decline.
func (h *handlers) getBackupSetEditHold(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "source") + "/" + chi.URLParam(r, "set")
	st, err := h.backend.BackupSetEditState(r.Context(), id)
	if err != nil {
		h.writeEditHoldError(w, r, err)
		return
	}
	resp := editHoldStateResponse{
		BackupSetID: st.BackupSetID,
		Held:        st.Held,
		Running:     toRunningWorkResponse(st.Running),
	}
	if st.Held {
		resp.ExpiresAt = st.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, resp)
}

// takeBackupSetEditHold is POST
// /api/v1/backup-sets/{source}/{set}/edit-hold: take a hold, or renew one
// holder's lease. The cycle currently processing this set, if any, is
// stopped at its next safe boundary, and no new pass starts for it until
// every hold on it is released or lapses.
//
// One route for both, told apart by the body (issue #1606). No token takes
// a NEW hold and answers its token. A token renews that holder and nothing
// else, and is refused with 409 EDIT_HOLD_GONE once the holder was
// released or lapsed: a renewal never creates a hold, which is what stops
// a heartbeat that was in flight when edit mode was left from putting the
// hold back after the release. A client still in edit mode that gets the
// refusal takes a fresh hold, so a heartbeat that arrived a second late
// still ends with the set held while the form is open.
//
// One route rather than a separate /renew, because a renewal and a take
// answer the same shape and share every requirement, and a fourth route
// would be a fourth entry in the MCP catalogue, the CLI echo table and
// every proxy for no difference a caller could act on.
//
// requireCSRF and NOT requireDestructiveGate. Nothing reachable from here
// starts work or deletes anything: it STOPS work. Gating it would mean an
// operator who has not turned destructive operations on cannot safely
// edit a backup set at all, which is the opposite of what the gate is
// for.
func (h *handlers) takeBackupSetEditHold(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "source") + "/" + chi.URLParam(r, "set")
	req, ok := decodeEditHoldRequest(w, r)
	if !ok {
		return
	}
	var hold service.EditHold
	var err error
	if req.Token == "" {
		hold, err = h.backend.BeginBackupSetEdit(r.Context(), id)
	} else {
		hold, err = h.backend.RenewBackupSetEdit(r.Context(), id, req.Token)
	}
	if err != nil {
		h.writeEditHoldError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, editHoldResponse{
		BackupSetID: hold.BackupSetID,
		ExpiresAt:   hold.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Token:       hold.Token,
		Stopped:     toRunningWorkResponse(hold.Stopped),
	})
}

// releaseBackupSetEditHold is POST
// /api/v1/backup-sets/{source}/{set}/edit-hold/release: leave edit mode.
//
// A POST with a "/release" tail rather than a DELETE on the path above,
// for two reasons. This package's whole write surface is POST and one
// PATCH, and adding a fourth method for one route is a wider change than
// it looks (every CSRF and gate walk, every provider proxy, every
// client). And the release has to be callable from a page that is going
// away, where the browser APIs that survive an unload are POST-shaped.
//
// With a token it gives back that holder alone, so one tab leaving edit
// mode does not drop the hold another tab is still editing under. With no
// token it gives back every hold on the set, which is what a client from
// before tokens sends and what an operator clearing a hold somebody left
// behind means (issue #1606).
//
// 204, not 200: there is nothing to say. Releasing a hold that is not
// held, or a token that is already gone, is a success for the same reason
// core/service.EndBackupSetEdit says so: several routes out of edit mode
// fire for one edit, and a duplicate release must not be something a
// client has to avoid.
func (h *handlers) releaseBackupSetEditHold(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "source") + "/" + chi.URLParam(r, "set")
	req, ok := decodeEditHoldRequest(w, r)
	if !ok {
		return
	}
	if err := h.backend.EndBackupSetEdit(r.Context(), id, req.Token); err != nil {
		h.writeEditHoldError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeEditHoldError maps the refusals these three routes can produce. A
// set that does not exist uses the vocabulary the rest of the backup-set
// surface already uses, so a client does not learn a second code for the
// same condition. EDIT_HOLD_GONE is the renewal's own (issue #1606): the
// holder it named is not holding the set any more, nothing was held, and a
// client that still means to edit takes a fresh hold.
func (h *handlers) writeEditHoldError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, service.ErrBackupSetNotFound) {
		writeError(w, http.StatusNotFound, "BACKUP_SET_NOT_FOUND", "no such backup set")
		return
	}
	if errors.Is(err, service.ErrEditHoldGone) {
		writeError(w, http.StatusConflict, "EDIT_HOLD_GONE",
			"this edit hold was released or has lapsed, so it was not renewed and nothing is held under it; take a new hold to keep editing")
		return
	}
	// Deliberately not err.Error(): an unclassified error here could
	// carry filesystem or transport-internal text, the same reason
	// writeBackupSetError's own default case gives.
	h.internalError(w, r, "INTERNAL", "failed to read or change this backup set's edit hold", err)
}
