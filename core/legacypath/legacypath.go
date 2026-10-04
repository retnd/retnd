package legacypath

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/retnd/retnd/core/internal/config"
)

// Package legacypath keeps the resolved-path result type shared by the
// engine and web host. The deployment-name compatibility window is closed,
// so current paths no longer have an alternate brand-derived location.

// Outcome is which cell of FR-38's table a resolved path landed
// in. It is a string so it can be read out of a log line, a `retnd check`
// run and an API response without a translation table in each.
type Outcome string

const (
	// UseNewPath: the resolved path is the one to use. Either it
	// holds the artifact, or neither spelling carries this product's
	// name so the rename never touched it.
	UseNewPath Outcome = "new-path"

	// AdoptLegacy: the resolved path holds nothing and the legacy
	// spelling holds the artifact, so the legacy path is what gets
	// served, with a warning on every start.
	AdoptLegacy Outcome = "adopted-legacy-path"

	// FreshInstall: neither path holds the artifact. This is the
	// one outcome that may proceed to a first run, and it is the only
	// one — which is the forbidden cell stated as code.
	FreshInstall Outcome = "fresh-install"

	// Ambiguous: both paths hold an artifact and they are
	// different directories. Choosing one silently is the worst option
	// available, so nothing chooses.
	Ambiguous Outcome = "ambiguous"
)

// Adoption is one resolved location and what the preflight decided about
// it. Both halves of FR-38 — the state database and the configuration
// directory — produce one of these, separately, because the two are
// resolved by different code and the configuration directory also holds
// the SSH key store and known_hosts.d/.
type Adoption struct {
	// What names the thing in operator language ("state database",
	// "configuration"), because it appears in a warning somebody reads
	// once, during an incident.
	What string

	// Renamed is the path as this release resolves it: the flag, the
	// environment variable or the compiled default, after
	// config.ResolvePath.
	Renamed string

	// Legacy is the pre-rename spelling of Renamed, or "" when Renamed
	// carries no renamed segment and the rename therefore cannot have
	// moved it.
	Legacy string

	// Path is the location to actually use. Renamed in every outcome but
	// AdoptLegacy, where it is Legacy, and "" in Ambiguous
	// because that outcome has no answer by construction.
	Path string

	Outcome Outcome
}

// Adopted reports whether this deployment is being served from a
// pre-rename path. The surfaces that report every other resolved path
// report this one too (FR-38: `retnd check`, the deployment-check route
// and the startup log), so an operator can answer "which directory is
// live" months later rather than only in a warning they scrolled past.
func (a Adoption) Adopted() bool { return a.Outcome == AdoptLegacy }

// ForConfig runs the preflight over a configuration path: the
// --config flag's value, which may name either config.yaml or the
// directory holding it.
//
// Separate from ForStateDatabase on purpose, and asserted separately by
// core/tests/compat, because the two locations are resolved by completely
// different code: this one goes through config.ResolvePath and ends at
// the file service.OpenConfigAndJournal stats to decide whether this is a
// first run at all, while the state database is a persisted absolute path
// a configuration names.
func ForConfig(requested string) Adoption {
	renamed := config.ResolvePath(requested)
	legacy := legacyCounterpart(renamed)
	if legacy != "" {
		// ResolvePath again, because the legacy spelling may be the
		// directory shape: an operator whose --config named
		// /etc/retnd/config has a /etc/retnd/config directory, not a
		// /etc/retnd/config file.
		legacy = config.ResolvePath(legacy)
	}
	return classifyPaths("configuration", renamed, legacy)
}

// ForStateDatabase runs the preflight over a state database path: the
// --state-database flag, $STATE_DATABASE or the packaged default for a
// process that has no configuration yet, and the absolute path
// config.yaml's state.database names for one that has.
//
// It applies to both, deliberately. A deployment whose configuration
// already names its journal cannot lose it to the rename — but a
// half-completed migration that rewrote the configuration and did not
// move the mount produces exactly FR-38's second row, at the one path
// where losing it means losing the journal.
func ForStateDatabase(requested string) Adoption {
	return classifyPaths("state database", requested, legacyCounterpart(requested))
}

