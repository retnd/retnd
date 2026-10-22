package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/legacypath"
	"github.com/retnd/retnd/core/service"
)

// configAbsentAt reports whether no configuration exists at the path the
// command was given, resolved exactly as service.Open resolves it, and
// returns that resolved path. An ambiguous legacy location is not
// "absent": it is a refusal the ordinary load reports.
func configAbsentAt(cfgPath string) (string, bool) {
	adoption := legacypath.ForConfig(cfgPath)
	if adoption.Outcome == legacypath.Ambiguous {
		return "", false
	}
	path := config.ResolvePath(adoption.Path)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path, true
	}
	return path, false
}

// printFirstRunNotice is what `status` and `check` say on a fresh install
// (retnd#1049): the deployment is installed and has nothing configured
// yet, which is a state and not a failure, so the command names the next
// step and exits 0. For `check` the one thing that can still be wrong is
// the configuration directory the engine will write its default into; an
// unwritable one is named and exits 1.
func printFirstRunNotice(out io.Writer, cfgPath, verb string) int {
	path, _ := configAbsentAt(cfgPath)
	if path == "" {
		path = config.ResolvePath(cfgPath)
	}

	// No configuration is a fresh install only when the journal agrees. A
	// journal that holds backups next to a missing configuration is a lost
	// or unmounted configuration volume, and it must read as the failure it
	// is: `status` is the image's HEALTHCHECK, and a green exit here would
	// report a deployment with no configuration as healthy (panel finding
	// C7 on retnd#1049).
	if err := service.CheckJournalIsFresh(context.Background(), service.StateDatabaseDefault()); err != nil {
		fmt.Fprintf(os.Stderr, cliecho.Binary+": %v\n", err)
		fmt.Fprintf(os.Stderr, "No configuration was found at %s. Restore the configuration volume, or mount the one this journal belongs to; nothing was written.\n", path)
		return exitFailure
	}
	writable := service.ConfigDirWritable(path)

	if verb == "check" {
		fmt.Fprintf(out, "config: none yet at %s (fresh install; the engine writes a default configuration on its first start)\n", path)
		if writable != nil {
			// The same sentence the web interface's first-run guide shows
			// (service.ConfigDirUnwritableMessage), so the two agree.
			fmt.Fprintf(os.Stderr, cliecho.Binary+": %s\n", service.ConfigDirUnwritableMessage(filepath.Dir(path)))
			fmt.Fprintf(os.Stderr, "Reason: %v\nRun check again once it is fixed.\n", writable)
			return exitFailure
		}
		_, _ = fmt.Fprintln(out, "config directory writable: yes")
	} else {
		fmt.Fprintf(out, cliecho.Binary+" is installed and not configured yet (first run, fresh install). Nothing is being backed up.\n")
		fmt.Fprintf(out, "The engine writes a default configuration to %s on its first start.\n", path)
		if writable != nil {
			fmt.Fprintf(out, "Note: %s (%v)\n", service.ConfigDirUnwritableMessage(filepath.Dir(path)), writable)
		}
	}
	fmt.Fprintf(out, "Next step: add the first backup set in the web interface, or with `%s backup-set create ...`.\n", cliecho.Binary)
	if verb == "status" {
		printEngineFirstRunSteps(out)
	}
	return exitOK
}

// printEngineFirstRunSteps adds the guided setup's step states to a fresh
// install's notice, when this host has been told where the engine is (#1152).
//
// The steps are the engine's to report: whether an administrator exists and
// whether a mail server is set up live in the web host, which this process
// does not contain. So they are asked for and attributed, "as reported by the
// engine at ...", rather than presented as a fact status found out itself.
//
// With no route to an engine this prints nothing, so a fresh install that was
// never told about one reads exactly as it did. A route that is set and does
// not answer says so on stderr and leaves the notice alone: an unreachable
// engine is not a reason for the one command an operator runs on a fresh
// install to fail.
func printEngineFirstRunSteps(out io.Writer) {
	if _, base := routeAddress(); base == "" {
		return
	}
	client, err := dialEngine()
	if err == nil {
		var got apicontract.FirstRunStatusResponse
		if got, err = client.FirstRunStatus(context.Background()); err == nil {
			_, base := routeAddress()
			fmt.Fprintf(out, "first-run steps (as reported by the engine at %s):\n", base)
			fmt.Fprintf(out, "  administrator: %s\n  email: %s\n  backup_set: %s\n", got.Steps.Administrator, got.Steps.Email, got.Steps.BackupSet)
			fmt.Fprintf(out, "  next step: %s\n", got.NextStep)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "note: could not read the first-run steps from the engine this host was told about: %v\n", err)
}
