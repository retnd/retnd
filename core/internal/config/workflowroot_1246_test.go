package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// #1246: a deployment whose config.yaml names no workflow root uses the
// conventional one when it is there. The directory check is the caller's
// (this package opens nothing), so every test here hands in its own answer
// and none of them depends on a real /workflows.

// noWorkflowConfig is a configuration written before anybody set a root: no
// workflows block at all, which is what a Docker host install has on disk.
func noWorkflowConfig() Config {
	return Config{
		PollInterval: Duration(time.Minute),
		State:        State{Database: "/var/lib/retnd/state.db"},
		Sources:      []Source{},
	}
}

// present answers "yes" for exactly one directory and records whether it was
// asked at all.
func present(dir string, asked *bool) func(string) bool {
	return func(d string) bool {
		*asked = true
		return d == dir
	}
}

func TestDefaultWorkflowRoot_AConfigWithNoRootUsesTheMountedOne(t *testing.T) {
	t.Parallel()

	cfg := noWorkflowConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	var asked bool
	cfg.ApplyDefaultWorkflowRoot(DefaultWorkflowRoot, present(DefaultWorkflowRoot, &asked))

	if got := cfg.WorkflowRoot(); got != DefaultWorkflowRoot {
		t.Errorf("WorkflowRoot() = %q, want %q: the directory is mounted and nothing says not to use it", got, DefaultWorkflowRoot)
	}
	if !cfg.WorkflowRootIsDefault() {
		t.Error("WorkflowRootIsDefault() = false; a root the file does not name has to be reported as the default, not as configured")
	}
	if !cfg.WorkflowsConfigured() {
		t.Error("WorkflowsConfigured() = false with a usable default root")
	}
	if cfg.Workflows.Root != "" {
		t.Errorf("Workflows.Root = %q; the field is what the file says, and the file says nothing", cfg.Workflows.Root)
	}
}

func TestDefaultWorkflowRoot_NothingMountedMeansNoRoot(t *testing.T) {
	t.Parallel()

	cfg := noWorkflowConfig()
	var asked bool
	cfg.ApplyDefaultWorkflowRoot(DefaultWorkflowRoot, present("/somewhere-else", &asked))

	if !asked {
		t.Error("the directory was never checked")
	}
	if cfg.WorkflowRoot() != "" || cfg.WorkflowRootIsDefault() || cfg.WorkflowsConfigured() {
		t.Errorf("root = %q (default %v); a directory that is not there is not a root", cfg.WorkflowRoot(), cfg.WorkflowRootIsDefault())
	}
}

func TestDefaultWorkflowRoot_AnExplicitRootWins(t *testing.T) {
	t.Parallel()

	cfg := noWorkflowConfig()
	cfg.Workflows.Root = "/srv/hooks"
	var asked bool
	cfg.ApplyDefaultWorkflowRoot(DefaultWorkflowRoot, present(DefaultWorkflowRoot, &asked))

	if got := cfg.WorkflowRoot(); got != "/srv/hooks" {
		t.Errorf("WorkflowRoot() = %q; the operator's root has to win over the default", got)
	}
	if cfg.WorkflowRootIsDefault() {
		t.Error("an explicit root is reported as the default")
	}
	if asked {
		t.Error("the default directory was probed for a configuration that names its own root")
	}
}

func TestDefaultWorkflowRoot_AClearedRootStaysOff(t *testing.T) {
	t.Parallel()

	const doc = "poll_interval: 1m\nstate:\n  database: /var/lib/retnd/state.db\nsources: []\nworkflows:\n  disabled: true\n"
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("a config with workflows.disabled does not parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	var asked bool
	cfg.ApplyDefaultWorkflowRoot(DefaultWorkflowRoot, present(DefaultWorkflowRoot, &asked))

	if cfg.WorkflowRoot() != "" || cfg.WorkflowsConfigured() {
		t.Errorf("WorkflowRoot() = %q; an operator who turned workflows off has to stay off whatever is mounted", cfg.WorkflowRoot())
	}
}

func TestDefaultWorkflowRoot_IsIdempotent(t *testing.T) {
	t.Parallel()

	cfg := noWorkflowConfig()
	var asked bool
	cfg.ApplyDefaultWorkflowRoot(DefaultWorkflowRoot, present(DefaultWorkflowRoot, &asked))
	cfg.ApplyDefaultWorkflowRoot(DefaultWorkflowRoot, present(DefaultWorkflowRoot, &asked))

	if cfg.WorkflowRoot() != DefaultWorkflowRoot || !cfg.WorkflowRootIsDefault() {
		t.Errorf("after two applications root = %q (default %v)", cfg.WorkflowRoot(), cfg.WorkflowRootIsDefault())
	}
}

// The default is what this process resolved, not what the operator wrote, so
// a settings save of a configuration that picked it up must not freeze it into
// config.yaml (or add a workflows block an older binary would refuse).
func TestDefaultWorkflowRoot_IsNeverWrittenBack(t *testing.T) {
	t.Parallel()

	cfg := noWorkflowConfig()
	var asked bool
	cfg.ApplyDefaultWorkflowRoot(DefaultWorkflowRoot, present(DefaultWorkflowRoot, &asked))

	out, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(out), "workflows") || strings.Contains(string(out), "root:") {
		t.Errorf("the default root reached the marshalled file:\n%s", out)
	}
}

func TestDefaultWorkflowRoot_ARootAndDisabledTogetherIsRefused(t *testing.T) {
	t.Parallel()

	cfg := noWorkflowConfig()
	cfg.Workflows.Root = "/srv/hooks"
	cfg.Workflows.Disabled = true

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "workflows.disabled") {
		t.Errorf("Validate() = %v; a root that is also turned off is two statements that cannot both be meant", err)
	}
}

func TestDefaultWorkflowRoot_DisabledRoundTrips(t *testing.T) {
	t.Parallel()

	cfg := noWorkflowConfig()
	cfg.Workflows.Disabled = true

	out, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), "disabled: true") {
		t.Fatalf("a cleared root does not survive a save:\n%s", out)
	}
	var back Config
	if err := yaml.Unmarshal(out, &back); err != nil || !back.Workflows.Disabled {
		t.Errorf("read back disabled = %v, %v", back.Workflows.Disabled, err)
	}
}
