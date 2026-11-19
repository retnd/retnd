package metrics

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/health"
)

// EPIC T, T1.5 (#1315): the copies-per-suite family carries a suite id as
// its one label, bounded by the registry, and never a key id. Written by
// reading; not run.

func TestNoKeyIdIsALabelOnTheSuiteFamily_1315(t *testing.T) {
	const keyID = "k0123456789abcdef"
	out := Render(keyCustodyReport(health.KeyCustodyHealth{
		Enabled: true, ActiveKeyID: keyID,
		EncryptedCopiesBySuite: map[string]int{"age/v1": 3, "age-pq/v1": 2, "some-old-build/v9": 1},
	}))
	for _, want := range []string{
		`retnd_retained_copy_encrypted_copies{suite="age/v1"} 3`,
		`retnd_retained_copy_encrypted_copies{suite="age-pq/v1"} 2`,
		`retnd_retained_copy_encrypted_copies{suite="unknown"} 1`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("the scrape lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, keyID) {
		t.Errorf("a key id is on the scrape")
	}
	if strings.Contains(out, "some-old-build/v9") {
		t.Errorf("an unregistered suite id became its own series; the label is bounded by the registry")
	}
	for _, kv := range labelValuesIn(out) {
		if strings.Contains(strings.ToLower(kv), "key") {
			t.Errorf("a label names or carries a key: %s", kv)
		}
	}
}

// The planted violation: a copies family labelled by key id is what the
// scan has to catch.
func TestAKeyIdLabelOnTheSuiteFamilyIsCaught_1315(t *testing.T) {
	planted := `retnd_retained_copy_encrypted_copies{key_id="k0123456789abcdef"} 1` + "\n"
	found := false
	for _, kv := range labelValuesIn(planted) {
		if strings.Contains(strings.ToLower(kv), "key") {
			found = true
		}
	}
	if !found {
		t.Fatal("the label scan did not see a key_id label, so the test above proves nothing")
	}
}
