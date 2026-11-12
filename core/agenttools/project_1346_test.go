package agenttools

import (
	"encoding/json"
	"strings"
	"testing"
)

// The projection (#1346, FR-98): a denied field is gone at every depth and a
// canary shape is gone from every string, and a field the table names is
// checked on its own so removing one from DeniedFields names it.

func decode(t *testing.T, doc string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func render(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Falsify: remove one field from DeniedFields.
func TestEachDeniedFieldIsDroppedAtEveryDepth(t *testing.T) {
	fields := []string{"recipient", "identity", "bundle", "password", "secret", "token", "private_key", "credential", "passphrase", "key_material"}
	if len(DeniedFields) != len(fields) {
		t.Errorf("DeniedFields = %v, the issue names %v", DeniedFields, fields)
	}
	for _, f := range fields {
		for _, doc := range []string{
			`{"` + f + `":"v","kept":"k"}`,
			`{"a":{"b":{"` + f + `":"v","kept":"k"}}}`,
			`{"keys":[{"id":"k1","status":{"inner":[{"` + f + `":"v","kept":"k"}]}}]}`,
		} {
			got := render(t, Project(decode(t, doc)))
			if strings.Contains(got, `"`+f+`"`) || strings.Contains(got, `"v"`) {
				t.Errorf("%s survived the projection of %s: %s", f, doc, got)
			}
			if !strings.Contains(got, `"kept":"k"`) {
				t.Errorf("the projection of %s lost a field it had no business with: %s", doc, got)
			}
		}
	}
}

func TestTheWordsAroundADeniedFieldAreDroppedToo(t *testing.T) {
	for _, k := range []string{"Recipient", "recipients", "currentPassword", "access_token", "session-token", "private_key_pem", "ssh_private_key", "client_secret", "recovery_bundle"} {
		got := render(t, Project(map[string]any{k: "v", "kept": 1}))
		if strings.Contains(got, `"`+k+`"`) {
			t.Errorf("%q survived: %s", k, got)
		}
	}
	// And the fields that only look related are kept: a flag that a bundle was
	// confirmed is a fact an agent may read.
	for _, k := range []string{"bundle_confirmed", "credentials_valid", "credential_configured", "token_file", "key_id", "passphrase_protected"} {
		if got := render(t, Project(map[string]any{k: true})); !strings.Contains(got, k) {
			t.Errorf("%q was dropped: %s", k, got)
		}
	}
}

const (
	canaryIdentity  = "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	canaryRecipient = "age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
	canaryPQ        = "age1pq1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
	canaryPEM       = "-----BEGIN OPENSSH PRIVATE KEY-----"
)

// Falsify: plant a canary recipient three levels deep in a key-list result.
func TestTheProjectionLeavesNoCanaryShapeInAnyString(t *testing.T) {
	for _, canary := range []string{canaryIdentity, canaryRecipient, canaryPQ, canaryPEM} {
		doc := map[string]any{
			"keys": []any{map[string]any{
				"id":    "k1",
				"state": map[string]any{"detail": map[string]any{"note": "see " + canary + " for more", "list": []any{"fine", canary}}},
			}},
			"top": canary,
		}
		got := render(t, Project(doc))
		if strings.Contains(got, canary[:20]) {
			t.Errorf("%q survived: %s", canary[:20], got)
		}
		if !strings.Contains(got, Withheld) || !strings.Contains(got, `"fine"`) {
			t.Errorf("the projection did not replace with the marker and keep the rest: %s", got)
		}
	}
}

// The sentence a refusal quotes for a secret that is not an identity is
// allowed, as it is in the canary corpus, and ordinary prose is untouched.
func TestTheProjectionKeepsWhatIsNotMaterial(t *testing.T) {
	in := map[string]any{
		"message": "expected AGE-SECRET-KEY-1 followed by an identity",
		"name":    "age1 is the prefix",
		"n":       float64(3), "ok": true, "none": nil,
		"list": []any{"a", float64(1)},
	}
	if got, want := render(t, Project(in)), render(t, in); got != want {
		t.Errorf("projection changed what held no material:\n got %s\nwant %s", got, want)
	}
}

func TestTheProjectionNeverChangesItsArgument(t *testing.T) {
	in := map[string]any{"recipient": canaryRecipient, "nested": map[string]any{"s": canaryIdentity}}
	before := render(t, in)
	_ = Project(in)
	if after := render(t, in); after != before {
		t.Errorf("Project changed its argument:\n%s\n%s", before, after)
	}
}

// The control: the detector the corpus uses can see each shape, so a pass above
// is not a detector that sees nothing.
func TestEachCanaryShapeSeesItsOwnCanary(t *testing.T) {
	for _, c := range []struct {
		name   string
		canary string
		shape  int
	}{{"identity", canaryIdentity, 0}, {"recipient", canaryRecipient, 1}, {"post-quantum recipient", canaryPQ, 1}, {"pem", canaryPEM, 2}} {
		if !CanaryShapes[c.shape].MatchString(c.canary) {
			t.Errorf("the %s shape does not match its own canary", c.name)
		}
	}
	if AgeIdentityShape.MatchString("AGE-SECRET-KEY-1") {
		t.Error("the bare prefix, which a refusal quotes, matches the identity shape")
	}
}
