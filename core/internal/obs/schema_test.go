package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// FR-100 (#900, EPIC O): the log line is a schema, and this file is where
// the schema is held.
//
// Everything a Loki pipeline, a jq filter or a dashboard parses out of a
// line is a field NAME, and until this file existed only half of that
// contract was guarded: events_test.go pins the event names, and nothing
// pinned the keys around them. Renaming action_id is exactly as breaking
// as renaming cycle_start, so it gets the same treatment, with the same
// rule: the pin is typed out again here as literals, never read back from
// the production constant it is guarding.

// wantReserved is the wire contract. time, level, msg and event are on
// every line; result, action and action_id are on a marked line (action.go);
// backup_set is on any line with a backup set in scope.
var wantReserved = []string{"time", "level", "msg", "event", "result", "action", "action_id", "backup_set"}

// everyLine is the part of the contract every line carries, whatever it is.
var everyLine = []string{"event", "level", "msg", "time"}

func TestReservedKeysArePinned(t *testing.T) {
	got := append([]string(nil), ReservedKeys...)
	want := append([]string(nil), wantReserved...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ReservedKeys = %v, want exactly %v: adding, removing or renaming a reserved log key is a breaking change for every log consumer (docs/observability.md)", ReservedKeys, wantReserved)
	}
}

