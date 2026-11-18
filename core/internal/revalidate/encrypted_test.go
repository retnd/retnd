package revalidate

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC N, #992 (N1.9 and the "could not check" half of N1.4): revalidation
// over an encrypted medium copy. The placements are written through the
// journal for the reason medium_test.go gives, with a real per-copy
// encryption record, so the stored size and the plaintext size differ the way
// they do in production.
//
// Written by reading; not run.

// countingKeys is custody that counts how often anything asked it for key
// material, and refuses when told the key is gone.
type countingKeys struct {
	asked atomic.Int64
	gone  bool
}

func (k *countingKeys) Active(context.Context) (keycustody.KeyRecord, bool, error) {
	k.asked.Add(1)
	return keycustody.KeyRecord{}, false, nil
}

func (k *countingKeys) WithKey(context.Context, string, func([]byte) error) error {
	k.asked.Add(1)
	return keycustody.ErrUnknownKey
}

const storedExtra = 300

// encryptedOnMedium leaves the artifact with one ACTIVE encrypted copy on
// offsite_s3 and returns its stored size.
func encryptedOnMedium(t *testing.T, j *state.Journal, artifact model.ArtifactID, content []byte) int64 {
	t.Helper()
	ctx := context.Background()
	long := time.Now().UTC().Add(-90 * 24 * time.Hour)
	completeArtifact(t, j, artifact, content, long)
	moveToMedium(t, j, artifact, "offsite_s3", content, long)
	if err := j.RegisterEncryptionKey(ctx, state.EncryptionKey{ID: "k0123", Provider: state.KeyProviderManaged, Recipient: "age1testrecipient", CreatedAt: long}); err != nil {
		t.Fatalf("RegisterEncryptionKey: %v", err)
	}
	stored := int64(len(content)) + storedExtra
	if err := j.RecordPlacementEncryption(ctx, artifact, "offsite_s3", state.PlacementEncryption{
		FormatID: "age/v1", FormatVersion: 1, KeyID: "k0123",
		StoredHash: sha256Hex([]byte("the ciphertext, not the artifact")), StoredHashAlg: "sha256", StoredSize: stored,
	}); err != nil {
		t.Fatalf("RecordPlacementEncryption: %v", err)
	}
	return stored
}

func TestTheAutomaticCeilingIsStillExistenceAndAnUnattendedPassNeverDecryptsAnEncryptedCopy_992(t *testing.T) {
	if automaticMediumClass != placement.Existence {
		t.Fatalf("automaticMediumClass = %q, want %q: a decrypt-verify is a Content check and unattended egress is not a default", automaticMediumClass, placement.Existence)
	}

	ctx := context.Background()
	j := openJournal(t)
	artifact := artifactNamed(t, "enc-on-medium.dump")
	content := []byte("bytes that live in a bucket as ciphertext")
	stored := encryptedOnMedium(t, j, artifact, content)

	store := &countingStore{size: stored}
	keys := &countingKeys{}
	deps := Deps{Journal: j, Store: store, Mediums: fixedMediums{id: "offsite_s3"}, Keys: keys}
	cfg := config.Revalidation{Interval: config.Duration(24 * time.Hour), MaxPerCycle: 10, Hash: true}

	report, err := Run(ctx, deps, artifact.Set, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("Findings = %+v (errors %+v), want exactly one", report.Findings, report.Errors)
	}
	f := report.Findings[0]
	if !f.Checked || !f.Passed || f.Class != placement.Existence {
		t.Errorf("an intact encrypted copy at its stored size: checked=%v passed=%v class=%q reason=%q; want a checked existence pass (the plaintext size is not the object's size)",
			f.Checked, f.Passed, f.Class, f.Reason)
	}
	if store.opens != 0 {
		t.Errorf("the unattended pass downloaded the object %d times", store.opens)
	}
	if keys.asked.Load() != 0 {
		t.Errorf("the unattended pass asked custody for a key %d times; it must perform no decrypt", keys.asked.Load())
	}
}

// A content revalidation with no way to resolve the key says it could not
// check. It does not fall back to a HEAD and call that a verdict, it does not
// read the object, and it does not quarantine a backup it never examined.
func TestAContentRevalidationWithNoResolverReportsCouldNotCheckNotAWeakerClass_992(t *testing.T) {
	ctx := context.Background()
	j := openJournal(t)
	artifact := artifactNamed(t, "enc-no-resolver.dump")
	content := []byte("bytes that cannot be decrypted by this deployment")
	stored := encryptedOnMedium(t, j, artifact, content)

	store := &countingStore{size: stored}
	deps := Deps{Journal: j, Store: store, Mediums: fixedMediums{id: "offsite_s3"}} // no Keys at all

	report, err := RunWith(ctx, deps, artifact.Set, scrubCfg, Options{Force: true, Only: &artifact, Content: true, AllowEgress: true})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	var copies []CopyResult
	for _, f := range report.Findings {
		if f.Checked && !f.Passed {
			t.Errorf("an unexaminable copy produced a failing finding: %+v", f)
		}
		copies = append(copies, f.Copies...)
	}
	for _, e := range report.Errors {
		copies = append(copies, e.Copies...)
	}
	if len(copies) != 1 {
		t.Fatalf("copy results = %+v, want exactly one", copies)
	}
	c := copies[0]
	if c.Verdict != state.ScrubCouldNotComplete || c.Class != "" {
		t.Errorf("verdict/class = %q/%q, want could_not_complete with no class: nothing weaker may stand in for the decrypt-verify", c.Verdict, c.Class)
	}
	if !strings.Contains(c.Reason, "could not check") || !strings.Contains(c.Reason, "k0123") {
		t.Errorf("the reason does not say it could not check and which key it needs: %q", c.Reason)
	}
	if store.stats != 0 || store.opens != 0 || store.digest != 0 {
		t.Errorf("requests were made in place of the check that could not run: %d HEAD, %d GET, %d attestations", store.stats, store.opens, store.digest)
	}
	rec, err := j.Get(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != "COMPLETE" {
		t.Errorf("the artifact left COMPLETE (now %s) for a check that never ran", rec.State)
	}
}

// With custody present but unable to open the recorded key the answer is the
// same, and custody is asked exactly once for the one copy.
func TestAContentRevalidationWithTheKeyGoneReportsCouldNotCheck_992(t *testing.T) {
	ctx := context.Background()
	j := openJournal(t)
	artifact := artifactNamed(t, "enc-key-gone.dump")
	stored := encryptedOnMedium(t, j, artifact, []byte("bytes whose key was lost"))

	keys := &countingKeys{gone: true}
	deps := Deps{Journal: j, Store: &countingStore{size: stored}, Mediums: fixedMediums{id: "offsite_s3"}, Keys: keys}
	report, err := RunWith(ctx, deps, artifact.Set, scrubCfg, Options{Force: true, Only: &artifact, Content: true, AllowEgress: true})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	var verdicts []state.ScrubVerdict
	for _, f := range report.Findings {
		for _, c := range f.Copies {
			verdicts = append(verdicts, c.Verdict)
		}
	}
	for _, e := range report.Errors {
		for _, c := range e.Copies {
			verdicts = append(verdicts, c.Verdict)
		}
	}
	if len(verdicts) != 1 || verdicts[0] != state.ScrubCouldNotComplete {
		t.Errorf("verdicts = %v, want one could_not_complete", verdicts)
	}
	if keys.asked.Load() != 1 {
		t.Errorf("custody was asked %d times, want 1", keys.asked.Load())
	}
}
