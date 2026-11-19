package config

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/retnd/retnd/core/internal/model"
)

// The pure half of the custom validators' reads (#1218): matching patterns
// against artifact names, finding the names two validators share, and
// suggesting a pattern for a name. Nothing here reads a journal, a file or a
// configuration, so each rule can be tested with a slice of strings.
//
// A caller hands in the names it already has, newest first, as SetNames, and
// every scan stops with the context's error as soon as the context is done.
// The patterns are bounded where they are accepted (CompileValidatorPattern),
// so the cost of one scan is the number of names, not the shape of a pattern.

// SetNames is one backup set's artifact names, newest first.
type SetNames struct {
	Set   model.BackupSetID
	Names []string
}

// scanCheckEvery is how many names a scan matches between looks at the
// context.
const scanCheckEvery = 0x100

func scanDone(ctx context.Context, i int) error {
	if i&(scanCheckEvery-1) == 0 {
		return ctx.Err()
	}
	return nil
}

// ValidatorMatches is what one validator's pattern matched in the scope.
type ValidatorMatches struct {
	// Count is how many names matched, Sets the sets they are in, and Example
	// the newest of them.
	Count   int
	Sets    []string
	Example string
}

// ScanValidatorMatches counts the names cv's pattern matches in the sets cv
// applies to.
func ScanValidatorMatches(ctx context.Context, cv ResolvedCustomValidator, scope []SetNames) (ValidatorMatches, error) {
	out := ValidatorMatches{Sets: []string{}}
	for _, sn := range scope {
		if !cv.AppliesTo(sn.Set) {
			continue
		}
		n := 0
		for i, name := range sn.Names {
			if err := scanDone(ctx, i); err != nil {
				return ValidatorMatches{}, err
			}
			if cv.Regexp.MatchString(name) {
				if out.Example == "" {
					out.Example = name
				}
				n++
			}
		}
		if n > 0 {
			out.Count += n
			out.Sets = append(out.Sets, sn.Set.String())
		}
	}
	return out, nil
}

// PreviewMatches is what a pattern that is not saved yet would match.
type PreviewMatches struct {
	Count int
	Sets  []string
	// Sample is up to the requested number of matching names, each as
	// source/set/name.
	Sample []string
}

// ScanPreview matches re against every name in the scope.
func ScanPreview(ctx context.Context, re *regexp.Regexp, scope []SetNames, maxSample int) (PreviewMatches, error) {
	out := PreviewMatches{Sets: []string{}, Sample: []string{}}
	for _, sn := range scope {
		n := 0
		for i, name := range sn.Names {
			if err := scanDone(ctx, i); err != nil {
				return PreviewMatches{}, err
			}
			if re.MatchString(name) {
				n++
				if len(out.Sample) < maxSample {
					out.Sample = append(out.Sample, sn.Set.String()+"/"+name)
				}
			}
		}
		if n > 0 {
			out.Count += n
			out.Sets = append(out.Sets, sn.Set.String())
		}
	}
	return out, nil
}

// OrderValidatorsFor is list in the order a test of a set tries it: the set's
// own validators first, then the global ones. With only the zero id every set's
// validators are kept. Declaration order is kept within each scope.
func OrderValidatorsFor(list []ResolvedCustomValidator, only model.BackupSetID) []ResolvedCustomValidator {
	out := make([]ResolvedCustomValidator, 0, len(list))
	for _, global := range []bool{false, true} {
		for _, cv := range list {
			if cv.Global() == global && (only.IsZero() || cv.AppliesTo(only)) {
				out = append(out, cv)
			}
		}
	}
	return out
}

// Ambiguity is one backup that more than one validator's pattern matches at
// the same scope.
type Ambiguity struct {
	Set        string
	Validators []string
	Example    string
}

// FindAmbiguities finds the names in scope that more than one validator
// matches at one scope, one entry per (set, validators), with the newest name
// as the example. A recovery test of such a name refuses to choose.
func FindAmbiguities(ctx context.Context, list []ResolvedCustomValidator, scope []SetNames) ([]Ambiguity, error) {
	out := []Ambiguity{}
	if len(list) < 2 {
		return out, nil
	}
	seen := map[string]bool{}
	for _, sn := range scope {
		for i, name := range sn.Names {
			if err := scanDone(ctx, i); err != nil {
				return nil, err
			}
			m := MatchCustomValidator(list, sn.Set, name)
			if len(m.Ambiguous) == 0 {
				continue
			}
			key := sn.Set.String() + "|" + strings.Join(m.Ambiguous, ",")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Ambiguity{Set: sn.Set.String(), Validators: m.Ambiguous, Example: sn.Set.String() + "/" + name})
		}
	}
	return out, nil
}

