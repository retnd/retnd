package keycustody_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
)

// The second suite at the custody seam (EPIC T, T1.3, #1313).

// openStoreWith is openStore with the minting format named, which is what
// service.NewKeyCustody does with copycrypt.Default().
func openStoreWith(t *testing.T, root string, f keycustody.Format) *keycustody.Store {
	t.Helper()
	s, err := keycustody.Open(filepath.Join(root, keycustody.DirName), openJournalRegistry(t, root), f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestGenerateWithNoSuiteMintsTheDefault(t *testing.T) {
	s := openStoreWith(t, t.TempDir(), copycrypt.Default())
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SuiteID != "age-pq/v1" {
		t.Fatalf("a first key minted with no suite named is %q, want the default age-pq/v1", rec.SuiteID)
	}
	// It is a key of that suite, read back through custody's own door.
	if r, err := (copycrypt.AgeMLKEM768X25519{}).Recipient(materialOf(t, s, rec.ID)); err != nil || r != rec.Recipient {
		t.Fatalf("the minted material derives %q, %v; want the recorded recipient", r, err)
	}
}

// An existing age/v1 deployment is untouched by the default moving: a
// rotation with no suite named stays in age/v1 (ADR 0033 D4).
func TestAnExistingAgeV1KeyIsUntouchedByTheNewDefault(t *testing.T) {
	root := t.TempDir()
	old := openStoreWith(t, root, copycrypt.AgeX25519{})
	first, err := old.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	// The upgrade: the same deployment opened by a build whose default is
	// age-pq/v1.
	s := openStoreWith(t, root, copycrypt.Default())
	active, ok, err := s.Active(context.Background())
	if err != nil || !ok || active.ID != first.ID || active.SuiteID != "age/v1" {
		t.Fatalf("after the upgrade the active key is %+v, %v, %v; want %s under age/v1", active, ok, err, first.ID)
	}
	rotated, err := s.Rotate(context.Background(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.SuiteID != "age/v1" {
		t.Fatalf("a rotation with no suite named moved the deployment to %q; it stays on age/v1", rotated.SuiteID)
	}
}

// Rotating into another suite is one write: the new suite's key is active
// and the old suite's key superseded, never two active and never none.
func TestRotatingAcrossSuitesIsOneWrite(t *testing.T) {
	root := t.TempDir()
	s := openStoreWith(t, root, copycrypt.AgeX25519{})
	first, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.RotateTo(context.Background(), fixedNow, "age-pq/v1")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := s.Keys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, k := range keys {
		switch {
		case k.Status == keycustody.StatusActive:
			active++
			if k.ID != next.ID || k.SuiteID != "age-pq/v1" {
				t.Errorf("the active key is %+v, want %s under age-pq/v1", k, next.ID)
			}
		case k.ID == first.ID && k.Status != keycustody.StatusSuperseded:
			t.Errorf("the age/v1 key is %s, want superseded", k.Status)
		}
	}
	if active != 1 {
		t.Fatalf("%d active keys after a rotation across suites, want exactly one", active)
	}
	// The superseded age/v1 key still opens its copies through custody.
	if _, err := (copycrypt.AgeX25519{}).Recipient(materialOf(t, s, first.ID)); err != nil {
		t.Fatalf("the superseded age/v1 key no longer reads as one: %v", err)
	}
}

func TestAnAgeV1BundleDoesNotOpenAnAgePQCopy(t *testing.T) {
	root := t.TempDir()
	s := openStoreWith(t, root, copycrypt.AgeX25519{})
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := s.Export(context.Background(), rec.ID, &buf, fixedNow); err != nil {
		t.Fatal(err)
	}
	b, err := keycustody.ReadBundle(&buf)
	if err != nil {
		t.Fatal(err)
	}
	pq := copycrypt.AgeMLKEM768X25519{}
	_, pqRecipient, err := pq.Generate()
	if err != nil {
		t.Fatal(err)
	}
	var sealed bytes.Buffer
	w, err := pq.Seal(&sealed, pqRecipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("an age-pq/v1 copy")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := b.Decrypt(copycrypt.AgeX25519{}, bytes.NewReader(sealed.Bytes()), &out); err == nil {
		t.Fatal("an age/v1 bundle opened an age-pq/v1 copy")
	}
	if err := b.Decrypt(pq, bytes.NewReader(sealed.Bytes()), &out); err == nil {
		t.Fatal("an age/v1 bundle was accepted as an age-pq/v1 key")
	}
	if out.Len() != 0 {
		t.Fatalf("%d plaintext bytes were written by a refused decrypt", out.Len())
	}
}