// legacyCounterpart returns no alternate location because the path rename's
// compatibility window is closed.
func legacyCounterpart(string) string {
	return ""
}

// classifyPaths is FR-38's table, and it is the whole decision. It takes
// two paths and two stat results and returns a cell; it reads no flag, no
// environment variable and no configuration, which is what makes the
// forbidden cell unreachable rather than merely unreached.
func classifyPaths(what, renamed, legacy string) Adoption {
	a := Adoption{What: what, Renamed: renamed, Legacy: legacy, Path: renamed}

	if legacy == "" || legacy == renamed {
		// Nothing was renamed here, so there is no second location and
		// no question to ask. A deployment at /data/state is in this
		// row, which is most of them.
		a.Legacy = ""
		a.Outcome = UseNewPath
		return a
	}

	renamedHolds := holdsArtifact(renamed)
	legacyHolds := holdsArtifact(legacy)

	switch {
	case renamedHolds && legacyHolds && !sameDirectory(renamed, legacy):
		// Two journals are visible and they are not the same one.
		a.Path = ""
		a.Outcome = Ambiguous
	case renamedHolds:
		// Either the legacy path holds nothing, or it is the very same
		// directory reached by its other name — the installer's
		// rollback window, which is not ambiguity.
		a.Outcome = UseNewPath
	case legacyHolds:
		a.Path = legacy
		a.Outcome = AdoptLegacy
	default:
		a.Outcome = FreshInstall
	}
	return a
}

// holdsArtifact answers "does this location hold the thing", which is the
// only question FR-38's table asks of either path.
//
// A regular file with bytes in it. Size matters: a zero-length state.db
// left by a start that died between create and first write is a valid
// empty SQLite database, and treating it as "the new path holds a state
// database" is the forbidden cell reached through a crash rather than
// through a flag. The same is true of a zero-length config.yaml, which
// config.Load would refuse anyway.
//
// The table's other column reads "absent or empty", and this is its
// complement: an absent directory, an empty one, and one holding
// unrelated files are the same answer, because none of them is state this
// deployment can serve.
func holdsArtifact(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Size() > 0
}

// sameDirectory is FR-38's same-device-and-inode test, applied to the
// DIRECTORIES holding the two artifacts rather than to the artifacts
// themselves.
//
// The directory is the thing an operator mounts and the thing the
// installer's rollback-window override binds twice, and a bind mount of
// one host directory at two container paths gives the two paths one
// device and one inode. Comparing the files would answer the same
// question here and stop answering it the moment either path is a symlink
// into the other's directory.
//
// os.SameFile, not a hand-rolled Stat_t comparison: it is what
// core/internal/lifecycle's commit path already uses for exactly this
// question, and it is the portable spelling of it.
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

// AmbiguousError is the refusal FR-38's fourth row produces, carried
// as an error so it travels the same way every other startup refusal does
// and so a caller can tell "this deployment is ambiguous" from "this
// deployment is broken".
type AmbiguousError struct{ Adoption Adoption }

func (e *AmbiguousError) Error() string { return e.Adoption.Refusal() }

// Preflight is FR-38 applied to one deployment: both halves, in the order
// the process resolves them.
//
// Two fields and not one, because the two are asserted separately and for
// a reason that outlives this rename: the configuration directory is
// resolved by config.ResolvePath and also holds the SSH key store and
// known_hosts.d/, while the state database is a persisted absolute path a
// configuration names. A deployment can have adopted one and not the
// other, and a single "adopted: yes" would be unable to say which.
type Preflight struct {
	Config Adoption
	State  Adoption
}

// Adoptions is the subset that are really adoptions, which is what every
// reporting surface wants: nothing at all on a normal deployment.
func (p Preflight) Adoptions() []Adoption {
	var out []Adoption
	if p.Config.Adopted() {
		out = append(out, p.Config)
	}
	if p.State.Adopted() {
		out = append(out, p.State)
	}
	return out
}

