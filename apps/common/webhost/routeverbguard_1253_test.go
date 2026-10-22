package webhost

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/cliecho"
)

// Two of the blind spots issue #1253 found in the #1150 guard above.
//
// The guard counts a route as answered when it has a cliecho entry, and an
// entry with nothing but a `why` is an entry. So a new route could land
// saying "there is no verb that ..." and stay green, and the only thing
// between that sentence and main was whether a reviewer happened to read
// routes.go. Most of those sentences are deliberate (a browser session
// has no terminal spelling, a first-run guide has no step to skip on a
// shell), which is why they can't simply be made failures: that would
// push people into writing builders that print something wrong. What
// they can be made is VISIBLE. The golden below is every sentence cliecho
// prints in place of a command, and a sentence added, removed or reworded
// changes a checked-in file that review has to look at.
//
// The second spot is the serve mux. The guard walks the chi routers, and
// serve composes those routers onto an http.ServeMux by pattern. A
// handler mounted on that mux directly under /api/v1 is a route no chi
// walk will ever see, and nothing today stops one landing. The mux has no
// way to list its own patterns, so this reads them out of the source,
// the way a reviewer would, and holds each against a table saying which
// walked router answers it.

// ------------------------------------------------- the gap golden ---

// gapGolden1253 is the checked-in list of every sentence cliecho can
// print in place of a command.
const gapGolden1253 = "testdata/cli_gaps_1253.golden"

// gapRepin1253 is the one command that rewrites the golden from what
// cliecho says now. It is in every failure message, because the person
// reading the failure is the person who has to run it.
const gapRepin1253 = "UPDATE_GOLDEN=1 go test ./common/webhost -run TestRouteVerbGuard1253_EveryGapSentenceIsInTheGolden (from apps/)"

// renderGaps writes gaps in the golden's format: a route per line, its
// kind beside it, and each sentence it can print indented under it.
//
// The kind is the part a reviewer most needs, so it is spelled out rather
// than left to be inferred. "no command" is a route with no builder at
// all, which is a whole UI action the terminal can't do. "refuses some
// requests" is a builder that declines for some bodies (a run_cycle on
// POST /operations, say). A route moving from the second to the first is
// exactly the change this file exists to make loud.
//
// Most builders also carry a sentence for a body they can't read, and
// that one is marked "(malformed body)", because it says nothing about
// what the terminal can do with a request the browser would really send.
// Without the mark, the two deliberate refusals the issue counted were
// lost among thirty sentences that only cover bad input.
func renderGaps(gaps []cliecho.Gap, built map[string]bool, malformed map[string]string) string {
	var b strings.Builder
	b.WriteString("# Every sentence core/cliecho prints in place of a command (#1253).\n")
	b.WriteString("#\n")
	b.WriteString("# A change here is a change to what the terminal can't do. Read it as\n")
	b.WriteString("# one: a new route under \"no command\" is a UI action with no CLI twin,\n")
	b.WriteString("# and a new unmarked sentence under \"refuses some requests\" is a\n")
	b.WriteString("# request the browser sends that the terminal has no line for.\n")
	b.WriteString("#\n")
	b.WriteString("# Re-pin with: " + gapRepin1253 + "\n")
	last := ""
	for _, g := range gaps {
		if g.Route != last {
			kind := "no command"
			if built[g.Route] {
				kind = "refuses some requests"
			}
			b.WriteString("\n" + g.Route + "  (" + kind + ")\n")
			last = g.Route
		}
		if built[g.Route] && malformed[g.Route] == g.Why {
			b.WriteString("\t(malformed body) " + g.Why + "\n")
			continue
		}
		b.WriteString("\t" + g.Why + "\n")
	}
	return b.String()
}

// routesWithABuilder is every route cliecho can build a command for at
// all, read off Examples, which carries one action per builder shape and
// none for a route whose entry is only a `why`.
func routesWithABuilder() map[string]bool {
	out := map[string]bool{}
	for _, ex := range cliecho.Examples() {
		out[ex.Method+" "+ex.Route] = true
	}
	return out
}

