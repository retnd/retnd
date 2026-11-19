package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// #1222: assurance.validators[].script_sha256 pins the script the operator
// approved.

func TestCustomValidatorPin_AWellFormedHashIsKeptAndResolved(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	c := validatorConfig(CustomValidator{Name: "manifest", Pattern: `^M`, ScriptSHA256: pin})
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := c.Assurance.Validators[0].ScriptSHA256; got != pin {
		t.Errorf("resolved pin = %q", got)
	}
}

func TestCustomValidatorPin_AnEntryWithNoHashStillLoads(t *testing.T) {
	c := validatorConfig(CustomValidator{Name: "manifest", Pattern: `^M`})
	if err := c.Validate(); err != nil {
		t.Fatalf("an entry written before pinning was refused: %v", err)
	}
	if c.Assurance.Validators[0].ScriptSHA256 != "" {
		t.Error("Validate invented a pin")
	}
}

func TestCustomValidatorPin_AMalformedHashIsRefusedWithItsOwnSentence(t *testing.T) {
	for name, pin := range map[string]string{
		"too short":  strings.Repeat("a", 63),
		"too long":   strings.Repeat("a", 65),
		"uppercase":  strings.Repeat("AB", 32),
		"not hex":    strings.Repeat("zz", 32),
		"whitespace": " " + strings.Repeat("a", 63),
	} {
		c := validatorConfig(CustomValidator{Name: "manifest", Pattern: `^M`, ScriptSHA256: pin})
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "assurance.validators[0].script_sha256") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCustomValidatorPin_TheKeyRoundTripsThroughYAMLAndIsOmittedWhenEmpty(t *testing.T) {
	pin := strings.Repeat("0f", 32)
	out, err := yaml.Marshal(AssuranceBlock{Validators: []CustomValidator{{Name: "a", Pattern: "x", ScriptSHA256: pin}, {Name: "b", Pattern: "y"}}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "script_sha256: "+pin) || strings.Count(text, "script_sha256") != 1 {
		t.Errorf("yaml = %s", text)
	}
	var back AssuranceBlock
	if err := yaml.Unmarshal(out, &back); err != nil || back.Validators[0].ScriptSHA256 != pin {
		t.Errorf("round trip = %+v, %v", back, err)
	}
}
