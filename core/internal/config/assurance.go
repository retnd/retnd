package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/workflow"
)

// Assurance configuration (EPIC Q, Q1.4 / FR-73): the global default and
// the per-set override for scrubbing and recovery testing.

// Recovery-test modes. The global block takes off or required; a backup set
// may also say inherit (the default) or custom.
const (
	RecoveryTestInherit  = "inherit"
	RecoveryTestOff      = "off"
	RecoveryTestRequired = "required"
	RecoveryTestCustom   = "custom"

	// RecoveryTestBuiltin schedules the product's own structure check (#1111):
	// no validator, and never what required means.
	RecoveryTestBuiltin = "builtin"
)

// builtin_check values (#1111): whether the built-in structure check may run
// when no validator is declared.
const (
	BuiltinCheckStructure = "structure"
	BuiltinCheckOff       = "off"
)

// validator_env values (#1221): what environment a recovery-test validator
// starts with. minimal is the RECOVERY_TEST_* variables, the product's own
// RETND_* facts and the sanitized baseline; workflow adds the set's merged
// workflow environment, secrets included.
const (
	ValidatorEnvMinimal  = "minimal"
	ValidatorEnvWorkflow = "workflow"
)

// ReservedValidatorPrefix begins every id the product itself uses for one of
// its built-in checks. An operator's validator_id may not start with it, so
// a stored verdict under such an id can only have come from the product.
const ReservedValidatorPrefix = "retnd-"

// IsReservedValidatorID is the one place the reserved-prefix rule lives
// (recoverytest.IsBuiltin answers through it).
func IsReservedValidatorID(id string) bool { return strings.HasPrefix(id, ReservedValidatorPrefix) }

// Scrub strategies.
const (
	ScrubStrategyAuto      = "auto"
	ScrubStrategyExistence = "existence"
	ScrubStrategyContent   = "content"
)

// Defaults and floors. Named constants so a test can drive one over its line.
const (
	DefaultScrubCadence        = 7 * 24 * time.Hour
	MinScrubCadence            = time.Hour
	DefaultRecoveryTestCadence = 30 * 24 * time.Hour
	MinRecoveryTestCadence     = 24 * time.Hour
	DefaultStagingTTL          = 24 * time.Hour

	// DefaultStagingDirName is the directory beside the state database that
	// holds recovered copies when staging_root is not set.
	DefaultStagingDirName     = "recovery-staging"
	DefaultMaxConcurrentReads = 2
	DefaultMaxPerDestination  = 1
)

// AssuranceScrub is the scrub half of the policy.
type AssuranceScrub struct {
	Cadence  Duration `yaml:"cadence,omitempty"`
	Strategy string   `yaml:"strategy,omitempty"`
}

// AssuranceRecoveryTest is the recovery-test half of the policy.
//
// ValidatorID selects the script a recovery test runs, by FILE NAME: NAME in
// NAME.local.sh, inside recovery-test/<source>/<set> under the workflow root.
// It is not an entry in FR-13's catalog of ingest validators (a different
// contract, materialised from embedded copies so a caller cannot choose
// code). RecoveryTestScriptName is the one place an id becomes a file name.
type AssuranceRecoveryTest struct {
	Mode        string   `yaml:"mode,omitempty"`
	Cadence     Duration `yaml:"cadence,omitempty"`
	ValidatorID string   `yaml:"validator_id,omitempty"`

	// BuiltinCheck is structure or off (#1111); empty is structure globally
	// and inherits per set. It governs the manual test with every mode.
	BuiltinCheck string `yaml:"builtin_check,omitempty"`

	// ValidatorEnv is minimal or workflow (#1221); empty is minimal globally
	// and inherits per set. workflow is the operator's opt-in to hand the
	// validator the set's workflow variables, secrets included.
	ValidatorEnv string `yaml:"validator_env,omitempty"`
}

