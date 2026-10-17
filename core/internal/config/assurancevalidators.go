package config

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	"github.com/retnd/retnd/core/internal/model"
)

// Custom recovery-test validators (#1203): an operator's NAME.local.sh
// attached to an artifact-name pattern, at a backup set's scope or the whole
// deployment's.
//
// The policy (name, pattern, scope) lives here, in the assurance block, for
// the reason every other piece of assurance policy does: it is validated with
// the rest of the file, hot-reloads with it and shows up in a config diff.
// The script itself lives in the workflow tree, in one flat stage directory
// (CustomValidatorDir), where the operator's other hooks live and where
// discovery's custody rules already apply. It cannot live in
// recovery-test/<source>/<set>: that directory holds a set's one validator
// by convention, and discovery refuses anything in a stage directory that is
// not a script, so a subdirectory there is not an option either.
//
// # Which one runs
//
// For the copy a recovery test is about to check, most specific first: a
// validator scoped to the copy's set whose pattern matches the artifact name,
// then a global one whose pattern matches, then the set's validator_id, then
// the one script in recovery-test/<source>/<set>, then the built-in structure
// check. A pattern says what the file is, which is what a validator checks;
// validator_id and the directory are set-wide catch-alls. Two patterns at one
// scope that both match a name are an ambiguity, and MatchCustomValidator
// reports it rather than choosing.

// CustomValidatorDir is the stage directory, relative to the workflow root,
// that holds every custom validator's script.
const CustomValidatorDir = "recovery-test-validators"

// MaxCustomValidators bounds how many custom validators one deployment may
// declare. It is discovery's own bound on one stage directory
// (workflow.MaxScriptsPerStage), restated here so a 65th is refused when it
// is declared and not the first time a test reads the directory.
const MaxCustomValidators = 64

// MaxCustomValidatorPatternBytes bounds a pattern's length, which keeps it
// something a person can read in a table cell and a config diff. It does NOT
// bound what the pattern costs to run: RE2 cannot backtrack, but its run time
// is still linear in the size of the compiled program, and 39 copies of
// [a-z]{0,1000} fit in 507 bytes and compile to a program that takes about
// 13 ms per match against a 255 byte name. The preview route matches every
// artifact name in scope on a request that needs only a CSRF token, so the
// cost has to be bounded where the pattern is accepted, by the next two
// constants.
const MaxCustomValidatorPatternBytes = 512

// MaxCustomValidatorRepeat is the largest repeat count a pattern may use
// ({n}, {n,m}). 255 is the longest name a backup can have, so no honest
// pattern needs more.
const MaxCustomValidatorRepeat = 255

// MaxCustomValidatorProgramSize is the most instructions a pattern may
// compile to. The patterns an operator writes are tens of instructions
// ("^MANIFEST-\\d{8}T\\d{6}Z\\.txt$" is under fifty); this leaves room for
// a few long bounded repeats and refuses the multiplied ones.
const MaxCustomValidatorProgramSize = 1000

// CustomValidator is one entry under assurance.validators.
type CustomValidator struct {
	// Name selects the script NAME.local.sh in CustomValidatorDir. It
	// follows validator_id's rules (RecoveryTestScriptName) and is unique
	// across the deployment.
	Name string `yaml:"name"`

	// Pattern is a Go RE2 regular expression matched against the artifact's
	// name (never its path).
	Pattern string `yaml:"pattern"`

	// BackupSet is "source/set" for a validator that applies to one set,
	// and empty for one that applies to every matching artifact in every
	// set.
	BackupSet string `yaml:"backup_set,omitempty"`

	// Env is minimal or workflow (#1221): the environment the validator's
	// script starts with. Empty means the entry does not say, and the set's
	// then the deployment's validator_env decides.
	Env string `yaml:"env,omitempty"`
}

// ResolvedCustomValidator is a validated entry, pattern compiled.
type ResolvedCustomValidator struct {
	Name    string
	Pattern string
	Regexp  *regexp.Regexp
	// BackupSet is the zero id for a global validator.
	BackupSet  model.BackupSetID
	ScriptFile string
	// Env is what the entry states, "" when it states nothing.
	Env string
}

// ResolveValidatorEnv is the precedence for one run (#1221): the validator's
// own env, then the set's validator_env (which already folds in the
// deployment's), then minimal.
func ResolveValidatorEnv(validatorEnv, inherited string) string {
	return ValidatorEnvMinimal
}

// Global reports whether the validator applies in every backup set.
func (v ResolvedCustomValidator) Global() bool { return v.BackupSet.IsZero() }

