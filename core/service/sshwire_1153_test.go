package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Issue #1153: the key store listing as the API serves it, in one place, so
// the engine's handler and the terminal's --json print the same object.

func TestSSHKeysToWire1153_IsTheObjectTheAPIServes(t *testing.T) {
	imported := time.Date(2026, 10, 1, 12, 30, 0, 0, time.FixedZone("x", 3600))
	got := SSHKeysToWire([]SSHKeyListing{
		{ID: "key_1", Algorithm: "ssh-ed25519", Fingerprint: "SHA256:abc", PublicKey: "ssh-ed25519 AAAA", ImportedAt: imported, UsedBy: []string{"a/b"}},
		{ID: "key_2", Algorithm: "ssh-rsa", Fingerprint: "SHA256:def", PassphraseProtected: true, Problem: "cannot be read without its passphrase"},
	})
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 2 {
		t.Fatalf("got %d keys from %s, want 2", len(doc.Keys), raw)
	}
	first, second := doc.Keys[0], doc.Keys[1]
	if first["imported_at"] != "2026-10-01T11:30:00Z" {
		t.Errorf("imported_at = %v, want RFC 3339 in UTC", first["imported_at"])
	}
	if second["imported_at"] != "" {
		t.Errorf("an unknown import time = %v, want the empty string and not an absent field", second["imported_at"])
	}
	if used, ok := second["used_by"].([]any); !ok || len(used) != 0 {
		t.Errorf("used_by of an unused key = %v, want an empty array: 'used by nothing' is an answer", second["used_by"])
	}
	for _, key := range doc.Keys {
		for _, forbidden := range []string{"key_file", "path", "private_key_pem"} {
			if _, present := key[forbidden]; present {
				t.Errorf("a key row carries %q", forbidden)
			}
		}
	}
	if second["problem"] == "" || second["passphrase_protected"] != true {
		t.Errorf("the second row lost its problem or its passphrase marker: %v", second)
	}
	if strings.Contains(string(raw), "PRIVATE KEY") {
		t.Error("the listing carries private key material")
	}
}

func TestSSHKeysToWire1153_NoKeysIsAnEmptyArray(t *testing.T) {
	raw, _ := json.Marshal(SSHKeysToWire(nil))
	if string(raw) != `{"keys":[]}` {
		t.Errorf("an empty store serialises as %s, want {\"keys\":[]}", raw)
	}
}

func TestSSHDiscoveryToWire1153_KeepsEveryLocationBesideTheCandidates(t *testing.T) {
	got := SSHDiscoveryToWire(SSHKeyDiscovery{
		Locations: []SSHKeyDiscoveryLocation{
			{Path: "/etc/retnd", Kind: "mount", Found: 1},
			{Path: "/root/.ssh", Kind: "home", Problem: "not mounted here"},
		},
		Candidates: []SSHKeyCandidate{{
			ID: "cand_1", Path: "/etc/retnd/id_ed25519", Location: "/etc/retnd", Algorithm: "ssh-ed25519",
			Fingerprint: "SHA256:abc", PublicKey: "ssh-ed25519 AAAA", Mode: "0600", Selectable: true,
		}},
	})
	if len(got.Locations) != 2 || got.Locations[1].Problem != "not mounted here" {
		t.Errorf("locations = %+v, want both, absent ones included with their problem", got.Locations)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].ID != "cand_1" || !got.Candidates[0].Selectable || got.Candidates[0].Path == "" {
		t.Errorf("candidates = %+v", got.Candidates)
	}

	empty, _ := json.Marshal(SSHDiscoveryToWire(SSHKeyDiscovery{}))
	if string(empty) != `{"candidates":[],"locations":[]}` {
		t.Errorf("an empty scan serialises as %s, want both arrays present and empty", empty)
	}
}
