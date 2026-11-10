package keycustody_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
)

// Keys belong to suites (EPIC T, T1.2, #1312; ADR 0033 D3 and D8): a key
// is recorded with its suite, a suite this build cannot read is refused for
// a key, and a bundle's format is the key's suite.

func TestAddKeyRefusesAnUnregisteredSuite(t *testing.T) {
	root := t.TempDir()
	reg := openJournalRegistry(t, root)
	rec := keycustody.KeyRecord{ID: "k00000000000000aa", Recipient: "age1notchecked", Created: fixedNow, Status: keycustody.StatusActive, SuiteID: "nonsense/v1"}
	if err := reg.AddKey(context.Background(), rec, ""); !errors.Is(err, keycustody.ErrUnknownSuite) {
		t.Fatalf("AddKey under an unregistered suite = %v, want ErrUnknownSuite", err)
	}
	keys, err := reg.Keys(context.Background())
	if err != nil || len(keys) != 0 {
		t.Fatalf("the refused key is in the registry: %+v, %v", keys, err)
	}
}

func TestAMintedKeyRecordsTheStoresSuite(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SuiteID != testFormat.ID() {
		t.Errorf("Create returned suite %q, want the store's %q", rec.SuiteID, testFormat.ID())
	}
	// Read back through a fresh registry: the suite is durable, not a field
	// of the value Create happened to return.
	again, err := keycustody.Open(filepath.Join(root, keycustody.DirName), openJournalRegistry(t, root), testFormat)
	if err != nil {
		t.Fatal(err)
	}
	active, ok, err := again.ActiveFor(context.Background(), testFormat.ID())
	if err != nil || !ok || active.ID != rec.ID || active.SuiteID != testFormat.ID() {
		t.Errorf("ActiveFor(%s) after a restart = %+v, %v, %v; want key %s", testFormat.ID(), active, ok, err, rec.ID)
	}
	if _, ok, err := again.ActiveFor(context.Background(), "age-pq/v1"); err != nil || ok {
		t.Errorf("ActiveFor(age-pq/v1) = %v, %v; an active key of another suite is never the answer", ok, err)
	}
}

func TestRotateIntoAnUnknownSuiteWritesNothing(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	first, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateTo(context.Background(), fixedNow, "nonsense/v1"); !errors.Is(err, keycustody.ErrUnknownSuite) {
		t.Fatalf("RotateTo(nonsense/v1) = %v, want ErrUnknownSuite", err)
	}
	if _, err := s.CreateIn(context.Background(), fixedNow, "nonsense/v1"); !errors.Is(err, keycustody.ErrUnknownSuite) {
		t.Fatalf("CreateIn(nonsense/v1) = %v, want ErrUnknownSuite", err)
	}
	keys, err := s.Keys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].ID != first.ID || keys[0].Status != keycustody.StatusActive {
		t.Fatalf("keys after the refusals = %+v, %v; want only the first key, still active", keys, err)
	}
}

func TestABundleCarriesTheKeysSuite(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
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
	if b.FormatID != rec.SuiteID || b.FormatID != copycrypt.AgeX25519ID {
		t.Errorf("the bundle's format is %q, want the key's suite %q", b.FormatID, rec.SuiteID)
	}
}

// Every suite in the registry is a custody Format, which is what lets the
// store hand a key of any suite to WithKey and Recipient.
func TestEverySuiteIsAFormat(t *testing.T) {
	for _, s := range copycrypt.Suites() {
		var f keycustody.Format = s
		if f.ID() != s.ID() {
			t.Errorf("suite %s reads as format %s", s.ID(), f.ID())
		}
	}
}
