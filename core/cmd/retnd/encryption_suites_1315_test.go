package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
)

// EPIC T, T1.5 (#1315): every suite fact has a verb, and the verbs say
// what the routes say. Written by reading the verbs and the service; not run.

func TestEncryptionSuitesListsTheRegistryWithCounts(t *testing.T) {
	clearTheRoute(t)
	cfg := writeTestConfig(t)
	code, out := runCapturing(t, "encryption", "suites", "--json", "--config", cfg)
	if code != exitOK {
		t.Fatalf("encryption suites exited %d: %s", code, out)
	}
	var got apicontract.ListEncryptionSuitesResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not the route's body: %v\n%s", err, out)
	}
	if len(got.Suites) != len(copycrypt.Suites()) {
		t.Fatalf("%d suites listed, the registry has %d", len(got.Suites), len(copycrypt.Suites()))
	}
	defaults := 0
	for i, s := range got.Suites {
		want := copycrypt.Suites()[i]
		if s.ID != want.ID() || s.Version != want.Version() || s.Status != string(want.Status()) || s.Describe != string(want.Describe()) {
			t.Errorf("row %d = %+v, want the registry's %s", i, s, want.ID())
		}
		if s.ActiveCopies != 0 || s.Keys != 0 {
			t.Errorf("%s counts %d copies and %d keys on a deployment that encrypts nothing", s.ID, s.ActiveCopies, s.Keys)
		}
		if s.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("%d suites say they are the default", defaults)
	}
	// A deployment that encrypts nothing gets its answer without a key
	// directory being created.
	if _, err := os.Stat(keycustody.DirBeside(cfg)); !os.IsNotExist(err) {
		t.Errorf("listing the suites created or touched %s (%v)", keycustody.DirBeside(cfg), err)
	}
}

func TestPolicyPatchToASuiteWithoutAKeySaysRotate(t *testing.T) {
	clearTheRoute(t)
	cfg := writeTestConfig(t)
	if code, out := runCapturing(t, "encryption", "key", "create", "--suite", "age-pq/v1", "--config", cfg); code != exitOK {
		t.Fatalf("key create exited %d: %s", code, out)
	}
	code, out := runCapturing(t, "encryption", "policy", "patch", "--suite", "age/v1", "--json", "--config", cfg)
	if code != exitOK {
		t.Fatalf("policy patch exited %d: %s", code, out)
	}
	var got apicontract.EncryptionPolicy
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.SuiteConfigured != "age/v1" || got.Suite != "age/v1" {
		t.Errorf("policy reads suite %q configured %q, want age/v1 both", got.Suite, got.SuiteConfigured)
	}
	if !got.RotationNeeded || !strings.HasSuffix(got.RotateCommand, "encryption key rotate --suite age/v1") {
		t.Errorf("rotation_needed=%v rotate_command=%q; a suite with no active key has to say how to get one", got.RotationNeeded, got.RotateCommand)
	}
	// It rotated nothing.
	_, listed := runCapturing(t, "encryption", "key", "list", "--json", "--config", cfg)
	var keys apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(listed), &keys); err != nil || len(keys.Keys) != 1 || keys.Keys[0].Suite != "age-pq/v1" {
		t.Fatalf("keys after the patch = %+v (%v); the patch writes nothing to the key store", keys, err)
	}
}

func TestKeyRotateAcrossSuitesIsOneWrite(t *testing.T) {
	clearTheRoute(t)
	cfg := writeTestConfig(t)
	if code, out := runCapturing(t, "encryption", "key", "create", "--suite", "age/v1", "--config", cfg); code != exitOK {
		t.Fatalf("key create exited %d: %s", code, out)
	}
	code, out := runCapturing(t, "encryption", "key", "rotate", "--suite", "age-pq/v1", "--json", "--config", cfg)
	if code != exitOK {
		t.Fatalf("key rotate exited %d: %s", code, out)
	}
	var rotated apicontract.EncryptionKeyStatus
	if err := json.Unmarshal([]byte(out), &rotated); err != nil || rotated.Suite != "age-pq/v1" || rotated.Status != "active" {
		t.Fatalf("the rotation answered %+v (%v)", rotated, err)
	}
	_, listed := runCapturing(t, "encryption", "key", "list", "--json", "--config", cfg)
	var keys apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(listed), &keys); err != nil {
		t.Fatal(err)
	}
	active, superseded := 0, 0
	for _, k := range keys.Keys {
		switch {
		case k.Status == "active" && k.Suite == "age-pq/v1":
			active++
		case k.Status == "superseded" && k.Suite == "age/v1":
			superseded++
		default:
			t.Errorf("unexpected key %+v", k)
		}
	}
	if active != 1 || superseded != 1 {
		t.Fatalf("after the rotation: %d active age-pq/v1, %d superseded age/v1; want one of each", active, superseded)
	}
}

func TestKeyCreateRefusesAnUnknownSuite(t *testing.T) {
	clearTheRoute(t)
	cfg := writeTestConfig(t)
	// The refusal goes to stderr, so both streams are read.
	code, out := runCapturingBothStreams(t, []string{"encryption", "key", "create", "--suite", "aes-256-military-grade", "--config", cfg})
	if code == exitOK || !strings.Contains(out, "aes-256-military-grade") {
		t.Fatalf("an unknown suite = exit %d: %s; want a refusal naming it", code, out)
	}
}

func TestDecryptTakesTheSuiteFromTheRecordAndRefusesToGuess(t *testing.T) {
	f := newDecryptFixture(t)
	// --suite is a confirmation: one that disagrees with the key and the
	// record is refused before anything is written.
	wrong := copycrypt.AgeX25519ID
	if f.suiteID() == wrong {
		wrong = copycrypt.AgeMLKEM768X25519ID
	}
	code, stdout, stderr := runAll(t, f.decryptArgs("--suite", wrong)...)
	if code == exitOK {
		t.Fatalf("decrypt with --suite %s accepted a %s copy", wrong, f.suiteID())
	}
	if !strings.Contains(stdout+stderr, wrong) {
		t.Errorf("the refusal does not name the suite it disagreed with:\n%s%s", stdout, stderr)
	}
	if names := dirNames(t, f.outDir); len(names) != 0 {
		t.Errorf("a refused decrypt left %v behind", names)
	}
	// The matching confirmation is accepted.
	if code, stdout, stderr := runAll(t, f.decryptArgs("--suite", f.suiteID())...); code != exitOK {
		t.Fatalf("decrypt with the right --suite exited %d\n%s%s", code, stdout, stderr)
	}
}
