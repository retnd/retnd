package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// Custom recovery-test validators (#1203):
//
//	GET    /assurance/validators[?backup_set=&artifact=]
//	POST   /assurance/validators                 CSRF, gate, step-up
//	PUT    /assurance/validators/{name}          CSRF, gate, step-up (a whole replace)
//	DELETE /assurance/validators/{name}          CSRF
//	GET    /assurance/validators/{name}/script
//	POST   /assurance/validator-preview          CSRF, writes nothing
//	GET    /assurance/validator-patterns[?backup_set=]
//	GET    /assurance/default-validator-script
//
// # Why the upload is gated when every other assurance write is not
//
// Everything else in handlers_assurance.go edits policy or starts a read.
// An upload puts a new program where the engine will run it on the host, as
// this process's runner, against a copy of the operator's data. Before this
// route only somebody with the filesystem could do that. So it asks what a
// recovery onto the source host asks (gate_profile.go): the destructive gate,
// shut on a gateway profile until #92 because a forged identity header would
// be enough to plant a script, and where it opens for local accounts the
// password again and the transport acknowledgement. The edit is gated the
// same way: a new pattern changes which data the script reads, and a new
// script is new code. Removing one only stops a script from running, which is
// the class of clearing a stage directory, so it is CSRF alone, and it has to
// work on a shut gate so an operator can take a bad validator out.
//
// The password is asked for wherever the gate is open, whether or not the
// gate says it asks for one for restores (requireValidatorGate, decide): an
// upload that needs a password only when the gate volunteers that it does is
// an upload the next gate implementation can forget to protect.
//
// # PUT is a whole replace
//
// The body carries the pattern, the scope and either the new script or the
// hash of the stored one to keep. Nothing is inferred from a field left out,
// because an inferred scope is how an edit of the pattern alone made a
// set-scoped validator global.
//
// The preview is a POST because it carries a pattern, and it writes nothing.
// It is behind CSRF like every other POST in this router.

// maxCustomValidatorBodyBytes bounds an upload's body: a 256 KiB script, with
// room for JSON escaping.
const maxCustomValidatorBodyBytes = 2 << 20

// CustomValidatorBackend is what the validator routes need from the backend.
// A backend without it answers 503 VALIDATORS_UNAVAILABLE.
type CustomValidatorBackend interface {
	ListCustomValidators(ctx context.Context, f service.CustomValidatorsFilter) (service.CustomValidators, error)
	CreateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error)
	UpdateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error)
	RemoveCustomValidator(ctx context.Context, name string) (service.CustomValidator, error)
	CustomValidatorScript(ctx context.Context, name string) (service.ValidatorScript, error)
	PreviewCustomValidator(ctx context.Context, pattern, backupSetID, replacing string) (service.CustomValidatorPreview, error)
	CustomValidatorPatterns(ctx context.Context, backupSetID string) (service.CustomValidatorPatterns, error)
}

var _ CustomValidatorBackend = (*service.BackupService)(nil)

func (h *handlers) customValidators(w http.ResponseWriter) (CustomValidatorBackend, bool) {
	b, ok := h.backend.(CustomValidatorBackend)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "VALIDATORS_UNAVAILABLE", "this deployment cannot keep custom validators")
	}
	return b, ok
}

