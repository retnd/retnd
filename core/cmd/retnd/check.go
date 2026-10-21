package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/service"
)

// cmdCheck is `retnd check`: a pre-flight answer to "can this
// deployment actually start" (see internal/app.Check's doc for exactly
// what it validates and, just as importantly, what it deliberately does
// not: it never contacts a configured remote).
//
// It is also one of FR-38's three reporting surfaces. EPIC R renamed the
// container-internal configuration directory, and a deployment whose
// compose file still mounts the pre-rename path is served from that path
// instead (core/legacypath). The warning that says so is printed once per
// start, into a log; this command is where an operator asks the question
// later, and it has to answer with the path that is really live rather
// than the one this release would have chosen.
func cmdCheck(args []string) int {
	fs, cfgPath := newFlagSet("check")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// A fresh install has no configuration yet, and the engine writes a
	// default on its first start (retnd#1049). So the question check
	// answers here is whether that write can happen: a writable
	// configuration directory passes, an unwritable one is the one real
	// thing to fix and is named.
	if _, absent := configAbsentAt(*cfgPath); absent {
		return printFirstRunNotice(os.Stdout, *cfgPath, "check")
	}

	cfg, preflight, err := app.Check(context.Background(), *cfgPath)
	if err != nil {
		return fail(err)
	}

	sets := 0
	for _, src := range cfg.Sources {
		sets += len(src.BackupSets)
	}
	// A configuration with no backup set is valid on a fresh install
	// (retnd#1049), but not over a journal that holds backups: that is a
	// configuration that lost its sets, and `check` is the command an
	// operator runs to ask whether the configuration is right.
	if len(cfg.Sources) == 0 {
		n, _, jerr := state.CountArtifactsReadOnly(context.Background(), cfg.State.Database)
		if jerr != nil || n > 0 {
			held := fmt.Sprintf("holds %d backup(s)", n)
			if jerr != nil {
				held = fmt.Sprintf("cannot be read (%v)", jerr)
			}
			fmt.Fprintf(os.Stderr, cliecho.Binary+": the configuration at %s names no source, but the journal at %s %s. Restore the sources, or mount the configuration this journal belongs to.\n", *cfgPath, cfg.State.Database, held)
			return exitFailure
		}
	}
	fmt.Printf("config OK: %d source(s), %d backup set(s)\n", len(cfg.Sources), sets)
	fmt.Printf("state database OK: %s\n", cfg.State.Database)
	printCheckWorkflowRoot(os.Stdout, cfg, workflowRootDefault())
	// Nothing at all on a deployment that is not serving a pre-rename
	// path, which is every deployment once the migration has been run:
	// a line saying "not adopted" would be a line every operator learns
	// to skip, on the surface where the adopted case most needs to be
	// noticed.
	for _, adoption := range preflight.Adoptions() {
		fmt.Printf("%s\n", adoption.Report())
	}
	return 0
}

// printCheckWorkflowRoot is check's line about the workflow root (#1246): the
// one the engine will run on, resolved exactly as the engine resolves it, so
// a deployment on the mounted default is not reported as having none.
// Silent when there is no root at all, for the reason at the end.
func printCheckWorkflowRoot(w io.Writer, cfg *config.Config, d service.WorkflowRootDefault) {
	r := service.ResolveWorkflowRoot(cfg, d)
	switch {
	case r.Disabled:
		sayln(w, "workflow root: none, workflows are turned off (workflows.disabled)")
	case r.Default:
		say(w, "workflow root: %s (the default: config.yaml names none and this directory is mounted)\n", r.Path)
	case r.Path != "":
		say(w, "workflow root: %s\n", r.Path)
	}
	// Nothing at all for a deployment with no root and nothing mounted,
	// which is every medium-free deployment FR-35 pins byte for byte
	// (core/tests/compat): check said nothing about workflows before, and
	// "none" is what it still means.
}

// workflowRootDefault is where this binary looks for the mounted default
// workflow root when config.yaml names none (#1246). The production answer,
// set here in main's package and nowhere below it; this package's TestMain
// replaces it with "look for none", so the suite never reads the /workflows
// of the machine it runs on.
var workflowRootDefault = service.ProductionWorkflowRootDefault
