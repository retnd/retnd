package encryptiongate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EPIC N's N2.8 (#997, held by a test on the release PR #1331 instead of a
// review): the compliance and submission pages say what retained-copy
// encryption changes about what is stored and that a key bundle is exported,
// and no store-screenshot row reads as done, because EPIC N and EPIC T changed
// the Settings and backup-set screens and no screenshot of them exists.
func TestTheSubmissionAndCompliancePagesSayWhatEncryptionChanged_1331(t *testing.T) {
	root := repoRoot(t)
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	for rel, phrases := range map[string][]string{
		"docs/compliance/privacy-policy.md":     {"Encrypted retained copies", "changes shape", "recovery bundle"},
		"docs/submission/privacy-disclosure.md": {"retained-copy encryption", "recovery bundle"},
		"docs/submission/screenshots.md":        {"## Encrypted retained copies", "PENDING_OPERATOR", "Suite select"},
	} {
		text := read(rel)
		for _, p := range phrases {
			if !strings.Contains(text, p) {
				t.Errorf("%s does not say %q", rel, p)
			}
		}
	}

	// Every Store screenshots row in the preflight ledger is outstanding or
	// not applicable: a cell that reads PASS or READY would be a changed screen
	// recorded as done.
	rows := 0
	for _, line := range strings.Split(read("docs/conformance/submission-preflight.md"), "\n") {
		if !strings.HasPrefix(line, "| Store screenshots |") {
			continue
		}
		rows++
		cells := strings.Split(strings.Trim(line, "| "), "|")
		// The summary table has one outcome per target; a per-target table
		// has the outcome and then the reason for it.
		outcomes := cells[1:]
		if len(cells) == 3 {
			outcomes = cells[1:2]
		}
		for _, c := range outcomes {
			c = strings.TrimSpace(c)
			if !strings.HasPrefix(c, "N/A") && !strings.HasPrefix(c, "OPERATOR") {
				t.Errorf("a Store screenshots row has the cell %.60q, but no screenshot of the screens EPIC N and EPIC T changed exists, so it is outstanding or not applicable:\n  %.160s", c, line)
			}
		}
	}
	if rows == 0 {
		t.Fatal("no Store screenshots row in docs/conformance/submission-preflight.md, so this read nothing")
	}
}
