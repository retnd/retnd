package service

import (
	"io"
	"os"

	"github.com/retnd/retnd/core/internal/config"
)

// The default workflow root (#1246).
//
// A Docker host install makes <prefix>/workflows and mounts it at
// /workflows, and never wrote a workflows block into config.yaml. So the
// engine had no root and custom validators refused with "no workflow root
// is configured", on a deployment that had the directory mounted the whole
// time. A configuration that names no root now runs on the default when
// that directory is there, and every surface says it is the default.
//
// The resolved root belongs to this package and never to config.Config. A
// Config is what the file says, and every rule config.Validate applies to
// hook directories, environments and the runner reads the file's root, so
// a copy of the running Config validates exactly as the file will on the
// next start. What the default reaches is what has no file-side rule:
// custom validators (where they are kept, and the stage a recovery test
// runs one from) and the reports that say which root is in use.
//
// It is resolved where a configuration becomes the running one: OpenWith
// (through useWorkflowRootDefault) and every reload (adoptConfig). A mount
// that appears later is picked up by the next configuration change or
// restart. Where it is looked for, and how, comes from the caller
// (OpenOptions.WorkflowRoot), so no test reads the real /workflows of the
// machine it runs on and no package variable is swapped to stop it.
//
// The file is not rewritten to say so. A write that needs a root in the
// FILE, because config.Validate requires one beside what it adds (a stage
// directory, a hook variable), writes the default there with it
// (writeDefaultWorkflowRoot).

// WorkflowRootDefault is where a deployment whose configuration names no
// workflow root looks for one, and how it decides the directory is there.
// The zero value looks for none.
type WorkflowRootDefault struct {
	Dir     string
	Present func(dir string) bool
}

// ProductionWorkflowRootDefault is config.DefaultWorkflowRoot, present when
// it is a directory. The engine's serve and the CLI pass it; nothing else
// should.
func ProductionWorkflowRootDefault() WorkflowRootDefault {
	return WorkflowRootDefault{Dir: config.DefaultWorkflowRoot, Present: func(dir string) bool {
		info, err := os.Stat(dir)
		return err == nil && info.IsDir()
	}}
}

// WorkflowRootState is the root a process uses for one configuration.
type WorkflowRootState struct {
	// Path is the root, "" for none.
	Path string
	// Default says Path is the default rather than the file's root.
	Default bool
	// Disabled says the file turned workflows off (workflows.disabled), so
	// no default is looked for either.
	Disabled bool
	// LookedFor is the default's directory when one was looked for and not
	// found, so a refusal can name it.
	LookedFor string
}

// ResolveWorkflowRoot is the root cfg runs on: the file's, else the default
// when d finds it, else none. An explicit root and a cleared one never ask
// d anything.
func ResolveWorkflowRoot(cfg *config.Config, d WorkflowRootDefault) WorkflowRootState {
	w := cfg.Workflows
	switch {
	case w.Root != "":
		return WorkflowRootState{Path: w.Root}
	case w.Disabled:
		return WorkflowRootState{Disabled: true}
	case d.Dir == "" || d.Present == nil:
		return WorkflowRootState{}
	case d.Present(d.Dir):
		return WorkflowRootState{Path: d.Dir, Default: true}
	default:
		return WorkflowRootState{LookedFor: d.Dir}
	}
}

// OpenOptions are OpenWith's.
type OpenOptions struct {
	// LogTo is where the structured log goes; nil is standard output.
	LogTo io.Writer
	// WorkflowRoot is the default root this process looks for (#1246).
	WorkflowRoot WorkflowRootDefault
}

// useWorkflowRootDefault sets where this service looks for a default root
// and resolves the running configuration against it. Called once, by
// OpenWith, before the service is handed to anyone.
func (b *BackupService) useWorkflowRootDefault(d WorkflowRootDefault) {
	b.configMu.Lock()
	defer b.configMu.Unlock()
	b.rootDefault = d
	st := b.state.Load()
	b.state.Store(&configState{inner: st.inner, revision: st.revision, workflowRoot: ResolveWorkflowRoot(st.inner.Config, d)})
}

// workflowRoot is the root the running configuration uses.
func (b *BackupService) workflowRoot() WorkflowRootState {
	return b.state.Load().workflowRoot
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
	running := b.workflowRoot()
	if file.Workflows.Root != "" || file.Workflows.Disabled || !running.Default {
		return
	}
	file.Workflows.Root = running.Path
}
