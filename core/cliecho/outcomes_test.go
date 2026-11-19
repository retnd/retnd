package cliecho

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// What every route echoes, for every kind of input, pinned (#1253).
//
// The route-to-verb guard (apps/common/webhost, #1150) counts a route as
// answered when it has an entry here, and an entry with nothing but a
// `why` is an entry. So a route could land saying "there is no verb that
// ..." and stay green. Most of those sentences are deliberate, which is
// why they can't simply be failures: that would push people into writing
// builders that print something wrong. What they can be is VISIBLE.
//
// The first version of this pinned the set of sentences, and the review
// showed why that isn't enough: a builder that starts declining a request
// it used to answer prints a sentence the golden already has, so nothing
// drifts. Turning a tier-chain retention into a silent nil, or adding an
// operation action with no arm, stayed green. So this pins the OUTCOME
// per input instead. For every route it records what each input gets:
//
//   - every body the canary corpus sends for the route's schema, one per
//     action, target kind, mode and tier shape, labelled with which;
//   - every example the table carries;
//   - a body that isn't JSON at all.
//
// Each comes out as "command", "refused: <sentence>" (a builder that
// declined on purpose, through refuse) or "malformed: <sentence>" (a
// builder that returned nil, which is only allowed for a body it can't
// read; TestNoBuilderDeclinesAReadableBodySilently holds that). A route
// with no builder at all is "no command: <sentence>".
//
// Re-pin with the command in outcomesRepin, and read the diff: a line
// going from "command" to anything else is the terminal losing something.

const outcomesGolden = "testdata/cli_outcomes.golden"

// outcomesRepin is in every failure message, because the person reading
// the failure is the person who has to run it. It has its own variable
// rather than the UPDATE_GOLDEN the #1252 golden reads, so re-pinning one
// can't quietly rewrite the other.
const outcomesRepin = "UPDATE_CLI_OUTCOMES=1 go test ./cliecho -run TestEveryRouteOutcomeIsInTheGolden (from core/)"

// routeInput is one input a route is driven with, and what to call it.
type routeInput struct {
	label  string
	action Action
}

var outcomeParams = map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar", "id": "offsite_s3", "op": "op-1", "grant": "g-1", "run": "r-1", "domain": "d-1", "step": "s-1"}

// readableInputs is every input a route's builder must answer with a
// command or a refusal: the corpus bodies for its schema and its own
// examples.
func readableInputs(t *testing.T, route string, bySchema map[string][]canaryBody) []routeInput {
	t.Helper()
	method, path, _ := strings.Cut(route, " ")
	var out []routeInput
	if schema, ok := requestSchemaByRoute()[route]; ok {
		for _, b := range bySchema[schema] {
			label := "corpus"
			if b.label != "" {
				label += " " + b.label
			}
			out = append(out, routeInput{label, Action{Method: method, Route: path, Params: outcomeParams, Body: b.body}})
		}
	}
	for _, ex := range routes[route].examples {
		ex.Method, ex.Route = method, path
		if ex.Params == nil {
			ex.Params = outcomeParams
		}
		label := "example"
		if q := ex.Query.Encode(); q != "" {
			label += " ?" + q
		}
		if len(ex.Body) > 0 {
			label += " " + string(ex.Body)
		}
		out = append(out, routeInput{label, ex})
	}
	return out
}

// outcomeOf runs one input through the route's own builder, so a
// deliberate refusal and a nil can be told apart (Echo prints both as a
// gap).
func outcomeOf(route string, a Action) string {
	e := routes[route]
	if e.build == nil {
		return "no command: " + e.why
	}
	c := e.build(a)
	switch {
	case c == nil:
		return "malformed: " + e.why
	case c.gap != "":
		return "refused: " + c.gap
	default:
		return "command"
	}
}

func corpusBySchema(t *testing.T) map[string][]canaryBody {
	t.Helper()
	out := map[string][]canaryBody{}
	for _, b := range canaryBodies(t, fieldCanaryFor) {
		out[b.schema] = append(out[b.schema], b)
	}
	return out
}

