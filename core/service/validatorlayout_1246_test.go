package service

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
)

// #1246, review round: the engine's idea of the packaged layout (the default
// root, the stage directory, its superseded sibling) is restated in
// canonical.json, the compose file and the installer. distribution and the
// installer suite hold their copies to canonical.json; this holds the engine's.
func TestThePackagedLayoutIsTheEnginesLayout(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "distribution", "packaging", "canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	var canonical struct {
		ContainerPaths map[string]string `json:"containerPaths"`
	}
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatal(err)
	}
	paths := canonical.ContainerPaths
	for role, want := range map[string]string{
		"workflows":            config.DefaultWorkflowRoot,
		"validators":           path.Join(config.DefaultWorkflowRoot, config.CustomValidatorDir),
		"validatorsSuperseded": path.Join(config.DefaultWorkflowRoot, config.CustomValidatorDir+supersededSuffix),
	} {
		if paths[role] != want {
			t.Errorf("canonical.json containerPaths.%s = %q, and the engine uses %q", role, paths[role], want)
		}
	}
}
