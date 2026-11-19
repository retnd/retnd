package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// The panel's findings on the recovery-test side of the assurance policy
// (C2, C3, C13). Each case drives the production entry point with the
// policy an operator can actually write, over a real journal, and asserts on
// what the deployment did, not on a unit in isolation.

func setPolicy(svc *Service, mode string, cadence time.Duration, validatorID string) {
	rt := &svc.Config.Sources[0].BackupSets[0].Assurance.RecoveryTest
	rt.Mode, rt.Cadence, rt.ValidatorID = mode, config.Duration(cadence), validatorID
}

func TestRecoveryTest_ModeOffRunsNothingEvenWithAScript(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	setPolicy(svc, config.RecoveryTestOff, 0, "")
	a := seedMovableArtifact(t, ctx, journal, bs, "off.dump", retentionTestNow.Add(-time.Hour))

	svc.RecoveryTestTick(ctx)

	if len(stages.validated) != 0 {
		t.Errorf("the validator ran %d times under mode off", len(stages.validated))
	}
	if got := localCopyOf(t, journal, a).RecoveryState; got != state.RecoveryUnknown {
		t.Errorf("the claim moved to %q under mode off", got)
	}
}

func TestRecoveryTest_ADefaultConfigRunsNoScheduledTest(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	// What a deployment that never wrote an assurance block resolves to.
	if err := svc.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	seedMovableArtifact(t, ctx, journal, bs, "default.dump", retentionTestNow.Add(-time.Hour))
	svc.RecoveryTestTick(ctx)
	if len(stages.validated) != 0 {
		t.Errorf("a default configuration ran a recovery test: the default mode is off")
	}
}

func TestRecoveryTest_UsesTheResolvedCadence(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	setPolicy(svc, config.RecoveryTestRequired, 48*time.Hour, "")
	a := seedMovableArtifact(t, ctx, journal, bs, "cad.dump", retentionTestNow.Add(-time.Hour))

	svc.RecoveryTestTick(ctx)
	p := localCopyOf(t, journal, a)
	if p.RecoveryState != state.RecoveryPassed || p.RecoveryNextDueAt == nil {
		t.Fatalf("after the tick: %q due %v", p.RecoveryState, p.RecoveryNextDueAt)
	}
	if want := retentionTestNow.Add(48 * time.Hour); !p.RecoveryNextDueAt.Equal(want) {
		t.Errorf("next due %v, want %v: the cadence the operator wrote, not a constant", p.RecoveryNextDueAt, want)
	}

	// And a pass 49 hours old is due again under that cadence.
	svc.Now = fixedNow(retentionTestNow.Add(49 * time.Hour))
	svc.RecoveryTestTick(ctx)
	if len(stages.validated) != 2 {
		t.Errorf("validator runs = %d, want a second after the 48h cadence", len(stages.validated))
	}
}

