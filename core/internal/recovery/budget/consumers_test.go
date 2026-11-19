package budget

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// EPIC Q panel finding C3 (#983): a budget nothing reads is a promise
// nothing keeps. Three budgets were written, given exit codes, documented in
// a table and "passed" on unit tests of this package alone, while no code
// path in the product ever asked them anything. This guard reads the
// product's own source: every budget name this package declares has to be
// referenced, as budget.<Name>, by non-test code outside this package.

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot find this test's own path")
	}
	// core/internal/recovery/budget/consumers_test.go
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..")
}

// declaredBudgets are the identifiers of the constants of type Name.
func declaredBudgets(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "budget.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "Name" {
				for _, n := range vs.Names {
					out = append(out, n.Name)
				}
			}
		}
	}
	return out
}

// referencedOutside returns the set of budget.<X> selectors used by non-test
// Go files of the product (core and apps), other than this package itself.
func referencedOutside(t *testing.T, root string) map[string]string {
	t.Helper()
	used := map[string]string{}
	self := filepath.Join(root, "core", "internal", "recovery", "budget")
	for _, top := range []string{"core", "apps"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p == self || d.Name() == "node_modules" || d.Name() == "testdata" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil || !strings.Contains(string(src), "recovery/budget") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, src, 0)
			if err != nil {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "budget" {
						used[sel.Sel.Name] = p
					}
				}
				return true
			})
			return nil
		})
	}
	return used
}

func TestEveryBudgetIsReadByAProductCodePath(t *testing.T) {
	names := declaredBudgets(t)
	if len(names) == 0 {
		t.Fatal("found no budget constants in budget.go, so this guard checks nothing")
	}
	used := referencedOutside(t, repoRoot(t))
	if len(used) == 0 {
		t.Fatal("found no product reference to the budget package at all, so the scan itself is broken")
	}
	for _, n := range names {
		if _, ok := used[n]; !ok {
			t.Errorf("budget %s is declared, documented and given an exit code, and no product code outside this package reads it", n)
		}
	}
}

// The scan has to be able to see a reference, or the guard above passes
// for the wrong reason.
func TestTheConsumerScanSeesARealConsumer(t *testing.T) {
	used := referencedOutside(t, repoRoot(t))
	for _, n := range []string{"ConcurrentReads", "DownloadStreams"} {
		if _, ok := used[n]; !ok {
			t.Errorf("the scan did not find the known consumer of budget.%s", n)
		}
	}
}

// The two read ceilings are the numbers the settings card and the
// operator-facing configuration name (assurance.max_concurrent_full_reads
// is 2 and assurance.max_per_destination is 1, #1052). The engine ran at 4
// and 2, so the card told the operator something untrue.
func TestTheReadCeilingDefaultsAreTheOnesTheOperatorIsTold(t *testing.T) {
	d := Defaults()
	if d.MaxConcurrentReads != 2 || d.MaxReadsPerDestination != 1 {
		t.Errorf("the engine's read ceilings default to %d and %d, but the configuration's documented defaults are 2 and 1", d.MaxConcurrentReads, d.MaxReadsPerDestination)
	}
}
