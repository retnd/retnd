package webhost

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1153: the terminal prints these listings with --json from
// core/service's conversion, so the engine's own handlers have to serve the
// same object. This diffs the two, field for field, so a field added to one
// side and not the other fails here and not in somebody's script.
func TestSSHListingsServedByTheHandlersMatchTheSharedConversion1153(t *testing.T) {
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), FirstRun: &fakeFirstRun{},
		BinaryVersion: "test", Commit: "test",
	})

	for _, tc := range []struct {
		path string
		want any
	}{
		{"/api/v1/ssh-keys", service.SSHKeysToWire([]service.SSHKeyListing{{
			ID: "key_1", Algorithm: "ssh-ed25519", Fingerprint: "SHA256:test",
		}})},
		{"/api/v1/ssh/key-candidates", service.SSHDiscoveryToWire(service.SSHKeyDiscovery{
			Locations: []service.SSHKeyDiscoveryLocation{{Path: "/etc/retnd", Kind: "mount"}},
		})},
	} {
		rec := getJSON(t, router, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
		var served, shared map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &served); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(tc.want)
		_ = json.Unmarshal(raw, &shared)
		if !reflect.DeepEqual(served, shared) {
			t.Errorf("GET %s serves\n  %v\nand core/service converts the same listing to\n  %v", tc.path, served, shared)
		}
	}
}
