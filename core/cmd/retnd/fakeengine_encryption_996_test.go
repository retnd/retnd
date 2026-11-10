package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/retnd/retnd/core/apicontract"
)

// The encryption writes #993 routes, served by the fake engine off the real
// BackupService behind it, for the reason fakeengine_configwrites_1148_test.go
// gives: a fake that answered from a fixture would let a verb that never
// reached the engine look correct.
//
// Until this file the fake engine knew none of these paths, so the six
// encryption rows of routeparity_test.go reached its 404 default on the
// engine-attached route and could only ever compare a refusal against the
// fixture's own "serves no" sentence. Serving them from the real service is
// what lets the engine route of an encryption verb answer at all, and it is
// what encryption_equivalence_996_test.go needs to compare the configuration
// the two routes leave behind.
//
// serveEncryption is the single hook api() calls ahead of serveConfigWrite,
// because a set's encryption path is a PUT or DELETE under /backup-sets/ and
// would otherwise be read as a set removal.

// serveEncryption reports whether it answered the request.
func (e *fakeEngine) serveEncryption(w http.ResponseWriter, r *http.Request, path string) bool {
	if rest, ok := strings.CutPrefix(path, "/backup-sets/"); ok {
		if id, ok := strings.CutSuffix(rest, "/encryption"); ok && (r.Method == http.MethodPut || r.Method == http.MethodDelete) {
			e.fakeSetEncryption(w, r, id)
			return true
		}
		return false
	}
	const keys = "/encryption/keys"
	switch {
	case path == "/encryption/policy" && r.Method == http.MethodPatch:
		var body apicontract.EncryptionPolicyPatch
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return true
		}
		got, err := e.svc.PatchEncryptionPolicy(r.Context(), body)
		answerEncryption(w, "patchEncryptionPolicy", got, err)
		return true
	case path == "/encryption/policy/reset" && r.Method == http.MethodPost:
		got, err := e.svc.ResetEncryptionPolicy(r.Context())
		answerEncryption(w, "resetEncryptionPolicy", got, err)
		return true
	case path == keys && r.Method == http.MethodPost:
		got, err := e.svc.CreateEncryptionKey(r.Context())
		answerEncryption(w, "createEncryptionKey", got, err)
		return true
	case path == keys+"/rotate" && r.Method == http.MethodPost:
		got, err := e.svc.RotateEncryptionKey(r.Context())
		answerEncryption(w, "rotateEncryptionKey", got, err)
		return true
	case strings.HasPrefix(path, keys+"/") && strings.HasSuffix(path, "/retire") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(path, keys+"/"), "/retire")
		got, err := e.svc.RetireEncryptionKey(r.Context(), id)
		answerEncryption(w, "retireEncryptionKey", got, err)
		return true
	case strings.HasPrefix(path, keys+"/") && strings.HasSuffix(path, "/export") && r.Method == http.MethodPost:
		// The bytes the service produced, written as they are, which is
		// what apps/common/webhost does. The CLI decodes them into the
		// contract's RecoveryBundle and writes them to its file.
		id := strings.TrimSuffix(strings.TrimPrefix(path, keys+"/"), "/export")
		raw, err := e.svc.ExportRecoveryBundleBytes(r.Context(), id)
		if err != nil {
			refuseServiceError(w, "exportRecoveryBundle", err)
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return true
	case strings.HasPrefix(path, keys+"/") && strings.HasSuffix(path, "/confirm") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(path, keys+"/"), "/confirm")
		var body apicontract.ConfirmRecoveryBundleRequest
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
				return true
			}
		}
		got, err := e.svc.ConfirmRecoveryBundle(r.Context(), id, body.Digest)
		answerEncryption(w, "confirmRecoveryBundle", got, err)
		return true
	case path == "/encryption/providers/test" && r.Method == http.MethodPost:
		var body apicontract.EncryptionProviderTestRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return true
		}
		got, err := e.svc.TestEncryptionProvider(r.Context(), body)
		answerEncryption(w, "testEncryptionProvider", got, err)
		return true
	}
	return false
}

func (e *fakeEngine) fakeSetEncryption(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodDelete {
		got, err := e.svc.ClearBackupSetEncryption(r.Context(), id)
		answerEncryption(w, "clearBackupSetEncryption", got, err)
		return
	}
	var body apicontract.EncryptionOverride
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}
	got, err := e.svc.SetBackupSetEncryption(r.Context(), id, body)
	answerEncryption(w, "setBackupSetEncryption", got, err)
}

func answerEncryption(w http.ResponseWriter, operation string, body any, err error) {
	if err != nil {
		refuseServiceError(w, operation, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}
