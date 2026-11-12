package compat

import (
	"strings"
	"testing"
)

// gateRows has to read the column that names the files a row certifies,
// and the four matrices do not agree on which one it is. EPIC E and R are
// `# | Outcome | Certifies | Where | Falsification`, EPIC N and T are
// `ID | Claim | Executable proof | Falsification | Status | Waits on`, so a
// fixed fourth column hands the falsification text of an N or T row to the
// "cites a file that exists" check (#1360). This builds one row of each
// shape, with the file in the column that shape calls its own and a
// different path in the other, and wants the right one back.
func TestGateRowsReadTheColumnThatNamesTheFilesOfEachMatrixShape_1360(t *testing.T) {
	const evidence = "`core/tests/compat/evidence_test.go`"
	const decoy = "`core/tests/compat/decoy_test.go`"

	cases := []struct {
		pair string
		row  string
	}{
		{"EPIC E", "| P1.1 | PASS | a claim | " + evidence + " | " + decoy + " |"},
		{"EPIC R", "| R1.1 | PASS | a claim | " + evidence + " | " + decoy + " |"},
		{"EPIC N", "| N1.1 | a claim | " + evidence + " | " + decoy + " | PASS | - |"},
		{"EPIC T", "| T1.1 | a claim | " + evidence + " | " + decoy + " | PASS | - |"},
	}
	for _, c := range cases {
		t.Run(c.pair, func(t *testing.T) {
			var pair matrixPair
			for _, p := range matrixPairs {
				if p.name == c.pair {
					pair = p
				}
			}
			if pair.name == "" {
				t.Fatalf("no pair named %q in matrixPairs", c.pair)
			}
			rows := gateRows(c.row, pair)
			if len(rows) != 1 {
				t.Fatalf("parsed %d rows out of %q, want 1", len(rows), c.row)
			}
			if !strings.Contains(rows[0].where, evidence) || strings.Contains(rows[0].where, decoy) {
				t.Fatalf("%s row %s reads %q as its files, want the column that names %s", c.pair, rows[0].id, rows[0].where, evidence)
			}
		})
	}
}
