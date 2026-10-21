package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
)

// #1250's review: the unhappy paths of `operation run` and of every follow.
// A slow POST, one failed poll in an hours-long --follow, and a Ctrl-C all
// used to read as a failed run while the engine carried on, and the key that
// would have made a retry safe was thrown away.

// The codes a script branches on, as literals for gateShutExit's reason.
const (
	lostTrackExit         = 51
	insecureTransportExit = 52
)

// fastRetries shortens every wait the submit and the follow sit out, so a
// test about giving up does not spend minutes doing it.
func fastRetries(t *testing.T) {
	t.Helper()
	fastPolls(t)
	oldWait, oldDeadline := operationSubmitRetryWait, operationSubmitDeadline
	oldPatience, oldRetry := operationFollowPatience, operationFollowRetryMax
	operationSubmitRetryWait = 5 * time.Millisecond
	operationSubmitDeadline = 2 * time.Second
	operationFollowPatience = 200 * time.Millisecond
	operationFollowRetryMax = 10 * time.Millisecond
	t.Cleanup(func() {
		operationSubmitRetryWait, operationSubmitDeadline = oldWait, oldDeadline
		operationFollowPatience, operationFollowRetryMax = oldPatience, oldRetry
	})
}

// dropFirst answers POST /operations by cutting the connection after
// reading the body, n times, and then the way answer says. It is the reply
// that went missing after the engine had already written the row.
func dropFirst(rec *submissions, n int32, answer func(http.ResponseWriter, map[string]any)) http.HandlerFunc {
	var seen atomic.Int32
	return rec.handler(func(w http.ResponseWriter, body map[string]any) {
		if seen.Add(1) <= n {
			panic(http.ErrAbortHandler)
		}
		answer(w, body)
	})
}

// M2: a lost reply is resent with the SAME key, which the engine dedupes,
// so the operator gets the run that started rather than a failure.
func TestOperationRunReview1250_ALostReplyIsResentWithTheSameKey(t *testing.T) {
	fastRetries(t)
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	// Two, because net/http itself replays a keyed request once on a
	// reused connection; the second drop is the one this command has to
	// handle on its own.
	e.handle("POST /operations", dropFirst(rec, 2, accepted(queuedRun("op-lost", "run_cycle", ""))))

	code, out, errOut := runOp(t, cfg, "operation", "run")
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	got := rec.all()
	if len(got) < 3 {
		t.Fatalf("the engine saw %d submissions, want the two lost ones and the one answered", len(got))
	}
	for _, s := range got[1:] {
		if s.key != got[0].key {
			t.Errorf("a resend carried key %q after %q, so the engine would start a second run", s.key, got[0].key)
		}
	}
	if !strings.Contains(out, "op-lost") {
		t.Errorf("stdout does not name the run the engine started: %q", out)
	}
}

func TestOperationRunReview1250_ASubmitThatNeverHearsBackSaysTheRunMayHaveStarted(t *testing.T) {
	fastRetries(t)
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", dropFirst(rec, 1000, accepted(queuedRun("never", "run_cycle", ""))))

	code, _, errOut := runOp(t, cfg, "operation", "run")
	if code != lostTrackExit {
		t.Fatalf("exit %d, want %d (the run may be going)\n%s", code, lostTrackExit, errOut)
	}
	if !strings.Contains(errOut, "operation list") || !strings.Contains(errOut, "may") {
		t.Errorf("stderr does not say the run may have started and where to look: %q", errOut)
	}
}

// M1 and M3: a poll that fails does not lose the id, and transient
// failures are sat out rather than reported as a failed run.
func TestOperationFollowReview1250_TransientPollFailuresAreSatOut(t *testing.T) {
	fastRetries(t)
	for _, verb := range [][]string{{"operation", "show", "op-t", "--follow"}, {"operation", "run", "--follow"}} {
		t.Run(verb[1], func(t *testing.T) {
			cfg := writeTestConfig(t)
			e := startFakeEngine(t, cfg)
			e.attach(t)
			rec := &submissions{}
			e.handle("POST /operations", rec.handler(accepted(queuedRun("op-t", "run_cycle", ""))))
			var polls atomic.Int32
			e.handle("GET /operations/op-t", func(w http.ResponseWriter, _ *http.Request) {
				switch polls.Add(1) {
				case 1:
					writeJSON(w, http.StatusOK, opOf("op-t", "run_cycle", "", "running"))
				case 2:
					panic(http.ErrAbortHandler)
				case 3:
					refuse(w, http.StatusInternalServerError, apicontract.ErrorCodeInternal, "busy")
				default:
					writeJSON(w, http.StatusOK, opOf("op-t", "run_cycle", "", "completed"))
				}
			})
			code, out, errOut := runOp(t, cfg, verb...)
			if code != 0 {
				t.Fatalf("exit %d, want 0 once the run completed\nstdout: %s\nstderr: %s", code, out, errOut)
			}
		})
	}
}

