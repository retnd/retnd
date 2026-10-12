package revalidate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q panel 2 (W-07b), QC-04 on #978, the listing half: a pass asks each
// destination what it holds under the set's namespace and compares that with
// the journal. An object at a key the journal does not know is a finding in
// the report. It is never a verified copy and it is never touched.

// prefixedMediums resolves one medium whose Prefix is the "retnd" the fixture
// keys start with, so the set's namespace is retnd/production/postgres-primary.
type prefixedMediums struct{ id string }

func (m prefixedMediums) MediumFor(id string) (transport.Medium, bool) {
	if id != m.id {
		return transport.Medium{}, false
	}
	return transport.Medium{ID: id, Type: transport.MediumTypeS3, Bucket: "nas-backups", Prefix: "retnd"}, true
}

const setNamespace = "retnd/production/postgres-primary"

// listingStore is a keyedObjectStore that can also list, the way a bucket
// does: only keys under the prefix, in key order.
type listingStore struct {
	*keyedObjectStore
	listErr error
	listed  []string
}

func (s *listingStore) ListObjects(_ context.Context, _ transport.Medium, prefix string) ([]transport.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listed = append(s.listed, prefix)
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []transport.ObjectInfo
	for k, n := range s.objects {
		if strings.HasPrefix(k, prefix+"/") {
			out = append(out, transport.ObjectInfo{Key: k, Size: n})
		}
	}
	return out, nil
}

