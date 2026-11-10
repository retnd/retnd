package movecrash_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC T's engine-level rows (#1319, T2.4): the suite policy that cannot be
// honoured, the retired suite, and re-encryption under the active suite, each
// through a real harness process over the real journal, with the move's
// source checked on disk after the cycle rather than asserted from a return
// value.
//
// The harness flags these cells lean on: -encrypt-suite (the set's resolved
// policy suite) and -test-retire-suite (the registry's test hook, in the
// harness process only). Re-encryption is the encrypt-existing entry point
// over a source copy that is already encrypted (T2.2 generalises #1291's
// source), so it takes no flag of its own.
//
// Written by reading the harness, the engine and the T1.4, T2.1 and T2.2
// issues; not run. Each names the defect that turns it red.

const (
	suiteAgeV1   = "age/v1"
	suiteAgePQV1 = "age-pq/v1"
)

// stanzaOf names the recipient stanza an age object's header carries.
func stanzaOf(t *testing.T, obj []byte) string {
	t.Helper()
	end := bytes.Index(obj, []byte("\n--- "))
	if !bytes.HasPrefix(obj, []byte("age-encryption.org/v1\n")) || end < 0 {
		t.Fatalf("not an age v1 object: %q", obj[:min(len(obj), 60)])
	}
	switch hdr := obj[:end]; {
	case bytes.Contains(hdr, []byte("\n-> mlkem768x25519 ")):
		return suiteAgePQV1
	case bytes.Contains(hdr, []byte("\n-> X25519 ")):
		return suiteAgeV1
	}
	t.Fatalf("an age header with neither stanza:\n%s", obj[:end])
	return ""
}

// enableEncryptionUnder is enableEncryption with the key minted under one
// named suite, so a cell can hold a key whose suite is not the policy's.
func (w *world) enableEncryptionUnder(suite string) keycustody.KeyRecord {
	w.t.Helper()
	w.keyDir = filepath.Join(w.dir, "keys")
	if err := os.MkdirAll(w.keyDir, 0o700); err != nil {
		w.t.Fatal(err)
	}
	j, err := state.Open(w.ctx, w.journal)
	if err != nil {
		w.t.Fatalf("opening the journal: %v", err)
	}
	defer func() { _ = j.Close() }()
	rec, err := w.keysOver(j).CreateSuite(w.ctx, suite, crashNow)
	if err != nil {
		w.t.Fatalf("minting a key under %s: %v", suite, err)
	}
	return rec
}

// rotateUnder makes a new key under suite the active one, the way
// `retnd encryption key rotate --suite` does, with the journal closed again
// before any harness opens it.
func (w *world) rotateUnder(suite string) keycustody.KeyRecord {
	w.t.Helper()
	j, err := state.Open(w.ctx, w.journal)
	if err != nil {
		w.t.Fatalf("opening the journal: %v", err)
	}
	defer func() { _ = j.Close() }()
	rec, err := w.keysOver(j).RotateSuite(w.ctx, suite, crashNow.Add(1))
	if err != nil {
		w.t.Fatalf("rotating to %s: %v", suite, err)
	}
	return rec
}

// lastPhase is the phase the artifact's newest move ended in, "" with no move.
func lastPhase(t *testing.T, w *world, j *state.Journal) placement.Phase {
	t.Helper()
	moves, err := j.MovesForArtifact(w.ctx, w.artifact)
	if err != nil {
		t.Fatalf("MovesForArtifact: %v", err)
	}
	if len(moves) == 0 {
		return ""
	}
	return placement.Phase(moves[len(moves)-1].Phase)
}

// pastTheDestructiveStep is every phase at or after the first one that can
// remove a copy.
func pastTheDestructiveStep(p placement.Phase) bool {
	switch p {
	case placement.SourceDeletePending, placement.Done:
		return true
	}
	return false
}