func TestRecoveryTest_ValidatorIDSelectsTheScript(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	stages.outcome.Name = "pg-check.local.sh"
	svc, journal, bs := recoveryService(t, stages)
	setPolicy(svc, config.RecoveryTestRequired, 0, "pg-check")
	a := seedMovableArtifact(t, ctx, journal, bs, "named.dump", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryPassed {
		t.Fatalf("State = %q (%s)", res.Result.State, res.Result.Reason)
	}
	if len(stages.validated) != 1 || stages.validated[0].Only != "pg-check.local.sh" {
		t.Fatalf("validated = %+v, want one run asking for exactly pg-check.local.sh", stages.validated)
	}
	if got := localCopyOf(t, journal, a).RecoveryValidatorID; got != "pg-check.local.sh" {
		t.Errorf("recorded validator = %q", got)
	}
}

func TestRecoveryTest_ANamedValidatorThatIsNotThereFailsClosed(t *testing.T) {
	ctx := context.Background()
	stages := accepts() // holds 10-check.local.sh
	svc, journal, bs := recoveryService(t, stages)
	setPolicy(svc, config.RecoveryTestRequired, 0, "pg-check")
	a := seedMovableArtifact(t, ctx, journal, bs, "missing.dump", retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryCouldNotComplete || !strings.Contains(res.Result.Reason, "pg-check") {
		t.Errorf("result = %q / %q, want could_not_complete naming the declared validator", res.Result.State, res.Result.Reason)
	}
	if len(stages.validated) != 0 {
		t.Errorf("a different script ran in its place: %d runs", len(stages.validated))
	}
	if got := localCopyOf(t, journal, a).RecoveryState; got != state.RecoveryCouldNotComplete {
		t.Errorf("recorded %q", got)
	}

	// With no workflow directory at all the declared validator is still
	// absent, not "not configured".
	svc2, journal2, bs2 := recoveryService(t, &validatorStages{configured: false})
	setPolicy(svc2, config.RecoveryTestRequired, 0, "pg-check")
	seedMovableArtifact(t, ctx, journal2, bs2, "nodir.dump", retentionTestNow.Add(-time.Hour))
	res2, err := svc2.RecoveryTest(ctx, RecoveryTestRequest{Set: bs2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Result.State != state.RecoveryCouldNotComplete {
		t.Errorf("no directory but a declared validator: %q, want could_not_complete", res2.Result.State)
	}
}

func TestHealth_RequiredModeDegradesASetWithNoPass(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := recoveryService(t, accepts())
	bs2 := bs
	bs2.StaleAfter = config.Duration(30 * 24 * time.Hour)
	svc.Config.Sources[0].BackupSets[0].StaleAfter = bs2.StaleAfter
	seedMovableArtifact(t, ctx, journal, bs, "req.dump", retentionTestNow.Add(-time.Hour))

	stateOf := func() health.State {
		rep, err := svc.BuildHealthReport(ctx, VersionInfo{})
		if err != nil {
			t.Fatal(err)
		}
		return rep.BackupSets[0].State
	}
	if got := stateOf(); got != health.Degraded {
		t.Errorf("a set that requires a recovery test and has none is %s, want DEGRADED", got)
	}
	setPolicy(svc, config.RecoveryTestOff, 0, "")
	if got := stateOf(); got != health.Healthy {
		t.Errorf("the same set with mode off is %s, want HEALTHY", got)
	}
}

func TestAssuranceTick_ScrubsAndRecoveryTestsInOneCall(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	svc.Config.Sources[0].BackupSets[0].Revalidation = scrubReval
	if err := svc.Config.ResolveAssurance(); err != nil {
		t.Fatal(err)
	}
	setPolicy(svc, config.RecoveryTestRequired, 0, "")
	a := seedMovableArtifact(t, ctx, journal, bs, "both.dump", retentionTestNow.Add(-48*time.Hour))

	svc.AssuranceTick(ctx)

	p := localCopyOf(t, journal, a)
	if p.AssuranceState != state.AssurancePassed {
		t.Errorf("scrub claim = %q, want passed", p.AssuranceState)
	}
	if p.RecoveryState != state.RecoveryPassed {
		t.Errorf("recovery claim = %q, want passed: the tick that scrubs must also test", p.RecoveryState)
	}
}

func TestRecoveryTestTick_IsBoundedPerTick(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	// Two sets, both due. The first test eats the whole bound.
	second := bs
	id, _ := model.NewBackupSetID("production", "second")
	second.ID, second.Name = id, id.Set
	second.LocalPath = t.TempDir()
	svc.Config.Sources[0].BackupSets = append(svc.Config.Sources[0].BackupSets, second)
	svc.Config.Sources[0].BackupSets[1].Assurance.RecoveryTest.Mode = config.RecoveryTestRequired
	seedMovableArtifact(t, ctx, journal, bs, "one.dump", retentionTestNow.Add(-time.Hour))
	seedMovableArtifact(t, ctx, journal, second, "two.dump", retentionTestNow.Add(-time.Hour))
	stages.delay = 50 * time.Millisecond
	svc.RecoveryTickBound = 10 * time.Millisecond

	svc.RecoveryTestTick(ctx)

	if n := len(stages.validated); n != 1 {
		t.Errorf("validator runs in one bounded tick = %d, want 1: the rest wait for the next tick, not for ever behind a slow validator", n)
	}
	svc.RecoveryTickBound = 0
	svc.RecoveryTestTick(ctx)
	if n := len(stages.validated); n != 2 {
		t.Errorf("after an unbounded tick the second set was still untested: runs = %d", n)
	}
}

// --- the alert path (C13) ---

func assuranceAlertService(t *testing.T) (*Service, *state.Journal, config.BackupSet, *recordingSink, *validatorStages) {
	t.Helper()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	svc.Config.Alerts = config.Alerts{Enabled: true, RepeatedFailureThreshold: 3}
	svc.Config.Sources[0].BackupSets[0].StaleAfter = config.Duration(30 * 24 * time.Hour)
	sink := &recordingSink{}
	if !svc.EnableAlerts(sink) {
		t.Fatal("EnableAlerts returned false for a config that opted in")
	}
	return svc, journal, bs, sink, stages
}

func TestARecoveryTestFailureRaisesRecoveryTestFailingThroughTheDispatcher(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs, sink, stages := assuranceAlertService(t)
	seedMovableArtifact(t, ctx, journal, bs, "fail.dump", retentionTestNow.Add(-time.Hour))
	stages.outcome.ExitCode = exitCode(7)
	stages.outcome.State = "failed"

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	svc.AlertTick(ctx)
	svc.AlertTick(ctx)

	if got := sink.countOf(alert.RecoveryTestFailing); got != 1 {
		t.Fatalf("RECOVERY_TEST_FAILING alerts = %d across two ticks, want exactly 1 (all: %v)", got, sink.kinds())
	}
	for _, a := range sink.delivered {
		if a.Kind == alert.RecoveryTestFailing && (strings.Contains(a.Message, ".dump") || strings.Contains(a.Message, bs.LocalPath) || !strings.Contains(a.Message, bs.ID.String())) {
			t.Errorf("the alert text names a file or no set: %q", a.Message)
		}
	}

	// A later pass resolves it, and a recurrence alerts again.
	stages.outcome.ExitCode = exitCode(0)
	stages.outcome.State = "success"
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	svc.AlertTick(ctx)
	stages.outcome.ExitCode = exitCode(7)
	stages.outcome.State = "failed"
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.RecoveryTestFailing); got != 2 {
		t.Errorf("after a clear and a recurrence there should be 2 alerts, got %d", got)
	}
}

func TestALapsedRecoveryTestRaisesRecoveryTestOverdueOnlyWhenRequired(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs, sink, _ := assuranceAlertService(t)
	setPolicy(svc, config.RecoveryTestRequired, 24*time.Hour, "")
	seedMovableArtifact(t, ctx, journal, bs, "lapse.dump", retentionTestNow.Add(-time.Hour))

	// A set no test has run on yet is untested, not overdue: nothing to say.
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.RecoveryTestOverdue); got != 0 {
		t.Fatalf("RECOVERY_TEST_OVERDUE = %d for a set that was never tested (all: %v)", got, sink.kinds())
	}
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.RecoveryTestOverdue); got != 0 {
		t.Fatalf("an on-time pass alerted: %v", sink.kinds())
	}

	// Two days later the pass has lapsed.
	svc.Now = fixedNow(retentionTestNow.Add(49 * time.Hour))
	svc.AlertTick(ctx)
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.RecoveryTestOverdue); got != 1 {
		t.Fatalf("RECOVERY_TEST_OVERDUE = %d after the pass lapsed, want exactly 1 across two ticks (all: %v)", got, sink.kinds())
	}

	// The same lapse on a set that does not require a test says nothing.
	svc2, journal2, bs2, sink2, _ := assuranceAlertService(t)
	setPolicy(svc2, config.RecoveryTestRequired, 24*time.Hour, "")
	seedMovableArtifact(t, ctx, journal2, bs2, "lapse.dump", retentionTestNow.Add(-time.Hour))
	if _, err := svc2.RecoveryTest(ctx, RecoveryTestRequest{Set: bs2.ID}); err != nil {
		t.Fatal(err)
	}
	setPolicy(svc2, config.RecoveryTestOff, 0, "")
	svc2.Now = fixedNow(retentionTestNow.Add(49 * time.Hour))
	svc2.AlertTick(ctx)
	if got := sink2.countOf(alert.RecoveryTestOverdue) + sink2.countOf(alert.RecoveryTestFailing); got != 0 {
		t.Errorf("a set that does not require a test alerted about one: %v", sink2.kinds())
	}
}

