// Package observabilitygate_test holds EPIC O's conformance matrix to the tree
// it makes claims about (#903): docs/conformance/epic-o-matrix.md, the claims of
// the observability integrations (#898), each tied to the proof that goes red
// and the mutation that was run and watched to fail.
//
// It has two modes, the split core/tests/agentgate and core/tests/encryptiongate
// have.
//
// Every run of the gate reads the matrix and refuses a row that lies about its
// own status: a PASS that names no date, a BY HAND that names nobody, a PARTIAL
// that does not say what is missing, and an exit-gate or ledger row that claims
// more than the detailed rows it cites. Rows may still be BLOCKED in this mode.
//
// With RETND_EPIC_O_CLOSURE=1 the same checker runs in closing mode, which is
// what scripts/release/check-epic-o-closed.sh sets: no row may be BLOCKED, and
// every proof a row cites has to resolve.
//
// What this does NOT prove, said here so nobody reads more into a green run: it
// re-runs no falsification. A PASS exit-gate or ledger row has to quote the label
// of a planted violation that is really in one of the self-tests below (or cite
// a guard test that plants its own violation), and a detailed row has to carry a
// date and a proof that resolves, but whether that plant was run and went red is
// the self-test's business (scripts/observability/selftest.sh, a gate step), not
// this reader's.
//
// The matrix has three tables that carry an outcome, in two shapes. The detailed
// rows, O-01 to O-25, are `# | Outcome | Certifies | Where | Falsification`, and
// every test and self-test in the tree refers to them by those ids. The exit
// gate (O1.1 to O1.21, one per line of the spec's "Phase 1 exit gate") and the
// ledger (OV.1 to OV.6, one per planted violation of the spec's section 4) are
// `ID | Claim | Executable proof | Falsification | Status | Waits on`, the shape
// core/tests/compat reads for every registered epic. An exit-gate row names the
// detailed rows it rests on, and it may not claim more than the weakest of them.
//
// core/tests/compat holds the matrix to the specification (one row per exit gate
// line, one OV row per guard, a spec box ticked exactly when its row is PASS).
// This package holds it to itself and to the files it cites.
package observabilitygate_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const matrixPath = "docs/conformance/epic-o-matrix.md"

// selfTests are the files whose planted violations a PASS exit-gate or ledger
// row may cite, by the verbatim label of the mutation in double quotes.
var selfTests = []string{
	"scripts/bdtools/observability/selftest.py",
	"scripts/tests/epic-o-closure.test.sh",
	// The contract drift and client path guards are not EPIC O's, and O-20
	// re-runs them: their own self-test is where their plants live.
	"scripts/bdtools/api/selftest.py",
}

var (
	detailID  = regexp.MustCompile(`^O-[0-9]{2}$`)
	tableID   = regexp.MustCompile(`^(O1\.[0-9]+|OV\.[0-9]+)$`)
	detailRef = regexp.MustCompile(`\bO-[0-9]{2}\b`)
	backtick  = regexp.MustCompile("`([^`]+)`")
	proofType = regexp.MustCompile(`^[A-Za-z0-9_./-]+(:Test[A-Za-z0-9_]+)?$`)
	labelRe   = regexp.MustCompile(`"([^"]{12,})"`)
	guardTest = regexp.MustCompile(`:Test[A-Za-z0-9_]*(CanActuallyFail|CanFail|TurnsRed|CanLie|WouldCatch)[A-Za-z0-9_]*$`)
	dateRe    = regexp.MustCompile(`20[0-9]{2}-[0-9]{2}-[0-9]{2}`)
	byHand    = regexp.MustCompile(`by [^,;:()]{3,60} on 20[0-9]{2}-[0-9]{2}-[0-9]{2}`)
	missingRe = regexp.MustCompile(`Missing: .{12,}`)
)

