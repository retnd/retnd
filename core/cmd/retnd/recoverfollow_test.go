package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/service"
)

// `--follow` on `recover` and `recovery-test` (EPIC Q, #1059; W-09).
//
// The claim is that the terminal and the browser tell one operation's story
// in the same words and the same order. These tests hold the terminal's half:
// the stage names `--follow` prints equal, in order, the stage names the
// engine emitted for the same kind of operation, read from the engine's own
// feed in the same test. That feed is recorded in
// testdata/recovery-follow-feed.json (see recordedFeedPath), so the browser's
// half can be held against the same list.

// recordedFeedPath is the recorded engine feed of a recovery and of a
// recovery test, written by TestRecordedRecoveryFeedIsCurrent when
// UPDATE_RECOVERY_FEED=1 and checked against a fresh recording otherwise.
//
// W-12a (the browser's follow-parity proof) reads this same file from the UI
// side: ui/shared tests import it as
// core/cmd/retnd/testdata/recovery-follow-feed.json and assert that the
// stage names the page renders for a followed operation equal the file's
// "stages" arrays, in order. Keep the shape stable: {"_doc", "recover":
// {"stages", "events"}, "recovery_test": {"stages", "events"}}.
const recordedFeedPath = "testdata/recovery-follow-feed.json"

type recordedEvent struct {
	Event      string `json:"event"`
	Stage      string `json:"stage,omitempty"`
	StageLabel string `json:"stage_label,omitempty"`
	Result     string `json:"result,omitempty"`
}

type recordedOp struct {
	Stages []string        `json:"stages"`
	Events []recordedEvent `json:"events"`
}

type recordedFeed struct {
	Doc          string     `json:"_doc"`
	Recover      recordedOp `json:"recover"`
	RecoveryTest recordedOp `json:"recovery_test"`
}

const recordedFeedDoc = "The activity-feed lines the engine emitted for one recovery and one recovery test of a local copy, in the order it emitted them: start, then each stage, then end. 'stages' is the stage ids alone. The CLI's --follow (core/cmd/retnd/recoverfollow_test.go) must print exactly these stages in this order, and the browser's follow (W-12a) must render the same. Regenerate with UPDATE_RECOVERY_FEED=1 go test ./cmd/retnd -run TestRecordedRecoveryFeedIsCurrent."

