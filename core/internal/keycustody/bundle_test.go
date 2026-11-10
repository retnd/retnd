package keycustody_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/keycustody"
)

// The disaster-recovery bundle, FR-66's recoverable half: the one moment
// managed key material deliberately leaves the host, carrying enough for a
// person with the shipped binary and docs/recovery-without-a-terminal.md
// to get a copy back with no state database at all.

// exportBundle mints a key, exports it and returns the bundle bytes, the
// receipt and the material it carries.
func exportBundle(t *testing.T, s *keycustody.Store) (keycustody.KeyRecord, []byte, keycustody.Receipt, []byte) {
	t.Helper()
	ctx := context.Background()
	rec, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var out bytes.Buffer
	receipt, err := s.Export(ctx, rec.ID, &out, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return rec, out.Bytes(), receipt, materialOf(t, s, rec.ID)
}

// A bundle exported in a test decrypts a fixture copy in a clean
// environment with no state database present.
func TestABundleDecryptsAFixtureCopyOnACleanMachine(t *testing.T) {
	s := openStore(t, t.TempDir())
	rec, bundleBytes, receipt, _ := exportBundle(t, s)

	plaintext := []byte("the retained copy, as the producer wrote it\n")
	ciphertext := sealTo(t, rec.Recipient, plaintext)

	// The clean machine: a directory holding the bundle file and nothing
	// else. No registry, no key directory, no journal, and the Store that
	// minted the key is not consulted again.
	clean := t.TempDir()
	bundlePath := filepath.Join(clean, "retnd-key-bundle.json")
	if err := os.WriteFile(bundlePath, bundleBytes, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	entries, _ := os.ReadDir(clean)
	if len(entries) != 1 {
		t.Fatalf("the clean machine has %d files, want only the bundle", len(entries))
	}

	f, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer f.Close()
	b, err := keycustody.ReadBundle(f)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if b.KeyID != rec.ID || b.FormatID != testFormat.ID() || b.Digest != receipt.Digest {
		t.Fatalf("bundle = %+v, want key %s, format %s, digest %s", b, rec.ID, testFormat.ID(), receipt.Digest)
	}
	if len(b.Instructions) == 0 || !strings.Contains(strings.Join(b.Instructions, "\n"), "recovery-without-a-terminal") {
		t.Fatalf("bundle instructions %q do not point a person at docs/recovery-without-a-terminal.md", b.Instructions)
	}

	var recovered bytes.Buffer
	if err := b.Decrypt(testFormat, bytes.NewReader(ciphertext), &recovered); err != nil {
		t.Fatalf("Decrypt from the bundle alone: %v", err)
	}
	if !bytes.Equal(recovered.Bytes(), plaintext) {
		t.Fatalf("recovered %q, want the fixture plaintext", recovered.Bytes())
	}
}

func TestATamperedBundleIsRefusedByItsIntegrityMetadata(t *testing.T) {
	s := openStore(t, t.TempDir())
	_, bundleBytes, _, _ := exportBundle(t, s)

	var raw map[string]any
	if err := json.Unmarshal(bundleBytes, &raw); err != nil {
		t.Fatalf("the bundle is not JSON: %v", err)
	}
	mat, _ := raw["material"].(string)
	if mat == "" {
		t.Fatalf("bundle has no material field: %v", raw)
	}
	// Flip one character of the material and leave the digest alone.
	flipped := []byte(mat)
	if flipped[0] == 'A' {
		flipped[0] = 'B'
	} else {
		flipped[0] = 'A'
	}
	raw["material"] = string(flipped)
	tampered, _ := json.Marshal(raw)

	_, err := keycustody.ReadBundle(bytes.NewReader(tampered))
	if !errors.Is(err, keycustody.ErrBundleIntegrity) {
		t.Fatalf("ReadBundle over altered material err = %v, want ErrBundleIntegrity", err)
	}

	raw["material"] = mat
	raw["key_id"] = "k-somebody-elses-id"
	relabelled, _ := json.Marshal(raw)
	if _, err := keycustody.ReadBundle(bytes.NewReader(relabelled)); !errors.Is(err, keycustody.ErrBundleIntegrity) {
		t.Fatalf("ReadBundle over a relabelled key id err = %v, want ErrBundleIntegrity: the digest covers the identity, not just the bytes", err)
	}

	// Control: the untouched bundle still reads.
	if _, err := keycustody.ReadBundle(bytes.NewReader(bundleBytes)); err != nil {
		t.Fatalf("the untouched bundle is refused: %v", err)
	}
}

func TestADecryptWithTheWrongBundleFailsClosed(t *testing.T) {
	a := openStore(t, t.TempDir())
	recA, _, _, _ := exportBundle(t, a)
	b := openStore(t, t.TempDir())
	_, bundleB, _, _ := exportBundle(t, b)

	ciphertext := sealTo(t, recA.Recipient, []byte("sealed under key A"))
	bundle, err := keycustody.ReadBundle(bytes.NewReader(bundleB))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	var out bytes.Buffer
	if err := bundle.Decrypt(testFormat, bytes.NewReader(ciphertext), &out); err == nil {
		t.Fatalf("decrypting A's copy with B's bundle succeeded")
	}
	if out.Len() != 0 {
		t.Fatalf("%d bytes of partial plaintext were written on a failed decrypt", out.Len())
	}
}

func TestExportRecordsTheExportAndConfirmNeedsItsDigest(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	ctx := context.Background()
	rec, _, receipt, _ := exportBundle(t, s)

	if receipt.KeyID != rec.ID || receipt.Digest == "" || receipt.ExportedAt.IsZero() {
		t.Fatalf("receipt = %+v, want key id, digest and export time", receipt)
	}

	// Confirming with a digest no export produced is refused: the
	// confirmation is a proof of possession, not a checkbox.
	err := s.Confirm(ctx, rec.ID, strings.Repeat("0", len(receipt.Digest)), fixedNow.Add(time.Hour))
	if !errors.Is(err, keycustody.ErrNoSuchExport) {
		t.Fatalf("Confirm with a made-up digest err = %v, want ErrNoSuchExport", err)
	}
	if err := s.Confirm(ctx, rec.ID, receipt.Digest, fixedNow.Add(time.Hour)); err != nil {
		t.Fatalf("Confirm with the export's digest: %v", err)
	}

	// Durable: a fresh Store over the same directory sees the confirmation.
	again := openStore(t, root)
	h, err := again.Health(ctx, true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.BundleConfirmed || h.BundleConfirmedAt == nil || !h.BundleConfirmedAt.Equal(fixedNow.Add(time.Hour)) {
		t.Fatalf("after a restart Health = %+v, want the confirmation and its time", h)
	}
}

func TestExportOfAnUnknownKeyIsRefused(t *testing.T) {
	s := openStore(t, t.TempDir())
	var out bytes.Buffer
	if _, err := s.Export(context.Background(), "k0000000000000000", &out, fixedNow); !errors.Is(err, keycustody.ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
	if out.Len() != 0 {
		t.Fatalf("%d bytes were written for a key that does not exist", out.Len())
	}
}

// The bundle file is the ONE surface that carries material, and it is
// written to a writer the caller chose, never to a path this package
// picked. Everything a route or a CLI verb gets back is the Receipt.
func TestTheReceiptCarriesNoMaterialAndTheBundleIsTheOnlyThingThatDoes(t *testing.T) {
	s := openStore(t, t.TempDir())
	_, bundleBytes, receipt, material := exportBundle(t, s)

	if !carriesMaterial(bundleBytes, material) {
		t.Fatalf("the bundle does not carry the key, so it cannot recover anything")
	}
	for _, rendered := range [][]byte{
		[]byte(fmt.Sprintf("%v", receipt)), []byte(fmt.Sprintf("%+v", receipt)), []byte(fmt.Sprintf("%#v", receipt)),
	} {
		if carriesMaterial(rendered, material) {
			t.Fatalf("the receipt renders the key")
		}
	}
	js, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	if carriesMaterial(js, material) {
		t.Fatalf("the receipt's JSON carries the key")
	}
}

// A copy cut short fails closed through the bundle: the Open contract that
// Bundle.Decrypt relies on (final-chunk flag), exercised through the real
// format. The untruncated copy decrypting is the control, in
// TestABundleDecryptsAFixtureCopyOnACleanMachine.
func TestATruncatedCopyFailsClosedThroughTheBundle(t *testing.T) {
	s := openStore(t, t.TempDir())
	rec, bundleBytes, _, _ := exportBundle(t, s)
	b, err := keycustody.ReadBundle(bytes.NewReader(bundleBytes))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	ciphertext := sealTo(t, rec.Recipient, bytes.Repeat([]byte("x"), 3*65536+17))
	var out bytes.Buffer
	err = b.Decrypt(testFormat, bytes.NewReader(ciphertext[:len(ciphertext)-40]), &out)
	if err == nil {
		t.Fatalf("a truncated copy decrypted without error")
	}
}

// A bundle whose material does not derive the recipient it names is refused
// at Decrypt even if its digest was recomputed by whoever edited it.
func TestABundleWithMismatchedMaterialIsRefusedAtDecrypt(t *testing.T) {
	a := openStore(t, t.TempDir())
	_, bundleA, _, _ := exportBundle(t, a)
	b := openStore(t, t.TempDir())
	recB, _, _, materialB := exportBundle(t, b)

	bundle, err := keycustody.ReadBundle(bytes.NewReader(bundleA))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	bundle.Material = string(materialB)
	// The copy is sealed to B, so B's material WOULD open it: only the
	// recipient check stands between a relabelled bundle and a decrypt.
	if err := bundle.Decrypt(testFormat, bytes.NewReader(sealTo(t, recB.Recipient, []byte("sealed to B"))), io.Discard); err == nil {
		t.Fatalf("Decrypt ran with material that is not the key the bundle names")
	}
}

// #990: an id the registry never minted is ErrUnknownKey, and it is not
// ErrNoSuchExport, so a caller can tell "no such key" from "that key has no
// matching export". The unknown key is checked before any export is read.
func TestConfirmingAnUnknownKeyIsErrUnknownKeyNotErrNoSuchExport_990(t *testing.T) {
	s := openStore(t, t.TempDir())
	err := s.Confirm(context.Background(), "knope", "sha256:00", fixedNow)
	if !errors.Is(err, keycustody.ErrUnknownKey) {
		t.Fatalf("Confirm on an unknown key err = %v, want ErrUnknownKey", err)
	}
	if errors.Is(err, keycustody.ErrNoSuchExport) {
		t.Fatalf("Confirm on an unknown key err = %v, must not be ErrNoSuchExport", err)
	}
}
