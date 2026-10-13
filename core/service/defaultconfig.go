package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/legacypath"
)

// This file is the writer of a fresh install's default configuration
// (retnd#1049). The invariant it exists for: a user must never meet an
// error on first install just because something has not been configured
// yet. A deployment that starts with an empty configuration directory
// gets a small, valid config.yaml from the engine itself, so from its
// first second it is a configured deployment with nothing to do yet,
// and "not configured" stops being a state the API, the CLI or the log
// have to refuse.
//
// It is the engine and not the installer that writes it. Only one
// install path runs an installer; app-store installs start from the same
// empty directory, and two writers of a first configuration is the shape
// #571 came from.

// DefaultConfigHeader is the comment the default file opens with. It names
// the writer and says what the file is, because an older binary refuses
// the file this release writes (it has no backup set in it), and the
// person who finds that out should be able to read why in the file.
const DefaultConfigHeader = "# Written by retnd on first start. Edit it in the web UI or with the\n" +
	"# retnd CLI; both rewrite this file. It declares no backup sets yet, which\n" +
	"# this release treats as valid and idle. To roll back to a release that\n" +
	"# refuses an empty configuration, add a backup set first or delete this file.\n"

// ErrDefaultConfigSkipped is what EnsureDefaultConfig returns, wrapped, when
// it deliberately wrote nothing and the caller should carry on as before.
// It is not a failure; it is the answer for a path this release must not
// decide on its own (an ambiguous legacy location).
var ErrDefaultConfigSkipped = errors.New("service: default configuration not written")

// DefaultConfigYAML returns the bytes of the default configuration for
// defaults: the documented poll interval, the journal path the process was
// started with, and no sources. Retention, alerts, capacity and storage
// mediums are omitted on purpose, for the reason CreateInitialConfig
// gives: config.Validate resolves them to the documented defaults at load,
// and writing today's numbers would freeze them across upgrades.
func DefaultConfigYAML(defaults FirstRunDefaults) ([]byte, error) {
	if defaults.StateDatabase == "" || !filepath.IsAbs(defaults.StateDatabase) {
		return nil, fmt.Errorf("service: the default configuration needs an absolute state database path, got %q", defaults.StateDatabase)
	}
	poll := defaults.PollInterval
	if poll <= 0 {
		poll = DefaultFirstRunPollInterval
	}
	// Three keys, written by hand rather than by marshalling a Config: the
	// marshalled form also spells out empty retention and alerts blocks,
	// and anything written here is frozen into the file across upgrades.
	// The database path goes through the YAML encoder so an unusual path
	// is quoted correctly.
	db, err := yaml.Marshal(defaults.StateDatabase)
	if err != nil {
		return nil, fmt.Errorf("service: encoding the default configuration: %w", err)
	}
	pollText := poll.String()
	if poll%time.Hour == 0 {
		pollText = fmt.Sprintf("%dh", int64(poll/time.Hour))
	}
	body := []byte("poll_interval: " + pollText + "\nstate:\n  database: " + strings.TrimSpace(string(db)) + "\nsources: []\n")
	// A configuration that this build would refuse is never written, so
	// the file can only ever be one the next start accepts.
	probe := &config.Config{}
	if err := yaml.Unmarshal(body, probe); err != nil {
		return nil, fmt.Errorf("service: the default configuration does not round-trip: %w", err)
	}
	if err := probe.Validate(); err != nil {
		return nil, fmt.Errorf("service: the default configuration does not validate: %w", err)
	}
	return append([]byte(DefaultConfigHeader), body...), nil
}

