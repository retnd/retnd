package serve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The serve mux, held to the route-to-verb guard (#1253).
//
// The guard in apps/common/webhost walks the chi routers, and this package
// composes those routers onto an http.ServeMux by pattern. A handler
// mounted on that mux directly, beside the router, is a set of routes no
// chi walk sees. The first check for that read Handle calls out of the
// source and held each pattern against a table of prose, and the review
// showed the gaps: a second mux reusing an explained pattern passed, a
// catch-all "/" mounted with the API router passed, and nothing checked
// the prose. So every serve mux is now built by one function, newMux, from
// a list of mounts that say what kind of handler each one is, and these
// tests hold the real lists rather than the source text.

// repoRoot is the checkout this package sits in.
const repoRoot = "../../../.."

// muxBuilders lists every place outside newMux that builds a serve mux or
// mounts onto the default one, as "file:line what".
func muxBuilders(fset *token.FileSet, file *ast.File, path string) []string {
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		inNewMux := ok && fn.Recv == nil && fn.Name.Name == "newMux" && strings.HasSuffix(filepath.ToSlash(path), "webhost/serve/mounts.go")
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "http" {
				return true
			}
			at := filepath.ToSlash(path) + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
			switch sel.Sel.Name {
			case "NewServeMux":
				if !inNewMux {
					out = append(out, at+" http.NewServeMux")
				}
			case "Handle", "HandleFunc":
				out = append(out, at+" http."+sel.Sel.Name+" on the default mux")
			}
			return true
		})
	}
	return out
}

func TestOnlyNewMuxBuildsAServeMux(t *testing.T) {
	fset := token.NewFileSet()
	var found []string
	files := 0
	for _, top := range []string{"apps", "core", "distribution"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "testdata" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			files++
			rel, _ := filepath.Rel(repoRoot, path)
			found = append(found, muxBuilders(fset, file, rel)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", top, err)
		}
	}
	if files < 500 {
		t.Fatalf("read %d Go files, which is far fewer than this repository holds, so the scan would pass having missed most of it", files)
	}
	for _, f := range found {
		t.Errorf("%s: a serve mux built outside newMux (apps/common/webhost/serve/mounts.go) is a set of routes the route-to-verb guard can't see. Build it from a mount list with newMux, so TestEveryAPIMountIsARouterTheGuardWalks holds it.", f)
	}
}

// The control: a mux built anywhere else, or a mount on the default mux,
// is found.
func TestOnlyNewMuxBuildsAServeMux_MutationIsCaught(t *testing.T) {
	const src = `package x

import "net/http"

func compose(h http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", h)
	http.HandleFunc("/api/v1/first-run/finish", h.ServeHTTP)
	return mux
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "apps/x/compose.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := muxBuilders(fset, file, "apps/x/compose.go")
	if len(got) != 2 {
		t.Errorf("want the NewServeMux and the default-mux HandleFunc reported, got %q", got)
	}
}
