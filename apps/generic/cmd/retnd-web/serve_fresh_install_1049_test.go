package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The process side of the first-run invariant (retnd#1049), through the
// same child-process entry point serve_signal_test.go uses, so what is
// asserted is what `/retnd-web serve` really does on a fresh volume.
//
// Two things an operator meets today on a just-installed deployment, both
// from the engine's own container log:
//
//	retnd-web: no configuration at /etc/retnd/config/config.yaml yet; serving the first-run setup flow
//	{"level":"ERROR",...,"op":"startup","error":"service: no configuration file at /etc/retnd/config/config.yaml"}
//
// The first line is right. The second is the product reporting its own
// expected state as a failure, and it is there on every start until a
// backup set exists. And nothing is written: the deployment stays
// "unconfigured" for as long as nobody completes the wizard, so every
// read the UI makes is refused, and `retnd status` in the same container
// fails.
//
// What a fresh install should do instead is come up on its own defaults:
// write a minimal configuration (the journal path it was given, the
// documented poll interval, no sources yet), say so once at INFO, and
// serve a configured engine with nothing to do yet.

// childOutput collects a child's stdout and stderr as lines, from both
// pipes at once, so a wait can look for a line on either stream.
type childOutput struct {
	mu    sync.Mutex
	lines []string
}

func (o *childOutput) add(line string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lines = append(o.lines, line)
}

func (o *childOutput) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.lines...)
}

// dump is the log as a failure message shows it. Lines carrying the
// bootstrap token this throwaway auth store minted are left out, on
// principle rather than because the value matters.
func (o *childOutput) dump() string {
	var kept []string
	for _, line := range o.snapshot() {
		if strings.Contains(strings.ToLower(line), "token") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// waitFor blocks until a line containing any of needles has been seen, or
// the deadline passes, and returns the line.
func (o *childOutput) waitFor(t *testing.T, within time.Duration, needles ...string) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		for _, line := range o.snapshot() {
			for _, needle := range needles {
				if strings.Contains(line, needle) {
					return line
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the serve child never printed any of %q within %s\n%s", needles, within, o.dump())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// errorLines returns every line logged at ERROR. A fresh install has
// nothing to run, so on a clean first start any such line is about the
// start itself.
func (o *childOutput) errorLines() []string {
	var found []string
	for _, line := range o.snapshot() {
		if strings.Contains(line, `"level":"ERROR"`) {
			found = append(found, line)
		}
	}
	return found
}

// startupErrorLines narrows that to the startup operation. A configured
// start runs a real cycle against the test fixture, and that cycle may
// log its own refusals (a remote delete it would not perform, a cycle
// cut short by the stop); those are the cycle's business, not this
// issue's.
func (o *childOutput) startupErrorLines() []string {
	var found []string
	for _, line := range o.errorLines() {
		if strings.Contains(line, `"op":"startup"`) {
			found = append(found, line)
		}
	}
	return found
}

// startServeChild re-executes this test binary as `retnd-web serve` against
// configPath, with the auth store and the journal under stateDir.
func startServeChild(t *testing.T, configPath, stateDir string) (*exec.Cmd, *childOutput) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeChildProcess$")
	cmd.Env = append(os.Environ(),
		serveChildEnv+"=1",
		serveChildConfig+"="+configPath,
		serveChildAuthStore+"="+filepath.Join(stateDir, "local-auth.json"),
		serveChildStateDB+"="+filepath.Join(stateDir, "state.db"),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the serve child: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	out := &childOutput{}
	for _, r := range []io.Reader{stdout, stderr} {
		go func(r io.Reader) {
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				out.add(sc.Text())
			}
		}(r)
	}
	return cmd, out
}

func stopServeChild(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Fatalf("waiting for the serve child: %v", err)
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("the serve child did not stop within 60s of SIGTERM")
	}
}

// The line the fixed product prints once, at INFO, when it writes a
// fresh install's first configuration; and the line today's product
// prints when it serves the setup flow instead. The test waits for
// either, so it reaches its assertions on both builds and fails on what
// it finds rather than on a timeout.
const (
	wroteDefaultsNotice  = "wrote a default configuration"
	servingSetupNotice   = "serving the first-run setup flow"
	defaultPollInterval  = "1h"
	defaultConfigMode    = os.FileMode(0o600)
	settleAfterStartup   = 3 * time.Second
	startupWithinTimeout = 90 * time.Second
)

func TestServe_AFreshInstallComesUpOnItsOwnDefaultsWithoutAnError(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	stateDir := filepath.Join(dir, "state")
	for _, d := range []string{configDir, stateDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	configPath := filepath.Join(configDir, "config.yaml")

	cmd, out := startServeChild(t, configPath, stateDir)
	out.waitFor(t, startupWithinTimeout, wroteDefaultsNotice, servingSetupNotice)
	// Startup logging finishes shortly after either notice; the window is
	// bounded so a late ERROR line is not missed, and short so the test is
	// not. The process is stopped only after the assertions, so the file
	// checked below is the one it is serving.
	time.Sleep(settleAfterStartup)

	info, err := os.Stat(configPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		t.Errorf("a fresh install's first start wrote no configuration at %s; the deployment stays unconfigured, every UI read is refused and `retnd status` fails until somebody completes the wizard\n%s", configPath, out.dump())
	case err != nil:
		t.Errorf("stat %s: %v", configPath, err)
	default:
		if mode := info.Mode().Perm(); mode != defaultConfigMode {
			t.Errorf("default configuration mode = %o, want %o: it names a journal path and, later, hosts and key files", mode, defaultConfigMode)
		}
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		var cfg struct {
			PollInterval string `yaml:"poll_interval"`
			State        struct {
				Database string `yaml:"database"`
			} `yaml:"state"`
			Sources []any `yaml:"sources"`
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("the default configuration does not parse as YAML: %v\n%s", err, raw)
		}
		if want := filepath.Join(stateDir, "state.db"); cfg.State.Database != want {
			t.Errorf("state.database = %q, want the --state-database this process was given (%q); the journal must stay where the packaging put it", cfg.State.Database, want)
		}
		if cfg.PollInterval == "" {
			t.Errorf("poll_interval is absent from the default configuration; the documented first-run cadence (%s) has to be stated so an operator can see what they are running", defaultPollInterval)
		}
		if len(cfg.Sources) != 0 {
			t.Errorf("sources = %d entries, want none: a default configuration invents no backup set", len(cfg.Sources))
		}
	}

	if lines := out.errorLines(); len(lines) > 0 {
		t.Errorf("a clean first start logged at ERROR; an unconfigured install is a normal state, not a failure:\n%s", strings.Join(lines, "\n"))
	}

	stopServeChild(t, cmd)
}

// The control beside the case above, and it is green today on purpose:
// whatever writes a first configuration must never touch one that exists.
// A deployment that has been backing up for months is restarted, and the
// bytes of its config.yaml are the same bytes afterwards.
func TestServe_AnExistingConfigurationIsLeftByteForByte(t *testing.T) {
	configPath := writeServeTestConfig(t)
	stateDir := filepath.Dir(configPath)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	cmd, out := startServeChild(t, configPath, stateDir)
	// The configured start proves itself the way serve_signal_test.go's
	// SIGTERM case does: the first cycle commits, which only happens past
	// Open, on the configuration under test.
	out.waitFor(t, startupWithinTimeout, `"event":"commit"`)
	stopServeChild(t, cmd)

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("a start against an existing configuration changed it\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if lines := out.startupErrorLines(); len(lines) > 0 {
		t.Errorf("a configured start logged its startup at ERROR:\n%s", strings.Join(lines, "\n"))
	}
}
