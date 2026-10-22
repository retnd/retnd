package webhost

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

// One of the blind spots issue #1253 found in the #1150 guard above. (The
// other one this file used to hold, the golden of every sentence cliecho
// prints in place of a command, lives beside cliecho now, as the outcome
// golden in core/cliecho/outcomes_test.go, since cliecho is all it reads.)
//
// The guard walks the chi routers, and serve composes those routers onto
// an http.ServeMux by pattern. A handler mounted on that mux directly
// under /api/v1 is a route no chi walk will ever see, and nothing stopped
// one landing. The mux has no way to list its own patterns, so this reads
// them out of the source, the way a reviewer would, and holds each
// against a table saying which walked router answers it.

// ---------------------------------------------- the serve mux walk ---

// muxMount is one pattern handed to an http.ServeMux, and where.
type muxMount struct {
	pattern string
	file    string
	at      string
}

// key is how muxPatternsTheGuardWalks names a mount: by file and pattern,
// not by line, so moving a line is not a change and the same pattern in
// two compositions (the UI proxy and the engine) is two claims.
func (m muxMount) key() string { return m.file + " " + m.pattern }

// muxMountsIn reads every Handle or HandleFunc call in one parsed file
// whose first argument is a pattern. A pattern that is not a string
// literal can't be read without running the code, so it comes back with
// an empty pattern, which nothing in the table can answer.
func muxMountsIn(fset *token.FileSet, file *ast.File) []muxMount {
	var out []muxMount
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		at := fset.Position(call.Pos())
		file := filepath.ToSlash(at.Filename)
		where := file + ":" + strconv.Itoa(at.Line)
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			out = append(out, muxMount{file: file, at: where})
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			out = append(out, muxMount{file: file, at: where})
			return true
		}
		out = append(out, muxMount{pattern: pattern, file: file, at: where})
		return true
	})
	return out
}

// everyMuxMount is muxMountsIn over every non-test Go file under this
// package, serve included. That is wider than the two files the issue
// names on purpose: a third composition anywhere under webhost is the same
// hole.
func everyMuxMount(t *testing.T) []muxMount {
	t.Helper()
	fset := token.NewFileSet()
	var out []muxMount
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		out = append(out, muxMountsIn(fset, file)...)
		return nil
	})
	if err != nil {
		t.Fatalf("reading the webhost sources: %v", err)
	}
	return out
}

// muxPath is the path half of a pattern. Go 1.22 patterns may lead with a
// method ("POST /api/v1/x") or a host, and either way it's the path that
// says whether the guard should care.
func muxPath(pattern string) string {
	if _, rest, ok := strings.Cut(pattern, " "); ok {
		pattern = strings.TrimSpace(rest)
	}
	if i := strings.Index(pattern, "/"); i > 0 {
		pattern = pattern[i:]
	}
	return pattern
}

// isAPIPattern says whether a mounted pattern is in the guard's territory:
// anything under /api/ or /health/, which is everything the route-to-verb
// rule covers. "/" and the static bundle are not routes an operator could
// have typed a command for.
func isAPIPattern(pattern string) bool {
	p := muxPath(pattern)
	return strings.HasPrefix(p, "/api/") || p == "/api" || strings.HasPrefix(p, "/health/") || p == "/health"
}

// unwalkedMounts lists the mounts in the guard's territory that the table
// doesn't answer, along with any whose pattern couldn't be read at all.
func unwalkedMounts(mounts []muxMount, walked map[string]string) []muxMount {
	var out []muxMount
	for _, m := range mounts {
		if m.pattern == "" {
			out = append(out, m)
			continue
		}
		if !isAPIPattern(m.pattern) {
			continue
		}
		if strings.TrimSpace(walked[m.key()]) == "" {
			out = append(out, m)
		}
	}
	return out
}

