package config

import (
	"strings"
	"testing"
	"time"
)

// #1111: builtin_check, mode builtin, and the reserved retnd- prefix.

func rtOf(t *testing.T, build func(c *Config)) (RecoveryTestResolved, error) {
	t.Helper()
	c := validConfig()
	build(&c)
	err := c.Validate()
	return RecoveryTestResolved{Global: c.Assurance.RecoveryTest, Set: c.Sources[0].BackupSets[0].Assurance.RecoveryTest}, err
}

// RecoveryTestResolved is the global and the first set's resolved halves.
type RecoveryTestResolved struct{ Global, Set AssuranceRecoveryTest }

func TestIssue1111_BuiltinCheckResolvesAndInherits(t *testing.T) {
	got, err := rtOf(t, func(c *Config) {})
	if err != nil {
		t.Fatal(err)
	}
	if got.Global.BuiltinCheck != BuiltinCheckStructure || got.Set.BuiltinCheck != BuiltinCheckStructure {
		t.Errorf("an empty builtin_check resolves to structure, got %q / %q", got.Global.BuiltinCheck, got.Set.BuiltinCheck)
	}
	got, err = rtOf(t, func(c *Config) {
		c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{BuiltinCheck: BuiltinCheckOff}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Set.BuiltinCheck != BuiltinCheckOff {
		t.Errorf("a set with no override follows the global off, got %q", got.Set.BuiltinCheck)
	}
	got, err = rtOf(t, func(c *Config) {
		c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{BuiltinCheck: BuiltinCheckOff}}
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{BuiltinCheck: BuiltinCheckStructure}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Global.BuiltinCheck != BuiltinCheckOff || got.Set.BuiltinCheck != BuiltinCheckStructure {
		t.Errorf("the set's own value beats the global: %q / %q", got.Global.BuiltinCheck, got.Set.BuiltinCheck)
	}
}

func TestIssue1111_BuiltinCheckIsAllowedWithEveryMode(t *testing.T) {
	for _, mode := range []string{"", RecoveryTestInherit, RecoveryTestOff, RecoveryTestBuiltin, RecoveryTestRequired, RecoveryTestCustom} {
		t.Run("mode "+mode, func(t *testing.T) {
			rt := AssuranceRecoveryTest{Mode: mode, BuiltinCheck: BuiltinCheckStructure}
			switch mode {
			case RecoveryTestRequired:
				rt.ValidatorID = "pg"
			case RecoveryTestCustom:
				rt.ValidatorID, rt.Cadence = "pg", Duration(48*time.Hour)
			}
			if _, err := rtOf(t, func(c *Config) { c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: rt} }); err != nil {
				t.Errorf("builtin_check with mode %q: %v", mode, err)
			}
		})
	}
}

func TestIssue1111_AnythingButStructureOrOffIsRefusedByName(t *testing.T) {
	for name, build := range map[string]func(c *Config){
		"global": func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{BuiltinCheck: "deep"}}
		},
		"set": func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{BuiltinCheck: "Structure"}}
		},
	} {
		_, err := rtOf(t, build)
		if err == nil || !strings.Contains(err.Error(), "builtin_check") {
			t.Errorf("%s: err = %v, want a refusal naming builtin_check", name, err)
		}
	}
}

