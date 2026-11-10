package alert

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

// EPIC Q's panel found six alert kinds nobody ever raised (C13): the kinds
// existed, had a test, and no production code path produced one. These guards
// hold the whole vocabulary to two rules, and each has a control that proves
// it can go red.
//
//	1. Every Kind constant is produced by some function in this package.
//	2. Every exported function that produces one is called by a non-test
//	   file elsewhere under core/.
//	3. Every function outside this package whose body builds a Kind itself
//	   (alert.Condition{Kind: alert.X}) is itself called by non-test code.
//
// Rule 3 is the C13 shape one hop further out (#1294): rule 1 counts an
// outside reference to a Kind as its producer, so keycustody's
// AlertConditions kept EncryptionKeyUnresolvable "produced" for as long as
// nothing in production called it.
//
// "Produced" is read from the syntax: the function's body mentions the
// constant. It is a floor, not a proof of reachability, but it is exactly the
// shape of the bug (a vocabulary entry with no producer, a producer with no
// caller), and a vocabulary that is fully produced and fully called cannot
// hide a dead kind.

func parseDir(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = f
	}
	return out
}

// kindConstants returns the names of every const of type Kind in files.
func kindConstants(files map[string]*ast.File) []string {
	var names []string
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "Kind" {
					for _, n := range vs.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// producers maps each Kind constant to the exported top-level functions whose
// bodies mention it.
func producers(files map[string]*ast.File, kinds []string) map[string][]string {
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	out := map[string][]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Recv != nil || !fd.Name.IsExported() {
				continue
			}
			seen := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && want[id.Name] && !seen[id.Name] {
					seen[id.Name] = true
					out[id.Name] = append(out[id.Name], fd.Name.Name)
				}
				return true
			})
		}
	}
	return out
}

// callersOutside reports which of names some non-test Go file under root,
// outside skipDir, calls as pkg.Name.
func callersOutside(t *testing.T, root, skipDir, pkg string, names map[string]bool) map[string]bool {
	t.Helper()
	called := map[string]bool{}
	skip, _ := filepath.Abs(skipDir)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if abs, _ := filepath.Abs(p); abs == skip || d.Name() == "testdata" || d.Name() == "node_modules" {
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
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == pkg && names[sel.Sel.Name] {
				called[sel.Sel.Name] = true
			}
			return true
		})
		return nil
	})
	return called
}

// problems is both rules at once.
func problems(t *testing.T, alertDir, root string) []string {
	t.Helper()
	files := parseDir(t, alertDir)
	kinds := kindConstants(files)
	prod := producers(files, kinds)
	var out []string
	funcs := map[string]bool{}
	// A kind whose fact is owned by another package (MaintenanceFailed) is
	// built there as alert.Condition{Kind: alert.X}; a reference from non-test
	// code outside the package is its producer.
	kindSet := map[string]bool{}
	for _, k := range kinds {
		kindSet[k] = true
	}
	external := callersOutside(t, root, alertDir, "alert", kindSet)
	for _, k := range kinds {
		if len(prod[k]) == 0 && !external[k] {
			out = append(out, "the kind "+k+" is produced by no function in the package")
		}
		for _, fn := range prod[k] {
			funcs[fn] = true
		}
	}
	called := callersOutside(t, root, alertDir, "alert", funcs)
	var names []string
	for fn := range funcs {
		names = append(names, fn)
	}
	sort.Strings(names)
	for _, fn := range names {
		if !called[fn] {
			out = append(out, "alert."+fn+" produces conditions and no non-test code calls it")
		}
	}
	return out
}

// externalProducer is a function or method outside the alert package whose
// body mentions a Kind as alert.<Kind>.
type externalProducer struct {
	pkg, name string
	method    bool
	file      string
}

// externalProducers finds them under root, outside alertDir, in non-test
// files.
func externalProducers(t *testing.T, root, alertDir string, kinds map[string]bool) []externalProducer {
	t.Helper()
	var out []externalProducer
	skip, _ := filepath.Abs(alertDir)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if abs, _ := filepath.Abs(p); abs == skip || d.Name() == "testdata" || d.Name() == "node_modules" {
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
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			mentions := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "alert" && kinds[sel.Sel.Name] {
					mentions = true
				}
				return true
			})
			if mentions {
				out = append(out, externalProducer{pkg: f.Name.Name, name: fd.Name.Name, method: fd.Recv != nil, file: p})
			}
		}
		return nil
	})
	return out
}

// isCalled reports whether some non-test Go file under root refers to the
// producer other than by declaring it. A function is referred to as
// pkg.Name from outside its package, or bare from inside it. A method is
// referred to as x.Name where x is not an imported package in that file (a
// selector on a package name is a function, which is how
// repomaintenance.AlertConditions must not satisfy keycustody's method of the
// same name). It is a floor, like the rest of this file: a name that
// collides with an unrelated method of the same name is accepted.
func isCalled(t *testing.T, root string, ep externalProducer) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "node_modules" {
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
		imported := map[string]bool{}
		for _, im := range f.Imports {
			path := strings.Trim(im.Path.Value, `"`)
			imported[path[strings.LastIndex(path, "/")+1:]] = true
			if im.Name != nil {
				imported[im.Name.Name] = true
			}
		}
		samePkg := f.Name.Name == ep.pkg
		ast.Inspect(f, func(n ast.Node) bool {
			if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == ep.name && p == ep.file {
				// Its own declaration, and any recursion inside it, is not a caller.
				return false
			}
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name != ep.name {
					return true
				}
				id, isIdent := x.X.(*ast.Ident)
				switch {
				case ep.method && !(isIdent && imported[id.Name]):
					found = true
				case !ep.method && isIdent && id.Name == ep.pkg && imported[id.Name]:
					found = true
				}
			case *ast.Ident:
				if !ep.method && samePkg && x.Name == ep.name {
					found = true
				}
			}
			return true
		})
		return nil
	})
	return found
}

