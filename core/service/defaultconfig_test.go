package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDefaultConfig_WritesOnceWithMode0600(t *testing.T) {
	configPath, statePath := emptyInstall(t)
	defaults := FirstRunDefaults{ConfigPath: configPath, StateDatabase: statePath}

	wrote, path, err := EnsureDefaultConfig(defaults)
	if err != nil || !wrote {
		t.Fatalf("first call = (%v, %q, %v), want it to write", wrote, path, err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	first, _ := os.ReadFile(configPath)
	if !bytes.HasPrefix(first, []byte("# Written by retnd")) {
		t.Errorf("default configuration lost its header comment:\n%s", first)
	}

	wrote, _, err = EnsureDefaultConfig(defaults)
	if err != nil || wrote {
		t.Fatalf("second call = (%v, %v), want a no-op", wrote, err)
	}
	second, _ := os.ReadFile(configPath)
	if !bytes.Equal(first, second) {
		t.Errorf("a second call changed the file")
	}
}

func TestEnsureDefaultConfig_NeverOverwritesAnExistingFile(t *testing.T) {
	configPath, statePath := emptyInstall(t)
	original := []byte("poll_interval: 2h\nstate:\n  database: " + statePath + "\nsources: []\n# mine\n")
	if err := os.WriteFile(configPath, original, 0o640); err != nil {
		t.Fatal(err)
	}
	wrote, _, err := EnsureDefaultConfig(FirstRunDefaults{ConfigPath: configPath, StateDatabase: statePath})
	if err != nil || wrote {
		t.Fatalf("= (%v, %v), want a no-op on an existing file", wrote, err)
	}
	got, _ := os.ReadFile(configPath)
	if !bytes.Equal(got, original) {
		t.Errorf("an existing configuration was changed:\n%s", got)
	}
}

func TestEnsureDefaultConfig_TheDefaultFileLoadsAndOpens(t *testing.T) {
	configPath, statePath := emptyInstall(t)
	if _, _, err := EnsureDefaultConfig(FirstRunDefaults{ConfigPath: configPath, StateDatabase: statePath}); err != nil {
		t.Fatal(err)
	}
	svc, cleanup, err := Open(t.Context(), configPath)
	if err != nil {
		t.Fatalf("Open on the engine's own default configuration: %v", err)
	}
	_ = svc
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestEnsureDefaultConfig_AnUnwritableDirectoryIsReportedNotSwallowed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "config")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ro, "config.yaml")
	wrote, _, err := EnsureDefaultConfig(FirstRunDefaults{ConfigPath: path, StateDatabase: filepath.Join(dir, "state.db")})
	if err == nil || wrote || errors.Is(err, ErrDefaultConfigSkipped) {
		t.Fatalf("= (%v, %v), want a real error naming the unwritable directory", wrote, err)
	}
	if ConfigDirWritable(path) == nil {
		t.Errorf("ConfigDirWritable said an unwritable directory was fine")
	}
}

// The first set created into a default configuration seeds the local
// destination, exactly as the wizard's first-configuration path does, so
// both doors leave the same file behind (retnd#1049).
func TestCreateBackupSet_IntoADefaultConfigSeedsTheLocalDestinationOnce(t *testing.T) {
	configPath, statePath := emptyInstall(t)
	if _, _, err := EnsureDefaultConfig(FirstRunDefaults{ConfigPath: configPath, StateDatabase: statePath}); err != nil {
		t.Fatal(err)
	}
	svc, cleanup, err := Open(t.Context(), configPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = cleanup() }()

	req := validCreateReq(t, svc, "alpha")
	req.SourceName = "production"
	if err := os.MkdirAll(filepath.Dir(req.LocalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateBackupSet(t.Context(), req); err != nil {
		t.Fatalf("CreateBackupSet into a default configuration: %v", err)
	}
	mediums := func() int {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		return bytes.Count(raw, []byte("type: local_volume"))
	}
	if got := mediums(); got != 1 {
		t.Fatalf("local destinations after the first set = %d, want 1", got)
	}

	second := validCreateReq(t, svc, "beta")
	second.SourceName = "production"
	if err := os.MkdirAll(filepath.Dir(second.LocalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateBackupSet(t.Context(), second); err != nil {
		t.Fatalf("second CreateBackupSet: %v", err)
	}
	if got := mediums(); got != 1 {
		t.Errorf("local destinations after a second set = %d, want still 1 (only the first set seeds it)", got)
	}
}

func TestIsDefaultSettings_TrueForTheDefaultsFalseOnceAnythingChanged(t *testing.T) {
	d := DefaultSettings()
	if !IsDefaultSettings(d) {
		t.Fatal("the defaults are not default")
	}
	changed := d
	changed.Service.PollInterval *= 2
	if IsDefaultSettings(changed) {
		t.Errorf("a changed polling interval still reads as the defaults")
	}
}
