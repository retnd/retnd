// Package agentgate_test holds EPIC U's conformance matrix to the tree it makes
// claims about (#1353): docs/conformance/epic-u-matrix.md, the claims of the MCP
// server for agents (#1343), each tied to the proof that goes red and the
// mutation that was run and watched to fail.
//
// It has two modes, and the split is the one core/tests/encryptiongate has.
//
// Every run of the gate reads the matrix and refuses a row that lies about its
// own status: a PASS that cites no mutation that exists and no date, a BY HAND
// that names nobody, a PARTIAL that does not say what is missing. Rows may
// still be BLOCKED in this mode, because the sub-issues land one pull request
// at a time and the gate has to stay usable while they do.
//
// With RETND_EPIC_U_CLOSURE=1 the same checker runs in closing mode, which is
// what scripts/release/check-epic-u-closed.sh sets: no row may be BLOCKED, every
// proof a row cites has to resolve, and every PASS has to name a mutation. The
// epic does not close with a blocked row, and this is the check that stops it.
//
// The checker is a function over text plus a resolver, so the same code runs
// against deliberately broken matrices (TestTheMatrixCheckerCanLie). That is the
// mutation self-test a new guard owes. The checker is a copy of
// encryptiongate's rather than an import because a _test package cannot be
// imported; the two are held to the same lie table on purpose.
//
// core/tests/compat holds the matrix to the specification (one row per exit gate
// line, one UV row per guard, a spec box ticked exactly when its row is PASS).
// This package holds it to itself and to the files it cites.
package agentgate_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const matrixPath = "docs/conformance/epic-u-matrix.md"

// selfTests are the files whose planted violations a PASS row may cite, by the
// verbatim label of the mutation in double quotes.
var selfTests = []string{
	"scripts/tests/epic-u-closure.test.sh",
	"core/tests/agentgate/matrix_test.go",
	"scripts/bdtools/agents/controls_tokens.py",
	"scripts/bdtools/agents/controls_catalogue.py",
	"scripts/bdtools/agents/controls_audit.py",
	"scripts/bdtools/agents/controls_design.py",
	"scripts/bdtools/agents/controls_confirm.py",
	"scripts/bdtools/agents/controls_revision.py",
	"scripts/bdtools/agents/controls_ui.py",
}

var (
	rowID     = regexp.MustCompile(`^(U[12]\.[0-9]+|UC-[0-9]{2}|UV\.[0-9]+)$`)
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

// The status word is the start of the cell: "BLOCKED (#1343)" is BLOCKED.
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

func readMatrix(t *testing.T, root string) []row {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", matrixPath, err)
	}

	return parseMatrix(string(raw))
}

