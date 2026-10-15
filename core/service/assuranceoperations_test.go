package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q, #980 and #1071: scrub now, cancel, the validator output of the
// newest recovery test, and two operators asking for the same test.

// processStages runs the validator the way the host runner would run a
// local script: a real child process in a process group of its own, through
// lifecycle.RunRestoreCheck, which is the kill-the-group contract (FR-13)
// every operator-supplied executable in this product lives under.
type processStages struct {
	exe string

	mu   sync.Mutex
	runs int
}

func (s *processStages) RunStage(ctx context.Context, req app.StageRequest) (app.StageResult, error) {
	name := "pg-check.local.sh"
	res := app.StageResult{Configured: true, Scripts: []app.StageScript{{
		Name: name, SHA256: strings.Repeat("ab", 32), Target: "local",
	}}}
	if strings.HasPrefix(req.SetDir, "assurance-after") {
		return app.StageResult{Configured: true, Scripts: []app.StageScript{{Name: "10-notify.local.sh", State: "success"}}}, nil
	}
	if req.Only != "" && req.Only != name {
		return app.StageResult{}, app.ErrStageScriptMissing
	}
	if req.DryRun {
		return res, nil
	}
	s.mu.Lock()
	s.runs++
	s.mu.Unlock()
	r, err := lifecycle.RunRestoreCheck(ctx, config.Command{Executable: s.exe, Timeout: config.Duration(2 * time.Minute)}, req.Vars["RECOVERY_TEST_FILE"])
	if err != nil {
		return app.StageResult{}, err
	}
	code := 1
	if r.Passed {
		code = 0
	}
	sc := &res.Scripts[0]
	sc.State, sc.Disposition, sc.ExitCode, sc.Output = "success", "exited", &code, []byte(r.Detail)
	return res, nil
}

// hangingValidator writes a script that starts a child, records its own pid
// (the process group's id, since the runner makes the script the leader of a
// fresh group) and the child's, and then waits for the child for ten minutes.
func hangingValidator(t *testing.T) (exe, pgidFile, childFile string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "validate.sh")
	pgidFile, childFile = filepath.Join(dir, "pgid"), filepath.Join(dir, "child")
	script := "#!/bin/sh\nsleep 600 &\necho $! > " + childFile + "\necho $$ > " + pgidFile + "\nwait\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return exe, pgidFile, childFile
}

func readPID(t *testing.T, path string, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				return n
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never written: the validator did not start", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// liveMembers lists the processes still alive in process group pgid. A
// zombie does not count: it has already been killed and is waiting for a
// parent that, in a container, may be nobody. /proc is Linux's; elsewhere
// the same listing comes from ps.
func liveMembers(pgid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return liveMembersByPS(pgid)
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp ...: comm may hold spaces, so split after the last ")".
		rest := string(b)[strings.LastIndexByte(string(b), ')')+2:]
		f := strings.Fields(rest)
		if len(f) < 3 || f[0] == "Z" || f[0] == "X" {
			continue
		}
		if g, _ := strconv.Atoi(f[2]); g == pgid {
			out = append(out, pid)
		}
	}
	return out
}

// liveMembersByPS is liveMembers for a machine with no /proc (macOS): ps
// prints pid, group and state for every process, and the rule is the same,
// a zombie does not count. The positive control in the cancel test needs the
// members counted, which the old probe of the group with signal 0 could not
// do, so that probe is only the answer when ps itself cannot be run.
func liveMembersByPS(pgid int) []int {
	listing, err := exec.Command("ps", "-axo", "pid=,pgid=,stat=").Output()
	if err != nil {
		if syscall.Kill(-pgid, 0) == nil {
			return []int{-pgid}
		}
		return nil
	}
	var out []int
	for _, line := range strings.Split(string(listing), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(f[2], "Z") {
			continue
		}
		pid, perr := strconv.Atoi(f[0])
		g, gerr := strconv.Atoi(f[1])
		if perr == nil && gerr == nil && g == pgid {
			out = append(out, pid)
		}
	}
	return out
}

func idle(t *testing.T, f *recoveryFixture) {
	t.Helper()
	done := make(chan struct{})
	go func() { f.b.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("background work did not finish")
	}
}

func waitRan(t *testing.T, stages *stubStages) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for stages.ran() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the background test never reached its validator")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- cancel ---

