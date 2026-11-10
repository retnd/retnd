package copycrypt

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// AgeX25519ID is the suite id ADR 0027 recorded per copy and ADR 0033 D1
// keeps for ever: age v1, one X25519 recipient, ChaCha20-Poly1305 payload.
const AgeX25519ID = "age/v1"

// AgeX25519 is the suite age/v1 (https://c2sp.org/age) through
// filippo.io/age, with an X25519 identity as the key. The identity string
// ("AGE-SECRET-KEY-1...") is what a managed key file and a bundle hold, so
// a person with the bundle and the stock `age` CLI can recover a copy:
// `age -d -i identity.txt copy.age`.
//
// No scrypt recipient is used, so there is no passphrase and no work factor
// for an operator to get wrong. keycustody.AgeFormat is this type.
type AgeX25519 struct{}

// ID implements Suite.
func (AgeX25519) ID() string { return AgeX25519ID }

// Version implements Suite.
func (AgeX25519) Version() int { return 1 }

// Status implements Suite.
func (AgeX25519) Status() Status { return StatusSupported }

// Describe implements Suite.
func (AgeX25519) Describe() Description {
	return "age v1 file, one X25519 recipient, ChaCha20-Poly1305 in 64 KiB authenticated chunks. " +
		"128-bit security level. Not post-quantum. Opens with `age -d -i key.txt`."
}

// Generate implements Suite. age.GenerateX25519Identity draws from
// crypto/rand. The identity is a Go string inside the library and cannot be
// zeroed; the copy returned here is the one the caller takes responsibility
// for.
func (AgeX25519) Generate() ([]byte, string, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, "", fmt.Errorf("keycustody: generating an age identity: %w", err)
	}
	return []byte(id.String()), id.Recipient().String(), nil
}

// ErrNotAnIdentity is the refusal for a secret that is not an age X25519
// identity. It never quotes the secret. keycustody.ErrNotAnIdentity is this
// value, and its text predates this package's ownership of it.
var ErrNotAnIdentity = errors.New("keycustody: the key is not an age X25519 identity (expected one line starting AGE-SECRET-KEY-1)")

func parseX25519Identity(secret []byte) (*age.X25519Identity, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(secret)))
	if err != nil {
		// The parse error is dropped on purpose: it can quote the input.
		return nil, ErrNotAnIdentity
	}
	return id, nil
}

// Recipient implements Suite.
func (AgeX25519) Recipient(secret []byte) (string, error) {
	id, err := parseX25519Identity(secret)
	if err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}

// Open implements Suite. age.Decrypt authenticates the header before it
// returns and the payload per 64 KiB chunk, with a final-chunk flag, so a
// truncated or reordered stream is a read error. A consumer must still not
// publish what it read until it reaches EOF without error (ADR 0027).
func (AgeX25519) Open(secret []byte, ciphertext io.Reader) (io.Reader, error) {
	id, err := parseX25519Identity(secret)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(ciphertext, id)
	if err != nil {
		return nil, fmt.Errorf("keycustody: opening the age stream: %w", err)
	}
	return r, nil
}

// Seal implements Suite: exactly one X25519 recipient, written by
// age.Encrypt, so the age spec's rule against mixing recipient types in one
// file holds by construction.
func (AgeX25519) Seal(dst io.Writer, recipient string) (io.WriteCloser, error) {
	rcpt, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return nil, fmt.Errorf("%w: the recipient is not a valid age X25519 public key", ErrKeyUnavailable)
	}
	return age.Encrypt(dst, rcpt)
}

var _ Suite = AgeX25519{}
