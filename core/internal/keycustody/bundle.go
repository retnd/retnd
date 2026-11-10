package keycustody

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
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

	// Digest is hex SHA-256 over the identity fields, the export time and
	// the material (see digest), so a bundle whose key id was edited to match a different
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
// invalidate every bundle in a safe. The export time is inside it, so two
// exports of one key are two digests and two recorded rows.
func (b Bundle) digest() string {
	h := sha256.New()
	for _, field := range []string{b.Version, b.KeyID, b.FormatID, b.Recipient, b.Created.UTC().Format(time.RFC3339Nano), b.Exported.UTC().Format(time.RFC3339Nano), b.Material} {
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
	if rec.Provider == ProviderExternal {
		return Receipt{}, fmt.Errorf("%w: %s", ErrExternalKeyNotExportable, id)
	}
	b := Bundle{
		Version:      bundleVersion,
		KeyID:        rec.ID,
		FormatID:     suiteOf(rec),
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
	receipt := Receipt{KeyID: rec.ID, Digest: b.Digest, ExportedAt: b.Exported}
	// Record before writing: an export the registry refuses must not leave
	// key material in the destination.
	if err := s.registry.RecordExport(ctx, ExportRecord{KeyID: rec.ID, Digest: b.Digest, ExportedAt: b.Exported}); err != nil {
		return Receipt{}, err
	}
	if _, err := w.Write(append(out, '\n')); err != nil {
		return Receipt{}, fmt.Errorf("keycustody: writing bundle: %w", err)
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

// instructions are the bundle's human-readable half. They name the two verbs
// that exist (`retnd encryption decrypt` and `retnd encryption key import`,
// #1289), the doc, and the age CLI as the way that needs nothing of this
// product. A verb named here that the binary lacks is worse than none, which
// is why a test in this package and one in core/cmd/retnd read them.
func instructions(keyID string) []string {
	return []string{
		"This file holds the encryption key " + keyID + " for this deployment's encrypted retained copies. Anyone holding it can read those copies. Keep it where you keep the thing you would need after losing the machine: a password manager, a printed copy in a safe, an encrypted USB key kept elsewhere.",
		"Every encrypted copy records the key id it was written under, both in the deployment's journal and in the recovery sidecar stored next to the copy. A copy recording key " + keyID + " is one this file opens.",
		"To recover on a clean machine: install the shipped binary and run: retnd encryption decrypt <copy> --bundle <this file> --sidecar <the copy's sidecar> --out <recovered file>. It checks this file's digest, checks the copy against the stored-bytes digest the sidecar records, decrypts, and creates the output file only after every chunk authenticated and the plaintext digest matched, so a damaged or cut-short copy leaves nothing behind. To make a deployment know this key (so copies recording key " + keyID + " resolve there), run: retnd encryption key import --bundle <this file>. docs/recovery-without-a-terminal.md walks through both without assuming a terminal.",
		"The copies are age files (https://c2sp.org/age) and the \"material\" field is an age identity. The \"format\" field names the suite: an age/v1 key opens its copies with any age release, an age-pq/v1 key needs age 1.3 or newer. Without this product at all, and without the checks above: save that one line to identity.txt and run: age -d -i identity.txt copy.age > recovered. The \"recipient\" field is the public half, and the copy's age header names it.",
		"If the deployment ever reports that its key bundle is not confirmed, confirm it by entering the digest below where the interface asks for it. The digest is not secret; the material above is.",
		"Do not edit this file. The digest covers the key id, the format, the export time and the material, and an edited file is refused.",
	}
}

// keyIDPattern is what an imported key id has to look like, because the id
// becomes a file name in the key directory. Minted ids are "k" and sixteen
// hex digits; this is wider so another build's ids still import, and
// narrower than anything that could leave the directory.
var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ErrKeyConflict is Import's refusal when the id is already held by a
// different key, in the registry or as a file in the key directory. It never
// quotes either key.
var ErrKeyConflict = errors.New("keycustody: a different key is already held under this id")

// Import registers the key a bundle carries on this machine, so copies that
// record its id resolve here. It is the clean-machine counterpart of Export:
// the bundle goes in, and nothing about the key comes back out.
//
// The imported key is registered as superseded, never active. It exists to
// open copies written elsewhere, and making it the key new copies are
// written under would be a rotation nobody asked for. Holding the bundle is
// the proof Confirm asks a person for, so the export and the confirmation it
// carries are recorded too, and the key does not sit in Health as
// unconfirmed.
//
// It is idempotent: a key already held under the id, with the same recipient
// and the same bytes, is left alone and imported is false. A key file that is
// missing for a registered key is restored (imported is true). The file is
// written under the commit discipline mint uses, before the registry is
// touched, so a refusal or a crash leaves no record without a key.
func (s *Store) Import(ctx context.Context, b Bundle, now time.Time) (rec KeyRecord, imported bool, err error) {
	if b.Version != bundleVersion || b.Digest != b.digest() {
		return KeyRecord{}, false, ErrBundleIntegrity
	}
	if !keyIDPattern.MatchString(b.KeyID) {
		return KeyRecord{}, false, fmt.Errorf("%w: %q is not a key id this build can hold", ErrBundleIntegrity, b.KeyID)
	}
	// The bundle's format is the key's suite (ADR 0033 D8). A suite this
	// build has no adapter for is refused naming it: the material could be
	// stored, but no copy written under it could ever be opened here.
	f, err := s.formatOf(b.FormatID)
	if err != nil {
		return KeyRecord{}, false, fmt.Errorf("keycustody: the bundle was exported under format %q, which this build cannot open: %w", b.FormatID, err)
	}
	material := []byte(b.Material)
	defer zero(material)
	if recipient, rerr := f.Recipient(material); rerr != nil || recipient != b.Recipient {
		return KeyRecord{}, false, fmt.Errorf("keycustody: the bundle's material is not the key %s names", b.KeyID)
	}
	if err := checkDirCustody(s.dir); err != nil {
		return KeyRecord{}, false, err
	}

	keys, err := s.registry.Keys(ctx)
	if err != nil {
		return KeyRecord{}, false, err
	}
	var existing *KeyRecord
	for i := range keys {
		if keys[i].ID == b.KeyID {
			existing = &keys[i]
		}
	}
	if existing != nil && existing.Recipient != b.Recipient {
		return KeyRecord{}, false, fmt.Errorf("%w: %s", ErrKeyConflict, b.KeyID)
	}

	path := s.keyPath(b.KeyID)
	li, statErr := os.Lstat(path)
	switch {
	case statErr == nil:
		if !li.Mode().IsRegular() {
			return KeyRecord{}, false, fmt.Errorf("%w: %s is not a plain file", ErrKeyConflict, b.KeyID)
		}
		onDisk, err := os.ReadFile(path)
		if err != nil {
			return KeyRecord{}, false, fmt.Errorf("keycustody: reading key file for %s: %w", b.KeyID, err)
		}
		same := subtle.ConstantTimeCompare(bytes.TrimSpace(onDisk), bytes.TrimSpace(material)) == 1
		zero(onDisk)
		if !same {
			return KeyRecord{}, false, fmt.Errorf("%w: %s", ErrKeyConflict, b.KeyID)
		}
	case errors.Is(statErr, os.ErrNotExist):
		encoded := append(append([]byte(nil), material...), '\n')
		defer zero(encoded)
		tmp := path + ".partial"
		if err := writeFileSync(tmp, encoded, 0o600); err != nil {
			os.Remove(tmp)
			return KeyRecord{}, false, fmt.Errorf("keycustody: writing key file: %w", err)
		}
		if err := os.Link(tmp, path); err != nil {
			os.Remove(tmp)
			return KeyRecord{}, false, fmt.Errorf("keycustody: publishing key file: %w", err)
		}
		os.Remove(tmp)
		if err := fsyncDir(s.dir); err != nil {
			return KeyRecord{}, false, err
		}
		imported = true
	default:
		return KeyRecord{}, false, fmt.Errorf("keycustody: checking key file for %s: %w", b.KeyID, statErr)
	}

	if existing != nil {
		rec = *existing
	} else {
		rec = KeyRecord{ID: b.KeyID, Recipient: b.Recipient, Created: b.Created.UTC(), Status: StatusSuperseded, SuiteID: f.ID()}
		if err := s.registry.AddKey(ctx, rec, ""); err != nil {
			return KeyRecord{}, false, err
		}
		imported = true
	}

	exports, err := s.registry.Exports(ctx, rec.ID)
	if err != nil {
		return KeyRecord{}, false, err
	}
	var exported bool
	for _, e := range exports {
		exported = exported || e.Digest == b.Digest
	}
	if !exported {
		if err := s.registry.RecordExport(ctx, ExportRecord{KeyID: rec.ID, Digest: b.Digest, ExportedAt: b.Exported.UTC()}); err != nil {
			return KeyRecord{}, false, err
		}
	}
	confirmations, err := s.registry.Confirmations(ctx)
	if err != nil {
		return KeyRecord{}, false, err
	}
	var confirmed bool
	for _, c := range confirmations {
		confirmed = confirmed || (c.KeyID == rec.ID && c.Digest == b.Digest)
	}
	if !confirmed {
		if err := s.registry.Confirm(ctx, Confirmation{KeyID: rec.ID, Digest: b.Digest, ConfirmedAt: now.UTC()}); err != nil {
			return KeyRecord{}, false, err
		}
	}
	return rec, imported, nil
}