// Collision is a validator at the same scope that already matches a name a
// new pattern also matches.
type Collision struct {
	Validator string
	Example   string
}

// FindCollisions finds the validators in list at set's scope that would share
// a name with re. skip is the name being replaced.
func FindCollisions(ctx context.Context, list []ResolvedCustomValidator, re *regexp.Regexp, set model.BackupSetID, skip string, scope []SetNames) ([]Collision, error) {
	var out []Collision
	seen := map[string]bool{}
	for _, other := range list {
		if other.Name == skip || other.BackupSet != set || seen[other.Name] {
			continue
		}
		for _, sn := range scope {
			if !set.IsZero() && sn.Set != set {
				continue
			}
			for i, name := range sn.Names {
				if err := scanDone(ctx, i); err != nil {
					return nil, err
				}
				if re.MatchString(name) && other.Regexp.MatchString(name) {
					out = append(out, Collision{Validator: other.Name, Example: sn.Set.String() + "/" + name})
					seen[other.Name] = true
					break
				}
			}
			if seen[other.Name] {
				break
			}
		}
	}
	return out, nil
}

// PatternSuggestion is one suggested pattern.
type PatternSuggestion struct {
	Pattern string
	Example string
	Matches int
	Sets    []string
	// Validator is the validator in list whose pattern is exactly this one at
	// the requested scope, "" when none is.
	Validator string
}

// SuggestPatterns suggests a pattern for every shape of name in scope, most
// common first and then by pattern, at most limit of them. list marks the
// suggestions a validator at set's scope already uses.
func SuggestPatterns(ctx context.Context, list []ResolvedCustomValidator, set model.BackupSetID, scope []SetNames, limit int) ([]PatternSuggestion, error) {
	type acc struct {
		p    PatternSuggestion
		sets map[string]bool
	}
	byPattern := map[string]*acc{}
	for _, sn := range scope {
		for i, name := range sn.Names {
			if err := scanDone(ctx, i); err != nil {
				return nil, err
			}
			p := SuggestArtifactPattern(name)
			a, ok := byPattern[p]
			if !ok {
				a = &acc{p: PatternSuggestion{Pattern: p, Example: name}, sets: map[string]bool{}}
				byPattern[p] = a
			}
			a.p.Matches++
			a.sets[sn.Set.String()] = true
		}
	}
	out := make([]PatternSuggestion, 0, len(byPattern))
	for _, a := range byPattern {
		a.p.Sets = make([]string, 0, len(a.sets))
		for s := range a.sets {
			a.p.Sets = append(a.p.Sets, s)
		}
		sort.Strings(a.p.Sets)
		for _, cv := range list {
			if cv.Pattern == a.p.Pattern && cv.BackupSet == set {
				a.p.Validator = cv.Name
			}
		}
		out = append(out, a.p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Matches != out[j].Matches {
			return out[i].Matches > out[j].Matches
		}
		return out[i].Pattern < out[j].Pattern
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// digitRun is a maximal run of ASCII digits, which \d means in RE2.
var digitRun = regexp.MustCompile(`[0-9]+`)

// SuggestArtifactPattern generalises an artifact name into the anchored RE2
// pattern an operator would otherwise write by hand: every run of digits
// becomes \d{n} and everything else is matched literally, so
// MANIFEST-20260902T153521Z.txt becomes ^MANIFEST-\d{8}T\d{6}Z\.txt$ and
// db-2026-09-02.sql.gz becomes ^db-\d{4}-\d{2}-\d{2}\.sql\.gz$.
func SuggestArtifactPattern(name string) string {
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, loc := range digitRun.FindAllStringIndex(name, -1) {
		b.WriteString(regexp.QuoteMeta(name[last:loc[0]]))
		fmt.Fprintf(&b, `\d{%d}`, loc[1]-loc[0])
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(name[last:]))
	b.WriteString("$")
	return b.String()
}

// PatternAnchored says p begins with ^ and ends with $, so it matches whole
// names. An unanchored pattern is allowed and matches anywhere in the name; a
// surface may want to say so.
func PatternAnchored(p string) bool {
	return strings.HasPrefix(p, "^") && strings.HasSuffix(p, "$") && !strings.HasSuffix(p, `\$`)
}