func TestIssue1111_ModeBuiltinNeedsNoValidatorAndTakesACadence(t *testing.T) {
	got, err := rtOf(t, func(c *Config) {
		c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin, Cadence: Duration(72 * time.Hour)}}
	})
	if err != nil {
		t.Fatalf("global builtin: %v", err)
	}
	if got.Global.Mode != RecoveryTestBuiltin || got.Set.Mode != RecoveryTestBuiltin || got.Set.Cadence.Duration() != 72*time.Hour {
		t.Errorf("resolved = %+v / %+v", got.Global, got.Set)
	}
	got, err = rtOf(t, func(c *Config) {
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin, Cadence: Duration(48 * time.Hour)}}
	})
	if err != nil {
		t.Fatalf("set builtin with its own cadence: %v", err)
	}
	if got.Set.Mode != RecoveryTestBuiltin || got.Set.Cadence.Duration() != 48*time.Hour {
		t.Errorf("set = %+v", got.Set)
	}
	got, err = rtOf(t, func(c *Config) {
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin}}
	})
	if err != nil || got.Set.Cadence.Duration() != DefaultRecoveryTestCadence {
		t.Errorf("a set builtin with no cadence takes the deployment's: %v / %+v", err, got.Set)
	}
	if _, err := rtOf(t, func(c *Config) {
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin, Cadence: Duration(time.Minute)}}
	}); err == nil {
		t.Error("a builtin cadence under the floor was accepted")
	}
	if _, err := rtOf(t, func(c *Config) {
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin, ValidatorID: "pg"}}
	}); err == nil || !strings.Contains(err.Error(), "validator_id") {
		t.Errorf("mode builtin runs no validator, so naming one is refused: %v", err)
	}
}

func TestIssue1111_ModeBuiltinWithBuiltinCheckOffIsRefusedNamingBothKeys(t *testing.T) {
	for name, build := range map[string]func(c *Config){
		"global": func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin, BuiltinCheck: BuiltinCheckOff}}
		},
		"set": func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin, BuiltinCheck: BuiltinCheckOff}}
		},
		"set mode builtin under a global off": func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{BuiltinCheck: BuiltinCheckOff}}
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin}}
		},
		"inherited builtin mode under a set off": func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin}}
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{BuiltinCheck: BuiltinCheckOff}}
		},
	} {
		_, err := rtOf(t, build)
		if err == nil || !strings.Contains(err.Error(), "builtin_check") || !strings.Contains(err.Error(), "mode") {
			t.Errorf("%s: err = %v, want a refusal naming mode and builtin_check", name, err)
		}
	}
}

func TestIssue1111_ModeBuiltinIsNotWhatRequiredMeans(t *testing.T) {
	c := validConfig()
	c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestBuiltin}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Sources[0].BackupSets[0].Assurance.RecoveryTestEnabled() {
		t.Error("RecoveryTestEnabled (mode required or custom) is true for mode builtin: a built-in pass would satisfy required")
	}
	if !c.Sources[0].BackupSets[0].Assurance.RecoveryTestScheduled() {
		t.Error("mode builtin is scheduled")
	}
	for _, mode := range []string{RecoveryTestOff, RecoveryTestInherit, ""} {
		c := validConfig()
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: mode, BuiltinCheck: BuiltinCheckStructure}}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Sources[0].BackupSets[0].Assurance.RecoveryTestScheduled() {
			t.Errorf("mode %q is scheduled", mode)
		}
	}
}

func TestIssue1111_TheRetndPrefixIsReservedForValidatorIDs(t *testing.T) {
	for _, id := range []string{"retnd-foo", "retnd-builtin-structure", "retnd-"} {
		if !IsReservedValidatorID(id) {
			t.Errorf("IsReservedValidatorID(%q) = false", id)
		}
		if got, err := RecoveryTestScriptName(id); err == nil {
			t.Errorf("RecoveryTestScriptName(%q) = %q, want a refusal", id, got)
		} else if !strings.Contains(err.Error(), "retnd-") {
			t.Errorf("RecoveryTestScriptName(%q) refusal does not say why: %v", id, err)
		}
	}
	for _, id := range []string{"pg", "retnd", "xretnd-foo", "Retnd-foo"} {
		if IsReservedValidatorID(id) {
			t.Errorf("IsReservedValidatorID(%q) = true", id)
		}
	}
	for name, build := range map[string]func(c *Config){
		"global": func(c *Config) {
			c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "retnd-foo"}}
		},
		"set": func(c *Config) {
			c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{Mode: RecoveryTestRequired, ValidatorID: "retnd-foo"}}
		},
	} {
		_, err := rtOf(t, build)
		if err == nil || !strings.Contains(err.Error(), "ids beginning retnd- are the product's own built-in checks") {
			t.Errorf("%s: an existing config using a reserved id must be refused at load with the clear message, got %v", name, err)
		}
	}
}