// Run resolves both of a deployment's locations and decides
// FR-38's table for each, and it is what the REPORTING surfaces call:
// the startup log before it announces anything, `retnd check`, and the
// deployment-check route.
//
// OpenConfigAndJournal applies the same decision itself rather than
// taking it from here, and that duplication is deliberate. This function
// is a pure function of two paths and the filesystem, so both arrive at
// the same answer; and applying it inside the one door every caller opens
// a deployment through is what makes the forbidden cell unreachable for
// callers that never think to ask — `retnd check` and every provider app
// included. A preflight only the well-behaved caller runs is not a
// preflight.
//
// stateDatabase is the flag/environment/packaged default, used only when
// the configuration cannot name one: once a configuration exists it names
// its own journal, and that persisted absolute path is the one FR-38 has
// to be asked about.
func Run(configPath, stateDatabase string) (Preflight, error) {
	p := Preflight{Config: ForConfig(configPath)}
	if p.Config.Outcome == Ambiguous {
		return p, &AmbiguousError{Adoption: p.Config}
	}

	// The journal a configuration names wins over the default, because
	// it is the one this deployment actually has. A configuration that
	// cannot be read at all is not this function's failure to report:
	// Open says so, in its own words, a moment later. What matters here
	// is that a deployment whose configuration is unreadable still gets
	// the default asked about rather than nothing.
	candidate := stateDatabase
	if cfg, err := config.Load(p.Config.Path); err == nil && cfg.State.Database != "" {
		candidate = cfg.State.Database
	}

	p.State = ForStateDatabase(candidate)
	if p.State.Outcome == Ambiguous {
		return p, &AmbiguousError{Adoption: p.State}
	}
	return p, nil
}

// Warning is the line a start prints when it has adopted a legacy path,
// on EVERY start rather than only the first.
//
// It carries the two things an operator needs and nothing else: the
// compose line to change, named by the container path on its right-hand
// side because that is how they will find it in their own file, and the
// installer command that does the whole move — mount, persisted
// configuration and systemd units — in one step. A warning that says only
// "this is deprecated" is a warning that costs an operator an
// investigation.
//
// Its wording is pinned by core/tests/compat's cell 20, because an
// operator meets it once, in an incident, and reads whatever it says
// today.
func (a Adoption) Warning() string {
	if !a.Adopted() {
		return ""
	}
	return fmt.Sprintf(
		"serving this deployment's %s from %s, the pre-rename path: nothing is at %s and %s holds it. "+
			"This is a one-release compatibility adoption (EPIC R, FR-38), not the supported layout. "+
			"To move it: change the compose volume line whose container side is %s so that it reads %s, "+
			"then run `python3 scripts/install/install_docker_host.py migrate-identity --prefix <your install prefix>`, "+
			"which moves the mount, rewrites the persisted paths in config.yaml and renames the systemd units in one step.",
		a.What, a.Path, a.Renamed, a.Path,
		filepath.Dir(a.Legacy), filepath.Dir(a.Renamed),
	)
}

// Refusal is the line a start prints instead of starting, when both
// locations hold an artifact and they are two different directories.
//
// It names both paths, because the operator has to decide which one this
// deployment means and cannot do that from a message naming one. It does
// not guess, and it does not offer a flag to make it guess: a flag that
// picks one of two journals is the forbidden cell with a switch on it.
//
// Pinned by core/tests/compat's cell 22.
func (a Adoption) Refusal() string {
	if a.Outcome != Ambiguous {
		return ""
	}
	return fmt.Sprintf(
		"refusing to start: two different %s directories are populated. %s holds one and %s holds a different one "+
			"(different device and inode), so which of the two this deployment means is not something this process can observe. "+
			"EPIC R renamed %s to %s; adoption is for one populated pre-rename path and refusal is reserved for two. "+
			"Keep the one this deployment should serve, move the other aside, and start again.",
		a.What, a.Renamed, a.Legacy,
		filepath.Dir(a.Legacy), filepath.Dir(a.Renamed),
	)
}

// Report is the one-line form the surfaces that report every other
// resolved path use: `retnd check`'s output, the deployment-check route's
// response and the startup log. Empty for a deployment that is not
// serving a pre-rename path, so a normal deployment prints nothing extra.
func (a Adoption) Report() string {
	if !a.Adopted() {
		return ""
	}
	return fmt.Sprintf("%s adopted from the pre-rename path: %s (renamed path %s is empty)", a.What, a.Path, a.Renamed)
}
