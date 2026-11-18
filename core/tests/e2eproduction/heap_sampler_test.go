package e2eproduction_test

import (
	"runtime"
	"runtime/debug"
	"testing"
)

// The scale budgets defend "memory does not scale with the source", which
// is a claim about the heap the engine HOLDS, not about how much garbage
// the collector has not got round to yet. A budget read off HeapInuse moves
// with GC pacing and with how loaded the machine is (the deep-tree shape,
// 128 entries and half a megabyte, read 125 MB on an idle run and 325 MB
// under the full gate, against a 256 MiB budget), so the gate failed on
// timing and said nothing about the source. See #1278.

// TestTheHeapSamplerIgnoresGarbageTheCollectorHasNotCollected pins that
// the sampler the scale shapes use counts live bytes only. With the
// collector off, 512 MiB of dead buffers stay in HeapInuse, and only
// the last 8 MiB buffer is still referenced.
func TestTheHeapSamplerIgnoresGarbageTheCollectorHasNotCollected(t *testing.T) {
	old := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(old) })

	// Start from a collected heap, so what earlier tests left behind is
	// not counted as live here.
	runtime.GC()

	peak := newHeapSampler()

	var sink []byte

	for range 64 {
		sink = make([]byte, 8<<20)
		for i := 0; i < len(sink); i += 4096 {
			sink[i] = 1
		}
	}

	// stop() takes one last sample, so no sleep is needed to let the
	// sampler see the garbage.
	got := peak.stop()

	runtime.KeepAlive(sink)

	const limit = 256 << 20

	if got > limit {
		t.Fatalf("the sampler read %d bytes with about 8 MiB live and 512 MiB of uncollected garbage; it must count what is live, not what is merely not yet freed (limit %d)", got, uint64(limit))
	}
}

// TestTheHeapSamplerStillSeesWhatIsHeld is the control: a sampler that
// always reads zero would pass the test above, so a buffer that really is
// held must still show.
func TestTheHeapSamplerStillSeesWhatIsHeld(t *testing.T) {
	runtime.GC()

	peak := newHeapSampler()

	held := make([]byte, 192<<20)
	for i := 0; i < len(held); i += 4096 {
		held[i] = 1
	}

	// The live figure only moves when a collection finishes, so finish
	// one while the buffer is referenced.
	runtime.GC()

	got := peak.stop()

	runtime.KeepAlive(held)

	if got < 150<<20 {
		t.Fatalf("the sampler read %d bytes with 192 MiB held live; it must still see a real allocation", got)
	}
}
