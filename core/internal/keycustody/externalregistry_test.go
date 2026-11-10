package keycustody_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/secretref"
)

// An external key as a registry entry (#1292): a row in encryption_keys
// with provider external and the recipient the reference resolved to, never
// the reference's content and never the material. These tests are written
// against the design in the issue and have not been run.

// externalStore opens a store whose config declares refs, which is what the
// service hands it from the global policy and every per-set override.
func externalStore(t *testing.T, refs ...secretref.Ref) (*keycustody.Store, string) {
	t.Helper()
	root := t.TempDir()
	s := openStore(t, root)
	s.UseExternalRefs(func() []secretref.Ref { return refs })
	return s, root
}

func TestRegisteringAnExternalKeyAddsAnExternalRowWithItsRecipient(t *testing.T) {
	ctx := context.Background()
	ref, material := externalKeyFile(t)
	s, _ := externalStore(t, ref)

	rec, err := s.RegisterExternal(ctx, ref, fixedNow)
	if err != nil {
		t.Fatalf("RegisterExternal: %v", err)
	}
	want, err := testFormat.Recipient(material)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Provider != keycustody.ProviderExternal || rec.Recipient != want || rec.Status != keycustody.StatusActive {
		t.Fatalf("record = %+v, want an active external row with recipient %s", rec, want)
	}
	if !strings.HasPrefix(rec.ID, "x") || strings.HasPrefix(rec.ID, "k") {
		t.Fatalf("id %q: an external key's id must be told apart from a managed key's (k...)", rec.ID)
	}
	keys, err := s.Keys(ctx)
	if err != nil || len(keys) != 1 || keys[0].ID != rec.ID || keys[0].Provider != keycustody.ProviderExternal {
		t.Fatalf("Keys = %+v, %v, want the one external row", keys, err)
	}
}

// The id follows the key, not the reference and not the process: the same
// key reached through two references, or after a restart, is one row.
func TestTheSameExternalKeyIsOneRowWhateverReferenceReachesIt(t *testing.T) {
	ctx := context.Background()
	ref, material := externalKeyFile(t)
	t.Setenv("RETND_TEST_EXTERNAL_KEY", string(material))
	envRef := secretref.Ref{Env: "RETND_TEST_EXTERNAL_KEY"}
	s, root := externalStore(t, ref, envRef)

	a, err := s.RegisterExternal(ctx, ref, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.RegisterExternal(ctx, envRef, fixedNow.Add(1))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("one key got ids %q and %q through two references", a.ID, b.ID)
	}
	again := openStore(t, root)
	again.UseExternalRefs(func() []secretref.Ref { return []secretref.Ref{ref} })
	c, err := again.RegisterExternal(ctx, ref, fixedNow.Add(2))
	if err != nil || c.ID != a.ID {
		t.Fatalf("after a restart RegisterExternal = %+v, %v, want id %q", c, err, a.ID)
	}
	if keys, _ := again.Keys(ctx); len(keys) != 1 {
		t.Fatalf("%d rows after registering one key three times, want 1", len(keys))
	}
}

