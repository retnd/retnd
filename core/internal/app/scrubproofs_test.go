package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q panel 2 (W-07), proofs on #978: the scrub_failure lines a pass
// writes are the failures it counted, and the scrub's work list and verdicts
// come from the journal and never from what a destination happens to hold.

// scrubFailureLines decodes the JSON log a recording logger wrote and returns
// the scrub_failure lines, in order.
func scrubFailureLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", raw, err)
		}
		if line["event"] == obs.EventScrubFailure {
			out = append(out, line)
		}
	}
	return out
}

// mediumOnlyArtifact seeds an artifact whose only ACTIVE copy is the one at
// key on the test medium, the shape a finished move leaves.
func mediumOnlyArtifact(t *testing.T, ctx context.Context, j *state.Journal, bs config.BackupSet, name, key string, size int64) model.ArtifactID {
	t.Helper()
	a := seedMovableArtifact(t, ctx, j, bs, name, retentionTestNow.Add(-48*time.Hour))
	if _, err := j.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "retire-local-" + name, From: "COMPLETE", To: "COMPLETE", OccurredAt: retentionTestNow,
		Placement: &state.PlacementUpdate{Medium: state.MediumLocal, Location: filepath.Join(bs.LocalPath, name), Status: state.PlacementGone},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "on-medium-" + name, From: "COMPLETE", To: "COMPLETE", OccurredAt: retentionTestNow,
		Placement: &state.PlacementUpdate{Medium: moveTestMedium, Location: key, Size: &size, Hash: "x", HashAlg: "sha256", Status: state.PlacementActive},
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

// keyedStore answers a stat only for keys it holds, the way a bucket does.
type keyedStore struct {
	transport.MediumStore
	mu      sync.Mutex
	objects map[string]int64
	stats   []string
	listed  []string
}

func (s *keyedStore) StatObject(_ context.Context, _ transport.Medium, key string) (transport.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats = append(s.stats, key)
	n, ok := s.objects[key]
	if !ok {
		return transport.ObjectInfo{}, transport.NewError(transport.NotFound, "stat_object", errors.New("object not found"))
	}
	return transport.ObjectInfo{Key: key, Size: n}, nil
}
func (s *keyedStore) OpenObject(context.Context, transport.Medium, string) (io.ReadCloser, error) {
	return nil, errors.New("a scrub must not download")
}

// ListObjects answers the way a bucket does: the keys under prefix.
func (s *keyedStore) ListObjects(_ context.Context, _ transport.Medium, prefix string) ([]transport.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listed = append(s.listed, prefix)
	return objectsUnder(s.objects, prefix), nil
}

func objectsUnder(objects map[string]int64, prefix string) []transport.ObjectInfo {
	var out []transport.ObjectInfo
	for k, n := range objects {
		if strings.HasPrefix(k, prefix+"/") {
			out = append(out, transport.ObjectInfo{Key: k, Size: n})
		}
	}
	return out
}

// Q1.18: one scrub_failure line per failure in the pass, no more and no
// fewer. The pass mixes every way a copy can be a failure: bytes that rotted,
// a copy that is gone from its medium, and a medium that did not answer.
func TestScrub_Q118_ScrubFailureLinesEqualTheFailuresOfThePass(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	var buf bytes.Buffer
	svc.Logger = obs.New(&buf, obs.LevelDebug)

	good := seedMovableArtifact(t, ctx, journal, bs, "good.dump", retentionTestNow.Add(-48*time.Hour))
	_ = good
	for _, n := range []string{"rot1.dump", "rot2.dump", "rot3.dump"} {
		seedMovableArtifact(t, ctx, journal, bs, n, retentionTestNow.Add(-48*time.Hour))
		if err := os.WriteFile(filepath.Join(bs.LocalPath, n), []byte("rotted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	size := int64(len("the bytes of gone.dump"))
	mediumOnlyArtifact(t, ctx, journal, bs, "gone.dump", "retnd/gone.dump", size)
	mediumOnlyArtifact(t, ctx, journal, bs, "silent.dump", "retnd/silent.dump", size)
	svc.MediumStore = &routedStore{
		objects: map[string]int64{},
		down:    map[string]bool{"retnd/silent.dump": true},
	}

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	failures := sum.Failed + sum.CouldNotComplete
	if failures != 5 {
		t.Fatalf("the fixture was meant to fail 3 rotted + 1 gone + 1 unreachable, summary = %+v", sum)
	}
	lines := scrubFailureLines(t, &buf)
	if len(lines) != failures {
		t.Errorf("the pass counted %d failures (%d failed, %d could not be checked) and wrote %d scrub_failure lines", failures, sum.Failed, sum.CouldNotComplete, len(lines))
	}
	seen := map[string]int{}
	for _, l := range lines {
		seen[l["artifact"].(string)+"@"+l["medium"].(string)]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s has %d scrub_failure lines in one pass, want exactly 1", k, n)
		}
	}
}

// routedStore is a keyedStore that can also refuse to answer for named keys.
type routedStore struct {
	transport.MediumStore
	objects map[string]int64
	down    map[string]bool
}

func (s *routedStore) StatObject(_ context.Context, _ transport.Medium, key string) (transport.ObjectInfo, error) {
	if s.down[key] {
		return transport.ObjectInfo{}, transport.NewError(transport.Transient, "stat", errors.New("dial tcp: i/o timeout"))
	}
	n, ok := s.objects[key]
	if !ok {
		return transport.ObjectInfo{}, transport.NewError(transport.NotFound, "stat_object", errors.New("object not found"))
	}
	return transport.ObjectInfo{Key: key, Size: n}, nil
}
func (s *routedStore) ListObjects(_ context.Context, _ transport.Medium, prefix string) ([]transport.ObjectInfo, error) {
	return objectsUnder(s.objects, prefix), nil
}
func (s *routedStore) OpenObject(context.Context, transport.Medium, string) (io.ReadCloser, error) {
	return nil, errors.New("a scrub must not download")
}

// QC-04, unknown-key half: an object sitting at a key the journal has no
// placement for is not a copy of anything. The scrub must not stat it, count
// it, or let it stand in for the copy the journal expects at another key.
func TestScrub_QC04_AnObjectAtAKeyTheJournalDoesNotKnowIsNotAVerifiedCopy(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	size := int64(len("the bytes of moved.dump"))
	a := mediumOnlyArtifact(t, ctx, journal, bs, "moved.dump", "retnd/moved.dump", size)
	store := &keyedStore{objects: map[string]int64{
		"retnd/stray/moved.dump": size, // right name and size, wrong key
		"retnd/unrelated.bin":    1,
	}}
	svc.MediumStore = store

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Passed != 0 {
		t.Errorf("Passed = %d: an object at an unknown key was counted as a verified copy", sum.Passed)
	}
	for _, k := range store.stats {
		if k != "retnd/moved.dump" {
			t.Errorf("the scrub asked the medium about %q, a key the journal does not hold", k)
		}
	}
	rec, _ := journal.Get(ctx, a)
	for _, p := range rec.ActiveMediumPlacements() {
		if p.AssuranceState == state.AssurancePassed {
			t.Errorf("the copy at %q is recorded as passed though the object there is absent", p.Location)
		}
	}
}

// QC-04, absent half: a copy the journal expects and the destination does
// not hold is a failure, and it is counted as one.
func TestScrub_QC04_AnExpectedButAbsentCopyIsACountedFailure(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	size := int64(len("the bytes of vanished.dump"))
	a := mediumOnlyArtifact(t, ctx, journal, bs, "vanished.dump", "retnd/vanished.dump", size)
	svc.MediumStore = &keyedStore{objects: map[string]int64{}}

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Failed != 1 || sum.Passed != 0 {
		t.Errorf("summary = %+v, want exactly the one absent copy counted as failed", sum)
	}
	rec, _ := journal.Get(ctx, a)
	if ps := rec.ActiveMediumPlacements(); len(ps) == 1 && ps[0].AssuranceState != state.AssuranceFailed {
		t.Errorf("the absent copy is recorded as %q, want failed", ps[0].AssuranceState)
	}
}

// Q1.18, the two-copy shape: an artifact with a copy that is provably gone
// and another that did not answer. The journal records BOTH copies as not
// passing in this pass, so an operator reading the feed must see both, each
// once and each naming its own medium. One line for the pair would leave a
// failed copy that the pass recorded but never reported.
func TestScrub_Q118_EveryNotPassingCopyOfOneArtifactGetsItsOwnLine(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	const second = "second_medium"
	svc.Config.StorageMediums = append(svc.Config.StorageMediums, svc.Config.StorageMediums[0])
	svc.Config.StorageMediums[1].ID = second
	var buf bytes.Buffer
	svc.Logger = obs.New(&buf, obs.LevelDebug)

	size := int64(len("the bytes of pair.dump"))
	a := mediumOnlyArtifact(t, ctx, journal, bs, "pair.dump", "retnd/pair.dump", size)
	if _, err := journal.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "on-second", From: "COMPLETE", To: "COMPLETE", OccurredAt: retentionTestNow,
		Placement: &state.PlacementUpdate{Medium: second, Location: "retnd/pair.dump", Size: &size, Hash: "x", HashAlg: "sha256", Status: state.PlacementActive},
	}); err != nil {
		t.Fatal(err)
	}
	svc.MediumStore = &perMediumStore{down: map[string]bool{second: true}}

	if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
		t.Fatalf("Scrub: %v", err)
	}

	rec, _ := journal.Get(ctx, a)
	recorded := map[string]bool{}
	for _, p := range rec.ActiveMediumPlacements() {
		if p.AssuranceState == state.AssuranceFailed || p.AssuranceState == state.AssuranceCouldNotComplete {
			recorded[p.Medium] = true
		}
	}
	if len(recorded) != 2 {
		t.Fatalf("the fixture should leave two copies not passing, journal says %v", recorded)
	}
	lines := scrubFailureLines(t, &buf)
	got := map[string]int{}
	for _, l := range lines {
		got[l["medium"].(string)]++
	}
	for m := range recorded {
		if got[m] != 1 {
			t.Errorf("copy on %q was recorded as not passing and has %d scrub_failure lines, want exactly 1 (lines: %v)", m, got[m], lines)
		}
	}
	if len(lines) != len(recorded) {
		t.Errorf("%d scrub_failure lines for %d not-passing copies", len(lines), len(recorded))
	}
}

