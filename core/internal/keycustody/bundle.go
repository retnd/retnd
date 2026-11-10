package keycustody

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// bundleVersion is the bundle's own container id. It is this package's, not
// ADR 0027's: the bundle wraps a key and records its integrity metadata, and
// the key inside is whatever the Format writes (for age, an identity line a
// person can lift out and hand to the stock age CLI).
const bundleVersion = "retnd-key-bundle/1"

// maxBundleSize bounds what ReadBundle will parse. A bundle is a few
// hundred bytes of JSON; a megabyte is a wide margin against a file that is
// not a bundle at all.
const maxBundleSize = 1 << 20

// ErrBundleIntegrity is ReadBundle's refusal when the digest does not cover
// the bytes it was handed: altered material, a relabelled key id, a
// different format, or a file that was never a bundle.
var ErrBundleIntegrity = errors.New("keycustody: the bundle's integrity digest does not match its contents")

// Bundle is the disaster-recovery bundle: the ONE shape in this package
// that carries managed key material, written once to a writer the caller
// chose and read back on a machine that has nothing else. It is listed as
// the single carrier in surfaces_test.go, and no route or CLI verb returns
// one; what they return is the Receipt.
type Bundle struct {
	// Version is bundleVersion.
	Version string `json:"version"`

	// KeyID is the managed key this bundle holds, as the journal and the
	// recovery sidecar record it against each copy.
	KeyID string `json:"key_id"`

	// FormatID is Format.ID at export time, so a recovery can refuse a
	// format the binary in hand does not ship rather than produce garbage.
	FormatID string `json:"format"`

	// Recipient is the key's public half. A recovery checks the material
	// derives it, and a person can match it to a copy's header.
	Recipient string `json:"recipient"`

	// Created is when the key was minted; Exported is when this bundle was.
	Created  time.Time `json:"created"`
	Exported time.Time `json:"exported"`

	// Material is the key exactly as the format writes it (for age, the
	// one-line identity, which `age -i` reads as is). This is the field, and
	// the only field anywhere in this package, that holds material.
	Material string `json:"material"`

	// Digest is hex SHA-256 over the identity fields and the material (see
	// digest), so a bundle whose key id was edited to match a different
	// copy's record is refused as firmly as one whose material was.
	Digest string `json:"digest"`

	// Instructions are the human-readable steps, for a person who has this
	// file, the shipped binary and docs/recovery-without-a-terminal.md and
	// nothing else.
	Instructions []string `json:"instructions"`
}

// Receipt is what Export returns to its caller, and therefore what a route
// or a CLI verb renders: the key id, the digest a person will later type
// back to confirm, and when. No material.
type Receipt struct {
	KeyID      string    `json:"key_id"`
	Digest     string    `json:"digest"`
	ExportedAt time.Time `json:"exported_at"`
}

