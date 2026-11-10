package copycrypt

import (
	"errors"
	"io"
)

// Status is where a suite is in its life (ADR 0033 D6). The vocabulary is
// fixed by this package and a test pins it, so a status change is a
// deliberate edit to the table below and to the golden list beside it.
type Status string

const (
	// StatusSupported reads and writes.
	StatusSupported Status = "supported"

	// StatusDeprecated still reads and writes, and health says so (#1316).
	StatusDeprecated Status = "deprecated"

	// StatusRetired reads and never writes: config naming it is refused and
	// the engine refuses a new copy under it, while every copy that records
	// it still decrypts (#1316).
	StatusRetired Status = "retired"
)

// Description is the one sentence a surface may print about a suite
// (FR-84). It is stated from what the code does, never from what a buyer
// might want to hear, and a test pins every suite's sentence.
type Description string

// Suite is one encryption suite: an established library's construction and
// that library's reader, with a stable id recorded per copy
// (placement_encryption.format_id) and per key.
//
// The first four methods are keycustody.Format's, declared here rather than
// embedded. copycrypt stays a leaf package (filippo.io/age and the standard
// library, nothing of this repository) because keycustody imports it for
// AgeFormat; embedding Format here would make the two packages import each
// other. keycustody asserts at compile time that every Suite is a Format.
//
// Nothing behind this interface is designed here (ADR 0033 D5): no
// envelope, no chunk framing, no KDF, no nonce scheme.
type Suite interface {
	// ID is the suite id, the value placement_encryption.format_id holds. It
	// names bytes, not a knob, and is never renamed once written.
	ID() string

	// Generate mints a fresh key from a CSPRNG: the secret a key file holds
	// and its public recipient.
	Generate() (secret []byte, recipient string, err error)

	// Recipient derives the public half from secret, or refuses a secret
	// that is not a key of this suite.
	Recipient(secret []byte) (string, error)

	// Open returns a reader of the plaintext behind ciphertext under secret.
	// It returns an error, and no reader, for a header it cannot
	// authenticate, and never yields a byte it has not authenticated.
	Open(secret []byte, ciphertext io.Reader) (io.Reader, error)

	// Version is the container version recorded beside the id
	// (placement_encryption.format_version). At least 1.
	Version() int

	// Status is the suite's place in its lifecycle.
	Status() Status

	// Describe is the one sentence any surface may print about the suite.
	Describe() Description

	// Seal returns a writer that encrypts what is written to it to
	// recipient and writes the stored bytes to dst. Close finishes the
	// stream (the final authenticated chunk); bytes written to dst before
	// Close are a prefix and must not be published.
	Seal(dst io.Writer, recipient string) (io.WriteCloser, error)
}

// ErrUnknownSuite is the refusal for a suite id this build has no adapter
// for. It is never a corruption and never a wrong key: nothing was tried.
var ErrUnknownSuite = errors.New("copycrypt: this build has no suite with that id")

// suites is the compile-time table, in the order surfaces list it. Adding a
// suite is one adapter file, one row here, one line in the golden list in
// suite_test.go, and the shared vectors in suitetest going green for it.
var suites = []Suite{
	AgeX25519{},
}

// defaultID is the suite a new managed key is minted under when no suite
// is named. Exactly one, and it must be supported; a test pins both.
const defaultID = AgeX25519ID

// Suites returns the whole table, in order. The slice is a copy.
func Suites() []Suite {
	return append([]Suite(nil), suites...)
}

// Default returns the build's default suite.
func Default() Suite {
	s, ok := Lookup(defaultID)
	if !ok {
		// The table and defaultID are both constants of this file and a
		// test pins that one names a row of the other.
		panic("copycrypt: the default suite " + defaultID + " is not in the table")
	}
	return s
}

// Lookup returns the suite with id, or false when this build has none.
func Lookup(id string) (Suite, bool) {
	for _, s := range suites {
		if s.ID() == id {
			return s, true
		}
	}
	return nil, false
}