// Scope is "backup_set" or "global", the words the API uses.
func (v ResolvedCustomValidator) Scope() string {
	if v.Global() {
		return CustomValidatorScopeGlobal
	}
	return CustomValidatorScopeBackupSet
}

// The two scopes.
const (
	CustomValidatorScopeBackupSet = "backup_set"
	CustomValidatorScopeGlobal    = "global"
)

// AppliesTo reports whether the validator is in scope for set at all.
func (v ResolvedCustomValidator) AppliesTo(set model.BackupSetID) bool {
	return v.Global() || v.BackupSet == set
}

// CompileValidatorPattern compiles a custom validator's pattern, refusing an
// empty one, one over MaxCustomValidatorPatternBytes, and anything RE2 will
// not compile. The refusal names the pattern so it can be echoed.
func CompileValidatorPattern(p string) (*regexp.Regexp, error) {
	if strings.TrimSpace(p) == "" {
		return nil, fmt.Errorf("a validator needs a pattern to match artifact names against")
	}
	if len(p) > MaxCustomValidatorPatternBytes {
		return nil, fmt.Errorf("the pattern is %d bytes and may be at most %d", len(p), MaxCustomValidatorPatternBytes)
	}
	if !utf8.ValidString(p) {
		return nil, fmt.Errorf("the pattern is not valid UTF-8")
	}
	tree, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("%q is not a Go (RE2) regular expression: %v", p, err)
	}
	if n := largestRepeat(tree); n > MaxCustomValidatorRepeat {
		return nil, fmt.Errorf("the pattern repeats something %d times and a repeat may be at most %d, the longest name a backup can have", n, MaxCustomValidatorRepeat)
	}
	// The estimate comes first so that a pattern built to multiply
	// ((a{255}){255}) is refused before anything is expanded to measure it.
	if est := programEstimate(tree); est > MaxCustomValidatorProgramSize {
		return nil, fmt.Errorf("the pattern compiles to a program of about %d instructions and may be at most %d; it is matched against every backup name, so use fewer or smaller repeats", est, MaxCustomValidatorProgramSize)
	}
	prog, err := syntax.Compile(tree.Simplify())
	if err != nil {
		return nil, fmt.Errorf("%q is not a Go (RE2) regular expression: %v", p, err)
	}
	if n := len(prog.Inst); n > MaxCustomValidatorProgramSize {
		return nil, fmt.Errorf("the pattern compiles to a program of %d instructions and may be at most %d; it is matched against every backup name, so use fewer or smaller repeats", n, MaxCustomValidatorProgramSize)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("%q is not a Go (RE2) regular expression: %v", p, err)
	}
	return re, nil
}

// largestRepeat is the biggest explicit repeat count anywhere in the tree.
func largestRepeat(re *syntax.Regexp) int {
	n := 0
	if re.Op == syntax.OpRepeat {
		n = max(re.Min, re.Max)
	}
	for _, sub := range re.Sub {
		n = max(n, largestRepeat(sub))
	}
	return n
}

// programEstimate is an upper-ish bound on the compiled program's size that
// is cheap to compute and saturates, so a hostile pattern cannot make the
// estimate itself expensive. A repeat costs its body once per copy.
func programEstimate(re *syntax.Regexp) int {
	const ceiling = 1 << 24
	sat := func(n int) int { return min(n, ceiling) }
	total := 1
	switch re.Op {
	case syntax.OpLiteral:
		total = max(1, len(re.Rune))
	case syntax.OpRepeat:
		copies := re.Max
		if copies < 0 {
			copies = re.Min + 1
		}
		body := 0
		for _, sub := range re.Sub {
			body += programEstimate(sub)
		}
		// Each optional copy also costs a split.
		return sat((body + 1) * max(copies, 1))
	}
	for _, sub := range re.Sub {
		total = sat(total + programEstimate(sub))
	}
	return sat(total)
}