// renderOutcomes is the golden's text, from whatever `routes` holds now.
func renderOutcomes(t *testing.T) string {
	t.Helper()
	bySchema := corpusBySchema(t)
	var b strings.Builder
	b.WriteString("# What core/cliecho echoes for every route, for every kind of input (#1253).\n")
	b.WriteString("#\n")
	b.WriteString("# A line that stops saying \"command\" is the terminal losing something a\n")
	b.WriteString("# click can do. \"no command\" is a whole UI action with no CLI twin.\n")
	b.WriteString("#\n")
	b.WriteString("# Re-pin with: " + outcomesRepin + "\n")
	for _, route := range Routes() {
		b.WriteString("\n" + route + "\n")
		e := routes[route]
		if e.build == nil {
			b.WriteString("\tno command: " + e.why + "\n")
			continue
		}
		var lines []string
		seen := map[string]bool{}
		for _, in := range readableInputs(t, route, bySchema) {
			line := in.label + ": " + outcomeOf(route, in.action)
			if !seen[line] {
				seen[line] = true
				lines = append(lines, line)
			}
		}
		method, path, _ := strings.Cut(route, " ")
		if method != "GET" {
			lines = append(lines, "body that isn't JSON: "+outcomeOf(route, Action{Method: method, Route: path, Params: outcomeParams, Body: []byte("not json")}))
		}
		for _, l := range lines {
			b.WriteString("\t" + l + "\n")
		}
	}
	return b.String()
}

