// The scrub driver's contract (EPIC Q, #978): due-ness lives on the copy, a
// HEAD is never content verification, an archive-class copy is never thawed,
// money is refused at the moment of spend, and the bounds are bounds.
//
// Most of these assert on the durable record the pass leaves on the copy
// rather than on the Finding, because the Finding is gone in an hour and the
// record is what an operator reads six months later.

package revalidate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

var scrubCfg = config.Revalidation{Interval: config.Duration(24 * time.Hour), MaxPerCycle: 100, Hash: true}

// classMediums resolves one medium with a storage class, so a test can say
// "this one is DEEP_ARCHIVE".
type classMediums struct{ id, class string }

func (m classMediums) MediumFor(id string) (transport.Medium, bool) {
	if id != m.id {
		return transport.Medium{}, false
	}
	return transport.Medium{ID: id, Type: transport.MediumTypeS3, Bucket: "b", StorageClass: m.class}, true
}

// bytesStore serves real bytes, so a content scrub can pass or fail, and
// counts what was asked.
type bytesStore struct {
	content []byte
	stats   atomic.Int32
	opens   atomic.Int32
}

func (s *bytesStore) StatObject(_ context.Context, _ transport.Medium, key string) (transport.ObjectInfo, error) {
	s.stats.Add(1)
	return transport.ObjectInfo{Key: key, Size: int64(len(s.content))}, nil
}
func (s *bytesStore) OpenObject(_ context.Context, _ transport.Medium, _ string) (io.ReadCloser, error) {
	s.opens.Add(1)
	return io.NopCloser(strings.NewReader(string(s.content))), nil
}
func (s *bytesStore) ObjectChecksum(context.Context, transport.Medium, string, transport.HashAlgorithm) (transport.ChecksumAttestation, error) {
	return transport.ChecksumAttestation{}, transport.NewError(transport.UnsupportedCapability, "object_checksum", errors.New("md5 only"))
}

func onMedium(t *testing.T, name string, content []byte) (*state.Journal, model.ArtifactID) {
	t.Helper()
	j := openJournal(t)
	a := artifactNamed(t, name)
	long := time.Now().UTC().Add(-90 * 24 * time.Hour)
	completeArtifact(t, j, a, content, long)
	moveToMedium(t, j, a, "offsite", content, long)
	return j, a
}

func mediumCopy(t *testing.T, j *state.Journal, a model.ArtifactID) state.Placement {
	t.Helper()
	rec, err := j.Get(context.Background(), a)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ps := rec.ActiveMediumPlacements()
	if len(ps) != 1 {
		t.Fatalf("want one medium copy, got %d", len(ps))
	}
	return ps[0]
}

// --- due-ness lives on the copy ---

func TestDueness_RewritingTheArtifactRowsClockChangesNothingAboutWhatIsDue(t *testing.T) {
	ctx := context.Background()
	j := openJournal(t)
	a := artifactNamed(t, "clock.dump")
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	commitArtifactWithPlacement(t, j, a, []byte("payload"), now.Add(-999*time.Hour))

	// A scrub passes at `now`; the copy is due a day later.
	deps := Deps{Journal: j, Now: func() time.Time { return now }, Bounds: Bounds{Rand: func() float64 { return 0 }}}
	if _, err := Run(ctx, deps, a.Set, scrubCfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	dueAfter := func(at time.Time) int {
		recs, _ := j.ListByBackupSet(ctx, a.Set)
		return len(SelectDue(recs, scrubCfg, at))
	}
	if n := dueAfter(now.Add(time.Hour)); n != 0 {
		t.Fatalf("due one hour after a pass = %d, want 0", n)
	}

	// Reconciliation or a catalog rebuild touches the ARTIFACT row, long
	// after, with a same-state write. Nothing about what is due may move.
	touch := now.Add(500 * time.Hour)
	if _, err := j.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "reconcile-touch", From: "COMMITTED", To: "COMMITTED", OccurredAt: touch,
	}); err != nil {
		t.Fatalf("touching the artifact row: %v", err)
	}
	rec, _ := j.Get(ctx, a)
	if !rec.UpdatedAt.Equal(touch) {
		t.Fatalf("fixture error: UpdatedAt = %v, want %v", rec.UpdatedAt, touch)
	}
	if n := dueAfter(now.Add(time.Hour)); n != 0 {
		t.Errorf("after the artifact row was touched, %d copies are due one hour after a pass: the touch looked like a check, or like staleness", n)
	}
	if n := dueAfter(now.Add(25 * time.Hour)); n != 1 {
		t.Errorf("a day after the pass, %d copies are due, want 1: the artifact row's clock postponed the copy", n)
	}
}

