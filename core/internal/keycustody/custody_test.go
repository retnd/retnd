package keycustody_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/secretref"
)

// The managed key store, FR-63: a key is minted once, held under the same
// custody rule core/internal/secretref enforces, and identified by
// something no restart can change.

var fixedNow = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

// openStore opens a store over its own fresh directory and a file registry
// beside it, which is the shape the service wires at
// filepath.Dir(configPath). The directory is created by Open, so the test
// also proves Open mints a 0700 directory rather than inheriting umask.
func openStore(t *testing.T, root string) *keycustody.Store {
	t.Helper()
	reg, err := keycustody.OpenFileRegistry(filepath.Join(root, "registry.json"))
	if err != nil {
		t.Fatalf("OpenFileRegistry: %v", err)
	}
	s, err := keycustody.Open(filepath.Join(root, keycustody.DirName), reg, testFormat)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// materialOf reads the key back through the one door that exists for it.
func materialOf(t *testing.T, s *keycustody.Store, id string) []byte {
	t.Helper()
	var got []byte
	err := s.WithKey(context.Background(), id, func(key []byte) error {
		got = append([]byte(nil), key...)
		return nil
	})
	if err != nil {
		t.Fatalf("WithKey(%s): %v", id, err)
	}
	return got
}

func TestCreateMintsAnActiveKeyUnderOwnerOnlyCustody(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.ID == "" || rec.Status != keycustody.StatusActive || !rec.Created.Equal(fixedNow) || rec.Retired != nil {
		t.Fatalf("Create = %+v, want an active record created at %s with no retirement", rec, fixedNow)
	}

	dir := filepath.Join(root, keycustody.DirName)
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat key dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("key directory mode = %04o, want 0700", di.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var keyFiles int
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if strings.HasSuffix(e.Name(), ".partial") {
			t.Fatalf("a .partial file %s survived a successful Create", e.Name())
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s has mode %04o, want 0600", e.Name(), info.Mode().Perm())
		}
		keyFiles++
	}
	if keyFiles != 1 {
		t.Fatalf("key directory holds %d files, want exactly the one key", keyFiles)
	}

	material := materialOf(t, s, rec.ID)
	if !strings.HasPrefix(string(material), "AGE-SECRET-KEY-1") {
		t.Fatalf("material is not an age X25519 identity")
	}
	if got, err := testFormat.Recipient(material); err != nil || got != rec.Recipient || !strings.HasPrefix(got, "age1") {
		t.Fatalf("record recipient = %q, derived %q (%v): the record must carry the public half of the key on disk", rec.Recipient, got, err)
	}
	other := openStore(t, t.TempDir())
	otherRec, err := other.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if string(materialOf(t, other, otherRec.ID)) == string(material) || otherRec.Recipient == rec.Recipient || otherRec.ID == rec.ID {
		t.Fatalf("two independent Creates produced the same key or id: that is not a CSPRNG output")
	}
}

func TestASecondCreateIsRefusedWhileAKeyIsActive(t *testing.T) {
	s := openStore(t, t.TempDir())
	if _, err := s.Create(context.Background(), fixedNow); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := s.Create(context.Background(), fixedNow.Add(time.Minute))
	if !errors.Is(err, keycustody.ErrActiveKeyExists) {
		t.Fatalf("second Create err = %v, want ErrActiveKeyExists: a second active key is a rotation, and rotation is a separate deliberate verb", err)
	}
}

// A restart, and a second process start, leave the key id and material
// unchanged: identity is not derived from process state.
func TestARestartLeavesTheKeyIDAndMaterialUnchanged(t *testing.T) {
	root := t.TempDir()
	first := openStore(t, root)
	rec, err := first.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantMaterial := materialOf(t, first, rec.ID)

	for restart := 1; restart <= 2; restart++ {
		again := openStore(t, root)
		active, ok, err := again.Active(context.Background())
		if err != nil || !ok {
			t.Fatalf("restart %d: Active = %+v, %v, %v; want the key minted before the restart", restart, active, ok, err)
		}
		if active.ID != rec.ID {
			t.Fatalf("restart %d: active id %q, want %q (identity must survive a process)", restart, active.ID, rec.ID)
		}
		if !active.Created.Equal(rec.Created) {
			t.Fatalf("restart %d: created %s, want %s", restart, active.Created, rec.Created)
		}
		if got := materialOf(t, again, rec.ID); string(got) != string(wantMaterial) {
			t.Fatalf("restart %d: material changed across a restart", restart)
		}
	}
}

func TestRotateMintsANewActiveKeyAndSupersedesTheOld(t *testing.T) {
	s := openStore(t, t.TempDir())
	ctx := context.Background()
	if _, err := s.Rotate(ctx, fixedNow); !errors.Is(err, keycustody.ErrNoActiveKey) {
		t.Fatalf("Rotate with no key err = %v, want ErrNoActiveKey", err)
	}
	old, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oldMaterial := materialOf(t, s, old.ID)

	later := fixedNow.Add(48 * time.Hour)
	fresh, err := s.Rotate(ctx, later)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if fresh.ID == old.ID {
		t.Fatalf("Rotate reused id %s", old.ID)
	}
	if fresh.Status != keycustody.StatusActive || !fresh.Created.Equal(later) {
		t.Fatalf("rotated key = %+v, want active and created at %s", fresh, later)
	}
	keys, err := s.Keys(ctx)
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("Keys = %+v, want both the superseded and the active key", keys)
	}
	for _, k := range keys {
		if k.ID == old.ID && k.Status != keycustody.StatusSuperseded {
			t.Fatalf("old key status = %s, want superseded: a rotated-out key still decrypts every copy recorded against it", k.Status)
		}
	}
	// The superseded key is still readable: #992's retention refusal is
	// what stands between it and retirement, not this package.
	if got := materialOf(t, s, old.ID); string(got) != string(oldMaterial) {
		t.Fatalf("superseded key material changed on rotation")
	}
	if got := materialOf(t, s, fresh.ID); string(got) == string(oldMaterial) {
		t.Fatalf("rotation produced the same material")
	}
}