// A required set whose policy suite has no active key refuses the copy before
// any destructive step: the cycle ends, the move's source (the local copy) is
// still on disk and byte-identical, nothing was written to the medium or left
// in staging, no encryption record exists, and the key of the other suite was
// not used as a substitute. Then the control: rotating to the policy's suite
// lets the very next cycle write the copy under it. Red if the engine
// substitutes the age/v1 key, deletes the source on a refused copy, or if the
// refusal is permanent (the control fails).
func TestTheMismatchRefusalLeavesTheSourceOnDiskAndWritesNothing_1319(t *testing.T) {
	w := newWorldOver(t, multiChunkArtifact())
	old := w.enableEncryptionUnder(suiteAgeV1)

	res := w.crashTo(crashMedium, "-plan", "-encrypt-suite", suiteAgePQV1)
	if res.killed() {
		t.Fatalf("a run with no kill plan was killed:\n%s", res.stderr)
	}
	if !strings.Contains(res.stdout, "FINISHED") || !strings.Contains(res.stdout, `refused="`) || strings.Contains(res.stdout, `refused=""`) {
		t.Fatalf("the cycle did not end with a refused outcome:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, suiteAgePQV1) || !strings.Contains(res.stdout, "rotate --suite") {
		t.Errorf("the refusal does not name the policy suite and the rotate command:\n%s", res.stdout)
	}
	if v := res.violations(); len(v) > 0 {
		t.Fatalf("invariant violations: %v", v)
	}

	got, err := os.ReadFile(w.localPath())
	if err != nil {
		t.Fatalf("the move's source is gone after a refused copy: %v", err)
	}
	if !bytes.Equal(got, w.content) {
		t.Fatal("the move's source changed under a refused copy")
	}
	if n := w.objectCount(t); n != 0 {
		t.Fatalf("%d object(s) on the medium after a refusal; nothing may be written under another suite", n)
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Fatalf("the staging area holds %v after a refusal", left)
	}
	j := w.journalNow()
	if p := lastPhase(t, w, j); pastTheDestructiveStep(p) {
		t.Fatalf("the move reached %s on a refused copy", p)
	}
	if _, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium); err != nil || ok {
		t.Fatalf("an encryption record exists after a refusal (ok=%v err=%v)", ok, err)
	}
	w.checkInvariantNow(j, "after the refusal")
	_ = j.Close()

	// The control: the refusal is about the missing key and nothing else.
	fresh := w.rotateUnder(suiteAgePQV1)
	res = w.crashTo(crashMedium, "-plan", "-encrypt-suite", suiteAgePQV1)
	if res.killed() || !strings.Contains(res.stdout, "FINISHED") {
		t.Fatalf("after the rotation the cycle did not finish:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if w.localExists() {
		t.Fatal("after the rotation the move still did not happen")
	}
	obj := w.objectAtKey()
	if s := stanzaOf(t, obj); s != suiteAgePQV1 {
		t.Fatalf("the copy written after the rotation is %s, want %s", s, suiteAgePQV1)
	}
	j = w.journalNow()
	rec, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium)
	if err != nil || !ok {
		t.Fatalf("no encryption record after the move (ok=%v err=%v)", ok, err)
	}
	if rec.FormatID != suiteAgePQV1 || rec.KeyID != fresh.ID || rec.KeyID == old.ID {
		t.Fatalf("the record says %s under %s, want %s under %s", rec.FormatID, rec.KeyID, suiteAgePQV1, fresh.ID)
	}
	if err := w.keysOver(j).WithKey(w.ctx, rec.KeyID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), mustSuite(t, rec.FormatID), k, w.hash)
		return err
	}); err != nil {
		t.Fatalf("the copy does not decrypt-verify through its record: %v", err)
	}
}

func mustSuite(t *testing.T, id string) copycrypt.Suite {
	t.Helper()
	s, err := copycrypt.Lookup(id)
	if err != nil {
		t.Fatalf("copycrypt.Lookup(%q): %v", id, err)
	}
	return s
}

