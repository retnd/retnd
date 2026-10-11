package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q, #978: the scrub driver as the deployment sees it. Every case here
// runs through Service.Scrub, the one entry point both the scheduler and the
// manual verb use, over a real journal, and asserts on what the pass left
// behind on the copies and in the feed.

var scrubReval = config.Revalidation{Interval: config.Duration(24 * time.Hour), MaxPerCycle: 50, Hash: true}

func scrubService(t *testing.T, reval config.Revalidation, sets ...config.BackupSet) (*Service, *state.Journal) {
	t.Helper()
	srcs := make([]config.BackupSet, len(sets))
	copy(srcs, sets)
	for i := range srcs {
		srcs[i].Revalidation = reval
	}
	cfg := alertingConfig(t, testSource("production", srcs...))
	cfg.StorageMediums = moveTestMediums()
	journal := openJournal(t)
	svc := New(cfg, journal, newFakeTransport(), nil)
	svc.Now = fixedNow(retentionTestNow)
	return svc, journal
}

func localCopyOf(t *testing.T, j *state.Journal, a model.ArtifactID) state.Placement {
	t.Helper()
	rec, err := j.Get(context.Background(), a)
	if err != nil {
		t.Fatalf("Get %s: %v", a, err)
	}
	p, ok := rec.LocalPlacement()
	if !ok {
		t.Fatalf("%s has no local copy", a)
	}
	return p
}

func scrubFeedRows(t *testing.T, j *state.Journal) int {
	t.Helper()
	acts, err := j.RecentActivity(context.Background(), 1_000_000)
	if err != nil {
		t.Fatalf("RecentActivity: %v", err)
	}
	return len(acts)
}

func TestScrub_ChecksEveryDueCopyAndRecordsItOnTheCopy(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "one.dump", retentionTestNow.Add(-48*time.Hour))

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Selected != 1 || sum.Passed != 1 || sum.Failed != 0 {
		t.Errorf("summary = %+v, want one selected and one passed", sum)
	}
	p := localCopyOf(t, journal, a)
	if p.AssuranceState != state.AssurancePassed || p.LastCheckedClass != state.VerificationContent || p.ContentVerifiedAt == nil {
		t.Errorf("copy = %q / %q / %v", p.AssuranceState, p.LastCheckedClass, p.ContentVerifiedAt)
	}
	if p.NextDueAt == nil || !p.NextDueAt.After(retentionTestNow.Add(24*time.Hour-time.Second)) {
		t.Errorf("NextDueAt = %v, want a day or more out", p.NextDueAt)
	}

	// And a second pass now has nothing to do.
	again, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("second Scrub: %v", err)
	}
	if again.Selected != 0 {
		t.Errorf("second pass selected %d copies; nothing was due", again.Selected)
	}
}

// The feed-volume claim, with its number: a pass over 1000 due copies across
// 50 sets writes ONE pass row with counts and appends no per-copy row to the
// transition log, so the activity feed grows by exactly the artifacts a
// failure quarantined, which is zero here.
func TestScrub_AThousandDueCopiesAcrossFiftySetsIsOnePassRowAndNoFeedRows(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 1000 artifacts")
	}
	ctx := context.Background()
	const sets, perSet = 50, 20
	var bsets []config.BackupSet
	dir := t.TempDir()
	for i := 0; i < sets; i++ {
		bs := testBackupSet(t, filepath.Join(dir, fmt.Sprintf("s%02d", i)))
		id, err := model.NewBackupSetID("production", fmt.Sprintf("set%02d", i))
		if err != nil {
			t.Fatal(err)
		}
		bs.ID = id
		bs.Name = id.Set
		bsets = append(bsets, bs)
	}
	svc, journal := scrubService(t, config.Revalidation{Interval: config.Duration(24 * time.Hour), MaxPerCycle: perSet, Hash: true}, bsets...)
	for _, bs := range bsets {
		for j := 0; j < perSet; j++ {
			seedMovableArtifact(t, ctx, journal, bs, fmt.Sprintf("%s-a%02d.dump", bs.ID.Set, j), retentionTestNow.Add(-48*time.Hour))
		}
	}
	before := scrubFeedRows(t, journal)

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Selected != sets*perSet || sum.Passed != sets*perSet {
		t.Fatalf("summary = %+v, want %d selected and passed", sum, sets*perSet)
	}
	passes, err := journal.ListScrubPasses(ctx, 10)
	if err != nil || len(passes) != 1 {
		t.Fatalf("scrub passes = %v, %v; want exactly one durable pass row", passes, err)
	}
	if passes[0].Selected != sets*perSet || passes[0].Passed != sets*perSet {
		t.Errorf("pass row = %+v", passes[0])
	}
	if after := scrubFeedRows(t, journal); after != before {
		t.Errorf("the feed grew by %d rows over a pass that found nothing wrong; the stated number is 0 (one per quarantined artifact, none per copy)", after-before)
	}
}