func runListing(t *testing.T, j *state.Journal, a model.ArtifactID, store placement.Store) Report {
	t.Helper()
	rep, err := Run(context.Background(), Deps{Journal: j, Store: store, Mediums: prefixedMediums{id: "offsite"}}, a.Set, scrubCfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

func unknownKeys(rep Report) []string {
	var out []string
	for _, u := range rep.Unknown {
		out = append(out, u.Medium+"|"+u.Key)
	}
	return out
}

func TestQC04Listing_AnObjectAtAnUnknownKeyIsAFindingAndTheKnownCopyStillPasses(t *testing.T) {
	content := []byte("the bytes that moved")
	j, a := onMedium(t, "kept.dump", content)
	known := mediumCopy(t, j, a).Location
	store := &listingStore{keyedObjectStore: &keyedObjectStore{objects: map[string]int64{
		known:                             int64(len(content)),
		setNamespace + "/stray.bin":       9,
		"retnd/production/other-set/x.db": 5, // another set's namespace: not this pass's business
	}}}

	rep := runListing(t, j, a, store)

	if got := unknownKeys(rep); len(got) != 1 || got[0] != "offsite|"+setNamespace+"/stray.bin" {
		t.Fatalf("Unknown = %v, want exactly the stray object under this set's namespace", got)
	}
	if rep.Unknown[0].Size != 9 {
		t.Errorf("the finding carries size %d, want 9", rep.Unknown[0].Size)
	}
	if len(rep.Findings) != 1 || !rep.Findings[0].Passed {
		t.Errorf("Findings = %+v: the journal's own copy is present and must still pass", rep.Findings)
	}
	if len(store.listed) != 1 || store.listed[0] != setNamespace {
		t.Errorf("listed prefixes = %v, want one listing of %q", store.listed, setNamespace)
	}
	for _, k := range store.asked {
		if k != known {
			t.Errorf("the pass statted %q, a key the journal does not hold", k)
		}
	}
}

func TestQC04Listing_AnObjectAtAnUnknownKeyCannotStandInForTheMissingCopy(t *testing.T) {
	content := []byte("gone")
	j, a := onMedium(t, "vanished.dump", content)
	store := &listingStore{keyedObjectStore: &keyedObjectStore{objects: map[string]int64{
		setNamespace + "/stray/vanished.dump": int64(len(content)), // right name and size, wrong key
	}}}

	rep := runListing(t, j, a, store)

	if len(rep.Unknown) != 1 {
		t.Fatalf("Unknown = %v, want the stray object reported", unknownKeys(rep))
	}
	failed := 0
	for _, f := range rep.Findings {
		if f.Passed {
			t.Errorf("Finding %+v passed on the strength of an object at an unknown key", f)
		}
		for _, c := range f.Copies {
			if c.Verdict == state.ScrubFailed {
				failed++
			}
		}
	}
	if failed != 1 {
		t.Errorf("failed copies = %d, want 1: the expected copy is absent and that is counted", failed)
	}
	if p := mediumCopy(t, j, a); p.AssuranceState != state.AssuranceFailed {
		t.Errorf("the journal records %q for the absent copy, want failed", p.AssuranceState)
	}
}

func TestQC04Listing_ACleanDestinationReportsNothingUnknownAndNothingUnlisted(t *testing.T) {
	content := []byte("tidy")
	j, a := onMedium(t, "tidy.dump", content)
	known := mediumCopy(t, j, a).Location
	rep := runListing(t, j, a, &listingStore{keyedObjectStore: &keyedObjectStore{objects: map[string]int64{known: int64(len(content))}}})
	if len(rep.Unknown) != 0 || len(rep.Unlisted) != 0 {
		t.Errorf("Unknown = %v, Unlisted = %v, want neither", unknownKeys(rep), rep.Unlisted)
	}
}

func TestQC04Listing_ADestinationThatCannotBeListedIsSaidSoAndIsNotAFailure(t *testing.T) {
	content := []byte("quiet")
	j, a := onMedium(t, "quiet.dump", content)
	known := mediumCopy(t, j, a).Location
	store := &listingStore{
		keyedObjectStore: &keyedObjectStore{objects: map[string]int64{known: int64(len(content))}},
		listErr:          transport.NewError(transport.Transient, "list_objects", errors.New("dial tcp: i/o timeout")),
	}

	rep := runListing(t, j, a, store)

	if len(rep.Unlisted) != 1 || rep.Unlisted[0].Medium != "offsite" || !strings.Contains(rep.Unlisted[0].Reason, "i/o timeout") {
		t.Errorf("Unlisted = %+v, want offsite with the listing error as its reason", rep.Unlisted)
	}
	if len(rep.Unknown) != 0 {
		t.Errorf("Unknown = %v: a listing that failed proves nothing about what is there", unknownKeys(rep))
	}
	if len(rep.Findings) != 1 || !rep.Findings[0].Passed || len(rep.Errors) != 0 {
		t.Errorf("a failed listing must not fail the copy checks: findings %+v errors %+v", rep.Findings, rep.Errors)
	}
}

func TestQC04Listing_AStoreWithNoListingCapabilityIsSaidSo(t *testing.T) {
	content := []byte("nolist")
	j, a := onMedium(t, "nolist.dump", content)
	known := mediumCopy(t, j, a).Location
	rep := runListing(t, j, a, &keyedObjectStore{objects: map[string]int64{known: int64(len(content))}})
	if len(rep.Unlisted) != 1 || rep.Unlisted[0].Medium != "offsite" {
		t.Errorf("Unlisted = %+v, want offsite named as not listed", rep.Unlisted)
	}
}

func TestQC04Listing_ANarrowedPassDoesNotListTheDestination(t *testing.T) {
	content := []byte("narrow")
	j, a := onMedium(t, "narrow.dump", content)
	known := mediumCopy(t, j, a).Location
	store := &listingStore{keyedObjectStore: &keyedObjectStore{objects: map[string]int64{known: int64(len(content)), setNamespace + "/stray.bin": 1}}}
	rep, err := RunWith(context.Background(), Deps{Journal: j, Store: store, Mediums: prefixedMediums{id: "offsite"}}, a.Set, scrubCfg,
		Options{Force: true, Only: &a})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if len(store.listed) != 0 || len(rep.Unknown) != 0 {
		t.Errorf("a pass narrowed to one artifact listed %v and found %v; the set-wide comparison belongs to a set-wide pass", store.listed, unknownKeys(rep))
	}
}