func TestAScrubFailureRaisesTheScrubFailingAlertThroughTheDispatcher(t *testing.T) {
	ctx := context.Background()
	bs := testBackupSet(t, t.TempDir())
	bs.StaleAfter = config.Duration(30 * 24 * time.Hour)
	svc, journal := scrubService(t, scrubReval, bs)
	svc.Config.Alerts = config.Alerts{Enabled: true, RepeatedFailureThreshold: 3}
	a := seedMovableArtifact(t, ctx, journal, bs, "away.dump", retentionTestNow.Add(-time.Hour))
	size := int64(len("the bytes of away.dump"))
	// The copy lives on a medium that stops answering, as a finished move leaves it.
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
	svc.MediumStore = &unreachableStore{down: true, size: size}
	sink := &recordingSink{}
	if !svc.EnableAlerts(sink) {
		t.Fatal("EnableAlerts returned false")
	}

	now := retentionTestNow
	for i := 0; i < health.ScrubFailureThreshold; i++ {
		now = now.Add(48 * time.Hour)
		svc.Now = fixedNow(now)
		if _, err := svc.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil {
			t.Fatalf("Scrub: %v", err)
		}
	}

	svc.AlertTick(ctx)
	svc.AlertTick(ctx)
	if got := sink.countOf(alert.ScrubFailing); got != 1 {
		t.Fatalf("SCRUB_FAILING alerts = %d, want 1 across two ticks (all: %v)", got, sink.kinds())
	}
}

