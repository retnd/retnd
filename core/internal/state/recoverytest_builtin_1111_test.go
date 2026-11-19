package state

import (
	"context"
	"strings"
	"testing"
	"time"
)

// #1111: the journal stores the built-in check's three verdict shapes with
// no change to what it validates, which is the reason no migration is spent.

const builtinID = "retnd-builtin-structure"

func TestIssue1111_TheJournalAcceptsTheBuiltInsPassFailureAndIncompleteShapes(t *testing.T) {
	ctx := context.Background()
	hash := strings.Repeat("cd", 32)
	for name, rt := range map[string]func(CopyRef) RecoveryTest{
		"pass": func(ref CopyRef) RecoveryTest {
			r := passingTest(ref)
			r.ValidatorID, r.ValidatorHash = builtinID, hash
			return r
		},
		"failure after a digest match": func(ref CopyRef) RecoveryTest {
			r := passingTest(ref)
			r.State, r.ValidatorID, r.ValidatorHash = RecoveryFailed, builtinID, hash
			r.Reason = "the recovered copy matched its recorded digest, so this is what was ingested, and it does not read as a complete gzip stream: unexpected EOF after 1.9 GiB of 2.3 GiB"
			return r
		},
		"could not complete (a bound was hit)": func(ref CopyRef) RecoveryTest {
			return RecoveryTest{Copy: ref, State: RecoveryCouldNotComplete, At: recAt, NextDue: recAt.Add(time.Hour), Reason: "the built-in check stopped at its 2h budget"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			j, ref := copyRef(t)
			if err := j.RecordRecoveryTest(ctx, rt(ref)); err != nil {
				t.Fatalf("RecordRecoveryTest: %v", err)
			}
			p := localCopy(t, j, ref.Artifact)
			want := builtinID
			if strings.HasPrefix(name, "could") {
				want = ""
			}
			if p.RecoveryValidatorID != want {
				t.Errorf("stored id = %q, want %q", p.RecoveryValidatorID, want)
			}
		})
	}
}

// The same journal still refuses a built-in pass that skipped the digest gate:
// the identity does not buy a way round it.
func TestIssue1111_ABuiltInPassStillNeedsTheDigestGate(t *testing.T) {
	j, ref := copyRef(t)
	r := passingTest(ref)
	r.ValidatorID, r.ValidatorHash, r.DigestMatched = builtinID, strings.Repeat("cd", 32), nil
	if err := j.RecordRecoveryTest(context.Background(), r); err == nil {
		t.Error("a built-in pass with no digest comparison was stored")
	}
}