func TestTheMatrixCheckerCanLie(t *testing.T) {
	good := func() []row {
		var rows []row
		for i := 1; i <= 8; i++ {
			rows = append(rows, row{fmt.Sprintf("U1.%d", i), "c", "`pkg:TestReal`",
				"Planted control \"a real planted mutation\", run 2026-11-11.", "PASS", "-"})
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
		{"an undated PASS", func(r []row) []row { r[1].falsif = "Planted control \"a real planted mutation\"."; return r }, "says PASS", false},
		{"a PASS citing a mutation nobody wrote", func(r []row) []row {
			r[1].falsif = "Planted control \"an invented mutation label\", run 2026-11-11."
			return r
		}, "says PASS", false},
		{"a PASS naming no mutation at all", func(r []row) []row { r[1].falsif = "run 2026-11-11, it failed."; return r }, "says PASS", false},
		{"a BY HAND row that names nobody", func(r []row) []row { r[2].status, r[2].falsif = "BY HAND", "looked fine"; return r }, "who checked it", false},
		{"a PARTIAL row that says nothing is missing", func(r []row) []row { r[2].status, r[2].falsif = "PARTIAL", "mostly"; return r }, "what is missing", false},
		{"a status that is none of the four", func(r []row) []row { r[3].status = "DONE"; return r }, "has status", false},
		{"a BLOCKED row while closing", func(r []row) []row { r[4].status = "BLOCKED (#1343)"; return r }, "does not close with a blocked row", true},
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
	open[4].status = "BLOCKED (#1343)"
	open[5].proof = "`pkg:TestGone`"
	if got := checkRows(open, res, false); len(got) != 0 {
		t.Errorf("a BLOCKED row or an unresolved proof was refused before closing: %v", got)
	}
}

// The parser has to read every family of EPIC U's rows (the U1.n and U2.n gate
// lines, the UC-nn consensus rows, the UV.n ledger) or a BLOCKED row of the
// family it forgets is simply not seen. This feeds a one-row table per family
// through it, and a table that is not the matrix through it too.
func TestTheCheckerReadsEveryEpicUFamilyAndRefusesABlockedOneWhileClosing(t *testing.T) {
	res := resolver{
		exists:   func(ref string) bool { return ref == "pkg:TestReal" },
		contains: func(path, needle string) bool { return false },
	}
	for _, id := range []string{"U1.1", "U2.5", "UC-01", "UV.21"} {
		table := "| ID | Claim | Proof | Falsification | Status | Waits on |\n|---|---|---|---|---|---|\n" +
			"| " + id + " | c | `pkg:TestReal` | not yet | BLOCKED (#1343) | #1353 |\n"
		rows := parseMatrix(table)
		if len(rows) != 1 {
			t.Fatalf("the parser read %d rows out of a one-row table with id %s; it has to read every EPIC U family", len(rows), id)
		}
		if got := strings.Join(checkRows(rows, res, true), "\n"); !strings.Contains(got, "does not close with a blocked row") {
			t.Errorf("a BLOCKED %s row was not refused while closing: %q", id, got)
		}
		if open := checkRows(rows, res, false); len(open) != 0 {
			t.Errorf("a BLOCKED %s row was refused before closing: %v", id, open)
		}
	}
	if got := parseMatrix("| ID | Claim |\n|---|---|\n| N9 | x |\n| T1.1 | y | z | w | PASS | - |\n"); len(got) != 0 {
		t.Fatalf("a table that is not EPIC U's matrix parsed to rows: %v", got)
	}
}

func TestTheMatrixParserReadsTheRealTableRatherThanItsShape(t *testing.T) {
	rows := readMatrix(t, repoRoot(t))
	// How many rows there should be is the specification's to say, and
	// core/tests/compat holds the matrix to it. What this parser has to show
	// is that it read every family and lost nothing in the middle of one: each
	// is present and numbered 1..n without a gap.
	family := regexp.MustCompile(`^(U1|U2|UC|UV)[.-]0*([0-9]+)$`)
	seen := map[string][]int{}
	for _, r := range rows {
		if m := family.FindStringSubmatch(r.id); m != nil {
			var n int
			_, _ = fmt.Sscanf(m[2], "%d", &n)
			seen[m[1]] = append(seen[m[1]], n)
		}
	}
	for _, f := range []string{"U1", "U2", "UC", "UV"} {
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
}

func TestTheEpicUMatrixDoesNotMisstateAStatus(t *testing.T) {
	root := repoRoot(t)
	for _, p := range checkRows(readMatrix(t, root), repoResolver(root), false) {
		t.Error(p)
	}
}

// TestEpicUIsClosed is the closing check, run by
// scripts/release/check-epic-u-closed.sh with RETND_EPIC_U_CLOSURE=1: no row of
// docs/conformance/epic-u-matrix.md is BLOCKED, every proof a row cites
// resolves, every PASS is dated and cites a mutation that exists. It is skipped
// otherwise, so the gate stays usable while the epic's pull requests land one at
// a time.
func TestEpicUIsClosed(t *testing.T) {
	if os.Getenv("RETND_EPIC_U_CLOSURE") != "1" {
		t.Skip("closing check: set RETND_EPIC_U_CLOSURE=1 (scripts/release/check-epic-u-closed.sh does)")
	}
	root := repoRoot(t)
	rows := readMatrix(t, root)
	if len(rows) == 0 {
		t.Fatalf("%s parsed to no rows, so the parser or the file is wrong", matrixPath)
	}
	for _, p := range checkRows(rows, repoResolver(root), true) {
		t.Error(p)
	}
}
