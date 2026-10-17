package workflowrun

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/hostrunner"
	"github.com/retnd/retnd/core/internal/workflow"
	"github.com/retnd/retnd/core/internal/workflowexec"
)

// A Host Workflow Runner that stops answering while a hook is in flight
// (#931). The step's script_timeout is the only thing that can end that
// wait, so the run has to FINALISE inside it rather than sit in progress
// where nothing will schedule the set again and no hold explains why.
//
// The runner is a raw socket that welcomes, takes the request, says one
// line of output and then holds the connection open and silent. That is a
// stopped runner, not a dead one: a dead one closes the socket and the
// kernel reports it at once, which was never the problem.

const vanishBound = 700 * time.Millisecond

// slack is generous on purpose: MARS runs these under load.
const vanishSlack = 8 * time.Second

func silentRunner(t *testing.T, welcome bool) hostrunner.Client {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "bdw")
	if err != nil {
		t.Fatalf("preparing a short temporary root: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	path := filepath.Join(dir, "r.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := hostrunner.ReadMessage(conn); err != nil {
					return
				}
				if welcome {
					_ = hostrunner.WriteMessage(conn, hostrunner.Message{Kind: hostrunner.KindWelcome, Welcome: &hostrunner.Welcome{Protocol: hostrunner.Protocol, Version: "test"}})
					if _, err := hostrunner.ReadMessage(conn); err != nil {
						return
					}
					_ = hostrunner.WriteMessage(conn, hostrunner.Message{Kind: hostrunner.KindChunk, Chunk: &hostrunner.Chunk{Stream: hostrunner.StreamStdout, Seq: 1, Data: []byte("flushing tables\n")}})
				}
				buf := make([]byte, 64)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()

	return hostrunner.Client{SocketPath: path, Version: "test", Token: strings.Repeat("a", 64)}
}

func TestRunHooks_AHookWhoseRunnerGoesSilentIsBoundedByItsTimeout(t *testing.T) {
	for name, welcome := range map[string]bool{"silent after the request": true, "silent before the welcome": false} {
		t.Run(name, func(t *testing.T) {
			tr := newTree(t, map[stage][]string{setBefore: {"10-quiesce.local.sh", "20-next.local.sh"}})
			tr.timeout = vanishBound
			eng := &Engine{Local: LocalExecutor{Client: silentRunner(t, welcome)}, Now: newClock().Now}

			type ans struct {
				res HookRunResult
				err error
			}
			done := make(chan ans, 1)
			start := time.Now()
			go func() {
				res, err := eng.RunHooks(context.Background(), tr.snapshot(t, "vanish-hooks"), HookRunRequest{})
				done <- ans{res, err}
			}()

			var got ans
			select {
			case got = <-done:
			case <-time.After(vanishSlack):
				t.Fatalf("RunHooks was still blocked %s after a %s step bound", vanishSlack, vanishBound)
			}
			if got.err != nil {
				t.Fatalf("RunHooks: %v", got.err)
			}
			if elapsed := time.Since(start); elapsed < vanishBound/2 {
				t.Errorf("returned after %s, inside the bound: it did not wait for the runner at all", elapsed)
			}
			if got.res.AllSucceeded() || len(got.res.Steps) != 2 {
				t.Fatalf("result = %+v", got.res)
			}

			first, second := got.res.Steps[0], got.res.Steps[1]
			if first.State != workflow.StateTimedOut || first.Outcome.Disposition != DispositionTimedOut {
				t.Errorf("first step: state %q disposition %q, want timed_out: the step's own bound expired and the result must say so, not call it a lost transport", first.State, first.Outcome.Disposition)
			}
			if first.Outcome.ExitCode != nil {
				t.Errorf("a step that never reported carries exit code %d", *first.Outcome.ExitCode)
			}
			if first.Outcome.Certainty != workflowexec.TerminationUnconfirmed {
				t.Errorf("termination certainty %q: nothing proved the hook stopped, because the runner that would have done it is the one that went quiet", first.Outcome.Certainty)
			}
			if welcome && !strings.Contains(string(first.Output), "flushing tables") {
				t.Errorf("output printed before the runner went quiet was lost: %q", first.Output)
			}
			if second.State != workflow.StateSkipped {
				t.Errorf("the step after a timed-out one is %q, want skipped (fail closed)", second.State)
			}
		})
	}
}

// TestRun_ARunWhoseRunnerGoesSilentFinalisesInsideTheBound is the
// regression guard for the acceptance line in #931: the run must reach a
// terminal state, the backup must not run, and the journal must not be
// left saying the step is still running with nothing to act on.
func TestRun_ARunWhoseRunnerGoesSilentFinalisesInsideTheBound(t *testing.T) {
	h := newHarness(t)
	h.engine.Local = LocalExecutor{Client: silentRunner(t, true)}

	tr := newTree(t, map[stage][]string{
		setBefore: {"10-quiesce.local.sh"},
		setAfter:  {"90-resume.local.sh"},
	})
	tr.timeout = vanishBound
	plan := tr.snapshot(t, "vanish-run")

	type ans struct {
		res RunResult
		err error
	}
	done := make(chan ans, 1)
	go func() {
		res, err := h.run(t, plan)
		done <- ans{res, err}
	}()

	var got ans
	select {
	case got = <-done:
	case <-time.After(vanishSlack):
		t.Fatalf("Run was still in progress %s after a %s step bound: the set is wedged with no run and no hold", vanishSlack, vanishBound)
	}
	if got.err != nil {
		t.Fatalf("Run: %v", got.err)
	}

	if got.res.WorkflowStatus != workflow.StatusFailed {
		t.Errorf("workflow status %q, want failed", got.res.WorkflowStatus)
	}
	if got.res.BackupStatus != workflow.StatusSkipped {
		t.Errorf("backup status %q: the backup ran although its before hook never reported", got.res.BackupStatus)
	}
	if got.res.FailedStep == "" {
		t.Error("no failed step named: an operator has nothing to start from")
	}
	switch got.res.State {
	case workflow.StateRunning, "":
		t.Errorf("run state %q: not terminal", got.res.State)
	}

	step := stateOf(t, h.store, "vanish-run", "10-quiesce.local.sh")
	if workflow.State(step.State) != workflow.StateTimedOut {
		t.Errorf("journal says the step is %q, want timed_out", step.State)
	}

	// Either the run is wholly accounted for, or a hold is outstanding
	// for the operator. "No run and no hold" is the one outcome that is
	// not allowed, and a finalised run satisfies it by being finalised.
	for _, s := range h.rec.dispatched() {
		if s == "backup" {
			t.Error("the backup ran")
		}
	}
}
