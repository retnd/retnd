package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// #1246: what config.yaml says about the workflow root. The mounted default
// is resolved by the service and never lives on a Config (core/service's
// ResolveWorkflowRoot); this package holds the file's half: workflows.disabled,
// and keeping the validator directories out of every hook stage.

// noWorkflowConfig is a configuration written before anybody set a root: no
// workflows block at all, which is what a Docker host install has on disk.
func noWorkflowConfig() Config {
	return Config{
		PollInterval: Duration(time.Minute),
		State:        State{Database: "/var/lib/retnd/state.db"},
		Sources:      []Source{},
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

// The validator directories are written by the engine through the API, so a
// hook stage pointed at one would run whatever an upload put there.
func TestValidateRefusesAHookStageThatIsAValidatorDirectory(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{
		CustomValidatorDir,
		"./" + CustomValidatorDir,
		CustomValidatorDir + "/",
		"/workflows/" + CustomValidatorDir,
		CustomValidatorDir + ".superseded",
		"/workflows/" + CustomValidatorDir + ".superseded/",
	} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			global := workflowConfig()
			global.Workflows.Global.BeforeDir = dir
			if err := global.Validate(); err == nil || !strings.Contains(err.Error(), "workflows.global.before_dir") {
				t.Errorf("global before_dir %q: Validate() = %v", dir, err)
			}

			set := workflowConfig()
			set.Sources[0].BackupSets[0].Workflow.AfterDir = dir
			if err := set.Validate(); err == nil || !strings.Contains(err.Error(), "workflow.after_dir") {
				t.Errorf("per-set after_dir %q: Validate() = %v", dir, err)
			}
		})
	}
}

// And a stage that only looks like one is still fine.
func TestValidateAcceptsAStageNamedNearAValidatorDirectory(t *testing.T) {
	t.Parallel()

	cfg := workflowConfig()
	cfg.Workflows.Global.BeforeDir = CustomValidatorDir + "-mine"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}