// RecoveryTestScriptName turns a recovery-test validator_id into the file
// name of the script it selects, and refuses an id that could never be one.
//
// The id is checked as the workflow runner will check the file, so the
// refusal happens when the setting is saved rather than the first time a test
// is due: a leading dash or dot, whitespace, control or non-ASCII characters
// and a path are all refused. A caller that passes the whole file name gets
// told to drop the suffix, because NAME.local.sh.local.sh would be accepted
// by the name rule and never exist.
func RecoveryTestScriptName(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("a validator id must not be empty")
	}
	if err := validValidatorID(id); err != nil {
		return "", err
	}
	if IsReservedValidatorID(id) {
		return "", fmt.Errorf("%q is not usable: ids beginning %s are the product's own built-in checks", id, ReservedValidatorPrefix)
	}
	if strings.HasSuffix(id, ".sh") {
		return "", fmt.Errorf("%q is a file name; name the script without its .local.sh suffix", id)
	}
	name := id + ".local.sh"
	if _, err := workflow.ParseScriptName(name); err != nil {
		return "", fmt.Errorf("%q cannot be the name of a recovery-test script: %w", id, err)
	}
	return name, nil
}

// AssuranceBlock is the deployment-wide `assurance:` block. Nil on Config
// means "write nothing", which resolves to the defaults.
type AssuranceBlock struct {
	Scrub        AssuranceScrub        `yaml:"scrub,omitempty"`
	RecoveryTest AssuranceRecoveryTest `yaml:"recovery_test,omitempty"`

	// Host-scoped knobs: global only, and the per-set answer is no.
	StagingRoot            string   `yaml:"staging_root,omitempty"`
	StagingTTL             Duration `yaml:"staging_ttl,omitempty"`
	MaxConcurrentFullReads int      `yaml:"max_concurrent_full_reads,omitempty"`
	MaxPerDestination      int      `yaml:"max_per_destination,omitempty"`
}

// AssuranceOverride is a backup set's own `assurance:` block.
type AssuranceOverride struct {
	Scrub        AssuranceScrub        `yaml:"scrub,omitempty"`
	RecoveryTest AssuranceRecoveryTest `yaml:"recovery_test,omitempty"`
}

// Assurance is the fully resolved policy one backup set decides under.
type Assurance struct {
	Scrub        AssuranceScrub
	RecoveryTest AssuranceRecoveryTest
}

// AssuranceDefaults is the resolved deployment-wide policy, host knobs included.
type AssuranceDefaults struct {
	Assurance
	StagingRoot            string
	StagingTTL             time.Duration
	MaxConcurrentFullReads int
	MaxPerDestination      int

	// MaxConcurrentFullReadsWritten and MaxPerDestinationWritten say the
	// operator put a number on that knob, as against the product's default
	// filling it in. The two read the same after resolution when the operator
	// wrote exactly the default, and a host that declares a limit of its own
	// has to tell them apart: it yields to a number somebody wrote and not to
	// a default nobody did. They exist so a consumer never has to read the raw
	// AssuranceBlock to find that out, which is what the guard in
	// TestAssurance_NoConsumerReadsThePointers is there to stop.
	MaxConcurrentFullReadsWritten bool
	MaxPerDestinationWritten      bool
}

// AssuranceIsOverride reports whether this set declares its own policy.
func (b BackupSet) AssuranceIsOverride() bool { return b.AssuranceConfig != nil }

// ScrubStrategies and RecoveryTestModes spell the closed vocabularies in a
// fixed order, for messages and for readers that render a chooser.
var (
	ScrubStrategies     = []string{ScrubStrategyAuto, ScrubStrategyExistence, ScrubStrategyContent}
	GlobalRecoveryModes = []string{RecoveryTestOff, RecoveryTestBuiltin, RecoveryTestRequired}
	SetRecoveryModes    = []string{RecoveryTestInherit, RecoveryTestOff, RecoveryTestBuiltin, RecoveryTestRequired, RecoveryTestCustom}
	BuiltinChecks       = []string{BuiltinCheckStructure, BuiltinCheckOff}
)

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// resolveAssurance is phase 2 of Validate for the assurance policy: it
// resolves the deployment-wide block into c.Assurance without touching the
// operator's own block (so a re-marshal stays byte-identical), then every
// backup set's policy from its override and that resolved global.
//
// Any mutation of c.AssuranceBlock has to be followed by Validate. A
// consumer that patched the global and skipped it would leave every
// inheriting set deciding under the old policy, which is how the retention
// override flags were once a silent no-op.
func (c *Config) resolveAssurance(v *validator) {
	global := c.resolveGlobalAssurance(v)
	c.Assurance = global
	for i := range c.Sources {
		for j := range c.Sources[i].BackupSets {
			bs := &c.Sources[i].BackupSets[j]
			path := fmt.Sprintf("sources[%d].backup_sets[%d]", i, j)
			bs.Assurance = resolveSetAssurance(v, path, bs.AssuranceConfig, global.Assurance)
		}
	}
}

