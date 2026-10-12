package workflowrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/workflow"
	"github.com/retnd/retnd/core/internal/workflowexec"
)

// RunHooks runs the scripts of a plan OUTSIDE a five-stage run (EPIC Q,
// #978 and #980).
//
// # Why this exists, and what it is not
//
// Two things in EPIC Q want an operator's script and are not backups: a
// recovery test hands a recovered copy to the operator's validator, and a
// scrub that keeps failing tells the operator through a script instead of
// through a built-in notifier. Both should run the way every other hook in
// this product runs, so they go through the same plan (discovery, custody,
// naming, size bound, bytes read once and hashed), the same executors (the
// host runner's hardened container for NAME.local.sh, the proven exec
// channel for NAME.remote.sh) and the same resolved environment.
//
// What they must not do is enter a workflow RUN. A run is a backup's
// five-stage lifecycle: it takes the set's lock for all five stages, writes
// a cleanup obligation before the first command and can block the set in
// recovery_required. None of that is true of a validator or a notifier. A
// notification script that took the set lock would stop backups while it
// ran, and a validator that left an obligation behind would put a backup
// set into recovery over a test that touched nothing. So there is no row
// here, no lock, no obligation and no recovery: the scripts run, the
// outcomes come back, and nothing is durable but what the caller records.
//
// # What it still does for the caller
//
// It dispatches by target and refuses, before running anything, a plan that
// needs an executor this engine does not have. It resolves the plan's
// environment once per step and layers that step's own secret values onto
// the redactor for the duration of the step, so a script that prints its
// credential does not hand it to the caller. It bounds what it keeps of a
// step's output and says when it cut it. And it stops at the first script
// that does not succeed unless the caller asked otherwise, recording the
// ones it did not run as skipped, never as absent: a list shorter than the
// plan is how a half-run plan looks complete.
//
// The Reconcile gate is deliberately not consulted. It exists so a backup
// cannot start over a machine a previous run left quiesced, and these are
// not backups.

// DefaultHookOutputBytes bounds how much of one script's output RunHooks
// keeps when the caller does not say.
const DefaultHookOutputBytes int64 = 16 << 10

// HookRunRequest tunes one RunHooks call.
type HookRunRequest struct {
	// ContinueOnFailure runs the remaining scripts after one that did not
	// succeed. A validator wants the default, because a second validator
	// running after the first rejected the copy is noise. A notifier wants
	// this, because one failing notification script must not silence the
	// others.
	ContinueOnFailure bool

	// MaxOutputBytes bounds the output kept per script. Zero takes
	// DefaultHookOutputBytes.
	MaxOutputBytes int64

	// Facts are the built-ins the engine cannot derive (RETND_SOURCE_HOST
	// and the like). They are only those the documented set contains.
	Facts map[string]string
}

// HookStepResult is one script's outcome.
type HookStepResult struct {
	Step    workflow.Step
	State   workflow.State
	Outcome StepOutcome

	// Output is the redacted output kept, bounded to MaxOutputBytes.
	// It is untrusted terminal input: a caller sanitises it before it
	// reaches any field, notification or terminal.
	Output          []byte
	OutputTruncated bool

	Duration time.Duration
}

// HookRunResult is the outcome of a whole plan.
type HookRunResult struct {
	// Steps has one entry per planned script, in plan order, including the
	// ones that were skipped.
	Steps []HookStepResult
}

// AllSucceeded reports whether every planned script ran and succeeded.
// A skipped script is not a success, so a result with a failure in it is
// never true however the caller read the rest.
func (r HookRunResult) AllSucceeded() bool {
	for _, s := range r.Steps {
		if s.State != workflow.StateSuccess {
			return false
		}
	}

	return len(r.Steps) > 0
}

// RunHooks runs plan's scripts in plan order. An error means the plan could
// not be run at all (no plan, a missing executor); a script that ran and
// failed is a result, not an error.
func (e *Engine) RunHooks(ctx context.Context, plan workflow.Plan, req HookRunRequest) (HookRunResult, error) {
	if plan.IsZero() {
		return HookRunResult{}, errors.New("workflowrun: RunHooks needs a plan, and the zero plan is what a run with no scripts looks like")
	}

	steps := plan.Steps()
	for _, step := range steps {
		if _, err := e.executorFor(step.Target); err != nil {
			return HookRunResult{}, fmt.Errorf("workflowrun: %s cannot be run, and nothing was started: %w", step.ScriptName, err)
		}
	}

	limit := req.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultHookOutputBytes
	}

	started := e.clock()
	out := HookRunResult{Steps: make([]HookStepResult, 0, len(steps))}
	stopped := false

	for _, step := range steps {
		if stopped || ctx.Err() != nil {
			out.Steps = append(out.Steps, HookStepResult{Step: step, State: workflow.StateSkipped})

			continue
		}

		res := e.runHookStep(ctx, plan, step, req, limit, started)
		out.Steps = append(out.Steps, res)

		if res.State != workflow.StateSuccess && !req.ContinueOnFailure {
			stopped = true
		}
	}

	return out, nil
}

