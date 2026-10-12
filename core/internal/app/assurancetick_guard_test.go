package app

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

// The panel's C2: RecoveryTestTick had exactly one caller, under `retnd
// daemon`, and the process the product runs (`retnd serve`) never reached it.
// Two schedulers each listing the passes they run will drift again, so there
// is one list (Service.AssuranceTick) and these guards hold both schedulers to
// it:
//
//	1. Every exported *Tick method of Service other than AlertTick (its own
//	   timer) and AssuranceTick itself is called by AssuranceTick.
//	2. Both schedulers call AssuranceTick, and neither calls a pass directly.

func parseGoFiles(t *testing.T, dir string) []*ast.File {
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

func recvIsService(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Service"
}

// calledIn returns the names selected (x.Name) inside the function declared
// as fn in files.
func calledIn(files []*ast.File, fn string) (map[string]bool, bool) {
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != fn || fd.Body == nil {
				continue
			}
			out := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					out[sel.Sel.Name] = true
				}
				return true
			})
			return out, true
		}
	}
	return nil, false
}

func tickProblems(t *testing.T, appDir string, schedulers map[string][]*ast.File) []string {
	t.Helper()
	files := parseGoFiles(t, appDir)
	var ticks []string
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && recvIsService(fd) && fd.Name.IsExported() && strings.HasSuffix(fd.Name.Name, "Tick") {
				ticks = append(ticks, fd.Name.Name)
			}
		}
	}
	sort.Strings(ticks)
	var out []string
	inner, ok := calledIn(files, "AssuranceTick")
	if !ok {
		return []string{"Service.AssuranceTick does not exist"}
	}
	for _, name := range ticks {
		if name == "AssuranceTick" || name == "AlertTick" {
			continue
		}
		if !inner[name] {
			out = append(out, "Service."+name+" is a scheduled pass that AssuranceTick does not call, so a scheduler cannot reach it")
		}
	}
	for who, fs := range schedulers {
		var all map[string]bool
		for _, fn := range []string{"scrubTickRecovered", "tickScrub"} {
			if m, ok := calledIn(fs, fn); ok {
				all = m
			}
		}
		switch {
		case all == nil:
			out = append(out, who+" has no scrub tick function")
		case !all["AssuranceTick"]:
			out = append(out, who+" does not call AssuranceTick")
		default:
			for _, direct := range ticks {
				if direct != "AssuranceTick" && direct != "AlertTick" && all[direct] {
					out = append(out, who+" calls Service."+direct+" directly, bypassing AssuranceTick")
				}
			}
		}
	}
	return out
}

func TestEveryEntryPointInAssuranceTickIsCalledByBothSchedulers(t *testing.T) {
	schedulers := map[string][]*ast.File{
		"the daemon loop (internal/app)":       parseGoFiles(t, "."),
		"the serving scheduler (core/service)": parseGoFiles(t, filepath.Join("..", "..", "service")),
	}
	if got := tickProblems(t, ".", schedulers); len(got) != 0 {
		t.Fatalf("the two schedulers can drift:\n  %s", strings.Join(got, "\n  "))
	}
}

func TestTheAssuranceTickGuardCanFail(t *testing.T) {
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	appDir := filepath.Join(t.TempDir(), "app")
	write(appDir, "a.go", `package app

type Service struct{}

func (s *Service) ScrubTick()        {}
func (s *Service) NewPassTick()      {}
func (s *Service) AlertTick()        {}
func (s *Service) AssuranceTick()    { s.ScrubTick() }
func (s *Service) scrubTickRecovered() { s.AssuranceTick() }
`)
	svcDir := filepath.Join(t.TempDir(), "service")
	write(svcDir, "s.go", `package service

type B struct{ inner interface{ ScrubTick() } }

func (b *B) tickScrub() { b.inner.ScrubTick() }
`)
	got := strings.Join(tickProblems(t, appDir, map[string][]*ast.File{
		"daemon": parseGoFiles(t, appDir), "serve": parseGoFiles(t, svcDir),
	}), "\n")
	for _, want := range []string{
		"Service.NewPassTick is a scheduled pass that AssuranceTick does not call",
		"serve does not call AssuranceTick",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the guard did not report %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "daemon") {
		t.Errorf("the daemon loop does call AssuranceTick and was reported:\n%s", got)
	}

	// And a scheduler that calls a pass directly is caught, even when it also
	// calls AssuranceTick.
	write(svcDir, "s.go", `package service

type B struct{ inner interface{ AssuranceTick(); ScrubTick() } }

func (b *B) tickScrub() { b.inner.AssuranceTick(); b.inner.ScrubTick() }
`)
	got = strings.Join(tickProblems(t, appDir, map[string][]*ast.File{"serve": parseGoFiles(t, svcDir)}), "\n")
	if !strings.Contains(got, "serve calls Service.ScrubTick directly") {
		t.Errorf("a direct call was not reported:\n%s", got)
	}
}
