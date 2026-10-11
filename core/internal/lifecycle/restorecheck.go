// This file is the untrusted-subprocess primitive Phase 4's scheduled
// revalidation (internal/revalidate) needs for its restore-test hook: the
// stronger form of revalidation that proves an artifact still actually
// restores, not merely that its bytes are unchanged.
//
// RunRestoreCheck's contract is deliberately identical to verify.go's
// unexported runValidator, which already implements this exact untrusted-
// subprocess shape for FR-13's one-time, at-verification application
// validator: a fixed, minimal environment, its own process group so a
// timeout can kill anything it spawned, bounded captured output, and
// fail-closed on a timeout or a non-zero exit. It is a separate,
// independent implementation rather than an exported alias for
// runValidator, on purpose: verify.go is already-shipped, already-tested
// FR-13 code (see this package's other files), and this PR does not touch
// it. The two are small enough (under a hundred lines each) that keeping
// them independent costs little, and a future change that wants to unify
// them can do so once both have their own callers and their own tests to
// prove it did not change behavior for either.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/retnd/retnd/core/internal/config"
)

// maxRestoreCheckOutput mirrors verify.go's maxValidatorOutput: it bounds
// how much of a restore-test hook's combined stdout+stderr this package
// keeps, both while capturing it (so a runaway untrusted process cannot
// exhaust memory) and in the detail string a caller might persist to the
// journal (so one hook cannot bloat it without limit).
const maxRestoreCheckOutput = 16 << 10 // 16 KiB

// RestoreCheckResult is RunRestoreCheck's verdict.
type RestoreCheckResult struct {
	// Passed is the hook's pass/fail verdict: true only for a clean exit
	// (status 0). It is meaningless when RunRestoreCheck also returns a
	// non-nil error, which means the hook never got to render a verdict at
	// all.
	Passed bool

	// Detail is the hook's captured stdout+stderr, bounded to
	// maxRestoreCheckOutput, plus a trailing note when it was killed for
	// exceeding its own timeout.
	Detail string
}

// RunRestoreCheck runs cmd against localPath and reports its pass/fail
// verdict.
//
// err is non-nil only when the hook could not be run at all (a bad
// executable, permission denied to exec it) or when ctx itself was
// cancelled or timed out, both infrastructure conditions distinct from the
// hook having an opinion about localPath. Once the process actually
// starts, whatever happens to it next, a clean non-zero exit or being
// killed for exceeding cmd.Timeout, is the hook's verdict, reported
// through the returned RestoreCheckResult with a nil error: a restore-test
// hook that never answers is treated the same as an explicit "no" (fail
// closed), never as license to treat the artifact as still good.
func RunRestoreCheck(ctx context.Context, cmd config.Command, localPath string) (RestoreCheckResult, error) {
	o, err := RunRestoreCheckOutcome(ctx, cmd, localPath)
	if err != nil {
		return RestoreCheckResult{}, err
	}
	detail := o.Output
	if o.Truncated {
		detail += truncatedSuffix
	}
	if o.TimedOut {
		detail += fmt.Sprintf("\n(retnd: restore-test hook killed after exceeding its %s timeout)", cmd.Timeout.Duration())
	}
	return RestoreCheckResult{Passed: o.Passed, Detail: detail}, nil
}

const truncatedSuffix = "... (truncated)"

// Termination says how the hook's process ended, and how sure anyone is.
const (
	// TerminationExited means the process ended on its own and nothing it
	// started is still in its process group.
	TerminationExited = "exited"
	// TerminationKilledGroup means the timeout fired and the process group
	// was killed and observed gone.
	TerminationKilledGroup = "killed_group"
	// TerminationUnconfirmed means the process group could not be observed
	// gone after it was killed. The kill was requested and its effect is not
	// known, which is a different fact from it having worked, and a verdict
	// from such a hook is not one anybody can rely on.
	TerminationUnconfirmed = "unconfirmed"
)

// RestoreCheckOutcome is RunRestoreCheck's verdict with the facts a recovery
// test records beside it (EPIC Q, #980).
type RestoreCheckOutcome struct {
	// Passed is true only for a clean exit (status 0) of a process whose
	// whole process group is observed gone afterwards.
	Passed bool

	// Output is the captured stdout and stderr, bounded to the same cap
	// RunRestoreCheck uses. It is RAW: untrusted terminal input, which a
	// caller must sanitise before it reaches any field, notification or
	// terminal. Truncated says the cap cut it; the marker is the caller's to
	// add, as a separate kind of record, so that output which merely
	// contains the marker's words cannot pass for it.
	Output    string
	Truncated bool

	// TimedOut is true when cmd.Timeout fired and the group was killed.
	TimedOut bool

	// Termination is one of the Termination* constants.
	Termination string

	// ExitCode is the process's exit status, or -1 when it did not exit on
	// its own.
	ExitCode int
}

// groupGoneWait bounds how long RunRestoreCheckOutcome waits to observe the
// process group gone after killing it. Killing is asynchronous: the signal
// is delivered, the processes die, and a zombie keeps the group alive until
// its parent reaps it, so "kill returned" is not "they are gone".
const groupGoneWait = 2 * time.Second

// captureGrace is how long the capture read is given to see end of file
// once the process group has been swept.
const captureGrace = 2 * time.Second

