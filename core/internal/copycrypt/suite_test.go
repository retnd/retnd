package copycrypt_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
)

// The suite registry (EPIC T, T1.2, #1312; ADR 0033 D5). The table is a
// compile-time constant of this package, and these tests are what make a
// change to it a deliberate edit rather than a side effect.

// goldenSuites is the table as it must read: id, version, status, and
// whether it is the default. Changing a row here is the point of a pull
// request that changes a suite, never a by-product of one.
var goldenSuites = []string{
	"age/v1 1 supported",
	"age-pq/v1 1 supported default",
}

func TestSuiteIDsAreStableAgainstTheGoldenList(t *testing.T) {
	t.Parallel()
	var got []string
	for _, s := range copycrypt.Suites() {
		line := fmt.Sprintf("%s %d %s", s.ID(), s.Version(), s.Status())
		if s.ID() == copycrypt.Default().ID() {
			line += " default"
		}
		got = append(got, line)
	}
	if strings.Join(got, "\n") != strings.Join(goldenSuites, "\n") {
		t.Fatalf("the suite table reads\n  %s\nand the golden list says\n  %s\nA suite id names bytes already written; change the golden list in the same commit as the table, on purpose",
			strings.Join(got, "\n  "), strings.Join(goldenSuites, "\n  "))
	}
}

func TestTheRegistryHasExactlyOneDefaultAndItIsSupported(t *testing.T) {
	t.Parallel()
	def := copycrypt.Default()
	if def == nil {
		t.Fatal("Default() returned nil")
	}
	if def.Status() != copycrypt.StatusSupported {
		t.Errorf("the default suite %s is %s; a new key is minted under the default, so it has to be supported", def.ID(), def.Status())
	}
	seen := map[string]bool{}
	defaults := 0
	for _, s := range copycrypt.Suites() {
		if seen[s.ID()] {
			t.Errorf("two suites share the id %q", s.ID())
		}
		seen[s.ID()] = true
		if s.ID() == def.ID() {
			defaults++
		}
		if s.Version() < 1 {
			t.Errorf("suite %s has version %d; format_version is at least 1", s.ID(), s.Version())
		}
		switch s.Status() {
		case copycrypt.StatusSupported, copycrypt.StatusDeprecated, copycrypt.StatusRetired:
		default:
			t.Errorf("suite %s has status %q, which is not in the vocabulary", s.ID(), s.Status())
		}
		if strings.TrimSpace(string(s.Describe())) == "" {
			t.Errorf("suite %s has no Describe() sentence; that sentence is the only thing a surface may print about it", s.ID())
		}
	}
	if defaults != 1 {
		t.Errorf("the default %s appears %d times in the table, want exactly once", def.ID(), defaults)
	}
}

func TestLookupOfAnUnknownIdFails(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "age", "AGE/V1", "age/v2", "age-pq", "default", "tink-aes256gcm-hkdf-1mb/v1"} {
		if s, ok := copycrypt.Lookup(id); ok {
			t.Errorf("Lookup(%q) = %s, want no suite", id, s.ID())
		}
	}
	if s, ok := copycrypt.Lookup("age/v1"); !ok || s.ID() != "age/v1" {
		t.Errorf("Lookup(age/v1) = %v, %v; the control has to find the suite", s, ok)
	}
}

// Staged carries the id and version of the suite that produced the bytes,
// which is what placement_encryption records (FR-83).
func TestStagedNamesTheSuiteThatProducedIt(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)
	plain := fixture(31, chunk+17)
	suite := copycrypt.AgeX25519{}
	s, err := copycrypt.EncryptToStaging(context.Background(), t.TempDir(), "a.bin", bytes.NewReader(plain), suite, recipient, sum(plain))
	if err != nil {
		t.Fatal(err)
	}
	if s.SuiteID != suite.ID() || s.Version != suite.Version() {
		t.Errorf("Staged names %s/%d, want the suite that sealed it, %s/%d", s.SuiteID, s.Version, suite.ID(), suite.Version())
	}
	if _, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(read(t, s.Path)), suite, identity, sum(plain)); err != nil {
		t.Errorf("the staged object does not decrypt-verify under its own suite: %v", err)
	}
}

