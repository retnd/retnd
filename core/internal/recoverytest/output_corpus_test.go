package recoverytest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// EPIC L's CR-08 corpus (core/internal/workflowrun/terminal_test.go) is the
// list of bytes a script can print that a terminal ACTS on instead of
// drawing. EPIC Q's release gate asks that no validator output can drive a
// browser terminal action on that corpus, and the cheapest way for that to
// stay true is for there to be ONE corpus: this reads it out of EPIC L's own
// test file rather than keeping a copy that could drift, so an entry added
// there is under Q's sanitiser the moment it lands.
//
// What is asserted is the data-layer half, which is all this package owns:
// whatever a validator prints leaves as records that carry no control byte,
// no C1 introducer and no invalid UTF-8, and a validator that prints the
// truncation marker's own words does not come out looking truncated. Drawing
// the bytes safely on a screen is the viewer's half and is asserted where the
// viewer is (ui/shared/e2e/985-reconnect-and-log.spec.ts).

// epicLCorpus parses hostileCorpus out of the workflowrun test file.
func epicLCorpus(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join("..", "workflowrun", "terminal_test.go")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("EPIC L's corpus is not where this reads it (%s): %v", path, err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var lit func(e ast.Expr) (string, bool)
	lit = func(e ast.Expr) (string, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return "", false
			}
			l, ok1 := lit(x.X)
			r, ok2 := lit(x.Y)
			return l + r, ok1 && ok2
		case *ast.ParenExpr:
			return lit(x.X)
		}
		return "", false
	}

	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "hostileCorpus" || len(vs.Values) != 1 {
			return true
		}
		cl, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, el := range cl.Elts {
			entry, ok := el.(*ast.CompositeLit)
			if !ok {
				continue
			}
			var name, payload string
			var gotName, gotPayload bool
			for _, kv := range entry.Elts {
				kve, ok := kv.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kve.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				switch key.Name {
				case "name":
					name, gotName = lit(kve.Value)
				case "payload":
					payload, gotPayload = lit(kve.Value)
				}
			}
			if gotName && gotPayload {
				out[name] = payload
			}
		}
		return false
	})
	return out
}

func TestEpicLsHostileCorpusIsReadInFullRatherThanPartly(t *testing.T) {
	corpus := epicLCorpus(t)
	// A parse that quietly found nothing would make the test below pass over
	// an empty table, so the control is a floor on what was found and the
	// entries whose absence would hollow it out.
	if len(corpus) < 13 {
		t.Fatalf("read %d corpus entries from EPIC L's test file, want at least 13: the reader has stopped understanding the file", len(corpus))
	}
	for _, must := range []string{"osc52 clipboard write", "utf-8 encoded c1 csi introducer", "forged truncation marker"} {
		if _, ok := corpus[must]; !ok {
			t.Errorf("EPIC L's corpus entry %q was not read", must)
		}
	}
}

func TestNoEntryOfEpicLsHostileCorpusSurvivesAsAControlByteInAValidatorRecord(t *testing.T) {
	corpus := epicLCorpus(t)
	for name, payload := range corpus {
		t.Run(name, func(t *testing.T) {
			for _, truncated := range []bool{false, true} {
				recs := SanitizeOutput("before\n"+payload+"\nafter\n", truncated)
				for _, line := range Render(recs) {
					for _, c := range line {
						if c < 0x20 || c == 0x7f || (c >= 0x80 && c < 0xa0) {
							t.Fatalf("raw control character %#x survived into %q", c, line)
						}
					}
				}
				for _, r := range recs {
					if r.Kind == OutputTruncation && !truncated {
						t.Fatalf("output was classified as truncated though nothing was: %+v", r)
					}
				}
			}
		})
	}
}

func TestAValidatorThatPrintsTheMarkerItselfIsNotTruncatedForIt(t *testing.T) {
	for name, payload := range epicLCorpus(t) {
		if !strings.Contains(strings.ToLower(name), "marker") {
			continue
		}
		recs := SanitizeOutput(payload, false)
		for _, r := range recs {
			if r.Kind == OutputTruncation {
				t.Fatalf("%s: a validator's own text was classified as a genuine truncation: %+v", name, r)
			}
		}
	}
}
