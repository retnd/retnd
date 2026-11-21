package main

import (
	"strings"
	"testing"
)

// Issue #1594: `retnd retire --accept-unverified-copy` is the terminal's way to
// retire a FAILED backup whose own copy at its final name could not be
// verified once its source is gone. The flag has to exist (asked for with no
// artifact, the command gets as far as naming the missing operand rather than
// refusing the flag), and the refusal that names the acknowledgement has to
// name this flag.
func TestRetireTakesTheUnverifiedCopyAcknowledgement_1594(t *testing.T) {
	run := commands["retire"]
	var code int
	stderr := captureStderr(t, func() {
		code = run([]string{"--config", "/nonexistent/no-such-config.yaml", "--accept-unverified-copy"})
	})
	if strings.Contains(stderr, "not defined") || !strings.Contains(stderr, "expected <source/backup-set/artifact>") {
		t.Errorf("retire --accept-unverified-copy: exit %d, stderr %q; want the flag accepted and the operand named", code, stderr)
	}
	if got := flagFor("accept_unverified_copy"); got != "accept-unverified-copy" {
		t.Errorf("flagFor(accept_unverified_copy) = %q, want accept-unverified-copy", got)
	}
}
