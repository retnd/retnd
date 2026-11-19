package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #980 and #1071: `retnd assurance cancel <op>` and
// `retnd recovery-test <set> --show-output`.

const rtSet = "production/postgres-primary"

func seedOutput(t *testing.T, configPath string, records ...state.RecoveryOutputRecord) {
	t.Helper()
	ctx := context.Background()
	j, err := state.Open(ctx, journalNamedByTestConfig(t, configPath))
	if err != nil {
		t.Fatalf("opening the test deployment's journal: %v", err)
	}
	defer func() { _ = j.Close() }()
	set, err := model.ParseBackupSetID(rtSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.RecordRecoveryOutput(ctx, state.RecoveryOutput{
		Set: set, OperationID: "op-seeded", Artifact: rtSet + "/backup.dump", Medium: state.MediumLocal,
		State: state.RecoveryFailed, RecordedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		Truncated: len(records) > 0 && records[len(records)-1].Kind == state.RecoveryOutputKindTruncation, Records: records,
	}); err != nil {
		t.Fatalf("seeding the output: %v", err)
	}
}

func TestAssuranceCancelBesideNoServingEngineSaysThereIsNothingToCancel(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	var code int
	errOut := captureStderr(t, func() { code = run([]string{"assurance", "cancel", "--config", cfg, "op-1"}) })
	if code == 0 || code == exitUsage {
		t.Fatalf("assurance cancel with no engine = %d; stderr %q, want a refusal that is not a usage error", code, errOut)
	}
	if !strings.Contains(errOut, "RETND_API_URL") {
		t.Errorf("stderr %q does not say how to reach the engine that holds the operation", errOut)
	}
}

func TestAssuranceCancelOfAnOperationTheEngineDoesNotHoldReportsItsRefusal(t *testing.T) {
	cli := writeTestConfig(t)
	e := startFakeEngine(t, cli)
	e.attach(t)

	var code int
	errOut := captureStderr(t, func() { code = run([]string{"assurance", "cancel", "--config", cli, "no-such-op"}) })
	if code != 1 {
		t.Fatalf("assurance cancel of an unknown operation = %d; stderr %q, want 1", code, errOut)
	}
	if !strings.Contains(errOut, "no-such-op") || !strings.Contains(strings.ToLower(errOut), "not found") {
		t.Errorf("stderr %q does not name the operation the engine could not find", errOut)
	}
	if got := e.requests(); !containsRequest(got, "POST /assurance/operations/no-such-op/cancel") {
		t.Errorf("the engine saw %v, want the cancel route", got)
	}
}

func TestAssuranceCancelNeedsExactlyOneOperationId(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	// The control: one operation id is a well-formed request (it is refused
	// for having no engine, which is not a usage error), so the usage exits
	// below are about the arguments and not about the verb being unknown.
	var ok int
	captureStderr(t, func() { ok = run([]string{"assurance", "cancel", "--config", cfg, "op-1"}) })
	if ok == exitUsage {
		t.Fatalf("assurance cancel op-1 is a usage error: the verb does not exist")
	}
	for _, args := range [][]string{{"assurance"}, {"assurance", "cancel", "--config", cfg}, {"assurance", "cancel", "--config", cfg, "a", "b"}, {"assurance", "frobnicate", "--config", cfg, "a"}} {
		var code int
		errOut := captureStderr(t, func() { code = run(args) })
		if code != exitUsage {
			t.Errorf("%v = %d (stderr %q), want the usage exit %d", args, code, errOut, exitUsage)
		}
	}
}

func TestRecoveryTestShowOutputPrintsTheStoredOutputInertAndRunsNoTest(t *testing.T) {
	cfg, _ := committedArtifact(t)
	seedOutput(t, cfg,
		state.RecoveryOutputRecord{Kind: state.RecoveryOutputKindText, Text: "restore started"},
		state.RecoveryOutputRecord{Kind: state.RecoveryOutputKindText, Text: "bad \ufffd[31m escape"},
		state.RecoveryOutputRecord{Kind: state.RecoveryOutputKindTruncation, Text: state.RecoveryOutputTruncationText},
	)
	var code int
	out := captureStdout(t, func() { code = run([]string{"recovery-test", "--config", cfg, rtSet, "--show-output"}) })
	if code != 0 {
		t.Fatalf("--show-output = %d; stdout %q", code, out)
	}
	for _, want := range []string{"| restore started", "| bad \ufffd[31m escape", state.RecoveryOutputTruncationText, "op-seeded", "failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("a raw escape byte reached the terminal:\n%q", out)
	}
	// It read; it did not run a test, so the seeded record is still the newest.
	j, err := state.Open(context.Background(), journalNamedByTestConfig(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	set, _ := model.ParseBackupSetID(rtSet)
	got, ok, err := j.RecoveryOutputOf(context.Background(), set)
	if err != nil || !ok || got.OperationID != "op-seeded" {
		t.Errorf("after --show-output the newest output is %+v (ok=%v err=%v): the flag ran a test", got, ok, err)
	}
}

func TestRecoveryTestShowOutputJSONIsTheContractObject(t *testing.T) {
	cfg, _ := committedArtifact(t)
	seedOutput(t, cfg, state.RecoveryOutputRecord{Kind: state.RecoveryOutputKindText, Text: "one line"})
	var code int
	out := captureStdout(t, func() { code = run([]string{"recovery-test", "--config", cfg, rtSet, "--show-output", "--json"}) })
	if code != 0 {
		t.Fatalf("= %d; %q", code, out)
	}
	var got apicontract.RecoveryTestOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not the contract's RecoveryTestOutput: %v\n%s", err, out)
	}
	if !got.Recorded || got.BackupSetID != rtSet || got.OperationID != "op-seeded" || got.State != "failed" ||
		len(got.Records) != 1 || got.Records[0].Kind != "output" || got.Records[0].Text != "one line" {
		t.Errorf("got %+v", got)
	}
}

func TestRecoveryTestShowOutputBeforeAnyTestSaysSoAndSucceeds(t *testing.T) {
	cfg, _ := committedArtifact(t)
	var code int
	out := captureStdout(t, func() { code = run([]string{"recovery-test", "--config", cfg, rtSet, "--show-output"}) })
	if code != 0 || !strings.Contains(out, "no recovery test") {
		t.Errorf("= %d, stdout %q, want exit 0 and a line saying no recovery test has recorded output", code, out)
	}
}

func TestRecoveryTestShowOutputRefusesTheFlagsThatOnlyApplyToRunningATest(t *testing.T) {
	cfg, _ := committedArtifact(t)
	// The control: the flag on its own is a well-formed request.
	var ok int
	captureStdout(t, func() { ok = run([]string{"recovery-test", "--config", cfg, rtSet, "--show-output"}) })
	if ok != 0 {
		t.Fatalf("--show-output on its own = %d, so the refusals below could be about the flag not existing", ok)
	}
	for _, extra := range [][]string{{"--follow"}, {"--acknowledge-cost"}, {"--artifact", rtSet + "/backup.dump"}} {
		var code int
		args := append([]string{"recovery-test", "--config", cfg, rtSet, "--show-output"}, extra...)
		errOut := captureStderr(t, func() { code = run(args) })
		if code != exitUsage {
			t.Errorf("%v = %d (stderr %q), want the usage exit %d: it would run nothing and ignore the flag", extra, code, errOut, exitUsage)
		}
	}
}

// The usage block keeps every line it had, and the new verbs are added beside
// them (compat cell 06b pins the old lines; this pins the new ones).
func TestUsageListsTheNewVerbsBesideThePinnedLines(t *testing.T) {
	out := captureStderr(t, usage)
	for _, want := range []string{
		"  recovery-test <source/backup-set> [--artifact <source/backup-set/name>] [--acknowledge-cost] [--json]",
		"  recovery-test <source/backup-set> --show-output [--json]",
		"  assurance cancel <operation-id>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage() lacks the line %q", want)
		}
	}
}