// perMediumStore answers by medium: the first medium holds nothing (so a
// stat is not-found), and a medium named in down does not answer.
type perMediumStore struct {
	transport.MediumStore
	down map[string]bool
}

func (s *perMediumStore) StatObject(_ context.Context, m transport.Medium, key string) (transport.ObjectInfo, error) {
	if s.down[m.ID] {
		return transport.ObjectInfo{}, transport.NewError(transport.Transient, "stat", errors.New("dial tcp: i/o timeout"))
	}
	return transport.ObjectInfo{}, transport.NewError(transport.NotFound, "stat_object", errors.New("object not found"))
}
func (s *perMediumStore) ListObjects(context.Context, transport.Medium, string) ([]transport.ObjectInfo, error) {
	return nil, nil
}
func (s *perMediumStore) OpenObject(context.Context, transport.Medium, string) (io.ReadCloser, error) {
	return nil, errors.New("a scrub must not download")
}

// scrubUnknownLines decodes the scrub_unknown_object lines of a log.
func scrubUnknownLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", raw, err)
		}
		if line["event"] == obs.EventScrubUnknownObject {
			out = append(out, line)
		}
	}
	return out
}

// QC-04, the listing half, end to end through the driver: the destination is
// listed, an object at a key the journal does not know comes out as a finding
// (counted, logged, on the wire), the expected copy that is absent is still
// the one counted failure, and the unknown object adds no scrub_failure line.
func TestScrub_QC04_ListingTurnsAnUnknownKeyIntoAFindingNotACopy(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	var buf bytes.Buffer
	svc.Logger = obs.New(&buf, obs.LevelDebug)
	ns := "retnd/" + bs.ID.Source + "/" + bs.ID.Set
	size := int64(len("the bytes of moved.dump"))
	a := mediumOnlyArtifact(t, ctx, journal, bs, "moved.dump", ns+"/moved.dump", size)
	store := &keyedStore{objects: map[string]int64{
		ns + "/stray/moved.dump": size, // right name and size, wrong key
		"retnd/other/set/x.bin":  3,    // another namespace: not this set's
	}}
	svc.MediumStore = store

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Passed != 0 || sum.Failed != 1 {
		t.Errorf("summary = %+v, want the one absent copy failed and nothing passed", sum)
	}
	if sum.Unknown != 1 {
		t.Errorf("Unknown = %d, want 1", sum.Unknown)
	}
	if len(store.listed) != 1 || store.listed[0] != ns {
		t.Errorf("listed %v, want the set's namespace %q once", store.listed, ns)
	}
	if lines := scrubFailureLines(t, &buf); len(lines) != sum.Failed+sum.CouldNotComplete {
		t.Errorf("%d scrub_failure lines for %d failures: an unknown object is not one", len(lines), sum.Failed+sum.CouldNotComplete)
	}
	unk := scrubUnknownLines(t, &buf)
	if len(unk) != 1 || unk[0]["key"] != ns+"/stray/moved.dump" || unk[0]["medium"] != moveTestMedium {
		t.Errorf("scrub_unknown_object lines = %v, want one for the stray key on %s", unk, moveTestMedium)
	}
	w := sum.Wire()
	if len(w.Sets) != 1 || len(w.Sets[0].UnknownObjects) != 1 {
		t.Fatalf("wire = %+v, want the one unknown object", w.Sets)
	}
	if u := w.Sets[0].UnknownObjects[0]; u.Key != ns+"/stray/moved.dump" || u.Medium != moveTestMedium || u.Size != size {
		t.Errorf("wire unknown object = %+v", u)
	}
	rec, _ := journal.Get(ctx, a)
	for _, p := range rec.ActiveMediumPlacements() {
		if p.AssuranceState == state.AssurancePassed {
			t.Errorf("the copy at %q is recorded as passed", p.Location)
		}
	}
}

