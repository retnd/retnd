package movecrash_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EPIC N's N1.3 and NC-14 (#991, proven on the release PR #1331): a required
// set whose active key cannot be resolved refuses the copy before anything
// destructive, in a real process over the real engine. The managed key file
// is moved out of the key directory, so the key row is there and the
// material is not, which is what a lost or unmounted key directory looks
// like. After the cycle the local copy is still on disk and byte-identical,
// nothing reached the medium or the staging area, no move got as far as
// SOURCE_DELETE_PENDING, no encryption record exists, and the process never
// reached the medium upload (the harness's kill-before-upload hook is armed
// and must not fire).
//
// The control puts the key file back and the very next cycle writes the copy,
// so the refusal is about the key and nothing else.
//
// Its planted violation, "N1.3 an unreadable key let through to the
// encrypting pass" in scripts/bdtools/encryption/controls_r2.py, removes the
// readability check from the engine's key selection, so the refusal only
// comes at the read-back after the upload; this cell is what goes red.
func TestAnUnresolvableKeyRefusesTheCopyBeforeAnythingIsWritten_1331(t *testing.T) {
	w := newWorldOver(t, multiChunkArtifact())
	w.enableEncryption()

	keyFiles, err := filepath.Glob(filepath.Join(w.keyDir, "*.key"))
	if err != nil || len(keyFiles) != 1 {
		t.Fatalf("want exactly one managed key file in %s, found %v (err=%v)", w.keyDir, keyFiles, err)
	}
	away := filepath.Join(w.dir, "key-moved-away")
	if err := os.Rename(keyFiles[0], away); err != nil {
		t.Fatal(err)
	}

	// The upload hook is armed: a process that reaches the medium upload with
	// a key nobody can read dies there, which is the boundary this row is
	// about. A correct engine refuses first and never reaches it.
	res := w.crashTo(crashMedium, "-plan", "-kill-before-upload")
	if res.killed() {
		t.Fatalf("the process reached the medium upload with a key nobody can read:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "FINISHED") || !strings.Contains(res.stdout, "does not resolve") {
		t.Fatalf("the cycle did not end refusing the unresolvable key:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if v := res.violations(); len(v) > 0 {
		t.Fatalf("invariant violations: %v", v)
	}
	got, err := os.ReadFile(w.localPath())
	if err != nil {
		t.Fatalf("the local copy is gone after a refused copy: %v", err)
	}
	if !bytes.Equal(got, w.content) {
		t.Fatal("the local copy changed under a refused copy")
	}
	if n := w.objectCount(t); n != 0 {
		t.Fatalf("%d object(s) on the medium although the key could not be read; nothing may be written before the key is known", n)
	}
	if left := w.stagingLeftovers(); len(left) != 0 {
		t.Fatalf("the staging area holds %v after a refusal", left)
	}
	j := w.journalNow()
	if p := lastPhase(t, w, j); pastTheDestructiveStep(p) {
		t.Fatalf("the move reached %s with a key nobody can read", p)
	}
	if _, ok, err := j.PlacementEncryption(w.ctx, w.artifact, crashMedium); err != nil || ok {
		t.Fatalf("an encryption record exists after a refusal (ok=%v err=%v)", ok, err)
	}
	w.checkInvariantNow(j, "after the refusal")
	_ = j.Close()

	// The control: the same deployment with its key file back moves the copy.
	if err := os.Rename(away, keyFiles[0]); err != nil {
		t.Fatal(err)
	}
	res = w.crashTo(crashMedium, "-plan")
	if res.killed() || !strings.Contains(res.stdout, "FINISHED") {
		t.Fatalf("with the key back the cycle did not finish:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if w.localExists() {
		t.Fatal("with the key back the move still did not happen")
	}
	if len(w.objectAtKey()) == 0 {
		t.Fatal("with the key back there is no object at the key")
	}
}
