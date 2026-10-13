package app

import (
	"context"
	"errors"

	"github.com/retnd/retnd/core/internal/config"
)

// StageRunner runs a directory of the operator's scripts, with EPIC L's
// machinery, for a purpose that is not a backup (EPIC Q, #978 and #980).
//
// Two things in EPIC Q want an operator's script. A recovery test hands a
// recovered copy to the operator's validator, and a scrub or a recovery test
// that keeps failing tells the operator through a script and not through a
// notifier this product would have to grow. Both run the way every other hook
// in this product runs: the same stage directory conventions and custody
// checks, the same script naming, the same bytes read once and hashed, the
// same host runner container and the same resolved environment, and the same
// shell verification that gates saving a workflow.
//
// What they do not do is enter a workflow RUN. A run takes the set's lock,
// writes a cleanup obligation and can block the set in recovery, and none of
// that is true of a validator or a notification. See
// workflowrun.Engine.RunHooks.
//
// The interface is here and the implementation is core/service's, for
// app/workflow.go's reason: the engine needs a journal, a runner client and a
// dialer, and the service layer is the one that already holds them.
type StageRunner interface {
	RunStage(ctx context.Context, req StageRequest) (StageResult, error)
}

// ErrStageNotConfigured is returned (or reported in StageResult) when there
// is nothing to run: no workflow root, or no stage directory for this
// purpose. It is the "not configured" fact, and it is never a failure.
var ErrStageNotConfigured = errors.New("app: no scripts are configured for this")

// ErrStageScriptMissing is returned when a request names one script (Only)
// and the stage directory does not hold it. A purpose that was told which
// script to run never falls back to another.
var ErrStageScriptMissing = errors.New("app: the named script is not in its stage directory")

// StageRequest says which directories to run and what to tell them.
type StageRequest struct {
	Set config.BackupSet

	// SetDir and GlobalDir are stage directories relative to the workflow
	// root. The set's runs first and the global one after it, the order the
	// "after" stages of a backup unwind in. Either may be empty, and a
	// directory that does not exist is "not configured" for this purpose:
	// these are conventions, not operator-declared stage directories, so a
	// missing one is absence and not a mistake.
	SetDir    string
	GlobalDir string

	// Vars are ordinary environment variables layered over the set's
	// workflow environment. They may not use the RETND_ prefix:
	// those names are the product's own facts and the environment model
	// refuses them in configuration for the same reason.
	Vars map[string]string

	// ContinueOnFailure runs the remaining scripts after one that failed.
	ContinueOnFailure bool

	// LocalOnly refuses a NAME.remote.sh in these directories. A validator
	// runs against a file on THIS machine, which a script running on the
	// source host cannot see.
	LocalOnly bool

	// MaxScripts bounds how many scripts the directories may hold between
	// them. Zero means no extra bound beyond the workflow engine's own.
	MaxScripts int

	// Only, when set, is the file name of the one script to run. The other
	// scripts in the directory are neither read nor run, and a directory
	// that does not hold it is ErrStageScriptMissing, never another script
	// in its place. It is how a recovery test's validator_id selects the
	// validator.
	Only string

	// MaxOutputBytes bounds the output kept per script. Zero takes the
	// engine's default.
	MaxOutputBytes int64

	// DryRun builds and verifies the plan and runs nothing: it answers "is
	// there something here, and would it be allowed to run" with the
	// scripts' names and hashes. Scripts come back with an empty State.
	DryRun bool
}

// StageScript is one script's outcome.
type StageScript struct {
	// Dir is the stage directory it came from, Name its file name.
	Dir  string
	Name string

	// SHA256 is the hex hash of the exact bytes that ran. It is the
	// script's identity: a script edited by one byte is a different one.
	SHA256 string

	// Target is "local" or "remote".
	Target string

	// State is the workflow step state: success, failed, timed_out,
	// canceled or skipped.
	State string

	// ExitCode is set only when the process exited and the status was
	// observed.
	ExitCode *int

	// Disposition and Termination say what became of the process and how
	// sure anyone is about it: Termination is "", "confirmed" or
	// "unconfirmed".
	Disposition string
	Termination string

	// Detail is a short note from the executor, never an environment value.
	Detail string

	// Output is the redacted, bounded output. It is untrusted terminal
	// input and has to be sanitised before it reaches any field,
	// notification or terminal.
	Output          []byte
	OutputTruncated bool
}

// StageResult is what running the directories did.
type StageResult struct {
	// Configured is false when there was nothing to run.
	Configured bool
	Scripts    []StageScript
}

// Succeeded reports whether something ran and every script succeeded.
func (r StageResult) Succeeded() bool {
	if !r.Configured || len(r.Scripts) == 0 {
		return false
	}
	for _, s := range r.Scripts {
		if s.State != "success" {
			return false
		}
	}

	return true
}
