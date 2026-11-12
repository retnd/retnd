package main

import (
	"net/http"
	"strconv"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// GET /agent/audit (#1348), served by the fake engine off the real
// BackupService behind it, for the reason fakeengine_encryption_996_test.go
// gives. The entries go to the wire through service.AgentAuditEntry.Contract,
// the conversion the real route uses, so this file mirrors only the query.

// serveAgentAudit reports whether it answered the request.
func (e *fakeEngine) serveAgentAudit(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/agent/audit" || r.Method != http.MethodGet {
		return false
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries, next, err := e.svc.ListAgentAudit(r.Context(), service.AgentAuditQuery{
		TokenID: q.Get("token"), Since: q.Get("since"), Before: q.Get("before"), Limit: limit,
	})
	if err != nil {
		refuseServiceError(w, "listAgentAudit", err)
		return true
	}
	out := apicontract.ListAgentAuditResponse{Entries: make([]apicontract.AgentAuditEntry, 0, len(entries)), NextCursor: next}
	for _, en := range entries {
		out.Entries = append(out.Entries, en.Contract())
	}
	writeJSON(w, http.StatusOK, out)
	return true
}
