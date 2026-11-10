package webhost

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// Retained-copy encryption (#993, FR-59, FR-63, FR-64, FR-66):
//
//	GET    /encryption/policy
//	PATCH  /encryption/policy                    CSRF
//	POST   /encryption/policy/reset              CSRF
//	PUT    /backup-sets/{source}/{set}/encryption     CSRF
//	DELETE /backup-sets/{source}/{set}/encryption     CSRF
//	GET    /encryption/keys
//	POST   /encryption/keys                      CSRF
//	POST   /encryption/keys/rotate               CSRF
//	POST   /encryption/keys/{id}/retire          CSRF
//	POST   /encryption/keys/{id}/export          CSRF, gate, step-up
//	POST   /encryption/keys/{id}/confirm         CSRF
//	POST   /encryption/providers/test            CSRF, writes nothing
//	GET    /encryption/copies?artifact=
//	POST   /encryption/verify                    CSRF
//
// Every one of these lands on a method of core/service that the CLI verb of
// the same name also calls, so a refusal has one author. The words in a
// refusal are the service's own, echoed whole: they are built from key ids,
// copy names, source descriptions and the operator's own submitted values,
// and the types they are built from have no field that holds key material.
//
// # The one route that returns key material
//
// POST /encryption/keys/{id}/export answers with the recovery bundle, the
// shape keycustody.Bundle defines as the single carrier of managed key
// material. It is the browser's "Download bundle" and the CLI's `retnd
// encryption key export --out`. Because it is the one place a web session
// can take a key off the host, it is gated like a validator upload (the
// destructive gate, then where the gate opens for local accounts the
// password), and its response is never cached. Whether the API should return
// the bundle at all, or whether export should be a terminal-only act, is an
// open decision recorded in docs/api/contract.md; removing this route is the
// whole change if the answer is no, and nothing else here depends on it.
//
// # Everything else is write-only
//
// A key reference goes in as {file | env | command} and comes back as a
// description. The apicontract type that reads back (EncryptionKeyProviderState)
// has no field a reference could be rebuilt from.

// EncryptionBackend is what the encryption routes need from the backend. A
// backend without it answers 503 ENCRYPTION_UNAVAILABLE.
type EncryptionBackend interface {
	EncryptionPolicy(ctx context.Context) (apicontract.EncryptionPolicy, error)
	PatchEncryptionPolicy(ctx context.Context, req apicontract.EncryptionPolicyPatch) (apicontract.EncryptionPolicy, error)
	ResetEncryptionPolicy(ctx context.Context) (apicontract.EncryptionPolicy, error)
	SetBackupSetEncryption(ctx context.Context, id string, o apicontract.EncryptionOverride) (apicontract.EncryptionSetPolicy, error)
	ClearBackupSetEncryption(ctx context.Context, id string) (apicontract.EncryptionSetPolicy, error)
	ListEncryptionKeys(ctx context.Context) (apicontract.ListEncryptionKeysResponse, error)
	CreateEncryptionKey(ctx context.Context) (apicontract.EncryptionKeyStatus, error)
	RotateEncryptionKey(ctx context.Context) (apicontract.EncryptionKeyStatus, error)
	RetireEncryptionKey(ctx context.Context, id string) (apicontract.EncryptionKeyStatus, error)
	ExportRecoveryBundleBytes(ctx context.Context, id string) ([]byte, error)
	ConfirmRecoveryBundle(ctx context.Context, id, digest string) (apicontract.EncryptionKeyStatus, error)
	TestEncryptionProvider(ctx context.Context, req apicontract.EncryptionProviderTestRequest) (apicontract.EncryptionProviderProbe, error)
	EncryptionCopies(ctx context.Context, artifactID string) (apicontract.ListEncryptionCopiesResponse, error)
	StartDecryptVerify(ctx context.Context, req apicontract.StartDecryptVerifyRequest) (service.ScrubStarted, error)
}

