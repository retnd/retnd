package main

import (
	"errors"
	"fmt"
	"io"
	"os"

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
	writable := service.ConfigDirWritable(path)

	if verb == "check" {
		fmt.Fprintf(out, "config: none yet at %s (fresh install; the engine writes a default configuration on its first start)\n", path)
		if writable != nil {
			fmt.Fprintf(os.Stderr, cliecho.Binary+": the configuration directory is not writable, so the engine cannot write its default configuration there: %v\n", writable)
			fmt.Fprintf(os.Stderr, "Next step: mount the configuration directory writable, then run check again.\n")
			return exitFailure
		}
		fmt.Fprintln(out, "config directory writable: yes")
	} else {
		fmt.Fprintf(out, cliecho.Binary+" is installed and not configured yet (first run, fresh install). Nothing is being backed up.\n")
		fmt.Fprintf(out, "The engine writes a default configuration to %s on its first start.\n", path)
		if writable != nil {
			fmt.Fprintf(out, "Note: that directory is not writable right now: %v\n", writable)
		}
	}
	fmt.Fprintf(out, "Next step: add the first backup set in the web interface, or with `%s backup-set create ...`.\n", cliecho.Binary)
	return exitOK
}
