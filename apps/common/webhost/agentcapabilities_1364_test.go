package webhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Q14 of the EPIC U panel (#1364): GET /system/capabilities says whether the
// streamable HTTP transport is on, so the Agents card can state it and an
// operator can ask "is HTTP on?" without reading config.yaml.

func capabilitiesOf(t *testing.T, router http.Handler) map[string]any {
	t.Helper()
	rec := serve(router, httptest.NewRequest(http.MethodGet, "/api/v1/system/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Falsify: report a constant, or read the field from anything but the
// resolved agents: block.
func TestCapabilitiesReportTheAgentHTTPTransport(t *testing.T) {
	on, _ := mcpRouter(service.AgentPolicy{HTTPTransport: true, RateLimitPerMinute: 60})
	off, _ := mcpRouter(service.AgentPolicy{RateLimitPerMinute: 60})
	plain, _ := agentRouter()
	for name, tc := range map[string]struct {
		router http.Handler
		want   string
	}{
		"on":                  {on, "on"},
		"off":                 {off, "off"},
		"a backend with none": {plain, "off"},
	} {
		if got := capabilitiesOf(t, tc.router)["agents_http_transport"]; got != tc.want {
			t.Errorf("%s: agents_http_transport = %v, want %q", name, got, tc.want)
		}
	}
}

// The contract names the field, as a required two-word enum, so both
// bindings carry it and a client never has to guess a missing key.
func TestTheContractDeclaresTheAgentHTTPTransportField(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "v1", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	caps := doc.Components.Schemas["CapabilitiesResponse"]
	if got := caps.Properties["agents_http_transport"].Enum; !reflect.DeepEqual(got, []string{"on", "off"}) {
		t.Errorf("CapabilitiesResponse.agents_http_transport enum = %v, want [on off]", got)
	}
	required := false
	for _, r := range caps.Required {
		required = required || r == "agents_http_transport"
	}
	if !required {
		t.Error("agents_http_transport is not required in CapabilitiesResponse")
	}
}
