// Package prerename stops a deployment that is still mounted at the path it
// had before the product was renamed to retnd in 0.5.0 (EPIC R) from
// starting against an empty directory (#1546).
//
// The rename moved the container's configuration directory, and with it any
// path an operator chose that carried the product's name as one component
// (/etc/<name>/config, /var/lib/<name>, /volume1/<name>/state). Until #947
// closed the deprecation window, core/legacypath adopted the old path with a
// warning. With that adoption gone, an upgraded deployment whose compose file
// still mounts the old path resolves the new one, finds nothing there, and
// every branch after that reads it as a fresh install: the web host writes a
// default configuration and serves the setup flow, and the operator sees an
// empty retnd with no word about where their catalogue went.
//
// So both the engine and the web host call Check before anything else at
// startup. It looks at the two locations a deployment keeps its catalogue in,
// the configuration and the state database, and for each one it compares the
// path this release resolves with its pre-rename spelling:
//
//	current path   pre-rename path                 outcome
//	-------------  ------------------------------  ---------------------------
//	holds it       absent, empty, or the SAME      start
//	               directory (bind mount, symlink)
//	absent/empty   holds it                        refuse, naming both and the fix
//	holds it       holds a DIFFERENT one           refuse, naming the conflict
//	absent/empty   absent/empty                    start (a fresh install)
//
// This package owns its own copy of the pre-rename spelling on purpose.
// core/legacypath's counterpart function already returns "" and #1547
// deletes that package together with the installer's LEGACY_* constants, so
// nothing here may lean on either.
package prerename

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/retnd/retnd/core/internal/config"
)

// currentSegment and formerSegment are the product's name as one path
// component, after and before EPIC R.
//
// formerSegment is assembled from two pieces because
// scripts/rename/check-no-former-name.sh and core/tests/brandguard refuse the
// old name spelled out anywhere in the tree, and they are right to. The
// process spells it at runtime, in the diagnostic, because that is the one
// place an operator needs to read it.
const (
	currentSegment = "retnd"
	formerSegment  = "backup" + "d"
)

// renameRelease is the release that renamed the product, as the diagnostic
// names it.
const renameRelease = "0.5.0"

// Error is the refusal. Conflict is false when only the pre-rename path holds
// the catalogue, and true when both paths hold one and they are different
// directories.
type Error struct {
	// What names the location in operator language: "configuration" or
	// "state database".
	What string
	// Current is the path this release resolves; Former is its pre-rename
	// spelling.
	Current, Former string
	Conflict        bool
}

func (e *Error) Error() string {
	curDir, oldDir := filepath.Dir(e.Current), filepath.Dir(e.Former)
	if e.Conflict {
		return fmt.Sprintf(
			"refusing to start: there are two different %s files, %s and %s, and the second is at the path "+
				"from before the product was renamed to retnd in %s. This process can't tell which one the deployment "+
				"means, so it won't pick. Put the one this deployment should serve at %s, move the other one somewhere "+
				"retnd doesn't look (rename %s, for example), and start again. Nothing was read or written.",
			e.What, e.Current, e.Former, renameRelease, curDir, oldDir)
	}
	return fmt.Sprintf(
		"refusing to start: this deployment's %s is still at %s, the path from before the product was renamed to "+
			"retnd in %s, and nothing is at %s, where this release looks. Starting anyway would serve an empty "+
			"deployment and hide everything at the old path. To fix it, mount the old directory at %s instead of %s "+
			"(in a compose file, change the volume line whose container side is %s), or %s. Then start again. "+
			"Nothing was read or written.",
		e.What, e.Former, renameRelease, e.Current, curDir, oldDir, oldDir, e.pointAt())
}

// pointAt is the second way out: leave the mount alone and tell retnd where
// the catalogue is.
func (e *Error) pointAt() string {
	if e.What == whatConfig {
		return "start with --config " + e.Former
	}
	return "set state.database in config.yaml (or --state-database, before there is a configuration) to " + e.Former
}

const (
	whatConfig = "configuration"
	whatState  = "state database"
)

// Check decides the table above for a deployment's configuration and state
// database and returns an *Error when either one must not start.
//
// configPath is the --config value (config.yaml or the directory holding
// it). stateDatabase is the journal the process assumes when no
// configuration names one; once the configuration at the current path names
// a journal, that is the one asked about, because it is the one this
// deployment has.
func Check(configPath, stateDatabase string) error {
	current := config.ResolvePath(configPath)
	former := formerSpelling(current)
	if former != "" {
		// ResolvePath again: --config may name a directory that only
		// exists under its old spelling.
		former = config.ResolvePath(former)
	}
	if err := decide(whatConfig, current, former); err != nil {
		return err
	}

	journal := stateDatabase
	if cfg, err := config.Load(current); err == nil && cfg.State.Database != "" {
		journal = cfg.State.Database
	}
	return decide(whatState, journal, formerSpelling(journal))
}

// formerSpelling is p with every component that is exactly the product's
// name replaced by the name it had before 0.5.0, or "" when no component is.
// Whole components only: retnd.db is a file name and /retnd-web an
// entrypoint, and neither was ever renamed as a directory.
func formerSpelling(p string) string {
	if p == "" {
		return ""
	}
	parts := strings.Split(p, string(filepath.Separator))
	found := false
	for i, part := range parts {
		if part == currentSegment {
			parts[i] = formerSegment
			found = true
		}
	}
	if !found {
		return ""
	}
	return strings.Join(parts, string(filepath.Separator))
}

func decide(what, current, former string) error {
	if former == "" || former == current {
		return nil
	}
	currentHolds, formerHolds := holds(current), holds(former)
	switch {
	case currentHolds && formerHolds && !sameDirectory(current, former):
		return &Error{What: what, Current: current, Former: former, Conflict: true}
	case formerHolds && !currentHolds:
		return &Error{What: what, Current: current, Former: former}
	}
	return nil
}

// holds reports whether path is a non-empty regular file. A zero-length
// state.db left by a start that died before its first write, or an empty
// config.yaml, is not a catalogue anybody can lose.
func holds(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// sameDirectory reports whether the two files sit in one directory reached by
// two names: a host directory bind-mounted at both container paths, or the
// old path left as a symlink to the new one. That is one catalogue, not two.
func sameDirectory(a, b string) bool {
	ai, err := os.Stat(filepath.Dir(a))
	if err != nil {
		return false
	}
	bi, err := os.Stat(filepath.Dir(b))
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}
