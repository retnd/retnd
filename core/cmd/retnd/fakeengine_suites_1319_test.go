package main

import (
	"encoding/json"
	"net/http"

	"github.com/retnd/retnd/core/apicontract"
)

// The EPIC T routes the fake engine did not know (#1319): the suites listing
// and the re-encryption plan, served off the real BackupService behind it for
// the reason fakeengine_encryption_996_test.go gives (a fixture answer would
// let a verb that never reached the engine look correct). The policy patch
// with a suite and the key routes with a suite go through serveEncryption,
// whose bodies already carry the contract types.
//
// serveSuites reports whether it answered the request.
func (e *fakeEngine) serveSuites(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == "/encryption/suites" && r.Method == http.MethodGet:
		got, err := e.svc.ListEncryptionSuites(r.Context())
		answerEncryption(w, "listEncryptionSuites", got, err)
		return true
	case path == "/encryption/reencrypt/plan" && r.Method == http.MethodPost:
		var body apicontract.ReencryptRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return true
		}
		got, err := e.svc.PlanReencrypt(r.Context(), body)
		answerEncryption(w, "planReencrypt", got, err)
		return true
	}
	return false
}