// ResolveAssurance runs the assurance phase on its own, for a caller that
// builds a Config by hand, the way ResolveBackupSetRetention does.
func (c *Config) ResolveAssurance() error {
	v := &validator{}
	c.resolveAssurance(v)
	return v.err()
}

func checkCadence(v *validator, path string, d Duration, floor time.Duration) {
	if d.Duration() < floor {
		v.addf("%s: %s is below the floor of %s; a cadence that short would re-read the same bytes continuously", path, d, floor)
	}
}

func (c *Config) resolveGlobalAssurance(v *validator) AssuranceDefaults {
	var b AssuranceBlock
	if c.AssuranceBlock != nil {
		b = *c.AssuranceBlock
	}
	const p = "assurance"
	var out AssuranceDefaults

	out.Scrub.Cadence = b.Scrub.Cadence
	if out.Scrub.Cadence == 0 {
		out.Scrub.Cadence = Duration(DefaultScrubCadence)
	}
	checkCadence(v, p+".scrub.cadence", out.Scrub.Cadence, MinScrubCadence)
	out.Scrub.Strategy = b.Scrub.Strategy
	if out.Scrub.Strategy == "" {
		out.Scrub.Strategy = ScrubStrategyAuto
	}
	if !containsString(ScrubStrategies, out.Scrub.Strategy) {
		v.addf("%s.scrub.strategy: %q is not one of %v", p, out.Scrub.Strategy, ScrubStrategies)
	}

	rt := &out.RecoveryTest
	rt.Mode = b.RecoveryTest.Mode
	if rt.Mode == "" {
		rt.Mode = RecoveryTestOff
	}
	if !containsString(GlobalRecoveryModes, rt.Mode) {
		v.addf("%s.recovery_test.mode: %q is not one of %v (inherit and custom are per-set spellings)", p, rt.Mode, GlobalRecoveryModes)
	}
	rt.Cadence = b.RecoveryTest.Cadence
	if rt.Cadence == 0 {
		rt.Cadence = Duration(DefaultRecoveryTestCadence)
	}
	checkCadence(v, p+".recovery_test.cadence", rt.Cadence, MinRecoveryTestCadence)
	rt.ValidatorID = b.RecoveryTest.ValidatorID
	if rt.ValidatorID != "" {
		if _, err := RecoveryTestScriptName(rt.ValidatorID); err != nil {
			v.addf("%s.recovery_test.validator_id: %v", p, err)
		}
	}
	if rt.Mode == RecoveryTestRequired && rt.ValidatorID == "" {
		v.addf("%s.recovery_test.validator_id: mode required needs a validator; a recovery test with nothing to run proves only that the bytes came back", p)
	}
	rt.BuiltinCheck = b.RecoveryTest.BuiltinCheck
	if rt.BuiltinCheck == "" {
		rt.BuiltinCheck = BuiltinCheckStructure
	}
	if !containsString(BuiltinChecks, rt.BuiltinCheck) {
		v.addf("%s.recovery_test.builtin_check: %q is not one of %v", p, rt.BuiltinCheck, BuiltinChecks)
	} else if rt.Mode == RecoveryTestBuiltin && rt.BuiltinCheck == BuiltinCheckOff {
		v.addf("%s.recovery_test: mode %s runs the built-in structure check, so builtin_check: %s refuses it; set builtin_check to %s or choose another mode", p, RecoveryTestBuiltin, BuiltinCheckOff, BuiltinCheckStructure)
	}

	out.StagingRoot = b.StagingRoot
	if out.StagingRoot != "" && !filepath.IsAbs(out.StagingRoot) {
		v.addf("%s.staging_root: %q must be an absolute path", p, out.StagingRoot)
	}
	if out.StagingRoot == "" && c.State.Database != "" {
		out.StagingRoot = filepath.Join(filepath.Dir(c.State.Database), DefaultStagingDirName)
	}
	out.StagingTTL = b.StagingTTL.Duration()
	if out.StagingTTL < 0 {
		v.addf("%s.staging_ttl: %s must not be negative", p, b.StagingTTL)
	}
	if out.StagingTTL == 0 {
		out.StagingTTL = DefaultStagingTTL
	}
	out.MaxConcurrentFullReads = positiveOrDefault(v, p+".max_concurrent_full_reads", b.MaxConcurrentFullReads, DefaultMaxConcurrentReads)
	out.MaxPerDestination = positiveOrDefault(v, p+".max_per_destination", b.MaxPerDestination, DefaultMaxPerDestination)
	out.MaxConcurrentFullReadsWritten = b.MaxConcurrentFullReads > 0
	out.MaxPerDestinationWritten = b.MaxPerDestination > 0
	if out.MaxPerDestination > out.MaxConcurrentFullReads {
		v.addf("%s.max_per_destination: %d is above max_concurrent_full_reads (%d), so it could never bind", p, out.MaxPerDestination, out.MaxConcurrentFullReads)
	}
	return out
}

