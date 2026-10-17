package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/state"
)

// #1200: `retnd assurance show <op>` is the Operation page's twin. It reads
// one scrub's or recovery test's lines back from the durable activity record,
// with nothing serving and through a serving engine alike, and says how the
// run ended in the page's own words.

// seedRun writes a failed recovery test's durable rows (start, the one stage
// row the writer keeps, end) into the deployment's journal.
func seedRun(t *testing.T, configPath, opID string) {
	t.Helper()
	ctx := context.Background()
	j, err := state.Open(ctx, journalNamedByTestConfig(t, configPath))
	if err != nil {
		t.Fatalf("opening the test deployment's journal: %v", err)
	}
	defer func() { _ = j.Close() }()
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	fields := func(extra string) string {
		return `[{"k":"op_id","v":"` + opID + `"},{"k":"backup_set","v":"` + rtSet + `"}` + extra + `]`
	}
	rows := []state.ActivityEventRow{
		{Seq: 501, At: at, Level: "info", Event: "recovery_test_start", Scope: "set", SetID: rtSet, OpID: opID,
			Message: "recovery test starting", Action: "recovery_test", ActionID: opID, Fields: fields(`,{"k":"artifact","v":"` + rtSet + `/backup.dump"}`)},
		{Seq: 502, At: at.Add(9 * time.Second), Level: "info", Event: "recovery_stage", Scope: "set", SetID: rtSet, OpID: opID,
			Message: "recovery stage", Action: "recovery_test", ActionID: opID, Fields: fields(`,{"k":"stage","v":"verifying"},{"k":"stage_label","v":"Verifying"}`), Coalesce: true},
		{Seq: 503, At: at.Add(10 * time.Second), Level: "error", Event: "recovery_test_end", Scope: "set", SetID: rtSet, OpID: opID,
			Message: "recovery test finished", Result: "error", Action: "recovery_test", ActionID: opID, Fields: fields(`,{"k":"reason","v":"sha256 differs"}`)},
	}
	if err := j.AppendActivityEvents(ctx, rows); err != nil {
		t.Fatalf("seeding the activity record: %v", err)
	}
}

func TestAssuranceShowReadsAFinishedRunFromTheJournal(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	seedRun(t, cfg, "op-seed")
	var code int
	out := captureStdout(t, func() { code = run([]string{"assurance", "show", "--config", cfg, "op-seed"}) })
	if code != 0 {
		t.Fatalf("assurance show = %d; stdout %q", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "Recovery test failed: "+rtSet+" (op-seed)" {
		t.Errorf("heading = %q", lines[0])
	}
	for _, want := range []string{"stage:  Verifying (verifying)", "reason: sha256 differs"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	// The lines, oldest first, in the --follow format.
	var events []string
	for _, l := range lines {
		for _, name := range []string{"recovery_test_start", "recovery_stage", "recovery_test_end"} {
			if strings.Contains(l, " "+name+" ") {
				events = append(events, name)
			}
		}
	}
	if strings.Join(events, ",") != "recovery_test_start,recovery_stage,recovery_test_end" {
		t.Errorf("lines = %v, want start, stage, end:\n%s", events, out)
	}
	if !strings.Contains(out, "ERROR error") {
		t.Errorf("the end line does not carry its level and result the way --follow prints them:\n%s", out)
	}
}

// The page also shows the validator's output; the record does not carry it,
// so the verb says where to get it rather than leaving a gap that looks like
// there was none (#1200 review).
func TestAssuranceShowOfARecoveryTestPointsAtTheValidatorOutput(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	seedRun(t, cfg, "op-seed")
	out := captureStdout(t, func() { run([]string{"assurance", "show", "--config", cfg, "op-seed"}) })
	if want := "validator output: retnd recovery-test " + rtSet + " --show-output"; !strings.Contains(out, want) {
		t.Errorf("stdout lacks %q:\n%s", want, out)
	}
}

func TestAssuranceShowJSONIsTheContractObject(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	seedRun(t, cfg, "op-seed")
	var code int
	out := captureStdout(t, func() { code = run([]string{"assurance", "show", "--config", cfg, "--json", "op-seed"}) })
	if code != 0 {
		t.Fatalf("= %d; %q", code, out)
	}
	var got apicontract.AssuranceOperationRecord
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not the contract's AssuranceOperationRecord: %v\n%s", err, out)
	}
	if got.OperationID != "op-seed" || got.Kind != "recovery_test" || got.State != "failed" || got.Stage != "verifying" ||
		got.Reason != "sha256 differs" || got.BackupSetID != rtSet || len(got.Events) != 3 || got.StartedAt == "" || got.EndedAt == "" {
		t.Errorf("got %+v", got)
	}
}

func TestAssuranceShowOfAnIDWithNothingOnRecordSaysWhy(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	seedRun(t, cfg, "op-seed")
	var code int
	errOut := captureStderr(t, func() { code = run([]string{"assurance", "show", "--config", cfg, "op-pruned"}) })
	if code != 1 {
		t.Fatalf("= %d; stderr %q, want 1", code, errOut)
	}
	for _, want := range []string{"op-pruned", "nothing is on record", "30 days"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr %q lacks %q", errOut, want)
		}
	}
}

func TestAssuranceShowNeedsExactlyOneOperationID(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	seedRun(t, cfg, "op-seed")
	var ok int
	captureStdout(t, func() { ok = run([]string{"assurance", "show", "--config", cfg, "op-seed"}) })
	if ok != 0 {
		t.Fatalf("the control, one id, = %d", ok)
	}
	for _, args := range [][]string{{"assurance", "show", "--config", cfg}, {"assurance", "show", "--config", cfg, "a", "b"}} {
		var code int
		errOut := captureStderr(t, func() { code = run(args) })
		if code != exitUsage {
			t.Errorf("%v = %d (stderr %q), want the usage exit %d", args, code, errOut, exitUsage)
		}
	}
}

// Attached, the engine answers, and the same run prints the same way.
func TestAssuranceShowAsksTheServingEngineWhenAttached(t *testing.T) {
	cli := writeTestConfig(t)
	seedRun(t, cli, "op-seed")
	clearInheritedRouteSettings()
	direct := captureStdout(t, func() { run([]string{"assurance", "show", "--config", cli, "op-seed"}) })

	e := startFakeEngine(t, cli)
	e.attach(t)
	var code int
	out := captureStdout(t, func() { code = run([]string{"assurance", "show", "--config", cli, "op-seed"}) })
	if code != 0 {
		t.Fatalf("attached assurance show = %d; stdout %q", code, out)
	}
	if !containsRequest(e.requests(), "GET /assurance/operations/op-seed") {
		t.Errorf("the engine saw %v, want the read route", e.requests())
	}
	if out != direct {
		t.Errorf("attached and direct disagree:\nattached:\n%s\ndirect:\n%s", out, direct)
	}
	var missing int
	errOut := captureStderr(t, func() { missing = run([]string{"assurance", "show", "--config", cli, "op-pruned"}) })
	if missing != 1 || !strings.Contains(errOut, "nothing is on record") {
		t.Errorf("attached, an id with nothing on record = %d, stderr %q", missing, errOut)
	}
}

func TestUsageListsAssuranceShow(t *testing.T) {
	out := captureStderr(t, usage)
	if !strings.Contains(out, "  assurance show <operation-id> [--json]") {
		t.Error("usage() lacks `assurance show <operation-id> [--json]`")
	}
}
