package keycustody

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// AgeFormatID is the format id ADR 0027 records per copy.
const AgeFormatID = "age/v1"

// AgeFormat is Format over age v1 (https://c2sp.org/age) through
// filippo.io/age, with an X25519 identity as the key. The identity string
// ("AGE-SECRET-KEY-1...") is what a managed key file and a bundle hold, so
// a person with the bundle and the stock `age` CLI can recover a copy:
// `age -d -i identity.txt copy.age`.
//
// No scrypt recipient is used, so there is no passphrase and no work factor
// for an operator to get wrong.
type AgeFormat struct{}

// ID implements Format.
func (AgeFormat) ID() string { return AgeFormatID }

// Generate implements Format. age.GenerateX25519Identity draws from
// crypto/rand. The identity is a Go string inside the library and cannot be
// zeroed; the copy returned here is the one this package takes
// responsibility for.
func (AgeFormat) Generate() ([]byte, string, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, "", fmt.Errorf("keycustody: generating an age identity: %w", err)
	}
	return []byte(id.String()), id.Recipient().String(), nil
}

// ErrNotAnIdentity is the refusal for a secret that is not an age X25519
// identity. It never quotes the secret.
var ErrNotAnIdentity = errors.New("keycustody: the key is not an age X25519 identity (expected one line starting AGE-SECRET-KEY-1)")

func parseIdentity(secret []byte) (*age.X25519Identity, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(secret)))
	if err != nil {
		return nil, ErrNotAnIdentity
	}
	return id, nil
}

// Recipient implements Format.
func (AgeFormat) Recipient(secret []byte) (string, error) {
	id, err := parseIdentity(secret)
	if err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}

// Open implements Format. age.Decrypt authenticates the header before it
// returns and the payload per 64 KiB chunk, with a final-chunk flag, so a
// truncated or reordered stream is a read error. A consumer must still not
// publish what it read until it reaches EOF without error (ADR 0027).
func (AgeFormat) Open(secret []byte, ciphertext io.Reader) (io.Reader, error) {
	id, err := parseIdentity(secret)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(ciphertext, id)
	if err != nil {
		return nil, fmt.Errorf("keycustody: opening the age stream: %w", err)
	}
	return r, nil
}

var _ Format = AgeFormat{}