// EnsureDefaultConfig writes the default configuration when, and only when,
// no configuration exists at defaults.ConfigPath. It reports whether it
// wrote one and the resolved path it looked at.
//
// The file is created exclusively (writeConfigExclusively: a temp file in
// the same directory, fsynced, then link(2) into place) with mode 0600, so
// it never overwrites anything, a reader never sees a partial file, and a
// second writer racing this one is ErrAlreadyConfigured, which is reported
// as "nothing written" and not as a failure. It is idempotent by
// construction: once the file exists the function returns before writing.
//
// A configuration that is missing next to a journal that holds backups is
// not a fresh install: nothing is written and the error wraps
// ErrConfigLostOverJournal (see CheckJournalIsFresh).
//
// A directory that does not exist yet is created (mode 0755), because a
// fresh install may mount only its parent. A directory that cannot be
// written is an error the caller reports (a WARN naming it) and carries on
// from: the process still serves the first-run setup flow, so nothing
// regresses.
func EnsureDefaultConfig(defaults FirstRunDefaults) (wrote bool, path string, err error) {
	path = config.ResolvePath(defaults.ConfigPath)

	// The same preflight Open runs, before the same stat: a configuration
	// that exists under a path this release adopts, or that is ambiguous,
	// must not be answered with a second, fresh one.
	adoption := legacypath.ForConfig(defaults.ConfigPath)
	switch adoption.Outcome {
	case legacypath.Ambiguous, legacypath.AdoptLegacy:
		return false, path, fmt.Errorf("%w: %s resolves through the legacy path preflight", ErrDefaultConfigSkipped, path)
	}

	if _, statErr := os.Stat(path); statErr == nil {
		return false, path, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, path, fmt.Errorf("service: looking for a configuration at %s: %w", path, statErr)
	}

	// A journal with history in it means the configuration was lost, not
	// that nothing was ever configured (retnd#1049, panel finding C7).
	// Writing a default over it would turn a missing volume into a healthy
	// looking, empty deployment pointing at the old state, so nothing is
	// written and the caller gets an error it has to take seriously. This
	// runs before the state directory is touched, because the check is
	// read-only and the directory probe is not.
	if defaults.StateDatabase != "" {
		if err := CheckJournalIsFresh(context.Background(), defaults.StateDatabase); err != nil {
			return false, path, err
		}
	}

	// A state directory this process cannot use is the first-run setup
	// flow's problem, which it already reports and refuses to complete
	// through. Writing a configuration naming a journal that cannot open
	// would turn that served, diagnosable state into a start that exits,
	// so nothing is written and the caller carries on as before.
	if err := validateStateDir(defaults.StateDatabase); err != nil {
		return false, path, fmt.Errorf("%w: %v", ErrDefaultConfigSkipped, err)
	}

	body, err := DefaultConfigYAML(defaults)
	if err != nil {
		return false, path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, path, fmt.Errorf("service: the configuration directory %s cannot be created: %w", filepath.Dir(path), err)
	}
	if err := writeConfigExclusively(path, body); err != nil {
		if errors.Is(err, ErrAlreadyConfigured) {
			return false, path, nil
		}
		return false, path, err
	}
	return true, path, nil
}

// ConfigDirWritable reports whether a configuration could be created next
// to path. It is what `retnd check` asks on a fresh install: the engine
// writes its default there on first start, so an unwritable directory is
// the one real thing to fix, and it is named. It creates and removes one
// temp file, the same primitive writeConfigExclusively starts with.
func ConfigDirWritable(configPath string) error {
	dir := filepath.Dir(config.ResolvePath(configPath))
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A directory that will be created on first start is fine as
			// long as its nearest existing parent can be written to.
			parent := filepath.Dir(dir)
			if parent == dir {
				return err
			}
			return ConfigDirWritable(filepath.Join(parent, "config.yaml"))
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	tmp, err := os.CreateTemp(dir, ".config-probe-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_ = tmp.Close()
	return os.Remove(name)
}

// ConfigDirUnwritableMessage is the one sentence an operator is given when
// the engine has no configuration and cannot write its default one because
// dir is not writable (retnd#1049). It is a function of the directory alone
// so the command line (`retnd check`, `retnd status`) and the web
// interface's first-run guide say exactly the same thing: which directory,
// and what to do about it.
func ConfigDirUnwritableMessage(dir string) string {
	return fmt.Sprintf("The configuration directory %s is not writable, so retnd cannot write its default configuration there. "+
		"Mount that directory writable for the user retnd runs as, then restart retnd.", dir)
}

// DefaultSettings returns the settings an engine runs with when nobody has
// changed anything: the documented defaults, resolved the way a load
// resolves them. It is what a deployment still waiting for its
// configuration file answers GET /settings with (retnd#1049), so the
// Settings page shows the real defaults rather than refusing.
func DefaultSettings() Settings {
	cfg := &config.Config{
		PollInterval: config.Duration(DefaultFirstRunPollInterval),
		State:        config.State{Database: "/data/state/state.db"},
		Sources:      []config.Source{},
	}
	// Validate resolves retention, alerts and capacity to their defaults
	// in place; the config here is built by this function and valid.
	_ = cfg.Validate()
	return Settings{
		Retention: toRetentionSettings(cfg.Retention),
		Capacity:  toCapacitySettings(cfg),
		Service:   toServiceSettings(cfg),
		Mediums:   toStorageMediumSummaries(cfg),
	}
}

// IsDefaultSettings reports whether s is exactly what DefaultSettings
// answers: nobody has changed retention, capacity or the polling interval,
// and no destination beyond the implicit local one is declared. Combined
// with "no backup set exists" it is what first_run means on the wire.
func IsDefaultSettings(s Settings) bool {
	d := DefaultSettings()
	return reflect.DeepEqual(s.Retention, d.Retention) &&
		reflect.DeepEqual(s.Capacity, d.Capacity) &&
		s.Service == d.Service &&
		len(s.Mediums) <= 1
}
