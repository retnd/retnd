package recoverytest

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/retnd/retnd/core/internal/state"
)

// A validator's output is untrusted terminal input.
//
// EPIC L paid for this lesson and wrote it down as CR-08
// (docs/conformance/epic-l-matrix.md): at the data layer a hostile OSC, title
// or window-control sequence has no effect beyond being bytes, and a forged
// truncation marker is distinguishable from a genuine one by KIND and never
// by text. This file takes the same two decisions for a recovery test's
// output.
//
//   - Text is sanitised, not escaped. Every control byte (C0 except newline
//     and tab inside a body line, DEL, and the C1 range that terminals treat
//     as introducers) becomes U+FFFD, and so does any byte that is not valid
//     UTF-8. An ESC survives as nothing, so a "[31m" left behind is inert
//     text, and a JSON encoder, a notification body and a terminal all
//     receive the same harmless characters.
//   - The truncation marker is a RECORD of its own kind. A validator that
//     prints the marker's words produces an output record that happens to
//     contain them; only the runner can produce a truncation record, so a
//     reader who branches on Kind cannot be fooled by a validator that
//     wants to look cut off, or wants its real cut-off to look like output.

// OutputKind says what an OutputRecord is, and is the only thing a reader may
// branch on.
type OutputKind string

const (
	// OutputText is a line of the validator's own output.
	OutputText OutputKind = "output"
	// OutputTruncation is the runner's statement that the validator's output
	// was cut at the capture bound.
	OutputTruncation OutputKind = "truncation"
)

// OutputRecord is one line of sanitised validator output, or the runner's
// truncation marker.
type OutputRecord struct {
	Kind OutputKind
	Text string
}

// truncationText is what a genuine truncation record says. It is not what
// identifies one: Kind is.
const truncationText = state.RecoveryOutputTruncationText

// SanitizeOutput turns raw captured output into records safe to put in a
// structural field, a notification body or a terminal.
func SanitizeOutput(raw string, truncated bool) []OutputRecord {
	var out []OutputRecord
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" && raw == "" {
			continue
		}
		out = append(out, OutputRecord{Kind: OutputText, Text: sanitizeText(line)})
	}
	if truncated {
		out = append(out, OutputRecord{Kind: OutputTruncation, Text: truncationText})
	}
	return out
}

// sanitizeText replaces every control byte, C1 introducer and invalid UTF-8
// sequence with U+FFFD. Tab is kept as a space-width character inside a body
// line because validators align columns with it; nothing else is.
func sanitizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			b.WriteRune('�')
		case r == '\t':
			b.WriteRune(' ')
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// SanitizeLabel makes text safe for a single-line structural field: every
// control character (newline included) becomes a space, runs of space
// collapse, and the result is cut on a rune boundary at max bytes.
func SanitizeLabel(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError, r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if len(out) <= max {
		return out
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut]
}

// Render flattens records into display lines. A truncation record is shown
// as itself; an output line that merely resembles one is shown as output.
func Render(rs []OutputRecord) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		switch r.Kind {
		case OutputTruncation:
			out = append(out, r.Text)
		default:
			out = append(out, fmt.Sprintf("| %s", r.Text))
		}
	}
	return out
}

// BoundOutput fits sanitised records into what the journal will store: at most
// state.MaxRecoveryOutputRecords records, each at most
// state.MaxRecoveryOutputLineBytes, all together at most
// state.MaxRecoveryOutputBytes. cut reports that anything was dropped or
// shortened, or that the input already ended in a truncation record.
//
// When it cuts, the result ends with exactly one truncation record, which is
// this function's own and never a validator's: a record of the output kind is
// never promoted to a truncation, whatever its text says.
func BoundOutput(rs []OutputRecord) (out []OutputRecord, cut bool) {
	// Room for the runner's own record is reserved up front, so appending it
	// can never push the result over a bound it was just held to.
	maxRecords := state.MaxRecoveryOutputRecords - 1
	maxBytes := state.MaxRecoveryOutputBytes - len(truncationText)

	out = make([]OutputRecord, 0, len(rs)+1)
	total := 0
	truncated := false
	for _, r := range rs {
		if r.Kind == OutputTruncation {
			// The capture was already cut upstream. Its record is re-added
			// once, last.
			truncated = true
			continue
		}
		text := r.Text
		if len(text) > state.MaxRecoveryOutputLineBytes {
			text, cut = cutRunes(text, state.MaxRecoveryOutputLineBytes), true
		}
		if len(out) >= maxRecords || total+len(text) > maxBytes {
			cut = true
			break
		}
		out = append(out, OutputRecord{Kind: OutputText, Text: text})
		total += len(text)
	}
	if cut || truncated {
		out = append(out, OutputRecord{Kind: OutputTruncation, Text: truncationText})
	}
	return out, cut || truncated
}

// cutRunes shortens s to at most max bytes without splitting a character.
func cutRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