func positiveOrDefault(v *validator, path string, n, def int) int {
	if n < 0 {
		v.addf("%s: %d must not be negative", path, n)
	}
	if n <= 0 {
		return def
	}
	return n
}

// resolveSetAssurance resolves one set's policy. Each field a set does not
// name is inherited from the already-resolved global, so a global change
// reaches it, and the override block itself is never written to.
func resolveSetAssurance(v *validator, path string, o *AssuranceOverride, global Assurance) Assurance {
	out := global
	if o == nil {
		return out
	}
	path += ".assurance"
	if *o == (AssuranceOverride{}) {
		v.addf("%s: names nothing; remove the block to inherit the deployment's policy, or name a field", path)
		return out
	}
	if o.Scrub.Cadence != 0 {
		checkCadence(v, path+".scrub.cadence", o.Scrub.Cadence, MinScrubCadence)
		out.Scrub.Cadence = o.Scrub.Cadence
	}
	if o.Scrub.Strategy != "" {
		if !containsString(ScrubStrategies, o.Scrub.Strategy) {
			v.addf("%s.scrub.strategy: %q is not one of %v", path, o.Scrub.Strategy, ScrubStrategies)
		} else {
			out.Scrub.Strategy = o.Scrub.Strategy
		}
	}

	rt, rp := o.RecoveryTest, path+".recovery_test"
	switch rt.Mode {
	case "", RecoveryTestInherit:
		if rt.Cadence != 0 || rt.ValidatorID != "" {
			v.addf("%s: cadence and validator_id need mode required or custom; inherit takes the deployment's", rp)
		}
	case RecoveryTestOff:
		if rt.Cadence != 0 || rt.ValidatorID != "" {
			v.addf("%s: cadence and validator_id are only used when the mode is required or custom; remove them", rp)
		}
		out.RecoveryTest.Mode = RecoveryTestOff
	case RecoveryTestBuiltin:
		out.RecoveryTest.Mode = RecoveryTestBuiltin
		if rt.ValidatorID != "" {
			v.addf("%s.validator_id: mode %s runs no validator; remove it, or use mode required or custom", rp, RecoveryTestBuiltin)
		}
		out.RecoveryTest.ValidatorID = ""
		if rt.Cadence != 0 {
			checkCadence(v, rp+".cadence", rt.Cadence, MinRecoveryTestCadence)
			out.RecoveryTest.Cadence = rt.Cadence
		}
	case RecoveryTestRequired:
		if rt.Cadence != 0 {
			v.addf("%s.cadence: mode required runs on the deployment's cadence; use mode custom for the set's own", rp)
		}
		out.RecoveryTest.Mode = RecoveryTestRequired
		if rt.ValidatorID != "" {
			out.RecoveryTest.ValidatorID = rt.ValidatorID
		}
	case RecoveryTestCustom:
		out.RecoveryTest.Mode = RecoveryTestCustom
		if rt.Cadence == 0 {
			v.addf("%s.cadence: mode custom needs the set's own cadence", rp)
		} else {
			checkCadence(v, rp+".cadence", rt.Cadence, MinRecoveryTestCadence)
			out.RecoveryTest.Cadence = rt.Cadence
		}
		if rt.ValidatorID == "" {
			v.addf("%s.validator_id: mode custom needs the set's own validator", rp)
		}
		out.RecoveryTest.ValidatorID = rt.ValidatorID
	default:
		v.addf("%s.mode: %q is not one of %v", rp, rt.Mode, SetRecoveryModes)
	}
	if rt.BuiltinCheck != "" {
		if !containsString(BuiltinChecks, rt.BuiltinCheck) {
			v.addf("%s.builtin_check: %q is not one of %v", rp, rt.BuiltinCheck, BuiltinChecks)
		} else {
			out.RecoveryTest.BuiltinCheck = rt.BuiltinCheck
		}
	}
	if (rt.Mode == RecoveryTestBuiltin || rt.BuiltinCheck != "") && out.RecoveryTest.Mode == RecoveryTestBuiltin && out.RecoveryTest.BuiltinCheck == BuiltinCheckOff {
		v.addf("%s: mode %s runs the built-in structure check, so builtin_check: %s refuses it; set builtin_check to %s or choose another mode", rp, RecoveryTestBuiltin, BuiltinCheckOff, BuiltinCheckStructure)
	}
	if rt.ValidatorID != "" {
		if _, err := RecoveryTestScriptName(rt.ValidatorID); err != nil {
			v.addf("%s.validator_id: %v", rp, err)
		}
	}
	if out.RecoveryTest.Mode == RecoveryTestRequired || out.RecoveryTest.Mode == RecoveryTestCustom {
		if out.RecoveryTest.ValidatorID == "" {
			v.addf("%s.validator_id: mode %s needs a validator, on the set or on the deployment", rp, out.RecoveryTest.Mode)
		}
	}
	return out
}

