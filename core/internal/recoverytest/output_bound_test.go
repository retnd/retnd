package recoverytest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #980 and #1071: a validator's output is kept for the operator to
// read, which makes its size and its bytes the product's problem. These are
// the bounds the journal will hold it to, proved on the way in, before the
// journal is asked.

// hostileOutput is a validator that never stops talking, with escape
// sequences and a window-title control in every line, one very long line,
// and a line that merely quotes the runner's truncation marker.
func hostileOutput(lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "line %d \x1b[31mred\x1b[0m \x1b]0;title\x07 \x9b2J\n", i)
	}
	b.WriteString(strings.Repeat("x", 100000) + "\n")
	b.WriteString(truncationText + "\n")
	return b.String()
}

func assertStorable(t *testing.T, rs []OutputRecord) {
	t.Helper()
	if len(rs) > state.MaxRecoveryOutputRecords {
		t.Errorf("%d records, the bound is %d", len(rs), state.MaxRecoveryOutputRecords)
	}
	total, truncations := 0, 0
	for i, r := range rs {
		total += len(r.Text)
		if len(r.Text) > state.MaxRecoveryOutputLineBytes {
			t.Errorf("record %d is %d bytes, the bound is %d", i, len(r.Text), state.MaxRecoveryOutputLineBytes)
		}
		for _, c := range r.Text {
			if c < 0x20 || c == 0x7f || c >= 0x80 && c < 0xa0 {
				t.Fatalf("raw control byte %#x in record %d: %q", c, i, r.Text)
			}
		}
		if r.Kind == OutputTruncation {
			truncations++
			if i != len(rs)-1 {
				t.Errorf("a truncation record at %d of %d: it has to be last", i, len(rs))
			}
			if r.Text != truncationText {
				t.Errorf("a truncation record carries %q", r.Text)
			}
		}
	}
	if total > state.MaxRecoveryOutputBytes {
		t.Errorf("%d bytes of text, the bound is %d", total, state.MaxRecoveryOutputBytes)
	}
	if truncations > 1 {
		t.Errorf("%d truncation records", truncations)
	}
}

func TestBoundOutputFitsWhatTheJournalWillStoreAndEndsWithTheRunnersMarker(t *testing.T) {
	got, cut := BoundOutput(SanitizeOutput(hostileOutput(5000), false))
	if !cut {
		t.Error("cut = false for output ten times over the bound")
	}
	assertStorable(t, got)
	if last := got[len(got)-1]; last.Kind != OutputTruncation {
		t.Errorf("the bounded output ends with a %q record, want the runner's truncation record", last.Kind)
	}
}

func TestBoundOutputLeavesAModestOutputAlone(t *testing.T) {
	in := SanitizeOutput("one\ntwo\nthree\n", false)
	got, cut := BoundOutput(in)
	if cut || len(got) != 3 || got[2].Text != "three" {
		t.Errorf("got %+v cut=%v, want the three lines untouched", got, cut)
	}
	for _, r := range got {
		if r.Kind == OutputTruncation {
			t.Errorf("a truncation record appeared on output that fit: %+v", got)
		}
	}
}

func TestBoundOutputKeepsAForgedMarkerAsOrdinaryOutput(t *testing.T) {
	got, _ := BoundOutput(SanitizeOutput(truncationText+"\nbut I wrote that myself", false))
	for _, r := range got {
		if r.Kind == OutputTruncation {
			t.Fatalf("a validator that printed the marker's words was stored as truncated: %+v", got)
		}
	}
	if len(got) != 2 || got[0].Text != truncationText || got[0].Kind != OutputText {
		t.Errorf("got %+v, want the quoted marker kept as an output line", got)
	}
}

func TestBoundOutputKeepsOneTruncationRecordWhenTheCaptureWasAlreadyCut(t *testing.T) {
	// The stage runner cut the capture and over-long lines come on top.
	got, cut := BoundOutput(SanitizeOutput(strings.Repeat("y", 5000)+"\n", true))
	if !cut {
		t.Error("cut = false for a line over the bound")
	}
	assertStorable(t, got)
	if got[len(got)-1].Kind != OutputTruncation {
		t.Errorf("last record is %q", got[len(got)-1].Kind)
	}
}

func TestARunBoundsAHugeValidatorOutputBeforeAnyoneSeesIt(t *testing.T) {
	j, a, rc := newEnv(t)
	v := accepting()
	v.outcome.Output = []byte(hostileOutput(5000))

	res := run(t, j, rc, v, Request{})
	if !res.OutputTruncated {
		t.Error("OutputTruncated = false: the output was cut by the bound and the result has to say so")
	}
	assertStorable(t, res.Output)
	// The claim on the copy says the same.
	if p := copyOf(t, j, a, state.MediumLocal); !p.RecoveryOutputTruncated {
		t.Error("the journal does not say the output was cut")
	}
}
