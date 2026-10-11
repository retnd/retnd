// Package assurancegate_test holds EPIC Q's conformance matrix to the tree
// it makes claims about (#988).
//
// The matrix at docs/conformance/epic-q-matrix.md is the one artefact of the
// epic that is prose, and the epic's own rule is that no gate item may be
// satisfied by prose alone. So the prose is checked: this package reads the
// matrix, and refuses it unless
//
//   - every one of the nineteen adversarial-review findings the epic lists is
//     present as a row, by id and by a word the finding uses for itself, so a
//     finding that quietly stopped being tracked turns the gate red;
//   - every row names at least one executable proof and every proof RESOLVES:
//     a `<package dir>:<TestName>` reference has to be a Go test function that
//     exists in that directory today, and a bare path has to be a file that
//     exists;
//   - a row that says PASS cites a falsification that is a real mutation in a
//     real self-test (`<selftest file>` ("<label>")), and the label has to
//     occur in that file, so a PASS row cannot cite a mutation that was never
//     written;
//   - a row that says BY HAND names who checked, what they did and the date,
//     and never borrows a neighbouring green;
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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// matrixPath is the matrix, relative to the repository root.
const matrixPath = "docs/conformance/epic-q-matrix.md"

// findings are the nineteen rows of the epic's adversarial-review consensus
// table, in the order it lists them, each with a word that has to appear in
// the row's own claim. They are restated here rather than parsed out of the
// issue because the issue is not in the tree: a checkout has to be able to
// run this gate.
var findings = []struct {
	id      string
	keyword string
	about   string
}{
	{"QC-01", "gate", "recovery is its own route pair, outside the destructive gate, and completes on a deployment whose gate is shut"},
	{"QC-02", "audit", "no role exists, so every grant and every served range is audited and the grant is single-use and bound to its session"},
	{"QC-03", "existence", "an existence check is never recorded or rendered as content verification"},
	{"QC-04", "journal", "a scrub reads its work list from the journal and a listing is only evidence about a row"},
	{"QC-05", "due", "due-ness is a property of the copy and a rebuilt catalog reads unknown, which is due"},
	{"QC-06", "unknown", "an assurance status is never green because nobody looked"},
	{"QC-07", "reachability", "a recovery test is a recovery and a digest match, never reachability"},
	{"QC-08", "budget", "money and bandwidth are refusals at the moment of spend"},
	{"QC-09", "per-set", "the per-set answer is written down per knob, including where it is no"},
	{"QC-10", "inherit", "a global change moves a set that inherits, through the route"},
	{"QC-11", "kopia", "one assurance vocabulary for both engines, with no second drill"},
	{"QC-12", "untrusted", "validator output is untrusted terminal input, bounded, with a marker that cannot be forged"},
	{"QC-13", "staging", "a crash with a staging directory present is reconciled and a cleanup failure is separately visible"},
	{"QC-14", "volume", "a scrub is one durable event per pass, and the feed volume is stated and asserted"},
	{"QC-15", "boundary", "the no-download promise is reversed everywhere it was made"},
	{"QC-16", "encryption", "Q works with M and N absent and takes no verdict from either"},
	{"QC-17", "requires_restore", "an unreadable copy is its own state and never counts towards a satisfied policy"},
	{"QC-18", "verb", "the vocabulary is recover and scrub, and neither existing restore verb is renamed"},
	{"QC-19", "browser", "a recovery a browser cannot complete is no recovery on an appliance"},
}

// A row of the matrix table.
type row struct {
	id     string
	claim  string
	proofs []string
	falsif string
	status string
	line   int
}

