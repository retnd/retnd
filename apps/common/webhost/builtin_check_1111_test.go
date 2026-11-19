package webhost

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// #1111 on the wire a browser reads.

func TestAssuranceRoute_Issue1111_TheAnswersCarryBuiltinCheckAndTheSetsEffectiveCheck(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()

	code, body := call(t, router, http.MethodGet, "/api/v1/backup-sets/production/alpha/assurance", "")
	if code != http.StatusOK {
		t.Fatalf("GET set assurance = %d: %s", code, body)
	}
	var set struct {
		RecoveryTest map[string]any `json:"recovery_test"`
	}
	if err := json.Unmarshal([]byte(body), &set); err != nil {
		t.Fatal(err)
	}
	if set.RecoveryTest["builtin_check"] != "structure" {
		t.Errorf("builtin_check = %v, want structure", set.RecoveryTest["builtin_check"])
	}
	switch set.RecoveryTest["effective_check"] {
	case "validator", "builtin":
	case "none":
		if r, _ := set.RecoveryTest["effective_check_reason"].(string); r == "" {
			t.Error("effective_check none with no reason")
		}
	default:
		t.Errorf("effective_check = %v", set.RecoveryTest["effective_check"])
	}

	code, body = call(t, router, http.MethodGet, "/api/v1/settings/assurance", "")
	if code != http.StatusOK || !strings.Contains(body, `"builtin_check":"structure"`) {
		t.Errorf("GET settings = %d: %s", code, body)
	}
	if strings.Contains(body, "effective_check") {
		t.Errorf("the deployment-wide answer has no set to look in: %s", body)
	}
}

func TestAssuranceRoute_Issue1111_TheBuiltinCheckPatchesRoundTripAndBadValuesAreRefused(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()

	code, body := call(t, router, http.MethodPatch, "/api/v1/settings/assurance", `{"recovery_test_builtin_check":"off"}`)
	if code != http.StatusOK || !strings.Contains(body, `"builtin_check":"off"`) {
		t.Fatalf("PATCH settings = %d: %s", code, body)
	}
	code, body = call(t, router, http.MethodGet, "/api/v1/backup-sets/production/alpha/assurance", "")
	if code != http.StatusOK || !strings.Contains(body, `"builtin_check":"off"`) {
		t.Errorf("an inheriting set did not follow = %d: %s", code, body)
	}
	code, body = call(t, router, http.MethodPatch, "/api/v1/backup-sets/production/alpha/assurance", `{"recovery_test_builtin_check":"structure"}`)
	if code != http.StatusOK || !strings.Contains(body, `"builtin_check":"structure"`) || !strings.Contains(body, `"is_override":true`) {
		t.Errorf("PATCH set = %d: %s", code, body)
	}
	if code, body = call(t, router, http.MethodPatch, "/api/v1/settings/assurance", `{"recovery_test_builtin_check":"deep"}`); code != http.StatusBadRequest || !strings.Contains(body, "builtin_check") {
		t.Errorf("deep = %d: %s", code, body)
	}
	if code, body = call(t, router, http.MethodPatch, "/api/v1/settings/assurance", `{"recovery_test_mode":"builtin"}`); code != http.StatusBadRequest {
		t.Errorf("mode builtin with the check off must be refused = %d: %s", code, body)
	}
}

func TestIssue1111_APlacementSaysWhichCheckProducedItsVerdictAndOmitsItWithoutOne(t *testing.T) {
	for name, tc := range map[string]struct {
		check string
		want  string
	}{
		"builtin":   {"builtin", `"recovery_check":"builtin"`},
		"validator": {"validator", `"recovery_check":"validator"`},
	} {
		out := toPlacementResponses([]service.Placement{{Medium: "local", MediumType: "local", Status: "ACTIVE", Access: "immediate", ScrubState: "unknown", RecoveryState: "passed", RecoveryCheck: tc.check}})
		b, _ := json.Marshal(out[0])
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("%s: %s lacks %s", name, b, tc.want)
		}
	}
	out := toPlacementResponses([]service.Placement{{Medium: "local", MediumType: "local", Status: "ACTIVE", Access: "immediate", ScrubState: "unknown", RecoveryState: "unknown"}})
	b, _ := json.Marshal(out[0])
	if strings.Contains(string(b), "recovery_check") {
		t.Errorf("a copy with no verdict carries recovery_check: %s", b)
	}
}
