package encryptionsuites_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
)

// Suite B of #1319: for each shipped suite, the five crypto negatives (bit
// flip, truncated tail, reordered chunk, swapped header, wrong key) and the
// cross-suite key in both directions, through the custody the product
// resolves keys with, each failing closed with no plaintext on disk. A
// failure is a "Decrypt failed" on every surface that renders it and never
// the content class; at this level that is the error class and the absence
// of any output file. The classes are copycrypt's: ErrCorrupt for damaged
// bytes, ErrWrongKey for a key of the right suite that is not this copy's,
// and ErrKeyUnavailable for a key of another suite, which decrypt refuses
// before a byte of the object is read. The string a person sees
// is the browser and terminal half's claim.
//
// Two planted openers stand in for broken adapters, and the checker has to
// call each of them out: one that accepts a truncated tail and one whose key
// lookup accepts any suite's key. They are the falsification the matrix cell
// records; a checker that passed them would make every green row below
// meaningless.

// opener recovers obj, recorded under suiteID and keyID, into dir/name.
type opener func(r *rig, dir, name, suiteID, keyID string, obj []byte, want string) (string, error)

func realOpener(r *rig, dir, name, suiteID, keyID string, obj []byte, want string) (string, error) {
	return r.recoverTo(dir, name, suiteID, keyID, obj, want)
}

type negCase struct {
	name    string
	obj     []byte
	suiteID string // the suite the copy's record names
	keyID   string // the key the copy's record names
	by      *rig   // whose custody resolves keyID
	want    error
}

// negativesFor builds every negative over one copy sealed under suite id in
// base, with foreign as the other deployment (the wrong key) and the other
// shipped suite's key in base as the cross-suite key.
func negativesFor(t *testing.T, base, foreign *rig, id string, plain []byte) (good []byte, cases []negCase) {
	t.Helper()
	good, _ = base.seal(id, "artifact.dump", plain)
	hdr := headerEnd(t, good)
	payload := hdr + nonceLen

	// A second encryption of the same plaintext to the SAME recipient: a
	// header for another recipient reads as a wrong key and would test
	// nothing about a swapped header.
	again, _ := sealUnder(t, base.ctx, suiteOf(t, id), base.keys[id].Recipient, base.staging, "again.dump", plain)
	againHdr := headerEnd(t, again)

	swapped := bytes.Clone(good)
	a, c := swapped[payload:payload+chunkSealed], swapped[payload+chunkSealed:payload+2*chunkSealed]
	tmp := bytes.Clone(a)
	copy(a, c)
	copy(c, tmp)

	flip := func(at int) []byte { b := bytes.Clone(good); b[at] ^= 0x01; return b }

	other := ageV1
	if id == ageV1 {
		other = agePQV1
	}
	otherObj, _ := sealUnder(t, base.ctx, suiteOf(t, other), base.keys[other].Recipient, base.staging, "other.dump", plain)

	key := base.keys[id].ID
	cases = []negCase{
		{"bit flip in the second chunk", flip(payload + chunkSealed + 1000), id, key, base, copycrypt.ErrCorrupt},
		{"bit flip in the final tag", flip(len(good) - 1), id, key, base, copycrypt.ErrCorrupt},
		{"bit flip in the recipient stanza", flip(hdr / 2), id, key, base, nil},
		{"truncated tail, one byte", good[:len(good)-1], id, key, base, copycrypt.ErrCorrupt},
		{"truncated tail, mid chunk", good[:payload+chunkSealed+5], id, key, base, copycrypt.ErrCorrupt},
		{"truncated tail, exactly on a chunk boundary", good[:payload+3*chunkSealed], id, key, base, copycrypt.ErrCorrupt},
		{"reordered chunk", swapped, id, key, base, copycrypt.ErrCorrupt},
		{"swapped header", append(bytes.Clone(again[:againHdr]), good[hdr:]...), id, key, base, copycrypt.ErrCorrupt},
		{"wrong key (another deployment's key under the same suite)", good, id, foreign.keys[id].ID, foreign, copycrypt.ErrWrongKey},
		// The cross-suite key: the copy's record names this suite, the key
		// handed over is the other suite's identity in the same deployment.
		{"cross-suite key: a " + other + " identity on a " + id + " copy", good, id, base.keys[other].ID, base, copycrypt.ErrKeyUnavailable},
		// The record and the key agree with each other and both name the
		// other suite; the bytes are this suite's. A reader that trusted the
		// record's suite over what the header says would still fail closed
		// here, and one that sniffed the header and picked a key by it would
		// open it: that is the planted lookup below.
		{"cross-suite record: " + other + " record and key over " + id + " bytes", good, other, base.keys[other].ID, base, copycrypt.ErrWrongKey},
		// And the other suite's copy under this suite's record and key.
		{"cross-suite key: a " + id + " identity on a " + other + " copy", otherObj, other, key, base, copycrypt.ErrKeyUnavailable},
	}
	return good, cases
}