// commitArtifactWithPlacement commits through the real pipeline and records
// the local placement lifecycle's Commit records in production, which the
// older helpers in this package predate.
func commitArtifactWithPlacement(t *testing.T, j *state.Journal, a model.ArtifactID, content []byte, at time.Time) string {
	t.Helper()
	path := commitArtifact(t, j, a, content, at)
	addLocalPlacementFrom(t, j, a, "COMMITTED", path, content, at)
	return path
}

func addLocalPlacementFrom(t *testing.T, j *state.Journal, a model.ArtifactID, st, path string, content []byte, at time.Time) {
	t.Helper()
	size := int64(len(content))
	if _, err := j.RecordTransition(context.Background(), state.Transition{
		Artifact: a, Key: a.String() + ":placement-" + st, From: st, To: st, OccurredAt: at,
		Placement: &state.PlacementUpdate{
			Medium: state.MediumLocal, Location: path, Size: &size,
			Hash: sha256Hex(content), HashAlg: "sha256", Status: state.PlacementActive,
		},
	}); err != nil {
		t.Fatalf("recording the local placement: %v", err)
	}
}

func TestDueness_ACopyNobodyScheduledIsDueAndSortsFirst(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	scheduled := copyDue(t, "scheduled.dump", "COMPLETE", timePtr(now.Add(-time.Hour)), now)
	never := copyDue(t, "never.dump", "COMPLETE", nil, now)
	got := SelectDue([]state.Record{scheduled, never}, scrubCfg, now)
	if len(got) != 2 || got[0].Artifact.Name != "never.dump" {
		var names []string
		for _, r := range got {
			names = append(names, r.Artifact.Name)
		}
		t.Errorf("due = %v, want never.dump (unknown, never scheduled) first", names)
	}
}

