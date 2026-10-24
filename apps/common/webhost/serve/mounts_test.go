package serve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"path/filepath"
	"slices"
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

// sentinel is a handler that is only ever equal to itself.
type sentinel struct{ name string }

func (*sentinel) ServeHTTP(http.ResponseWriter, *http.Request) {}

func TestEveryAPIMountIsARouterTheGuardWalks(t *testing.T) {
	api, auth := &sentinel{"api"}, &sentinel{"auth"}
	proxy, downloads, static := &sentinel{"proxy"}, &sentinel{"downloads"}, &sentinel{"static"}
	for _, withAuth := range []http.Handler{auth, nil} {
		engine := engineMounts(api, withAuth)
		ui := uiMounts(proxy, downloads, static)
		for _, p := range mountProblems(engine, ui, api, withAuth) {
			t.Errorf("%s. The route-to-verb guard (apps/common/webhost) walks webhost.NewRouter and the auth router at %s, so anything else mounted under /api or /health is routes it never sees.", p, authPrefix)
		}
	}

	// Vacuity: the patterns the guard depends on are in view.
	var patterns []string
	for _, m := range append(engineMounts(api, auth), uiMounts(proxy, downloads, static)...) {
		patterns = append(patterns, m.pattern)
	}
	for _, must := range []string{"/api/v1/", "/api/v1/auth/", "/health/", "/api/v1/recoveries", "/"} {
		if !slices.Contains(patterns, must) {
			t.Fatalf("no composition mounts %s, so this test would pass without covering it", must)
		}
	}
}

// The mutations the source scan could not see, each against the real
// lists with one thing wrong.
func TestEveryAPIMountIsARouterTheGuardWalks_MutationsAreCaught(t *testing.T) {
	api, auth := &sentinel{"api"}, &sentinel{"auth"}
	proxy, downloads, static := &sentinel{"proxy"}, &sentinel{"downloads"}, &sentinel{"static"}
	engine := engineMounts(api, auth)
	ui := uiMounts(proxy, downloads, static)
	other := &sentinel{"other"}
	for _, tc := range []struct {
		name       string
		engine, ui []mount
	}{
		{"a handler beside the router under /api/v1",
			append(slices.Clone(engine), mount{pattern: "POST /api/v1/first-run/finish", handler: other, kind: mountAPIRouter}), ui},
		{"the API router mounted as a catch-all",
			append(slices.Clone(engine), mount{pattern: "/", handler: api, kind: mountAPIRouter}), ui},
		{"a second mount of /api/v1/ on another handler",
			append(slices.Clone(engine), mount{pattern: "/api/v1/", handler: other, kind: mountAPIRouter}), ui},
		{"the auth router at a prefix the guard doesn't walk",
			[]mount{{pattern: "/api/v2/auth/", handler: auth, kind: mountAuthRouter, strip: "/api/v2/auth"}, engine[1], engine[2]}, ui},
		{"the UI forwarding a path no engine mount answers",
			engine, append(slices.Clone(ui), mount{pattern: "/api/v2/", handler: proxy, kind: mountEngineProxy})},
		{"the UI's catch-all serving something other than the bundle",
			engine, append(slices.Clone(ui[:4]), mount{pattern: "/", handler: proxy, kind: mountEngineProxy})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mountProblems(tc.engine, tc.ui, api, auth); len(got) == 0 {
				t.Error("not reported")
			}
		})
	}
}