func TestNoSuiteIsRefusedBeforeAnythingIsRead(t *testing.T) {
	t.Parallel()
	identity, recipient := newKey(t)
	plain := fixture(32, 100)
	dir := t.TempDir()
	if _, err := copycrypt.EncryptToStaging(context.Background(), dir, "a.bin", bytes.NewReader(plain), nil, recipient, sum(plain)); !errors.Is(err, copycrypt.ErrUnknownSuite) {
		t.Errorf("EncryptToStaging with no suite = %v, want ErrUnknownSuite", err)
	}
	if got := names(t, dir); len(got) != 0 {
		t.Errorf("a refused encryption left %v behind", got)
	}
	if _, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(nil), nil, identity, sum(plain)); !errors.Is(err, copycrypt.ErrUnknownSuite) {
		t.Errorf("DecryptVerify with no suite = %v, want ErrUnknownSuite", err)
	}
}

// TestTheDefaultIsAgePQ pins ADR 0033 D2: a deployment with no active key
// mints its first key under age-pq/v1. age/v1 stays supported.
func TestTheDefaultIsAgePQ(t *testing.T) {
	t.Parallel()
	if got := copycrypt.Default().ID(); got != "age-pq/v1" {
		t.Fatalf("Default() = %s, want age-pq/v1", got)
	}
	v1, ok := copycrypt.Lookup("age/v1")
	if !ok || v1.Status() != copycrypt.StatusSupported {
		t.Fatalf("age/v1 = %v, %v; it stays supported when the default moves", v1, ok)
	}
}

// describeSentences is every suite's one sentence, pinned: a surface may
// print this and nothing else about a suite (FR-84), so a change to it is a
// change to what the product claims.
var describeSentences = map[string]string{
	"age/v1": "age v1 file, one X25519 recipient, ChaCha20-Poly1305 in 64 KiB authenticated chunks. " +
		"128-bit security level. Not post-quantum. Opens with `age -d -i key.txt`.",
	"age-pq/v1": "age v1 file, one ML-KEM-768 + X25519 hybrid recipient, ChaCha20-Poly1305 in 64 KiB authenticated chunks. " +
		"128-bit security level, post-quantum key establishment. Not AES, not FIPS-validated. " +
		"Opens with `age -d -i key.txt` (age 1.3 or newer).",
}

func TestDescribeSentencesCarryNoMarketingWords(t *testing.T) {
	t.Parallel()
	for _, s := range copycrypt.Suites() {
		got := string(s.Describe())
		if want, ok := describeSentences[s.ID()]; !ok || got != want {
			t.Errorf("%s says %q; the pinned sentence is %q", s.ID(), got, want)
		}
		low := strings.ToLower(got)
		for _, banned := range []string{"military", "256-bit", "256 bit", "unbreakable", "bank-grade"} {
			if strings.Contains(low, banned) {
				t.Errorf("%s's sentence carries %q, which the code cannot state (ADR 0033 D2)", s.ID(), banned)
			}
		}
	}
	if len(describeSentences) != len(copycrypt.Suites()) {
		t.Errorf("%d pinned sentence(s) for %d suite(s); every suite needs its sentence pinned", len(describeSentences), len(copycrypt.Suites()))
	}
}

// An age/v1 identity cannot open an age-pq/v1 copy, and the reverse.
func TestTheTwoAgeSuitesDoNotOpenEachOther(t *testing.T) {
	t.Parallel()
	pq, v1 := copycrypt.AgeMLKEM768X25519{}, copycrypt.AgeX25519{}
	plain := fixture(51, chunk+3)
	for _, pair := range []struct{ write, read copycrypt.Suite }{{pq, v1}, {v1, pq}} {
		_, recipient, err := pair.write.Generate()
		if err != nil {
			t.Fatal(err)
		}
		readSecret, _, err := pair.read.Generate()
		if err != nil {
			t.Fatal(err)
		}
		s, err := copycrypt.EncryptToStaging(context.Background(), t.TempDir(), "a.bin", bytes.NewReader(plain), pair.write, recipient, sum(plain))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := copycrypt.DecryptVerify(context.Background(), bytes.NewReader(read(t, s.Path)), pair.read, readSecret, sum(plain)); !errors.Is(err, copycrypt.ErrKeyUnavailable) && !errors.Is(err, copycrypt.ErrWrongKey) {
			t.Errorf("a %s key on a %s copy = %v, want a refusal", pair.read.ID(), pair.write.ID(), err)
		}
	}
}
