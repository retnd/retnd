package keycustody_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/secretref"
)

// EPIC T, T1.5 (#1315): the suite custody remembers for an external
// reference belongs to that exact reference, and only while it resolves.
// Written by reading; not run.

// keyFileOf writes material to a 0600 file in a 0700 directory.
func keyFileOf(t *testing.T, material []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "k.key")
	if err := os.WriteFile(path, append(material, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Two command references running one executable with different arguments
// describe themselves the same way (a description leaves the arguments
// out), and must still be remembered apart.
func TestTwoCommandsOfOneExecutableAreRememberedApart(t *testing.T) {
	ctx := context.Background()
	v1, _, err := copycrypt.AgeX25519{}.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pq, _, err := copycrypt.AgeMLKEM768X25519{}.Generate()
	if err != nil {
		t.Fatal(err)
	}
	refA := secretref.Ref{Command: []string{"/bin/cat", keyFileOf(t, v1)}}
	refB := secretref.Ref{Command: []string{"/bin/cat", keyFileOf(t, pq)}}
	if refA.String() != refB.String() {
		t.Fatalf("the two references describe themselves as %q and %q; this test is about the case where they agree", refA.String(), refB.String())
	}
	s, _ := externalStore(t, refA, refB)

	if got, err := s.ExternalKeySuite(ctx, refA); err != nil || got != copycrypt.AgeX25519ID {
		t.Fatalf("resolving A = %q, %v", got, err)
	}
	if got, err := s.ExternalKeySuite(ctx, refB); err != nil || got != copycrypt.AgeMLKEM768X25519ID {
		t.Fatalf("resolving B = %q, %v", got, err)
	}
	if got, ok := s.LastExternalKeySuite(refA); !ok || got != copycrypt.AgeX25519ID {
		t.Errorf("A is remembered as %q, %v after B was resolved; one entry is shared between two references", got, ok)
	}
	if got, ok := s.LastExternalKeySuite(refB); !ok || got != copycrypt.AgeMLKEM768X25519ID {
		t.Errorf("B is remembered as %q, %v", got, ok)
	}
}

// A reference that resolved and then stops resolving has no suite to
// report, through either way custody resolves it.
func TestAFailedResolutionForgetsTheSuite(t *testing.T) {
	ctx := context.Background()
	for name, resolve := range map[string]func(s *keycustody.Store, ref secretref.Ref){
		"explicit":    func(s *keycustody.Store, ref secretref.Ref) { _, _ = s.ExternalKeySuite(ctx, ref) },
		"alert probe": func(s *keycustody.Store, ref secretref.Ref) { s.Resolvability(ctx, keycustody.Provider{External: ref}) },
	} {
		t.Run(name, func(t *testing.T) {
			ref, _ := externalKeyFile(t)
			s, _ := externalStore(t, ref)
			resolve(s, ref)
			if got, ok := s.LastExternalKeySuite(ref); !ok || got != copycrypt.AgeX25519ID {
				t.Fatalf("after a good resolution the suite is %q, %v", got, ok)
			}
			if err := os.Remove(ref.File); err != nil {
				t.Fatal(err)
			}
			resolve(s, ref)
			if got, ok := s.LastExternalKeySuite(ref); ok {
				t.Errorf("after a failed resolution the suite still reads %q", got)
			}
		})
	}
}