// --- recovery stage events (C2) ---

func eventsOf(t *testing.T, stream *bytes.Buffer, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stream.String()), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		if ev["event"] == name {
			out = append(out, ev)
		}
	}
	return out
}

func TestARecoveryTestEmitsTheStartStagesAndEndTheUIRenders(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	var stream bytes.Buffer
	svc, journal, bs := recoveryService(t, stages)
	svc.Logger = obs.New(&stream, obs.LevelInfo)
	seedMovableArtifact(t, ctx, journal, bs, "ev.dump", retentionTestNow.Add(-time.Hour))

	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}

	if n := len(eventsOf(t, &stream, obs.EventRecoveryTestStart)); n != 1 {
		t.Fatalf("recovery_test_start events = %d, want 1\n%s", n, stream.String())
	}
	var got []string
	var opID any
	for _, ev := range eventsOf(t, &stream, obs.EventRecoveryStage) {
		got = append(got, ev["stage"].(string))
		if opID == nil {
			opID = ev["op_id"]
		}
		if ev["op_id"] != opID {
			t.Errorf("stage events carry two operation ids: %v and %v", opID, ev["op_id"])
		}
	}
	want := []string{"resolving_copy", "reading", "verifying", "validating", "cleaning_temporary_files", "complete"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stages = %v, want %v, each a stage obs.RecoveryStages names and in its order", got, want)
	}
	for _, s := range got {
		if obs.StageLabel(s) == "" {
			t.Errorf("stage %q is not in obs.RecoveryStages", s)
		}
	}
	end := eventsOf(t, &stream, obs.EventRecoveryTestEnd)
	if len(end) != 1 || end[0]["result"] != "success" || end[0]["op_id"] != opID {
		t.Errorf("recovery_test_end = %v, want one success line for operation %v", end, opID)
	}
}

func TestAFailedRecoveryTestEndsWithAnErrorResultNotASuccess(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	stages.outcome.ExitCode = exitCode(9)
	stages.outcome.State = "failed"
	var stream bytes.Buffer
	svc, journal, bs := recoveryService(t, stages)
	svc.Logger = obs.New(&stream, obs.LevelInfo)
	seedMovableArtifact(t, ctx, journal, bs, "evf.dump", retentionTestNow.Add(-time.Hour))
	if _, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID}); err != nil {
		t.Fatal(err)
	}
	end := eventsOf(t, &stream, obs.EventRecoveryTestEnd)
	if len(end) != 1 || end[0]["result"] == "success" {
		t.Errorf("recovery_test_end = %v, want one non-success line", end)
	}
}