// engineFeed runs op against a freshly opened service on configPath and
// returns the lines the engine put on its own feed for the first operation
// that started, in emission order. It reads the service's feed directly, not
// through any CLI code.
func engineFeed(t *testing.T, configPath string, op func(ctx context.Context, svc *service.BackupService)) recordedOp {
	t.Helper()
	ctx := context.Background()
	svc, closeFn, err := service.Open(ctx, configPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	defer func() { _ = closeFn() }()
	op(ctx, svc)
	la, err := svc.LiveActivity(ctx, service.LiveActivityRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var all []service.LiveActivityEvent
	for _, s := range la.Sets {
		all = append(all, s.Events...)
	}
	if la.Deployment != nil {
		all = append(all, la.Deployment.Events...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Sequence < all[j].Sequence })
	var rec recordedOp
	opID := ""
	for _, e := range all {
		if opID == "" && (e.Event == obs.EventRecoveryStart || e.Event == obs.EventRecoveryTestStart) {
			opID = e.ActionID
		}
		if opID == "" || e.ActionID != opID {
			continue
		}
		re := recordedEvent{Event: e.Event, Result: e.Result}
		for _, f := range e.Fields {
			switch f.Key {
			case "stage":
				re.Stage = f.Value
			case "stage_label":
				re.StageLabel = f.Value
			}
		}
		if e.Event == obs.EventRecoveryStage {
			rec.Stages = append(rec.Stages, re.Stage)
		}
		rec.Events = append(rec.Events, re)
	}
	if len(rec.Stages) == 0 {
		t.Fatalf("the engine emitted no stages for the operation; feed: %+v", all)
	}
	return rec
}

// stagesPrinted pulls the stage ids out of what --follow printed, in order.
func stagesPrinted(printed string) []string {
	var got []string
	for _, line := range strings.Split(printed, "\n") {
		if !strings.Contains(line, " "+obs.EventRecoveryStage+" ") {
			continue
		}
		for _, tok := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(tok, "stage="); ok {
				got = append(got, v)
			}
		}
	}
	return got
}

func recordFeeds(t *testing.T) recordedFeed {
	t.Helper()
	cfg, _ := committedArtifact(t)
	recover := engineFeed(t, cfg, func(ctx context.Context, svc *service.BackupService) {
		_, _, err := svc.RecoverNow(ctx, service.RecoverySession{Actor: "test"}, service.RecoveryRequest{
			ArtifactID: recoverID, Target: service.RecoveryTarget{Kind: "staging", Path: "rec/feed.dump"},
		}, nil)
		if err != nil {
			t.Fatalf("RecoverNow: %v", err)
		}
	})
	cfg2, _ := committedArtifact(t)
	rtest := engineFeed(t, cfg2, func(ctx context.Context, svc *service.BackupService) {
		set := strings.TrimSuffix(recoverID, "/backup.dump")
		id, err := model.ParseBackupSetID(set)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RunRecoveryTest(ctx, id, service.RecoveryTestOptions{}); err != nil {
			t.Fatalf("RunRecoveryTest: %v", err)
		}
	})
	return recordedFeed{Doc: recordedFeedDoc, Recover: recover, RecoveryTest: rtest}
}

func TestRecordedRecoveryFeedIsCurrent(t *testing.T) {
	fresh := recordFeeds(t)
	want, err := json.MarshalIndent(fresh, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_RECOVERY_FEED") == "1" {
		if err := os.WriteFile(recordedFeedPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(recordedFeedPath)
	if err != nil {
		t.Fatalf("the recorded feed is missing (UPDATE_RECOVERY_FEED=1 writes it): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale against what the engine emits now (UPDATE_RECOVERY_FEED=1 rewrites it)\nfile:  %s\nfresh: %s", recordedFeedPath, got, want)
	}
}

func TestRecoverFollowPrintsTheEnginesStagesInOrderAndExitsZero(t *testing.T) {
	recorded := recordFeeds(t).Recover
	cfg, local := committedArtifact(t)
	_ = local
	var code int
	printed := captureStderr(t, func() {
		code = run([]string{"recover", "--config", cfg, recoverID, "--to", "rec/followed.dump", "--follow"})
	})
	if code != 0 {
		t.Fatalf("recover --follow = %d; stderr %q", code, printed)
	}
	got := stagesPrinted(printed)
	if fmt.Sprint(got) != fmt.Sprint(recorded.Stages) {
		t.Fatalf("--follow printed stages %v, the engine emitted %v", got, recorded.Stages)
	}
	if last := got[len(got)-1]; last != "complete" {
		t.Fatalf("the last stage printed is %q, want complete", last)
	}
}

func TestRecoverFollowOfACorruptedCopyExitsWithTheRecoverysOwnCodeAndNeverPrintsComplete(t *testing.T) {
	cfg, local := committedArtifact(t)
	data, _ := os.ReadFile(local)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(local, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	printed := captureStderr(t, func() {
		code = run([]string{"recover", "--config", cfg, recoverID, "--to", "rec/bad.dump", "--follow"})
	})
	if want := recoverExitCodes[service.RecoveryLocalCopyCorrupt]; code != want {
		t.Fatalf("recover --follow of a corrupt copy = %d, want %d; stderr %q", code, want, printed)
	}
	for _, s := range stagesPrinted(printed) {
		if s == "complete" {
			t.Fatalf("a failed recovery printed the complete stage: %q", printed)
		}
	}
	if !strings.Contains(printed, obs.EventRecoveryEnd) {
		t.Fatalf("the failed recovery's end line was not printed: %q", printed)
	}
}

func TestRecoveryTestFollowPrintsTheEnginesStagesInOrderAndExitsWithTheTestsResult(t *testing.T) {
	recorded := recordFeeds(t).RecoveryTest
	cfg, _ := committedArtifact(t)
	cfg2, _ := committedArtifact(t)
	var code int
	printed := captureStderr(t, func() {
		code = run([]string{"recovery-test", "--config", cfg, strings.TrimSuffix(recoverID, "/backup.dump"), "--follow"})
	})
	// This fixture declares no recovery-test validator, so the test cannot
	// pass and the verb exits 1 with or without the flag. What --follow
	// must not do is change that: the exit status is the test's own.
	plain := run([]string{"recovery-test", "--config", cfg2, strings.TrimSuffix(recoverID, "/backup.dump")})
	if code != plain || code == 0 {
		t.Fatalf("recovery-test --follow = %d and without the flag = %d; stderr %q", code, plain, printed)
	}
	got := stagesPrinted(printed)
	if fmt.Sprint(got) != fmt.Sprint(recorded.Stages) {
		t.Fatalf("--follow printed stages %v, the engine emitted %v", got, recorded.Stages)
	}
}

// Without the flag nothing is printed to standard error beyond what the verb
// printed before: --follow is opt-in.
func TestRecoverWithoutFollowPrintsNoStageLines(t *testing.T) {
	cfg, _ := committedArtifact(t)
	printed := captureStderr(t, func() {
		if code := run([]string{"recover", "--config", cfg, recoverID, "--to", "rec/plain.dump"}); code != 0 {
			t.Fatalf("recover = %d", code)
		}
	})
	if got := stagesPrinted(printed); len(got) != 0 {
		t.Fatalf("stage lines printed without --follow: %v", got)
	}
}
