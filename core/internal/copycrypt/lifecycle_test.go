package copycrypt

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EPIC T, T2.1 (#1316, FR-87): the lifecycle's behaviour table, the
// registry's test hook, and the status guard.
//
// Written by reading; not run (the suites run on the release PR).

func TestEachStatusHasTheBehaviourTheLifecycleTableStates(t *testing.T) {
	for _, c := range []struct {
		status                  Status
		writes, reads, degrades bool
	}{
		{StatusSupported, true, true, false},
		{StatusDeprecated, true, true, true},
		{StatusRetired, false, true, true},
		{Status("sunset"), false, false, true}, // outside the vocabulary: fails closed
	} {
		if got := c.status.Writes(); got != c.writes {
			t.Errorf("%q.Writes() = %v, want %v", c.status, got, c.writes)
		}
		if got := c.status.Reads(); got != c.reads {
			t.Errorf("%q.Reads() = %v, want %v", c.status, got, c.reads)
		}
		if got := c.status.Degrades(); got != c.degrades {
			t.Errorf("%q.Degrades() = %v, want %v", c.status, got, c.degrades)
		}
	}
}

func TestCheckWriteRefusesOnlyARetiredSuiteAndNamesTheVersion(t *testing.T) {
	base := Default()
	dep := InjectForTest(t, base, TestSuitePrefix+"deprecated/v1", StatusDeprecated, "0.10.0")
	ret := InjectForTest(t, base, TestSuitePrefix+"retired/v1", StatusRetired, "0.9.0")

	if err := CheckWrite(base); err != nil {
		t.Errorf("the default suite is refused a write: %v", err)
	}
	if err := CheckWrite(dep); err != nil {
		t.Errorf("a deprecated suite is refused a write; it keeps writing until it is retired: %v", err)
	}
	err := CheckWrite(ret)
	if !errors.Is(err, ErrSuiteRetired) {
		t.Fatalf("CheckWrite(retired) = %v, want ErrSuiteRetired", err)
	}
	for _, want := range []string{ret.ID(), "0.9.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
	if err := CheckWrite(nil); !errors.Is(err, ErrUnknownSuite) {
		t.Errorf("CheckWrite(nil) = %v, want ErrUnknownSuite", err)
	}
}

// The hook is visible through Suites and Lookup for the life of the test and
// gone after it; it never becomes the default; and the production table has
// no test id in it at all.
func TestTheTestHookNeverReachesTheProductionTable(t *testing.T) {
	for _, s := range Suites() {
		if strings.HasPrefix(s.ID(), TestSuitePrefix) {
			t.Fatalf("the production table holds %q, a test suite id", s.ID())
		}
	}
	before := len(Suites())
	id := TestSuitePrefix + "hook/v1"

	t.Run("injected", func(t *testing.T) {
		InjectForTest(t, Default(), id, StatusRetired, "0.9.0")
		if _, ok := Lookup(id); !ok {
			t.Fatalf("Lookup(%q) does not see the injected suite", id)
		}
		if len(Suites()) != before+1 {
			t.Fatalf("Suites() has %d entries with one injected, want %d", len(Suites()), before+1)
		}
		if strings.HasPrefix(Default().ID(), TestSuitePrefix) {
			t.Fatal("an injected suite became the default")
		}
	})

	if _, ok := Lookup(id); ok {
		t.Fatalf("the injected suite %q outlived its test", id)
	}
	if len(Suites()) != before {
		t.Fatalf("Suites() has %d entries after the test, want %d", len(Suites()), before)
	}
}

// The status guard: the shape of core/internal/alert/producers_test.go,
// applied to the suite status vocabulary.
//
//  1. Every Status constant has a behaviour: some non-test function in this
//     package mentions it (Writes, Reads, Degrades and CheckWrite are where a
//     status becomes behaviour).
//  2. Every Status constant is consumed: some non-test file outside this
//     package names it as copycrypt.<Status> (the health section, the config
//     refusal, the engine).
//
// It is read from the syntax, so it is a floor and not a proof of
// reachability, but it is exactly the shape of the bug: a status added to the
// vocabulary that nothing gives meaning to, or that no surface acts on.

func statusConstants(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	for _, f := range parseNonTest(t, dir) {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "Status" {
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

func parseNonTest(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// behaviours is rule 1: the constants some non-test function body in dir
// mentions.
func behaviours(t *testing.T, dir string, names []string) map[string]bool {
	t.Helper()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	got := map[string]bool{}
	for _, f := range parseNonTest(t, dir) {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && want[id.Name] {
					got[id.Name] = true
				}
				return true
			})
		}
	}
	return got
}

// consumers is rule 2: the constants some non-test Go file under root,
// outside dir, names as copycrypt.<Name>.
func consumers(t *testing.T, root, dir string, names []string) map[string]bool {
	t.Helper()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	got := map[string]bool{}
	skip, _ := filepath.Abs(dir)
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
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "copycrypt" && want[sel.Sel.Name] {
					got[sel.Sel.Name] = true
				}
			}
			return true
		})
		return nil
	})
	return got
}