// resolveCustomValidators validates assurance.validators against the whole
// file (phase 2: it needs every set's id) and returns them compiled, in the
// order they are written.
func (c *Config) resolveCustomValidators(v *validator, list []CustomValidator) []ResolvedCustomValidator {
	if len(list) == 0 {
		return nil
	}
	const p = "assurance.validators"
	if len(list) > MaxCustomValidators {
		v.addf("%s: %d validators are declared and a deployment may have at most %d, the most one stage directory may hold", p, len(list), MaxCustomValidators)
	}
	sets := map[model.BackupSetID]bool{}
	for _, src := range c.Sources {
		for _, bs := range src.BackupSets {
			sets[bs.ID] = true
		}
	}
	// Keyed by the folded name: on a case-insensitive volume Foo.local.sh and
	// foo.local.sh are one file, and a name must mean one script everywhere.
	names := map[string]int{}
	type scoped struct {
		set     model.BackupSetID
		pattern string
	}
	patterns := map[scoped]string{}
	out := make([]ResolvedCustomValidator, 0, len(list))
	for i, cv := range list {
		path := fmt.Sprintf("%s[%d]", p, i)
		r := ResolvedCustomValidator{Name: cv.Name, Pattern: cv.Pattern}
		ok := true
		file, err := RecoveryTestScriptName(cv.Name)
		if err != nil {
			v.addf("%s.name: %v", path, err)
			ok = false
		}
		r.ScriptFile = file
		folded := strings.ToLower(cv.Name)
		if j, dup := names[folded]; dup && cv.Name != "" {
			v.addf("%s.name: %q is already the name of %s[%d] (names are compared without regard to case, since a case-insensitive volume would make them one file); each validator is one script file, so names are unique across the deployment", path, cv.Name, p, j)
			ok = false
		}
		names[folded] = i
		re, err := CompileValidatorPattern(cv.Pattern)
		if err != nil {
			v.addf("%s.pattern: %v", path, err)
			ok = false
		}
		r.Regexp = re
		if cv.BackupSet != "" {
			id, err := model.ParseBackupSetID(cv.BackupSet)
			switch {
			case err != nil:
				v.addf("%s.backup_set: %q is not a backup set id (source/name)", path, cv.BackupSet)
				ok = false
			case !sets[id]:
				v.addf("%s.backup_set: %q is not a backup set in this configuration; leave it out to apply the validator to every set", path, cv.BackupSet)
				ok = false
			default:
				r.BackupSet = id
			}
		}
		key := scoped{r.BackupSet, cv.Pattern}
		if other, dup := patterns[key]; dup && ok {
			v.addf("%s.pattern: %q is already %s's pattern at the same scope; two validators for exactly the same names is an ambiguity, not a choice", path, cv.Pattern, other)
			ok = false
		}
		patterns[key] = cv.Name
		if ok {
			out = append(out, r)
		}
	}
	return out
}

// CustomValidatorMatch is what MatchCustomValidator found.
type CustomValidatorMatch struct {
	// Validator is the one that applies, when exactly one does at the most
	// specific scope with any match.
	Validator *ResolvedCustomValidator
	// Ambiguous names every validator that matched at that scope, when more
	// than one did. Validator is nil then.
	Ambiguous []string
}

// Found reports whether a validator (or an ambiguity) was found.
func (m CustomValidatorMatch) Found() bool { return m.Validator != nil || len(m.Ambiguous) > 0 }

// MatchCustomValidator is the resolution order's first two rungs for one
// artifact: the set's own patterns, then the deployment's. The first scope
// with any match decides; a scope where more than one pattern matches is
// reported as ambiguous and never resolved by declaration order.
func MatchCustomValidator(list []ResolvedCustomValidator, set model.BackupSetID, artifactName string) CustomValidatorMatch {
	for _, global := range []bool{false, true} {
		var hits []int
		for i := range list {
			cv := list[i]
			if cv.Global() != global || (!global && cv.BackupSet != set) {
				continue
			}
			if cv.Regexp != nil && cv.Regexp.MatchString(artifactName) {
				hits = append(hits, i)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return CustomValidatorMatch{Validator: &list[hits[0]]}
		default:
			m := CustomValidatorMatch{}
			for _, i := range hits {
				m.Ambiguous = append(m.Ambiguous, list[i].Name)
			}
			return m
		}
	}
	return CustomValidatorMatch{}
}

// DropCustomValidatorsFor removes every validator scoped to set from the
// block and returns their names, so removing a backup set does not leave a
// configuration that refuses to load. Global validators are untouched.
func (b *AssuranceBlock) DropCustomValidatorsFor(set string) []string {
	if b == nil || len(b.Validators) == 0 {
		return nil
	}
	var dropped []string
	kept := b.Validators[:0:0]
	for _, cv := range b.Validators {
		if cv.BackupSet == set {
			dropped = append(dropped, cv.Name)
			continue
		}
		kept = append(kept, cv)
	}
	if len(kept) == 0 {
		kept = nil
	}
	b.Validators = kept
	return dropped
}

// IsZero reports whether the block names nothing at all, so a writer can
// remove it rather than write an empty one.
func (b AssuranceBlock) IsZero() bool {
	return len(b.Validators) == 0 && b.Scrub == (AssuranceScrub{}) && b.RecoveryTest == (AssuranceRecoveryTest{}) &&
		b.StagingRoot == "" && b.StagingTTL == 0 && b.MaxConcurrentFullReads == 0 && b.MaxPerDestination == 0
}
