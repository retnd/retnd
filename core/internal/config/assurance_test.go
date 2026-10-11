package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// EPIC Q, Q1.4 (#982). The global assurance default, the per-set override
// in Retention's three-field pattern, and the re-resolve after a mutation of
// the global.

func TestAssurance_ANilBlockResolvesToUsefulDefaults(t *testing.T) {
	c := validConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	if c.AssuranceBlock != nil {
		t.Fatalf("Validate must not write the defaults back into the operator's block, got %+v", c.AssuranceBlock)
	}
	got := c.Sources[0].BackupSets[0].Assurance
	if got.Scrub.Cadence.Duration() != DefaultScrubCadence {
		t.Errorf("scrub cadence = %s, want the default %s", got.Scrub.Cadence, DefaultScrubCadence)
	}
	if got.Scrub.Strategy != ScrubStrategyAuto {
		t.Errorf("scrub strategy = %q, want %q", got.Scrub.Strategy, ScrubStrategyAuto)
	}
	if got.RecoveryTest.Mode != RecoveryTestOff {
		t.Errorf("recovery test mode = %q, want off on a deployment that named no validator", got.RecoveryTest.Mode)
	}
	d := c.Assurance
	if d.StagingTTL != DefaultStagingTTL || d.MaxConcurrentFullReads != DefaultMaxConcurrentReads ||
		d.MaxPerDestination != DefaultMaxPerDestination || d.WebDownloadMemoryBytes != DefaultWebDownloadMemory {
		t.Errorf("host-scoped defaults not resolved: %+v", d)
	}
}

func TestAssurance_AConfigThatMentionsNothingSurvivesAReMarshalByteIdentically(t *testing.T) {
	c := validConfig()
	before, err := yaml.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "assurance") {
		t.Fatalf("a config that names no assurance must not marshal the key:\n%s", before)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	after, err := yaml.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "assurance") {
		t.Errorf("Validate leaked the resolved assurance policy into the marshalled file:\n%s", after)
	}
}

func TestAssurance_PerSetModesEachResolveThroughValidate(t *testing.T) {
	type tc struct {
		name      string
		global    *AssuranceBlock
		override  *AssuranceOverride
		mode      string
		cadence   time.Duration
		validator string
	}
	cases := []tc{
		{name: "inherit by absence", mode: RecoveryTestOff, cadence: DefaultRecoveryTestCadence},
		{
			name:     "explicit inherit follows a required global",
			global:   &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "pg"}},
			override: &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestInherit}},
			mode:     RecoveryTestRequired, cadence: DefaultRecoveryTestCadence, validator: "pg",
		},
		{
			name:     "off beats a required global",
			global:   &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "pg"}},
			override: &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestOff}},
			mode:     RecoveryTestOff, cadence: DefaultRecoveryTestCadence, validator: "pg",
		},
		{
			name:     "required with its own validator",
			override: &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "mysql"}},
			mode:     RecoveryTestRequired, cadence: DefaultRecoveryTestCadence, validator: "mysql",
		},
		{
			name: "custom takes its own cadence and validator",
			override: &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{
				Mode: RecoveryTestCustom, Cadence: Duration(48 * time.Hour), ValidatorID: "mysql"}},
			mode: RecoveryTestCustom, cadence: 48 * time.Hour, validator: "mysql",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			c.AssuranceBlock = tc.global
			c.Sources[0].BackupSets[0].AssuranceConfig = tc.override
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			got := c.Sources[0].BackupSets[0].Assurance.RecoveryTest
			if got.Mode != tc.mode || got.Cadence.Duration() != tc.cadence || got.ValidatorID != tc.validator {
				t.Errorf("resolved = %+v, want mode %q cadence %s validator %q", got, tc.mode, tc.cadence, tc.validator)
			}
		})
	}
}