// epicIssues are the tracker and the sub-issues the matrix's own header names
// ("Tracker: #898. Sub-issues: O1.1 #899, ..."). Every one of them closes when
// the epic's last pull request merges, so a row that is still PARTIAL cannot
// wait on one: it would read as tracked by work that is already closed (#1600's
// review). Read from the text, so the rule needs no network.
var headerIssues = regexp.MustCompile(`(?m)^Tracker: .*$`)

var issueRef = regexp.MustCompile(`#([0-9]+)`)

func epicIssues(text string) map[int]bool {
	out := map[int]bool{}
	for _, m := range issueRef.FindAllStringSubmatch(headerIssues.FindString(text), -1) {
		var n int
		_, _ = fmt.Sscanf(m[1], "%d", &n)
		out[n] = true
	}

	return out
}

type row struct {
	id, claim, proof, falsif, status, waitsOn string
	detail                                    bool
}

func cells(line string) []string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(trimmed, "|"), "|"), "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}

	return out
}

func parseMatrix(text string) []row {
	var rows []row
	for _, line := range strings.Split(text, "\n") {
		c := cells(line)
		switch {
		case len(c) >= 5 && detailID.MatchString(c[0]):
			// `# | Outcome | Certifies | Where | Falsification`. What a detailed
			// row waits on is the issue its outcome cell names.
			rows = append(rows, row{id: c[0], claim: c[2], proof: c[3], falsif: c[4], status: strings.ToUpper(c[1]), waitsOn: c[1], detail: true})
		case len(c) >= 6 && tableID.MatchString(c[0]):
			rows = append(rows, row{id: c[0], claim: c[1], proof: c[2], falsif: c[3], status: strings.ToUpper(c[4]), waitsOn: c[5]})
		}
	}

	return rows
}

// The status word is the start of the cell: "BLOCKED (#903)" is BLOCKED.
func statusWord(s string) string {
	for _, w := range []string{"BY HAND", "BLOCKED", "PARTIAL", "PASS"} {
		if strings.HasPrefix(s, w) {
			return w
		}
	}

	return s
}

