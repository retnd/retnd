package encryptionsuites_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/state"
)

// Suite C's byte-level half of #1319: rotation from one suite to the other,
// and a test-only deprecated suite degrading health. The engine-level half
// (a retired suite refusing a write before any destructive step, a kill in a
// re-encryption stream) is core/tests/movecrash/suite_cells_1319_test.go.
//
// Written by reading; not run.

// oneSuiteRig is a deployment that has only ever had an age/v1 key, which is
// what every EPIC N deployment is on the day it upgrades.
func oneSuiteRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	j, err := state.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	store, err := keycustody.Open(filepath.Join(root, keycustody.DirName), keycustody.NewJournalRegistry(j), keycustody.AgeFormat{})
	if err != nil {
		t.Fatalf("keycustody.Open: %v", err)
	}
	r := &rig{
		t: t, ctx: ctx, root: root,
		staging: filepath.Join(root, "staging"), bucket: filepath.Join(root, "bucket"),
		journal: j, store: store,
		keys: map[string]keycustody.KeyRecord{ageV1: mintUnder(t, ctx, store, ageV1, mintedAt)},
	}
	if err := os.MkdirAll(r.bucket, 0o700); err != nil {
		t.Fatal(err)
	}
	return r
}

// Rotating across suites leaves one active managed key, under the new suite,
// and the old suite's key superseded (not retired) in the same write; the
// copy written before the rotation recovers through its record's suite and
// key; the copy written after is age-pq/v1; neither key opens the other's
// copy. Red if the rotation leaves two active keys, retires the old one (its
// copies would stop recovering), or if recovery goes through the active key.
func TestRotationAcrossSuitesKeepsEveryEarlierCopyRecoverable_1319(t *testing.T) {
	r := oneSuiteRig(t)
	plain := fixture(21)
	before, _ := r.seal(ageV1, "before.dump", plain)
	old := r.keys[ageV1]

	fresh := rotateTo(t, r.ctx, r.store, agePQV1, mintedAt.Add(time.Hour))
	r.keys[agePQV1] = fresh
	if fresh.SuiteID != agePQV1 || fresh.ID == old.ID {
		t.Fatalf("the rotation minted %+v, want a new age-pq/v1 key", fresh)
	}

	keys, err := r.store.Keys(r.ctx)
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	var active, superseded []keycustody.KeyRecord
	for _, k := range keys {
		switch k.Status {
		case keycustody.StatusActive:
			active = append(active, k)
		case keycustody.StatusSuperseded:
			superseded = append(superseded, k)
		}
	}
	if len(active) != 1 || active[0].ID != fresh.ID {
		t.Fatalf("after a rotation across suites the active keys are %+v, want only %s", active, fresh.ID)
	}
	if len(superseded) != 1 || superseded[0].ID != old.ID {
		t.Fatalf("the superseded keys are %+v, want the age/v1 key %s", superseded, old.ID)
	}
	if _, ok := activeUnder(t, r.ctx, r.store, ageV1); ok {
		t.Fatal("age/v1 still has an active key after the policy rotated away from it")
	}

	after, _ := r.seal(agePQV1, "after.dump", plain)
	for _, c := range []struct {
		name         string
		obj          []byte
		suite, keyID string
		want         error
	}{
		{"the pre-rotation copy through its record", before, ageV1, old.ID, nil},
		{"the post-rotation copy through its record", after, agePQV1, fresh.ID, nil},
		{"the pre-rotation copy through the active key", before, agePQV1, fresh.ID, copycrypt.ErrWrongKey},
		{"the post-rotation copy through the superseded key", after, ageV1, old.ID, copycrypt.ErrWrongKey},
	} {
		t.Run(c.name, func(t *testing.T) {
			path, err := r.recoverTo(filepath.Join(r.root, "out-"+c.keyID+"-"+filepath.Base(c.suite)), "x.dump", c.suite, c.keyID, c.obj, sum(plain))
			if c.want == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if got, rerr := os.ReadFile(path); rerr != nil || sum(got) != sum(plain) {
					t.Fatalf("the recovered file is not the artifact (err=%v)", rerr)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// A test-only deprecated suite degrades health with the retirement version,
// and still writes: a copy sealed under it after the status moved recovers.
// The supported suite beside it stays quiet. The control is the same report
// with the status restored, which says nothing about deprecation. Red if a
// deprecated suite is quiet, stops writing, or if the health entry lacks the
// version a person needs to plan the move.
func TestADeprecatedSuiteDegradesHealthAndStillWrites_1319(t *testing.T) {
	r := newRig(t)

	quiet, err := r.store.Health(r.ctx, true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	for _, s := range quiet.Suites {
		if s.Status == string(copycrypt.StatusDeprecated) || s.RetirementVersion != "" {
			t.Fatalf("control: before anything is deprecated health says %+v", s)
		}
	}

	restore, err := copycrypt.SetStatusForTest(ageV1, copycrypt.StatusDeprecated)
	if err != nil {
		t.Fatalf("SetStatusForTest: %v", err)
	}
	defer restore()

	h, err := r.store.Health(r.ctx, true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	byID := map[string]int{}
	for i, s := range h.Suites {
		byID[s.ID] = i
	}
	i, ok := byID[ageV1]
	if !ok {
		t.Fatalf("health has no entry for the deprecated suite: %+v", h.Suites)
	}
	if s := h.Suites[i]; s.Status != string(copycrypt.StatusDeprecated) || s.Keys != 1 || s.RetirementVersion == "" {
		t.Errorf("the deprecated suite's entry is %+v, want deprecated, one key and a retirement version", s)
	}
	if j, ok := byID[agePQV1]; ok && h.Suites[j].Status == string(copycrypt.StatusDeprecated) {
		t.Errorf("the supported suite reads deprecated too: %+v", h.Suites[j])
	}

	plain := fixture(23)
	obj, _ := r.seal(ageV1, "deprecated.dump", plain)
	if _, err := r.recoverTo(filepath.Join(r.root, "out-deprecated"), "x.dump", ageV1, r.keys[ageV1].ID, obj, sum(plain)); err != nil {
		t.Fatalf("a copy written under a deprecated suite does not recover: %v", err)
	}
}