// failuresOf runs every case through open and returns one line per case that
// did not fail closed. A case with want == nil only has to fail (the
// stanza flip may read as either class); every other case has to fail with
// its own class, because a wrong key read as corruption sends an operator
// looking for a damaged medium.
func failuresOf(t *testing.T, base *rig, plain []byte, cases []negCase, open opener) []string {
	t.Helper()
	var out []string
	for i, c := range cases {
		dir := filepath.Join(base.root, fmt.Sprintf("recovered-%d", i))
		path, err := open(c.by, dir, "artifact.dump", c.suiteID, c.keyID, c.obj, sum(plain))
		switch {
		case err == nil:
			out = append(out, c.name+": recovered (the content class)")
		case c.want != nil && !errors.Is(err, c.want):
			out = append(out, fmt.Sprintf("%s: err = %v, want %v", c.name, err, c.want))
		}
		if err != nil && path != "" {
			out = append(out, fmt.Sprintf("%s: a failed recovery returned the path %q", c.name, path))
		}
		if entries, _ := os.ReadDir(dir); err != nil && len(entries) != 0 {
			out = append(out, fmt.Sprintf("%s: a failed recovery left %d entries", c.name, len(entries)))
		}
		if err != nil {
			for _, r := range []*rig{base, c.by} {
				if found := plaintextUnder(t, r.root, plain); len(found) != 0 {
					out = append(out, fmt.Sprintf("%s: plaintext left on disk at %v", c.name, found))
				}
			}
		}
		_ = os.RemoveAll(dir) // under t.TempDir; keeps one case's success from colouring the next
	}
	return out
}

func TestEveryCryptoNegativeFailsClosedForEachSuiteAndAcrossSuites_1319(t *testing.T) {
	for _, id := range shipped {
		t.Run(id, func(t *testing.T) {
			plain := fixture(7)
			base, foreign := newRig(t), newRig(t)
			good, cases := negativesFor(t, base, foreign, id, plain)
			for _, f := range failuresOf(t, base, plain, cases, realOpener) {
				t.Error(f)
			}

			// The control: the untouched object recovers through its own
			// record, and the plaintext scan sees what it wrote, so the empty
			// scans above are not an empty scanner.
			out := filepath.Join(base.root, "recovered-control")
			path, err := base.recoverTo(out, "artifact.dump", id, base.keys[id].ID, good, sum(plain))
			if err != nil {
				t.Fatalf("the untouched %s object does not recover: %v", id, err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("the recovered file is not the artifact (err=%v)", err)
			}
			if found := plaintextUnder(t, base.root, plain); len(found) != 1 || found[0] != path {
				t.Fatalf("the scan found %v after a successful recovery, want exactly %s", found, path)
			}
		})
	}
}

// The planted "accept truncation" adapter: wherever the real reader refuses
// an object that is a strict prefix of the good one, it hands back what a
// streaming reader had already released, as if the end of input were the end
// of the file. The checker has to name every truncation case and only those
// cases.
func TestTheNegativesCatchAnAdapterThatAcceptsTruncation_1319(t *testing.T) {
	for _, id := range shipped {
		t.Run(id, func(t *testing.T) {
			plain := fixture(9)
			base, foreign := newRig(t), newRig(t)
			good, cases := negativesFor(t, base, foreign, id, plain)
			hdr := headerEnd(t, good)

			planted := func(r *rig, dir, name, suiteID, keyID string, obj []byte, want string) (string, error) {
				path, err := realOpener(r, dir, name, suiteID, keyID, obj, want)
				if errors.Is(err, copycrypt.ErrCorrupt) && len(obj) < len(good) && bytes.HasPrefix(good, obj) && len(obj) > hdr {
					released := max(0, (len(obj)-hdr-nonceLen)/chunkSealed) * chunk
					if err := os.MkdirAll(dir, 0o700); err != nil {
						return "", err
					}
					p := filepath.Join(dir, name)
					return p, os.WriteFile(p, plain[:min(released, len(plain))], 0o600)
				}
				return path, err
			}

			got := failuresOf(t, base, plain, cases, planted)
			var truncations []string
			for _, c := range cases {
				if len(c.obj) < len(good) && bytes.HasPrefix(good, c.obj) {
					truncations = append(truncations, c.name)
				}
			}
			if len(truncations) != 3 {
				t.Fatalf("the corpus has %d truncation cases, want 3: %v", len(truncations), truncations)
			}
			for _, name := range truncations {
				if !containsPrefix(got, name+":") {
					t.Errorf("the planted truncation-accepting adapter passed %q; the checker cannot see it", name)
				}
			}
		})
	}
}

// The planted "accept any suite's key" lookup: it ignores the key id the
// copy's record names and tries every key the deployment holds, of either
// suite, until one opens the bytes. Every cross-suite case has to be named,
// because each of them is exactly a copy opened by a key it was not recorded
// under.
func TestTheNegativesCatchAKeyLookupThatAcceptsAnySuitesKey_1319(t *testing.T) {
	for _, id := range shipped {
		t.Run(id, func(t *testing.T) {
			plain := fixture(13)
			base, foreign := newRig(t), newRig(t)
			_, cases := negativesFor(t, base, foreign, id, plain)

			planted := func(r *rig, dir, name, _, _ string, obj []byte, want string) (string, error) {
				var last error
				for _, sid := range shipped {
					path, err := r.recoverTo(dir, name, sid, r.keys[sid].ID, obj, want)
					if err == nil {
						return path, nil
					}
					last = err
				}
				return "", last
			}

			got := failuresOf(t, base, plain, cases, planted)
			var cross int
			for _, c := range cases {
				if !bytes.Contains([]byte(c.name), []byte("cross-suite")) {
					continue
				}
				cross++
				if !containsPrefix(got, c.name+":") {
					t.Errorf("the planted any-suite lookup passed %q; the checker cannot see it", c.name)
				}
			}
			if cross != 3 {
				t.Fatalf("the corpus has %d cross-suite cases, want 3", cross)
			}
		})
	}
}

func containsPrefix(lines []string, prefix string) bool {
	for _, l := range lines {
		if len(l) >= len(prefix) && l[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
