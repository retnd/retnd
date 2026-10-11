// Package assurancegate_test holds EPIC Q's conformance matrix to the tree
// it makes claims about (#988).
//
// The matrix at docs/conformance/epic-q-matrix.md is the one artefact of the
// epic that is prose, and the epic's own rule is that no gate item may be
// satisfied by prose alone. So the prose is checked: this package reads the
// matrix, and refuses it unless
//
//   - every row the specification (#954) numbers is present: the exit-gate
//     lines Q1.1 to Q1.23 and Q2.1 to Q2.16, the nineteen adversarial-review
//     findings QC-01 to QC-19 and the planted-violation ledger QV.1 to QV.22,
//     so a row that quietly stopped being tracked turns the gate red;
//   - every proof a row cites RESOLVES: a `<package dir>:<TestName>` reference
//     has to be a Go test function that exists in that directory today, and a
//     bare path has to be a file that exists;
//   - a row that says PASS cites a falsification that was RUN: a mutation
//     label that really occurs in one of the self-tests, or a guard test that
//     plants the violation itself, and the date it was run;
//   - a row that says BY HAND names who checked, what they did and the date,
//     and never borrows a neighbouring green;
//   - a row that says PARTIAL says what is missing and what it waits on;
//   - no row is BLOCKED. The epic does not close with one, so this is the
//     check that stops it closing with one.
//
// The checker is a function over text plus a resolver, so the whole of it can
// be run against deliberately broken matrices. That is
// TestTheMatrixCheckerRefusesEveryWayAMatrixCanLie, and it is the mutation
// self-test docs/epic-checklist.md section 12 requires of a new guard: without
// it this file would be a guard whose only evidence is that it passes on the
// tree it shipped with.
package assurancegate_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// matrixPath is the matrix, relative to the repository root.
const matrixPath = "docs/conformance/epic-q-matrix.md"

// selfTests are the files whose planted violations a PASS row may cite. A row
// cites one by the verbatim label of the mutation, in double quotes.
var selfTests = []string{
	"scripts/bdtools/recovery/selftest.py",
	"scripts/bdtools/assurance/selftest.py",
	"scripts/bdtools/assurance/ui_selftest.py",
}

// requiredRows lists every row id the specification numbers.
func requiredRows() []string {
	var ids []string
	for i := 1; i <= 23; i++ {
		ids = append(ids, fmt.Sprintf("Q1.%d", i))
	}
	for i := 1; i <= 16; i++ {
		ids = append(ids, fmt.Sprintf("Q2.%d", i))
	}
	for i := 1; i <= 19; i++ {
		ids = append(ids, fmt.Sprintf("QC-%02d", i))
	}
	for i := 1; i <= 22; i++ {
		ids = append(ids, fmt.Sprintf("QV.%d", i))
	}

	return ids
}

// A row of the matrix table.
type row struct {
	id      string
	claim   string
	proof   string
	falsif  string
	status  string
	waitsOn string
	line    int
}

var (
	rowID    = regexp.MustCompile(`^(Q[12]\.[0-9]+|QC-[0-9]{2}|QV\.[0-9]+)$`)
	backtick = regexp.MustCompile("`([^`]+)`")
	// A proof reference: a path, or a package directory and a test name.
	proofShape = regexp.MustCompile(`^[A-Za-z0-9_./-]+(:Test[A-Za-z0-9_]+)?$`)
	labelRe    = regexp.MustCompile(`"([^"]{12,})"`)
	// A guard test that plants the violation itself.
	guardTest = regexp.MustCompile(`:Test[A-Za-z0-9_]*(CanActuallyFail|CanFail|TurnsRed|CanLie|WouldCatch)[A-Za-z0-9_]*$`)
	dateRe    = regexp.MustCompile(`20[0-9]{2}-[0-9]{2}-[0-9]{2}`)
	byHand    = regexp.MustCompile(`by [^,;:()]{3,60} on 20[0-9]{2}-[0-9]{2}-[0-9]{2}`)
	missingRe = regexp.MustCompile(`Missing: .{12,}`)
)