// malformedBodySentences is, for each route with a builder, the sentence
// it prints for a body that isn't JSON at all.
func malformedBodySentences(built map[string]bool) map[string]string {
	out := map[string]string{}
	for route := range built {
		method, path, _ := strings.Cut(route, " ")
		line := cliecho.Echo(cliecho.Action{Method: method, Route: path, Body: []byte("not json")})
		if line.GapDetail != "" {
			out[route] = line.GapDetail
		}
	}
	return out
}

// currentGaps renders what cliecho says now, in the golden's format.
func currentGaps(gaps []cliecho.Gap) string {
	built := routesWithABuilder()
	return renderGaps(gaps, built, malformedBodySentences(built))
}

// gapDrift reports the lines that differ between the golden and what
// cliecho says now, as "+ line" for one the golden lacks and "- line" for
// one it still has and cliecho no longer prints. Comments and blank lines
// are not compared, so rewording the header is not drift.
func gapDrift(golden, current string) []string {
	set := func(s string) map[string]bool {
		out := map[string]bool{}
		route := ""
		for _, line := range strings.Split(s, "\n") {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "\t") {
				// A sentence is keyed by its route, so the same sentence
				// moving from one route to another is two lines of drift.
				out[route+"\t"+strings.TrimPrefix(line, "\t")] = true
				continue
			}
			route = line
			out[line] = true
		}
		return out
	}
	want, got := set(golden), set(current)
	var out []string
	for line := range got {
		if !want[line] {
			out = append(out, "+ "+line)
		}
	}
	for line := range want {
		if !got[line] {
			out = append(out, "- "+line)
		}
	}
	sort.Strings(out)
	return out
}

func TestRouteVerbGuard1253_EveryGapSentenceIsInTheGolden(t *testing.T) {
	gaps := cliecho.Gaps()
	if len(gaps) == 0 {
		t.Fatal("cliecho reports no gap sentences at all, so the golden would pin nothing")
	}
	current := currentGaps(gaps)

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(gapGolden1253), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(gapGolden1253, []byte(current), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s; read the diff before committing it", gapGolden1253)
		return
	}

	golden, err := os.ReadFile(gapGolden1253)
	if err != nil {
		t.Fatalf("reading %s: %v\nRe-pin with: %s", gapGolden1253, err, gapRepin1253)
	}
	if drift := gapDrift(string(golden), current); len(drift) > 0 {
		t.Errorf("what core/cliecho prints in place of a command has changed, and %s has not:\n  %s\n\nEvery line marked + is a UI action the terminal now can't do, or a new reason it can't. That may be the right call, but it has to be made in review, so re-pin with:\n  %s\nand let the diff of the golden say it.",
			gapGolden1253, strings.Join(drift, "\n  "), gapRepin1253)
	}
}

// The mutation the golden exists for: a route lands with a `why` and no
// builder, and the guard above stays green because a `why` counts as an
// answer. Here it has to show up as drift.
func TestRouteVerbGuard1253_MutationANewWhyOnlyRouteIsCaught(t *testing.T) {
	before := currentGaps(cliecho.Gaps())

	gaps := append(cliecho.Gaps(), cliecho.Gap{
		Route: "POST /a-route-with-only-a-why",
		Why:   "there is no verb that does what this button does",
	})
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Route < gaps[j].Route })
	after := currentGaps(gaps)

	drift := gapDrift(before, after)
	if !contains(drift, "+ POST /a-route-with-only-a-why  (no command)") {
		t.Errorf("a new route answered only by a `why` did not show up as drift; got %v", drift)
	}

	// And a builder that starts refusing a request it used to answer is
	// drift too, under the route that already had a builder.
	gaps = append(cliecho.Gaps(), cliecho.Gap{Route: "POST /backup-sets", Why: "a new refusal"})
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Route < gaps[j].Route })
	drift = gapDrift(before, currentGaps(gaps))
	if !contains(drift, "+ POST /backup-sets  (refuses some requests)\ta new refusal") {
		t.Errorf("a new refusal on a route with a builder did not show up as drift; got %v", drift)
	}

	// The control: the same list twice is not drift.
	if drift := gapDrift(before, before); len(drift) != 0 {
		t.Errorf("the golden compared with itself drifted: %v", drift)
	}
}

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
