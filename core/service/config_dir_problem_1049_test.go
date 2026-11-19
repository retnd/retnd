package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// retnd#1049: when the engine cannot write its default configuration
// because the directory is read-only, the setup flow has to say which
// directory and what to do. The sentence is one function, so the command
// line and the web interface cannot drift apart.

func TestFirstRun_ConfigDirProblemNamesTheUnwritableDirectoryAndTheFix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere, so the directory cannot be made unwritable")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "config")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })

	f, err := NewFirstRun(FirstRunDefaults{
		ConfigPath:    filepath.Join(ro, "config.yaml"),
		StateDatabase: filepath.Join(dir, "state", "state.db"),
	})
	if err != nil {
		t.Fatal(err)
	}

	gotDir, message := f.ConfigDirProblem()
	if gotDir != ro {
		t.Errorf("ConfigDirProblem dir = %q, want %q", gotDir, ro)
	}
	if message != ConfigDirUnwritableMessage(ro) {
		t.Errorf("message = %q, want the shared sentence %q", message, ConfigDirUnwritableMessage(ro))
	}
	for _, want := range []string{ro, "not writable", "writable"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not say %q:\n%s", want, message)
		}
	}
}

func TestFirstRun_ConfigDirProblemIsEmptyWhenTheDirectoryCanBeWritten(t *testing.T) {
	configPath, statePath := emptyInstall(t)
	f, err := NewFirstRun(FirstRunDefaults{ConfigPath: configPath, StateDatabase: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if dir, message := f.ConfigDirProblem(); message != "" {
		t.Errorf("a writable configuration directory %q reported a problem: %s", dir, message)
	}
}