func TestRouteVerbGuard1253_EveryServeMuxPatternIsWalked(t *testing.T) {
	mounts := everyMuxMount(t)

	// Vacuity: the four the issue names have to be in view, or this
	// would pass having read none of them.
	seen, mounted := map[string]bool{}, map[string]bool{}
	for _, m := range mounts {
		seen[m.pattern] = true
		mounted[m.key()] = true
	}
	for _, must := range []string{"/api/v1/", "/api/v1/auth/", "/api/v1/recoveries", "/health/"} {
		if !seen[must] {
			t.Fatalf("the source walk did not find %s mounted on a serve mux, so this test would pass without covering it", must)
		}
	}

	for _, m := range unwalkedMounts(mounts, muxPatternsTheGuardWalks) {
		if m.pattern == "" {
			t.Errorf("%s mounts a handler on a mux with a pattern that is not a string literal, so this test can't read it. Spell the pattern out where it is mounted.", m.at)
			continue
		}
		t.Errorf("%s mounts %q on a serve mux, and nothing says which router the route-to-verb guard walks behind it. If it is one of the routers everyRegisteredRoute walks, add %q to muxPatternsTheGuardWalks saying which; if it is a new handler, add it to everyRegisteredRoute first, because every route behind it is otherwise invisible to the guard.", m.at, m.pattern, m.key())
	}

	// A line for a pattern nothing mounts is a stale claim about
	// coverage, which is the worst kind to leave lying around.
	for mount := range muxPatternsTheGuardWalks {
		if !mounted[mount] {
			t.Errorf("muxPatternsTheGuardWalks explains %q and that file no longer mounts that pattern. Delete the line.", mount)
		}
	}
}

// The mutation the walk exists for: a handler mounted straight onto the
// mux under /api/v1, beside the router rather than inside it.
func TestRouteVerbGuard1253_MutationAMuxMountedHandlerIsCaught(t *testing.T) {
	const src = `package serve

import "net/http"

func compose(api, finish http.Handler, pattern string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", api)
	mux.HandleFunc("POST /api/v1/first-run/finish", finish.ServeHTTP)
	mux.Handle(pattern, api)
	mux.Handle("/", api)
	return mux
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mutation.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := unwalkedMounts(muxMountsIn(fset, file), muxPatternsTheGuardWalks)

	var patterns []string
	for _, m := range got {
		patterns = append(patterns, m.pattern)
	}
	if !contains(patterns, "POST /api/v1/first-run/finish") {
		t.Errorf("a handler mounted on the mux under /api/v1 with no router behind it was not reported; got %q", patterns)
	}
	if !contains(patterns, "") {
		t.Errorf("a pattern that is not a literal was not reported as unreadable; got %q", patterns)
	}

	// The control: the static bundle at "/" is not the guard's business.
	if contains(patterns, "/") {
		t.Errorf("the static bundle's \"/\" was reported as an unwalked API route; got %q", patterns)
	}
}

// muxPatternsTheGuardWalks is every /api/ or /health/ pattern mounted on
// a serve mux, keyed "file pattern", and which of the routers
// everyRegisteredRoute walks answers it. Adding a line is a claim that the guard already sees every
// route behind the pattern.
//
// serve/firstrun.go is the engine's own composition (newEngineHandler), so
// each of its lines names the router directly. serve/ui.go is the UI
// container's, and everything it mounts under /api or /health is a
// reverse proxy to that engine: it adds no route of its own, so each line
// there says which engine mount the request lands on.
var muxPatternsTheGuardWalks = map[string]string{
	"serve/firstrun.go /api/v1/auth/": "the auth router (cfg.AuthRoutes, apps/common/auth/local), walked at /api/v1/auth the way this line strips it",
	"serve/firstrun.go /health/":      "webhost.NewRouter, walked configured and unconfigured; the health probes are its /health/live and /health/ready",
	"serve/firstrun.go /api/v1/":      "webhost.NewRouter, walked configured and unconfigured (the first-run surface is the unconfigured one)",

	"serve/ui.go /health/":            "the reverse proxy to the engine, which answers it with serve/firstrun.go's /health/ mount",
	"serve/ui.go /api/v1/":            "the reverse proxy to the engine, which answers it with serve/firstrun.go's /api/v1/ and /api/v1/auth/ mounts",
	"serve/ui.go /api/v1/recoveries/": "the same reverse proxy with a rolling write deadline for downloads; the engine answers it with webhost.NewRouter",
	"serve/ui.go /api/v1/recoveries":  "the same proxy for the exact path (POST /api/v1/recoveries); the engine answers it with webhost.NewRouter",
}