func TestScrub_TheBoundPerSetHoldsAndTheRestWaitsForTheNextPass(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, config.Revalidation{Interval: config.Duration(24 * time.Hour), MaxPerCycle: 2, Hash: true}, bs)
	for i := 0; i < 5; i++ {
		seedMovableArtifact(t, ctx, journal, bs, fmt.Sprintf("b%d.dump", i), retentionTestNow.Add(-48*time.Hour))
	}
	total := 0
	for pass := 0; pass < 4 && total < 5; pass++ {
		sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
		if err != nil {
			t.Fatal(err)
		}
		if sum.Selected > 2 {
			t.Fatalf("pass %d selected %d, bound is 2", pass, sum.Selected)
		}
		total += sum.Selected
	}
	if total != 5 {
		t.Errorf("five due copies took %d selections to drain, want exactly 5", total)
	}
}

func TestScrub_ASetUnderAnEditHoldIsNotScrubbedAndTheSkipIsCounted(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "held.dump", retentionTestNow.Add(-48*time.Hour))

	hctx := WithBackupSetHolds(ctx, staticHolds{bs.ID.String(): true})
	sum, err := svc.Scrub(hctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Passed != 0 || sum.SkippedHeld < 1 {
		t.Errorf("summary = %+v, want nothing passed and the hold counted", sum)
	}
	if p := localCopyOf(t, journal, a); p.AssuranceState != state.AssuranceUnknown {
		t.Errorf("a held set's copy was scrubbed: %q", p.AssuranceState)
	}
	passes, _ := journal.ListScrubPasses(ctx, 1)
	if len(passes) != 1 || passes[0].SkippedHeld < 1 {
		t.Errorf("the durable pass row does not show the skip: %+v", passes)
	}
}

type staticHolds map[string]bool

func (h staticHolds) Held(id string) bool      { return h[id] }
func (h staticHolds) Changed() <-chan struct{} { return make(chan struct{}) }

