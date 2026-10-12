package packaging

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Panel finding C7 on retnd#1050: since retnd#1049 `retnd status` exits 0 on
// a fresh install, but three places still told the reader it exits non-zero
// there. The words have to match the behaviour, and the behaviour that does
// exit non-zero in that neighbourhood is a configuration that is missing
// over a journal that holds backups.

var (
	statusFreshInstallClaim = regexp.MustCompile(`(?is)status[^.]{0,300}non-zero[^.]{0,300}(fresh install|no configuration at all)`)
	statusJournalClaim      = regexp.MustCompile(`(?is)status[^.]{0,400}non-zero[^.]{0,400}journal`)
)

// flatten joins the array-of-lines JSON notes and the hard-wrapped markdown
// into running prose, so a sentence is one string whatever the file wraps at.
func flatten(s string) string {
	s = strings.ReplaceAll(s, "\\n", " ")
	s = regexp.MustCompile(`",\s*"`).ReplaceAllString(s, " ")
	return regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
}

func TestRuntimeContractSaysWhatStatusDoes(t *testing.T) {
	for _, rel := range []string{
		"canonical.json",
		"../compose/runtime-contract.json",
		"../../docs/deployment.md",
	} {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			raw, err := os.ReadFile(rel)
			if err != nil {
				t.Fatal(err)
			}
			text := flatten(string(raw))
			if m := statusFreshInstallClaim.FindString(text); m != "" {
				t.Errorf("%s still says `status` exits non-zero on a fresh install, which it no longer does:\n%s", rel, m)
			}
			if !statusJournalClaim.MatchString(text) {
				t.Errorf("%s does not say that `status` exits non-zero when the configuration is missing over a journal that holds backups", rel)
			}
		})
	}
}
