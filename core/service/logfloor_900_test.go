package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/obs"
)

// FR-100 (#900): observability.logs.level is the engine's log floor, the
// environment outranks it, a PATCH moves it without a restart, and the read
// says which of the two is in force.

func openLogFloorService(t *testing.T, logsBlock string) (*BackupService, *bytes.Buffer) {
	t.Helper()
	dir := symlinkFreeTempDir(t)
	remote, local := filepath.Join(dir, "remote"), filepath.Join(dir, "local")
	for _, d := range []string{remote, local} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	content := "poll_interval: 15m\n" +
		"state:\n  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n  - id: production\n    backup_sets:\n      - id: alpha\n        remote:\n          type: local\n" +
		"        remote_path: " + remote + "\n        local_path: " + local + "\n" +
		"        include:\n          - \"*.dump\"\n        completion:\n          strategy: rename\n        stale_after: 24h\n" +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n" + logsBlock
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	svc, cleanup, err := OpenWith(context.Background(), configPath, OpenOptions{LogTo: &buf})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(); _ = cleanup() })
	return svc, &buf
}

func clearLogEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RETND_DEBUG", "")
	t.Setenv("RM_DEBUG", "")
	t.Setenv("LOG_LEVEL", "")
}

func TestLogFloor_TheConfiguredLevelIsTheFloorWhenTheEnvironmentSaysNothing(t *testing.T) {
	clearLogEnv(t)
	svc, _ := openLogFloorService(t, "observability:\n  logs:\n    level: warn\n")
	if got := svc.logger.Floor(); got != obs.LevelWarn {
		t.Errorf("floor = %v, want WARN", got)
	}
	s, _ := svc.ObservabilitySettings(context.Background())
	if s.Logs.Level != "warn" || s.Logs.LevelInForce != "warn" || s.Logs.LevelSource != LogLevelSourceConfig {
		t.Errorf("settings = %+v, want warn in force from config", s.Logs)
	}
}

func TestLogFloor_TheEnvironmentWinsAndTheReadSaysSo(t *testing.T) {
	clearLogEnv(t)
	t.Setenv("LOG_LEVEL", "error")
	svc, _ := openLogFloorService(t, "observability:\n  logs:\n    level: debug\n")
	if got := svc.logger.Floor(); got != obs.LevelError {
		t.Errorf("floor = %v, want ERROR from LOG_LEVEL", got)
	}
	s, _ := svc.ObservabilitySettings(context.Background())
	if s.Logs.Level != "debug" || s.Logs.LevelInForce != "error" || s.Logs.LevelSource != LogLevelSourceEnvironment {
		t.Errorf("settings = %+v, want configured debug, error in force, from the environment", s.Logs)
	}
}

func TestLogFloor_APatchMovesTheFloorWithoutARestart(t *testing.T) {
	clearLogEnv(t)
	svc, buf := openLogFloorService(t, "")
	ctx := context.Background()
	svc.logger.Event(ctx, obs.LevelDebug, "probe", "quiet")
	if bytes.Contains(buf.Bytes(), []byte("quiet")) {
		t.Fatal("a debug line was written at the default info floor")
	}
	debug := "debug"
	s, err := svc.UpdateObservabilitySettings(ctx, UpdateObservabilitySettingsRequest{LogsLevel: &debug})
	if err != nil {
		t.Fatal(err)
	}
	if s.Logs.LevelInForce != "debug" || s.Logs.LevelSource != LogLevelSourceConfig {
		t.Errorf("after the PATCH settings = %+v, want debug in force from config", s.Logs)
	}
	svc.logger.Event(ctx, obs.LevelDebug, "probe", "loud")
	if !bytes.Contains(buf.Bytes(), []byte("loud")) {
		t.Errorf("the PATCH did not reach the logger the process holds; wrote %q", buf.String())
	}
}

func TestLogFloor_APatchCannotOverrideTheEnvironment(t *testing.T) {
	clearLogEnv(t)
	t.Setenv("RETND_DEBUG", "1")
	svc, _ := openLogFloorService(t, "")
	errLevel := "error"
	s, err := svc.UpdateObservabilitySettings(context.Background(), UpdateObservabilitySettingsRequest{LogsLevel: &errLevel})
	if err != nil {
		t.Fatal(err)
	}
	if svc.logger.Floor() != obs.LevelDebug || s.Logs.LevelInForce != "debug" || s.Logs.LevelSource != LogLevelSourceEnvironment {
		t.Errorf("floor %v, settings %+v: RETND_DEBUG must still win after a PATCH", svc.logger.Floor(), s.Logs)
	}
}
