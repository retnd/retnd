package config

import (
	"regexp/syntax"
	"strings"
	"testing"
	"time"
)

// #1203 review: what a pattern may cost, and which names count as one name.

// 39 copies of a bounded repeat is 507 bytes, inside the 512 byte cap, and
// compiles to a program that costs on the order of 13 ms per match against a
// 255 byte name. The byte cap never bounded that.
func TestCompileValidatorPattern_RefusesARepeatPastTheBound(t *testing.T) {
	huge := strings.Repeat(`[a-z]{0,1000}`, 39)
	if len(huge) > MaxCustomValidatorPatternBytes {
		t.Fatalf("the fixture is %d bytes; it has to fit the byte cap or it proves nothing", len(huge))
	}
	if _, err := CompileValidatorPattern(huge); err == nil || !strings.Contains(err.Error(), "repeat") {
		t.Errorf("a {0,1000} repeat was accepted or refused for another reason: %v", err)
	}
	if _, err := CompileValidatorPattern(`^[a-z]{0,300}$`); err == nil {
		t.Error("a repeat count above the bound was accepted")
	}
}

func TestCompileValidatorPattern_RefusesAProgramPastTheBound(t *testing.T) {
	// Every repeat is inside its own bound; together they are 4000 instructions.
	many := "^" + strings.Repeat(`[a-z]{0,100}`, 38) + "$"
	_, err := CompileValidatorPattern(many)
	if err == nil || !strings.Contains(err.Error(), "program") {
		t.Fatalf("a %d byte pattern that compiles to thousands of instructions was accepted: %v", len(many), err)
	}
	// And the nested form, which multiplies rather than adds.
	if _, err := CompileValidatorPattern(`^(?:(?:[a-z]{1,50}){1,50}){1,50}$`); err == nil {
		t.Error("a nested repeat was accepted")
	}
}

func TestCompileValidatorPattern_StillAcceptsWhatOperatorsWrite(t *testing.T) {
	for _, p := range []string{
		`^MANIFEST-\d{8}T\d{6}Z\.txt$`,
		`^db-\d{4}-\d{2}-\d{2}\.sql\.(?:gz|zst)$`,
		`\.sql\.zst$`,
		`^.{1,255}$`,
		`(?i)^backup-[a-z0-9]{1,64}\.tar$`,
	} {
		if _, err := CompileValidatorPattern(p); err != nil {
			t.Errorf("%q was refused: %v", p, err)
		}
	}
}

// Whatever is accepted is cheap: its program is inside the bound, so matching
// a 255 byte name is a small number of steps. This does not time the match
// against a tight limit, because -race and a loaded host make that a flaky
// test of the machine; it checks the program size, which is what the cost is
// proportional to, and keeps a generous wall-clock limit only to catch an
// order of magnitude going missing.
func TestCompileValidatorPattern_AnAcceptedPatternIsCheapToMatch(t *testing.T) {
	name := strings.Repeat("a", 255)
	for _, p := range []string{`^[a-z]{0,255}$`, `^(?:[a-z]{0,10}){0,10}$`, `^[a-z]{0,100}[a-z]{0,100}[a-z]{0,50}$`} {
		re, err := CompileValidatorPattern(p)
		if err != nil {
			continue // refused is fine: it is bounded either way
		}
		prog, perr := syntax.Compile(mustParse(t, p).Simplify())
		if perr != nil || len(prog.Inst) > MaxCustomValidatorProgramSize {
			t.Errorf("%q was accepted with a program of %d instructions (%v)", p, len(prog.Inst), perr)
		}
		start := time.Now()
		for i := 0; i < 20; i++ {
			re.MatchString(name)
		}
		if per := time.Since(start) / 20; per > 100*time.Millisecond {
			t.Errorf("%q costs %s per match against a 255 byte name", p, per)
		}
	}
}

func mustParse(t *testing.T, p string) *syntax.Regexp {
	t.Helper()
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

// On a case-insensitive volume Foo.local.sh and foo.local.sh are one file.
func TestCustomValidators_NamesThatDifferOnlyByCaseAreOneName(t *testing.T) {
	err := validatorConfig(
		CustomValidator{Name: "Foo", Pattern: "^a"},
		CustomValidator{Name: "foo", Pattern: "^b"},
	).Validate()
	if err == nil || !strings.Contains(err.Error(), "names are unique") {
		t.Errorf("err = %v, want a refusal for Foo vs foo", err)
	}
}