// externalProducerProblems is rule 3.
func externalProducerProblems(t *testing.T, alertDir, root string) []string {
	t.Helper()
	kinds := map[string]bool{}
	for _, k := range kindConstants(parseDir(t, alertDir)) {
		kinds[k] = true
	}
	var out []string
	for _, ep := range externalProducers(t, root, alertDir, kinds) {
		if !isCalled(t, root, ep) {
			out = append(out, ep.pkg+"."+ep.name+" builds an alert kind and no non-test code calls it ("+filepath.Base(ep.file)+")")
		}
	}
	sort.Strings(out)
	return out
}

func TestEveryAlertKindHasAProducerAndEveryProducerHasACaller(t *testing.T) {
	if got := externalProducerProblems(t, ".", filepath.Join("..", "..", "..")); len(got) != 0 {
		t.Fatalf("an alert producer outside the package that nothing calls:\n  %s", strings.Join(got, "\n  "))
	}
	if got := problems(t, ".", filepath.Join("..", "..", "..")); len(got) != 0 {
		t.Fatalf("dead alert vocabulary:\n  %s", strings.Join(got, "\n  "))
	}
}

func TestTheAlertProducerGuardCanFail(t *testing.T) {
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
	write("internal/alert/alert.go", "package alert\n\ntype Kind string\n\nconst (\n\tLive Kind = \"LIVE\"\n\tDead Kind = \"DEAD\"\n)\n")
	write("internal/alert/prod.go", "package alert\n\ntype Condition struct{ Kind Kind }\n\nfunc Raise() []Condition { return []Condition{{Kind: Live}} }\n")
	alertDir := filepath.Join(tree, "internal", "alert")

	got := problems(t, alertDir, tree)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "kind Dead is produced by no function") {
		t.Errorf("a kind nothing produces was not found:\n%s", joined)
	}
	if !strings.Contains(joined, "alert.Raise produces conditions and no non-test code calls it") {
		t.Errorf("a producer nothing calls was not found:\n%s", joined)
	}

	// A test file calling it does not count; a production file does.
	write("internal/app/uses_test.go", "package app\n\nimport \"x/alert\"\n\nvar _ = alert.Raise\n")
	if joined := strings.Join(problems(t, alertDir, tree), "\n"); !strings.Contains(joined, "alert.Raise produces conditions") {
		t.Errorf("a test-only caller was accepted:\n%s", joined)
	}
	write("internal/app/uses.go", "package app\n\nimport \"x/alert\"\n\nvar _ = alert.Raise\n")
	if joined := strings.Join(problems(t, alertDir, tree), "\n"); strings.Contains(joined, "alert.Raise produces conditions") {
		t.Errorf("a production caller was not seen:\n%s", joined)
	}

	// Rule 3's control: a function outside the package builds a kind and
	// nothing calls it. It must be named, and a production caller (not a
	// test, and not a same-named function in another package) must clear it.
	write("internal/keycustody/k.go", "package keycustody\n\nimport \"x/alert\"\n\ntype S struct{}\n\nfunc (s *S) Conds() []alert.Condition { return []alert.Condition{{Kind: alert.Live}} }\n\nfunc Free() alert.Condition { return alert.Condition{Kind: alert.Live} }\n")
	if joined := strings.Join(externalProducerProblems(t, alertDir, tree), "\n"); !strings.Contains(joined, "keycustody.Conds builds an alert kind and no non-test code calls it") || !strings.Contains(joined, "keycustody.Free builds an alert kind and no non-test code calls it") {
		t.Errorf("an external producer with no caller was not found:\n%s", joined)
	}
	write("internal/app/calls_test.go", "package app\n\nfunc _(s interface{ Conds() }) { s.Conds() }\n")
	if joined := strings.Join(externalProducerProblems(t, alertDir, tree), "\n"); !strings.Contains(joined, "keycustody.Conds builds") {
		t.Errorf("a test-only caller of an external producer was accepted:\n%s", joined)
	}
	write("internal/other/other.go", "package other\n\nimport \"x/other2\"\n\nfunc _() { other2.Conds() }\n")
	if joined := strings.Join(externalProducerProblems(t, alertDir, tree), "\n"); !strings.Contains(joined, "keycustody.Conds builds") {
		t.Errorf("a call to a same-named function in another package cleared a method of keycustody:\n%s", joined)
	}
	write("internal/app/calls.go", "package app\n\nimport \"x/keycustody\"\n\nfunc _(s *keycustody.S) { s.Conds(); keycustody.Free() }\n")
	if joined := strings.Join(externalProducerProblems(t, alertDir, tree), "\n"); strings.Contains(joined, "keycustody.") {
		t.Errorf("production callers of an external producer were not seen:\n%s", joined)
	}
}
