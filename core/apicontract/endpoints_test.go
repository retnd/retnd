package apicontract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The generated Endpoints table has to say what api/v1/openapi.json says.
// contract.gen.go once reached main with an operations table from an older
// contract (and the wrong spelling of the two CSRF codes), so this package
// did not build and nothing that imports it did either.

func TestEndpointsMatchTheContractDocument(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "v1", "openapi.json"))
	if err != nil {
		t.Fatalf("read api/v1/openapi.json: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse api/v1/openapi.json: %v", err)
	}
	want := map[string]bool{}
	for path, item := range doc.Paths {
		for method, op := range item {
			if op.OperationID != "" {
				want[fmt.Sprintf("%s %s %s", op.OperationID, method, path)] = true
			}
		}
	}
	got := map[string]bool{}
	for _, e := range Endpoints {
		got[fmt.Sprintf("%s %s %s", e.ID, strings.ToLower(e.Method), e.Path)] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("the contract declares %q but Endpoints does not; run scripts/api/generate.sh", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("Endpoints has %q but the contract does not; run scripts/api/generate.sh", k)
		}
	}
}

func TestCSRFRequiredEndpointsRefuseWithTheCSRFCodes(t *testing.T) {
	if !slices.Contains(ErrorCodes, ErrorCodeCSRFTokenMissing) || !slices.Contains(ErrorCodes, ErrorCodeCSRFTokenMismatch) {
		t.Fatalf("the registry lacks the CSRF codes")
	}
	for _, e := range Endpoints {
		if !e.CSRFRequired {
			continue
		}
		forbidden := e.ErrorCodes[403]
		if !slices.Contains(forbidden, ErrorCodeCSRFTokenMissing) || !slices.Contains(forbidden, ErrorCodeCSRFTokenMismatch) {
			t.Errorf("%s requires CSRF but its 403 codes are %v", e.ID, forbidden)
		}
	}
}