func TestCancelDuringTheValidatorStageKillsTheWholeProcessGroup(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	exe, pgidFile, childFile := hangingValidator(t)
	stages := &processStages{exe: exe}
	f.b.state.Load().inner.Stages = stages
	a := seedComplete(t, f, "cancel.dump", state.MediumLocal)

	started, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	pgid := readPID(t, pgidFile, 30*time.Second)
	child := readPID(t, childFile, 30*time.Second)

	// The positive control: the check below can see the script and the child it
	// started, so an empty answer afterwards means they are gone and not that
	// the check looks at the wrong thing.
	if before := liveMembers(pgid); len(before) < 2 {
		t.Fatalf("group %d has live members %v before the cancel, want the script and its child", pgid, before)
	}

	got, err := f.b.CancelAssuranceOperation(ctx, started.OperationID)
	if err != nil {
		t.Fatalf("CancelAssuranceOperation: %v", err)
	}
	if got.OperationID != started.OperationID || got.Kind != "recovery_test" || got.BackupSetID != "production/postgres" || got.Status != "cancelling" {
		t.Errorf("answer = %+v", got)
	}

	idle(t, f)
	if left := liveMembers(pgid); len(left) != 0 {
		t.Errorf("process group %d still has live members %v after the cancel: a validator's child outlived the test", pgid, left)
	}
	if err := syscall.Kill(child, 0); err == nil && len(liveMembers(pgid)) == 0 {
		// The pid answers but the group is empty: only a zombie or a reused
		// pid is possible, and neither is an orphan still running.
		t.Logf("pid %d answers signal 0 with no live group member (a zombie awaiting a reaper)", child)
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Errorf("a cancelled test left %v staged", e)
	}
	if st := claimOf(t, f, a, state.MediumLocal).RecoveryState; st == state.RecoveryPassed || st == state.RecoveryFailed {
		t.Errorf("a cancelled test recorded a verdict (%q) about a copy it never finished testing", st)
	}
	end := f.eventsNamed(t, obs.EventRecoveryTestEnd)
	if len(end) != 1 || end[0]["op_id"] != started.OperationID || end[0]["result"] == "success" {
		t.Errorf("end lines = %v, want one for the cancelled operation that is not a success", end)
	}

	// The operation is gone from the registry once it ended.
	if _, err := f.b.CancelAssuranceOperation(ctx, started.OperationID); !errors.Is(err, ErrOperationNotFound) {
		t.Errorf("cancelling a finished operation: err = %v, want ErrOperationNotFound", err)
	}
}

func TestCancelOfAnOperationNobodyStartedIsNotFound(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	for _, id := range []string{"", "no-such-op", "00000000-0000-0000-0000-000000000000"} {
		if _, err := f.b.CancelAssuranceOperation(context.Background(), id); !errors.Is(err, ErrOperationNotFound) {
			t.Errorf("cancel %q: err = %v, want ErrOperationNotFound", id, err)
		}
	}
}

// stallStore holds the first existence check of a copy on a storage medium
// until the scrub's context ends, which is what a scrub of a slow destination
// looks like from outside.
type stallStore struct {
	*recoveryStore
	entered chan struct{}
	once    sync.Once
}

func (s *stallStore) StatObject(ctx context.Context, m transport.Medium, key string) (transport.ObjectInfo, error) {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return transport.ObjectInfo{}, ctx.Err()
}

