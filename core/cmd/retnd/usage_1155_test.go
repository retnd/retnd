package main

import (
	"strings"
	"testing"
)

// Issue #1155: usage() names the verbs the terminal now has for the one
// administrator's login, and says plainly that every secret is stdin only.
func TestUsage1155_NamesTheAuthVerbs(t *testing.T) {
	text := captureStderr(t, usage)
	for _, want := range []string{
		"  auth password --password-stdin",
		"  auth recovery show",
		"  auth recovery set --current-password-stdin",
		"  auth recovery test",
		"  auth verify-email",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("usage() does not list %q", want)
		}
	}
	block := usageBlock(t, text, "  auth password --password-stdin", "  catalog ", "  quarantine ")
	if !strings.Contains(block, "stdin") || !strings.Contains(block, "set-password") {
		t.Errorf("the auth entry does not say secrets are stdin only, or name the offline way, retnd-web auth set-password:\n%s", block)
	}
}