// A retired suite refuses a write before any destructive step, and its copies
// still recover. The world holds a real age/v1 copy on the first medium; the
// hop to the second medium runs with age/v1 retired in the harness process
// and is refused, with the first medium's object untouched. Then, with age/v1
// retired in this process too, the copy decrypt-verifies through its own
// record and key. Red if retirement stops reads (an operator loses access to
// every copy of the old suite on an upgrade) or if it fails to stop a write.
func TestARetiredSuiteRefusesTheWriteAndItsCopiesStillRecover_1319(t *testing.T) {
	w := newWorldOver(t, multiChunkArtifact())
	key := w.enableEncryptionUnder(suiteAgeV1)
	res := w.crashTo(crashMedium, "-plan", "-encrypt-suite", suiteAgeV1)
	if res.killed() || !strings.Contains(res.stdout, "FINISHED") || w.localExists() {
		t.Fatalf("the first hop under age/v1 did not finish:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	before := w.objectAtKey()
	if stanzaOf(t, before) != suiteAgeV1 {
		t.Fatal("control: the first copy is not age/v1, so retiring age/v1 tests nothing")
	}

	res = w.crashTo(crashSecondMedium, "-plan", "-encrypt-suite", suiteAgeV1, "-test-retire-suite", suiteAgeV1)
	if res.killed() || !strings.Contains(res.stdout, "FINISHED") || strings.Contains(res.stdout, `refused=""`) {
		t.Fatalf("a write under a retired suite was not refused:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if n := w.objectCountOn(t, w.secondBucket); n != 0 {
		t.Fatalf("%d object(s) on the second medium under a retired suite", n)
	}
	if !bytes.Equal(w.objectAtKey(), before) {
		t.Fatal("the first medium's copy changed under a refused write")
	}
	j := w.journalNow()
	if p := lastPhase(t, w, j); pastTheDestructiveStep(p) && p != placement.Done {
		t.Fatalf("the refused move reached %s", p)
	}

	restore, err := copycrypt.SetStatusForTest(suiteAgeV1, copycrypt.StatusRetired)
	if err != nil {
		t.Fatalf("SetStatusForTest: %v", err)
	}
	defer restore()
	rec, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium)
	if err != nil || !ok || rec.FormatID != suiteAgeV1 || rec.KeyID != key.ID {
		t.Fatalf("the copy's record is %+v (ok=%v err=%v)", rec, ok, err)
	}
	if err := w.keysOver(j).WithKey(w.ctx, rec.KeyID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(before), mustSuite(t, rec.FormatID), k, w.hash)
		return err
	}); err != nil {
		t.Fatalf("a copy under a retired suite no longer decrypt-verifies: %v", err)
	}
	h, err := w.keysOver(j).Health(w.ctx, true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	var seen bool
	for _, s := range h.Suites {
		if s.ID == suiteAgeV1 {
			seen = true
			if s.Status != string(copycrypt.StatusRetired) || s.ActiveCopies != 1 {
				t.Errorf("health for the retired suite says %+v, want retired with one active copy", s)
			}
		}
	}
	if !seen {
		t.Errorf("health names no entry for the retired suite while a copy records it: %+v", h.Suites)
	}
}

// Re-encryption under the active suite with a kill part-way through the
// decrypt-into-encrypt stream: the old age/v1 copy on the first medium is
// read through its suite's reader straight into age-pq/v1's writer, and the
// process dies mid-read. At the crash the old copy is untouched and nothing
// on local disk holds plaintext; after the restart the move is DONE, the new
// copy is age-pq/v1 under the new key and decrypt-verifies, and the old copy
// is gone only now. Red if the kill leaves plaintext in staging or in the
// set's directory, if the old copy is retired before the new one verified, or
// if the restart re-uses a half-written object.
func TestAReencryptionKilledMidStreamReconcilesWithNoPlaintextOnDisk_1319(t *testing.T) {
	w := newWorldOver(t, multiChunkArtifact())
	w.enableEncryptionUnder(suiteAgeV1)
	res := w.crashTo(crashMedium, "-plan", "-encrypt-suite", suiteAgeV1)
	if res.killed() || !strings.Contains(res.stdout, "FINISHED") || w.localExists() {
		t.Fatalf("the first hop under age/v1 did not finish:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	old := w.objectAtKey()
	fresh := w.rotateUnder(suiteAgePQV1)

	res = w.crashTo(crashSecondMedium, "-plan", "-encrypt-existing", "-encrypt-suite", suiteAgePQV1, "-kill-during-stage")
	if !res.killed() {
		t.Fatalf("the harness was not killed by SIGKILL (err=%v)\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "MOVECRASH_SELF_KILL: ") || !strings.Contains(res.stderr, "was interrupted in flight") {
		t.Fatalf("the process died, but not mid-read:\n%s", res.stderr)
	}
	if !bytes.Equal(w.objectAtKey(), old) {
		t.Fatal("the old copy changed or vanished at the crash, before the new one verified")
	}
	for _, dir := range []string{w.root, filepath.Join(w.root, placement.StagingDirName), w.secondBucket} {
		if found := plaintextIn(t, dir, w.content); len(found) != 0 {
			t.Fatalf("plaintext on disk after a kill mid-re-encryption: %v", found)
		}
	}
	j := w.journalNow()
	w.checkInvariantNow(j, "at the instant of the crash")

	keys := w.keysOver(j)
	engine, guard := w.restartEngine(j, false)
	engine.Sets = crashSets{set: config.BackupSet{
		Name: crashSet, ID: w.artifact.Set, LocalPath: w.root,
		RetainedCopyEncryption: config.RetainedCopyEncryption{
			Mode:        config.RetainedCopyEncryptionRequired,
			KeyProvider: config.RetainedCopyKeyProvider{Managed: true},
			Suite:       suiteAgePQV1,
		},
	}}
	engine.Keys = keys
	report, err := engine.EncryptExisting(w.ctx, w.retroPlans())
	if err != nil {
		t.Fatalf("the restart failed: %v", err)
	}
	guard.fail()
	if report.Resumed != 1 {
		t.Fatalf("the restart did not pick the re-encryption up: %+v", report)
	}
	if p := lastPhase(t, w, j); p != placement.Done {
		t.Fatalf("the re-encryption ended in %s, want DONE", p)
	}
	obj := w.ciphertextObject()
	if stanzaOf(t, obj) != suiteAgePQV1 {
		t.Fatal("the new copy is not age-pq/v1")
	}
	rec, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashSecondMedium)
	if err != nil || !ok || rec.FormatID != suiteAgePQV1 || rec.KeyID != fresh.ID || rec.StoredHash != hexSum(obj) {
		t.Fatalf("the new copy's record is %+v (ok=%v err=%v), want age-pq/v1 under %s describing the object", rec, ok, err, fresh.ID)
	}
	if err := keys.WithKey(w.ctx, rec.KeyID, func(k []byte) error {
		_, err := copycrypt.DecryptVerify(w.ctx, bytes.NewReader(obj), mustSuite(t, rec.FormatID), k, w.hash)
		return err
	}); err != nil {
		t.Fatalf("the new copy does not decrypt-verify: %v", err)
	}
	if w.objectAtKey() != nil {
		t.Fatal("the old copy survived a DONE re-encryption")
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Fatalf("the staging area holds %v after the restart", left)
	}
}

// plaintextIn lists files under dir holding a 64-byte stretch of plain from
// its start, a chunk boundary or its end. A missing dir holds nothing.
func plaintextIn(t *testing.T, dir string, plain []byte) []string {
	t.Helper()
	n := len(plain)
	windows := [][]byte{plain[:64], plain[65536-32 : 65536+32], plain[n-64:]}
	var found []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, win := range windows {
			if bytes.Contains(b, win) {
				found = append(found, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return found
}
