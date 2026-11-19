package config

import (
	"strings"
	"testing"
	"time"
)

// #1221: validator_env decides whether a recovery-test validator gets the
// set's workflow environment. The default is the small one, and anything
// that is not exactly a known value is refused rather than guessed at.

func TestIssue1221_ValidatorEnvDefaultsToMinimalEverywhere(t *testing.T) {
	got, err := rtOf(t, func(c *Config) {})
	if err != nil {
		t.Fatal(err)
	}
	if got.Global.ValidatorEnv != ValidatorEnvMinimal || got.Set.ValidatorEnv != ValidatorEnvMinimal {
		t.Errorf("an unset validator_env resolves to minimal, got %q / %q", got.Global.ValidatorEnv, got.Set.ValidatorEnv)
	}
}

func TestIssue1221_ValidatorEnvInheritsAndASetCanOptInOrBackOut(t *testing.T) {
	got, err := rtOf(t, func(c *Config) {
		c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{ValidatorEnv: ValidatorEnvWorkflow}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Set.ValidatorEnv != ValidatorEnvWorkflow {
		t.Errorf("a set with no override follows the global opt-in, got %q", got.Set.ValidatorEnv)
	}
	got, err = rtOf(t, func(c *Config) {
		c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{ValidatorEnv: ValidatorEnvWorkflow}}
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{ValidatorEnv: ValidatorEnvMinimal}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Global.ValidatorEnv != ValidatorEnvWorkflow || got.Set.ValidatorEnv != ValidatorEnvMinimal {
		t.Errorf("the set's own minimal beats the global opt-in: %q / %q", got.Global.ValidatorEnv, got.Set.ValidatorEnv)
	}
	got, err = rtOf(t, func(c *Config) {
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{ValidatorEnv: ValidatorEnvWorkflow}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Global.ValidatorEnv != ValidatorEnvMinimal || got.Set.ValidatorEnv != ValidatorEnvWorkflow {
		t.Errorf("one set's opt-in does not widen the global: %q / %q", got.Global.ValidatorEnv, got.Set.ValidatorEnv)
	}
}

func TestIssue1221_ValidatorEnvIsAllowedWithEveryMode(t *testing.T) {
	for _, mode := range []string{"", RecoveryTestInherit, RecoveryTestOff, RecoveryTestBuiltin, RecoveryTestRequired, RecoveryTestCustom} {
		t.Run("mode "+mode, func(t *testing.T) {
			rt := AssuranceRecoveryTest{Mode: mode, ValidatorEnv: ValidatorEnvWorkflow}
			switch mode {
			case RecoveryTestRequired:
				rt.ValidatorID = "pg"
			case RecoveryTestCustom:
				rt.ValidatorID, rt.Cadence = "pg", Duration(48*time.Hour)
			}
			got, err := rtOf(t, func(c *Config) { c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: rt} })
			if err != nil {
				t.Fatalf("validator_env with mode %q: %v", mode, err)
			}
			if got.Set.ValidatorEnv != ValidatorEnvWorkflow {
				t.Errorf("validator_env was dropped under mode %q: %q", mode, got.Set.ValidatorEnv)
			}
		})
	}
}

func TestIssue1221_ValidatorEnvRefusesAnythingButTheTwoValuesByName(t *testing.T) {
	for _, bad := range []string{"all", "Workflow", "WORKFLOW", " workflow", "workflow ", "secrets", "true", "none"} {
		for name, build := range map[string]func(c *Config){
			"global": func(c *Config) {
				c.AssuranceBlock = &AssuranceBlock{RecoveryTest: AssuranceRecoveryTest{ValidatorEnv: bad}}
			},
			"set": func(c *Config) {
				c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{ValidatorEnv: bad}}
			},
		} {
			_, err := rtOf(t, build)
			if err == nil || !strings.Contains(err.Error(), "validator_env") {
				t.Errorf("%s %q: err = %v, want a refusal naming validator_env", name, bad, err)
			}
		}
	}
}

func TestIssue1221_ABadValidatorEnvNeverResolvesToTheOptIn(t *testing.T) {
	got, _ := rtOf(t, func(c *Config) {
		c.Sources[0].BackupSets[0].AssuranceConfig = &AssuranceOverride{RecoveryTest: AssuranceRecoveryTest{ValidatorEnv: "Workflow"}}
	})
	if got.Set.ValidatorEnv == ValidatorEnvWorkflow {
		t.Errorf("a refused spelling resolved to the opt-in: %q", got.Set.ValidatorEnv)
	}
}