// A copy rewritten while the test ran: the result is dropped by the journal,
// and that is neither a pass, nor a failure, nor an episode.
func TestARecoveryTestOfACopyThatChangedIsNotAnnounced(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := recoveryService(t, stages)
	a := seedMovableArtifact(t, ctx, journal, bs, "chg.dump", retentionTestNow.Add(-time.Hour))
	stages.beforeReturn = func() {
		size := int64(3)
		p := localCopyOf(t, journal, a)
		if _, err := journal.RecordTransition(ctx, state.Transition{
			Artifact: a, Key: "rewritten", From: "COMPLETE", To: "COMPLETE", OccurredAt: retentionTestNow,
			Placement: &state.PlacementUpdate{
				Medium: state.MediumLocal, Location: p.Location, Size: &size, Hash: "eeee", HashAlg: "sha256",
				VerificationClass: state.VerificationExistence, Status: state.PlacementActive,
			},
		}); err != nil {
			t.Error(err)
		}
	}
	stages.outcome.ExitCode = exitCode(7)
	stages.outcome.State = "failed"

	_, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if !errors.Is(err, state.ErrCopyChanged) {
		t.Fatalf("err = %v, want ErrCopyChanged", err)
	}
	if got := stages.events(); len(got) != 0 {
		t.Errorf("a verdict about bytes that are gone was announced: %v", got)
	}
	if got := localCopyOf(t, journal, a).RecoveryState; got != state.RecoveryUnknown {
		t.Errorf("recorded %q", got)
	}
}

// A local_volume destination is a directory, and every surface that asks
// whether reading it costs money or how to dial it has to get the same
// answer: MediumFor used to cast the configured type straight across, so the
// copy on a local volume looked like a download from a storage medium.
func TestMediumFor_ALocalVolumeIsALocalDirectoryAndNotAnObjectStore(t *testing.T) {
	cfg := &config.Config{StorageMediums: []config.StorageMedium{
		{ID: "disk2", Type: config.StorageMediumTypeLocalVolume, Path: "/mnt/disk2"},
	}}
	m, _, err := MediumFor(cfg, "disk2")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != transport.MediumTypeLocalDir {
		t.Errorf("Type = %q, want %q: this is a directory on a disk, nothing is billed for reading it", m.Type, transport.MediumTypeLocalDir)
	}
	if m.Bucket != "/mnt/disk2" {
		t.Errorf("Bucket = %q, want the volume's path, which is what the transport dials", m.Bucket)
	}
}

// The scrub reads under the same ceilings the settings name, not under its own.
func TestAssuranceHostKnobsReachTheScrubLimits(t *testing.T) {
	bs := testBackupSet(t, t.TempDir())
	svc, _ := scrubService(t, scrubReval, bs)
	svc.Config.Assurance.MaxConcurrentFullReads = 1
	svc.Config.Assurance.MaxPerDestination = 1

	limits := svc.scrubLimitsShared()
	release, err := limits.Acquire(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if r, err := limits.Acquire(ctx, "other-destination"); err == nil {
		r()
		t.Fatal("a second full read was admitted under assurance.max_concurrent_full_reads = 1")
	}
}

func TestTheScrubAndTheSettingsShareTheirDefaults(t *testing.T) {
	// A hand-built configuration resolves no ceilings; the scrub must then
	// run under the very numbers the settings report as defaults.
	bs := testBackupSet(t, t.TempDir())
	svc, _ := scrubService(t, scrubReval, bs)
	svc.Config.Assurance.MaxConcurrentFullReads = 0
	svc.Config.Assurance.MaxPerDestination = 0
	limits := svc.scrubLimitsShared()
	var held []func()
	for i := 0; i < config.DefaultMaxConcurrentReads; i++ {
		r, err := limits.Acquire(context.Background(), fmt.Sprintf("d%d", i))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, r)
	}
	defer func() {
		for _, r := range held {
			r()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if r, err := limits.Acquire(ctx, "one-more"); err == nil {
		r()
		t.Errorf("more than the settings' default of %d full reads ran at once", config.DefaultMaxConcurrentReads)
	}
}