func TestWithKeyOnAnExternalIDResolvesThroughADeclaredReferenceOnly(t *testing.T) {
	ctx := context.Background()
	ref, material := externalKeyFile(t)
	s, root := externalStore(t, ref)
	rec, err := s.RegisterExternal(ctx, ref, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := materialOf(t, s, rec.ID); !bytes.Equal(got, bytes.TrimSpace(material)) {
		t.Fatalf("WithKey handed different material than the reference holds")
	}

	// No reference declared any more: the callback is never called.
	bare := openStore(t, root)
	bare.UseExternalRefs(func() []secretref.Ref { return nil })
	called := false
	err = bare.WithKey(ctx, rec.ID, func([]byte) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("WithKey with no declared reference: err %v, callback called %v, want a refusal and no call", err, called)
	}
	if !errors.Is(err, keycustody.ErrExternalKeyUnresolvable) {
		t.Fatalf("err = %v, want ErrExternalKeyUnresolvable", err)
	}
}

// A reference that now yields a different key is not the registered key.
func TestAnExternalReferenceThatChangedKeyIsAMismatchAndCallsNothing(t *testing.T) {
	ctx := context.Background()
	ref, _ := externalKeyFile(t)
	s, _ := externalStore(t, ref)
	rec, err := s.RegisterExternal(ctx, ref, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := testFormat.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref.File, append(other, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = s.WithKey(ctx, rec.ID, func([]byte) error { called = true; return nil })
	if called || !errors.Is(err, keycustody.ErrExternalKeyUnresolvable) {
		t.Fatalf("err = %v, called = %v, want ErrExternalKeyUnresolvable and no call", err, called)
	}
	if carriesMaterial([]byte(err.Error()), other) {
		t.Fatal("the refusal quotes the other key's material")
	}
}

func TestAnExternalReferenceThatIsNotAnAgeIdentityIsNotRegistered(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plain.key")
	if err := os.WriteFile(path, []byte("hunter2-not-an-age-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := secretref.Ref{File: path}
	s, _ := externalStore(t, ref)
	_, err := s.RegisterExternal(ctx, ref, fixedNow)
	if !errors.Is(err, keycustody.ErrNotAnIdentity) {
		t.Fatalf("err = %v, want ErrNotAnIdentity", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatal("the refusal quotes the secret")
	}
	if keys, _ := s.Keys(ctx); len(keys) != 0 {
		t.Fatalf("%d row(s) for a key that cannot be opened by the format", len(keys))
	}
}

func TestAnUnresolvableExternalReferenceRegistersNothing(t *testing.T) {
	ctx := context.Background()
	ref := secretref.Ref{File: filepath.Join(t.TempDir(), "gone.key")}
	s, _ := externalStore(t, ref)
	if _, err := s.RegisterExternal(ctx, ref, fixedNow); err == nil {
		t.Fatal("RegisterExternal succeeded for a file that does not exist")
	}
	if keys, _ := s.Keys(ctx); len(keys) != 0 {
		t.Fatalf("%d row(s) for a reference that never resolved", len(keys))
	}
}

// An external key is not the managed active key: Active, Create and Rotate
// are about the key this product holds.
func TestAnExternalKeyIsNeverTheManagedActiveKey(t *testing.T) {
	ctx := context.Background()
	ref, _ := externalKeyFile(t)
	s, _ := externalStore(t, ref)
	if _, err := s.RegisterExternal(ctx, ref, fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Active(ctx); err != nil || ok {
		t.Fatalf("Active = _, %v, %v with only an external key registered, want none", ok, err)
	}
	if _, err := s.Rotate(ctx, fixedNow); !errors.Is(err, keycustody.ErrNoActiveKey) {
		t.Fatalf("Rotate err = %v, want ErrNoActiveKey", err)
	}
	managed, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Create beside an external key: %v", err)
	}
	if got, ok, _ := s.Active(ctx); !ok || got.ID != managed.ID {
		t.Fatalf("Active = %+v, want the managed key", got)
	}
}

func TestRotatingAnExternalKeySupersedesTheOldOneAndKeepsItDecrypting(t *testing.T) {
	ctx := context.Background()
	oldRef, oldMaterial := externalKeyFile(t)
	newRef, _ := externalKeyFile(t)
	s, _ := externalStore(t, oldRef, newRef)
	old, err := s.RegisterExternal(ctx, oldRef, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.RotateExternal(ctx, old.ID, newRef, fixedNow.Add(1))
	if err != nil {
		t.Fatalf("RotateExternal: %v", err)
	}
	if next.ID == old.ID || next.Status != keycustody.StatusActive {
		t.Fatalf("new record = %+v", next)
	}
	keys, _ := s.Keys(ctx)
	status := map[string]keycustody.Status{}
	for _, k := range keys {
		status[k.ID] = k.Status
	}
	if status[old.ID] != keycustody.StatusSuperseded || status[next.ID] != keycustody.StatusActive {
		t.Fatalf("statuses = %v, want old superseded and new active", status)
	}
	if got := materialOf(t, s, old.ID); !bytes.Equal(got, bytes.TrimSpace(oldMaterial)) {
		t.Fatal("a superseded external key no longer resolves to its own material")
	}
	if _, err := s.RotateExternal(ctx, "xdoesnotexist", newRef, fixedNow); err == nil {
		t.Fatal("RotateExternal superseded a key that is not registered")
	}
}

// The bundle is the one moment material leaves the host; an operator's own
// key is already held by the operator and is never copied into one.
func TestAnExternalKeyIsNotExportable(t *testing.T) {
	ctx := context.Background()
	ref, material := externalKeyFile(t)
	s, _ := externalStore(t, ref)
	rec, err := s.RegisterExternal(ctx, ref, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err = s.Export(ctx, rec.ID, &out, fixedNow)
	if !errors.Is(err, keycustody.ErrExternalKeyNotExportable) {
		t.Fatalf("Export err = %v, want ErrExternalKeyNotExportable", err)
	}
	if out.Len() != 0 || carriesMaterial(out.Bytes(), material) {
		t.Fatal("something was written for a refused export")
	}
}

// An external key has no bundle to confirm, so it is neither the active key
// nor an unconfirmed one in the health section.
func TestAnExternalKeyDoesNotDegradeKeyCustodyHealth(t *testing.T) {
	ctx := context.Background()
	ref, _ := externalKeyFile(t)
	s, _ := externalStore(t, ref)
	rec, err := s.RegisterExternal(ctx, ref, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Health(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if h.ActiveKeyID == rec.ID {
		t.Fatal("the external key is reported as the active managed key")
	}
	for _, id := range h.UnconfirmedKeyIDs {
		if id == rec.ID {
			t.Fatal("the external key is listed as awaiting a bundle confirmation")
		}
	}
}

// Canary: neither the registry rows nor their JSON carry the material, the
// reference's source or any rendering of either. The control plants the
// canary and must be caught by the same scan.
func TestExternalRegistryRowsCarryNoMaterialCanary(t *testing.T) {
	ctx := context.Background()
	ref, material := externalKeyFile(t)
	s, root := externalStore(t, ref)
	if _, err := s.RegisterExternal(ctx, ref, fixedNow); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.Keys(ctx)
	js, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	scanned := append(js, readAllFiles(t, root)...)
	for i, m := range append(renderings(material), []byte(ref.File)) {
		if carriesMaterial(scanned, m) {
			t.Fatalf("the registry JSON or the state files carry rendering #%d of the material or the reference source", i)
		}
	}
	planted := append(append([]byte(nil), scanned...), canary...)
	if !carriesMaterial(planted, canary) {
		t.Fatal("control: the scan did not catch a planted canary, so the checks above prove nothing")
	}
}

// A deployment whose only key is an external one, under `required`, is
// healthy while its reference resolves: there is no managed key to be
// active and no bundle to confirm, and Degraded must not read that absence
// as a problem. Red while Store.Health reports no active key for it.
func TestAnExternalOnlyDeploymentReadsHealthyWhileItsReferenceResolves_1292(t *testing.T) {
	ctx := context.Background()
	ref, _ := externalKeyFile(t)
	s, _ := externalStore(t, ref)
	rec, err := s.RegisterExternal(ctx, ref, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Health(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if h.Degraded() {
		t.Fatalf("an external-only deployment reads degraded: %+v", h)
	}
	if h.ActiveKeyID != rec.ID {
		t.Fatalf("ActiveKeyID = %q, want the external key %q", h.ActiveKeyID, rec.ID)
	}
	// Control: nothing registered under required is still degraded.
	empty, _ := externalStore(t)
	if eh, err := empty.Health(ctx, true); err != nil || !eh.Degraded() {
		t.Fatalf("control: an empty registry under required = %+v, %v, want degraded", eh, err)
	}
}
