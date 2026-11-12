package packaging

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The U2.2 row of docs/conformance/epic-u-matrix.md (#1350, #1381): the design
// note is in docs/design as 1350-agents.html and .png, both themes, every
// token state and every confirmation state, with the contrast ratio of each
// text pair recorded. Nothing read the note before this file, so a state
// dropped from it, a ratio left out or a ratio under its line went unnoticed.
// scripts/bdtools/agents/controls_design.py plants each of the three.

const (
	agentsNoteHTML = "../../docs/design/1350-agents.html"
	agentsNotePNG  = "../../docs/design/1350-agents.png"
)

func readAgentsNote(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(agentsNoteHTML)
	if err != nil {
		t.Fatalf("the design note is not there: %v", err)
	}

	return string(raw)
}

// surface returns the body of one drawing function of the note's script, from
// its name to the next function at the same indent, so a state has to be drawn
// in the surface that owns it and not merely be mentioned somewhere.
func surface(t *testing.T, note, name string) string {
	t.Helper()
	start := strings.Index(note, "\n "+name+"(){return `")
	if start < 0 {
		t.Fatalf("the note's script has no %s surface", name)
	}
	rest := note[start+1:]
	if end := regexp.MustCompile("\n [a-z0-9]+\\(\\)\\{").FindStringIndex(rest[1:]); end != nil {
		rest = rest[:end[0]+1]
	}

	return rest
}

func TestTheAgentsDesignNoteDrawsEveryTokenState(t *testing.T) {
	note := readAgentsNote(t)
	for _, state := range []string{"active", "expired", "revoked"} {
		if !strings.Contains(note, `st:"`+state+`"`) {
			t.Errorf("the tokens list in the note draws no %s token, so the spec's every-token-state line is not met", state)
		}
	}
}

func TestTheAgentsDesignNoteDrawsEveryConfirmationState(t *testing.T) {
	note := readAgentsNote(t)
	drawn := strings.ToLower(surface(t, note, "confirm") + surface(t, note, "confirm2"))
	for state, marker := range map[string]string{
		"pending":  "waiting. approve calls nothing yet",
		"approved": "approved, and refused",
		"denied":   "denied. nothing was run",
		"expired":  "expired before it was approved",
	} {
		if !strings.Contains(drawn, marker) {
			t.Errorf("no %s confirmation is drawn in the note's confirmation surfaces (looked for %q)", state, marker)
		}
	}
}

func TestTheAgentsDesignNoteShowsBothThemes(t *testing.T) {
	note := readAgentsNote(t)
	for _, theme := range []string{"light", "dark"} {
		if !strings.Contains(note, `mk("`+theme+`",false)`) {
			t.Errorf("the note's frames do not include a %s theme one", theme)
		}
	}
}

func TestEveryContrastRatioInTheAgentsDesignNoteIsStatedAndMeetsItsLine(t *testing.T) {
	note := readAgentsNote(t)
	rows := regexp.MustCompile(`<tr><td>([^<]+)</td><td>([^<]*)</td><td>([^<]*)</td><td>([^<]*)</td></tr>`).FindAllStringSubmatch(note, -1)
	pairs := 0
	for _, r := range rows {
		if !strings.HasPrefix(r[1], "--") && !strings.HasPrefix(r[1], "glyph --") {
			continue
		}
		pairs++
		needs, err := strconv.ParseFloat(r[4], 64)
		if err != nil {
			t.Errorf("%q: the line it needs is %q, not a number", r[1], r[4])

			continue
		}
		for theme, cell := range map[string]string{"light": r[2], "dark": r[3]} {
			got, err := strconv.ParseFloat(cell, 64)
			if err != nil {
				t.Errorf("%q has no ratio recorded in the %s theme (the cell says %q)", r[1], theme, cell)

				continue
			}
			if got < needs {
				t.Errorf("%q is %.2f:1 in the %s theme and needs %.1f", r[1], got, theme, needs)
			}
		}
	}
	if pairs < 20 {
		t.Errorf("the contrast table records %d pairs; the note's own table has 25, and fewer means pairs were dropped", pairs)
	}
	for _, must := range []string{"--text on --surface (", "--text-2 on --surface (", "--text-3 on --surface-3", "--edge-3 on --surface ("} {
		if !strings.Contains(note, "<td>"+must) {
			t.Errorf("the contrast table has no row for %q", must)
		}
	}
}

func TestTheAgentsDesignNoteHasItsPicture(t *testing.T) {
	raw, err := os.ReadFile(agentsNotePNG)
	if err != nil {
		t.Fatalf("the spec asks for 1350-agents.html and .png, and the .png is not there: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) || len(raw) < 10000 {
		t.Errorf("%s is %d bytes and not a picture of the note", agentsNotePNG, len(raw))
	}
}