var _ EncryptionBackend = (*service.BackupService)(nil)

func (h *handlers) encryption(w http.ResponseWriter) (EncryptionBackend, bool) {
	b, ok := h.backend.(EncryptionBackend)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "ENCRYPTION_UNAVAILABLE", "this deployment cannot manage retained-copy encryption")
	}
	return b, ok
}

// requireKeyExportGate is the gate for the bundle export.
func requireKeyExportGate(e gateEnforcement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ref, refused := e.decide(r, gatedAct{keyExport: true}); refused {
				ref.write(w, r, "export a recovery bundle")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (h *handlers) getEncryptionPolicy(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	got, err := b.EncryptionPolicy(r.Context())
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to read the encryption policy")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) patchEncryptionPolicy(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	var body apicontract.EncryptionPolicyPatch
	if !decodeStrict(w, r, &body) {
		return
	}
	got, err := b.PatchEncryptionPolicy(r.Context(), body)
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to change the encryption policy")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) resetEncryptionPolicy(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	got, err := b.ResetEncryptionPolicy(r.Context())
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to reset the encryption policy")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) setBackupSetEncryption(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	var body apicontract.EncryptionOverride
	if !decodeStrict(w, r, &body) {
		return
	}
	got, err := b.SetBackupSetEncryption(r.Context(), chi.URLParam(r, "source")+"/"+chi.URLParam(r, "set"), body)
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to set the backup set's encryption")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) clearBackupSetEncryption(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	got, err := b.ClearBackupSetEncryption(r.Context(), chi.URLParam(r, "source")+"/"+chi.URLParam(r, "set"))
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to clear the backup set's encryption")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) listEncryptionKeys(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	got, err := b.ListEncryptionKeys(r.Context())
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to list the encryption keys")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) createEncryptionKey(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	got, err := b.CreateEncryptionKey(r.Context())
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to create the encryption key")
		return
	}
	writeJSON(w, http.StatusCreated, got)
}

func (h *handlers) rotateEncryptionKey(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	got, err := b.RotateEncryptionKey(r.Context())
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to rotate the encryption key")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) retireEncryptionKey(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	got, err := b.RetireEncryptionKey(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to retire the encryption key")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// bundleFileSafe is what a key id may look like in a download's file name.
// Ids are "k" and hex, so anything else is not one of ours and is left out
// of the header rather than escaped into it.
var bundleFileSafe = regexp.MustCompile(`^[a-z0-9]{1,40}$`)

func (h *handlers) exportRecoveryBundle(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	raw, err := b.ExportRecoveryBundleBytes(r.Context(), id)
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to export the recovery bundle")
		return
	}
	defer func() {
		for i := range raw {
			raw[i] = 0
		}
	}()
	name := "retnd-recovery-bundle.json"
	if bundleFileSafe.MatchString(id) {
		name = "retnd-recovery-bundle-" + id + ".json"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (h *handlers) confirmRecoveryBundle(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	var body apicontract.ConfirmRecoveryBundleRequest
	if r.ContentLength != 0 {
		if !decodeStrict(w, r, &body) {
			return
		}
	}
	got, err := b.ConfirmRecoveryBundle(r.Context(), chi.URLParam(r, "id"), body.Digest)
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to confirm the recovery bundle")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) testEncryptionProvider(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	var body apicontract.EncryptionProviderTestRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	got, err := b.TestEncryptionProvider(r.Context(), body)
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to test the key provider")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) listEncryptionCopies(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	got, err := b.EncryptionCopies(r.Context(), r.URL.Query().Get("artifact"))
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to read the copies' encryption")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *handlers) startDecryptVerify(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryption(w)
	if !ok {
		return
	}
	var body apicontract.StartDecryptVerifyRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	got, err := b.StartDecryptVerify(r.Context(), body)
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to start the decrypt-verify")
		return
	}
	writeJSON(w, http.StatusAccepted, apicontract.ScrubStarted{BackupSetID: got.BackupSetID, OperationID: got.OperationID})
}

// EncryptExistingBackend is the dry run and the run of encrypting retained
// copies that already sit on a medium in the clear (#1291). It is its own
// interface rather than two more methods on EncryptionBackend so a backend
// that predates it, and every fake of EncryptionBackend, still satisfies the
// one it was written against; a backend without it answers the same 503
// every other encryption route answers with no encryption.
type EncryptExistingBackend interface {
	PlanEncryptExisting(ctx context.Context, set, artifact, destination string) (apicontract.EncryptExistingPlan, error)
	StartEncryptExisting(ctx context.Context, req apicontract.StartEncryptExistingRequest) (service.ScrubStarted, error)
}

var _ EncryptExistingBackend = (*service.BackupService)(nil)

func (h *handlers) encryptExisting(w http.ResponseWriter) (EncryptExistingBackend, bool) {
	b, ok := h.backend.(EncryptExistingBackend)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "ENCRYPTION_UNAVAILABLE", "this deployment cannot manage retained-copy encryption")
	}
	return b, ok
}