func goFuncDecl(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(`)
}

// parseMatrix reads the rows out of every table in the file.
func parseMatrix(text string) []row {
	var rows []row

	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := splitRow(trimmed)
		if len(cells) < 6 || !rowID.MatchString(cells[0]) {
			continue
		}
		rows = append(rows, row{
			id:      cells[0],
			claim:   cells[1],
			proof:   cells[2],
			falsif:  cells[3],
			status:  strings.ToUpper(cells[4]),
			waitsOn: cells[5],
			line:    i + 1,
		})
	}

	return rows
}

func splitRow(line string) []string {
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}

	return cells
}

// resolver answers whether one proof reference names something that exists.
// It is a parameter so the checker can be run against a tree that is not this
// one. contains answers whether a self-test file holds a string.
type resolver struct {
	exists   func(ref string) (bool, string)
	contains func(path, needle string) bool
}

// proofRefs returns the backticked tokens of a proof cell that have the shape
// of a path or a test reference. Other backticked words (a function name in
// prose) are not references and are not resolved.
func proofRefs(cell string) []string {
	var refs []string
	for _, m := range backtick.FindAllStringSubmatch(cell, -1) {
		if tok := strings.TrimSpace(m[1]); proofShape.MatchString(tok) && (strings.Contains(tok, "/") || strings.Contains(tok, ":")) {
			refs = append(refs, tok)
		}
	}

	return refs
}

// ranMutation says whether a falsification cell cites a mutation that exists
// and a date it was run: a quoted label in one of the self-tests, or a guard
// test that plants the violation itself and resolves.
func ranMutation(cell string, r resolver) (ok bool, why string) {
	if !dateRe.MatchString(cell) {
		return false, "carries no date it was run"
	}
	for _, m := range labelRe.FindAllStringSubmatch(cell, -1) {
		for _, f := range selfTests {
			if r.contains(f, m[1]) {
				return true, ""
			}
		}
	}
	for _, ref := range proofRefs(cell) {
		if guardTest.MatchString(ref) {
			if found, _ := r.exists(ref); found {
				return true, ""
			}
		}
	}

	return false, "cites no mutation label that occurs in " + strings.Join(selfTests, ", ") + " and no guard test that plants the violation itself"
}

// checkMatrix returns one finding per way the matrix does not hold. An empty
// result is the gate passing.
func checkMatrix(rows []row, r resolver) []string {
	var out []string

	byID := map[string]row{}
	for _, x := range rows {
		if _, dup := byID[x.id]; dup {
			out = append(out, x.id+" appears twice, so one of the two is not being read")
		}
		byID[x.id] = x
	}
	for _, id := range requiredRows() {
		if _, ok := byID[id]; !ok {
			out = append(out, id+" has no row at all: the specification numbers it, so it has to be answered")
		}
	}

	for _, x := range rows {
		refs := proofRefs(x.proof)
		if len(refs) == 0 {
			out = append(out, x.id+" names no executable proof (a backticked path or `dir:TestName`)")
		}
		for _, ref := range refs {
			if ok, why := r.exists(ref); !ok {
				out = append(out, x.id+" cites "+ref+", which does not resolve: "+why)
			}
		}

		switch x.status {
		case "PASS":
			if ok, why := ranMutation(x.falsif, r); !ok {
				out = append(out, x.id+" says PASS but its falsification "+why)
			}
		case "BY HAND":
			if !byHand.MatchString(x.falsif) {
				out = append(out, x.id+" is BY HAND but does not say who checked it and when (`by <name> on YYYY-MM-DD`), so it is borrowing a neighbour's green")
			}
		case "PARTIAL":
			if !missingRe.MatchString(x.falsif) {
				out = append(out, x.id+" is PARTIAL but does not say what is missing (`Missing: ...`)")
			}
			if x.waitsOn == "" || x.waitsOn == "-" {
				out = append(out, x.id+" is PARTIAL but names nothing it waits on")
			}
		case "BLOCKED":
			out = append(out, x.id+" is BLOCKED, and this epic does not close with a blocked row")
		default:
			out = append(out, x.id+" has status "+x.status+"; a row is PASS (a mutation was run and watched to fail), BY HAND (a named person did it on a named day), PARTIAL (and says what is missing) and nothing else")
		}
	}

	sort.Strings(out)

	return out
}

// requiredStatements are sentences the matrix has to carry in its own prose,
// because the matrix is the one place the limits of these claims are written
// down, and a limit that can be deleted without anything noticing is a
// paragraph rather than a deliverable. Substrings, so the wording may improve
// while the claim may not disappear.
var requiredStatements = []struct {
	text  string
	about string
}{
	{"A reachable copy is not a recovered one", "the limit of every green in the Stored column"},
	{"not a store screenshot", "the captures are documentation pictures and never a store submission"},
	{"the ungated direction", "the one equivalence direction checked by hand rather than by a suite"},
}

func checkStatements(text string) []string {
	var out []string
	flat := strings.Join(strings.Fields(text), " ")
	for _, want := range requiredStatements {
		if !strings.Contains(flat, strings.Join(strings.Fields(want.text), " ")) {
			out = append(out, "the matrix no longer states \""+want.text+"\", which is "+want.about)
		}
	}

	return out
}

func repoResolver(root string) resolver {
	withoutApps := os.Getenv("RETND_CORE_WITHOUT_APPS") == "1"

	return resolver{
		exists: func(ref string) (bool, string) {
			dir, name, isGo := strings.Cut(ref, ":")
			if !isGo {
				if _, err := os.Stat(filepath.Join(root, ref)); err != nil {
					return false, "no file at that path"
				}

				return true, ""
			}
			// The architecture gate deletes apps/ before running every core
			// test; app-owned proofs stay valid references in that one run.
			if withoutApps && strings.HasPrefix(dir, "apps/") {
				return true, ""
			}
			entries, err := os.ReadDir(filepath.Join(root, dir))
			if err != nil {
				return false, "no package directory " + dir
			}
			decl := goFuncDecl(name)
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
					continue
				}
				body, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
				if err == nil && decl.Match(body) {
					return true, ""
				}
			}

			return false, "no func " + name + " in " + dir
		},
		contains: func(path, needle string) bool {
			body, err := os.ReadFile(filepath.Join(root, path))

			return err == nil && strings.Contains(string(body), needle)
		},
	}
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

// TestEveryRowIsAnsweredWithAProofThatExistsAndNoRowIsBlocked is the gate.
func TestEveryRowIsAnsweredWithAProofThatExistsAndNoRowIsBlocked(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", matrixPath, err)
	}
	text := string(raw)

	rows := parseMatrix(text)
	if len(rows) < len(requiredRows()) {
		t.Fatalf("%s parsed to %d rows, fewer than the %d the specification numbers, so the parser or the file is wrong", matrixPath, len(rows), len(requiredRows()))
	}
	problems := append(checkMatrix(rows, repoResolver(root)), checkStatements(text)...)
	for _, p := range problems {
		t.Error(p)
	}
}

// TestTheMatrixCheckerRefusesEveryWayAMatrixCanLie runs the checker against
// matrices broken on purpose, one lie each. A checker that cannot tell these
// from the truth certifies nothing.
func TestTheMatrixCheckerRefusesEveryWayAMatrixCanLie(t *testing.T) {
	good := func() []row {
		var rows []row
		for _, id := range requiredRows() {
			rows = append(rows, row{
				id: id, claim: "a claim", proof: "`pkg:TestReal`",
				falsif: "Planted control \"a real planted mutation\", run 2026-10-11.", status: "PASS", waitsOn: "-",
			})
		}

		return rows
	}
	res := resolver{
		exists: func(ref string) (bool, string) {
			if ref == "pkg:TestReal" || ref == "pkg:TestTheGuardCanActuallyFail" || ref == "scripts/bdtools/assurance/selftest.py" {
				return true, ""
			}

			return false, "not in the fake tree"
		},
		contains: func(path, needle string) bool {
			return path == "scripts/bdtools/assurance/selftest.py" && needle == "a real planted mutation"
		},
	}

	if got := checkMatrix(good(), res); len(got) != 0 {
		t.Fatalf("the control matrix should be clean, got %v", got)
	}

	lies := []struct {
		name   string
		mutate func(rows []row) []row
		want   string
	}{
		{"a row the specification numbers, missing", func(r []row) []row { return r[1:] }, "Q1.1 has no row"},
		{"a duplicated row", func(r []row) []row { return append(r, r[0]) }, "appears twice"},
		{"a row with no proof", func(r []row) []row { r[3].proof = "none yet"; return r }, "names no executable proof"},
		{"a proof that does not resolve", func(r []row) []row { r[4].proof = "`pkg:TestGone`"; return r }, "does not resolve"},
		{"a PASS with no date", func(r []row) []row {
			r[5].falsif = "Planted control \"a real planted mutation\"."
			return r
		}, "carries no date it was run"},
		{"a PASS citing a mutation nobody wrote", func(r []row) []row {
			r[6].falsif = "Planted control \"an invented mutation label\", run 2026-10-11."
			return r
		}, "cites no mutation label"},
		{"a PASS with no falsification at all", func(r []row) []row { r[6].falsif = "-"; return r }, "carries no date"},
		{"a BY HAND row that names nobody", func(r []row) []row {
			r[7].status = "BY HAND"
			r[7].falsif = "looked fine"
			return r
		}, "who checked it and when"},
		{"a PARTIAL row that says nothing is missing", func(r []row) []row {
			r[8].status = "PARTIAL"
			r[8].falsif = "mostly there"
			r[8].waitsOn = "#987"
			return r
		}, "does not say what is missing"},
		{"a PARTIAL row that waits on nothing", func(r []row) []row {
			r[8].status = "PARTIAL"
			r[8].falsif = "Missing: the half that needs a provider this repository does not have."
			r[8].waitsOn = "-"
			return r
		}, "names nothing it waits on"},
		{"a BLOCKED row", func(r []row) []row { r[9].status = "BLOCKED"; return r }, "does not close with a blocked row"},
		{"a made-up status", func(r []row) []row { r[10].status = "GREEN"; return r }, "has status GREEN"},
	}
	for _, l := range lies {
		t.Run(l.name, func(t *testing.T) {
			got := strings.Join(checkMatrix(l.mutate(good()), res), "\n")
			if !strings.Contains(got, l.want) {
				t.Fatalf("the checker did not refuse %q: wanted a finding mentioning %q, got:\n%s", l.name, l.want, got)
			}
		})
	}

	t.Run("a guard test that plants its own violation counts as a run mutation", func(t *testing.T) {
		rows := good()
		rows[0].falsif = "The guard turns itself red when the comparison is removed: `pkg:TestTheGuardCanActuallyFail`, run 2026-10-11."
		if got := checkMatrix(rows, res); len(got) != 0 {
			t.Fatalf("a guard that plants its own violation was refused: %v", got)
		}
	})
	t.Run("a BY HAND row that names who and when is accepted", func(t *testing.T) {
		rows := good()
		rows[0].status = "BY HAND"
		rows[0].falsif = "checked by the author of this change on 2026-10-11: planted the break and watched it fail"
		if got := checkMatrix(rows, res); len(got) != 0 {
			t.Fatalf("a properly named hand check was refused: %v", got)
		}
	})
	t.Run("a PARTIAL row that says what is missing and what it waits on is accepted", func(t *testing.T) {
		rows := good()
		rows[0].status = "PARTIAL"
		rows[0].falsif = "Missing: the half that needs an S3-compatible provider this repository does not have."
		rows[0].waitsOn = "retnd-tests#89"
		if got := checkMatrix(rows, res); len(got) != 0 {
			t.Fatalf("an honest PARTIAL was refused: %v", got)
		}
	})
}

// TestTheRequiredStatementsCannotBeQuietlyDeleted shows the prose check can fail.
func TestTheRequiredStatementsCannotBeQuietlyDeleted(t *testing.T) {
	var full strings.Builder
	for _, s := range requiredStatements {
		full.WriteString(s.text + ". ")
	}
	if got := checkStatements(full.String()); len(got) != 0 {
		t.Fatalf("a document that states everything was refused: %v", got)
	}
	for _, s := range requiredStatements {
		gone := strings.ReplaceAll(full.String(), s.text, "")
		if got := checkStatements(gone); len(got) != 1 || !strings.Contains(got[0], s.text) {
			t.Fatalf("deleting %q was not noticed: %v", s.text, got)
		}
	}
}

// TestTheMatrixParserReadsTheRealTablesRatherThanTheirShape pins the parser to
// the row shape so a reformatted table cannot make every row vanish and the
// gate pass on nothing.
func TestTheMatrixParserReadsTheRealTablesRatherThanTheirShape(t *testing.T) {
	text := "| ID | Claim | Proof | Falsification | Status | Waits on |\n|---|---|---|---|---|---|\n" +
		"| Q1.1 | the gate claim | `core/x:TestA` `docs/y.md` | \"m\" run 2026-10-11 | PASS | - |\n" +
		"| not-a-row | x | y | z | PASS | - |\n" +
		"| QC-01 | five cells only | `a/b` | z | PASS |\n"
	rows := parseMatrix(text)
	if len(rows) != 1 {
		t.Fatalf("parsed %d rows, want exactly the one real one", len(rows))
	}
	if r := rows[0]; r.id != "Q1.1" || len(proofRefs(r.proof)) != 2 || r.status != "PASS" {
		t.Fatalf("parsed the row wrongly: %+v", r)
	}
}
