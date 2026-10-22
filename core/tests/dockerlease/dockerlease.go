// Package dockerlease stops test containers outliving the process that
// created them.
//
// Every docker fixture in this repository already registers a t.Cleanup that
// runs `docker rm -f`. That is correct, and it covers a normal finish and a
// t.Fatalf. What it cannot cover is the test binary being KILLED: a `go test`
// timeout, a Ctrl-C, or an editor or agent cancelling the command. t.Cleanup
// runs in-process, so a SIGKILL takes the cleanup with it and the container
// survives.
//
// scripts/ci-local.sh runs from .husky/pre-commit, so every interrupted
// commit used to leave a container behind, and nothing ever removed them. I
// found 44 of them on one machine, the oldest running for 4h24m, with the
// Docker VM at ~400% CPU. See issue #150.
//
// So the fix is not to try harder on the way out, which cannot work against
// SIGKILL. It is to make the leak self-correcting: label every container at
// creation, and sweep old labelled containers on the way IN. A run started
// after a killed one cleans up what the killed one left.
package dockerlease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// LabelKey marks a container as belonging to this repository's tests.
	// Sweep only ever removes containers carrying it, so it is the whole
	// safety boundary: an unlabelled container is never touched.
	LabelKey = "retnd-test"

	// StaleAfter is how old a labelled container must be before Sweep will
	// remove it. It has to clear the slowest legitimate test comfortably,
	// because a threshold that is too tight would delete a container out
	// from under a running test in a parallel package. Fifteen minutes is
	// far longer than any fixture in this repo stays up, and far shorter
	// than the hours the leaked ones survived.
	StaleAfter = 15 * time.Minute

	// dockerTimeout bounds every docker call this package makes. A test
	// helper must never be the reason a suite hangs.
	dockerTimeout = 30 * time.Second
)

// LabelFlag and LabelSpec are the `docker run` flag pair that marks a
// container sweepable. They are two separate names, rather than one
// []string, so they splice straight into an argument literal without
// restructuring it, and so they land BEFORE the image name where docker
// requires its flags:
//
//	args := []string{
//		"run", "-d", "--name", name,
//		dockerlease.LabelFlag, dockerlease.LabelSpec,
//		"-p", "127.0.0.1::22",
//		"some/image",
//	}
//
// LabelSpec is a variable rather than a constant since #1260, because the
// label's VALUE is now the owner (below), and the owner is only known at
// run time. Every call site reads the same; what changed is what Sweep
// lists.
const LabelFlag = "--label"

var LabelSpec = LabelKey + "=" + ownerID()

// The owner is the worktree this test binary runs in, and it is the
// whole of #1260's item 6. Sweep used to remove any labelled container
// older than StaleAfter whoever started it, which was right when one
// developer ran one gate at a time and wrong once thirty worktrees shared
// one daemon: a sibling lane's fixture still in use after fifteen minutes
// was removed out from under its test, and that looked like a flaky
// crash-matrix failure in a lane that had done nothing wrong.
//
// So the label carries an owner, and Sweep lists only containers carrying
// THIS owner. The owner is derived from the worktree rather than from the
// process or the run, on purpose: a run id would make every leak
// permanent, because the next run would never match it, and the point of
// the sweep is that a run started after a killed one cleans up what the
// killed one left. Per worktree, a lane reclaims its own leftovers and
// nobody else's.
//
// Resolved once per process. RETND_TEST_OWNER wins when set, which is how
// a test drives two owners from one worktree; otherwise it is the git
// toplevel of the working directory (every test binary in this tree runs
// inside the repository), and failing that the hostname, which is the
// pre-#1260 behaviour for one machine.
var (
	once         sync.Once
	networksOnce sync.Once
	ownerOnce    sync.Once
	owner        string
)

func ownerID() string {
	ownerOnce.Do(func() {
		if v := os.Getenv("RETND_TEST_OWNER"); v != "" {
			owner = ownerFor(v)
			return
		}
		if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
			if top := strings.TrimSpace(string(out)); top != "" {
				owner = ownerFor(top)
				return
			}
		}
		host, _ := os.Hostname()
		owner = ownerFor("host:" + host)
	})
	return owner
}

