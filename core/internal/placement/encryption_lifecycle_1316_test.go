package placement_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC T, T2.1 (#1316, FR-87): what the move engine does with a suite's
// status. A retired suite writes nothing, refused at the required refusal's
// position, before any destructive step; and every copy it ever wrote still
// decrypt-verifies, decrypts and re-homes, because a read opens a copy with
// the suite its record names and never asks the status.
//
// The suites are the registry's test hook over the real default adapter, so
// the bytes are real age and the production table is never edited.
//
// Written by reading; not run (the suites run on the release PR).

// underSuite points the fixture's set at suite s and mints the managed key
// new copies of s are written under. It is the one place that names the
// key-per-suite API, so a change to that API is a change here only.
func underSuite(t *testing.T, f *fixture, s copycrypt.Suite) (*keycustody.Store, keycustody.KeyRecord) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keys, err := keycustody.Open(dir, keycustody.NewJournalRegistry(f.journal), keycustody.AgeFormat{})
	if err != nil {
		t.Fatalf("keycustody.Open: %v", err)
	}
	rec, err := keys.CreateIn(f.ctx, testNow2.Add(-time.Hour), s.ID())
	if err != nil {
		t.Fatalf("Create under %s: %v", s.ID(), err)
	}
	f.engine.Sets = fixedSets{set: config.BackupSet{
		Name: testSet, ID: f.artifact.Set, LocalPath: f.localDir,
		RetainedCopyEncryption: config.RetainedCopyEncryption{
			Mode:        config.RetainedCopyEncryptionRequired,
			Suite:       s.ID(),
			KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
		},
	}}
	f.engine.Keys = keys
	return keys, rec
}

func lifecycleSuite(t *testing.T, status copycrypt.Status, retiredIn string) *copycrypt.TestSuite {
	t.Helper()
	return copycrypt.InjectForTest(t, copycrypt.Default(), copycrypt.TestSuitePrefix+"lifecycle/v1", status, retiredIn)
}

// The refusal is crash-matrix shaped: the cycle runs, the process "dies" and
// a fresh cycle runs over the same journal, and after each the world is the
// one before the move: the local copy whole, nothing uploaded, no placement,
// no record, nothing staged, and the move row saying why. The anti-vacuity
// control is the same world with the suite deprecated, which completes, so
// the refusal is the status and nothing else.
//
// Red if the engine encrypts under a retired suite, if it checks the status
// after staging or uploading, or if it substitutes another suite.
func TestAWriteUnderARetiredSuiteIsRefusedBeforeAnyDestructiveStep(t *testing.T) {
	// The key is minted while the suite still writes (custody refuses to
	// mint under a retired one), then the suite is retired: the state a
	// deployment is in on the first cycle under a build that retired it.
	ts := lifecycleSuite(t, copycrypt.StatusSupported, "")
	f := newFixture(t, fixtureOpts{})
	underSuite(t, f, ts)
	ts.SetStatus(copycrypt.StatusRetired, "0.9.0")

	for attempt := 1; attempt <= 2; attempt++ {
		f.runCycle()
		f.guard.fail()

		mv := f.onlyMove()
		if placement.Phase(mv.Phase) == placement.Done {
			t.Fatalf("attempt %d: a move completed under a retired suite", attempt)
		}
		if n := f.medium.uploadCount(); n != 0 {
			t.Fatalf("attempt %d: %d upload(s) under a retired suite", attempt, n)
		}
		if p, ok := f.placement(testMedium); ok {
			t.Fatalf("attempt %d: a destination placement was recorded (class %q, status %s)", attempt, p.VerificationClass, p.Status)
		}
		if _, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, testMedium); err != nil || ok {
			t.Fatalf("attempt %d: a placement_encryption record exists (err %v)", attempt, err)
		}
		if !f.localExists() {
			t.Fatalf("attempt %d: the local source is gone", attempt)
		}
		if es, err := os.ReadDir(filepath.Join(f.localDir, placement.StagingDirName)); err == nil && len(es) != 0 {
			t.Fatalf("attempt %d: %d file(s) staged under a retired suite", attempt, len(es))
		}
		for _, want := range []string{ts.ID(), "retired", "0.9.0"} {
			if !strings.Contains(mv.Error, want) {
				t.Errorf("attempt %d: the move row %q does not say %q", attempt, mv.Error, want)
			}
		}
	}

	// Control: the same suite deprecated still writes.
	ts.SetStatus(copycrypt.StatusDeprecated, "0.9.0")
	g := newFixture(t, fixtureOpts{})
	underSuite(t, g, ts)
	if report := g.runCycle(); report.Completed != 1 {
		t.Fatalf("control: a deprecated suite was refused a write: %+v (move error %q)", report, g.onlyMove().Error)
	}
	enc, ok, err := g.journal.PlacementEncryption(g.ctx, g.artifact, testMedium)
	if err != nil || !ok || enc.FormatID != ts.ID() {
		t.Fatalf("control: the copy under a deprecated suite records %+v (ok %v, err %v), want format_id %q", enc, ok, err, ts.ID())
	}
}