func TestCancelStopsARunningScrubAndLeavesNoVerdictBehind(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	stall := &stallStore{recoveryStore: f.store, entered: make(chan struct{})}
	f.b.state.Load().inner.MediumStore = stall
	a := seedComplete(t, f, "scrubme.dump", "warm")

	started, err := f.b.StartScrub(ctx, "production/postgres", ScrubOptions{Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-stall.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the scrub never reached the medium")
	}
	// One at a time per set.
	if _, err := f.b.StartScrub(ctx, "production/postgres", ScrubOptions{}); !errors.Is(err, ErrScrubRunning) {
		t.Errorf("a second scrub of a running set: err = %v, want ErrScrubRunning", err)
	}
	got, err := f.b.CancelAssuranceOperation(ctx, started.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "scrub" || got.BackupSetID != "production/postgres" || got.Status != "cancelling" {
		t.Errorf("answer = %+v", got)
	}
	idle(t, f) // a scrub that ignored its context would hang here
	if st := claimOf(t, f, a, "warm").AssuranceState; st == state.AssurancePassed {
		t.Errorf("a cancelled scrub left the copy %q", st)
	}
	// And it can be run again.
	f.b.state.Load().inner.MediumStore = f.store
	if _, err := f.b.StartScrub(ctx, "production/postgres", ScrubOptions{}); err != nil {
		t.Errorf("a scrub after the cancelled one finished: %v", err)
	}
	idle(t, f)
}

// --- scrub now ---

func TestStartScrubRunsAPassInTheBackgroundUnderAnOperationId(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	a := seedComplete(t, f, "scrubbed.dump", state.MediumLocal)

	started, err := f.b.StartScrub(ctx, "production/postgres", ScrubOptions{Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if started.BackupSetID != "production/postgres" || len(started.OperationID) < 8 {
		t.Errorf("started = %+v, want the set and an operation id", started)
	}
	idle(t, f)
	if got := claimOf(t, f, a, state.MediumLocal).AssuranceState; got != state.AssurancePassed {
		t.Errorf("after the background scrub the copy is %q, want passed", got)
	}
	if rows := f.eventsNamed(t, obs.EventScrubPass); len(rows) != 1 {
		t.Errorf("%d scrub_pass rows, want 1", len(rows))
	}
}

func TestStartScrubRefusesWhatCannotBeScrubbed(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	for name, tc := range map[string]struct {
		set  string
		want error
	}{
		"an unknown set":  {"production/nope", ErrBackupSetNotFound},
		"a malformed set": {"nope", ErrInvalidRequest},
	} {
		if _, err := f.b.StartScrub(context.Background(), tc.set, ScrubOptions{}); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	incremental := newRecoveryFixture(t, fixtureOpts{engine: "kopia"})
	if _, err := incremental.b.StartScrub(context.Background(), "production/postgres", ScrubOptions{}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("an incremental set: err = %v, want ErrInvalidRequest (its verification ladder is its own)", err)
	}
	disabled := newRecoveryFixture(t, fixtureOpts{cfg: func(c *config.Config) { c.Sources[0].BackupSets[0].Disabled = true }})
	if _, err := disabled.b.StartScrub(context.Background(), "production/postgres", ScrubOptions{}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a disabled set: err = %v, want ErrInvalidRequest", err)
	}
}

// --- two operators, one test ---

func TestTwoConcurrentRecoveryTestRequestsGiveOneExecutionAndTwoReceipts(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	gate := make(chan struct{})
	stages := &stubStages{script: "pg-check.local.sh", gate: gate}
	withStages(f, stages)
	a := seedComplete(t, f, "shared.dump", state.MediumLocal)

	first, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Coalesced {
		t.Error("the first request is the execution, not a coalesced one")
	}
	waitRan(t, stages)

	second, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{Actor: "bob"})
	if err != nil {
		t.Fatalf("an identical request while the test runs: %v, want 202 with the running operation (not a refusal)", err)
	}
	if second.OperationID != first.OperationID || !second.Coalesced {
		t.Errorf("second = %+v, want the running operation %s with coalesced true", second, first.OperationID)
	}
	// Something else is still refused.
	if _, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{Actor: "carol", AcknowledgeCost: true}); !errors.Is(err, ErrRecoveryTestRunning) {
		t.Errorf("a different request: err = %v, want ErrRecoveryTestRunning", err)
	}
	if _, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{Actor: "carol", ArtifactID: a.String()}); !errors.Is(err, ErrRecoveryTestRunning) {
		t.Errorf("a request naming an artifact while the newest is tested: err = %v, want ErrRecoveryTestRunning", err)
	}

	close(gate)
	idle(t, f)

	if n := stages.ran(); n != 1 {
		t.Errorf("the validator ran %d times for two requests, want 1: the second request was a second read", n)
	}
	if n := f.store.opens.Load(); n != 0 {
		t.Errorf("the local copy's test opened a medium %d times", n)
	}
	if evs := f.eventsNamed(t, obs.EventRecoveryTestStart); len(evs) != 1 {
		t.Errorf("%d recovery_test_start lines, want 1", len(evs))
	}
	receipts, err := f.b.journal.RecoveryReceipts(ctx, first.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 2 {
		t.Fatalf("%d receipts, want one per accepted request (2): %+v", len(receipts), receipts)
	}
	if receipts[0].RequestedBy != "alice" || receipts[0].Coalesced || receipts[1].RequestedBy != "bob" || !receipts[1].Coalesced {
		t.Errorf("receipts = %+v, want alice's own and bob's coalesced one", receipts)
	}
	if receipts[0].Set.String() != "production/postgres" || receipts[1].Set.String() != "production/postgres" || receipts[1].RequestedAt.IsZero() {
		t.Errorf("receipts = %+v", receipts)
	}

	// Once it has finished, the next request is a new execution.
	stages.mu.Lock()
	stages.gate = nil
	stages.mu.Unlock()
	third, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{Actor: "alice"})
	if err != nil || third.Coalesced || third.OperationID == first.OperationID {
		t.Errorf("a request after the test finished: %+v, %v, want a new operation", third, err)
	}
	idle(t, f)
	if n := stages.ran(); n != 2 {
		t.Errorf("validator runs = %d, want 2 after a second, separate test", n)
	}
}

// --- the output ---

func TestRecoveryTestOutputIsWhatTheNewestTestPrintedAndNotRecordedBeforeAnyRan(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	stages := &stubStages{script: "pg-check.local.sh", output: []byte("restore ok\n\x1b[31mred\x1b[0m\n")}
	withStages(f, stages)
	seedComplete(t, f, "out.dump", state.MediumLocal)

	none, err := f.b.RecoveryTestOutput(ctx, "production/postgres")
	if err != nil || none.Recorded || none.BackupSetID != "production/postgres" || len(none.Records) != 0 {
		t.Fatalf("before any test: %+v, %v, want recorded=false and no records", none, err)
	}

	started, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	idle(t, f)

	got, err := f.b.RecoveryTestOutput(ctx, "production/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Recorded || got.OperationID != started.OperationID || got.State != state.RecoveryPassed || got.Medium != state.MediumLocal || got.ArtifactID != "production/postgres/out.dump" {
		t.Errorf("header = %+v", got)
	}
	if len(got.Records) != 2 || got.Records[0].Text != "restore ok" || got.Records[1].Text != "�[31mred�[0m" || got.Records[0].Kind != "output" {
		t.Errorf("records = %+v, want the two lines with the escape bytes made inert", got.Records)
	}
	if got.RecordedAt.IsZero() {
		t.Error("RecordedAt is zero")
	}

	if _, err := f.b.RecoveryTestOutput(ctx, "production/nope"); !errors.Is(err, ErrBackupSetNotFound) {
		t.Errorf("an unknown set: err = %v, want ErrBackupSetNotFound", err)
	}
	if _, err := f.b.RecoveryTestOutput(ctx, "nope"); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a malformed set id: err = %v, want ErrInvalidRequest", err)
	}
}

// The service never hands out anything the journal would not store: a
// truncated capture reaches a reader as the runner's record, last.
func TestRecoveryTestOutputOfACutCaptureEndsWithTheRunnersTruncationRecord(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{cfg: requireRecoveryTest})
	stages := &stubStages{script: "pg-check.local.sh", output: []byte(strings.Repeat("noise\n", 3000))}
	withStages(f, stages)
	seedComplete(t, f, "big.dump", state.MediumLocal)
	if _, err := f.b.StartRecoveryTest(ctx, "production/postgres", RecoveryTestOptions{}); err != nil {
		t.Fatal(err)
	}
	idle(t, f)
	got, err := f.b.RecoveryTestOutput(ctx, "production/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Records) > state.MaxRecoveryOutputRecords {
		t.Fatalf("truncated=%v with %d records, want the bound applied and reported", got.Truncated, len(got.Records))
	}
	last := got.Records[len(got.Records)-1]
	if last.Kind != "truncation" || last.Text != state.RecoveryOutputTruncationText {
		t.Errorf("last record = %+v, want the runner's truncation record", last)
	}
	for _, r := range got.Records[:len(got.Records)-1] {
		if r.Kind == "truncation" {
			t.Errorf("a truncation record that is not last: %+v", r)
		}
	}
}
