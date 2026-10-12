package obs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EPIC Q's panel found a family of recovery events that were written, tested
// and never emitted (C10, C13): obs.RecoveryStage had no caller outside obs,
// so the stage line the CLI's --follow and the browser render did not exist.
// This holds the family to the rule that makes the catalog true: every
// exported method of Logger declared in assurance_events.go has a call site in
// a non-test file outside this package.

func assuranceEmitters(t *testing.T, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || !fd.Name.IsExported() {
			continue
		}
		star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if id, ok := star.X.(*ast.Ident); ok && id.Name == "Logger" {
			out = append(out, fd.Name.Name)
		}
	}
	sort.Strings(out)
	return out
}

// uncalled returns the emitters no non-test file under root (outside skipDir)
// selects.
func uncalled(t *testing.T, root, skipDir string, emitters []string) []string {
	t.Helper()
	want := map[string]bool{}
	for _, e := range emitters {
		want[e] = true
	}
	seen := map[string]bool{}
	skip, _ := filepath.Abs(skipDir)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if abs, _ := filepath.Abs(p); abs == skip || d.Name() == "node_modules" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && want[sel.Sel.Name] {
				seen[sel.Sel.Name] = true
			}
			return true
		})
		return nil
	})
	var out []string
	for _, e := range emitters {
		if !seen[e] {
			out = append(out, e)
		}
	}
	return out
}

func TestEveryAssuranceEventHasAProductionEmitter(t *testing.T) {
	emitters := assuranceEmitters(t, "assurance_events.go")
	if len(emitters) == 0 {
		t.Fatal("read no Logger methods out of assurance_events.go: the guard is looking at nothing")
	}
	if got := uncalled(t, filepath.Join("..", ".."), ".", emitters); len(got) != 0 {
		t.Fatalf("events nothing emits: %v. A catalog entry with no emitter is a promise the product does not keep; call it or delete it", got)
	}
}

func TestTheAssuranceEmitterGuardCanFail(t *testing.T) {
	tree := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(tree, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/obs/e.go", "package obs\n\ntype Logger struct{}\n\nfunc (l *Logger) Stage()   {}\nfunc (l *Logger) Dormant() {}\nfunc (l *Logger) hidden()  {}\n")
	emitters := assuranceEmitters(t, filepath.Join(tree, "internal/obs/e.go"))
	if strings.Join(emitters, ",") != "Dormant,Stage" {
		t.Fatalf("emitters = %v", emitters)
	}
	obsDir := filepath.Join(tree, "internal", "obs")
	write("internal/app/a_test.go", "package app\n\nfunc f(l interface{ Stage() }) { l.Stage() }\n")
	if got := uncalled(t, tree, obsDir, emitters); strings.Join(got, ",") != "Dormant,Stage" {
		t.Errorf("a test-only call counted: uncalled = %v", got)
	}
	write("internal/app/a.go", "package app\n\nfunc f(l interface{ Stage() }) { l.Stage() }\n")
	if got := uncalled(t, tree, obsDir, emitters); strings.Join(got, ",") != "Dormant" {
		t.Errorf("uncalled = %v, want only Dormant", got)
	}
}

// The scrub and recovery-test events EPIC Q added to events.go sit outside
// assurance_events.go, so the guard above never looked at them. They are held
// to the same rule by name, and the test fails if one of them is renamed or
// deleted, so a rename cannot make it pass by looking at nothing.
func TestTheScrubAndRecoveryTestEventsHaveAProductionEmitterToo(t *testing.T) {
	want := []string{"RecoveryTest", "ScrubFailure", "ScrubPass"}
	declared := map[string]bool{}
	for _, name := range assuranceEmitters(t, "events.go") {
		declared[name] = true
	}
	for _, name := range want {
		if !declared[name] {
			t.Fatalf("events.go no longer declares Logger.%s: update this guard with the rename, do not let it look at nothing", name)
		}
	}
	if got := uncalled(t, filepath.Join("..", ".."), ".", want); len(got) != 0 {
		t.Fatalf("events nothing emits: %v. A catalog entry with no emitter is a promise the product does not keep; call it or delete it", got)
	}
}
