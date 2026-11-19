package main

import (
	"strings"
	"testing"
)

// Issue #1153: usage() names the key store verbs and the host-key probe, and
// the new passphrase flag on create and patch.
func TestUsage1153_NamesTheSSHVerbs(t *testing.T) {
	text := captureStderr(t, usage)
	for _, want := range []string{
		"  ssh-key list [--json]",
		"  ssh-key import --file F [--passphrase-stdin] | --candidate ID",
		"  ssh-key discover [--json]",
		"  ssh host-key-probe HOST [--port N] [--json]",
		"--ssh-key-passphrase-stdin",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("usage() does not contain %q", want)
		}
	}
	block := usageBlock(t, text, "  ssh-key import --file F", "  ssh host-key-probe", "  ssh-key discover")
	for _, say := range []string{"stdin", "never"} {
		if !strings.Contains(block, say) {
			t.Errorf("the import entry does not say %q (secrets are stdin only and never printed):\n%s", say, block)
		}
	}
}
