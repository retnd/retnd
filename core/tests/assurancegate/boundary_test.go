package assurancegate_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The no-download promise (docs/recovery.md said "restoring an artifact is not
// an automated command", the reference page said "there is no restore here and
// there is no download") was made independently in several places, so it is
// reversed independently in several places, and a fix that edited one of them
// would leave the rest still promising the old thing. The check is the one the
// epic's checklist asks for (section 9, the #788 rule): ask where the boundary
// sentence has to be, and ask where the deleted claim still is, by its words.
//
// The sentence is held as text rather than as a hash so that a reworded
// sentence is a visible diff in this file, not an invisible one.

// The sentence is two clauses and a place may join them with a semicolon (the
// documents) or a comma (the store listing's bullet), and the store listing
// addresses the reader ("your responsibility") where the documents say "the
// operator's". So both clauses have to be there, in order, rather than one
// exact string.
var boundaryClauses = []*regexp.Regexp{
	regexp.MustCompile(`recovers and exports the retained artifact`),
	regexp.MustCompile(`restoring it into an application remains (the operator's|your) responsibility`),
}

// hasBoundary reports whether the text carries both clauses, first before second.
func hasBoundary(flat string) bool {
	at := 0
	for _, c := range boundaryClauses {
		loc := c.FindStringIndex(flat[at:])
		if loc == nil {
			return false
		}
		at += loc[1]
	}

	return true
}

// boundaryPlaces are the documents that have to carry the sentence: every
// place the old claim was made, found by what the claim was split into.
var boundaryPlaces = []string{
	"docs/recovery.md",
	"docs/recovery-without-a-terminal.md",
	"docs/incremental-runbooks.md",
	"docs/site/reference.html",
	"docs/site/index.html",
	"docs/submission/description.md",
}

// goneClaims are the words of the deleted promise. None of them may survive
// in a document a reader is meant to believe.
var goneClaims = []string{
	"restoring an artifact is not an automated command",
	"there is no restore here and there is no download",
	"there is no restore and there is no download",
}

// quoted holds the documents that mention the old words on purpose: the ADR
// that records the reversal, the epic's own specification and this matrix.
func quoted(rel string) bool {
	return strings.HasPrefix(rel, "docs/adr/") ||
		strings.HasPrefix(rel, "docs/EPIC") ||
		strings.HasPrefix(rel, "docs/conformance/") ||
		strings.HasPrefix(rel, "docs/design/") ||
		strings.HasPrefix(rel, "docs/prs/") ||
		strings.HasPrefix(rel, "docs/assets/")
}

// flatten makes prose comparable: whitespace collapsed, HTML apostrophes and
// emphasis markers removed, lower case.
func flatten(s string) string {
	for _, p := range [][2]string{{"&rsquo;", "'"}, {"’", "'"}, {"&#39;", "'"}, {"**", ""}, {"<strong>", ""}, {"</strong>", ""}} {
		s = strings.ReplaceAll(s, p[0], p[1])
	}

	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// checkBoundary returns one finding per place the reversal is incomplete. read
// returns a document's text; docs lists every document to scan for the old words.
func checkBoundary(read func(rel string) (string, bool), docs []string) []string {
	var out []string
	for _, rel := range boundaryPlaces {
		text, ok := read(rel)
		if !ok {
			out = append(out, rel+" is where the boundary sentence has to be, and it cannot be read")

			continue
		}
		if !hasBoundary(flatten(text)) {
			out = append(out, rel+" does not carry the boundary sentence, so it still says whatever it said before")
		}
	}
	for _, rel := range docs {
		if quoted(rel) {
			continue
		}
		text, ok := read(rel)
		if !ok {
			continue
		}
		flat := flatten(text)
		for _, gone := range goneClaims {
			if strings.Contains(flat, gone) {
				out = append(out, rel+" still makes the deleted promise: \""+gone+"\"")
			}
		}
	}

	return out
}

func TestTheBoundarySentenceIsEverywhereTheOldPromiseWasMadeAndThePromiseIsGone(t *testing.T) {
	root := repoRoot(t)
	var docs []string
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return fs.SkipDir
			}

			return nil
		}
		if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".html") {
			rel, _ := filepath.Rel(root, path)
			docs = append(docs, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking docs: %v", err)
	}
	docs = append(docs, "README.md")
	if len(docs) < 20 {
		t.Fatalf("found only %d documents to scan; the walk is not seeing the docs tree", len(docs))
	}

	read := func(rel string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(root, rel))

		return string(b), err == nil
	}
	for _, f := range checkBoundary(read, docs) {
		t.Error(f)
	}
}

func TestTheBoundaryCheckCanFail(t *testing.T) {
	good := map[string]string{}
	for _, rel := range boundaryPlaces {
		good[rel] = "<p>This product recovers and exports the retained artifact; restoring it into an application remains the operator&rsquo;s responsibility.</p>"
	}
	reader := func(m map[string]string) func(string) (string, bool) {
		return func(rel string) (string, bool) { s, ok := m[rel]; return s, ok }
	}
	docs := append(append([]string{}, boundaryPlaces...), "docs/other.md")

	if got := checkBoundary(reader(good), docs); len(got) != 0 {
		t.Fatalf("a complete reversal was refused: %v", got)
	}

	missing := map[string]string{}
	for k, v := range good {
		missing[k] = v
	}
	missing["docs/site/index.html"] = "<p>Nothing to see.</p>"
	if got := checkBoundary(reader(missing), docs); len(got) != 1 || !strings.Contains(got[0], "docs/site/index.html") {
		t.Fatalf("a place missing the sentence was not named: %v", got)
	}

	old := map[string]string{"docs/other.md": "Restoring an artifact is **not an automated command**.", "docs/recovery.md": good["docs/recovery.md"]}
	for k, v := range good {
		if _, ok := old[k]; !ok {
			old[k] = v
		}
	}
	old["docs/other.md"] = "What has not changed: restoring an artifact is not an automated command."
	got := strings.Join(checkBoundary(reader(old), docs), "\n")
	if !strings.Contains(got, "docs/other.md still makes the deleted promise") {
		t.Fatalf("the old promise surviving in another document was not named: %v", got)
	}

	if got := checkBoundary(reader(map[string]string{}), docs); len(got) < len(boundaryPlaces) {
		t.Fatalf("unreadable places were not reported: %v", got)
	}
}