func TestAssurance_AScrubOverrideInheritsEveryFieldItDoesNotName(t *testing.T) {
	c := validConfig()
	c.AssuranceBlock = &AssuranceBlock{Scrub: AssuranceScrub{Cadence: Duration(72 * time.Hour), Strategy: ScrubStrategyContent}}
	c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{Scrub: AssuranceScrub{Cadence: Duration(2 * time.Hour)}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	got := c.Sources[0].BackupSets[0].Assurance.Scrub
	if got.Cadence.Duration() != 2*time.Hour || got.Strategy != ScrubStrategyContent {
		t.Errorf("resolved scrub = %+v, want 2h cadence and the inherited content strategy", got)
	}
	if c.Sources[0].BackupSets[0].AssuranceConfig.Scrub.Strategy != "" {
		t.Error("Validate wrote the inherited strategy into the operator's override")
	}
}

// The retention-override trap: mutate the global, forget to re-resolve, and
// every inheriting set goes on deciding under the old policy.
func TestAssurance_AGlobalMutationFollowedByValidateMovesAnInheritingSet(t *testing.T) {
	c := validConfig()
	inheriting := c.Sources[0].BackupSets[0]
	inheriting.Name = "inherits"
	pinned := inheriting
	pinned.Name = "pinned"
	pinned.AssuranceConfig = &AssuranceOverride{Scrub: AssuranceScrub{Cadence: Duration(5 * time.Hour)}}
	c.Sources[0].BackupSets = []BackupSet{inheriting, pinned}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.AssuranceBlock = &AssuranceBlock{Scrub: AssuranceScrub{Cadence: Duration(12 * time.Hour)}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.Sources[0].BackupSets[0].Assurance.Scrub.Cadence.Duration(); got != 12*time.Hour {
		t.Errorf("inheriting set cadence = %s after a global change, want 12h", got)
	}
	if got := c.Sources[0].BackupSets[1].Assurance.Scrub.Cadence.Duration(); got != 5*time.Hour {
		t.Errorf("overriding set cadence = %s, a global change must not move it (want 5h)", got)
	}
}

func TestAssurance_InvalidInputsAreRefusedWithTheirOwnSentence(t *testing.T) {
	cases := []struct {
		name  string
		build func(c *Config)
		want  string
	}{
		{"global cadence below the floor", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{Scrub: AssuranceScrub{Cadence: Duration(time.Minute)}}
		}, "assurance.scrub.cadence"},
		{"set cadence below the floor", func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{Scrub: AssuranceScrub{Cadence: Duration(time.Minute)}}
		}, "scrub.cadence"},
		{"required with no validator anywhere", func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired}}
		}, "validator_id"},
		{"global required with no validator", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired}}
		}, "validator_id"},
		{"custom with no cadence", func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestCustom, ValidatorID: "x"}}
		}, "cadence"},
		{"unknown mode", func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: "sometimes"}}
		}, "mode"},
		{"unknown strategy", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{Scrub: AssuranceScrub{Strategy: "vibes"}}
		}, "strategy"},
		{"global inherit is meaningless", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestInherit}}
		}, "mode"},
		{"cadence under off", func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestOff, Cadence: Duration(48 * time.Hour)}}
		}, "cadence"},
		{"a path smuggled in as a validator id", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "../bin/sh"}}
		}, "validator_id"},
		{"a script file name carried whole", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "nightly.local.sh"}}
		}, "validator_id"},
		{"a name that starts with a dash", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "-nightly"}}
		}, "validator_id"},
		{"a hidden file name", func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestCustom, Cadence: Duration(48 * time.Hour), ValidatorID: ".nightly"}}
		}, "validator_id"},
		{"a name with a non-ASCII letter", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "n\u0131ghtly"}}
		}, "validator_id"},
		{"an empty per-set block", func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{}
		}, "assurance"},
		{"negative concurrency", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{MaxConcurrentFullReads: -1}
		}, "max_concurrent_full_reads"},
		{"relative staging root", func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{StagingRoot: "staging"}
		}, "staging_root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.build(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("Validate accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestAssurance_UnknownKeyIsRefusedByLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "assurance:\n  scrub:\n    cadance: 24h\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("a misspelt assurance key was accepted")
	}
}

// The guard the retention override needed and did not have: a consumer that
// reads the pointer decides under the raw override, not the resolved policy.
func TestAssurance_NoConsumerReadsThePointers(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := func(path string) bool {
		slash := filepath.ToSlash(path)
		return strings.Contains(slash, "core/internal/config/") ||
			strings.Contains(slash, "core/service/assurance")
	}
	var offenders []string
	for _, top := range []string{"core", "apps", "distribution"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowed(path) {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			s := string(b)
			if strings.Contains(s, ".AssuranceConfig") || strings.Contains(s, ".AssuranceBlock") {
				offenders = append(offenders, path)
			}
			return nil
		})
	}
	if len(offenders) > 0 {
		t.Errorf("these files read the raw assurance pointers instead of the resolved field: %v", offenders)
	}
}

// Panel finding C3 (S3): validator_id names the recovery-test script by file
// name, NAME in NAME.local.sh, so the config layer is where an id that could
// never be a script's name is refused, at save time.
func TestRecoveryTestScriptName(t *testing.T) {
	for id, want := range map[string]string{
		"pg":                    "pg.local.sh",
		"nightly-restore_check": "nightly-restore_check.local.sh",
		"10-verify.v2":          "10-verify.v2.local.sh",
	} {
		got, err := RecoveryTestScriptName(id)
		if err != nil || got != want {
			t.Errorf("RecoveryTestScriptName(%q) = %q, %v; want %q", id, got, err, want)
		}
	}
	for _, id := range []string{"", "nightly.local.sh", "nightly.sh", "-x", ".x", "a/b", "a b", "n\u0131ghtly", "a\nb", ".", ".."} {
		if got, err := RecoveryTestScriptName(id); err == nil {
			t.Errorf("RecoveryTestScriptName(%q) = %q, want a refusal", id, got)
		}
	}
}
