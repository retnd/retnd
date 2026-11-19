package keycustody_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"io"
	"testing"

	"filippo.io/age"

	"github.com/retnd/retnd/core/internal/keycustody"
)

// testFormat is the production format, keycustody.AgeFormat: the claims in
// this package are about where a key lives and whether it can recover a
// copy, and a double would prove nothing about the format ADR 0027 chose.
var testFormat keycustody.Format = keycustody.AgeFormat{}

// sealTo writes plaintext as an age file to recipient. It is the write half
// the custody package deliberately does not have, here only to make a
// fixture copy a bundle can open.
func sealTo(t *testing.T, recipient string, plaintext []byte) []byte {
	t.Helper()
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		t.Fatalf("parse recipient: %v", err)
	}
	var out bytes.Buffer
	w, err := age.Encrypt(&out, r)
	if err != nil {
		t.Fatalf("age.Encrypt: %v", err)
	}
	if _, err := io.Copy(w, bytes.NewReader(plaintext)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out.Bytes()
}

// renderings is every way key material could plausibly reach a text
// surface: the raw bytes, the base64 the key file holds, the URL-safe
// base64 a careless encoder would pick, and hex. A scan that only looked
// for the raw bytes would pass a JSON surface that base64-encoded them.
func renderings(material []byte) [][]byte {
	return [][]byte{
		material,
		[]byte(base64.StdEncoding.EncodeToString(material)),
		[]byte(base64.RawStdEncoding.EncodeToString(material)),
		[]byte(base64.RawURLEncoding.EncodeToString(material)),
		[]byte(hex.EncodeToString(material)),
	}
}

// carriesMaterial reports whether any rendering of material appears in
// surface. It is the one predicate every canary test and every control in
// this package shares, so a control that proves it catches a planted
// value proves it for the real tests too.
func carriesMaterial(surface, material []byte) bool {
	for _, r := range renderings(material) {
		if bytes.Contains(surface, r) {
			return true
		}
	}
	return false
}
