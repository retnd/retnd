package keycustody_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/secretref"
)

// The ONE new alert.Kind, FR-66's refusing half: a `required` policy whose
// key cannot be resolved. It is a condition rather than health because it
// resolves by itself the moment the key resolves again, which is the
// behaviour alert.Dispatcher's model is built for.

type recordingSink struct {
	mu        sync.Mutex
	delivered []alert.Alert
}

func (s *recordingSink) Deliver(_ context.Context, a alert.Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered = append(s.delivered, a)
	return nil
}

func (s *recordingSink) count(kind alert.Kind) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for _, a := range s.delivered {
		if a.Kind == kind {
			n++
		}
	}
	return n
}

func TestTheKindIsRegisteredWithATitle(t *testing.T) {
	var found bool
	for _, k := range alert.Kinds {
		if k == alert.EncryptionKeyUnresolvable {
			found = true
		}
	}
	if !found {
		t.Fatalf("alert.Kinds does not carry EncryptionKeyUnresolvable")
	}
	if string(alert.EncryptionKeyUnresolvable) != "ENCRYPTION_KEY_UNRESOLVABLE" {
		t.Fatalf("Kind = %q, want ENCRYPTION_KEY_UNRESOLVABLE (a wire contract)", alert.EncryptionKeyUnresolvable)
	}
	if title := alert.EncryptionKeyUnresolvable.Title(); title == "" || title == "retnd alert" {
		t.Fatalf("Title = %q, want a headline of its own", title)
	}
}

// Fires while a required key is unresolvable, resolves when it resolves,
// and re-alerts on a genuine recurrence.
func TestARequiredExternalKeyThatCannotResolveFiresResolvesAndRecurs(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, t.TempDir())
	ref, material := externalKeyFile(t)
	policies := []keycustody.SetPolicy{
		{Set: "nas-a/postgres", Required: true, Provider: keycustody.Provider{External: ref}},
		{Set: "nas-a/photos", Required: false, Provider: keycustody.Provider{External: secretref.Ref{File: "/nowhere"}}},
		{Set: "nas-a/plaintext"},
	}
	sink := &recordingSink{}
	d := alert.NewDispatcher(sink, nil)
	now := fixedNow

	observe := func() {
		conds, unevaluated := s.AlertConditions(ctx, policies)
		d.Observe(ctx, conds, unevaluated, now)
		now = now.Add(time.Minute)
	}

	// Resolvable: nothing fires, including for the optional set whose
	// reference is broken, because `off` and `inherit`-to-off refuse nothing.
	observe()
	if got := sink.count(alert.EncryptionKeyUnresolvable); got != 0 {
		t.Fatalf("delivered %d while every required key resolves", got)
	}

	// Break it: fires once, and stays quiet on the next pass.
	if err := os.Chmod(ref.File, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	observe()
	observe()
	if got := sink.count(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d across two passes of one unresolved condition, want exactly 1", got)
	}
	a := sink.delivered[0]
	if a.Scope != "nas-a/postgres" {
		t.Fatalf("Scope = %q, want the set whose policy refuses", a.Scope)
	}
	if !strings.Contains(a.Message, ref.String()) || !strings.Contains(a.Message, "refus") {
		t.Fatalf("Message = %q, want the reference description and the word that says copies are being refused", a.Message)
	}
	if carriesMaterial([]byte(a.Message+a.Title), material) {
		t.Fatalf("the alert carries the key")
	}

	// Fix it: resolves silently.
	if err := os.Chmod(ref.File, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	observe()
	if got := sink.count(alert.EncryptionKeyUnresolvable); got != 1 {
		t.Fatalf("delivered %d after the key resolved again, want still 1", got)
	}

	// Break it again: a genuine recurrence alerts again.
	if err := os.Rename(ref.File, ref.File+".moved"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	observe()
	if got := sink.count(alert.EncryptionKeyUnresolvable); got != 2 {
		t.Fatalf("delivered %d after a recurrence, want 2", got)
	}
}

func TestARequiredManagedKeyThatCannotResolveFiresToo(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := openStore(t, root)
	rec, err := s.Create(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	policies := []keycustody.SetPolicy{{Set: "nas-b/db", Required: true, Provider: keycustody.Provider{Managed: rec.ID}}}

	conds, _ := s.AlertConditions(ctx, policies)
	if len(conds) != 0 {
		t.Fatalf("conditions = %+v while the managed key resolves, want none", conds)
	}

	path := filepath.Join(root, keycustody.DirName, rec.ID+".key")
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	conds, _ = s.AlertConditions(ctx, policies)
	if len(conds) != 1 || conds[0].Kind != alert.EncryptionKeyUnresolvable || conds[0].Scope != "nas-b/db" {
		t.Fatalf("conditions = %+v, want one EncryptionKeyUnresolvable for nas-b/db", conds)
	}
	if !strings.Contains(conds[0].Detail, rec.ID) || !strings.Contains(conds[0].Detail, path) {
		t.Fatalf("Detail = %q, want the key id and the path to fix", conds[0].Detail)
	}
	material := materialOf(t, openStoreAfterRepair(t, root, path), rec.ID)
	if carriesMaterial([]byte(conds[0].Detail), material) {
		t.Fatalf("Detail carries the key")
	}
}

// A required set that names NO provider at all is an unresolvable key too:
// Validate (#989) refuses that configuration at Load, so reaching this is
// a hole somewhere, and the honest answer is the alert, not silence.
func TestARequiredSetWithNoProviderIsUnresolvable(t *testing.T) {
	s := openStore(t, t.TempDir())
	conds, _ := s.AlertConditions(context.Background(), []keycustody.SetPolicy{{Set: "nas-c/x", Required: true}})
	if len(conds) != 1 || conds[0].Kind != alert.EncryptionKeyUnresolvable {
		t.Fatalf("conditions = %+v, want one EncryptionKeyUnresolvable", conds)
	}
	if !strings.Contains(conds[0].Detail, "no key provider") {
		t.Fatalf("Detail = %q, want it to say no provider is declared", conds[0].Detail)
	}
}

func openStoreAfterRepair(t *testing.T, root, keyPath string) *keycustody.Store {
	t.Helper()
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return openStore(t, root)
}