// DefaultScrubMaxPerCycle bounds how many artifacts one backup set's scheduled
// scrub looks at per tick when the resolved policy, and not an explicit
// `revalidation:` block, drives it. With the default weekly cadence it is far
// above what a set owes in a tick; it is the ceiling on a backlog (a freshly
// rebuilt catalog, a first upgrade), not the pace.
const DefaultScrubMaxPerCycle = 50

// Revalidation is the scrub configuration this resolved policy implies for one
// backup set, and the ONE place that derivation is written. The scheduled
// scrub (internal/app) calls it for every set on every tick; anything else
// that needs to know what a set's scrub is allowed to do asks here instead of
// reading assurance.scrub.* again.
//
//   - Interval is assurance.scrub.cadence. A hand-built Config that never ran
//     Validate resolves to zero, and a zero cadence must not read as "due
//     continuously", so the product default stands in.
//   - MaxPerCycle is DefaultScrubMaxPerCycle.
//   - Hash follows assurance.scrub.strategy: auto and content re-hash a local
//     copy, existence does not. A storage medium is never read in full by an
//     unattended scrub whatever the strategy says (FR-31: anything that costs
//     egress is operator-initiated), so strategy content is the local-copy
//     reading of the word and `retnd scrub --content` is the other.
//
// explicit is the set's own `revalidation:` block, which predates the policy
// and keeps working: a block that turns anything on (Hash or Command) is
// complete by validation (interval and max_per_cycle are required with it)
// and is returned unchanged, so a hand-edited config behaves exactly as it
// did. A block that is absent, or present but off, defers to the policy.
func (a Assurance) Revalidation(explicit Revalidation) Revalidation {
	if explicit.Hash || explicit.Command != nil {
		return explicit
	}
	interval := a.Scrub.Cadence.Duration()
	if interval <= 0 {
		interval = DefaultScrubCadence
	}
	return Revalidation{
		Interval:    Duration(interval),
		MaxPerCycle: DefaultScrubMaxPerCycle,
		Hash:        a.Scrub.Strategy != ScrubStrategyExistence,
	}
}

// RecoveryTestEnabled reports whether the policy asks for recovery tests at
// all (mode required or custom). The recovery-test runner asks here, from the
// same resolved policy the scrub does, so the two sides cannot disagree about
// which sets are in scope.
func (a Assurance) RecoveryTestEnabled() bool {
	return a.RecoveryTest.Mode == RecoveryTestRequired || a.RecoveryTest.Mode == RecoveryTestCustom
}

// RecoveryTestInterval is the resolved recovery-test cadence, with the
// product default standing in for the zero a hand-built Config resolves to.
func (a Assurance) RecoveryTestInterval() time.Duration {
	if d := a.RecoveryTest.Cadence.Duration(); d > 0 {
		return d
	}
	return DefaultRecoveryTestCadence
}

// RecoveryTestScheduled reports whether the scheduler starts recovery tests
// for this policy: mode required or custom (the operator's validator) or mode
// builtin (the product's structure check). Only RecoveryTestEnabled says the
// operator requires a validator's pass.
func (a Assurance) RecoveryTestScheduled() bool {
	return a.RecoveryTestEnabled() || a.RecoveryTest.Mode == RecoveryTestBuiltin
}
