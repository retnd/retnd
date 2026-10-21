package service

import (
	"context"
	"io"
	"os"

	"github.com/retnd/retnd/core/internal/config"
)

// The default workflow root (#1246).
//
// A Docker host install makes <prefix>/workflows and mounts it at
// /workflows, and never wrote a workflows block into config.yaml. So the
// engine had no root, custom validators refused with "no workflow root is
// configured", and no hook could run, on a deployment that had the
// directory mounted the whole time. A configuration that names no root now
// uses config.DefaultWorkflowRoot when that directory is there, and every
// surface says it is the default (config.Config.ApplyDefaultWorkflowRoot
// has the rule; this file is where the directory is looked at).
//
// It is resolved where a configuration becomes the running one: when a
// deployment is opened (OpenConfigAndJournal, and New for a caller that
// built its own) and on every reload (adoptConfig). A mount that appears
// later is picked up by the next configuration change or restart, which is
// what the refusal below tells an operator.
//
// The file is not rewritten to say so. A write that needs a root in the
// FILE, because config.Validate requires one beside what it adds (a stage
// directory, a hook variable), writes the default there with it
// (writeDefaultWorkflowRoot), so the file never says less than the
// deployment does; the installer's upgrade writes it for a Docker host too.

// defaultWorkflowRoot and workflowRootPresent are where the default root is
// looked for and how. Variables, so a test points both at a directory of
// its own rather than depending on what /workflows is on the machine
// running it.
var (
	defaultWorkflowRoot = config.DefaultWorkflowRoot
	workflowRootPresent = func(dir string) bool {
		info, err := os.Stat(dir)
		return err == nil && info.IsDir()
	}
)

// applyDefaultWorkflowRoot resolves cfg's default root against this
// machine. Idempotent: every door a configuration comes through calls it,
// and a configuration that came through two of them is asked once.
func applyDefaultWorkflowRoot(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.ApplyDefaultWorkflowRoot(defaultWorkflowRoot, workflowRootPresent)
}

// writeDefaultWorkflowRoot puts the running process's default root into
// file, the configuration a write is about to save, when file names none
// and has not turned workflows off. A write that adds a hook directory or
// a hook variable needs a root in the file (config.Validate refuses one
// without), and refusing it on a deployment that is running on that root
// would be the original bug one step further on.
//
// The caller holds configMu and has re-read file from disk.
func (b *BackupService) writeDefaultWorkflowRoot(file *config.Config) {
	running := b.state.Load().inner.Config
	if file.Workflows.Root != "" || file.Workflows.Disabled || !running.WorkflowRootIsDefault() {
		return
	}
	file.Workflows.Root = running.WorkflowRoot()
}

// Placeholders for the #1246 review's injected default; the fix fills them in.

// WorkflowRootDefault is where a deployment with no root looks for one.
type WorkflowRootDefault struct {
	Dir     string
	Present func(dir string) bool
}

// WorkflowRootState is the root a running process uses.
type WorkflowRootState struct {
	Path     string
	Default  bool
	Disabled bool
}

// ResolveWorkflowRoot resolves cfg's root against d.
func ResolveWorkflowRoot(cfg *config.Config, d WorkflowRootDefault) WorkflowRootState {
	return WorkflowRootState{Path: cfg.WorkflowRoot(), Default: cfg.WorkflowRootIsDefault(), Disabled: cfg.Workflows.Disabled}
}

// OpenOptions are Open's.
type OpenOptions struct {
	LogTo        io.Writer
	WorkflowRoot WorkflowRootDefault
}

// OpenWith is Open with options.
func OpenWith(ctx context.Context, configPath string, opts OpenOptions) (*BackupService, func() error, error) {
	return OpenWithLog(ctx, configPath, os.Stdout)
}