// A destination the pass cannot list is counted and said, on the wire too.
func TestScrub_QC04_AnUnlistableDestinationIsCountedAndNotAFailure(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	ns := "retnd/" + bs.ID.Source + "/" + bs.ID.Set
	size := int64(len("the bytes of kept.dump"))
	mediumOnlyArtifact(t, ctx, journal, bs, "kept.dump", ns+"/kept.dump", size)
	svc.MediumStore = &noListStore{keyedStore: &keyedStore{objects: map[string]int64{ns + "/kept.dump": size}}}

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Unlisted != 1 || sum.Unknown != 0 || sum.Failed != 0 || sum.Passed != 1 {
		t.Errorf("summary = %+v, want one unlisted medium and the copy still passed", sum)
	}
	w := sum.Wire()
	if len(w.Sets[0].Unlisted) != 1 || w.Sets[0].Unlisted[0].Medium != moveTestMedium {
		t.Errorf("wire unlisted = %+v", w.Sets[0].Unlisted)
	}
}

// noListStore is a keyedStore whose listing fails the way a down medium does.
type noListStore struct{ *keyedStore }

func (s *noListStore) ListObjects(context.Context, transport.Medium, string) ([]transport.ObjectInfo, error) {
	return nil, transport.NewError(transport.Transient, "list_objects", errors.New("dial tcp: i/o timeout"))
}