// outcomeDrift reports the lines that differ, "+" for one the golden
// lacks and "-" for one it has that isn't true any more. A line is keyed
// by its route, so the same outcome moving between routes is drift.
func outcomeDrift(golden, current string) []string {
	set := func(s string) map[string]bool {
		out := map[string]bool{}
		route := ""
		for _, line := range strings.Split(s, "\n") {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "\t") {
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

func TestEveryRouteOutcomeIsInTheGolden(t *testing.T) {
	current := renderOutcomes(t)
	if os.Getenv("UPDATE_CLI_OUTCOMES") == "1" {
		if os.Getenv("CI") != "" {
			t.Fatal("UPDATE_CLI_OUTCOMES is set under CI, which would turn this check into a writer that always passes")
		}
		if err := os.MkdirAll(filepath.Dir(outcomesGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outcomesGolden, []byte(current), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s; read the diff before committing it", outcomesGolden)
		return
	}
	golden, err := os.ReadFile(outcomesGolden)
	if err != nil {
		t.Fatalf("reading %s: %v\nRe-pin with: %s", outcomesGolden, err, outcomesRepin)
	}
	if drift := outcomeDrift(string(golden), current); len(drift) > 0 {
		t.Errorf("what core/cliecho echoes has changed, and %s has not:\n  %s\n\nA + line that isn't \"command\" is an input the terminal now has no line for. That may be the right call, but it has to be made in review, so re-pin with:\n  %s\nand let the diff of the golden say it.",
			outcomesGolden, strings.Join(drift, "\n  "), outcomesRepin)
	}
}

// silentDeclines lists every readable input a builder answers with nil.
// nil is the route-level "can't read this body" answer, so a builder
// that returns it for a body it did read is declining without saying
// why, and the sentence it ends up printing is about something else.
func silentDeclines(t *testing.T) []string {
	t.Helper()
	bySchema := corpusBySchema(t)
	var out []string
	for _, route := range Routes() {
		if routes[route].build == nil {
			continue
		}
		for _, in := range readableInputs(t, route, bySchema) {
			if routes[route].build(in.action) == nil {
				out = append(out, route+" ("+in.label+")")
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestNoBuilderDeclinesAReadableBodySilently(t *testing.T) {
	for _, s := range silentDeclines(t) {
		t.Errorf("%s: the builder returned nil for a body it can read. nil means \"this body won't decode\" and prints the route's `why`, so a deliberate decline has to go through refuse() with its own sentence, declared in the entry's refusals.", s)
	}
}

// And every sentence the corpus reaches is one Gaps() declares. The
// declared-sentence check in verbs_test.go only drives examples and empty
// bodies, so a refusal only a corpus body reaches was invisible to it.
func TestEveryRefusalTheCorpusReachesIsDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, g := range Gaps() {
		declared[g.Why] = true
	}
	bySchema := corpusBySchema(t)
	for _, route := range Routes() {
		for _, in := range readableInputs(t, route, bySchema) {
			if line := Echo(in.action); line.GapDetail != "" && !declared[line.GapDetail] {
				t.Errorf("%s (%s) prints a sentence Gaps() doesn't declare:\n  %s", route, in.label, line.GapDetail)
			}
		}
	}
}

// ------------------------------------------------- mutation self-tests ---
//
// Each of these breaks a real entry for the length of the test and runs
// the whole pipeline, rather than handing the comparison a list somebody
// edited by hand.

// swapRoute replaces one entry until the test ends.
func swapRoute(t *testing.T, route string, e entry) {
	t.Helper()
	saved, had := routes[route]
	t.Cleanup(func() {
		if had {
			routes[route] = saved
		} else {
			delete(routes, route)
		}
	})
	routes[route] = e
}

// The review's mutation: a tier-chain retention silently declined.
func TestOutcomes_MutationASilentDeclineIsCaught(t *testing.T) {
	golden := renderOutcomes(t)
	const route = "PUT /backup-sets/{source}/{set}/retention"
	saved := routes[route]
	mutated := saved
	mutated.build = func(a Action) *cmd {
		if strings.Contains(string(a.Body), `"tiers"`) {
			return nil
		}
		return saved.build(a)
	}
	swapRoute(t, route, mutated)

	if got := silentDeclines(t); len(got) == 0 || !strings.HasPrefix(got[0], route) {
		t.Errorf("a builder returning nil for a tier chain was not reported as a silent decline; got %v", got)
	}
	drift := outcomeDrift(golden, renderOutcomes(t))
	found := false
	for _, d := range drift {
		if strings.HasPrefix(d, "+ "+route+"\t") && strings.Contains(d, "malformed: ") {
			found = true
		}
	}
	if !found {
		t.Errorf("a tier chain going from a command to a decline was not drift; got %v", drift)
	}
}

// A new route with only a `why`, which the #1150 guard counts as answered.
func TestOutcomes_MutationANewWhyOnlyRouteIsCaught(t *testing.T) {
	golden := renderOutcomes(t)
	const route = "POST /a-route-with-only-a-why"
	swapRoute(t, route, entry{why: "there is no verb that does what this button does"})
	drift := outcomeDrift(golden, renderOutcomes(t))
	want := "+ " + route + "\tno command: there is no verb that does what this button does"
	if !strings.Contains(strings.Join(drift, "\n"), want) {
		t.Errorf("a new why-only route did not show up as %q; got %v", want, drift)
	}
}

// A builder that starts refusing with a sentence the golden already has.
func TestOutcomes_MutationAnOldSentenceOnANewRefusalIsCaught(t *testing.T) {
	golden := renderOutcomes(t)
	const route = "POST /backups/{source}/{set}/{name}/recovery"
	saved := routes[route]
	mutated := saved
	mutated.build = func(a Action) *cmd {
		if strings.Contains(string(a.Body), `"kind":"staging"`) {
			return newCmd().refuse(gapRecoveryTarget)
		}
		return saved.build(a)
	}
	swapRoute(t, route, mutated)
	drift := outcomeDrift(golden, renderOutcomes(t))
	found := false
	for _, d := range drift {
		if strings.HasPrefix(d, "- "+route+"\t") && strings.Contains(d, "target.kind=staging: command") {
			found = true
		}
	}
	if !found {
		t.Errorf("staging recoveries starting to refuse, with a sentence the golden already holds, was not drift; got %v", drift)
	}
}