// strength orders the outcomes an exit-gate row may aggregate. BY HAND counts
// as finished: it is the outcome a question about a person reading a screen
// ends in, and the row that carries it says who and when.
func strength(word string) int {
	switch word {
	case "PASS", "BY HAND":
		return 3
	case "PARTIAL":
		return 2
	case "BLOCKED":
		return 1
	}

	return 0
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
func checkRows(rows []row, epic map[int]bool, r resolver, closing bool) []string {
	var out []string
	seen := map[string]bool{}
	details := map[string]string{}
	for _, x := range rows {
		if x.detail {
			details[x.id] = statusWord(x.status)
		}
	}
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
		word := statusWord(x.status)
		switch word {
		case "PASS":
			switch {
			case x.detail && !dateRe.MatchString(x.falsif):
				out = append(out, x.id+" says PASS but its falsification carries no date, so nobody can tell when it was watched to fail")
			case !x.detail && !ranMutation(x.falsif, r):
				out = append(out, x.id+" says PASS but its falsification carries no date, or quotes no mutation label that occurs in a self-test and cites no guard test that plants the violation itself")
			}
		case "BY HAND":
			if !x.detail {
				out = append(out, x.id+" is BY HAND, and an exit-gate or ledger row is PASS, PARTIAL or BLOCKED: the BY HAND half belongs on the detailed row it cites")
			} else if !byHand.MatchString(x.falsif) {
				out = append(out, x.id+" is BY HAND but does not say who checked it and when (by <name> on YYYY-MM-DD)")
			}
		case "PARTIAL":
			if !missingRe.MatchString(x.falsif) {
				out = append(out, x.id+" is PARTIAL but does not say what is missing (Missing: ...)")
			}
			if !strings.Contains(x.waitsOn, "#") {
				out = append(out, x.id+" is PARTIAL but names no issue it waits on")
			}
			for _, m := range issueRef.FindAllStringSubmatch(x.waitsOn, -1) {
				var n int
				_, _ = fmt.Sscanf(m[1], "%d", &n)
				if epic[n] {
					out = append(out, fmt.Sprintf("%s is PARTIAL and waits on #%d, which is this epic's own tracker or a sub-issue and closes with it; open a follow-up and cite that", x.id, n))
				}
			}
		case "BLOCKED":
			if closing {
				out = append(out, x.id+" is BLOCKED, and this epic does not close with a blocked row")
			}
		default:
			out = append(out, x.id+" has status "+x.status+"; a row is PASS, BY HAND, PARTIAL or BLOCKED and nothing else")
		}
		if x.detail {
			continue
		}
		// An exit-gate or ledger row claims no more than the detailed rows it
		// rests on: a PASS line resting on a PARTIAL row is the composite green
		// this file exists to refuse.
		for _, ref := range detailRef.FindAllString(x.claim, -1) {
			got, ok := details[ref]
			if !ok {
				out = append(out, x.id+" rests on "+ref+", which is not a row of the matrix")
				continue
			}
			if strength(word) > strength(got) {
				out = append(out, fmt.Sprintf("%s says %s and rests on %s, which is %s, so it claims more than the row it rests on", x.id, word, ref, got))
			}
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

func readEpicIssues(t *testing.T, root string) map[int]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", matrixPath, err)
	}
	epic := epicIssues(string(raw))
	if len(epic) < 2 {
		t.Fatalf("%s's header names the issues %v; it has to name the tracker and the sub-issues on its \"Tracker:\" line, or the follow-up rule reads nothing", matrixPath, epic)
	}

	return epic
}

func TestTheEpicOMatrixCheckerCanLie(t *testing.T) {
	good := func() []row {
		rows := []row{
			{id: "O-01", claim: "c", proof: "`pkg/a_test.go`", falsif: "planted and red, run 2026-11-20", status: "PASS", waitsOn: "PASS (#899)", detail: true},
			{id: "O-02", claim: "c", proof: "`pkg/b_test.go`", falsif: "read by hand by Rom on 2026-11-20", status: "BY HAND", waitsOn: "BY HAND (#903)", detail: true},
		}
		for i := 1; i <= 4; i++ {
			rows = append(rows, row{id: fmt.Sprintf("O1.%d", i), claim: "c (O-01, O-02)", proof: "`pkg:TestReal`",
				falsif: "Planted \"a real planted mutation\", run 2026-11-20.", status: "PASS", waitsOn: "-"})
		}
		rows = append(rows, row{id: "OV.1", claim: "PV1 (O-01)", proof: "`pkg:TestReal`",
			falsif: "Planted \"a real planted mutation\", run 2026-11-20.", status: "PASS", waitsOn: "-"})

		return rows
	}
	epic := epicIssues("# x\n\nTracker: #898. Sub-issues: O1.1 #899, O1.5 #903.\n")
	if !epic[898] || !epic[899] || !epic[903] || len(epic) != 3 {
		t.Fatalf("the header reader read %v", epic)
	}
	res := resolver{
		exists: func(ref string) bool {
			return ref == "pkg:TestReal" || ref == "pkg/a_test.go" || ref == "pkg/b_test.go"
		},
		contains: func(path, needle string) bool { return path == selfTests[0] && needle == "a real planted mutation" },
	}
	for _, closing := range []bool{false, true} {
		if got := checkRows(good(), epic, res, closing); len(got) != 0 {
			t.Fatalf("the control matrix should be clean (closing=%v): %v", closing, got)
		}
	}
	lies := []struct {
		name    string
		mutate  func(r []row) []row
		want    string
		closing bool
	}{
		{"a duplicated row", func(r []row) []row { return append(r, r[2]) }, "appears twice", false},
		{"an undated detailed PASS", func(r []row) []row { r[0].falsif = "planted and red"; return r }, "carries no date", false},
		{"an undated exit-gate PASS", func(r []row) []row { r[3].falsif = "Planted \"a real planted mutation\"."; return r }, "says PASS", false},
		{"an exit-gate PASS citing a mutation nobody wrote", func(r []row) []row {
			r[3].falsif = "Planted \"an invented mutation label\", run 2026-11-20."
			return r
		}, "says PASS", false},
		{"a ledger PASS naming no mutation at all", func(r []row) []row { r[6].falsif = "run 2026-11-20, it failed."; return r }, "says PASS", false},
		{"a BY HAND row that names nobody", func(r []row) []row { r[1].falsif = "looked fine"; return r }, "who checked it", false},
		{"a BY HAND exit-gate row", func(r []row) []row { r[4].status = "BY HAND"; return r }, "belongs on the detailed row", false},
		{"a PARTIAL row that says nothing is missing", func(r []row) []row {
			r[0].status, r[0].falsif, r[0].waitsOn = "PARTIAL", "mostly, 2026-11-20", "PARTIAL (#903)"
			return r
		}, "what is missing", false},
		{"a PARTIAL row that waits on nothing", func(r []row) []row {
			r[4].status, r[4].falsif = "PARTIAL", "Missing: the log half of the corpus"
			return r
		}, "names no issue", false},
		{"a PARTIAL detailed row waiting on the epic's own closing sub-issue", func(r []row) []row {
			r[1].status, r[1].falsif, r[1].waitsOn = "PARTIAL", "Missing: a person reading the screen", "PARTIAL (#903)"
			return r
		}, "waits on #903, which is this epic's own tracker", false},
		{"a PARTIAL gate row waiting on the tracker", func(r []row) []row {
			r[4].status, r[4].falsif, r[4].waitsOn = "PARTIAL", "Missing: the full gate run", "#898"
			return r
		}, "waits on #898", false},
		{"a status that is none of the four", func(r []row) []row { r[5].status = "DONE"; return r }, "has status", false},
		{"an exit-gate PASS resting on a PARTIAL row", func(r []row) []row {
			r[0].status, r[0].falsif, r[0].waitsOn = "PARTIAL", "Missing: the log half of the corpus", "PARTIAL (#903)"
			return r
		}, "claims more than the row it rests on", false},
		{"an exit-gate PASS resting on a BLOCKED row", func(r []row) []row { r[0].status = "BLOCKED (#903)"; return r }, "rests on O-01, which is BLOCKED", false},
		{"an exit-gate row resting on a row that does not exist", func(r []row) []row { r[2].claim = "c (O-09)"; return r }, "not a row of the matrix", false},
		{"a BLOCKED row while closing", func(r []row) []row { r[3].status = "BLOCKED (#903)"; return r }, "does not close with a blocked row", true},
		{"a proof that does not resolve while closing", func(r []row) []row { r[5].proof = "`pkg:TestGone`"; return r }, "does not resolve", true},
		{"a row with no proof while closing", func(r []row) []row { r[5].proof = "none yet"; return r }, "names no executable proof", true},
	}
	for _, l := range lies {
		got := checkRows(l.mutate(good()), epic, res, l.closing)
		if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), l.want) {
			t.Errorf("%s was not refused with %q: %v", l.name, l.want, got)
		}
	}
	// Negative control for the two closing-only rules: the same lies are
	// accepted while the epic is still open, which is what keeps the gate usable
	// between pull requests.
	open := good()
	open[3].status = "BLOCKED (#903)"
	open[5].proof = "`pkg:TestGone`"
	if got := checkRows(open, epic, res, false); len(got) != 0 {
		t.Errorf("a BLOCKED row or an unresolved proof was refused before closing: %v", got)
	}
}