func TestScrub_AFailingCopyIsRecordedAndTheArtifactQuarantinedWithOneFeedRow(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	good := seedMovableArtifact(t, ctx, journal, bs, "good.dump", retentionTestNow.Add(-48*time.Hour))
	bad := seedMovableArtifact(t, ctx, journal, bs, "bad.dump", retentionTestNow.Add(-48*time.Hour))
	if err := os.WriteFile(filepath.Join(bs.LocalPath, "bad.dump"), []byte("rotted"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := scrubFeedRows(t, journal)

	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Passed != 1 || sum.Failed != 1 {
		t.Errorf("summary = %+v, want 1 passed and 1 failed", sum)
	}
	if rec, _ := journal.Get(ctx, bad); lifecycle.State(rec.State) != lifecycle.QuarantinedLost {
		t.Errorf("bad artifact state = %s", rec.State)
	}
	if p := localCopyOf(t, journal, bad); p.AssuranceState != state.AssuranceFailed {
		t.Errorf("bad copy = %q", p.AssuranceState)
	}
	if p := localCopyOf(t, journal, good); p.AssuranceState != state.AssurancePassed {
		t.Errorf("good copy = %q", p.AssuranceState)
	}
	if after := scrubFeedRows(t, journal); after-before != 1 {
		t.Errorf("the feed grew by %d rows, want exactly 1 (the quarantine)", after-before)
	}
}

// --- restart and rebuild ---

func TestScrub_ARestartResumesDueStateFromTheJournal(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "journal.db")
	journal := openJournalAt(t, dbPath)
	cfg := alertingConfig(t, testSource("production", withReval(bs, scrubReval)))
	svc := New(cfg, journal, newFakeTransport(), nil)
	svc.Now = fixedNow(retentionTestNow)
	seedMovableArtifact(t, ctx, journal, bs, "r.dump", retentionTestNow.Add(-48*time.Hour))
	if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	// A new process, a journal reopened from disk, and the artifact row's
	// clock rewritten to now: none of it may make the copy due.
	reopened := openJournalAt(t, dbPath)
	svc2 := New(cfg, reopened, newFakeTransport(), nil)
	svc2.Now = fixedNow(retentionTestNow.Add(time.Hour))
	artifact, _ := model.NewArtifactID(bs.ID, "r.dump")
	if _, err := reopened.RecordTransition(ctx, state.Transition{
		Artifact: artifact, Key: "touch", From: "COMPLETE", To: "COMPLETE", OccurredAt: retentionTestNow.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	sum, err := svc2.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Selected != 0 {
		t.Errorf("after a restart and a touched artifact row, %d copies were selected; the copy was not due", sum.Selected)
	}
}

func withReval(bs config.BackupSet, r config.Revalidation) config.BackupSet {
	bs.Revalidation = r
	return bs
}

func TestScrub_AfterACatalogRebuildEveryCopyIsUnknownAndDueNeverFresh(t *testing.T) {
	ctx := context.Background()
	localDir := t.TempDir()
	bs := withReval(testBackupSet(t, localDir), scrubReval)
	bs.RemotePath = ""
	tr := newFakeTransport()
	tr.put("backup.dump", "payload for rebuild", epoch.Unix())

	dbPath := filepath.Join(t.TempDir(), "journal.db")
	journal := openJournalAt(t, dbPath)
	cfg := testConfig(t, testSource("production", bs))
	svc := New(cfg, journal, tr, nil)
	svc.Now = fixedNow(epoch)
	svc.RunCycle(ctx)
	artifact, _ := model.NewArtifactID(bs.ID, "backup.dump")

	// The original copy is scrubbed and fresh.
	if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
		t.Fatal(err)
	}
	if p := localCopyOf(t, journal, artifact); p.AssuranceState != state.AssurancePassed {
		t.Fatalf("precondition: original copy %q", p.AssuranceState)
	}

	// The catalog is lost and rebuilt from the sidecar.
	_ = journal.Close()
	_ = os.Remove(dbPath)
	lost := openJournalAt(t, dbPath)
	svc2 := New(cfg, lost, tr, nil)
	svc2.Now = fixedNow(epoch)
	if _, err := svc2.RebuildCatalog(ctx, bs.ID, false); err != nil {
		t.Fatalf("RebuildCatalog: %v", err)
	}

	p := localCopyOf(t, lost, artifact)
	if p.AssuranceState != state.AssuranceUnknown {
		t.Errorf("a rebuilt copy reads %q, want unknown", p.AssuranceState)
	}
	if p.NextDueAt != nil || p.ContentVerifiedAt != nil || p.VerificationClass != "" {
		t.Errorf("a rebuilt copy carries claims nobody made: due=%v content=%v class=%q", p.NextDueAt, p.ContentVerifiedAt, p.VerificationClass)
	}
	sum, err := svc2.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Selected != 1 {
		t.Errorf("a rebuilt copy was not due: selected %d", sum.Selected)
	}
}

// --- persistent failure degrades health and raises exactly one alert ---

// unreachableStore is a medium that does not answer until it is told to.
type unreachableStore struct {
	transport.MediumStore
	mu   sync.Mutex
	down bool
	size int64
}

func (s *unreachableStore) StatObject(_ context.Context, _ transport.Medium, key string) (transport.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return transport.ObjectInfo{}, transport.NewError(transport.Transient, "stat", errors.New("dial tcp: i/o timeout"))
	}
	return transport.ObjectInfo{Key: key, Size: s.size}, nil
}
func (s *unreachableStore) OpenObject(context.Context, transport.Medium, string) (io.ReadCloser, error) {
	return nil, errors.New("a scrub must not download")
}

func (s *unreachableStore) setDown(d bool) { s.mu.Lock(); s.down = d; s.mu.Unlock() }

func TestScrub_NConsecutiveFailuresDegradeHealthAndRaiseExactlyOneAlertAndALaterPassClearsBoth(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	bs.StaleAfter = config.Duration(30 * 24 * time.Hour)
	svc, journal := scrubService(t, scrubReval, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "away.dump", retentionTestNow.Add(-time.Hour))
	size := int64(len("the bytes of away.dump"))
	// Retire the local copy and keep one on the medium, as a finished move does.
	if _, err := journal.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "retire-local", From: "COMPLETE", To: "COMPLETE", OccurredAt: retentionTestNow,
		Placement: &state.PlacementUpdate{Medium: state.MediumLocal, Location: filepath.Join(bs.LocalPath, "away.dump"), Status: state.PlacementGone},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.RecordTransition(ctx, state.Transition{
		Artifact: a, Key: "on-medium", From: "COMPLETE", To: "COMPLETE", OccurredAt: retentionTestNow,
		Placement: &state.PlacementUpdate{Medium: moveTestMedium, Location: "retnd/away.dump", Size: &size, Hash: "x", HashAlg: "sha256", Status: state.PlacementActive},
	}); err != nil {
		t.Fatal(err)
	}
	store := &unreachableStore{down: true, size: size}
	svc.MediumStore = store
	sink := &recordingSink{}
	if !svc.EnableAlerts(sink) {
		t.Fatal("alerting did not enable")
	}

	now := retentionTestNow
	scrubAt := func() {
		svc.Now = fixedNow(now)
		if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
			t.Fatalf("Scrub: %v", err)
		}
		svc.AlertTick(ctx)
	}
	healthState := func() health.State {
		rep, err := svc.BuildHealthReport(ctx, VersionInfo{})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range rep.BackupSets {
			if h.Set == bs.ID {
				return h.State
			}
		}
		t.Fatal("set missing from the health report")
		return ""
	}

	for i := 1; i <= health.ScrubFailureThreshold; i++ {
		if got := healthState(); got != health.Healthy {
			t.Fatalf("after %d failures the set is %s; it degrades at %d", i-1, got, health.ScrubFailureThreshold)
		}
		now = now.Add(48 * time.Hour) // past any backoff
		scrubAt()
	}
	if got := healthState(); got != health.Degraded {
		t.Errorf("after %d consecutive failures the set is %s, want DEGRADED", health.ScrubFailureThreshold, got)
	}
	if n := sink.countOf(alert.ScrubFailing); n != 1 {
		t.Errorf("alerts = %d, want exactly 1 (all: %v)", n, sink.kinds())
	}
	// More failures do not page again.
	now = now.Add(48 * time.Hour)
	scrubAt()
	if n := sink.countOf(alert.ScrubFailing); n != 1 {
		t.Errorf("a further failure raised another alert: %d", n)
	}

	// The medium comes back and the next pass clears both.
	store.setDown(false)
	now = now.Add(48 * time.Hour)
	scrubAt()
	if got := healthState(); got != health.Healthy {
		t.Errorf("after a passing scrub the set is %s, want HEALTHY", got)
	}
	// Cleared means a recurrence alerts again.
	store.setDown(true)
	for i := 0; i < health.ScrubFailureThreshold; i++ {
		now = now.Add(48 * time.Hour)
		scrubAt()
	}
	if n := sink.countOf(alert.ScrubFailing); n != 2 {
		t.Errorf("alerts after clear and recurrence = %d, want 2", n)
	}
}

