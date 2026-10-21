package webhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUnconfiguredInstanceNamesItsDeployment_1254 is what lets a terminal
// finish a first run (#1254).
//
// `retnd backup-set create` hands a first configuration to a first-run
// engine through POST /system/first-run, and like every routed write it
// first asks GET /system/version which deployment it has reached, refusing
// when the answer is empty (core/cmd/retnd's deploymentcheck.go). A
// first-run engine already HAS an identity: announcing about its journal
// mints one beside it. It just never said so, because the version handler
// only asked the backend, and a first-run instance has none. So every
// routed first run would be refused as "an engine that names no
// deployment", which is the same dead end the issue is about.
func TestUnconfiguredInstanceNamesItsDeployment_1254(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	router := unconfiguredRouter(&fakeFirstRun{deploymentID: id})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/system/version status = %d, want 200", rec.Code)
	}
	var version struct {
		DeploymentID string `json:"deployment_id"`
		Configured   bool   `json:"configured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &version); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if version.DeploymentID != id {
		t.Errorf("an unconfigured instance answered deployment_id %q, want %q; a terminal cannot confirm it is finishing THIS deployment's first run without it", version.DeploymentID, id)
	}
	if version.Configured {
		t.Error("naming the deployment made an unconfigured instance report configured")
	}

	// And a surface that cannot name one still says nothing, which a client
	// reads as "cannot confirm" rather than as agreement.
	rec = httptest.NewRecorder()
	unconfiguredRouter(&fakeFirstRun{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &version); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if version.DeploymentID != "" {
		t.Errorf("a first-run surface with no identity answered deployment_id %q", version.DeploymentID)
	}
}