// RunRestoreCheckOutcome runs cmd against localPath exactly as RunRestoreCheck
// does, and additionally sweeps the hook's process group once the hook has
// returned and records whether the group was observed gone.
//
// The sweep is the part RunRestoreCheck did not do. A hook that exits 0
// after starting a background child leaves the child running in the group,
// and a hook that hangs is killed with its group but nothing used to check
// that the kill took. Both are decided here rather than assumed: the group
// is killed, then polled until it is gone or groupGoneWait passes, and a
// group that is still there makes the termination unconfirmed and the
// verdict a non-pass, because a pass from a hook that left something
// running is a pass nobody can stand behind.
//
// A process that calls setsid has left the group and is invisible to this.
// That limit is real and is stated in the recorded termination's meaning:
// it describes the process group this product created, not every descendant
// a hook could conjure.
func RunRestoreCheckOutcome(ctx context.Context, cmd config.Command, localPath string) (RestoreCheckOutcome, error) {
	timeout := cmd.Timeout.Duration()
	if timeout <= 0 {
		return RestoreCheckOutcome{}, fmt.Errorf("lifecycle: RunRestoreCheck: timeout must be positive, got %s", cmd.Timeout)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(runCtx, cmd.Executable, localPath)

	// A fixed, minimal environment: never this process's own os.Environ(),
	// which could carry secrets this manager holds (an sftp private key
	// path, ambient credentials). Mirrors verify.go's runValidator exactly,
	// including the env var duplicating argv[1] for a hook that prefers
	// reading its target from the environment, and including the legacy
	// name exported beside it: RCLONE_MANAGER_ARTIFACT_PATH is what this
	// variable was called before EPIC R (#885) renamed the product, and
	// dropping it in the same release as the rename would break an
	// operator's own hook silently, because an unset variable is the
	// empty string rather than an error. See runValidator for the full
	// reasoning; issue #895 removes the second entry in the release after
	// the one that ships this EPIC, and it has to happen in both places
	// at once.
	c.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"RETND_ARTIFACT_PATH=" + localPath,
		"RCLONE_MANAGER_ARTIFACT_PATH=" + localPath,
	}

	// A fresh process group, so the timeout below can kill whatever the
	// hook spawned along with it, not just the hook itself.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		if killErr := syscall.Kill(-c.Process.Pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			return killErr
		}
		return nil
	}

	// The capture is a pipe this function reads itself, not a writer handed to
	// exec. With a writer, exec runs copying goroutines and Wait does not
	// return until every holder of the pipe has closed it or WaitDelay
	// passes, which means a hook that exits 0 after leaving a child running
	// keeps the whole run waiting on the child. Reading the pipe here lets
	// Wait return the moment the hook itself has gone, the group sweep below
	// kills what it left, and closing those children's ends of the pipe is
	// what ends the read.
	pr, pw, pipeErr := os.Pipe()
	if pipeErr != nil {
		return RestoreCheckOutcome{}, fmt.Errorf("lifecycle: RunRestoreCheck: could not create a capture pipe: %w", pipeErr)
	}
	defer pr.Close()
	c.Stdout = pw
	c.Stderr = pw

	if startErr := c.Start(); startErr != nil {
		pw.Close()
		return RestoreCheckOutcome{}, fmt.Errorf("lifecycle: RunRestoreCheck: could not start %q: %w", cmd.Executable, startErr)
	}
	// The child holds its own copy of the write end now; this one must go,
	// or the read below could never see end of file.
	pw.Close()

	out := &boundedWriter{limit: maxRestoreCheckOutput}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(out, pr)
	}()

	runErr := c.Wait()

	// See verify.go's runValidator for why classification reads the
	// contexts' own Err() directly rather than inspecting runErr's type or
	// wrapped chain: once c.Cancel above kills the process promptly, Go's
	// exec package is not guaranteed to surface ctx.Err() as the returned
	// error at all.
	cancelled := runErr != nil && ctx.Err() != nil
	gone := sweepGroup(c)

	// The sweep closed the pipe for everything in the group. Something that
	// left the group (setsid) may still hold it, so the read is given a
	// moment and then abandoned; output that arrives after that is not
	// captured, which the bound would have discarded anyway.
	select {
	case <-readDone:
	case <-time.After(captureGrace):
		pr.Close()
		<-readDone
	}
	raw := out.buf.String()
	truncated := out.buf.Len() >= out.limit

	if cancelled {
		return RestoreCheckOutcome{}, fmt.Errorf("lifecycle: RunRestoreCheck: cancelled: %w", ctx.Err())
	}

	o := RestoreCheckOutcome{Output: raw, Truncated: truncated, ExitCode: -1}
	switch {
	case runErr == nil:
		o.ExitCode = 0
		o.Termination = TerminationExited
		o.Passed = true
	case runCtx.Err() != nil:
		o.TimedOut = true
		o.Termination = TerminationKilledGroup
	default:
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return RestoreCheckOutcome{}, fmt.Errorf("lifecycle: RunRestoreCheck: could not start %q: %w", cmd.Executable, runErr)
		}
		o.ExitCode = exitErr.ExitCode()
		o.Termination = TerminationExited
	}
	if !gone {
		o.Termination = TerminationUnconfirmed
		o.Passed = false
	}
	return o, nil
}

// sweepGroup kills whatever is left in c's process group and reports whether
// the group was observed gone within groupGoneWait. A command that never
// started has no group and is trivially gone.
func sweepGroup(c *exec.Cmd) bool {
	if c.Process == nil {
		return true
	}
	pgid := c.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		return errors.Is(err, syscall.ESRCH)
	}
	deadline := time.Now().Add(groupGoneWait)
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
