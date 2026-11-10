package keycustody_test

import (
	"context"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/secretref"
)

// #1304: an external key row stores no reference, so a copy under it can be
// read only while some declared reference still yields the key. These tests
// are written by reading and have not been run.

func TestUnreadableExternalKeysNamesAKeyWhoseReferenceWasDropped_1304(t *testing.T) {
	ctx := context.Background()
	refA, materialA := externalKeyFile(t)
	refB, _ := externalKeyFile(t)
	declared := []secretref.Ref{refA}
	s := openStore(t, t.TempDir())
	s.UseExternalRefs(func() []secretref.Ref { return declared })

	a, err := s.RegisterExternal(ctx, refA, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.UnreadableExternalKeys(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("with its reference declared: %v, %v, want nothing unreadable", got, err)
	}

	// The reference is dropped (a superseding key's reference replaces it).
	declared = []secretref.Ref{refB}
	got, err = s.UnreadableExternalKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reason, ok := got[a.ID]
	if !ok || len(got) != 1 {
		t.Fatalf("after dropping the reference: %v, want exactly %s", got, a.ID)
	}
	if _, listed := got[managed.ID]; listed {
		t.Errorf("a managed key was listed as an unreadable external key")
	}

	// The reason names the references tried and never the material. The
	// control proves the scan can fail: it must see a planted canary.
	canary := string(materialA)
	if !strings.Contains("x"+canary+"x", canary) {
		t.Fatal("control: the scan did not see a planted canary")
	}
	if strings.Contains(reason, canary) || strings.Contains(reason, strings.TrimSpace(canary)) {
		t.Errorf("the reason carries key material: %q", reason)
	}

	// The reference comes back: readable again, so nothing is listed.
	declared = []secretref.Ref{refB, refA}
	if got, _ := s.UnreadableExternalKeys(ctx); len(got) != 0 {
		t.Errorf("with the reference declared again: %v, want nothing", got)
	}
}
