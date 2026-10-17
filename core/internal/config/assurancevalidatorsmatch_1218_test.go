package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/retnd/retnd/core/internal/model"
)

// #1218: the matching rules and the suggestions, tested with names and
// patterns and nothing else. No service, no journal, no files.

var (
	setA = model.BackupSetID{Source: "production", Set: "alpha"}
	setB = model.BackupSetID{Source: "production", Set: "beta"}
)

func resolved(t *testing.T, name, pattern string, set model.BackupSetID) ResolvedCustomValidator {
	t.Helper()
	re, err := CompileValidatorPattern(pattern)
	if err != nil {
		t.Fatalf("pattern %q: %v", pattern, err)
	}
	file, err := RecoveryTestScriptName(name)
	if err != nil {
		t.Fatal(err)
	}
	return ResolvedCustomValidator{Name: name, Pattern: pattern, Regexp: re, BackupSet: set, ScriptFile: file}
}

func TestSuggestArtifactPattern_GeneralisesDigitRuns(t *testing.T) {
	for name, want := range map[string]string{
		"MANIFEST-20260902T153521Z.txt": `^MANIFEST-\d{8}T\d{6}Z\.txt$`,
		"db-2026-09-02.sql.gz":          `^db-\d{4}-\d{2}-\d{2}\.sql\.gz$`,
		"plain.tar":                     `^plain\.tar$`,
		"a+b(1).zip":                    `^a\+b\(\d{1}\)\.zip$`,
	} {
		if got := SuggestArtifactPattern(name); got != want {
			t.Errorf("SuggestArtifactPattern(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPatternAnchored(t *testing.T) {
	for p, want := range map[string]bool{
		`^a$`: true, `^a`: false, `a$`: false, `a`: false, `^a\$`: false, ``: false,
	} {
		if got := PatternAnchored(p); got != want {
			t.Errorf("PatternAnchored(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestOrderValidatorsFor_SetScopedFirstThenGlobalAndOnlyWhatApplies(t *testing.T) {
	g1 := resolved(t, "g1", `\.a$`, model.BackupSetID{})
	a1 := resolved(t, "a1", `\.b$`, setA)
	b1 := resolved(t, "b1", `\.c$`, setB)
	g2 := resolved(t, "g2", `\.d$`, model.BackupSetID{})
	list := []ResolvedCustomValidator{g1, a1, b1, g2}

	names := func(l []ResolvedCustomValidator) []string {
		out := []string{}
		for _, v := range l {
			out = append(out, v.Name)
		}
		return out
	}
	if got, want := names(OrderValidatorsFor(list, model.BackupSetID{})), []string{"a1", "b1", "g1", "g2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("no set: %v, want %v", got, want)
	}
	if got, want := names(OrderValidatorsFor(list, setA)), []string{"a1", "g1", "g2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("set A: %v, want %v", got, want)
	}
}

func TestScanValidatorMatches_CountsPerSetAndKeepsTheNewestAsTheExample(t *testing.T) {
	cv := resolved(t, "dumps", `\.sql$`, model.BackupSetID{})
	scope := []SetNames{
		{Set: setA, Names: []string{"new.sql", "x.txt", "old.sql"}},
		{Set: setB, Names: []string{"y.txt"}},
	}
	got, err := ScanValidatorMatches(context.Background(), cv, scope)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 || got.Example != "new.sql" || !reflect.DeepEqual(got.Sets, []string{"production/alpha"}) {
		t.Errorf("matches = %+v", got)
	}

	// A set-scoped validator ignores the sets it is not scoped to.
	scoped := resolved(t, "scoped", `\.sql$`, setB)
	got, err = ScanValidatorMatches(context.Background(), scoped, []SetNames{{Set: setA, Names: []string{"a.sql"}}, {Set: setB, Names: []string{"b.sql"}}})
	if err != nil || got.Count != 1 || got.Example != "b.sql" {
		t.Errorf("scoped matches = %+v, %v", got, err)
	}
}

func TestScanPreview_SamplesAreBoundedAndNamedBySet(t *testing.T) {
	re, _ := CompileValidatorPattern(`\.sql$`)
	names := make([]string, 30)
	for i := range names {
		names[i] = fmt.Sprintf("n%02d.sql", i)
	}
	got, err := ScanPreview(context.Background(), re, []SetNames{{Set: setA, Names: names}}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 30 || len(got.Sample) != 5 || got.Sample[0] != "production/alpha/n00.sql" || !reflect.DeepEqual(got.Sets, []string{"production/alpha"}) {
		t.Errorf("preview = %+v", got)
	}
}

func TestFindAmbiguities_OnePerSetAndPairWithTheNewestAsExample(t *testing.T) {
	a := resolved(t, "a", `^db-`, setA)
	b := resolved(t, "b", `\.sql$`, setA)
	list := []ResolvedCustomValidator{a, b}
	scope := []SetNames{{Set: setA, Names: []string{"db-2.sql", "db-1.sql", "other.sql"}}}

	got, err := FindAmbiguities(context.Background(), list, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Set != "production/alpha" || !reflect.DeepEqual(got[0].Validators, []string{"a", "b"}) || got[0].Example != "production/alpha/db-2.sql" {
		t.Errorf("ambiguities = %+v", got)
	}
	if got, _ := FindAmbiguities(context.Background(), list[:1], scope); len(got) != 0 {
		t.Errorf("a single validator cannot be ambiguous: %+v", got)
	}
}

func TestFindCollisions_OnlyAtTheSameScopeAndNeverWithItself(t *testing.T) {
	other := resolved(t, "other", `\.sql$`, setA)
	global := resolved(t, "global", `\.sql$`, model.BackupSetID{})
	list := []ResolvedCustomValidator{other, global}
	scope := []SetNames{{Set: setA, Names: []string{"a.sql"}}}
	re, _ := CompileValidatorPattern(`^a`)

	got, err := FindCollisions(context.Background(), list, re, setA, "", scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Validator != "other" || got[0].Example != "production/alpha/a.sql" {
		t.Errorf("collisions = %+v", got)
	}
	if got, _ := FindCollisions(context.Background(), list, re, setA, "other", scope); len(got) != 0 {
		t.Errorf("a validator collided with the one it replaces: %+v", got)
	}
	if got, _ := FindCollisions(context.Background(), list, re, model.BackupSetID{}, "", scope); len(got) != 1 || got[0].Validator != "global" {
		t.Errorf("global scope collisions = %+v", got)
	}
}

func TestSuggestPatterns_MostCommonFirstBoundedAndMarkedWhenDeclared(t *testing.T) {
	declared := resolved(t, "manifest", `^MANIFEST-\d{8}\.txt$`, setA)
	scope := []SetNames{{Set: setA, Names: []string{"MANIFEST-20260902.txt", "MANIFEST-20260903.txt", "db-1.sql"}}, {Set: setB, Names: []string{"db-2.sql"}}}

	got, err := SuggestPatterns(context.Background(), []ResolvedCustomValidator{declared}, setA, scope, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("suggestions = %+v", got)
	}
	if top := got[0]; top.Pattern != `^MANIFEST-\d{8}\.txt$` || top.Matches != 2 || top.Validator != "manifest" || top.Example != "MANIFEST-20260902.txt" {
		t.Errorf("top = %+v", top)
	}
	if got[1].Validator != "" || !reflect.DeepEqual(got[1].Sets, []string{"production/alpha", "production/beta"}) {
		t.Errorf("second = %+v", got[1])
	}
	if one, _ := SuggestPatterns(context.Background(), nil, model.BackupSetID{}, scope, 1); len(one) != 1 {
		t.Errorf("the limit was not applied: %+v", one)
	}
}

func TestScans_StopWhenTheContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cv := resolved(t, "a", `x`, model.BackupSetID{})
	scope := []SetNames{{Set: setA, Names: []string{"x"}}}
	if _, err := ScanValidatorMatches(ctx, cv, scope); !errors.Is(err, context.Canceled) {
		t.Errorf("ScanValidatorMatches = %v", err)
	}
	if _, err := ScanPreview(ctx, cv.Regexp, scope, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("ScanPreview = %v", err)
	}
	if _, err := FindAmbiguities(ctx, []ResolvedCustomValidator{cv, resolved(t, "b", `x`, model.BackupSetID{})}, scope); !errors.Is(err, context.Canceled) {
		t.Errorf("FindAmbiguities = %v", err)
	}
	if _, err := SuggestPatterns(ctx, nil, model.BackupSetID{}, scope, 5); !errors.Is(err, context.Canceled) {
		t.Errorf("SuggestPatterns = %v", err)
	}
}
