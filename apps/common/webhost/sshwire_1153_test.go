package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// Issue #1153: the terminal prints these listings with --json from
// core/service's conversion, so the engine's own handlers have to serve the
// same object. This diffs the two, field for field, so a field added to one
// side and not the other fails here and not in somebody's script.
func TestSSHListingsServedByTheHandlersMatchTheSharedConversion1153(t *testing.T) {
	listing := []service.SSHKeyListing{{
		ID: "key_1", Algorithm: "ssh-ed25519", Fingerprint: "SHA256:test", PublicKey: "ssh-ed25519 AAAA",
		ImportedAt: time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC), UsedBy: []string{"api/db"},
	}, {
		ID: "key_2", Algorithm: "ssh-rsa", Fingerprint: "SHA256:other", PassphraseProtected: true, Problem: "needs its passphrase",
	}}
	scan := service.SSHKeyDiscovery{
		Locations:  []service.SSHKeyDiscoveryLocation{{Path: "/etc/retnd", Kind: "mount", Found: 1}, {Path: "/root/.ssh", Kind: "home", Problem: "not mounted here"}},
		Candidates: []service.SSHKeyCandidate{{ID: "cand_1", Path: "/etc/retnd/id_ed25519", Location: "/etc/retnd", Algorithm: "ssh-ed25519", Fingerprint: "SHA256:c", Mode: "0600", Selectable: true}},
	}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: &sshListingBackend1153{syncFakeBackend: newSyncFakeBackend(), keys: listing, scan: scan},
		Gate: alwaysPassGate{}, BinaryVersion: "test", Commit: "test",
	})

	for _, tc := range []struct {
		path string
		want any
	}{
		{"/api/v1/ssh-keys", service.SSHKeysToWire(listing)},
		{"/api/v1/ssh/key-candidates", service.SSHDiscoveryToWire(scan)},
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

// sshListingBackend1153 answers the two reads with fixed rows, so the diff
// above compares the handler with the conversion on data both sides share.
type sshListingBackend1153 struct {
	*syncFakeBackend
	keys []service.SSHKeyListing
	scan service.SSHKeyDiscovery
}

func (b *sshListingBackend1153) ListSSHKeys(context.Context) ([]service.SSHKeyListing, error) {
	return b.keys, nil
}

func (b *sshListingBackend1153) DiscoverSSHKeyCandidates(context.Context) (service.SSHKeyDiscovery, error) {
	return b.scan, nil
}
