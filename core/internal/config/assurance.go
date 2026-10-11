package config

import "time"

// Assurance configuration (EPIC Q, Q1.4 / FR-73): the global default and
// the per-set override for scrubbing and recovery testing.

// Recovery-test modes. The global block takes off or required; a backup set
// may also say inherit (the default) or custom.
const (
	RecoveryTestInherit  = "inherit"
	RecoveryTestOff      = "off"
	RecoveryTestRequired = "required"
	RecoveryTestCustom   = "custom"
)

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
	DefaultMaxConcurrentReads  = 2
	DefaultMaxPerDestination   = 1
	DefaultWebDownloadMemory   = int64(64 << 20)
)

// AssuranceScrub is the scrub half of the policy.
type AssuranceScrub struct {
	Cadence  Duration `yaml:"cadence,omitempty"`
	Strategy string   `yaml:"strategy,omitempty"`
}

// AssuranceRecoveryTest is the recovery-test half of the policy.
type AssuranceRecoveryTest struct {
	Mode        string   `yaml:"mode,omitempty"`
	Cadence     Duration `yaml:"cadence,omitempty"`
	ValidatorID string   `yaml:"validator_id,omitempty"`
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
	WebDownloadMemoryBytes int64    `yaml:"web_download_memory_bytes,omitempty"`
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
	WebDownloadMemoryBytes int64
}

// AssuranceIsOverride reports whether this set declares its own policy.
func (b BackupSet) AssuranceIsOverride() bool { return b.AssuranceConfig != nil }

func (c *Config) resolveAssurance(v *validator) {}