// digest is the bundle's integrity value: SHA-256 over the fields a
// recovery trusts, newline-separated in a fixed order. The instructions are
// deliberately outside it, so a translation or a wording fix does not
// invalidate every bundle in a safe.
func (b Bundle) digest() string {
	h := sha256.New()
	for _, field := range []string{b.Version, b.KeyID, b.FormatID, b.Recipient, b.Created.UTC().Format(time.RFC3339Nano), b.Material} {
		h.Write([]byte(field))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Export writes the bundle for managed key id to w, records the export and
// returns its receipt. This is the one moment material leaves the host, and
// it is explicit: nothing calls it but a deliberate operator action.
func (s *Store) Export(ctx context.Context, id string, w io.Writer, now time.Time) (Receipt, error) {
	rec, err := s.record(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	b := Bundle{
		Version:      bundleVersion,
		KeyID:        rec.ID,
		FormatID:     s.format.ID(),
		Recipient:    rec.Recipient,
		Created:      rec.Created.UTC(),
		Exported:     now.UTC(),
		Instructions: instructions(rec.ID),
	}
	if err := s.WithKey(ctx, id, func(key []byte) error {
		b.Material = string(key)
		return nil
	}); err != nil {
		return Receipt{}, err
	}
	b.Digest = b.digest()

	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return Receipt{}, fmt.Errorf("keycustody: encoding bundle: %w", err)
	}
	if _, err := w.Write(append(out, '\n')); err != nil {
		return Receipt{}, fmt.Errorf("keycustody: writing bundle: %w", err)
	}
	receipt := Receipt{KeyID: rec.ID, Digest: b.Digest, ExportedAt: b.Exported}
	if err := s.registry.RecordExport(ctx, ExportRecord{KeyID: rec.ID, Digest: b.Digest, ExportedAt: b.Exported}); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

// Confirm records that a person holds the bundle with digest for key id.
// The digest has to match an export this registry recorded, so confirming
// is proof of possession of the receipt and not a checkbox.
func (s *Store) Confirm(ctx context.Context, id, digest string, now time.Time) error {
	if _, err := s.record(ctx, id); err != nil {
		return err
	}
	exports, err := s.registry.Exports(ctx, id)
	if err != nil {
		return err
	}
	var matched bool
	for _, e := range exports {
		if e.Digest == digest {
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("%w: key %s", ErrNoSuchExport, id)
	}
	return s.registry.Confirm(ctx, Confirmation{KeyID: id, Digest: digest, ConfirmedAt: now.UTC()})
}

// ReadBundle parses a bundle and refuses one whose digest does not cover
// its contents. It needs nothing but the bytes: no registry, no key
// directory, no journal.
func ReadBundle(r io.Reader) (Bundle, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBundleSize+1))
	if err != nil {
		return Bundle{}, fmt.Errorf("keycustody: reading bundle: %w", err)
	}
	if len(raw) > maxBundleSize {
		return Bundle{}, fmt.Errorf("%w: the file is larger than any bundle", ErrBundleIntegrity)
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return Bundle{}, fmt.Errorf("%w: not a bundle: %v", ErrBundleIntegrity, err)
	}
	if b.Version != bundleVersion {
		return Bundle{}, fmt.Errorf("%w: version %q is not one this build reads", ErrBundleIntegrity, b.Version)
	}
	if b.Digest != b.digest() {
		return Bundle{}, ErrBundleIntegrity
	}
	if b.Material == "" || b.Recipient == "" {
		return Bundle{}, fmt.Errorf("%w: the bundle holds no key", ErrBundleIntegrity)
	}
	return b, nil
}

// Decrypt streams ciphertext through f under this bundle's key into out.
// It is the clean-machine door: a person with the bundle and the binary,
// and no deployment, gets a copy back. It refuses a format other than the
// one the bundle was exported under, and it relies on Format.Open's
// contract that nothing unauthenticated is yielded.
func (b Bundle) Decrypt(f Format, ciphertext io.Reader, out io.Writer) error {
	if f.ID() != b.FormatID {
		return fmt.Errorf("keycustody: the bundle was exported under format %q and this build opens %q", b.FormatID, f.ID())
	}
	key := []byte(b.Material)
	defer zero(key)
	if recipient, err := f.Recipient(key); err != nil || recipient != b.Recipient {
		return fmt.Errorf("keycustody: the bundle's material is not the key %s names", b.KeyID)
	}
	plain, err := f.Open(key, ciphertext)
	if err != nil {
		return fmt.Errorf("keycustody: opening the copy with key %s: %w", b.KeyID, err)
	}
	if _, err := io.Copy(out, plain); err != nil {
		return fmt.Errorf("keycustody: recovering the copy with key %s: %w", b.KeyID, err)
	}
	return nil
}

// instructions are the bundle's human-readable half. They name the doc and
// the shape of the recovery, and not a CLI verb, because the verbs are
// #993's and a wrong verb in a safe is worse than none.
func instructions(keyID string) []string {
	return []string{
		"This file holds the encryption key " + keyID + " for this deployment's encrypted retained copies. Anyone holding it can read those copies. Keep it where you keep the thing you would need after losing the machine: a password manager, a printed copy in a safe, an encrypted USB key kept elsewhere.",
		"Every encrypted copy records the key id it was written under, both in the deployment's journal and in the recovery sidecar stored next to the copy. A copy recording key " + keyID + " is one this file opens.",
		"To recover on a clean machine: install the shipped binary, give it this file and the encrypted copy, and it will check the digest below, decrypt the copy, and refuse to call it recovered unless the plaintext digest matches what the sidecar recorded. docs/recovery-without-a-terminal.md walks through it without assuming a terminal.",
		"The copies are age files (https://c2sp.org/age) and the \"material\" field is an age identity. Without this product at all: save that one line to identity.txt and run: age -d -i identity.txt copy.age > recovered. The \"recipient\" field is the public half, and the copy's age header names it.",
		"If the deployment ever reports that its key bundle is not confirmed, confirm it by entering the digest below where the interface asks for it. The digest is not secret; the material above is.",
		"Do not edit this file. The digest covers the key id, the format and the material, and an edited file is refused.",
	}
}
