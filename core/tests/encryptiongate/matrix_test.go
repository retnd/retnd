// Package encryptiongate_test holds EPIC N's conformance matrix, and the
// media the epic owes, to the tree they make claims about (#998).
//
// It has two modes, and the split is deliberate.
//
// Every run of the gate reads the matrix and refuses a row that lies about
// its own status: a PASS that cites no mutation that exists and no date, a
// BY HAND that names nobody, a PARTIAL that does not say what is missing. It
// also holds the capture manifest to the capture script, so a still cannot be
// added to one and forgotten in the other. Rows may still be BLOCKED in this
// mode, because the sub-issues land one pull request at a time and the gate
// has to stay usable while they do.
//
// With RETND_EPIC_N_CLOSURE=1 the same checker runs in closing mode, which is
// what the release job sets (scripts/release/check-epic-n-closed.sh): no row
// may be BLOCKED, every proof a row cites has to resolve, and every file the
// manifest lists has to exist. The epic does not close with a blocked row or a
// missing picture, and this is the check that stops it closing with one.
//
// The checker is a function over text plus a resolver, so the same code runs
// against deliberately broken matrices (TestTheMatrixCheckerCanLie). That is
// the mutation self-test a new guard owes: without it this file would be a
// guard whose only evidence is that it passes on the tree it shipped with.
//
// EPIC T (#1310, suite agility) closes through the same checker (#1320): its
// matrix is docs/conformance/epic-t-matrix.md, read in open mode on every run
// and in closing mode under RETND_EPIC_T_CLOSURE=1, which
// scripts/release/check-epic-t-closed.sh sets. Its captures are entries in the
// same manifest.
package encryptiongate_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	matrixPath      = "docs/conformance/epic-n-matrix.md"
	epicTMatrixPath = "docs/conformance/epic-t-matrix.md"
	manifestPath    = "docs/site/tools/encryption-captures.json"
	scriptPath      = "docs/site/tools/capture-encryption.mjs"
	screensDir      = "docs/site/screens"
)

// selfTests are the files whose planted violations a PASS row may cite, by the
// verbatim label of the mutation in double quotes.
var selfTests = []string{
	"scripts/tests/epic-n-closure.test.sh",
	"scripts/tests/epic-t-closure.test.sh",
	"core/tests/encryptiongate/matrix_test.go",
}

var (
	rowID     = regexp.MustCompile(`^(N[12]\.[0-9]+|NC-[0-9]{2}|NV\.[0-9]+|T[12]\.[0-9]+|TC-[0-9]{2}|TV\.[0-9]+)$`)
	backtick  = regexp.MustCompile("`([^`]+)`")
	proofType = regexp.MustCompile(`^[A-Za-z0-9_./-]+(:Test[A-Za-z0-9_]+)?$`)
	labelRe   = regexp.MustCompile(`"([^"]{12,})"`)
	guardTest = regexp.MustCompile(`:Test[A-Za-z0-9_]*(CanActuallyFail|CanFail|TurnsRed|CanLie|WouldCatch)[A-Za-z0-9_]*$`)
	dateRe    = regexp.MustCompile(`20[0-9]{2}-[0-9]{2}-[0-9]{2}`)
	byHand    = regexp.MustCompile(`by [^,;:()]{3,60} on 20[0-9]{2}-[0-9]{2}-[0-9]{2}`)
	missingRe = regexp.MustCompile(`Missing: .{12,}`)
)

type row struct {
	id, claim, proof, falsif, status, waitsOn string
}

func parseMatrix(text string) []row {
	var rows []row
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(trimmed, "|"), "|"), "|")
		cells := make([]string, 0, len(parts))
		for _, p := range parts {
			cells = append(cells, strings.TrimSpace(p))
		}
		if len(cells) < 6 || !rowID.MatchString(cells[0]) {
			continue
		}
		rows = append(rows, row{cells[0], cells[1], cells[2], cells[3], strings.ToUpper(cells[4]), cells[5]})
	}

	return rows
}

