package revalidate

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q panel 2 (W-07), QC-04 on #978: the work list and every verdict come
// from the journal's placements. What a destination happens to hold at some
// other key is never consulted, so it can neither stand in for a missing copy
// nor be reported as one.

// keyedObjectStore holds objects by key and answers a stat the way a bucket
// does, including remembering every key it was asked about.
type keyedObjectStore struct {
	mu      sync.Mutex
	objects map[string]int64
	asked   []string
}

func (s *keyedObjectStore) StatObject(_ context.Context, _ transport.Medium, key string) (transport.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, key)
	n, ok := s.objects[key]
	if !ok {
		return transport.ObjectInfo{}, transport.NewError(transport.NotFound, "stat_object", errors.New("object not found"))
	}
	return transport.ObjectInfo{Key: key, Size: n}, nil
}
func (s *keyedObjectStore) OpenObject(context.Context, transport.Medium, string) (io.ReadCloser, error) {
	return nil, errors.New("an automatic pass must not download")
}
func (s *keyedObjectStore) ObjectChecksum(context.Context, transport.Medium, string, transport.HashAlgorithm) (transport.ChecksumAttestation, error) {
	return transport.ChecksumAttestation{}, transport.NewError(transport.UnsupportedCapability, "object_checksum", errors.New("md5 only"))
}

func TestQC04_AnObjectAtAKeyTheJournalDoesNotKnowIsNotAVerifiedCopy(t *testing.T) {
	ctx := context.Background()
	content := []byte("the bytes that moved")
	j, a := onMedium(t, "moved.dump", content)
	known := mediumCopy(t, j, a).Location
	store := &keyedObjectStore{objects: map[string]int64{
		"retnd/stray/" + a.Name: int64(len(content)), // same name and size, key the journal never wrote
		"retnd/other.bin":       7,
	}}

	rep, err := Run(ctx, Deps{Journal: j, Store: store, Mediums: fixedMediums{id: "offsite"}}, a.Set, scrubCfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Findings) != 1 || rep.Findings[0].Passed {
		t.Fatalf("Findings = %+v: an object at an unknown key was reported as a verified copy", rep.Findings)
	}
	for _, k := range store.asked {
		if k != known {
			t.Errorf("the pass asked the medium about %q; the only key the journal holds is %q", k, known)
		}
	}
	if p := mediumCopy(t, j, a); p.AssuranceState == state.AssurancePassed {
		t.Errorf("the journal's copy at %q is recorded as passed though nothing is there", p.Location)
	}
}

func TestQC04_AnExpectedButAbsentCopyIsAFailureTheReportCounts(t *testing.T) {
	ctx := context.Background()
	j, a := onMedium(t, "vanished.dump", []byte("gone"))
	store := &keyedObjectStore{objects: map[string]int64{}}

	rep, err := Run(ctx, Deps{Journal: j, Store: store, Mediums: fixedMediums{id: "offsite"}}, a.Set, scrubCfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	failed := 0
	for _, f := range rep.Findings {
		for _, c := range f.Copies {
			if c.Verdict == state.ScrubFailed {
				failed++
			}
		}
	}
	if failed != 1 {
		t.Errorf("failed copies in the report = %d, want 1 (report %+v)", failed, rep)
	}
	if p := mediumCopy(t, j, a); p.AssuranceState != state.AssuranceFailed || p.ConsecutiveFailures != 1 {
		t.Errorf("the journal records %q / %d, want failed / 1", p.AssuranceState, p.ConsecutiveFailures)
	}
}
