package apicontract

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The two request fields whose VALUE picks a code path, held to one list
// each (#1253's review).
//
// core/cliecho's canary corpus has to send every value of these two
// fields, because a body whose selector is a canary string reaches no arm
// of the switch behind it. It used to carry its own copies of both
// vocabularies, and the action copy went stale for months: the three
// snapshot actions from #788 were never added, so nothing ever checked
// what their arms put on a command line. The corpus now reads Actions and
// RecoveryTargetKinds from here, and these tests are what make the two
// slices complete: a constant left out of its slice fails, and so does a
// kind the contract document has and this package doesn't.

// constsAndSlices reads this package's hand-written sources and returns
// every string constant (name to value) and, for each package-level
// []string var, the constant names its literal lists.
func constsAndSlices(t *testing.T) (consts map[string]string, slicesByName map[string][]string) {
	t.Helper()
	consts, slicesByName = map[string]string{}, map[string][]string{}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".gen.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					switch v := vs.Values[i].(type) {
					case *ast.BasicLit:
						if gen.Tok == token.CONST && v.Kind == token.STRING {
							if s, err := strconv.Unquote(v.Value); err == nil {
								consts[name.Name] = s
							}
						}
					case *ast.CompositeLit:
						if gen.Tok != token.VAR {
							continue
						}
						var elems []string
						for _, e := range v.Elts {
							if id, ok := e.(*ast.Ident); ok {
								elems = append(elems, id.Name)
							} else {
								elems = append(elems, "<not a constant>")
							}
						}
						slicesByName[name.Name] = elems
					}
				}
			}
		}
	}
	return consts, slicesByName
}

func TestActionsListsEveryActionConstant(t *testing.T) {
	consts, slicesByName := constsAndSlices(t)
	listed, ok := slicesByName["Actions"]
	if !ok {
		t.Fatal("apicontract has no Actions slice, so core/cliecho's corpus has to keep its own copy of the action names, which is the copy that went stale")
	}
	var want []string
	for name := range consts {
		if strings.HasPrefix(name, "Action") {
			want = append(want, name)
		}
	}
	sort.Strings(want)
	got := slices.Clone(listed)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("Actions lists %v and the Action constants are %v. A constant missing from the slice is an action the corpus never sends.", got, want)
	}
}

// openAPIEnum reads one property's enum out of api/v1/openapi.json.
func openAPIEnum(t *testing.T, schema, property string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "v1", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	enum := doc.Components.Schemas[schema].Properties[property].Enum
	if len(enum) == 0 {
		t.Fatalf("api/v1/openapi.json declares no enum for %s.%s", schema, property)
	}
	return enum
}

func TestRecoveryTargetKindsAreTheContractsEnum(t *testing.T) {
	consts, slicesByName := constsAndSlices(t)
	listed, ok := slicesByName["RecoveryTargetKinds"]
	if !ok {
		t.Fatal("apicontract has no RecoveryTargetKinds slice, so the recover builders and the canary corpus each spell the kinds out as literals")
	}
	var got []string
	for _, name := range listed {
		got = append(got, consts[name])
	}
	want := openAPIEnum(t, "RecoveryTarget", "kind")
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("RecoveryTargetKinds is %v and the contract's RecoveryTarget.kind enum is %v", got, want)
	}
}
