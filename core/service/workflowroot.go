package service

import (
	"os"

	"github.com/retnd/retnd/core/internal/config"
)

// Placeholders for #1246's default workflow root.
var (
	defaultWorkflowRoot = config.DefaultWorkflowRoot
	workflowRootPresent = func(dir string) bool {
		info, err := os.Stat(dir)
		return err == nil && info.IsDir()
	}
	renameValidatorFile = os.Rename
)
