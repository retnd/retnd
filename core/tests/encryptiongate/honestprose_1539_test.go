package encryptiongate_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// EPIC N's sentences that only a reader can check, held so that deleting one
// turns this red (#1539). Each row of docs/conformance/epic-n-matrix.md that
// says "delete the sentence; the docs check goes red" names one of these.
//
// This is a sentence guard and nothing more: it proves the sentence is still
// on the page, not that the page around it is right. The page around it is
// the N2.7 prose review, which stays a person's job.

// commentMarker is a line's leading comment syntax, so a sentence wrapped
// across `--` or `//` lines reads as one sentence.
var commentMarker = regexp.MustCompile(`(?m)^[ \t]*(--|//)[ \t]?`)

// prose is a file with its comment markers gone and every run of whitespace
// collapsed to one space, so a rewrap does not read as a deletion.
func prose(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return strings.Join(strings.Fields(commentMarker.ReplaceAllString(string(raw), "")), " ")
}

func requireSentences(t *testing.T, root string, want map[string][]string) {
	t.Helper()
	for rel, sentences := range want {
		text := prose(t, root, rel)
		for _, s := range sentences {
			if !strings.Contains(text, s) {
				t.Errorf("%s no longer says %q", rel, s)
			}
		}
	}
}

// N1.15 and NC-10: the object key layout did not change, so encryption hides
// what is in an object and not what it is called, and the operator pages say
// so, with the reason (changing the layout would orphan every plaintext
// object already in the bucket).
func TestTheDocsSayEncryptionProtectsContentAndNotNames_1539(t *testing.T) {
	requireSentences(t, repoRoot(t), map[string][]string{
		"docs/encrypted-copies.md": {
			"Object names are visible",
			"changing it would orphan every plaintext object already in the bucket",
		},
		"README.md":               {"It hides contents, not names."},
		"docs/storage-mediums.md": {"It protects content, not names."},
		"docs/site/index.html":    {"It hides what is inside an object, not what the object is called."},
		"docs/adr/0028-what-encrypted-copies-prove-and-what-they-leave-visible.md": {
			"`MediumKey` was not changed, so object names are not confidential",
		},
	})
}

// NC-04: a managed key sits beside the copies' state, so it protects them
// from whoever holds the bucket and not from whoever owns the machine, and
// the recovery bundle is the one moment the key leaves. That is the sentence
// the spec asks to see "in writing".
func TestTheDocsSayWhatAManagedKeyProtectsAgainst_1539(t *testing.T) {
	requireSentences(t, repoRoot(t), map[string][]string{
		"docs/encrypted-copies.md": {
			"a managed key does **not** protect your copies from whoever owns this machine",
			"It protects them from whoever owns the **bucket**. That is the whole claim.",
			"the one way key material leaves is the recovery bundle",
			"a bundle is the one moment a managed key leaves this machine",
		},
	})
}

// NC-07 and N2.7: the two comments that called placements.hash the digest of
// the bytes at the destination. 0007 is frozen (internal/state/migrate.go
// refuses a shipped migration whose text moved, comments included), so its
// sentence is still there and has to stay; what this holds is that 0015 says
// it was wrong and why, and that engine.go no longer says it at all.
func TestTheTwoFalsifiedDigestCommentsStayCorrected_1539(t *testing.T) {
	root := repoRoot(t)
	requireSentences(t, root, map[string][]string{
		"core/migrations/0015_retained_copy_encryption.sql": {
			"THE COMMENT THIS CORRECTS. 0007 says hash/hash_alg are \"the content hash recorded FOR THIS COPY\"",
			"placements.hash has only ever held the ARTIFACT'S digest",
		},
		"core/internal/placement/engine.go": {
			"the hash it carries is the artifact's digest",
			"it is never written into Hash",
		},
	})
	// The sentence #991 replaced, as it read before the fix.
	if text := prose(t, root, "core/internal/placement/engine.go"); strings.Contains(text, "a copy is the same bytes under the same hash") {
		t.Errorf("core/internal/placement/engine.go says again that a copy is the same bytes under the same hash, which is false for an encrypted copy (NC-07)")
	}
}
