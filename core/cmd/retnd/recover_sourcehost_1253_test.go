package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #1253: `recover --to source: --preflight` describes where a
// source-host recovery would write from configuration alone, the way the
// browser's recovery dialog does on open, and touches the host not at all.
// The write check, which writes one probe file on a production machine
// and removes it, is behind --write-check, the way it is behind a button
// in the browser.

// aCountingHost is a loopback listener standing in for the source host. It
// accepts whatever arrives and closes it, and counts: the only question
// these tests ask of it is whether anything connected at all.
func aCountingHost(t *testing.T) (port int, connections *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, &n
}

// writeSFTPTestConfigReaching is writeSFTPTestConfig with the source host
// pointed at a port on this machine, so a connection can be noticed.
func writeSFTPTestConfigReaching(t *testing.T, port int) string {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	known := filepath.Join(dir, "known_hosts")
	for _, f := range []string{key, known} {
		if err := os.WriteFile(f, []byte("unused by these tests\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	content := "poll_interval: 15m\n" +
		"state:\n" +
		"  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n" +
		"  - id: production\n" +
		"    backup_sets:\n" +
		"      - id: postgres-primary\n" +
		"        remote:\n" +
		"          type: sftp\n" +
		"          host: 127.0.0.1\n" +
		"          port: " + strconv.Itoa(port) + "\n" +
		"          user: retnd-pull\n" +
		"          key:\n" +
		"            file: " + key + "\n" +
		"          known_hosts: " + known + "\n" +
		"        remote_path: /var/backups/postgres\n" +
		"        local_path: " + filepath.Join(dir, "local") + "\n" +
		"        completion:\n" +
		"          strategy: rename\n" +
		"        stale_after: 24h\n" +
		"retention:\n" +
		"  timezone: UTC\n" +
		"  week_starts_on: monday\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

type sourcePreflightJSON struct {
	Available   bool   `json:"available"`
	URL         string `json:"url"`
	WriteAccess string `json:"write_access"`
	Destination string `json:"destination_id"`
}

func runSourcePreflight(t *testing.T, args ...string) (int, sourcePreflightJSON) {
	t.Helper()
	var code int
	stdout := captureStdout(t, func() { code = run(args) })
	var got sourcePreflightJSON
	if code == exitOK {
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, stdout)
		}
	}
	return code, got
}

func TestRecover1253_PreflightDescribesWithoutTouchingTheHost(t *testing.T) {
	port, connections := aCountingHost(t)
	configPath := writeSFTPTestConfigReaching(t, port)

	code, got := runSourcePreflight(t, "recover", "--config", configPath, recoverID, "--to", "source:", "--preflight", "--json")
	if code != exitOK {
		t.Fatalf("recover --to source: --preflight = %d", code)
	}
	if !got.Available || got.WriteAccess != "not_checked" || !strings.HasPrefix(got.URL, "sftp://") || !strings.HasPrefix(got.Destination, "sh1-") {
		t.Errorf("preflight = %+v, want the description with write_access not_checked", got)
	}
	if n := connections.Load(); n != 0 {
		t.Errorf("the preflight connected to the source host %d time(s); a description is read from configuration and must not", n)
	}
}

func TestRecover1253_PreflightOfASetDescribesWithoutTouchingTheHost(t *testing.T) {
	port, connections := aCountingHost(t)
	configPath := writeSFTPTestConfigReaching(t, port)

	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"recover", "--config", configPath, "--set", "production/postgres-primary", "--to", "source:", "--preflight"})
	})
	// The plan after the description is about a set with nothing on
	// record, and may refuse as such; what this test is about is the
	// description printed before it.
	if code == exitUsage {
		t.Fatalf("recover --set --to source: --preflight = %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "not_checked") {
		t.Errorf("the set preflight does not say the write access is unchecked:\n%s", stdout)
	}
	if n := connections.Load(); n != 0 {
		t.Errorf("the set preflight connected to the source host %d time(s)", n)
	}
}

func TestRecover1253_WriteCheckIsTheProbeAndIsExplicit(t *testing.T) {
	port, connections := aCountingHost(t)
	configPath := writeSFTPTestConfigReaching(t, port)

	code, got := runSourcePreflight(t, "recover", "--config", configPath, recoverID, "--to", "source:", "--preflight", "--write-check", "--json")
	if code != exitOK {
		t.Fatalf("recover --to source: --preflight --write-check = %d", code)
	}
	// The stand-in is not an SSH server, so the probe cannot get in; what
	// matters is that it tried, which is the whole difference the flag makes.
	if got.WriteAccess != "unreachable" {
		t.Errorf("write check = %+v, want write_access unreachable from a host that is not an SSH server", got)
	}
	if n := connections.Load(); n == 0 {
		t.Error("--write-check never connected to the source host, so it ran no probe")
	}
}

func TestRecover1253_WriteCheckWithoutThePreflightIsAUsageError(t *testing.T) {
	port, _ := aCountingHost(t)
	configPath := writeSFTPTestConfigReaching(t, port)
	for name, args := range map[string][]string{
		"alone":               {"recover", "--config", configPath, recoverID, "--to", "source:", "--write-check"},
		"with another target": {"recover", "--config", configPath, recoverID, "--to", "-", "--preflight", "--write-check"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(args); got != exitUsage {
				t.Errorf("run(%v) = %d, want %d", args, got, exitUsage)
			}
		})
	}
}

func TestRecover1253_UsageSaysThePreflightIsReadOnly(t *testing.T) {
	text := captureStderr(t, usage)
	if !strings.Contains(text, "--write-check") {
		t.Errorf("usage() never names --write-check")
	}
	if strings.Contains(text, "--to source: --preflight prints\n                                                  where it would write and runs the write check") {
		t.Errorf("usage() still says the preflight runs the write check")
	}
}