// --- an operator's scrub ---

func TestScrub_ManualScrubOfOneCopyForcesItWhetherOrNotItIsDue(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, journal := scrubService(t, scrubReval, bs)
	a := seedMovableArtifact(t, ctx, journal, bs, "force.dump", retentionTestNow.Add(-time.Hour))
	if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
		t.Fatal(err)
	}
	// Nothing is due, but the operator named it.
	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubByManual, Artifact: &a, Medium: state.MediumLocal})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Selected != 1 || sum.Passed != 1 {
		t.Errorf("summary = %+v, want the named copy scrubbed", sum)
	}
	passes, _ := journal.ListScrubPasses(ctx, 1)
	if len(passes) != 1 || passes[0].TriggeredBy != state.ScrubByManual {
		t.Errorf("pass row = %+v", passes)
	}
}

func TestScrub_AnIncrementalEngineSetIsNotScrubbed(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	bs.Engine = model.EngineKopia
	svc, _ := scrubService(t, scrubReval, bs)
	sum, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule})
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if sum.Selected != 0 {
		t.Errorf("selected %d for an incremental set: it has no artifacts and a different ladder", sum.Selected)
	}
}

func TestScrub_ReportsAnUnknownBackupSetRatherThanScrubbingNothingQuietly(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	svc, _ := scrubService(t, scrubReval, bs)
	other, _ := model.NewBackupSetID("production", "nope")
	if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubByManual, Set: &other}); err == nil {
		t.Error("scrubbing a set that is not configured succeeded quietly")
	}
}