// A managed key file whose mode gains group-read is refused on the next
// use, naming the path and never the material.
func TestAKeyFileThatGainsGroupReadIsRefusedByName(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	material := materialOf(t, s, rec.ID)
	path := filepath.Join(root, keycustody.DirName, rec.ID+".key")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	err = s.WithKey(context.Background(), rec.ID, func([]byte) error {
		t.Fatalf("the callback ran over a key file another account can read")
		return nil
	})
	assertCustodyRefusal(t, err, path, material)

	probe := s.Probe(context.Background(), rec.ID)
	if probe.State != keycustody.Unreachable {
		t.Fatalf("Probe after the mode drift = %+v, want Unreachable", probe)
	}
}

// The directory gaining group-write is the same refusal one level up:
// secretref's ancestor walk, applied to the managed key's own directory.
func TestAKeyDirectoryThatGainsGroupWriteIsRefusedByName(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	material := materialOf(t, s, rec.ID)
	dir := filepath.Join(root, keycustody.DirName)
	if err := os.Chmod(dir, 0o720); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = s.WithKey(context.Background(), rec.ID, func([]byte) error {
		t.Fatalf("the callback ran under a directory another account can write")
		return nil
	})
	assertCustodyRefusal(t, err, dir, material)
}

// Group-READ on the directory is the stricter half of "0600 in 0700" and
// is this package's own check: secretref's walk only asks about writers.
func TestAKeyDirectoryThatGainsGroupReadIsRefusedByName(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	material := materialOf(t, s, rec.ID)
	dir := filepath.Join(root, keycustody.DirName)
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = s.WithKey(context.Background(), rec.ID, func([]byte) error { return nil })
	assertCustodyRefusal(t, err, dir, material)

	// A fresh Open over the drifted directory refuses too, so a restart
	// cannot quietly re-adopt a directory a human loosened.
	reg, err := keycustody.OpenFileRegistry(filepath.Join(root, "registry.json"))
	if err != nil {
		t.Fatalf("OpenFileRegistry: %v", err)
	}
	if _, err := keycustody.Open(dir, reg, testFormat); !errors.Is(err, secretref.ErrCustody) {
		t.Fatalf("Open over a 0750 directory err = %v, want ErrCustody", err)
	}
}

