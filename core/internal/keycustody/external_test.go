package keycustody_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/secretref"
)

// The external key, FR-64: declared in secretref.Ref's shape and nothing
// else, probed for WHETHER it resolves without revealing WHAT it resolves
// to.

// externalKeyFile writes an operator-style external key under custody
// secretref accepts and returns its ref and its material.
func externalKeyFile(t *testing.T) (secretref.Ref, []byte) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	path := filepath.Join(dir, "offsite.key")
	material, _, err := testFormat.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := os.WriteFile(path, append(material, '\n'), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return secretref.Ref{File: path}, material
}

// The resolvability probe distinguishes "resolves", "declared but
// unreachable" and "not declared", and none of the three renders material.
func TestProbeExternalDistinguishesThreeStatesAndRendersNoMaterial(t *testing.T) {
	ctx := context.Background()
	ref, material := externalKeyFile(t)

	resolves := keycustody.ProbeExternal(ctx, ref)
	if resolves.State != keycustody.Resolves {
		t.Fatalf("a readable 0600 key file probes as %+v, want Resolves", resolves)
	}
	if resolves.Source != ref.String() {
		t.Fatalf("Source = %q, want the reference description %q (that is what the journal records)", resolves.Source, ref.String())
	}

	missing := keycustody.ProbeExternal(ctx, secretref.Ref{File: filepath.Join(t.TempDir(), "gone.key")})
	if missing.State != keycustody.Unreachable {
		t.Fatalf("a declared file that does not exist probes as %+v, want Unreachable", missing)
	}
	if missing.Reason == "" || !strings.Contains(missing.Reason, "gone.key") {
		t.Fatalf("Unreachable carries reason %q, want one naming the source an operator has to fix", missing.Reason)
	}

	drifted := ref
	if err := os.Chmod(ref.File, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	custody := keycustody.ProbeExternal(ctx, drifted)
	if custody.State != keycustody.Unreachable || !strings.Contains(custody.Reason, "sole custody") {
		t.Fatalf("a 0644 key file probes as %+v, want Unreachable with secretref's custody wording", custody)
	}

	undeclared := keycustody.ProbeExternal(ctx, secretref.Ref{})
	if undeclared.State != keycustody.NotDeclared {
		t.Fatalf("an empty reference probes as %+v, want NotDeclared", undeclared)
	}

	ambiguous := keycustody.ProbeExternal(ctx, secretref.Ref{File: ref.File, Env: "RETND_KEY"})
	if ambiguous.State != keycustody.Unreachable || !strings.Contains(ambiguous.Reason, "more than one") {
		t.Fatalf("two sources probe as %+v, want Unreachable carrying ErrAmbiguousSource's wording", ambiguous)
	}

	for _, p := range []keycustody.Probe{resolves, missing, custody, undeclared, ambiguous} {
		for _, rendered := range [][]byte{
			[]byte(fmt.Sprintf("%v", p)), []byte(fmt.Sprintf("%+v", p)), []byte(fmt.Sprintf("%#v", p)), []byte(p.String()),
		} {
			if carriesMaterial(rendered, material) {
				t.Fatalf("probe %+v renders the external key", p.State)
			}
		}
		js, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("json: %v", err)
		}
		if carriesMaterial(js, material) {
			t.Fatalf("probe JSON renders the external key")
		}
	}
}

func TestResolvabilityStatesHaveDistinctStableNames(t *testing.T) {
	seen := map[string]keycustody.Resolvability{}
	for _, r := range []keycustody.Resolvability{keycustody.NotDeclared, keycustody.Unreachable, keycustody.Resolves} {
		name := r.String()
		if name == "" || strings.Contains(name, "Resolvability(") {
			t.Fatalf("%d renders as %q, want a word an operator reads", r, name)
		}
		if prev, dup := seen[name]; dup {
			t.Fatalf("%d and %d both render as %q", prev, r, name)
		}
		seen[name] = r
	}
}

func TestWithExternalKeyHandsTheMaterialToTheCallbackOnlyAndZeroesIt(t *testing.T) {
	ref, material := externalKeyFile(t)
	var seen []byte
	err := keycustody.WithExternalKey(context.Background(), ref, func(key []byte) error {
		if string(key) != string(material) {
			t.Fatalf("callback got %d bytes that are not the file's trimmed content", len(key))
		}
		seen = key
		return nil
	})
	if err != nil {
		t.Fatalf("WithExternalKey: %v", err)
	}
	for _, b := range seen {
		if b != 0 {
			t.Fatalf("the slice handed to the callback still holds material after the call returned")
		}
	}
}

func TestWithExternalKeyRefusesWhatSecretrefRefuses(t *testing.T) {
	err := keycustody.WithExternalKey(context.Background(), secretref.Ref{}, func([]byte) error {
		t.Fatalf("callback ran with no source declared")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "no secret source") {
		t.Fatalf("err = %v, want secretref.ErrNoSource's refusal passed through", err)
	}
}

// The external reference IS secretref.Ref. There is no fourth field and no
// wrapper type with one, so TestRefFieldSetMatchesTheConfiguredOnes keeps
// covering this surface for free. A Provider carries the Ref itself.
func TestTheExternalReferenceIsSecretrefRefItself(t *testing.T) {
	var p keycustody.Provider
	f, ok := reflect.TypeOf(p).FieldByName("External")
	if !ok {
		t.Fatalf("Provider has no External field")
	}
	if f.Type != reflect.TypeFor[secretref.Ref]() {
		t.Fatalf("Provider.External is %s, want secretref.Ref itself so the one-shape pin covers it", f.Type)
	}
}

func TestProviderDescribesItselfWithoutMaterial(t *testing.T) {
	ref, material := externalKeyFile(t)
	for _, p := range []keycustody.Provider{
		{Managed: "k0123456789abcdef"},
		{External: ref},
		{},
	} {
		d := p.Describe()
		if d == "" {
			t.Fatalf("%+v describes as nothing", p)
		}
		if carriesMaterial([]byte(d), material) {
			t.Fatalf("%+v describes with the key in it", p)
		}
	}
	if d := (keycustody.Provider{External: ref}).Describe(); !strings.Contains(d, ref.String()) {
		t.Fatalf("an external provider describes as %q, want the reference %q in it", d, ref.String())
	}
	if d := (keycustody.Provider{Managed: "k0123456789abcdef"}).Describe(); !strings.Contains(d, "k0123456789abcdef") {
		t.Fatalf("a managed provider describes as %q, want the key id in it", d)
	}
}
