package placement_test

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/placement"
)

// The suite policy in the engine (EPIC T, T1.4, #1314; FR-85, FR-86). The
// fixture's active key is age/v1. A policy pinning age-pq/v1 has no active
// key of its suite, so the copy is refused before anything is encrypted,
// uploaded or deleted, and the age/v1 key is never used in its place.

func pinSuite(f *encFixture, suite string) {
	bs := f.engine.Sets.(fixedSets).set
	bs.RetainedCopyEncryption.Suite = suite
	f.engine.Sets = fixedSets{set: bs}
}

func TestAnActiveKeyOfAnotherSuiteIsNotASubstitute(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	pinSuite(f, copycrypt.AgeMLKEM768X25519ID)

	f.runCycle()
	f.guard.fail()

	mv := f.onlyMove()
	if placement.Phase(mv.Phase) == placement.Done {
		t.Fatal("a move completed under a policy whose suite has no active key")
	}
	if n := f.medium.uploadCount(); n != 0 {
		t.Fatalf("%d upload(s): the copy was written under the age/v1 key in place of an age-pq/v1 one", n)
	}
	if _, ok := f.encryption(); ok {
		t.Fatal("a placement_encryption record was written")
	}
	for _, want := range []string{"no active key for suite age-pq/v1", "retnd encryption key rotate --suite age-pq/v1"} {
		if !strings.Contains(mv.Error, want) {
			t.Errorf("the move says %q, which does not say %q", mv.Error, want)
		}
	}
}

func TestNoActiveKeyForThePolicySuiteRefusesBeforeAnyDestructiveStep(t *testing.T) {
	f := newEncryptedFixture(t, fixtureOpts{})
	pinSuite(f, copycrypt.AgeMLKEM768X25519ID)

	f.runCycle()
	f.guard.fail()

	if !f.localExists() {
		t.Fatal("the local copy was deleted although no copy was written anywhere")
	}
	if _, ok := f.placement(testMedium); ok {
		t.Fatal("a destination placement was recorded")
	}
	if got := f.staging(); len(got) != 0 {
		t.Fatalf("the staging area holds %v; nothing may be encrypted before the key is known", got)
	}
}

// The control for both tests above, so neither passes by refusing every
// copy: the same fixture with the default policy, and with age/v1 pinned,
// writes the copy under the active age/v1 key.
func TestTheDefaultPolicyAndAMatchingPinWriteUnderTheActiveKey(t *testing.T) {
	for _, suite := range []string{"", "default", copycrypt.AgeX25519ID} {
		t.Run("suite="+suite, func(t *testing.T) {
			f := newEncryptedFixture(t, fixtureOpts{})
			pinSuite(f, suite)
			f.runCycle()
			f.guard.fail()
			enc, ok := f.encryption()
			if !ok {
				t.Fatalf("no copy was written (move error %q)", f.onlyMove().Error)
			}
			if enc.FormatID != copycrypt.AgeX25519ID || enc.KeyID != f.ck.ID {
				t.Fatalf("the copy records %s under %s, want age/v1 under the active key %s", enc.FormatID, enc.KeyID, f.ck.ID)
			}
		})
	}
}