func TestASymlinkedKeyFileIsRefused(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	material := materialOf(t, s, rec.ID)
	dir := filepath.Join(root, keycustody.DirName)
	path := filepath.Join(dir, rec.ID+".key")
	moved := filepath.Join(root, "elsewhere.key")
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.Symlink(moved, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	err = s.WithKey(context.Background(), rec.ID, func([]byte) error { return nil })
	assertCustodyRefusal(t, err, path, material)
}

// The negative control for the four refusals above: an untouched 0600 key
// in its 0700 directory is accepted. Without this, a WithKey that refused
// everything would pass every refusal test.
func TestAnUntouchedKeyIsAcceptedControl(t *testing.T) {
	s := openStore(t, t.TempDir())
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var ran bool
	if err := s.WithKey(context.Background(), rec.ID, func(key []byte) error {
		ran = strings.HasPrefix(string(key), "AGE-SECRET-KEY-1")
		return nil
	}); err != nil || !ran {
		t.Fatalf("WithKey over an untouched key: ran=%v err=%v", ran, err)
	}
	if p := s.Probe(context.Background(), rec.ID); p.State != keycustody.Resolves {
		t.Fatalf("Probe = %+v, want Resolves", p)
	}
}

func TestWithKeyZeroesTheMaterialAfterTheCallback(t *testing.T) {
	s := openStore(t, t.TempDir())
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var seen []byte
	if err := s.WithKey(context.Background(), rec.ID, func(key []byte) error {
		seen = key // deliberately keep the slice the callback was handed
		return nil
	}); err != nil {
		t.Fatalf("WithKey: %v", err)
	}
	for _, b := range seen {
		if b != 0 {
			t.Fatalf("the slice handed to the callback still holds material after WithKey returned")
		}
	}
}

func TestAnUnknownKeyIDIsItsOwnRefusal(t *testing.T) {
	s := openStore(t, t.TempDir())
	err := s.WithKey(context.Background(), "k0000000000000000", func([]byte) error { return nil })
	if !errors.Is(err, keycustody.ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
	if p := s.Probe(context.Background(), "k0000000000000000"); p.State != keycustody.NotDeclared {
		t.Fatalf("Probe of an id the registry never minted = %+v, want NotDeclared", p)
	}
}

// assertCustodyRefusal is what every refusal above has to look like: the
// secretref sentinel, the offending path in the message, and not one
// rendering of the material anywhere in it.
func assertCustodyRefusal(t *testing.T, err error, path string, material []byte) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a custody refusal naming %s, got nil", path)
	}
	if !errors.Is(err, secretref.ErrCustody) {
		t.Fatalf("err = %v, want secretref.ErrCustody: one custody rule, one sentinel", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("refusal %q does not name %s, which is what the operator has to go and fix", err, path)
	}
	if carriesMaterial([]byte(err.Error()), material) {
		t.Fatalf("the refusal carries the key material")
	}
}

// A key file that no longer derives the recipient the registry recorded for
// its id (swapped, restored from the wrong backup, hand-edited) is refused
// by ErrKeyMismatch rather than used. The untouched key is accepted by
// TestAnUntouchedKeyIsAcceptedControl above, which is this guard's negative
// control.
func TestAKeyFileSwappedForAnotherKeyIsRefused(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	ctx := context.Background()
	rec, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	material := materialOf(t, s, rec.ID)
	foreign, _, err := testFormat.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	path := filepath.Join(root, keycustody.DirName, rec.ID+".key")
	if err := os.WriteFile(path, append(foreign, '\n'), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	err = s.WithKey(ctx, rec.ID, func([]byte) error {
		t.Fatalf("the callback ran over a key file that is not the recorded key")
		return nil
	})
	if !errors.Is(err, keycustody.ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}
	if carriesMaterial([]byte(err.Error()), material) || carriesMaterial([]byte(err.Error()), foreign) {
		t.Fatalf("the refusal carries key material")
	}
	if p := s.Probe(ctx, rec.ID); p.State != keycustody.Unreachable {
		t.Fatalf("Probe over a swapped key file = %+v, want Unreachable", p)
	}

	// A file that is not a key at all is refused the same way, by name of
	// the format's own sentinel and without quoting its content.
	if err := os.WriteFile(path, []byte("not an identity "+string(canary)+"\n"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	err = s.WithKey(ctx, rec.ID, func([]byte) error { return nil })
	if !errors.Is(err, keycustody.ErrNotAnIdentity) || carriesMaterial([]byte(err.Error()), canary) {
		t.Fatalf("err = %v, want ErrNotAnIdentity without the file's content", err)
	}
}

// An external key that resolves through secretref but is not a key of the
// format would refuse every copy at write time, so the probe says so now.
// The valid-identity case is the negative control.
func TestAnExternalReferenceThatResolvesToANonKeyIsUnreachable(t *testing.T) {
	s := openStore(t, t.TempDir())
	ctx := context.Background()

	good, _ := externalKeyFile(t)
	if p := s.Resolvability(ctx, keycustody.Provider{External: good}); p.State != keycustody.Resolves {
		t.Fatalf("a valid external identity probes as %+v, want Resolves", p)
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	path := filepath.Join(dir, "wrong.key")
	if err := os.WriteFile(path, append([]byte("a passphrase, not an identity "), canary...), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	p := s.Resolvability(ctx, keycustody.Provider{External: secretref.Ref{File: path}})
	if p.State != keycustody.Unreachable || !strings.Contains(p.Reason, "AGE-SECRET-KEY-1") {
		t.Fatalf("a non-identity external key probes as %+v, want Unreachable naming the expected shape", p)
	}
	if carriesMaterial([]byte(p.String()), canary) {
		t.Fatalf("the probe quotes the file's content")
	}
}
