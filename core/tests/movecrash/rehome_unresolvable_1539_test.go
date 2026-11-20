package movecrash_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EPIC N's NC-14, the re-home seam (#1539). An encrypted copy already on one
// medium is moved to a second medium while its key cannot be resolved, in a
// real process over the real engine. The ciphertext is copied as it is, so
// the copy itself needs no key; what needs it is the decrypt-verify of the
// destination, and that has to refuse before the VERIFIED to
// SOURCE_DELETE_PENDING advance, which is the first step that can remove the
// source. The in-process proof of the same refusal is
// core/internal/placement:TestARehomeThatCannotDecryptTheDestinationKeepsTheSource_992;
// this is the crash cell beside it.
//
// The harness is armed to die the instant SOURCE_DELETE_PENDING commits,
// which every removal of the source has to pass through. A correct engine
// never reaches it, so the process must finish on its own, with the source
// object still at its key and byte-identical. The harness is deliberately
// not armed on object deletes: an unverifiable destination is discarded
// (recopyOrAbandon deletes it from the second medium), and that delete is
// the engine cleaning up after itself, not a step towards the source.
//
// The control puts the key file back and the very next cycle completes the
// re-home, so the refusal is about the key and nothing else.
func TestAnEncryptedRehomeWithAnUnresolvableKeyNeverReachesTheSourceDelete_1539(t *testing.T) {
	w := newWorldOver(t, multiChunkArtifact())
	w.enableEncryption()
	w.firstHop()

	source := w.objectAtKey()
	if !bytes.HasPrefix(source, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("the first hop left something other than age ciphertext at the key (%d bytes), so there is no encrypted copy to re-home", len(source))
	}

	keyFiles, err := filepath.Glob(filepath.Join(w.keyDir, "*.key"))
	if err != nil || len(keyFiles) != 1 {
		t.Fatalf("want exactly one managed key file in %s, found %v (err=%v)", w.keyDir, keyFiles, err)
	}
	away := filepath.Join(w.dir, "key-moved-away")
	if err := os.Rename(keyFiles[0], away); err != nil {
		t.Fatal(err)
	}

	res := w.crashTo(crashSecondMedium, "-plan", "-kill-after-phase=SOURCE_DELETE_PENDING")
	if res.killed() {
		t.Fatalf("the re-home committed SOURCE_DELETE_PENDING with a key nobody can read:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "FINISHED") {
		t.Fatalf("the cycle did not finish:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if v := res.violations(); len(v) > 0 {
		t.Fatalf("invariant violations: %v", v)
	}
	if got := w.objectAtKey(); !bytes.Equal(got, source) {
		t.Fatalf("the source object changed or went (%d bytes, was %d) under a re-home that could not decrypt its destination", len(got), len(source))
	}
	j := w.journalNow()
	if p := lastPhase(t, w, j); pastTheDestructiveStep(p) {
		t.Fatalf("the re-home reached %s with a key nobody can read", p)
	}
	w.checkInvariantNow(j, "after the refused re-home")
	_ = j.Close()

	// The control: with the key file back the same re-home completes, the
	// destination holds the same ciphertext and the source object is gone.
	if err := os.Rename(away, keyFiles[0]); err != nil {
		t.Fatal(err)
	}
	res = w.crashTo(crashSecondMedium, "-plan")
	if res.killed() || !strings.Contains(res.stdout, "FINISHED") {
		t.Fatalf("with the key back the re-home did not finish:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	dest, err := os.ReadFile(filepath.Join(w.secondBucket, filepath.FromSlash(w.key)))
	if err != nil {
		t.Fatalf("with the key back there is no object at the key on the second medium: %v", err)
	}
	if !bytes.Equal(dest, source) {
		t.Fatal("with the key back the destination is not the ciphertext the source held; a re-home copies the stored bytes as they are")
	}
	if len(w.objectAtKey()) != 0 {
		t.Fatal("with the key back the re-home completed and the source object is still there")
	}
}
