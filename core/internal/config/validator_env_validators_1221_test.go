package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// #1221, per validator: a custom validator's entry may say env: minimal or
// workflow. Exactly those two spellings load, the entry's own word beats the
// set's and the deployment's, and nothing that is not exactly workflow ever
// resolves to it.

func TestIssue1221_ValidatorEntryEnvIsKeptOnTheResolvedValidator(t *testing.T) {
	c := validatorConfig(
		CustomValidator{Name: "a", Pattern: "x", Env: ValidatorEnvWorkflow},
		CustomValidator{Name: "b", Pattern: "y", Env: ValidatorEnvMinimal},
		CustomValidator{Name: "c", Pattern: "z"},
	)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := c.Assurance.Validators
	if len(got) != 3 || got[0].Env != ValidatorEnvWorkflow || got[1].Env != ValidatorEnvMinimal || got[2].Env != "" {
		t.Errorf("resolved envs = %q / %q / %q, want workflow / minimal / (unstated)", got[0].Env, got[1].Env, got[2].Env)
	}
	if c.AssuranceBlock.Validators[0].Env != ValidatorEnvWorkflow {
		t.Error("Validate rewrote the operator's own block")
	}
}

func TestIssue1221_ValidatorEntryEnvRefusesAnythingButTheTwoValuesByName(t *testing.T) {
	for _, bad := range []string{"all", "Workflow", "WORKFLOW", "Minimal", " workflow", "workflow ", "secrets", "true", "none", "inherit"} {
		c := validatorConfig(CustomValidator{Name: "a", Pattern: "x", Env: bad})
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "assurance.validators[0].env") {
			t.Errorf("env %q: err = %v, want a refusal naming assurance.validators[0].env", bad, err)
		}
		for _, r := range c.Assurance.Validators {
			if r.Env == ValidatorEnvWorkflow {
				t.Errorf("env %q resolved to the opt-in", bad)
			}
		}
	}
}

func TestIssue1221_ABadEnvOnOneValidatorDoesNotHideAnotherEntrysFault(t *testing.T) {
	c := validatorConfig(
		CustomValidator{Name: "a", Pattern: "x", Env: "Workflow"},
		CustomValidator{Name: "b", Pattern: "(unclosed"},
	)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "validators[0].env") || !strings.Contains(err.Error(), "validators[1].pattern") {
		t.Errorf("err = %v, want both faults reported", err)
	}
}

func TestIssue1221_ValidatorEnvPrecedenceIsValidatorThenSetThenMinimal(t *testing.T) {
	cases := []struct {
		name, validator, inherited, want string
	}{
		{"nothing says anything", "", "", ValidatorEnvMinimal},
		{"the set says minimal", "", ValidatorEnvMinimal, ValidatorEnvMinimal},
		{"the set opts in", "", ValidatorEnvWorkflow, ValidatorEnvWorkflow},
		{"the validator opts in alone", ValidatorEnvWorkflow, ValidatorEnvMinimal, ValidatorEnvWorkflow},
		{"the validator opts in over an unstated set", ValidatorEnvWorkflow, "", ValidatorEnvWorkflow},
		{"the validator backs out of a set opt-in", ValidatorEnvMinimal, ValidatorEnvWorkflow, ValidatorEnvMinimal},
		{"a misspelt validator value is minimal, not the set's opt-in", "Workflow", ValidatorEnvWorkflow, ValidatorEnvMinimal},
		{"a padded validator value is minimal", "workflow ", ValidatorEnvWorkflow, ValidatorEnvMinimal},
		{"a misspelt set value is minimal", "", "WORKFLOW", ValidatorEnvMinimal},
		{"a misspelt set value does not beat a validator opt-in", ValidatorEnvWorkflow, "all", ValidatorEnvWorkflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveValidatorEnv(tc.validator, tc.inherited); got != tc.want {
				t.Errorf("ResolveValidatorEnv(%q, %q) = %q, want %q", tc.validator, tc.inherited, got, tc.want)
			}
		})
	}
}

func TestIssue1221_ValidatorEnvRoundTripsThroughTheFileAndAnUnstatedOneIsNotWritten(t *testing.T) {
	c := validatorConfig(
		CustomValidator{Name: "a", Pattern: "x", Env: ValidatorEnvWorkflow},
		CustomValidator{Name: "b", Pattern: "y"},
	)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(regexp.MustCompile(`(?m)^\s+env: (minimal|workflow)$`).FindAll(encoded, -1)); n != 1 || !strings.Contains(string(encoded), "env: workflow") {
		t.Errorf("the file should carry env once, for the entry that states it:\n%s", encoded)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	back, err := LoadAndValidate(p)
	if err != nil {
		t.Fatalf("LoadAndValidate: %v", err)
	}
	if v := back.Assurance.Validators; len(v) != 2 || v[0].Env != ValidatorEnvWorkflow || v[1].Env != "" {
		t.Errorf("read back %+v", v)
	}
}

func TestIssue1221_AMisspeltValidatorEntryEnvInTheFileDoesNotLoad(t *testing.T) {
	c := validatorConfig(CustomValidator{Name: "a", Pattern: "x", Env: "Workflow"})
	encoded, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAndValidate(p); err == nil || !strings.Contains(err.Error(), "assurance.validators[0].env") {
		t.Errorf("Load err = %v, want a refusal naming assurance.validators[0].env", err)
	}
}