var (
	proofRef = regexp.MustCompile("`([^`]+)`")
	// A falsification that cites a mutation: `<file>` ("<label>").
	mutationRef = regexp.MustCompile("`([^`]+)` \\(\"([^\"]+)\"\\)")
	// A hand check: by <who> on <YYYY-MM-DD>.
	handCheck = regexp.MustCompile(`by [^,;:]{3,60} on [0-9]{4}-[0-9]{2}-[0-9]{2}`)
	rowID     = regexp.MustCompile(`^QC-[0-9]{2}$`)
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
		if len(cells) < 5 || !rowID.MatchString(cells[0]) {
			continue
		}
		var proofs []string
		for _, m := range proofRef.FindAllStringSubmatch(cells[2], -1) {
			proofs = append(proofs, strings.TrimSpace(m[1]))
		}
		rows = append(rows, row{
			id:     cells[0],
			claim:  cells[1],
			proofs: proofs,
			falsif: cells[3],
			status: strings.ToUpper(cells[4]),
			line:   i + 1,
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
// one. contains answers whether a file holds a string, for falsification labels.
type resolver struct {
	exists   func(ref string) (bool, string)
	contains func(path, needle string) bool
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

	for _, want := range findings {
		got, ok := byID[want.id]
		if !ok {
			out = append(out, want.id+" ("+want.about+") has no row at all: every finding has to map to a test that exists")

			continue
		}
		if !strings.Contains(strings.ToLower(got.claim), strings.ToLower(want.keyword)) {
			out = append(out, got.id+"'s claim does not mention "+want.keyword+", so it is not the finding it is numbered as: "+want.about)
		}
	}

	for _, x := range rows {
		if len(x.proofs) == 0 {
			out = append(out, x.id+" names no executable proof")
		}
		for _, ref := range x.proofs {
			if ok, why := r.exists(ref); !ok {
				out = append(out, x.id+" cites "+ref+", which does not resolve: "+why)
			}
		}

		switch x.status {
		case "PASS":
			ms := mutationRef.FindAllStringSubmatch(x.falsif, -1)
			if len(ms) == 0 {
				out = append(out, x.id+" says PASS but cites no mutation as `<self-test file>` (\"<label>\"), so nobody can say it was watched to fail")
			}
			for _, m := range ms {
				if ok, why := r.exists(m[1]); !ok {
					out = append(out, x.id+" cites the self-test "+m[1]+", which does not resolve: "+why)
				} else if !r.contains(m[1], m[2]) {
					out = append(out, x.id+" cites the mutation \""+m[2]+"\", which "+m[1]+" does not contain")
				}
			}
		case "BY HAND":
			if !handCheck.MatchString(x.falsif) {
				out = append(out, x.id+" is BY HAND but does not say who checked it and when (`by <name> on YYYY-MM-DD`), so it is borrowing a neighbour's green")
			}
		case "BLOCKED":
			out = append(out, x.id+" is BLOCKED, and this epic does not close with a blocked row")
		default:
			out = append(out, x.id+" has status "+x.status+"; a row is PASS (a mutation was watched to fail), BY HAND (a named person did it on a named day) and nothing else")
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

// TestEveryFindingMapsToATestThatExistsAndNoRowIsBlocked is the gate.
func TestEveryFindingMapsToATestThatExistsAndNoRowIsBlocked(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		t.Fatalf("reading %s: %v", matrixPath, err)
	}
	text := string(raw)

	rows := parseMatrix(text)
	if len(rows) < len(findings) {
		t.Fatalf("%s parsed to %d rows, fewer than the %d findings, so the parser or the file is wrong", matrixPath, len(rows), len(findings))
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
		for _, f := range findings {
			rows = append(rows, row{
				id: f.id, claim: "the " + f.keyword + " claim", proofs: []string{"pkg:TestReal"},
				falsif: "`self.py` (\"a real mutation\")", status: "PASS",
			})
		}

		return rows
	}
	res := resolver{
		exists: func(ref string) (bool, string) {
			if ref == "pkg:TestReal" || ref == "self.py" {
				return true, ""
			}

			return false, "not in the fake tree"
		},
		contains: func(path, needle string) bool { return path == "self.py" && needle == "a real mutation" },
	}

	if got := checkMatrix(good(), res); len(got) != 0 {
		t.Fatalf("the control matrix should be clean, got %v", got)
	}

	lies := []struct {
		name   string
		mutate func(rows []row) []row
		want   string
	}{
		{"a finding with no row", func(r []row) []row { return r[1:] }, "QC-01"},
		{"a row about the wrong subject", func(r []row) []row { r[2].claim = "something else entirely"; return r }, "does not mention existence"},
		{"a duplicated row", func(r []row) []row { return append(r, r[0]) }, "appears twice"},
		{"a row with no proof", func(r []row) []row { r[3].proofs = nil; return r }, "names no executable proof"},
		{"a proof that does not resolve", func(r []row) []row { r[4].proofs = []string{"pkg:TestGone"}; return r }, "does not resolve"},
		{"a PASS with no mutation", func(r []row) []row { r[5].falsif = "I ran it once"; return r }, "cites no mutation"},
		{"a PASS citing a mutation nobody wrote", func(r []row) []row { r[6].falsif = "`self.py` (\"invented\")"; return r }, "does not contain"},
		{"a PASS citing a self-test that does not exist", func(r []row) []row { r[6].falsif = "`gone.py` (\"x\")"; return r }, "does not resolve"},
		{"a BY HAND row that names nobody", func(r []row) []row { r[7].status = "BY HAND"; r[7].falsif = "looked fine"; return r }, "who checked it and when"},
		{"a BLOCKED row", func(r []row) []row { r[8].status = "BLOCKED"; return r }, "does not close with a blocked row"},
		{"a made-up status", func(r []row) []row { r[9].status = "GREEN"; return r }, "has status GREEN"},
	}
	for _, l := range lies {
		t.Run(l.name, func(t *testing.T) {
			got := strings.Join(checkMatrix(l.mutate(good()), res), "\n")
			if !strings.Contains(got, l.want) {
				t.Fatalf("the checker did not refuse %q: wanted a finding mentioning %q, got:\n%s", l.name, l.want, got)
			}
		})
	}

	t.Run("a BY HAND row that names who and when is accepted", func(t *testing.T) {
		rows := good()
		rows[0].status = "BY HAND"
		rows[0].falsif = "checked by the author of this change on 2026-10-11: planted the break and watched it fail"
		if got := checkMatrix(rows, res); len(got) != 0 {
			t.Fatalf("a properly named hand check was refused: %v", got)
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
	text := "| ID | Claim | Proof | Falsification | Status |\n|---|---|---|---|---|\n" +
		"| QC-01 | the gate claim | `core/x:TestA`, `docs/y.md` | `s.py` (\"m\") | PASS |\n" +
		"| not-a-row | x | y | z | PASS |\n"
	rows := parseMatrix(text)
	if len(rows) != 1 {
		t.Fatalf("parsed %d rows, want exactly the one real one", len(rows))
	}
	if r := rows[0]; r.id != "QC-01" || len(r.proofs) != 2 || r.status != "PASS" {
		t.Fatalf("parsed the row wrongly: %+v", r)
	}
}