// A pinned suite id the registry does not know (Load refuses it; this
// plants it past Load) is refused and never replaced with the active key's
// suite or the default. Red if the engine substitutes.
func TestAWriteUnderAnUnknownSuiteIsRefusedAndNotSubstituted(t *testing.T) {
	ts := lifecycleSuite(t, copycrypt.StatusSupported, "")
	f := newFixture(t, fixtureOpts{})
	underSuite(t, f, ts)
	bs := f.engine.Sets.(fixedSets).set
	bs.RetainedCopyEncryption.Suite = copycrypt.TestSuitePrefix + "nobody/v1"
	f.engine.Sets = fixedSets{set: bs}

	f.runCycle()
	if n := f.medium.uploadCount(); n != 0 {
		t.Fatalf("%d upload(s) under a suite this build does not know", n)
	}
	if _, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, testMedium); err != nil || ok {
		t.Fatalf("a copy was recorded under another suite (err %v)", err)
	}
	if !strings.Contains(f.onlyMove().Error, bs.RetainedCopyEncryption.Suite) {
		t.Errorf("the move row %q does not name the pinned suite", f.onlyMove().Error)
	}
}

// Every read of a copy under a retired suite still works: content
// decrypt-verification through the recorded suite and key, a decrypt to a
// file, and a medium-to-medium move of the ciphertext. The copy is written
// while the suite is supported, then the suite is retired.
//
// Red if any read path consults the status (or the policy's suite) instead
// of the suite the copy's record names.
func TestReadsOfARetiredSuiteStillWork(t *testing.T) {
	ts := lifecycleSuite(t, copycrypt.StatusSupported, "")

	f := newFixture(t, fixtureOpts{content: multiChunk()})
	keys, ck := underSuite(t, f, ts)

	a := &transport.Medium{ID: testMedium, Type: transport.MediumTypeS3, Bucket: "nas-backups", Prefix: "retnd"}
	b := &transport.Medium{ID: mediumB, Type: transport.MediumTypeS3, Bucket: "nas-annual", Prefix: "annual"}
	f.engine.Mediums = twoMediums{a: a, b: b, class: placement.Content}
	local := &recordingLocal{LocalStore: f.engine.Local}
	f.engine.Local = local
	store := &recordingStore{MediumStore: f.engine.Store, opens: map[string]int{}}
	f.engine.Store = store
	keyB, err := transport.MediumKey(b.Prefix, f.artifact)
	if err != nil {
		t.Fatal(err)
	}
	two := &twoMediumFixture{fixture: f, a: a, b: b, keyB: keyB, local: local, store: store}
	if report := f.runCycle(); report.Completed != 1 {
		t.Fatalf("setup: the copy under %s was not written: %+v", ts.ID(), report)
	}
	enc, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, testMedium)
	if err != nil || !ok || enc.FormatID != ts.ID() || enc.KeyID != ck.ID {
		t.Fatalf("setup: record %+v (ok %v, err %v), want format_id %q under key %s", enc, ok, err, ts.ID(), ck.ID)
	}
	p, _ := f.placement(testMedium)

	ts.SetStatus(copycrypt.StatusRetired, "0.9.0")
	if err := copycrypt.CheckWrite(ts); !errors.Is(err, copycrypt.ErrSuiteRetired) {
		t.Fatalf("control: the suite is not retired after SetStatus: %v", err)
	}

	t.Run("decrypt-verify", func(t *testing.T) {
		res, err := placement.VerifyCopy(f.ctx, store, keys, *a, p, &enc, placement.Content, testNow2)
		if err != nil || !res.Passed || res.Class != placement.Content {
			t.Fatalf("a copy under a retired suite no longer decrypt-verifies: %+v, %v", res, err)
		}
		if res.Encryption == nil || res.Encryption.FormatID != ts.ID() {
			t.Errorf("verified as %+v, want the recorded suite %q", res.Encryption, ts.ID())
		}
	})

	t.Run("decrypt", func(t *testing.T) {
		suite, ok := copycrypt.Lookup(enc.FormatID)
		if !ok {
			t.Fatalf("the recorded suite %q is not in the registry", enc.FormatID)
		}
		out := t.TempDir()
		err := keys.WithKey(f.ctx, enc.KeyID, func(k []byte) error {
			_, err := copycrypt.DecryptToFile(f.ctx, bytes.NewReader(f.medium.bytesAt(f.key)), suite, k, f.hash, out, "artifact")
			return err
		})
		if err != nil {
			t.Fatalf("a copy under a retired suite no longer decrypts: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(out, "artifact"))
		if err != nil || !bytes.Equal(got, f.content) {
			t.Fatalf("the decrypted file is not the artifact (err %v)", err)
		}
	})

	t.Run("move", func(t *testing.T) {
		before := append([]byte(nil), f.medium.bytesAt(f.key)...)
		if report := two.moveToB(); report.Completed != 1 {
			t.Fatalf("a medium-to-medium move of a copy under a retired suite did not complete: %+v", report)
		}
		if !bytes.Equal(f.medium.bytesAt(keyB), before) {
			t.Fatal("the re-homed object is not the same ciphertext: a move must not re-encrypt, least of all under a retired suite")
		}
		dst, ok, err := f.journal.PlacementEncryption(f.ctx, f.artifact, mediumB)
		if err != nil || !ok || dst.FormatID != ts.ID() || dst.KeyID != ck.ID {
			t.Fatalf("the destination record is %+v (ok %v, err %v), want the source's suite %q and key %s", dst, ok, err, ts.ID(), ck.ID)
		}
		if row, ok := f.placement(mediumB); !ok || row.Status != state.PlacementActive || row.VerificationClass != state.VerificationContent {
			t.Fatalf("the destination placement is %+v (ok %v), want ACTIVE at content", row, ok)
		}
	})
}
