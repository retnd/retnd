// Package procseam is the one place this product starts a child process from.
//
// Every os/exec call in the product goes through CommandContext or Command
// here. In production they are exec.CommandContext and exec.Command and
// nothing else: no state, no cost beyond one atomic load. What the seam buys
// is a test's ability to say "this code path starts no process" and have a
// spawn FAIL it, rather than asserting it with a scan of import lines.
//
// # Why an import scan was not enough
//
// Artifact recovery promises it runs no program (FR-67): it reads bytes and
// compares digests. The first guard for that was a test that parsed the
// recovery package's own files and failed on an import of os/exec. That
// proves those files do not call exec. It says nothing about what they
// call: a secret resolver, a validator, a workflow hook or an engine
// adapter that spawns would sail past it. Forbid is installed for the
// duration of a test, and whatever code runs in that window, in any package
// of this module, either never reaches a spawn or fails the test by name.
//
// # What it does not cover
//
// Code that is not this module's: the embedded rclone library and the Go
// standard library start processes of their own on some paths (an sftp
// remote's ssh command, for one), and none of that goes through here. A
// test using Forbid shows that the product's own spawn sites were not
// reached, which is the claim the product makes; it does not show that the
// operating system saw no fork. TestEveryProductSpawnGoesThroughTheSeam keeps
// the first half honest: a new direct exec call in the product fails it.
package procseam

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
)

// ErrRefused is the error a command started while a Forbid is in force
// returns from Start and Run. The process is never created.
var ErrRefused = errors.New("procseam: a process spawn was refused because the test forbids them")

// T is the part of testing.TB that Forbid needs, so this package does not
// import "testing" into the product binary.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Cleanup(func())
}

type guard struct {
	t T
}

var current atomic.Pointer[guard]

// CommandContext is exec.CommandContext, or, while a Forbid is in force, a
// command that records the attempt as a test failure and cannot be started.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	refuse(cmd, name, args)
	return cmd
}

// Command is exec.Command with the same seam as CommandContext.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	refuse(cmd, name, args)
	return cmd
}

func refuse(cmd *exec.Cmd, name string, args []string) {
	g := current.Load()
	if g == nil {
		return
	}
	g.t.Helper()
	g.t.Errorf("a process was spawned where none is allowed: %s", strings.TrimSpace(name+" "+fmt.Sprint(len(args))+" arg(s)"))
	cmd.Err = ErrRefused
}

// Forbid makes every spawn through this package fail t, for the life of t.
// It is process-wide, so a test that calls it must not run in parallel with
// one that legitimately starts a process. Only one Forbid can be in force at
// a time; a second is a mistake in the tests and fails loudly.
//
// The arguments are deliberately not echoed: they can carry a secret
// reference or a path, and a failure message ends up in CI logs.
func Forbid(t T) {
	t.Helper()
	g := &guard{t: t}
	if !current.CompareAndSwap(nil, g) {
		t.Errorf("procseam.Forbid is already in force, so two tests are forbidding spawns at once")
		return
	}
	t.Cleanup(func() { current.CompareAndSwap(g, nil) })
}
