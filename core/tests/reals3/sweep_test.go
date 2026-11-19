package reals3_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/retnd/retnd/core/tests/reals3"
)

// TestSweep is the driver's teardown trap: it removes whatever a run left
// behind, then lists the retnd-test- buckets that remain. It only runs when
// RETND_TEST_S3_SWEEP names a run id.
func TestSweep(t *testing.T) {
	run := os.Getenv(reals3.SweepVar)
	if run == "" {
		t.Skip("set " + reals3.SweepVar + "=<runid> to sweep a run's buckets")
	}
	e := reals3.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	removed, err := e.Sweep(ctx, run)
	t.Logf("swept run %s: removed %v", run, removed)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	left, err := e.ListTestBuckets(ctx)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	t.Logf("retnd-test- buckets in the account now: %v", left)
}
