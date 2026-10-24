package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Issue #1253: `backup-set test-connection --candidate` proves a source
// that is not saved, the way `medium test-connection --candidate` proves a
// destination that is not declared. It is the terminal twin of the
// wizard's "Test connection" button, which runs the same check before the
// set exists.

// aStoredKey puts a private key into the deployment's own key store under
// an id, which is what --ssh-key-id names: the candidate check takes a
// reference to a key this deployment already holds, never key material.
func aStoredKey(t *testing.T, configPath, id string) {
	t.Helper()
	dir := filepath.Join(filepath.Dir(configPath), "ssh_keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	material, err := os.ReadFile(writeTestPrivateKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id), material, 0o600); err != nil {
		t.Fatal(err)
	}
}

func candidateArgs(t *testing.T, configPath string, extra ...string) []string {
	t.Helper()
	args := []string{"backup-set", "--config", configPath, "test-connection", "--candidate",
		"--host", "127.0.0.1", "--port", strconv.Itoa(aClosedLoopbackPort(t)),
		"--user", "backups", "--ssh-key-id", "key_cand",
		"--known-hosts-line", aKnownHostsLine, "--remote-path", "/srv/backups"}
	return append(args, extra...)
}

func TestRun1253_CandidateConnectionTestReportsTheStepsAndWritesNothing(t *testing.T) {
	configPath := writeTestConfig(t)
	aStoredKey(t, configPath, "key_cand")
	before := readFile(t, configPath)

	var code int
	stdout := captureStdout(t, func() {
		code = run(candidateArgs(t, configPath))
	})
	// A closed port is not a source, and the exit code is the verdict.
	if code != 1 {
		t.Fatalf("test-connection --candidate against a closed port = %d, want 1:\n%s", code, stdout)
	}
	for _, step := range []string{"credentials", "resolve", "connect"} {
		if !strings.Contains(stdout, step) {
			t.Errorf("the report does not name the %s step:\n%s", step, stdout)
		}
	}
	if !strings.Contains(stdout, "nothing was written") {
		t.Errorf("a candidate check must say it saved nothing, whatever the report says:\n%s", stdout)
	}
	if after := readFile(t, configPath); after != before {
		t.Errorf("a candidate check changed the configuration:\n%s", after)
	}
}

func TestRun1253_CandidateConnectionTestJSONIsTheContractsResponse(t *testing.T) {
	configPath := writeTestConfig(t)
	aStoredKey(t, configPath, "key_cand")

	var code int
	stdout := captureStdout(t, func() {
		code = run(candidateArgs(t, configPath, "--json"))
	})
	if code != 1 {
		t.Fatalf("--json against a closed port = %d, want 1 (the exit code stays the verdict):\n%s", code, stdout)
	}
	var got struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Step string `json:"step"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if got.OK || len(got.Checks) == 0 {
		t.Errorf("response = %+v, want a failed report with its steps", got)
	}
}

func TestRun1253_CandidateConnectionTestRefusesWhatItCannotMean(t *testing.T) {
	configPath := writeTestConfig(t)
	aStoredKey(t, configPath, "key_cand")

	for name, args := range map[string][]string{
		// A candidate is not saved, so there is no set to name.
		"with a set id": candidateArgs(t, configPath, "production/postgres-primary"),
		// Without the host there is nothing to check.
		"without --host": {"backup-set", "--config", configPath, "test-connection", "--candidate",
			"--user", "backups", "--ssh-key-id", "key_cand", "--known-hosts-line", aKnownHostsLine, "--remote-path", "/srv"},
		// The by-id mode takes no connection flags: a flag parsed and
		// ignored there would check something other than what was asked.
		"by id with a candidate flag": {"backup-set", "--config", configPath, "test-connection", "production/postgres-primary", "--host", "127.0.0.1"},
		// --candidate is test-connection's alone, and a create that
		// accepted it would be a create that did not check.
		"create with --candidate": createArgs(configPath, writeTestPrivateKey(t), "api/x", "--candidate"),
		"patch with --candidate":  {"backup-set", "--config", configPath, "patch", "production/postgres-primary", "--candidate"},
		// Not an operand the check can take: a candidate check cannot
		// import a key, because importing is a write.
		"with --ssh-key-file": candidateArgs(t, configPath, "--ssh-key-file", writeTestPrivateKey(t)),
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(args); got != 2 {
				t.Errorf("run(%v) = %d, want 2", args, got)
			}
		})
	}
}

func TestRun1253_CandidateConnectionTestIsInUsage(t *testing.T) {
	text := captureStderr(t, usage)
	if !strings.Contains(text, "backup-set test-connection --candidate") {
		t.Errorf("usage() does not list `backup-set test-connection --candidate`")
	}
}

// On a host with no configuration yet, the check runs against this host's
// own first-run surface, which is where the wizard's button sends it in
// that process (#1254's shape), and still writes nothing: no config.yaml
// appears because somebody asked whether a source works.
func TestRun1253_CandidateConnectionTestRunsOnAFirstRunHost(t *testing.T) {
	clearInheritedRouteSettings()
	dir := t.TempDir()
	t.Setenv("STATE_DATABASE", filepath.Join(dir, "state.db"))
	configPath := filepath.Join(dir, "config.yaml")
	aStoredKey(t, configPath, "key_cand")

	var code int
	stdout := captureStdout(t, func() {
		code = run(candidateArgs(t, configPath))
	})
	if code != 1 {
		t.Fatalf("test-connection --candidate with no configuration = %d, want 1 (a closed port, reported):\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "connect") || !strings.Contains(stdout, "nothing was written") {
		t.Errorf("the first-run check does not report its steps and say it saved nothing:\n%s", stdout)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a candidate check on a first-run host created %s (stat: %v)", configPath, err)
	}
}
