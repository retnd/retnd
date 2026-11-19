package packaging

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Q2.4 (#987) reversed the "no download" promise: for as long as this product
// had documents it told operators it would not do what EPIC Q does, and the
// replacement is one sentence, "this product recovers and exports the retained
// artifact; restoring it into an application remains the operator's
// responsibility".
//
// A reversal like that decays the way it was made: somebody copies a paragraph
// from an older page, or a rebase brings an old sentence back, and the product
// is once again promising a thing it no longer does. So the old sentences are
// held out of the documents that an operator reads. ADR 0029 and the
// CHANGELOG are exempt on purpose: they quote the retired sentences in order
// to say where they went, and a history that could not name what it replaced
// would be worse than the quotation.

// retiredBoundarySentences are the promises that were reversed, each as the
// shape the documents used (whitespace in a source line may fall anywhere, so
// the check collapses it first).
var retiredBoundarySentences = []struct {
	name string
	re   *regexp.Regexp
}{
	{"restoring is not an automated command", regexp.MustCompile(`(?i)not an automated command`)},
	{"the permanent shape rather than a workaround", regexp.MustCompile(`(?i)permanent shape rather than a workaround`)},
	{"there is no restore here and no download", regexp.MustCompile(`(?i)there is no restore here\b.{0,20}\bno download`)},
	{"no download and no restore button", regexp.MustCompile(`(?i)\bno download and no restore button`)},
	{"there is no restore execution", regexp.MustCompile(`(?i)there is no restore execution`)},
	{"the product offers no download", regexp.MustCompile(`(?i)\bthis (?:product|application|manager) (?:has|offers) no download\b`)},
}

// exemptFromBoundaryCheck are the places that quote the old sentences on
// purpose.
func exemptFromBoundaryCheck(rel string) bool {
	switch rel {
	case "CHANGELOG.md", "docs/adr/0029-the-recovery-boundary.md":
		return true
	}
	// Other ADRs and the epic's own specification are history and design
	// record, not what an operator is told to rely on.
	return strings.HasPrefix(rel, "docs/adr/") || strings.HasPrefix(rel, "docs/EPIC-") || strings.HasPrefix(rel, "docs/design/")
}

func retiredSentencesIn(text string) []string {
	flat := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(text, " ")), " ")
	flat = strings.NewReplacer("&rsquo;", "'", "&nbsp;", " ").Replace(flat)
	var found []string
	for _, s := range retiredBoundarySentences {
		if s.re.MatchString(flat) {
			found = append(found, s.name)
		}
	}
	return found
}

func TestTheOldNoDownloadSentencesAreGoneFromTheDocuments(t *testing.T) {
	roots := []string{"README.md", "docs"}
	checked := 0
	for _, root := range roots {
		start := Path(root)
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(p) {
			case ".md", ".html":
			default:
				return nil
			}
			rel, _ := filepath.Rel(RepoRoot, p)
			rel = filepath.ToSlash(rel)
			if exemptFromBoundaryCheck(rel) {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			checked++
			for _, name := range retiredSentencesIn(string(raw)) {
				t.Errorf("%s still says %q, a promise Q2.4 (#987) reversed. The boundary is: this product recovers and exports the retained artifact; restoring it into an application remains the operator's responsibility", rel, name)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	// A walk that found nothing would pass for the wrong reason.
	if checked < 20 {
		t.Fatalf("read only %d documents; the check is not looking where the sentences were", checked)
	}
}

// The positive control: the matcher has to fire on the sentences as the old
// documents actually spelled them, or the test above proves nothing.
func TestTheRetiredSentenceMatcherCanFail(t *testing.T) {
	old := map[string]string{
		"restoring is not an automated command":        "**Restoring an artifact is\nnot an automated command.**",
		"the permanent shape rather than a workaround": "the permanent shape rather\nthan a workaround",
		"there is no restore here and no download":     "<p>There is no restore here and there is no download. The footer</p>",
		"no download and no restore button":            "<p>no download and no restore button to be missing</p>",
		"there is no restore execution":                "<strong>There is no restore execution.</strong>",
	}
	for name, sample := range old {
		found := retiredSentencesIn(sample)
		if len(found) == 0 {
			t.Errorf("%s: the matcher missed %q, so the test could not have caught it", name, sample)
		}
	}
	if got := retiredSentencesIn("This product recovers and exports the retained artifact; restoring it into an application remains the operator's responsibility."); len(got) != 0 {
		t.Errorf("the replacement sentence matched as retired: %v", got)
	}
}
