package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/model"
	"gopkg.in/yaml.v3"
)

// #1203: custom recovery-test validators under assurance.validators.

func validatorConfig(list ...CustomValidator) *Config {
	c := validConfig()
	c.AssuranceBlock = &AssuranceBlock{Validators: list}
	return &c
}

func TestCustomValidators_ResolveCompiledInDeclarationOrder(t *testing.T) {
	c := validatorConfig(
		CustomValidator{Name: "manifest", Pattern: `^MANIFEST-\d{8}T\d{6}Z\.txt$`, BackupSet: "production/postgres-primary"},
		CustomValidator{Name: "dumps", Pattern: `\.sql\.zst$`},
	)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := c.Assurance.Validators
	if len(got) != 2 || got[0].Name != "manifest" || got[1].Name != "dumps" {
		t.Fatalf("validators = %+v", got)
	}
	if got[0].ScriptFile != "manifest.local.sh" || got[0].Regexp == nil || got[0].Global() || got[0].Scope() != CustomValidatorScopeBackupSet {
		t.Errorf("set-scoped validator resolved as %+v", got[0])
	}
	if !got[1].Global() || got[1].Scope() != CustomValidatorScopeGlobal {
		t.Errorf("a validator with no backup_set must be global: %+v", got[1])
	}
	if c.AssuranceBlock.Validators[0].Pattern != `^MANIFEST-\d{8}T\d{6}Z\.txt$` {
		t.Error("Validate rewrote the operator's own block")
	}
}

