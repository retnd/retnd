package transport_test

import (
	"testing"

	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1541 fixed a source file that was not readable yet by making
// discovery wait for it, and left this category alone on purpose. A 403 from
// a bucket or a directory owned by another uid is a fixed property of the
// request (FR-22), and every consumer of the category relies on that. This
// pins it, so the next fix for a publish race cannot be "make it retryable".
func TestPermissionDeniedIsNotRetryable_1541(t *testing.T) {
	if transport.PermissionDenied.Retryable() {
		t.Error("transport.PermissionDenied reports itself retryable; FR-22 says a denied request is a fixed property of the request, and #1541 is solved at discovery instead")
	}
}