// The status word is the start of the cell: "BLOCKED (#989)" is BLOCKED.
func statusWord(s string) string {
	for _, w := range []string{"BY HAND", "BLOCKED", "PARTIAL", "PASS"} {
		if strings.HasPrefix(s, w) {
			return w
		}
	}

	return s
}

type resolver struct {
	exists   func(ref string) bool
	contains func(path, needle string) bool
}

func proofRefs(cell string) []string {
	var refs []string
	for _, m := range backtick.FindAllStringSubmatch(cell, -1) {
		if tok := strings.TrimSpace(m[1]); proofType.MatchString(tok) && (strings.Contains(tok, "/") || strings.Contains(tok, ":")) {
			refs = append(refs, tok)
		}
	}

	return refs
}

func ranMutation(cell string, r resolver) bool {
	if !dateRe.MatchString(cell) {
		return false
	}
	for _, m := range labelRe.FindAllStringSubmatch(cell, -1) {
		for _, f := range selfTests {
			if r.contains(f, m[1]) {
				return true
			}
		}
	}
	for _, ref := range proofRefs(cell) {
		if guardTest.MatchString(ref) && r.exists(ref) {
			return true
		}
	}

	return false
}

// checkRows returns one finding per way the matrix does not hold. closing adds
// the rules that only bind at the end: no BLOCKED row, and every proof a row
// cites resolves.
func checkRows(rows []row, r resolver, closing bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range rows {
		if seen[x.id] {
			out = append(out, x.id+" appears twice, so one of the two is not being read")
		}
		seen[x.id] = true
		if closing {
			if len(proofRefs(x.proof)) == 0 {
				out = append(out, x.id+" names no executable proof (a backticked path or dir:TestName)")
			}
			for _, ref := range proofRefs(x.proof) {
				if !r.exists(ref) {
					out = append(out, x.id+" cites "+ref+", which does not resolve")
				}
			}
		}
		switch statusWord(x.status) {
		case "PASS":
			if !ranMutation(x.falsif, r) {
				out = append(out, x.id+" says PASS but its falsification carries no date, or cites no mutation label that occurs in a self-test and no guard test that plants the violation itself")
			}
		case "BY HAND":
			if !byHand.MatchString(x.falsif) {
				out = append(out, x.id+" is BY HAND but does not say who checked it and when (by <name> on YYYY-MM-DD)")
			}
		case "PARTIAL":
			if !missingRe.MatchString(x.falsif) {
				out = append(out, x.id+" is PARTIAL but does not say what is missing (Missing: ...)")
			}
			if x.waitsOn == "" || x.waitsOn == "-" {
				out = append(out, x.id+" is PARTIAL but names nothing it waits on")
			}
		case "BLOCKED":
			if closing {
				out = append(out, x.id+" is BLOCKED, and this epic does not close with a blocked row")
			}
		default:
			out = append(out, x.id+" has status "+x.status+"; a row is PASS, BY HAND, PARTIAL or BLOCKED and nothing else")
		}
	}
	sort.Strings(out)

	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		t.Fatalf("%s does not look like the repository root: %v", root, err)
	}

	return root
}