func statusProblems(t *testing.T, dir, root string) []string {
	t.Helper()
	names := statusConstants(t, dir)
	if len(names) == 0 {
		return []string{"no Status constants found in " + dir}
	}
	beh := behaviours(t, dir, names)
	con := consumers(t, root, dir, names)
	var out []string
	for _, n := range names {
		if !beh[n] {
			out = append(out, "the status "+n+" has no behaviour in the package")
		}
		if !con[n] {
			out = append(out, "the status "+n+" is consumed by no non-test code outside the package")
		}
	}
	return out
}

func TestEveryStatusHasAProducerAndAConsumer(t *testing.T) {
	if got := statusProblems(t, ".", filepath.Join("..", "..", "..")); len(got) != 0 {
		t.Fatalf("dead suite status vocabulary:\n  %s", strings.Join(got, "\n  "))
	}
}

func TestTheStatusGuardCanFail(t *testing.T) {
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
	write("internal/copycrypt/status.go", "package copycrypt\n\ntype Status string\n\nconst (\n\tLive Status = \"live\"\n\tDead Status = \"dead\"\n)\n\nfunc (s Status) Writes() bool { return s == Live }\n")
	dir := filepath.Join(tree, "internal", "copycrypt")

	joined := strings.Join(statusProblems(t, dir, tree), "\n")
	if !strings.Contains(joined, "the status Dead has no behaviour") {
		t.Errorf("a status with no behaviour was not found:\n%s", joined)
	}
	if !strings.Contains(joined, "the status Live is consumed by no non-test code") {
		t.Errorf("a status nothing outside consumes was not found:\n%s", joined)
	}

	// A test file naming it does not count; a production file does.
	write("internal/health/uses_test.go", "package health\n\nimport \"x/copycrypt\"\n\nvar _ = copycrypt.Live\n")
	if joined := strings.Join(statusProblems(t, dir, tree), "\n"); !strings.Contains(joined, "the status Live is consumed by no") {
		t.Errorf("a test-only consumer was accepted:\n%s", joined)
	}
	write("internal/health/uses.go", "package health\n\nimport \"x/copycrypt\"\n\nvar _ = copycrypt.Live\n")
	if joined := strings.Join(statusProblems(t, dir, tree), "\n"); strings.Contains(joined, "the status Live is consumed by no") {
		t.Errorf("a production consumer was not seen:\n%s", joined)
	}
	// A mention in the package's own test file is not a behaviour either.
	write("internal/copycrypt/status_test.go", "package copycrypt\n\nfunc dead() bool { return Dead.Writes() }\n")
	if joined := strings.Join(statusProblems(t, dir, tree), "\n"); !strings.Contains(joined, "the status Dead has no behaviour") {
		t.Errorf("a behaviour in a test file was accepted:\n%s", joined)
	}
}