func TestOperationFollowReview1250_GivingUpNamesTheOperationAndHasItsOwnCode(t *testing.T) {
	fastRetries(t)
	for _, verb := range [][]string{{"operation", "show", "op-g", "--follow"}, {"operation", "run", "--follow"}} {
		t.Run(verb[1], func(t *testing.T) {
			cfg := writeTestConfig(t)
			e := startFakeEngine(t, cfg)
			e.attach(t)
			rec := &submissions{}
			e.handle("POST /operations", rec.handler(accepted(queuedRun("op-g", "run_cycle", ""))))
			var polls atomic.Int32
			e.handle("GET /operations/op-g", func(w http.ResponseWriter, _ *http.Request) {
				if polls.Add(1) == 1 {
					writeJSON(w, http.StatusOK, opOf("op-g", "run_cycle", "", "running"))
					return
				}
				refuse(w, http.StatusInternalServerError, apicontract.ErrorCodeInternal, "busy")
			})
			code, _, errOut := runOp(t, cfg, verb...)
			if code != lostTrackExit {
				t.Fatalf("exit %d, want %d\n%s", code, lostTrackExit, errOut)
			}
			if !strings.Contains(errOut, "op-g") {
				t.Errorf("stderr does not name the operation to pick back up: %q", errOut)
			}
			if strings.Contains(errOut, "reading :") {
				t.Errorf("the id was lost from the message: %q", errOut)
			}
		})
	}
}

// An engine that restarted forgets its in-memory operations, so a 404 in
// the middle of a follow is losing track, not a failed run.
func TestOperationFollowReview1250_AnOperationThatVanishesIsLostTrack(t *testing.T) {
	fastRetries(t)
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	var polls atomic.Int32
	e.handle("GET /operations/op-v", func(w http.ResponseWriter, _ *http.Request) {
		if polls.Add(1) == 1 {
			writeJSON(w, http.StatusOK, opOf("op-v", "run_cycle", "", "running"))
			return
		}
		refuse(w, http.StatusNotFound, apicontract.ErrorCodeOperationNotFound, "operation op-v not found")
	})
	code, _, errOut := runOp(t, cfg, "operation", "show", "op-v", "--follow")
	if code != lostTrackExit || !strings.Contains(errOut, "op-v") {
		t.Fatalf("exit %d, want %d naming op-v\n%s", code, lostTrackExit, errOut)
	}
}

// A follow that is handed an operation with no id has nothing to poll, and
// must say so instead of asking for GET /operations/.
func TestOperationFollowReview1250_AnOperationWithNoIDIsRefusedUpFront(t *testing.T) {
	fastRetries(t)
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(accepted(opOf("", "run_cycle", "", "queued"))))
	code, _, errOut := runOp(t, cfg, "operation", "run", "--follow")
	if code == 0 {
		t.Fatalf("an operation with no id was followed: %s", errOut)
	}
	if containsRequest(e.requests(), "GET /operations/") {
		t.Errorf("the follow asked GET /operations/ with no id: %v", e.requests())
	}
}

// Ctrl-C ends the follow and not the run, and says so.
func TestOperationFollowReview1250_CtrlCStopsFollowingNotTheRun(t *testing.T) {
	fastPolls(t)
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	readings := []apicontract.Operation{}
	for range 400 {
		readings = append(readings, opOf("op-c", "run_cycle", "", "running"))
	}
	e.scriptOperation("op-c", append(readings, opOf("op-c", "run_cycle", "", "completed"))...)
	interruptOnceSeen(t, e, "GET /operations/op-c")

	code, _, errOut := runOp(t, cfg, "operation", "show", "op-c", "--follow")
	if code != lostTrackExit {
		t.Fatalf("Ctrl-C exited %d, want %d\n%s", code, lostTrackExit, errOut)
	}
	if !strings.Contains(errOut, "op-c") || !strings.Contains(errOut, "engine") {
		t.Errorf("stderr does not say the run carries on in the engine: %q", errOut)
	}
}