// ownerFor turns a worktree path into a label value: a short hash, so the
// label is the same length whatever the path is and carries no path into
// `docker ps` output. Stable for the same input, different for different
// inputs, which is all a label value needs to be.
func ownerFor(worktree string) string {
	sum := sha256.Sum256([]byte(worktree))
	return hex.EncodeToString(sum[:6])
}

// SweepNetworks is Sweep for the networks core/tests/machines creates
// (issue #447): labelled the same way, swept on the way in for the same
// reason, because a killed run leaves its network behind exactly as it
// leaves its containers. A network that still has an endpoint on it cannot
// be removed and is left alone; the container sweep frees it for the next
// run.
func SweepNetworks() {
	networksOnce.Do(func() { sweepNetworksOlderThan(time.Now().Add(-StaleAfter)) })
}

func sweepNetworksOlderThan(cutoff time.Time) {
	out, err := run("network", "ls", "-q", "--filter", "label="+LabelSpec)
	if err != nil {
		return
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return
	}
	// `{{json .Created}}` rather than `{{.Created}}`: a network's Created
	// is a time.Time on docker's side, and the bare template prints Go's
	// own String() form, which is not what time.Parse below reads. The
	// JSON form is RFC 3339, the same shape the container sweep parses.
	out, _ = run(append([]string{"network", "inspect", "--format", "{{.Id}} {{json .Created}}"}, ids...)...)
	for _, line := range strings.Split(out, "\n") {
		id, ts, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, strings.Trim(ts, "\""))
		if err != nil || !created.Before(cutoff) {
			continue
		}
		// One at a time: a batch `network rm` stops at the first network
		// that is still in use, and the stale empty ones after it would
		// survive.
		_, _ = run("network", "rm", id)
	}
}

// Sweep removes this worktree's labelled containers older than StaleAfter,
// at most once per test binary however many times it is called. Call it
// before creating a container. A container another worktree labelled, or
// one carrying the fixed value every lane shared before #1260, is never a
// candidate (see LabelSpec).
//
// It is best-effort by design and reports nothing: every error is swallowed,
// a missing or unreachable docker is a no-op, and it never fails a test. A
// housekeeping step that can break a suite is worse than the leak it fixes.
func Sweep() { once.Do(func() { sweepOlderThan(time.Now().Add(-StaleAfter)) }) }

// sweepOlderThan is Sweep's body with the cutoff lifted out, so a test can
// drive both sides of the decision against a real docker without waiting
// StaleAfter or backdating a container, which docker gives no way to do.
func sweepOlderThan(cutoff time.Time) { sweepIDs(listLabelled(), cutoff) }

// sweepIDs is sweepOlderThan with the listing lifted out too, so a test can
// hand it a batch containing an id that has already gone. That is not a
// contrived case: several worktrees on one machine share a single docker
// daemon, so a container listed a moment ago is routinely removed by
// somebody else's cleanup before this call gets to it.
func sweepIDs(ids []string, cutoff time.Time) {
	stale := make([]string, 0, len(ids))
	for id, created := range createdAt(ids) {
		if created.Before(cutoff) {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return
	}
	_, _ = run(append([]string{"rm", "-f"}, stale...)...)
}

func listLabelled() []string {
	out, err := run("ps", "-aq", "--filter", "label="+LabelSpec)
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// createdAt maps container id to creation time. It asks docker for RFC3339
// rather than parsing `docker ps --format`, whose human-facing timestamp is
// locale and timezone shaped and not meant to be machine-read.
func createdAt(ids []string) map[string]time.Time {
	if len(ids) == 0 {
		return nil
	}
	// The exit status is deliberately ignored, and that is the #161 fix.
	// `docker inspect` exits non-zero if ANY of its arguments is missing,
	// while still printing a good line for every id it did find. Reading
	// that status as "nothing can be dated" meant one container removed by
	// another worktree between listLabelled and here turned the whole
	// sweep into a silent no-op. Several worktrees share one daemon on this
	// machine, so that race is routine rather than exotic, and a sweeper
	// that silently sweeps nothing is worse than no sweeper: it looks like
	// the leak is handled. What actually comes back is parsed instead, so a
	// vanished id costs its own line and nothing more.
	out, _ := run(append([]string{"inspect", "--format", "{{.Id}} {{.Created}}"}, ids...)...)
	got := make(map[string]time.Time, len(ids))
	for _, line := range strings.Split(out, "\n") {
		id, ts, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		got[id] = created
	}
	return got
}

func run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	return string(out), err
}