func (e *Engine) runHookStep(ctx context.Context, plan workflow.Plan, step workflow.Step, req HookRunRequest, limit int64, started time.Time) HookStepResult {
	notAttempted := func(err error) HookStepResult {
		return HookStepResult{
			Step:    step,
			State:   workflow.StateFailed,
			Outcome: StepOutcome{Disposition: DispositionNotAttempted, Detail: err.Error()},
		}
	}

	script, err := plan.OpenScript(step.ID)
	if err != nil {
		return notAttempted(err)
	}

	resolved, err := plan.Env().Resolve(ctx, hookBuiltins(plan, step, req.Facts, started))
	if err != nil {
		return notAttempted(err)
	}

	executor, err := e.executorFor(step.Target)
	if err != nil {
		return notAttempted(err)
	}

	sink := newBoundedHookSink(limit, e.Redactor.WithValues(resolved.SecretValues()...))

	stepCtx, cancel := context.WithTimeout(ctx, step.Timeout)
	begun := e.clock()
	outcome, execErr := executor.ExecuteStep(stepCtx, StepRequest{
		RunID:   plan.RunID(),
		Step:    step,
		Script:  script,
		Environ: resolved.Environ(),
		Timeout: step.Timeout,
		Sink:    sink,
	})
	cancel()

	switch {
	case execErr != nil:
		outcome = StepOutcome{Disposition: DispositionNotAttempted, Detail: execErr.Error()}
	default:
		if err := outcome.validate(); err != nil {
			outcome = StepOutcome{Disposition: DispositionTransportLost, Detail: err.Error()}
		}
	}

	sink.flush()

	return HookStepResult{
		Step: step, State: outcome.state(), Outcome: outcome,
		Output: sink.bytes(), OutputTruncated: sink.truncated(),
		Duration: e.clock().Sub(begun),
	}
}

// hookBuiltins is the documented built-in set for a script that is not part
// of a backup. Every status is the honest one: no backup ran, so the backup
// is "unknown" (the value a before hook is told, a real value and not an
// empty string), and nothing is being recovered.
func hookBuiltins(plan workflow.Plan, step workflow.Step, facts map[string]string, started time.Time) map[string]string {
	out := map[string]string{}
	for name, value := range facts {
		out[name] = value
	}

	set := plan.BackupSetID()
	out[workflow.ReservedEnvName] = "1"
	out["RETND_RUN_ID"] = plan.RunID()
	out["RETND_BACKUP_SET_ID"] = set.String()
	out["RETND_BACKUP_SET_NAME"] = set.Set
	out["RETND_PHASE"] = string(step.Phase)
	out["RETND_STEP_ID"] = step.ID
	out["RETND_STEP_NAME"] = step.ScriptName
	out["RETND_STEP_TARGET"] = string(step.Target)
	out["RETND_BACKUP_STATUS"] = string(workflow.StatusUnknown)
	out["RETND_WORKFLOW_STATUS"] = string(workflow.StatusRunning)
	out["RETND_CLEANUP_STATUS"] = string(workflow.StatusUnknown)
	out["RETND_STARTED_AT"] = started.UTC().Format(time.RFC3339)
	out["RETND_RECOVERY"] = "0"
	out["RETND_CLEANUP_REASON"] = workflow.CleanupReasonRunCompleted

	return out
}

// boundedHookSink keeps the first limit bytes of a script's output after
// redaction, and drains the rest. It never returns an error: a script
// blocked on a full pipe is a script that never finishes, which is worse
// than output that says where it stopped.
//
// The redaction is streaming, one filter per stream, for logs.go's reason:
// a pipe returns data in arbitrary pieces, so a credential can straddle two
// chunks, and a filter applied to each chunk alone would pass both halves.
type boundedHookSink struct {
	limit int64

	mu      sync.Mutex
	buf     bytes.Buffer
	cut     bool
	filters map[workflowexec.StreamID]*obs.StreamFilter
}

func newBoundedHookSink(limit int64, red *obs.Redactor) *boundedHookSink {
	return &boundedHookSink{
		limit: limit,
		filters: map[workflowexec.StreamID]*obs.StreamFilter{
			workflowexec.StreamStdout: red.NewStreamFilter(),
			workflowexec.StreamStderr: red.NewStreamFilter(),
		},
	}
}

func (s *boundedHookSink) Chunk(c workflowexec.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filter, ok := s.filters[c.Stream]
	if !ok {
		return fmt.Errorf("workflowrun: a chunk arrived on the stream %q, which is neither stdout nor stderr", c.Stream)
	}
	s.keep(filter.Filter(c.Data))

	return nil
}

// flush releases what the filters are holding back at end of output.
func (s *boundedHookSink) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, f := range s.filters {
		s.keep(f.Flush())
	}
}

func (s *boundedHookSink) keep(data []byte) {
	room := s.limit - int64(s.buf.Len())
	if room <= 0 {
		if len(data) > 0 {
			s.cut = true
		}

		return
	}
	if int64(len(data)) > room {
		data = data[:room]
		s.cut = true
	}
	s.buf.Write(data)
}

func (s *boundedHookSink) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]byte(nil), s.buf.Bytes()...)
}

func (s *boundedHookSink) truncated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cut
}
