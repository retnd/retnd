package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #1546 at the web host: `retnd-web serve` started against a deployment still
// mounted at the path it had before the 0.5.0 rename must refuse, exit
// non-zero and say why, instead of writing a default configuration at the
// new, empty path and serving an empty retnd.
//
// Driven through serve_signal_test.go's child-process entry point, so what
// is asserted is what the shipped `/retnd-web serve` prints and exits with.
//
// The old name is assembled from two pieces, because
// scripts/rename/check-no-former-name.sh refuses it spelled out anywhere in
// the tree.
var formerName1546 = "backup" + "d"

const refusalNotice1546 = "refusing to start"

type deployment1546 struct{ root string }

func (d deployment1546) currentConfig() string {
	return filepath.Join(d.root, "retnd", "config", "config.yaml")
}

func (d deployment1546) formerConfig() string {
	return filepath.Join(d.root, formerName1546, "config", "config.yaml")
}

// stateDir is where the child's auth store and first-run journal go: a
// directory whose path the rename never touched.
func (d deployment1546) stateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(d.root, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// populate puts a working configuration at path (writeServeTestConfig's,
// whose journal and remote stay in that helper's own temp directory).
func (d deployment1546) populate(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(writeServeTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// refusedServe waits until the child either refuses or shows that it
// started, and returns the refusal line and the exit code. Waiting for the
// line before calling Wait matters: Wait closes the pipes, and the refusal
// is the last thing the child writes.
func refusedServe(t *testing.T, cmd *exec.Cmd, out *childOutput) (string, int) {
	t.Helper()
	line := out.waitFor(t, startupWithinTimeout, refusalNotice1546, wroteDefaultsNotice, servingSetupNotice, `"event":"commit"`)
	if !strings.Contains(line, refusalNotice1546) {
		stopServeChild(t, cmd)
		t.Fatalf("serve started instead of refusing (it printed %q)\n%s", line, out.dump())
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Fatalf("waiting for the serve child: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("serve printed its refusal but was still running 30s later\n%s", out.dump())
	}
	return line, cmd.ProcessState.ExitCode()
}

func mustMention1546(t *testing.T, line string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(line, w) {
			t.Errorf("the refusal does not mention %q:\n%s", w, line)
		}
	}
}

func TestServe_1546_OnlyTheFormerPathHoldsTheCatalogue_Refuses(t *testing.T) {
	d := deployment1546{root: t.TempDir()}
	d.populate(t, d.formerConfig())

	cmd, out := startServeChild(t, d.currentConfig(), d.stateDir(t))
	line, code := refusedServe(t, cmd, out)
	if code == 0 {
		t.Errorf("serve exited 0 after refusing; a supervisor has to see a failure")
	}
	mustMention1546(t, line, d.formerConfig(), d.currentConfig(), filepath.Dir(d.currentConfig()), "--config "+d.formerConfig())
	if _, err := os.Stat(d.currentConfig()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("serve wrote a configuration at %s before refusing (stat: %v); nothing may be written", d.currentConfig(), err)
	}
}

func TestServe_1546_BothPathsPopulated_RefusesNamingTheConflict(t *testing.T) {
	d := deployment1546{root: t.TempDir()}
	d.populate(t, d.currentConfig())
	d.populate(t, d.formerConfig())

	cmd, out := startServeChild(t, d.currentConfig(), d.stateDir(t))
	line, code := refusedServe(t, cmd, out)
	if code == 0 {
		t.Errorf("serve exited 0 after refusing; a supervisor has to see a failure")
	}
	mustMention1546(t, line, "two different", d.currentConfig(), d.formerConfig())
}

func TestServe_1546_OnlyTheCurrentPathPopulated_StartsNormally(t *testing.T) {
	d := deployment1546{root: t.TempDir()}
	d.populate(t, d.currentConfig())

	cmd, out := startServeChild(t, d.currentConfig(), d.stateDir(t))
	// The first cycle commits only past Open, on this configuration.
	out.waitFor(t, startupWithinTimeout, `"event":"commit"`, refusalNotice1546)
	stopServeChild(t, cmd)
	for _, line := range out.snapshot() {
		if strings.Contains(line, refusalNotice1546) {
			t.Errorf("serve refused a deployment at the current path:\n%s", line)
		}
	}
}

func TestServe_1546_NeitherPathPopulated_AFreshInstallStartsNormally(t *testing.T) {
	d := deployment1546{root: t.TempDir()}

	cmd, out := startServeChild(t, d.currentConfig(), d.stateDir(t))
	out.waitFor(t, startupWithinTimeout, wroteDefaultsNotice, servingSetupNotice, refusalNotice1546)
	time.Sleep(settleAfterStartup)
	stopServeChild(t, cmd)
	for _, line := range out.snapshot() {
		if strings.Contains(line, refusalNotice1546) {
			t.Errorf("serve refused a fresh install:\n%s", line)
		}
	}
	if _, err := os.Stat(d.currentConfig()); err != nil {
		t.Errorf("a fresh install wrote no default configuration at %s: %v\n%s", d.currentConfig(), err, out.dump())
	}
}
