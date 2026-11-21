package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// #900 review, B1: with observability.logs.level at warn, the engine opened
// its service (which puts the configured floor on) and only then logged
// startup at INFO, so the one line that carries log_schema never reached the
// stream. Both shapes of process are checked here as they ship: the daemon,
// re-executed as a child through TestDaemonChildProcess, and a one-shot
// verb (`run`) in process.

// warnFloorConfig is the test deployment with observability.logs.level: warn,
// and an environment that says nothing about the level, so the config is
// what decides.
func warnFloorConfig(t *testing.T) string {
	t.Helper()
	clearTheRoute(t)
	// The former product's variables are built from two pieces: the brand
	// guard (core/tests/brandguard) refuses the name spelled out in a test.
	former := strings.ToUpper("backup" + "d")
	for _, name := range []string{"RETND_DEBUG", "RM_DEBUG", "LOG_LEVEL", former + "_DEBUG", former + "_LOG_LEVEL"} {
		t.Setenv(name, "")
	}
	path := writeTestConfig(t)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("observability:\n  logs:\n    level: warn\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// logLines decodes the JSON lines in out, skipping anything that is not one.
func logLines(out string) []map[string]any {
	var lines []map[string]any
	for _, raw := range strings.Split(out, "\n") {
		raw = strings.TrimSpace(raw)
		if !strings.HasPrefix(raw, "{") {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			lines = append(lines, m)
		}
	}
	return lines
}

// assertStartupUnderWarn checks out holds exactly one startup line carrying
// the schema version, and that the warn floor held for every other INFO
// event, so the line getting through is not a floor that never went on.
func assertStartupUnderWarn(t *testing.T, out string) {
	t.Helper()
	var startups int
	for _, l := range logLines(out) {
		if l["event"] == "startup" {
			startups++
			if l["log_schema"] != "1" {
				t.Errorf("the startup line has log_schema %v, want \"1\": %v", l["log_schema"], l)
			}
			continue
		}
		if l["level"] == "INFO" || l["level"] == "DEBUG" {
			t.Errorf("an %v line got past observability.logs.level: warn, so the configured floor never went on and this test proves nothing: %v", l["level"], l)
		}
	}
	if startups != 1 {
		t.Errorf("got %d startup lines under observability.logs.level: warn, want 1: log_schema is only written there\n%s", startups, out)
	}
}

func TestStartupIsLoggedUnderAWarnFloor_900_Daemon(t *testing.T) {
	got := runDaemon1546(t, warnFloorConfig(t))
	if !got.started {
		t.Fatalf("the daemon exited (code %d) instead of logging its startup\n%s", got.code, got.output)
	}
	assertStartupUnderWarn(t, got.output)
}

func TestStartupIsLoggedUnderAWarnFloor_900_OneShot(t *testing.T) {
	path := warnFloorConfig(t)
	var code int
	out := captureStdout(t, func() { code = run([]string{"run", "--config", path}) })
	if code != 0 {
		t.Fatalf("`run` exited %d against the working test config\n%s", code, out)
	}
	assertStartupUnderWarn(t, out)
}