// planEncryptExisting is GET /encryption/encrypt-existing: the dry run. It
// changes nothing, so it needs no CSRF token and answers with the gate shut.
func (h *handlers) planEncryptExisting(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryptExisting(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	got, err := b.PlanEncryptExisting(r.Context(), q.Get("set"), q.Get("artifact"), q.Get("destination"))
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to plan the encryption of existing copies")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// startEncryptExisting is POST /encryption/encrypt-existing. The request has
// the fields the contract names and nothing else, so there is no spelling for
// skipping a verification or carrying a key.
func (h *handlers) startEncryptExisting(w http.ResponseWriter, r *http.Request) {
	b, ok := h.encryptExisting(w)
	if !ok {
		return
	}
	var body apicontract.StartEncryptExistingRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	got, err := b.StartEncryptExisting(r.Context(), body)
	if err != nil {
		h.writeEncryptionError(w, r, err, "failed to start the encryption of existing copies")
		return
	}
	writeJSON(w, http.StatusAccepted, apicontract.ScrubStarted{BackupSetID: got.BackupSetID, OperationID: got.OperationID})
}

// writeEncryptionError maps the encryption sentinels. The message is the
// error's own text, whole, so the terminal that printed the same error
// directly and the browser that got it here read one sentence.
func (h *handlers) writeEncryptionError(w http.ResponseWriter, r *http.Request, err error, internal string) {
	msg := err.Error()
	switch {
	case errors.Is(err, service.ErrEncryptionKeyNotFound):
		writeError(w, http.StatusNotFound, "ENCRYPTION_KEY_NOT_FOUND", msg)
	case errors.Is(err, service.ErrEncryptionKeyInUse):
		writeError(w, http.StatusConflict, "ENCRYPTION_KEY_IN_USE", msg)
	case errors.Is(err, service.ErrEncryptionKeyState):
		writeError(w, http.StatusConflict, "ENCRYPTION_KEY_STATE", msg)
	case errors.Is(err, service.ErrEncryptionKeyUnresolvable):
		writeError(w, http.StatusConflict, "ENCRYPTION_KEY_UNRESOLVABLE", msg)
	case errors.Is(err, service.ErrBackupSetNotFound):
		writeError(w, http.StatusNotFound, "BACKUP_SET_NOT_FOUND", msg)
	case errors.Is(err, service.ErrArtifactNotFound):
		writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", msg)
	case errors.Is(err, service.ErrScrubRunning), errors.Is(err, service.ErrEncryptExistingRunning):
		writeError(w, http.StatusConflict, "OPERATION_ALREADY_RUNNING", msg)
	case errors.Is(err, service.ErrConfigNotFileBacked):
		writeError(w, http.StatusServiceUnavailable, "ENCRYPTION_UNAVAILABLE", msg)
	default:
		h.writeAssuranceError(w, r, err, internal)
	}
}