func TestCustomValidators_InvalidEntriesAreRefusedWithTheirOwnSentence(t *testing.T) {
	cases := map[string]struct {
		list []CustomValidator
		want string
	}{
		"bad name":         {[]CustomValidator{{Name: "a/b", Pattern: "x"}}, "assurance.validators[0].name"},
		"reserved name":    {[]CustomValidator{{Name: "retnd-x", Pattern: "x"}}, "built-in checks"},
		"file name":        {[]CustomValidator{{Name: "x.local.sh", Pattern: "x"}}, "without its .local.sh suffix"},
		"duplicate name":   {[]CustomValidator{{Name: "a", Pattern: "x"}, {Name: "a", Pattern: "y"}}, "names are unique"},
		"empty pattern":    {[]CustomValidator{{Name: "a", Pattern: " "}}, "needs a pattern"},
		"bad regex":        {[]CustomValidator{{Name: "a", Pattern: "(unclosed"}}, "is not a Go (RE2) regular expression"},
		"perl lookahead":   {[]CustomValidator{{Name: "a", Pattern: "(?=x)"}}, "is not a Go (RE2) regular expression"},
		"long pattern":     {[]CustomValidator{{Name: "a", Pattern: strings.Repeat("a", MaxCustomValidatorPatternBytes+1)}}, "may be at most 512"},
		"bad set id":       {[]CustomValidator{{Name: "a", Pattern: "x", BackupSet: "nosep"}}, "is not a backup set id"},
		"unknown set":      {[]CustomValidator{{Name: "a", Pattern: "x", BackupSet: "production/nope"}}, "is not a backup set in this configuration"},
		"same scope twice": {[]CustomValidator{{Name: "a", Pattern: "x"}, {Name: "b", Pattern: "x"}}, "same scope"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validatorConfig(tc.list...).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestCustomValidators_TheSamePatternAtTwoScopesIsNotADuplicate(t *testing.T) {
	c := validatorConfig(
		CustomValidator{Name: "a", Pattern: "x", BackupSet: "production/postgres-primary"},
		CustomValidator{Name: "b", Pattern: "x"},
	)
	if err := c.Validate(); err != nil {
		t.Fatalf("a set-scoped and a global validator with one pattern are two scopes: %v", err)
	}
}

func TestCustomValidators_MoreThanTheDirectoryHoldsIsRefused(t *testing.T) {
	var list []CustomValidator
	for i := 0; i <= MaxCustomValidators; i++ {
		list = append(list, CustomValidator{Name: "v" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26)), Pattern: "p" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	err := validatorConfig(list...).Validate()
	if err == nil || !strings.Contains(err.Error(), "at most 64") {
		t.Errorf("err = %v", err)
	}
}

func TestCustomValidators_MatchIsMostSpecificFirstAndRefusesToGuess(t *testing.T) {
	set := model.BackupSetID{Source: "production", Set: "postgres-primary"}
	other := model.BackupSetID{Source: "production", Set: "other"}
	c := validatorConfig(
		CustomValidator{Name: "global-manifest", Pattern: `^MANIFEST-`},
		CustomValidator{Name: "set-manifest", Pattern: `^MANIFEST-\d{8}`, BackupSet: set.String()},
		CustomValidator{Name: "global-txt", Pattern: `\.txt$`},
	)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	list := c.Assurance.Validators

	m := MatchCustomValidator(list, set, "MANIFEST-20260902T153521Z.txt")
	if m.Validator == nil || m.Validator.Name != "set-manifest" {
		t.Errorf("the set's own pattern must beat every global one: %+v", m)
	}
	// In another set the set-scoped one is out of scope, and two global
	// patterns match: that is ambiguous, never first-declared-wins.
	m = MatchCustomValidator(list, other, "MANIFEST-20260902T153521Z.txt")
	if m.Validator != nil || len(m.Ambiguous) != 2 || m.Ambiguous[0] != "global-manifest" || m.Ambiguous[1] != "global-txt" {
		t.Errorf("two global matches = %+v, want an ambiguity naming both", m)
	}
	m = MatchCustomValidator(list, other, "notes.txt")
	if m.Validator == nil || m.Validator.Name != "global-txt" {
		t.Errorf("a single global match = %+v", m)
	}
	if m := MatchCustomValidator(list, set, "db.sql.zst"); m.Found() {
		t.Errorf("nothing should match db.sql.zst: %+v", m)
	}
}

func TestCustomValidators_DropForASetKeepsTheRest(t *testing.T) {
	b := &AssuranceBlock{Validators: []CustomValidator{
		{Name: "a", Pattern: "x", BackupSet: "s/one"}, {Name: "b", Pattern: "y"}, {Name: "c", Pattern: "z", BackupSet: "s/one"},
	}}
	dropped := b.DropCustomValidatorsFor("s/one")
	if len(dropped) != 2 || dropped[0] != "a" || dropped[1] != "c" || len(b.Validators) != 1 || b.Validators[0].Name != "b" {
		t.Errorf("dropped=%v left=%+v", dropped, b.Validators)
	}
	if !(AssuranceBlock{}).IsZero() || (AssuranceBlock{Validators: b.Validators}).IsZero() {
		t.Error("IsZero must count the validators")
	}
}

func TestCustomValidators_RoundTripThroughTheFile(t *testing.T) {
	c := validatorConfig(CustomValidator{Name: "manifest", Pattern: `^MANIFEST-\d{8}T\d{6}Z\.txt$`, BackupSet: "production/postgres-primary"})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	encoded, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "validators:") || !strings.Contains(string(raw), "backup_set: production/postgres-primary") {
		t.Errorf("the file does not carry the block:\n%s", raw)
	}
	back, err := LoadAndValidate(p)
	if err != nil {
		t.Fatalf("LoadAndValidate: %v", err)
	}
	if len(back.Assurance.Validators) != 1 || back.Assurance.Validators[0].Pattern != `^MANIFEST-\d{8}T\d{6}Z\.txt$` {
		t.Errorf("read back %+v", back.Assurance.Validators)
	}
}

func TestCustomValidators_TwoSetScopedMatchesAreAnAmbiguityNotAGuess(t *testing.T) {
	set := model.BackupSetID{Source: "production", Set: "postgres-primary"}
	c := validatorConfig(
		CustomValidator{Name: "set-a", Pattern: `^MANIFEST-`, BackupSet: set.String()},
		CustomValidator{Name: "set-b", Pattern: `\.txt$`, BackupSet: set.String()},
		CustomValidator{Name: "global", Pattern: `^MANIFEST-`},
	)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	m := MatchCustomValidator(c.Assurance.Validators, set, "MANIFEST-20260902T153521Z.txt")
	if m.Validator != nil || len(m.Ambiguous) != 2 || m.Ambiguous[0] != "set-a" || m.Ambiguous[1] != "set-b" {
		t.Errorf("two set-scoped matches = %+v, want an ambiguity naming both and not the global one", m)
	}
}