func TestDueness_ARecordWithNoCopyRowIsDueAndGetsOneAdopted(t *testing.T) {
	ctx := context.Background()
	j := openJournal(t)
	a := artifactNamed(t, "legacy.dump")
	commitArtifact(t, j, a, []byte("legacy"), time.Now().UTC().Add(-999*time.Hour))

	rec, _ := j.Get(ctx, a)
	if len(rec.Placements) != 0 {
		t.Fatalf("fixture error: %d placements", len(rec.Placements))
	}
	if got := SelectDue([]state.Record{rec}, scrubCfg, time.Now()); len(got) != 1 {
		t.Fatalf("a record with no copy row must be due, got %d", len(got))
	}
	if _, err := Run(ctx, Deps{Journal: j}, a.Set, scrubCfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec, _ = j.Get(ctx, a)
	p, ok := rec.LocalPlacement()
	if !ok {
		t.Fatal("no local copy row was adopted, so the verdict had nowhere to land")
	}
	if p.AssuranceState != state.AssurancePassed || p.ContentVerifiedAt == nil {
		t.Errorf("adopted copy reads %q / %v after a passing content scrub", p.AssuranceState, p.ContentVerifiedAt)
	}
	if p.VerificationClass != state.VerificationContent {
		t.Errorf("class = %q", p.VerificationClass)
	}
}

// --- a HEAD is never content verification ---

// assertNoContentClaim is the check the planted mutation below must turn
// red: after a pass that only HEADed the object, nothing on the copy may say
// content was verified.
func assertNoContentClaim(p state.Placement) error {
	if p.ContentVerifiedAt != nil {
		return fmt.Errorf("ContentVerifiedAt = %v after a HEAD", p.ContentVerifiedAt)
	}
	if p.LastCheckedClass == state.VerificationContent {
		return fmt.Errorf("LastCheckedClass = content after a HEAD")
	}
	return nil
}

// relabelingJournal is the planted violation: it rewrites the class of every
// result to content on its way into the write path, exactly the relabelling
// the epic's matrix names.
type relabelingJournal struct{ Journal }

func (r relabelingJournal) RecordCopyScrub(ctx context.Context, c state.CopyScrub) error {
	if c.Class == state.VerificationExistence {
		c.Class = state.VerificationContent
	}
	return r.Journal.RecordCopyScrub(ctx, c)
}

func headOnlyPass(t *testing.T, wrap func(Journal) Journal) state.Placement {
	t.Helper()
	content := []byte("bytes in a bucket")
	j, a := onMedium(t, "head.dump", content)
	var jr Journal = j
	if wrap != nil {
		jr = wrap(j)
	}
	store := &bytesStore{content: content}
	if _, err := Run(context.Background(), Deps{Journal: jr, Store: store, Mediums: classMediums{id: "offsite"}}, a.Set, scrubCfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.opens.Load() != 0 {
		t.Fatalf("the unattended pass downloaded the object %d times", store.opens.Load())
	}
	return mediumCopy(t, j, a)
}

func TestAHeadIsNeverRecordedAsContentVerification(t *testing.T) {
	p := headOnlyPass(t, nil)
	if err := assertNoContentClaim(p); err != nil {
		t.Error(err)
	}
	if p.LastCheckedClass != state.VerificationExistence || p.AssuranceState != state.AssurancePassed {
		t.Errorf("record = %q/%q, want a pass at existence", p.AssuranceState, p.LastCheckedClass)
	}
}

// The mutation self-test: the guard above must be able to fail. Run it
// against the planted relabelling and require that it does.
func TestTheContentClaimGuardTurnsRedWhenAHeadIsRelabelledContent(t *testing.T) {
	p := headOnlyPass(t, func(j Journal) Journal { return relabelingJournal{j} })
	if err := assertNoContentClaim(p); err == nil {
		t.Fatal("a HEAD relabelled content in the write path left the guard green: the guard cannot fail")
	}
}

// --- archive-class copies are never thawed, money is refused at spend ---

func TestAnArchiveClassCopyIsRecordedRequiresRestoreAndNothingIsRead(t *testing.T) {
	content := []byte("cold bytes")
	j, a := onMedium(t, "cold.dump", content)
	store := &bytesStore{content: content}
	deps := Deps{Journal: j, Store: store, Mediums: classMediums{id: "offsite", class: config.StorageClassDeepArchive}}

	// Unattended: a HEAD, which an archived object answers, and the record
	// says the bytes need a restore.
	if _, err := Run(context.Background(), deps, a.Set, scrubCfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	p := mediumCopy(t, j, a)
	if p.AssuranceState != state.AssuranceRequiresRestore {
		t.Errorf("AssuranceState = %q, want requires_restore", p.AssuranceState)
	}
	if p.LastCheckedClass != state.VerificationExistence || p.ContentVerifiedAt != nil {
		t.Errorf("class/content age = %q/%v after a HEAD of an archived object", p.LastCheckedClass, p.ContentVerifiedAt)
	}
	if store.opens.Load() != 0 {
		t.Errorf("a GET was issued against an archived object %d times", store.opens.Load())
	}
}

func TestAContentScrubOfAnArchiveClassCopyIsRefusedBeforeAnythingIsSpent(t *testing.T) {
	content := []byte("cold bytes")
	j, a := onMedium(t, "cold2.dump", content)
	store := &bytesStore{content: content}
	deps := Deps{Journal: j, Store: store, Mediums: classMediums{id: "offsite", class: config.StorageClassDeepArchive}}

	// Even with the cost acknowledged: an archived object cannot be read,
	// and this product does not start a restore to make it so.
	rep, err := RunWith(context.Background(), deps, a.Set, scrubCfg, Options{Force: true, Content: true, AllowEgress: true})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if store.opens.Load() != 0 || store.stats.Load() != 0 {
		t.Errorf("requests were spent on an archived copy: %d GET, %d HEAD", store.opens.Load(), store.stats.Load())
	}
	p := mediumCopy(t, j, a)
	if p.AssuranceState != state.AssuranceRequiresRestore {
		t.Errorf("AssuranceState = %q, want requires_restore", p.AssuranceState)
	}
	if len(rep.Findings) != 1 || rep.Findings[0].Checked {
		t.Errorf("findings = %+v: nothing was checked, and it must not read as a pass", rep.Findings)
	}
	rec, _ := j.Get(context.Background(), a)
	if rec.State != "COMPLETE" {
		t.Errorf("state = %q: an archived copy is not a failing copy", rec.State)
	}
}

func TestAContentScrubOfAMediumCopyIsRefusedUnlessTheCostWasAcknowledged(t *testing.T) {
	content := []byte("hot bytes in a bucket")
	j, a := onMedium(t, "hot.dump", content)
	store := &bytesStore{content: content}
	deps := Deps{Journal: j, Store: store, Mediums: classMediums{id: "offsite"}}

	rep, err := RunWith(context.Background(), deps, a.Set, scrubCfg, Options{Force: true, Content: true})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if store.opens.Load() != 0 {
		t.Fatalf("a read that costs egress was made %d times without the cost being acknowledged", store.opens.Load())
	}
	if len(rep.Findings) != 1 || len(rep.Findings[0].Copies) != 1 || !rep.Findings[0].Copies[0].Refused {
		t.Errorf("findings = %+v, want the one copy reported as refused", rep.Findings)
	}
	if p := mediumCopy(t, j, a); p.AssuranceState != state.AssuranceUnknown {
		t.Errorf("a refusal wrote %q onto the copy: a refusal to spend is not a fact about the copy", p.AssuranceState)
	}

	// Acknowledged, the same request reads the bytes and earns the age.
	if _, err := RunWith(context.Background(), deps, a.Set, scrubCfg, Options{Force: true, Content: true, AllowEgress: true}); err != nil {
		t.Fatalf("RunWith with the cost acknowledged: %v", err)
	}
	if store.opens.Load() != 1 {
		t.Errorf("opens = %d, want exactly 1", store.opens.Load())
	}
	p := mediumCopy(t, j, a)
	if p.AssuranceState != state.AssurancePassed || p.LastCheckedClass != state.VerificationContent || p.ContentVerifiedAt == nil {
		t.Errorf("record = %q/%q/%v, want content verified", p.AssuranceState, p.LastCheckedClass, p.ContentVerifiedAt)
	}
	if p.LastBytesRead == nil || *p.LastBytesRead != int64(len(content)) {
		t.Errorf("LastBytesRead = %v, want %d", p.LastBytesRead, len(content))
	}
}

func TestAContentScrubThatFindsDifferentBytesFailsTheCopy(t *testing.T) {
	content := []byte("what was ingested")
	j, a := onMedium(t, "rot.dump", content)
	store := &bytesStore{content: []byte("what is there now!")}
	store.content = append([]byte(nil), store.content[:len(content)]...)
	deps := Deps{Journal: j, Store: store, Mediums: classMediums{id: "offsite"}}

	if _, err := RunWith(context.Background(), deps, a.Set, scrubCfg, Options{Force: true, Content: true, AllowEgress: true}); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	p := mediumCopy(t, j, a)
	if p.AssuranceState != state.AssuranceFailed || p.ConsecutiveFailures != 1 {
		t.Errorf("record = %q / %d failures", p.AssuranceState, p.ConsecutiveFailures)
	}
	if p.ContentVerifiedAt != nil || p.VerificationClass != "" {
		t.Errorf("a failed content scrub left the claim standing: %v / %q", p.ContentVerifiedAt, p.VerificationClass)
	}
}

// --- a copy that fails, or cannot be asked ---

func TestAFailingLocalCopyRecordsTheFailureAndQuarantinesTheArtifact(t *testing.T) {
	ctx := context.Background()
	j := openJournal(t)
	a := artifactNamed(t, "bad.dump")
	path := commitArtifactWithPlacement(t, j, a, []byte("original"), time.Now().UTC().Add(-999*time.Hour))
	corrupt(t, path)

	rep, err := Run(ctx, Deps{Journal: j}, a.Set, scrubCfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Findings) != 1 || rep.Findings[0].To != "QUARANTINED" {
		t.Fatalf("findings = %+v, want the artifact quarantined", rep.Findings)
	}
	rec, _ := j.Get(ctx, a)
	var p state.Placement
	for _, c := range rec.Placements {
		p = c
	}
	if p.AssuranceState != state.AssuranceFailed || p.ConsecutiveFailures != 1 || p.LastFailureReason == "" {
		t.Errorf("copy record = %q / %d / %q", p.AssuranceState, p.ConsecutiveFailures, p.LastFailureReason)
	}
	if strings.ContainsAny(p.LastFailureReason, "\x00\x1b") {
		t.Errorf("control bytes in the stored reason: %q", p.LastFailureReason)
	}
}

func TestACopyThatCannotBeAskedBacksOffAndIsNotQuarantined(t *testing.T) {
	ctx := context.Background()
	content := []byte("unreachable")
	j, a := onMedium(t, "away.dump", content)
	store := &countingStore{statErr: transport.NewError(transport.Transient, "stat", errors.New("dial tcp: timeout"))}
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	deps := Deps{
		Journal: j, Store: store, Mediums: fixedMediums{id: "offsite"}, Now: func() time.Time { return now },
		Bounds: Bounds{Rand: func() float64 { return 0 }},
	}

	rep, err := Run(ctx, deps, a.Set, scrubCfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Errors) != 1 {
		t.Fatalf("errors = %+v, want the one medium that did not answer", rep.Errors)
	}
	rec, _ := j.Get(ctx, a)
	if rec.State != "COMPLETE" {
		t.Errorf("state = %q: an unreachable bucket is not evidence of loss", rec.State)
	}
	p := mediumCopy(t, j, a)
	if p.AssuranceState != state.AssuranceCouldNotComplete || p.ConsecutiveFailures != 1 {
		t.Errorf("record = %q / %d", p.AssuranceState, p.ConsecutiveFailures)
	}
	if p.NextDueAt == nil || !p.NextDueAt.Equal(now.Add(DefaultBackoffBase)) {
		t.Errorf("NextDueAt = %v, want %v (the first backoff step)", p.NextDueAt, now.Add(DefaultBackoffBase))
	}
}

// --- bounds ---

func TestBackoffDoublesAndIsCappedAtTheSmallerOfTheCapAndTheInterval(t *testing.T) {
	b := Bounds{Rand: func() float64 { return 0 }}.withDefaults()
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	interval := 24 * time.Hour
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute}
	for i, w := range want {
		if got := b.NextDueAfterFailure(now, interval, i+1).Sub(now); got != w {
			t.Errorf("streak %d backoff = %v, want %v", i+1, got, w)
		}
	}
	if got := b.NextDueAfterFailure(now, interval, 50).Sub(now); got != DefaultBackoffCap {
		t.Errorf("a long streak backs off %v, want the %v cap", got, DefaultBackoffCap)
	}
	if got := b.NextDueAfterFailure(now, time.Hour, 50).Sub(now); got != time.Hour {
		t.Errorf("with a 1h interval a long streak backs off %v: a struggling copy must never be checked less often than a healthy one", got)
	}
}

func TestJitterSpreadsDueTimesWithinItsBound(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	interval := 24 * time.Hour
	seen := map[time.Time]bool{}
	for _, r := range []float64{0, 0.25, 0.5, 0.75, 0.999} {
		b := Bounds{Rand: func() float64 { return r }}
		next := b.NextDueAfterCheck(now, interval)
		seen[next] = true
		if next.Before(now.Add(interval)) || next.After(now.Add(interval+time.Duration(DefaultJitterFraction*float64(interval)))) {
			t.Errorf("rand %v -> %v is outside [interval, interval*(1+jitter)]", r, next.Sub(now))
		}
	}
	if len(seen) != 5 {
		t.Errorf("five different draws produced %d distinct due times: a thousand copies would all be due together again", len(seen))
	}
}

// gate blocks every StatObject until released and records how many were in
// flight at once, per medium and in total.
type gateStore struct {
	release  chan struct{}
	mu       sync.Mutex
	inFlight int
	peak     int
	entered  chan struct{}
}

func (g *gateStore) StatObject(ctx context.Context, _ transport.Medium, key string) (transport.ObjectInfo, error) {
	g.mu.Lock()
	g.inFlight++
	if g.inFlight > g.peak {
		g.peak = g.inFlight
	}
	g.mu.Unlock()
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
	return transport.ObjectInfo{Key: key, Size: 5}, nil
}
func (g *gateStore) OpenObject(context.Context, transport.Medium, string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}
func (g *gateStore) ObjectChecksum(context.Context, transport.Medium, string, transport.HashAlgorithm) (transport.ChecksumAttestation, error) {
	return transport.ChecksumAttestation{}, errors.New("not used")
}

func TestConcurrencyIsBoundedPerDestination(t *testing.T) {
	ctx := context.Background()
	j := openJournal(t)
	const n = 6
	var set model.BackupSetID
	for i := 0; i < n; i++ {
		a := artifactNamed(t, fmt.Sprintf("c%d.dump", i))
		set = a.Set
		completeArtifact(t, j, a, []byte("hello"), time.Now().UTC().Add(-999*time.Hour))
		moveToMedium(t, j, a, "offsite", []byte("hello"), time.Now().UTC().Add(-999*time.Hour))
	}
	store := &gateStore{release: make(chan struct{}), entered: make(chan struct{}, 64)}
	limits := NewLimits(Bounds{GlobalConcurrency: 4, PerDestinationConcurrency: 2})
	deps := Deps{Journal: j, Store: store, Mediums: fixedMediums{id: "offsite"}, Limits: limits,
		Bounds: Bounds{GlobalConcurrency: 4, PerDestinationConcurrency: 2}}

	done := make(chan error, 1)
	go func() { _, err := Run(ctx, deps, set, scrubCfg); done <- err }()

	// Two get in; give a third every chance to sneak past the bound.
	for i := 0; i < 2; i++ {
		select {
		case <-store.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the first two checks never started")
		}
	}
	select {
	case <-store.entered:
		t.Fatal("a third check reached the same medium while two held its slots")
	case <-time.After(300 * time.Millisecond):
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.peak > 2 {
		t.Errorf("peak concurrency on one medium = %d, want at most 2", store.peak)
	}
}

func TestTheGlobalCapHoldsAcrossDestinations(t *testing.T) {
	l := NewLimits(Bounds{GlobalConcurrency: 2, PerDestinationConcurrency: 2})
	ctx := context.Background()
	r1, _ := l.Acquire(ctx, "a")
	r2, _ := l.Acquire(ctx, "b")
	cctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(cctx, "c"); err == nil {
		t.Fatal("a third slot was granted past the global cap of two")
	}
	r1()
	if r, err := l.Acquire(ctx, "c"); err != nil {
		t.Errorf("a freed slot was not reusable: %v", err)
	} else {
		r()
	}
	r2()
}

// --- one scrub per copy, edit holds ---

func TestTwoScrubbersOfOneCopyProduceOneScrub(t *testing.T) {
	ctx := context.Background()
	content := []byte("contended")
	j, a := onMedium(t, "contended.dump", content)
	store := &gateStore{release: make(chan struct{}), entered: make(chan struct{}, 8)}
	mk := func(holder string) Deps {
		return Deps{Journal: j, Store: store, Mediums: fixedMediums{id: "offsite"}, Holder: holder}
	}

	// A scrubber that wrongly reads the same copy twice would block on the
	// gate for ever; let it through after a while so that failure is an
	// assertion and not a hung suite.
	var once sync.Once
	open := func() { once.Do(func() { close(store.release) }) }
	t.Cleanup(open)
	time.AfterFunc(5*time.Second, open)

	first := make(chan Report, 1)
	go func() { r, _ := Run(ctx, mk("scheduler"), a.Set, scrubCfg); first <- r }()
	<-store.entered // the scheduler is mid-check, holding the copy

	// A manual scrub of the same copy arrives now.
	rep, err := RunWith(ctx, mk("cli"), a.Set, scrubCfg, Options{Force: true, Only: &a})
	if err != nil {
		t.Fatalf("manual RunWith: %v", err)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("the manual scrub produced findings %+v for a copy already being scrubbed", rep.Findings)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].Reason != SkipLeased {
		t.Errorf("skipped = %+v, want the copy reported as leased: a skip must be visible", rep.Skipped)
	}
	open()
	r := <-first
	if len(r.Findings) != 1 {
		t.Errorf("the scheduler's findings = %+v, want 1", r.Findings)
	}
	if got := store.peak; got != 1 {
		t.Errorf("peak concurrent checks of one copy = %d, want 1", got)
	}
}

func TestASetUnderAnEditHoldIsNotScrubbedAndTheSkipIsVisible(t *testing.T) {
	content := []byte("held")
	j, a := onMedium(t, "held.dump", content)
	store := &bytesStore{content: content}
	deps := Deps{Journal: j, Store: store, Mediums: classMediums{id: "offsite"},
		Held: func(s model.BackupSetID) bool { return s == a.Set }}

	rep, err := Run(context.Background(), deps, a.Set, scrubCfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.stats.Load() != 0 {
		t.Errorf("a held set was read: %d HEADs", store.stats.Load())
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].Reason != SkipHeld {
		t.Errorf("skipped = %+v, want the hold reported", rep.Skipped)
	}
	if p := mediumCopy(t, j, a); p.AssuranceState != state.AssuranceUnknown || p.NextDueAt != nil {
		t.Errorf("a held set's copy was touched: %q / %v", p.AssuranceState, p.NextDueAt)
	}
}

func TestAPassThatWasCancelledReleasesItsLeases(t *testing.T) {
	content := []byte("cancel me")
	j, a := onMedium(t, "cancel.dump", content)
	store := &gateStore{release: make(chan struct{}), entered: make(chan struct{}, 8)}
	deps := Deps{Journal: j, Store: store, Mediums: fixedMediums{id: "offsite"}, Holder: "h1"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = Run(ctx, deps, a.Set, scrubCfg); close(done) }()
	<-store.entered
	cancel()
	<-done

	ok, err := j.ClaimCopyScrub(context.Background(), a, "offsite", "h2", time.Now(), time.Minute)
	if err != nil || !ok {
		t.Errorf("the lease survived a cancelled pass: claim = %v, %v", ok, err)
	}
}

var _ = placement.Existence

// corrupt rewrites a file's bytes underneath the journal, which is what a
// failing disk does more slowly.
func corrupt(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("rotted bytes"), 0o600); err != nil {
		t.Fatalf("corrupting %s: %v", path, err)
	}
}