// requireValidatorGate is the gate for the upload and the edit.
func requireValidatorGate(e gateEnforcement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ref, refused := e.decide(r, gatedAct{validatorUpload: true}); refused {
				ref.write(w, r, "install a custom validator")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (h *handlers) listCustomValidators(w http.ResponseWriter, r *http.Request) {
	b, ok := h.customValidators(w)
	if !ok {
		return
	}
	l, err := b.ListCustomValidators(r.Context(), service.CustomValidatorsFilter{
		BackupSetID: r.URL.Query().Get("backup_set"), ArtifactID: r.URL.Query().Get("artifact"),
	})
	if err != nil {
		h.writeCustomValidatorError(w, r, err, "failed to list the custom validators")
		return
	}
	writeJSON(w, http.StatusOK, service.CustomValidatorsWire(l))
}

func decodeStrict(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxCustomValidatorBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeDecodeError(w, err, maxCustomValidatorBodyBytes)
		return false
	}
	return true
}

func (h *handlers) createCustomValidator(w http.ResponseWriter, r *http.Request) {
	b, ok := h.customValidators(w)
	if !ok {
		return
	}
	var body apicontract.CreateCustomValidatorRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	v, err := b.CreateCustomValidator(r.Context(), service.CreateCustomValidatorRequest{
		Name: body.Name, Pattern: body.Pattern, BackupSetID: body.BackupSetID, Script: body.Script, Env: body.Env,
	})
	if err != nil {
		h.writeCustomValidatorError(w, r, err, "failed to store the custom validator")
		return
	}
	writeJSON(w, http.StatusCreated, service.CustomValidatorWire(v))
}

func (h *handlers) updateCustomValidator(w http.ResponseWriter, r *http.Request) {
	b, ok := h.customValidators(w)
	if !ok {
		return
	}
	var body apicontract.UpdateCustomValidatorRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	v, err := b.UpdateCustomValidator(r.Context(), service.CreateCustomValidatorRequest{
		Name: chi.URLParam(r, "name"), Pattern: body.Pattern, BackupSetID: body.BackupSetID, Script: body.Script,
		ScriptSHA256: body.ScriptSha256, Env: body.Env,
	})
	if err != nil {
		h.writeCustomValidatorError(w, r, err, "failed to store the custom validator")
		return
	}
	writeJSON(w, http.StatusOK, service.CustomValidatorWire(v))
}

func (h *handlers) removeCustomValidator(w http.ResponseWriter, r *http.Request) {
	b, ok := h.customValidators(w)
	if !ok {
		return
	}
	v, err := b.RemoveCustomValidator(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		h.writeCustomValidatorError(w, r, err, "failed to remove the custom validator")
		return
	}
	writeJSON(w, http.StatusOK, service.CustomValidatorWire(v))
}

func (h *handlers) getCustomValidatorScript(w http.ResponseWriter, r *http.Request) {
	b, ok := h.customValidators(w)
	if !ok {
		return
	}
	s, err := b.CustomValidatorScript(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		h.writeCustomValidatorError(w, r, err, "failed to read the custom validator's script")
		return
	}
	writeJSON(w, http.StatusOK, service.ValidatorScriptWire(s))
}

func (h *handlers) previewCustomValidator(w http.ResponseWriter, r *http.Request) {
	b, ok := h.customValidators(w)
	if !ok {
		return
	}
	var body apicontract.PreviewCustomValidatorRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	p, err := b.PreviewCustomValidator(r.Context(), body.Pattern, body.BackupSetID, body.Replacing)
	if err != nil {
		h.writeCustomValidatorError(w, r, err, "failed to preview the pattern")
		return
	}
	writeJSON(w, http.StatusOK, service.CustomValidatorPreviewWire(p))
}

func (h *handlers) listCustomValidatorPatterns(w http.ResponseWriter, r *http.Request) {
	b, ok := h.customValidators(w)
	if !ok {
		return
	}
	p, err := b.CustomValidatorPatterns(r.Context(), r.URL.Query().Get("backup_set"))
	if err != nil {
		h.writeCustomValidatorError(w, r, err, "failed to suggest patterns")
		return
	}
	writeJSON(w, http.StatusOK, service.CustomValidatorPatternsWire(p))
}

// getDefaultValidatorScript serves the same bytes on every deployment and
// needs nothing from the backend.
func (h *handlers) getDefaultValidatorScript(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, service.ValidatorScriptWire(service.DefaultCustomValidatorScript()))
}

func (h *handlers) writeCustomValidatorError(w http.ResponseWriter, r *http.Request, err error, internal string) {
	// The service's sentences are built from the request's own values and
	// this deployment's configuration, and safe to echo; the package prefix
	// is not for an operator.
	msg := strings.TrimPrefix(err.Error(), "service: ")
	switch {
	case errors.Is(err, service.ErrValidatorNotFound):
		writeError(w, http.StatusNotFound, "VALIDATOR_NOT_FOUND", msg)
	case errors.Is(err, service.ErrValidatorExists):
		writeError(w, http.StatusConflict, "VALIDATOR_EXISTS", msg)
	case errors.Is(err, service.ErrValidatorConflict):
		writeError(w, http.StatusConflict, "VALIDATOR_PATTERN_CONFLICT", msg)
	case errors.Is(err, service.ErrValidatorLimit):
		writeError(w, http.StatusConflict, "VALIDATOR_LIMIT_REACHED", msg)
	case errors.Is(err, service.ErrValidatorScriptChanged):
		writeError(w, http.StatusConflict, "VALIDATOR_SCRIPT_CHANGED", msg)
	case errors.Is(err, service.ErrValidatorsUnavailable):
		writeError(w, http.StatusServiceUnavailable, "VALIDATORS_UNAVAILABLE", msg)
	default:
		h.writeAssuranceError(w, r, err, internal)
	}
}
