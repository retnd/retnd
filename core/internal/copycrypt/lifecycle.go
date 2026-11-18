package copycrypt

import (
	"errors"
	"fmt"
)

// This file gives each suite status its behaviour (EPIC T, T2.1, #1316,
// FR-87). The vocabulary itself (StatusSupported, StatusDeprecated,
// StatusRetired) is the registry's; what each one means is decided here and
// nowhere else, so that retiring a suite one day is a table edit with known
// consequences and not a design session.
//
//	status      new copies   reads (verify, recover, decrypt, move)   health
//	supported   yes          yes                                      quiet
//	deprecated  yes          yes                                      degraded, with the retirement version
//	retired     refused      yes                                      degraded while any active copy records it
//
// A read never consults the status. The suite that opens a copy is the one
// its record names, and a retired suite keeps opening every copy it ever
// wrote: removing that ability is a major-version decision outside this
// epic, and it needs the count of recorded copies at zero.
//
// The status guard (lifecycle_test.go) holds the vocabulary to two rules:
// every status has a behaviour in this package, and every status is acted on
// by some surface outside it. A status nothing produces or consumes is a test
// failure, with a control that proves the guard can fail.

// ErrSuiteRetired is a write under a suite whose status is retired. The
// placement engine wraps it in its ErrClassUnavailable, at the position of
// the required refusal, before any destructive step.
var ErrSuiteRetired = errors.New("copycrypt: the suite is retired and writes no new copies")

// Writes reports whether a suite of this status may encrypt a new copy. An
// unknown status writes nothing: a value outside the vocabulary is a
// mistake, and a mistake must fail closed.
func (s Status) Writes() bool {
	switch s {
	case StatusSupported, StatusDeprecated:
		return true
	case StatusRetired:
		return false
	default:
		return false
	}
}

// Reads reports whether copies recorded under a suite of this status are
// still opened. Every status in the vocabulary reads; only an unknown status
// does not, and nothing in this build can record one.
func (s Status) Reads() bool {
	switch s {
	case StatusSupported, StatusDeprecated, StatusRetired:
		return true
	default:
		return false
	}
}

// Degrades reports whether a suite of this status degrades the key custody
// health section when something still depends on it. Whether something does
// (an active copy, a key) is the caller's count; the status only says
// whether such a count is news.
func (s Status) Degrades() bool {
	switch s {
	case StatusSupported:
		return false
	case StatusDeprecated, StatusRetired:
		return true
	default:
		return true
	}
}

// RetiredIn is the version in which s is or will be retired, from its table
// row, or "" when the row names none. A suite entry carries it when it is
// deprecated (the version the retirement is announced for) or retired (the
// version that retired it), so the health sentence, `retnd encryption
// suites` and the CHANGELOG line all read one value and none of them types
// it.
//
// It is an optional method on purpose: a supported suite has nothing to say,
// and an adapter that never leaves supported need not grow a method for it.
func RetiredIn(s Suite) string {
	if r, ok := s.(interface{ RetiredIn() string }); ok {
		return r.RetiredIn()
	}
	return ""
}

// CheckWrite is the one question the engine asks before it encrypts a new
// copy under s: nil when s may write, ErrSuiteRetired (naming the id and the
// version that retired it) when it may not. It reads nothing and costs
// nothing, so the engine asks it before the first destructive step.
func CheckWrite(s Suite) error {
	if s == nil {
		return fmt.Errorf("%w: no suite was resolved", ErrUnknownSuite)
	}
	if st := s.Status(); !st.Writes() {
		if v := RetiredIn(s); v != "" {
			return fmt.Errorf("%w: %s is %s (retired in %s); copies under it still read, and new copies need another suite", ErrSuiteRetired, s.ID(), st, v)
		}
		return fmt.Errorf("%w: %s is %s; copies under it still read, and new copies need another suite", ErrSuiteRetired, s.ID(), st)
	}
	return nil
}