// The parser has to read every family of EPIC O's rows (the O-nn detailed rows,
// the O1.n gate lines, the OV.n ledger) or a BLOCKED row of the family it
// forgets is simply not seen. This feeds a one-row table per family through it,
// and a table that is not the matrix through it too.
func TestTheCheckerReadsEveryEpicOFamilyAndRefusesABlockedOneWhileClosing(t *testing.T) {
	res := resolver{
		exists:   func(ref string) bool { return ref == "pkg:TestReal" },
		contains: func(path, needle string) bool { return false },
	}
	tables := map[string]string{
		"O-07": "| # | Outcome | Certifies | Where | Falsification |\n| --- | --- | --- | --- | --- |\n| O-07 | BLOCKED (#903) | c | `pkg:TestReal` | not yet |\n",
		"O1.1": "| ID | Claim | Proof | Falsification | Status | Waits on |\n|---|---|---|---|---|---|\n| O1.1 | c | `pkg:TestReal` | not yet | BLOCKED (#903) | #903 |\n",
		"OV.6": "| ID | Claim | Proof | Falsification | Status | Waits on |\n|---|---|---|---|---|---|\n| OV.6 | c | `pkg:TestReal` | not yet | BLOCKED (#903) | #903 |\n",
	}
	for id, table := range tables {
		rows := parseMatrix(table)
		if len(rows) != 1 || rows[0].id != id {
			t.Fatalf("the parser read %v out of a one-row table with id %s; it has to read every EPIC O family", rows, id)
		}
		if got := strings.Join(checkRows(rows, nil, res, true), "\n"); !strings.Contains(got, "does not close with a blocked row") {
			t.Errorf("a BLOCKED %s row was not refused while closing: %q", id, got)
		}
		if open := checkRows(rows, nil, res, false); len(open) != 0 {
			t.Errorf("a BLOCKED %s row was refused before closing: %v", id, open)
		}
	}
	if got := parseMatrix("| ID | Claim |\n|---|---|\n| O9 | x |\n| U1.1 | y | z | w | PASS | - |\n| O-1 | PASS | a | b | c |\n"); len(got) != 0 {
		t.Fatalf("a table that is not EPIC O's matrix parsed to rows: %v", got)
	}
}

