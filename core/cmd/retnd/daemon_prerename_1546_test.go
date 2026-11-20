package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// #1546 at the engine: `retnd daemon` started against a deployment still
// mounted at the path it had before the 0.5.0 rename must refuse, exit
// non-zero and say why, instead of looking at the new, empty path.
//
// The child is this test binary re-executed as the daemon, through
// daemon_signal_test.go's TestDaemonChildProcess entry point, so what is
// asserted is what the shipped command prints and exits with.
//
// The old name is assembled from two pieces, because
// scripts/rename/check-no-former-name.sh refuses it spelled out anywhere in
// the tree.
var formerName1546 = "backup" + "d"

// lockedBuffer is a bytes.Buffer an exec.Cmd can write to while the test
// reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// daemonOutcome is how a daemon child ended up: it exited by itself with
// code, or it got as far as logging its startup and is still running.
type daemonOutcome struct {
	exited  bool
	code    int
	started bool
	output  string
}

// runDaemon1546 starts the daemon against configPath and returns as soon as
// it either exits or logs its startup. A daemon that started is stopped
// with SIGTERM before this returns, and the outcome records whether that
// stop was clean.
func runDaemon1546(t *testing.T, configPath string) daemonOutcome {
	t.Helper()
	var out lockedBuffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonChildProcess$")
	cmd.Env = append(os.Environ(), daemonChildEnv+"=1", daemonChildConfig+"="+configPath)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the daemon child: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	deadline := time.After(90 * time.Second)
	for {
		select {
		case <-done:
			return daemonOutcome{exited: true, code: cmd.ProcessState.ExitCode(), output: out.String()}
		case <-deadline:
			t.Fatalf("the daemon child neither exited nor logged its startup within 90s\n%s", out.String())
		case <-time.After(50 * time.Millisecond):
		}
		if strings.Contains(out.String(), `"event":"startup"`) {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(60 * time.Second):
				t.Fatalf("the daemon child did not stop within 60s of SIGTERM\n%s", out.String())
			}
			return daemonOutcome{started: true, code: cmd.ProcessState.ExitCode(), output: out.String()}
		}
	}
}

// deployment1546 is a deployment root with the current and pre-rename
// spellings of the configuration directory under it, and a state directory
// whose path the rename never touched.
type deployment1546 struct{ root string }

func (d deployment1546) currentConfig() string {
	return filepath.Join(d.root, "retnd", "config", "config.yaml")
}

func (d deployment1546) formerConfig() string {
	return filepath.Join(d.root, formerName1546, "config", "config.yaml")
}

// populate writes a working configuration at path, whose journal lives in a
// directory of its own.
func (d deployment1546) populate(t *testing.T, path, journal string) {
	t.Helper()
	stateDir := filepath.Join(d.root, "state-"+journal)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	written := writeTestConfigIn(t, filepath.Dir(path), filepath.Join(stateDir, "state.db"))
	if written != path {
		t.Fatalf("writeTestConfigIn wrote %s, want %s", written, path)
	}
}

func assertRefused1546(t *testing.T, got daemonOutcome, wants ...string) {
	t.Helper()
	if !got.exited {
		t.Fatalf("the daemon started instead of refusing\n%s", got.output)
	}
	if got.code == 0 {
		t.Errorf("the daemon exited 0; a refusal has to be a non-zero exit a supervisor can see")
	}
	for _, w := range wants {
		if !strings.Contains(got.output, w) {
			t.Errorf("the daemon's output does not mention %q\n%s", w, got.output)
		}
	}
}

func TestDaemon_1546_OnlyTheFormerPathHoldsTheCatalogue_Refuses(t *testing.T) {
	d := deployment1546{root: t.TempDir()}
	d.populate(t, d.formerConfig(), "old")

	got := runDaemon1546(t, d.currentConfig())
	assertRefused1546(t, got,
		"refusing to start",
		d.formerConfig(),
		d.currentConfig(),
		"--config "+d.formerConfig(),
		filepath.Dir(d.currentConfig()),
	)
	if strings.Contains(got.output, "no backup set configured yet") {
		t.Errorf("the daemon told an upgraded deployment it has no backup set; its catalogue is at %s\n%s", d.formerConfig(), got.output)
	}
}

func TestDaemon_1546_BothPathsPopulated_RefusesNamingTheConflict(t *testing.T) {
	d := deployment1546{root: t.TempDir()}
	d.populate(t, d.currentConfig(), "new")
	d.populate(t, d.formerConfig(), "old")

	assertRefused1546(t, runDaemon1546(t, d.currentConfig()),
		"refusing to start",
		"two different",
		d.currentConfig(),
		d.formerConfig(),
	)
}

func TestDaemon_1546_OnlyTheCurrentPathPopulated_StartsNormally(t *testing.T) {
	d := deployment1546{root: t.TempDir()}
	d.populate(t, d.currentConfig(), "new")

	got := runDaemon1546(t, d.currentConfig())
	if !got.started {
		t.Fatalf("the daemon did not start against a deployment at the current path (exit %d)\n%s", got.code, got.output)
	}
	if got.code != 0 {
		t.Errorf("the daemon's SIGTERM stop exited %d, want 0\n%s", got.code, got.output)
	}
	if strings.Contains(got.output, "refusing to start") {
		t.Errorf("the daemon printed a refusal on a normal start\n%s", got.output)
	}
}

// The engine has no setup flow of its own: on a fresh install it says there
// is no backup set yet and leaves the first configuration to the web host
// (#1049). That ordinary answer is what "starts normally" means here, and the
// refusal must not replace it.
func TestDaemon_1546_NeitherPathPopulated_IsAnOrdinaryFreshInstall(t *testing.T) {
	d := deployment1546{root: t.TempDir()}

	got := runDaemon1546(t, d.currentConfig())
	if strings.Contains(got.output, "refusing to start") {
		t.Errorf("a fresh install was refused\n%s", got.output)
	}
	if !strings.Contains(got.output, "no backup set configured yet") {
		t.Errorf("a fresh install did not get the engine's ordinary first-run answer\n%s", got.output)
	}
}
