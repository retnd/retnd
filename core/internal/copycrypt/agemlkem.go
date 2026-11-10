package copycrypt

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// AgeMLKEM768X25519ID is the suite id of age v1 with the native
// mlkem768x25519 hybrid recipient (ADR 0033 D1, D2).
const AgeMLKEM768X25519ID = "age-pq/v1"

// AgeMLKEM768X25519 is the suite age-pq/v1: the same age v1 container and
// payload as age/v1 (ChaCha20-Poly1305 STREAM, 64 KiB chunks, HMAC'd
// header), with the file key wrapped to one ML-KEM-768 + X25519 hybrid
// recipient through HPKE, which filippo.io/age ships natively since
// v1.3.0 (age.GenerateHybridIdentity, age.ParseHybridRecipient,
// age.ParseHybridIdentity). Nothing here is designed: the stanza, the KEM
// combination and the labels are the library's and the C2SP age spec's.
//
// The identity is one line, "AGE-SECRET-KEY-PQ-1...", and the recipient is
// "age1pq1..." (about 2000 characters). A copy opens on any machine with
// the stock `age` CLI 1.3 or newer: `age -d -i key.txt copy.age`.
//
// The age file key is 16 bytes, so this is a 128-bit security level
// construction like age/v1; what it adds is key establishment that a
// recording adversary with a future quantum computer cannot undo. The age
// spec says a file should not mix post-quantum and classical recipients,
// and Seal writes exactly one.
type AgeMLKEM768X25519 struct{}

// ID implements Suite.
func (AgeMLKEM768X25519) ID() string { return AgeMLKEM768X25519ID }

// Version implements Suite.
func (AgeMLKEM768X25519) Version() int { return 1 }

// Status implements Suite.
func (AgeMLKEM768X25519) Status() Status { return StatusSupported }

// Describe implements Suite.
func (AgeMLKEM768X25519) Describe() Description {
	return "age v1 file, one ML-KEM-768 + X25519 hybrid recipient, ChaCha20-Poly1305 in 64 KiB authenticated chunks. " +
		"128-bit security level, post-quantum key establishment. Not AES, not FIPS-validated. " +
		"Opens with `age -d -i key.txt` (age 1.3 or newer)."
}

// Generate implements Suite. age.GenerateHybridIdentity draws from
// crypto/rand inside the library.
func (AgeMLKEM768X25519) Generate() ([]byte, string, error) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		return nil, "", fmt.Errorf("copycrypt: generating an age post-quantum identity: %w", err)
	}
	return []byte(id.String()), id.Recipient().String(), nil
}

// ErrNotAHybridIdentity is the refusal for a secret that is not an age
// ML-KEM-768 + X25519 identity. It never quotes the secret.
var ErrNotAHybridIdentity = errors.New("copycrypt: the key is not an age post-quantum identity (expected one line starting AGE-SECRET-KEY-PQ-1)")

func parseHybridIdentity(secret []byte) (*age.HybridIdentity, error) {
	id, err := age.ParseHybridIdentity(strings.TrimSpace(string(secret)))
	if err != nil {
		// Dropped on purpose: a parse error can carry part of the input.
		return nil, ErrNotAHybridIdentity
	}
	return id, nil
}

// Recipient implements Suite.
func (AgeMLKEM768X25519) Recipient(secret []byte) (string, error) {
	id, err := parseHybridIdentity(secret)
	if err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}

// Open implements Suite, with age.Decrypt's guarantees: the header is
// authenticated before the reader is returned and every 64 KiB chunk before
// its bytes are released.
func (AgeMLKEM768X25519) Open(secret []byte, ciphertext io.Reader) (io.Reader, error) {
	id, err := parseHybridIdentity(secret)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(ciphertext, id)
	if err != nil {
		return nil, fmt.Errorf("copycrypt: opening the age stream: %w", err)
	}
	return r, nil
}

// Seal implements Suite: exactly one hybrid recipient, so the spec's rule
// against mixing recipient types holds by construction.
func (AgeMLKEM768X25519) Seal(dst io.Writer, recipient string) (io.WriteCloser, error) {
	rcpt, err := age.ParseHybridRecipient(strings.TrimSpace(recipient))
	if err != nil {
		return nil, fmt.Errorf("%w: the recipient is not a valid age post-quantum public key", ErrKeyUnavailable)
	}
	return age.Encrypt(dst, rcpt)
}

var _ Suite = AgeMLKEM768X25519{}