func repoResolver(root string) resolver {
	return resolver{
		exists: func(ref string) bool {
			dir, name, isGo := strings.Cut(ref, ":")
			if !isGo {
				_, err := os.Stat(filepath.Join(root, ref))

				return err == nil
			}
			entries, err := os.ReadDir(filepath.Join(root, dir))
			if err != nil {
				return false
			}
			decl := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(`)
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
					continue
				}
				if body, err := os.ReadFile(filepath.Join(root, dir, e.Name())); err == nil && decl.Match(body) {
					return true
				}
			}

			return false
		},
		contains: func(path, needle string) bool {
			body, err := os.ReadFile(filepath.Join(root, path))

			return err == nil && strings.Contains(string(body), needle)
		},
	}
}

// ---- the capture manifest ----

type variant struct {
	Suffix string `json:"suffix"`
	Theme  string `json:"theme"`
	Width  int    `json:"width"`
}

type media struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Scenario string `json:"scenario"`
	State    string `json:"state"`
	Alt      string `json:"alt"`
	Caption  string `json:"caption"`
	Reason   string `json:"reason"`
}

type manifest struct {
	Variants []variant `json:"variants"`
	Stills   []media   `json:"stills"`
	GIFs     []media   `json:"gifs"`
}

func readManifest(t *testing.T, root string) manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, manifestPath))
	if err != nil {
		t.Fatalf("reading %s: %v", manifestPath, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s is not valid JSON: %v", manifestPath, err)
	}

	return m
}

var (
	stillName = regexp.MustCompile(`^encryption-[0-9]{2}-[a-z0-9-]+$`)
	gifName   = regexp.MustCompile(`^encryption-[a-z0-9-]+$`)
)

// manifestProblems is the manifest held to itself and to the script. script
// is the text of the capture script; files is the set of names present in the
// screens directory ("" disables the presence rule).
func manifestProblems(m manifest, script string, files map[string]bool, requirePresent bool) []string {
	var out []string
	names := map[string]bool{}
	ids := map[string]bool{}
	add := func(kind string, x media, re *regexp.Regexp) {
		if !re.MatchString(x.Name) {
			out = append(out, kind+" "+x.ID+" has the file name "+x.Name+", which is not of the form the script writes")
		}
		if names[x.Name] || ids[x.ID] {
			out = append(out, kind+" "+x.ID+" repeats a name or an id")
		}
		names[x.Name], ids[x.ID] = true, true
		if strings.TrimSpace(x.Alt) == "" || strings.TrimSpace(x.Caption) == "" {
			out = append(out, kind+" "+x.ID+" has no alt text or caption, so #997 has nothing to embed it with")
		}
		switch x.State {
		case "capturable", "":
			if x.State == "" && kind == "still" {
				out = append(out, "still "+x.ID+" has no state")
			}
		case "needs-ui-state":
			if len(strings.TrimSpace(x.Reason)) < 20 {
				out = append(out, "still "+x.ID+" needs a UI state and does not say which")
			}
		default:
			out = append(out, kind+" "+x.ID+" has state "+x.State)
		}
		if x.State == "capturable" && !strings.Contains(script, `"`+x.ID+`":`) {
			out = append(out, kind+" "+x.ID+" is capturable but "+scriptPath+" has no \""+x.ID+"\": step for it")
		}
	}
	for _, s := range m.Stills {
		add("still", s, stillName)
	}
	for _, g := range m.GIFs {
		g.State = "capturable"
		add("gif", g, gifName)
	}
	if len(m.Variants) != 4 {
		out = append(out, "the manifest does not list the four variants (light, dark, narrow, narrow dark)")
	}
	themes := map[string]bool{}
	narrow := false
	for _, v := range m.Variants {
		themes[v.Theme] = true
		narrow = narrow || v.Width == 360
	}
	if !themes["light"] || !themes["dark"] || !narrow {
		out = append(out, "the variants must cover light, dark and a 360px window")
	}
	if requirePresent {
		for _, f := range expectedFiles(m) {
			if !files[f] {
				out = append(out, "missing: "+screensDir+"/"+f)
			}
		}
	}
	sort.Strings(out)

	return out
}

func expectedFiles(m manifest) []string {
	var out []string
	for _, s := range m.Stills {
		for _, v := range m.Variants {
			out = append(out, s.Name+v.Suffix+".png")
		}
	}
	for _, g := range m.GIFs {
		out = append(out, g.Name+".gif")
	}

	return out
}

func screensPresent(root string) map[string]bool {
	have := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(root, screensDir))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.Size() < 100 {
			continue
		}
		head := make([]byte, 6)
		f, err := os.Open(filepath.Join(root, screensDir, e.Name()))
		if err != nil {
			continue
		}
		_, _ = f.Read(head)
		_ = f.Close()
		if strings.HasPrefix(string(head), "\x89PNG") || strings.HasPrefix(string(head), "GIF8") {
			have[e.Name()] = true
		}
	}

	return have
}

// storeScreenshotProblems: the mock captures are documentation and never a
// store listing, so docs/submission must not point at them.
func storeScreenshotProblems(files map[string]string) []string {
	var out []string
	for p, text := range files {
		if strings.Contains(text, "screens/encryption-") {
			out = append(out, p+" points at a documentation capture; store screenshots are of real hardware and are never generated here")
		}
	}
	sort.Strings(out)

	return out
}

// ---- the tests ----

func TestTheMatrixCheckerCanLie(t *testing.T) {
	good := func() []row {
		var rows []row
		for i := 1; i <= 8; i++ {
			rows = append(rows, row{fmt.Sprintf("N1.%d", i), "c", "`pkg:TestReal`",
				"Planted control \"a real planted mutation\", run 2026-11-09.", "PASS", "-"})
		}

		return rows
	}
	res := resolver{
		exists:   func(ref string) bool { return ref == "pkg:TestReal" || ref == "pkg:TestTheGuardCanActuallyFail" },
		contains: func(path, needle string) bool { return path == selfTests[0] && needle == "a real planted mutation" },
	}
	for _, closing := range []bool{false, true} {
		if got := checkRows(good(), res, closing); len(got) != 0 {
			t.Fatalf("the control matrix should be clean (closing=%v): %v", closing, got)
		}
	}
	lies := []struct {
		name    string
		mutate  func(r []row) []row
		want    string
		closing bool
	}{
		{"a duplicated row", func(r []row) []row { return append(r, r[0]) }, "appears twice", false},
		{"a PASS with no date", func(r []row) []row { r[1].falsif = "Planted control \"a real planted mutation\"."; return r }, "says PASS", false},
		{"a PASS citing a mutation nobody wrote", func(r []row) []row {
			r[1].falsif = "Planted control \"an invented mutation label\", run 2026-11-09."
			return r
		}, "says PASS", false},
		{"a BY HAND row that names nobody", func(r []row) []row { r[2].status, r[2].falsif = "BY HAND", "looked fine"; return r }, "who checked it", false},
		{"a PARTIAL row that says nothing is missing", func(r []row) []row { r[2].status, r[2].falsif = "PARTIAL", "mostly"; return r }, "what is missing", false},
		{"a status that is none of the four", func(r []row) []row { r[3].status = "DONE"; return r }, "has status", false},
		{"a BLOCKED row while closing", func(r []row) []row { r[4].status = "BLOCKED (#989)"; return r }, "does not close with a blocked row", true},
		{"a proof that does not resolve while closing", func(r []row) []row { r[5].proof = "`pkg:TestGone`"; return r }, "does not resolve", true},
		{"a row with no proof while closing", func(r []row) []row { r[5].proof = "none yet"; return r }, "names no executable proof", true},
	}
	for _, l := range lies {
		got := checkRows(l.mutate(good()), res, l.closing)
		if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), l.want) {
			t.Errorf("%s was not refused with %q: %v", l.name, l.want, got)
		}
	}
	// Negative control for the two closing-only rules: the same lies are
	// accepted while the epic is still open, which is what keeps the gate
	// usable between pull requests.
	open := good()
	open[4].status = "BLOCKED (#989)"
	open[5].proof = "`pkg:TestGone`"
	if got := checkRows(open, res, false); len(got) != 0 {
		t.Errorf("a BLOCKED row or an unresolved proof was refused before closing: %v", got)
	}
}

// EPIC T (#1310) closes through the same checker (#1320): its matrix carries
// the T1.n and T2.n exit-gate rows, the TC-nn consensus rows and the TV.n
// guard rows, and the parser has to read them or a BLOCKED T row is simply
// not seen. This feeds a one-row EPIC T table through the parser, so a rowID
// that forgets a family fails here and not at release time.
func TestTheCheckerReadsEpicTRowsAndRefusesABlockedOneWhileClosing(t *testing.T) {
	res := resolver{
		exists:   func(ref string) bool { return ref == "pkg:TestReal" },
		contains: func(path, needle string) bool { return false },
	}
	for _, id := range []string{"T1.1", "T2.5", "TC-01", "TV.12"} {
		table := "| ID | Claim | Proof | Falsification | Status | Waits on |\n|---|---|---|---|---|---|\n" +
			"| " + id + " | c | `pkg:TestReal` | not yet | BLOCKED (#1320) | #1320 |\n"
		rows := parseMatrix(table)
		if len(rows) != 1 {
			t.Fatalf("the parser read %d rows out of a one-row EPIC T table with id %s; it has to read every EPIC T family", len(rows), id)
		}
		got := strings.Join(checkRows(rows, res, true), "\n")
		if !strings.Contains(got, "does not close with a blocked row") {
			t.Errorf("a BLOCKED %s row was not refused while closing: %q", id, got)
		}
		if open := checkRows(rows, res, false); len(open) != 0 {
			t.Errorf("a BLOCKED %s row was refused before closing: %v", id, open)
		}
	}
}

func TestTheEpicTMatrixDoesNotMisstateAStatus(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, epicTMatrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", epicTMatrixPath, err)
	}
	rows := parseMatrix(string(raw))
	if len(rows) == 0 {
		t.Fatalf("%s parsed to no rows, so the parser or the file is wrong", epicTMatrixPath)
	}
	for _, p := range checkRows(rows, repoResolver(root), false) {
		t.Error(p)
	}
}

func TestTheMatrixParserReadsTheRealTablesRatherThanTheirShape(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", matrixPath, err)
	}
	rows := parseMatrix(string(raw))
	// How many rows there should be is the specification's to say, and
	// core/tests/compat:TestTheMatrixHasARowPerGateLine already holds the matrix
	// to it (one row per exit-gate line, one per consensus finding, one per
	// guard). What this parser has to show is that it read every family and
	// lost nothing in the middle of one: each of the four families is present
	// and numbered 1..n without a gap.
	family := regexp.MustCompile(`^(N1|N2|NC|NV)[.-]0*([0-9]+)$`)
	seen := map[string][]int{}
	for _, r := range rows {
		if m := family.FindStringSubmatch(r.id); m != nil {
			var n int
			_, _ = fmt.Sscanf(m[2], "%d", &n)
			seen[m[1]] = append(seen[m[1]], n)
		}
	}
	for _, f := range []string{"N1", "N2", "NC", "NV"} {
		ns := seen[f]
		if len(ns) == 0 {
			t.Fatalf("%s parsed to no %s rows, so the parser or the file is wrong", matrixPath, f)
		}
		sort.Ints(ns)
		for i, n := range ns {
			if n != i+1 {
				t.Fatalf("%s rows %s are not numbered 1..%d without a gap (found %v), so a row was lost or the parser stopped reading", matrixPath, f, len(ns), ns)
			}
		}
	}
	if got := parseMatrix("| ID | Claim |\n|---|---|\n| N9 | x |\n"); len(got) != 0 {
		t.Fatalf("a table that is not the matrix parsed to rows: %v", got)
	}
}

func TestTheRealMatrixDoesNotMisstateAStatus(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", matrixPath, err)
	}
	for _, p := range checkRows(parseMatrix(string(raw)), repoResolver(root), false) {
		t.Error(p)
	}
}

func TestTheManifestCheckCanLie(t *testing.T) {
	script := `"a-still": async () => {}, "a-gif": async () => {}`
	good := func() manifest {
		return manifest{
			Variants: []variant{{"", "light", 1280}, {"-dark", "dark", 1280}, {"-narrow", "light", 360}, {"-narrow-dark", "dark", 360}},
			Stills:   []media{{ID: "a-still", Name: "encryption-01-a-still", State: "capturable", Alt: "a", Caption: "c"}},
			GIFs:     []media{{ID: "a-gif", Name: "encryption-a-gif", Alt: "a", Caption: "c"}},
		}
	}
	all := map[string]bool{}
	for _, f := range expectedFiles(good()) {
		all[f] = true
	}
	if got := manifestProblems(good(), script, all, true); len(got) != 0 {
		t.Fatalf("the control manifest should be clean: %v", got)
	}
	lies := []struct {
		name   string
		mutate func(m *manifest)
		script string
		files  map[string]bool
		want   string
	}{
		{"a still the script does not take", func(m *manifest) {}, `"a-gif":`, all, "has no \"a-still\""},
		{"a name of the wrong shape", func(m *manifest) { m.Stills[0].Name = "shot1" }, script, all, "not of the form"},
		{"a duplicated id", func(m *manifest) { m.Stills = append(m.Stills, m.Stills[0]) }, script, all, "repeats"},
		{"no alt text", func(m *manifest) { m.Stills[0].Alt = "" }, script, all, "no alt text"},
		{"a needs-ui-state still with no reason", func(m *manifest) { m.Stills[0].State = "needs-ui-state" }, script, all, "does not say which"},
		{"a variant dropped", func(m *manifest) { m.Variants = m.Variants[:3] }, script, all, "four variants"},
		{"a picture missing", func(m *manifest) {}, script, map[string]bool{}, "missing: docs/site/screens/encryption-01-a-still.png"},
	}
	for _, l := range lies {
		m := good()
		l.mutate(&m)
		got := strings.Join(manifestProblems(m, l.script, l.files, true), "\n")
		if !strings.Contains(got, l.want) {
			t.Errorf("%s was not refused with %q: %s", l.name, l.want, got)
		}
	}
	// Negative control: a missing picture is not a finding before closing.
	if got := manifestProblems(good(), script, map[string]bool{}, false); len(got) != 0 {
		t.Errorf("a missing picture was refused before closing: %v", got)
	}
	if got := storeScreenshotProblems(map[string]string{"docs/submission/x.md": "![a](../site/screens/encryption-01-a.png)"}); len(got) != 1 {
		t.Errorf("a store listing pointing at a documentation capture was not refused: %v", got)
	}
	if got := storeScreenshotProblems(map[string]string{"docs/submission/x.md": "nothing here"}); len(got) != 0 {
		t.Errorf("an innocent listing was refused: %v", got)
	}
}

func TestTheManifestMatchesTheCaptureScript(t *testing.T) {
	root := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(root, scriptPath))
	if err != nil {
		t.Fatalf("reading %s: %v", scriptPath, err)
	}
	for _, p := range manifestProblems(readManifest(t, root), string(script), nil, false) {
		t.Error(p)
	}
}

// TestEpicTIsClosed is EPIC T's closing check (#1320), run by
// scripts/release/check-epic-t-closed.sh with RETND_EPIC_T_CLOSURE=1: no row
// of docs/conformance/epic-t-matrix.md is BLOCKED, every proof a row cites
// resolves, every PASS cites a mutation that exists. The captures EPIC T owes
// are entries in the same manifest, so TestEpicNIsClosed's presence rule
// already covers them and this does not repeat it.
func TestEpicTIsClosed(t *testing.T) {
	if os.Getenv("RETND_EPIC_T_CLOSURE") != "1" {
		t.Skip("closing check: set RETND_EPIC_T_CLOSURE=1 (scripts/release/check-epic-t-closed.sh does)")
	}
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, epicTMatrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", epicTMatrixPath, err)
	}
	for _, p := range checkRows(parseMatrix(string(raw)), repoResolver(root), true) {
		t.Error(p)
	}
}

// TestEpicNIsClosed is the closing check. It is skipped unless the release job
// (or a person) sets RETND_EPIC_N_CLOSURE=1, so the gate stays usable while the
// epic's pull requests land one at a time.
func TestEpicNIsClosed(t *testing.T) {
	if os.Getenv("RETND_EPIC_N_CLOSURE") != "1" {
		t.Skip("closing check: set RETND_EPIC_N_CLOSURE=1 (scripts/release/check-epic-n-closed.sh does)")
	}
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", matrixPath, err)
	}
	for _, p := range checkRows(parseMatrix(string(raw)), repoResolver(root), true) {
		t.Error(p)
	}
	script, err := os.ReadFile(filepath.Join(root, scriptPath))
	if err != nil {
		t.Fatalf("reading %s: %v", scriptPath, err)
	}
	for _, p := range manifestProblems(readManifest(t, root), string(script), screensPresent(root), true) {
		t.Error(p)
	}
	docs := map[string]string{}
	entries, _ := os.ReadDir(filepath.Join(root, "docs/submission"))
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join(root, "docs/submission", e.Name())); err == nil {
			docs["docs/submission/"+e.Name()] = string(b)
		}
	}
	for _, p := range storeScreenshotProblems(docs) {
		t.Error(p)
	}
}
