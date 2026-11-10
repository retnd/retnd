package keycustody_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/keycustody"
)

// #1289: a key-import path. A machine with nothing but the recovery bundle
// registers the key it holds, so copies recorded under that key id resolve
// there. Import is the write half of ReadBundle, and it has the same two
// duties as everything else in this package: never show the material, and
// leave nothing half-done when it refuses.

// readBundle parses bundle bytes the way a caller would before Import.
func readBundle(t *testing.T, raw []byte) keycustody.Bundle {
	t.Helper()
	b, err := keycustody.ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	return b
}

// keyFiles lists the .key and .partial files in a store root's key directory.
func keyFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, keycustody.DirName))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A bundle exported on one machine registers on a clean one: the record is
// the same id and recipient, the key resolves there with the same material,
// it is NOT the key new copies are written under, and holding the bundle
// counts as having confirmed it.
func TestImportRegistersABundleOnACleanMachine_1289(t *testing.T) {
	ctx := context.Background()
	src := openStore(t, t.TempDir())
	rec, bundleBytes, receipt, material := exportBundle(t, src)

	cleanRoot := t.TempDir()
	clean := openStore(t, cleanRoot)
	got, imported, err := clean.Import(ctx, readBundle(t, bundleBytes), fixedNow.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !imported {
		t.Fatalf("Import reported nothing was imported on a machine that had never seen the key")
	}
	if got.ID != rec.ID || got.Recipient != rec.Recipient || !got.Created.Equal(rec.Created) {
		t.Fatalf("imported record = %+v, want id %s, recipient %s, created %s", got, rec.ID, rec.Recipient, rec.Created)
	}
	if got.Status != keycustody.StatusSuperseded {
		t.Fatalf("imported key is %q: a recovery key must not become the key new copies are encrypted under", got.Status)
	}
	if _, ok, err := clean.Active(ctx); err != nil || ok {
		t.Fatalf("Active = %v, %v: importing a key must not create an active one", ok, err)
	}
	if !bytes.Equal(materialOf(t, clean, rec.ID), material) {
		t.Fatalf("the imported key does not resolve to the exported material")
	}
	if probe := clean.Probe(ctx, rec.ID); probe.State != keycustody.Resolves {
		t.Fatalf("Probe = %+v, want the imported key to resolve", probe)
	}

	info, err := os.Stat(filepath.Join(cleanRoot, keycustody.DirName, rec.ID+".key"))
	if err != nil {
		t.Fatalf("no key file after Import: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("imported key file mode = %04o, want 0600", info.Mode().Perm())
	}
	for _, name := range keyFiles(t, cleanRoot) {
		if filepath.Ext(name) == ".partial" {
			t.Fatalf("a .partial file %s survived a successful Import", name)
		}
	}

	h, err := clean.Health(ctx, true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if len(h.UnconfirmedKeyIDs) != 0 {
		t.Fatalf("Health lists %v as unconfirmed: whoever holds the bundle has demonstrated they hold it", h.UnconfirmedKeyIDs)
	}
	exports, err := openJournalRegistry(t, cleanRoot).Exports(ctx, rec.ID)
	if err != nil || len(exports) != 1 || exports[0].Digest != receipt.Digest {
		t.Fatalf("Exports = %+v, %v, want the one export the bundle's digest names", exports, err)
	}
}

// Importing the same bundle twice, including from a fresh process over the
// same directory, changes nothing: one key, one key file with the same
// bytes, one export, one confirmation.
func TestImportIsIdempotent_1289(t *testing.T) {
	ctx := context.Background()
	src := openStore(t, t.TempDir())
	rec, bundleBytes, _, _ := exportBundle(t, src)

	root := t.TempDir()
	first := openStore(t, root)
	if _, imported, err := first.Import(ctx, readBundle(t, bundleBytes), fixedNow); err != nil || !imported {
		t.Fatalf("first Import = %v, %v", imported, err)
	}
	keyPath := filepath.Join(root, keycustody.DirName, rec.ID+".key")
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}

	// A restart: a new store over the same directory and database.
	second := openStore(t, root)
	got, imported, err := second.Import(ctx, readBundle(t, bundleBytes), fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if imported {
		t.Fatalf("the second Import reported it imported something")
	}
	if got.ID != rec.ID {
		t.Fatalf("second Import returned %+v, want the record for %s", got, rec.ID)
	}

	after, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("the key file changed on a repeat import (%v)", err)
	}
	keys, err := second.Keys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("Keys = %+v, %v, want exactly one", keys, err)
	}
	reg := openJournalRegistry(t, root)
	if exports, err := reg.Exports(ctx, rec.ID); err != nil || len(exports) != 1 {
		t.Fatalf("Exports = %+v, %v, want one: a repeat import must not record a second", exports, err)
	}
	if confs, err := reg.Confirmations(ctx); err != nil || len(confs) != 1 {
		t.Fatalf("Confirmations = %+v, %v, want one", confs, err)
	}
}

// Importing a bundle on the host that minted the key is a no-op for the
// key, so a person who tries their bundle on the original machine cannot
// damage it.
func TestImportOnTheHostThatMintedTheKeyChangesNoKey_1289(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := openStore(t, root)
	rec, bundleBytes, _, material := exportBundle(t, s)

	got, imported, err := s.Import(ctx, readBundle(t, bundleBytes), fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported {
		t.Fatalf("Import reported importing a key the host already holds")
	}
	if got.Status != keycustody.StatusActive {
		t.Fatalf("the host's active key is now %q after importing its own bundle", got.Status)
	}
	if !bytes.Equal(materialOf(t, s, rec.ID), material) {
		t.Fatalf("the key material changed")
	}
}

// A machine that already has its own active key keeps it: the imported key
// is for opening old copies only.
func TestImportLeavesTheActiveKeyAlone_1289(t *testing.T) {
	ctx := context.Background()
	src := openStore(t, t.TempDir())
	rec, bundleBytes, _, _ := exportBundle(t, src)

	s := openStore(t, t.TempDir())
	own, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := s.Import(ctx, readBundle(t, bundleBytes), fixedNow.Add(time.Hour)); err != nil {
		t.Fatalf("Import: %v", err)
	}
	active, ok, err := s.Active(ctx)
	if err != nil || !ok || active.ID != own.ID {
		t.Fatalf("Active = %+v, %v, %v, want the machine's own key %s", active, ok, err, own.ID)
	}
	if rec.ID == own.ID {
		t.Fatalf("fixture: two keys share an id")
	}
}

// An id already registered to a different key is refused, and nothing is
// written: copies recorded under that id would otherwise open, or fail to
// open, depending on which key won.
func TestImportRefusesAnIdRegisteredToADifferentKey_1289(t *testing.T) {
	ctx := context.Background()
	src := openStore(t, t.TempDir())
	rec, bundleBytes, _, material := exportBundle(t, src)

	root := t.TempDir()
	reg := openJournalRegistry(t, root)
	s, err := keycustody.Open(filepath.Join(root, keycustody.DirName), reg, testFormat)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	other := keycustody.KeyRecord{ID: rec.ID, Recipient: "age1someoneelse", Created: fixedNow, Status: keycustody.StatusSuperseded}
	if err := reg.AddKey(ctx, other, ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	_, imported, err := s.Import(ctx, readBundle(t, bundleBytes), fixedNow)
	if !errors.Is(err, keycustody.ErrKeyConflict) {
		t.Fatalf("Import err = %v, want ErrKeyConflict", err)
	}
	if imported {
		t.Fatalf("a refused Import reported success")
	}
	if len(keyFiles(t, root)) != 0 {
		t.Fatalf("a refused Import left %v in the key directory", keyFiles(t, root))
	}
	if carriesMaterial([]byte(err.Error()), material) {
		t.Fatalf("the refusal quotes the key: %v", err)
	}
	keys, _ := reg.Keys(ctx)
	if len(keys) != 1 || keys[0].Recipient != "age1someoneelse" {
		t.Fatalf("Keys = %+v: the existing record was touched", keys)
	}
}

// A key file already sitting under the id with other bytes is a conflict
// too, and it is left exactly as it was.
func TestImportRefusesAKeyFileThatHoldsADifferentKey_1289(t *testing.T) {
	ctx := context.Background()
	src := openStore(t, t.TempDir())
	rec, bundleBytes, _, _ := exportBundle(t, src)

	root := t.TempDir()
	s := openStore(t, root)
	path := filepath.Join(root, keycustody.DirName, rec.ID+".key")
	stray := []byte("AGE-SECRET-KEY-1NOTTHEKEYTHATTHEBUNDLEHOLDS\n")
	if err := os.WriteFile(path, stray, 0o600); err != nil {
		t.Fatalf("write stray key file: %v", err)
	}

	if _, _, err := s.Import(ctx, readBundle(t, bundleBytes), fixedNow); !errors.Is(err, keycustody.ErrKeyConflict) {
		t.Fatalf("Import err = %v, want ErrKeyConflict", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, stray) {
		t.Fatalf("the stray key file was changed by a refused Import (%v)", err)
	}
	if keys, _ := s.Keys(ctx); len(keys) != 0 {
		t.Fatalf("a refused Import registered %+v", keys)
	}
}

// A bundle edited after export is refused by Import even when it was built
// by hand rather than through ReadBundle, and nothing is registered.
func TestImportRefusesABundleWhoseDigestDoesNotCoverIt_1289(t *testing.T) {
	ctx := context.Background()
	src := openStore(t, t.TempDir())
	_, bundleBytes, _, _ := exportBundle(t, src)
	b := readBundle(t, bundleBytes)
	b.KeyID = "k0000000000000bad" // relabelled after the digest was taken

	root := t.TempDir()
	s := openStore(t, root)
	if _, imported, err := s.Import(ctx, b, fixedNow); !errors.Is(err, keycustody.ErrBundleIntegrity) || imported {
		t.Fatalf("Import = %v, %v, want ErrBundleIntegrity", imported, err)
	}
	if len(keyFiles(t, root)) != 0 {
		t.Fatalf("a refused Import left %v behind", keyFiles(t, root))
	}
	if keys, _ := s.Keys(ctx); len(keys) != 0 {
		t.Fatalf("a refused Import registered %+v", keys)
	}
}

// The key id becomes a file name, so one that could leave the key directory
// is refused no matter what the digest says. A digest an attacker computed
// themselves is no defence, so this builds a bundle with a valid one.
func TestImportRefusesAKeyIdThatIsNotAPlainName_1289(t *testing.T) {
	ctx := context.Background()
	src := openStore(t, t.TempDir())
	_, bundleBytes, _, _ := exportBundle(t, src)

	for _, id := range []string{"../escape", "a/b", "", ".hidden", "k1\x00"} {
		b := readBundle(t, bundleBytes)
		b.KeyID = id
		b.Digest = redigest(t, b)

		root := t.TempDir()
		s := openStore(t, root)
		if _, imported, err := s.Import(ctx, b, fixedNow); err == nil || imported {
			t.Errorf("Import with key id %q = %v, %v, want a refusal", id, imported, err)
		}
		if _, err := os.Stat(filepath.Join(root, "escape.key")); err == nil {
			t.Errorf("key id %q wrote outside the key directory", id)
		}
		if len(keyFiles(t, root)) != 0 {
			t.Errorf("key id %q left %v behind", id, keyFiles(t, root))
		}
	}
}

// Material that is not the key the bundle's recipient names is refused even
// with a matching digest: the same check Decrypt makes, made before
// anything is written.
func TestImportRefusesMaterialThatIsNotTheNamedKey_1289(t *testing.T) {
	ctx := context.Background()
	a := openStore(t, t.TempDir())
	_, bundleA, _, _ := exportBundle(t, a)
	b := openStore(t, t.TempDir())
	_, _, _, materialB := exportBundle(t, b)

	bundle := readBundle(t, bundleA)
	bundle.Material = string(materialB)
	bundle.Digest = redigest(t, bundle)

	root := t.TempDir()
	s := openStore(t, root)
	_, imported, err := s.Import(ctx, bundle, fixedNow)
	if err == nil || imported {
		t.Fatalf("Import of mismatched material = %v, %v, want a refusal", imported, err)
	}
	if carriesMaterial([]byte(err.Error()), materialB) {
		t.Fatalf("the refusal quotes the material: %v", err)
	}
	if len(keyFiles(t, root)) != 0 {
		t.Fatalf("a refused Import left %v behind", keyFiles(t, root))
	}
}

// redigest recomputes a bundle's digest the way an editor who knew the
// scheme would: SHA-256 over version, key id, format, recipient, created
// (RFC 3339 nano, UTC) and material, each followed by a newline, as bundle.go
// documents. It is spelled out here on purpose, so a change to the scheme
// has to touch this test too.
func redigest(t *testing.T, b keycustody.Bundle) string {
	t.Helper()
	h := sha256.New()
	for _, f := range []string{b.Version, b.KeyID, b.FormatID, b.Recipient, b.Created.UTC().Format(time.RFC3339Nano), b.Material} {
		h.Write([]byte(f))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The bundle's printed instructions name what now exists: the verb that
// decrypts, the verb that imports, and the age CLI as the way that needs
// nothing of this product. A person holding only the file reads these, so a
// verb the binary does not have, or a missing one, is a defect.
func TestTheBundleInstructionsNameTheVerbsThatExistAndKeepTheAgeFallback_1289(t *testing.T) {
	s := openStore(t, t.TempDir())
	_, bundleBytes, _, _ := exportBundle(t, s)
	text := strings.Join(readBundle(t, bundleBytes).Instructions, "\n")
	for _, want := range []string{
		"retnd encryption decrypt",
		"--bundle",
		"--sidecar",
		"retnd encryption key import",
		"age -d -i identity.txt",
		"recovery-without-a-terminal",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the bundle instructions do not mention %q:\n%s", want, text)
		}
	}
}