// interruptOnceSeen sends this process SIGINT the first time the engine
// sees route. A channel of its own is registered first, so that before a
// command handles the signal it is swallowed rather than killing the test
// binary.
func interruptOnceSeen(t *testing.T, e *fakeEngine, route string) {
	t.Helper()
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); signal.Stop(guard) })
	go func() {
		for ctx.Err() == nil {
			if containsRequest(e.requests(), route) {
				_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
}

// M4: the help no longer sends anyone to a flag fetch refuses, and a shut
// gate does not suggest a local run that would run the skipped scripts.
func TestOperationRunReview1250_SkipWorkflowScriptsIsDescribedHonestly(t *testing.T) {
	cfg := writeTestConfig(t)
	_, _, help := runOp(t, cfg, "operation", "run", "-h")
	if strings.Contains(help, "as fetch --skip-workflow-scripts does") {
		t.Errorf("the help points at a flag fetch refuses:\n%s", help)
	}
	if !strings.Contains(help, "records the bypass") {
		t.Errorf("the help does not say the engine records the bypass:\n%s", help)
	}

	e := startFakeEngine(t, cfg)
	e.attach(t)
	rec := &submissions{}
	e.handle("POST /operations", rec.handler(func(w http.ResponseWriter, _ map[string]any) {
		refuse(w, http.StatusForbidden, apicontract.ErrorCodeDestructiveOperationsDisabled, "refused")
	}))
	code, _, errOut := runOp(t, cfg, "operation", "run", "--backup-set", "a/one", "--skip-workflow-scripts")
	if code != gateShutExit {
		t.Fatalf("exit %d, want %d", code, gateShutExit)
	}
	if strings.Contains(errOut, "fetch --backup-set") {
		t.Errorf("a shut gate suggested fetch, which runs the very scripts that were asked to be skipped: %q", errOut)
	}
	if !strings.Contains(errOut, "no local equivalent") {
		t.Errorf("stderr does not say there is no local equivalent: %q", errOut)
	}
}

// M5: 50 is the gate on every routed call, not on operation run alone, and
// the engine's insecure-transport refusal has a code of its own.
func TestRoutedReview1250_TheGateExitsFiftyOnEveryRoutedCall(t *testing.T) {
	for _, c := range []struct {
		code apicontract.ErrorCode
		exit int
	}{
		{apicontract.ErrorCodeDestructiveOperationsDisabled, gateShutExit},
		{apicontract.ErrorCodeInsecureTransportAckRequired, insecureTransportExit},
	} {
		t.Run(string(c.code), func(t *testing.T) {
			cliCfg := writeTestConfig(t)
			e := startFakeEngineFor(t, cliCfg)
			e.attach(t)
			e.handle("POST /assurance/validators", func(w http.ResponseWriter, _ *http.Request) {
				refuse(w, http.StatusForbidden, c.code, "refused for the test")
			})
			script := t.TempDir() + "/v.local.sh"
			if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			code, _, errOut := runOp(t, cliCfg, "assurance", "validators", "add", "v", "--pattern", "^a", "--script", script)
			if !containsRequest(e.requests(), "POST /assurance/validators") {
				t.Fatalf("the add never reached the engine (exit %d), so this row tested nothing: %s\n%v", code, errOut, e.requests())
			}
			if code != c.exit {
				t.Errorf("a routed validator add refused with %s exited %d, want %d\n%s", c.code, code, c.exit, errOut)
			}
		})
	}
}

// M5 and M6: the codes are in the usage table and on the site, under one
// reservation policy, and recover's real range is the one stated.
func TestExitCodesReview1250_TheEngineCodesAreDocumentedEverywhere(t *testing.T) {
	usageText := captureStderr(t, usage)
	for _, want := range []string{"  50  ", "  51  ", "  52  ", "10-49", "50 onwards"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("the usage exit table does not carry %q", want)
		}
	}
	if strings.Contains(usageText, "50  operation run only") {
		t.Error("the usage table still scopes 50 to operation run")
	}
	site, err := os.ReadFile("../../../docs/site/reference.html")
	if err != nil {
		t.Fatal(err)
	}
	exits := string(site)
	if i := strings.Index(exits, `id="cli-exits"`); i >= 0 {
		exits = exits[i:]
		if j := strings.Index(exits, "</table>"); j >= 0 {
			exits = exits[:j]
		}
	} else {
		t.Fatal("reference.html has no cli-exits table")
	}
	for _, want := range []string{"<code>50</code>", "<code>51</code>", "<code>52</code>", "10 to 49"} {
		if !strings.Contains(exits, want) {
			t.Errorf("reference.html#cli-exits does not carry %q", want)
		}
	}
}

// #1258 refused --run on the routed first configuration and named no verb,
// because operation run did not exist yet. The routed sentence names it now,
// for the set that was just created; the direct path keeps `retnd run`.
func TestFirstRunReview1250_TheRoutedRunRefusalNamesOperationRun(t *testing.T) {
	engine, cliConfig, dbPath := firstRunFixture(t)
	engine.attach(t)
	code, _, stderr := runCLI(t, createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath, "--run")...)
	if code != exitUsage {
		t.Fatalf("--run on a routed first run exited %d, want %d\n%s", code, exitUsage, stderr)
	}
	if want := cliecho.Binary + " operation run --backup-set api/postgres"; !strings.Contains(stderr, want) {
		t.Errorf("the routed --run refusal does not name %q:\n%s", want, stderr)
	}
}
