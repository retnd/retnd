//go:build unix

package revalidate

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Panel finding C11 for the scrub's own reader: recomputeLocalHash opened the
// copy's path with a plain os.Open, so a fifo placed there blocked the call
// (and the lease, the read slot and the whole tick) for ever.
func TestRecomputeLocalHashOnAFifoReturnsInsteadOfBlocking_C11(t *testing.T) {
	p := filepath.Join(t.TempDir(), "artifact.dump")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("no fifos here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := recomputeLocalHash(context.Background(), p)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a fifo hashed without error")
		}
	case <-time.After(2 * time.Second):
		// The goroutine stays blocked; the test binary exits regardless.
		t.Error("recomputeLocalHash is still blocked on a fifo after 2s")
	}
}