func TestTheEpicOMatrixParserReadsTheRealTableRatherThanItsShape(t *testing.T) {
	rows := readMatrix(t, repoRoot(t))
	// How many gate and ledger rows there should be is the specification's to
	// say, and core/tests/compat holds the matrix to it. What this parser has to
	// show is that it read every family and lost nothing in the middle of one:
	// each is present and numbered 1..n without a gap.
	family := regexp.MustCompile(`^(O1|OV|O)[.-]0*([0-9]+)$`)
	seen := map[string][]int{}
	for _, r := range rows {
		if m := family.FindStringSubmatch(r.id); m != nil {
			var n int
			_, _ = fmt.Sscanf(m[2], "%d", &n)
			seen[m[1]] = append(seen[m[1]], n)
		}
	}
	for _, f := range []string{"O", "O1", "OV"} {
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

func TestTheEpicOMatrixDoesNotMisstateAStatus(t *testing.T) {
	root := repoRoot(t)
	for _, p := range checkRows(readMatrix(t, root), readEpicIssues(t, root), repoResolver(root), false) {
		t.Error(p)
	}
}

// TestEpicOIsClosed is the closing check, run by
// scripts/release/check-epic-o-closed.sh with RETND_EPIC_O_CLOSURE=1: no row of
// docs/conformance/epic-o-matrix.md is BLOCKED, every proof a row cites
// resolves, and the open-mode rules above still hold. It is skipped otherwise,
// so the gate stays usable while a row is still being worked.
func TestEpicOIsClosed(t *testing.T) {
	if os.Getenv("RETND_EPIC_O_CLOSURE") != "1" {
		t.Skip("closing check: set RETND_EPIC_O_CLOSURE=1 (scripts/release/check-epic-o-closed.sh does)")
	}
	root := repoRoot(t)
	rows := readMatrix(t, root)
	if len(rows) == 0 {
		t.Fatalf("%s parsed to no rows, so the parser or the file is wrong", matrixPath)
	}
	for _, p := range checkRows(rows, readEpicIssues(t, root), repoResolver(root), true) {
		t.Error(p)
	}
}