// TestTheReservedKeysAreTheStringsOnTheWire holds ReservedKeys to what
// emit really writes, so the list cannot drift from the line the way a
// doc comment can.
func TestTheReservedKeysAreTheStringsOnTheWire(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelDebug)
	ctx := WithBackupSet(context.Background(), "alpha")
	a := l.Begin(ctx, "probe_begin", ActionCycle, "begin")
	a.Succeeded(ctx, "done")
	seen := map[string]bool{}
	for _, line := range decodeLines(t, &buf) {
		for k := range line {
			seen[k] = true
		}
	}
	for _, k := range wantReserved {
		if !seen[k] {
			t.Errorf("a marked line with a backup set in scope carries no %q key; keys seen: %v", k, keysOf(seen))
		}
	}
	delete(seen, "duration") // End's own per-event attr, not a reserved key
	if len(seen) != len(wantReserved) {
		t.Errorf("a marked line carries keys beyond the eight reserved ones: %v", keysOf(seen))
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// lineSpec is what one corpus line is allowed to carry: the reserved keys
// it must have, and its own per-event attrs, which FR-100 explicitly
// allows and which therefore are never a finding.
type lineSpec struct {
	must []string
	own  []string
}

// problems is the schema check itself, kept apart from the logger so the
// mutation tests below can feed it doctored lines and watch it object.
// Every message names the key, because a red that does not say which key
// moved sends the reader to a diff.
func (s lineSpec) problems(line map[string]any) []string {
	var out []string
	for _, k := range s.must {
		if _, ok := line[k]; !ok {
			out = append(out, "reserved key "+strconv.Quote(k)+" is missing")
		}
	}
	allowed := map[string]bool{}
	for _, k := range wantReserved {
		allowed[k] = true
	}
	for _, k := range s.own {
		allowed[k] = true
	}
	for k := range line {
		if !allowed[k] {
			out = append(out, "unreserved key "+strconv.Quote(k)+" is on the line (a new reserved key, or a renamed one)")
		}
	}
	sort.Strings(out)
	return out
}

type corpusEntry struct {
	name string
	spec lineSpec
	emit func(l *Logger, ctx context.Context)
}

func corpus() []corpusEntry {
	marked := lineSpec{must: []string{"time", "level", "msg", "event", "result", "action", "action_id", "backup_set"}}
	opens := lineSpec{must: []string{"time", "level", "msg", "event", "action", "action_id", "backup_set"}}
	return []corpusEntry{
		{"action opens", opens, func(l *Logger, ctx context.Context) {
			l.Begin(ctx, "probe_begin", ActionCycle, "begin")
		}},
		{"action closes with a result", lineSpec{must: marked.must, own: []string{"duration"}}, func(l *Logger, ctx context.Context) {
			l.Begin(ctx, "probe_begin", ActionCycle, "begin").Succeeded(ctx, "done")
		}},
		{"completion without an action", lineSpec{must: []string{"time", "level", "msg", "event", "result", "backup_set"}}, func(l *Logger, ctx context.Context) {
			l.Completed(ctx, ResultWarn, "probe_done", "finished with a warning")
		}},
		{"plain event carries only the four", lineSpec{must: everyLine}, func(l *Logger, _ context.Context) {
			l.Event(context.Background(), LevelInfo, "probe_plain", "plain")
		}},
		{"per-event attrs are allowed", lineSpec{must: everyLine, own: []string{"probe_attr"}}, func(l *Logger, _ context.Context) {
			l.Event(context.Background(), LevelInfo, "probe_attr", "with an attr", slog.String("probe_attr", "x"))
		}},
	}
}

var actionIDValue = regexp.MustCompile(`^[0-9a-f]{1,16}$`)

// normalise replaces the two values that differ run to run, after
// checking they have the right shape, so the golden file can hold the rest
// byte for byte.
func normalise(t *testing.T, line map[string]any) map[string]any {
	t.Helper()
	if _, ok := line["time"].(string); !ok {
		t.Fatalf("line has no string time: %v", line)
	}
	line["time"] = "<time>"
	if _, ok := line["duration"]; ok {
		line["duration"] = "<duration>"
	}
	if id, ok := line["action_id"]; ok {
		s, _ := id.(string)
		if !actionIDValue.MatchString(s) {
			t.Fatalf("action_id %v is not a short hex id", id)
		}
		line["action_id"] = "<action_id>"
	}
	return line
}

func TestGoldenLines(t *testing.T) {
	var got []string
	for _, c := range corpus() {
		var buf bytes.Buffer
		l := New(&buf, LevelDebug)
		c.emit(l, WithBackupSet(context.Background(), "alpha"))
		lines := decodeLines(t, &buf)
		last := lines[len(lines)-1]
		if p := c.spec.problems(last); len(p) > 0 {
			t.Errorf("%s: %v", c.name, p)
		}
		b, err := json.Marshal(normalise(t, last)) // json.Marshal sorts keys
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c.name+"\t"+string(b))
	}
	path := filepath.Join("testdata", "log_schema.golden")
	body := strings.Join(got, "\n") + "\n"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the golden fixture: %v (UPDATE_GOLDEN=1 writes it)", err)
	}
	wl := strings.Split(strings.TrimRight(string(want), "\n"), "\n")
	if len(wl) != len(got) {
		t.Fatalf("golden has %d lines, corpus has %d", len(wl), len(got))
	}
	for i := range got {
		if got[i] == wl[i] {
			continue
		}
		var g, w map[string]any
		_ = json.Unmarshal([]byte(strings.SplitN(got[i], "\t", 2)[1]), &g)
		_ = json.Unmarshal([]byte(strings.SplitN(wl[i], "\t", 2)[1]), &w)
		for k := range g {
			if _, ok := w[k]; !ok {
				t.Errorf("%s: key %q appeared on the line and is not in the golden fixture", strings.SplitN(got[i], "\t", 2)[0], k)
			}
		}
		for k := range w {
			if _, ok := g[k]; !ok {
				t.Errorf("%s: key %q is in the golden fixture and no longer on the line", strings.SplitN(got[i], "\t", 2)[0], k)
			}
		}
		for k := range g {
			if w[k] != nil && g[k] != w[k] {
				t.Errorf("%s: key %q changed value: got %v, golden has %v", strings.SplitN(got[i], "\t", 2)[0], k, g[k], w[k])
			}
		}
	}
}

// The three drift shapes FR-100 names, planted into a real line and run
// through the check. Each must be refused and must name the key; this is
// what stops the check being one that cannot fail (epic-checklist.md
// section 12). The same three are planted into the production code by hand
// for the matrix row (docs/conformance/epic-o-matrix.md, O-07).
func realMarkedLine(t *testing.T) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	l := New(&buf, LevelDebug)
	ctx := WithBackupSet(context.Background(), "alpha")
	l.Begin(ctx, "probe_begin", ActionCycle, "begin").Succeeded(ctx, "done")
	lines := decodeLines(t, &buf)
	return lines[len(lines)-1]
}

func TestTheCheckRefusesAddingARenamingAndDroppingAReservedKey(t *testing.T) {
	spec := corpus()[1].spec
	for _, tc := range []struct {
		name   string
		mutate func(m map[string]any)
		names  string
	}{
		{"a ninth reserved key", func(m map[string]any) { m["trace_id"] = "x" }, "trace_id"},
		{"action_id renamed to actionId", func(m map[string]any) { m["actionId"] = m["action_id"]; delete(m, "action_id") }, "actionId"},
		{"result dropped from a marked event", func(m map[string]any) { delete(m, "result") }, "result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := realMarkedLine(t)
			if p := spec.problems(m); len(p) != 0 {
				t.Fatalf("the undoctored line is already refused: %v", p)
			}
			tc.mutate(m)
			p := spec.problems(m)
			if len(p) == 0 {
				t.Fatalf("a line with %s was accepted", tc.name)
			}
			if !strings.Contains(strings.Join(p, " "), strconv.Quote(tc.names)) {
				t.Errorf("the refusal %v does not name %q", p, tc.names)
			}
		})
	}
}

// The positive control: a per-event attr is NOT a reserved key, and FR-100
// allows them. A fixture that refused one would forbid the additions the
// schema permits.
func TestAPerEventAttrIsNotAReservedKeyAndStaysGreen(t *testing.T) {
	m := realMarkedLine(t)
	m["probe_attr"] = "x"
	spec := corpus()[1].spec
	spec.own = append(spec.own, "probe_attr")
	if p := spec.problems(m); len(p) != 0 {
		t.Errorf("a per-event attr was refused: %v", p)
	}
}

// A ninth reserved key stamped by emit itself lands on every line, which
// is a different shape from one helper growing an attr. This checks the
// real helpers: whatever key is on ALL of them, beyond the four every line
// carries and the marks, has been added by the logger and is a new reserved
// key. A per-event attr (on one helper only) stays green.
func TestNoUndeclaredKeyIsStampedOnEveryHelperLine(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelDebug)
	ctx := context.Background()
	l.Startup(ctx, "1.2.3", "deadbeef", "go1.27")
	l.RcloneVersion(ctx, "v1.75.0")
	l.Retry(ctx, "upload", 1, "transient", errProbe{})
	l.Event(ctx, LevelInfo, "probe_plain", "plain")
	lines := decodeLines(t, &buf)
	if len(lines) < 4 {
		t.Fatalf("got %d lines, want at least 4", len(lines))
	}
	common := map[string]int{}
	for _, line := range lines {
		for k := range line {
			common[k]++
		}
	}
	for k, n := range common {
		if n != len(lines) {
			continue
		}
		ok := false
		for _, e := range everyLine {
			ok = ok || e == k
		}
		if !ok {
			t.Errorf("key %q is on every line but is not one of %v: a new reserved key has been added", k, everyLine)
		}
	}
}

type errProbe struct{}

func (errProbe) Error() string { return "probe" }

// TestEveryEventConstantIsPinnedByName closes the gap that predates EPIC
// O: EventSnapshotPhase and EventSnapshotStats were in events.go and in no
// table. It reads the source, so a 54th constant added without a pin is a
// red here rather than a name nobody noticed.
func TestEveryEventConstantIsPinnedByName(t *testing.T) {
	pinned := map[string]string{}
	for _, c := range eventNameCases {
		pinned[c.name] = c.want
	}
	for name, pair := range assuranceNameCases {
		pinned[name] = pair[1]
	}
	declared := map[string]string{}
	fset := token.NewFileSet()
	for _, f := range []string{"events.go", "assurance_events.go"} {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, id := range vs.Names {
				if !strings.HasPrefix(id.Name, "Event") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					declared[id.Name] = v
				}
			}
			return true
		})
	}
	if len(declared) != 53 {
		t.Errorf("events.go and assurance_events.go declare %d Event* constants, the spec and docs/observability.md say 53; update both together", len(declared))
	}
	for name, v := range declared {
		want, ok := pinned[name]
		if !ok {
			t.Errorf("%s (%q) is declared and pinned by no table", name, v)
			continue
		}
		if want != v {
			t.Errorf("%s = %q, pinned as %q", name, v, want)
		}
	}
}

func TestStartupCarriesTheSchemaVersion(t *testing.T) {
	l, buf := newRecorder(t)
	l.Startup(context.Background(), "1.2.3", "deadbeef", "go1.27")
	got := decodeLines(t, buf)[0]
	if got["log_schema"] != "1" {
		t.Errorf("startup log_schema = %v, want \"1\": the schema version is on the startup event and in the shipped stream label, never on every line", got["log_schema"])
	}
	if LogSchemaVersion != "1" {
		t.Errorf("LogSchemaVersion = %q, want \"1\"", LogSchemaVersion)
	}
}
