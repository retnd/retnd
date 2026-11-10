package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/recovery"
	"github.com/retnd/retnd/core/service"
)

// `retnd encryption decrypt` and `retnd encryption key import` (#1289): the
// two verbs a lost-host recovery needs from the shipped binary.
//
// # decrypt
//
//	encryption decrypt <object> (--bundle FILE | --identity FILE)
//	                   (--sidecar FILE | --digest HEX [--stored-digest HEX]) --out FILE
//
// It opens one encrypted copy into one file. It reads no configuration and
// no state database, so it works on a machine that has the binary, the copy
// and the bundle and nothing else, which is the case it exists for. The age
// CLI stays the independent fallback (docs/encrypted-copies.md); this verb is
// the one that also checks the digests.
//
// What it promises, and where each promise is kept:
//
//   - No partial plaintext. copycrypt.DecryptToFile writes to a temporary
//     name in the destination directory and renames it only after the final
//     chunk authenticated and the plaintext digest matched, so a truncated
//     copy leaves nothing behind. Bundle.Decrypt streams into a writer and
//     cannot make that promise, so this verb does not use it.
//   - The recorded digests are checked, both of them. The stored-bytes digest
//     is checked before any key is used (it says whether this is the object
//     the sidecar describes and whether it was cut or altered), and the
//     plaintext digest is checked by the decrypt itself.
//   - The key and the plaintext are never printed. The only things written to
//     standard output are the destination, a byte count and digests.
//   - There is no standard output form for the plaintext, and an existing
//     destination is never replaced.
//
// # key import
//
// It registers the key a bundle carries in this host's own deployment, so
// copies recorded under that key id resolve here. It goes to the process that
// holds the deployment's journal and key directory, which means it refuses,
// with the engine-holds-the-deployment status, while an engine serves it.
//
// Neither verb has an API route. See the note in encryption.go.

// maxKeyFile bounds an identity file read, as maxBundleSize does for bundles.
const maxKeyFile = 1 << 20

func cmdEncryptionDecrypt(args []string) int {
	fs := flag.NewFlagSet("encryption decrypt", flag.ContinueOnError)
	bundlePath := fs.String("bundle", "", "the recovery bundle that holds the key")
	identityPath := fs.String("identity", "", "an age identity file that holds the key, in place of a bundle")
	sidecarPath := fs.String("sidecar", "", "the copy's recovery sidecar, which supplies both recorded digests and the key id")
	plainDigest := fs.String("digest", "", "the recorded plaintext digest (hex SHA-256), in place of a sidecar")
	storedDigest := fs.String("stored-digest", "", "with --digest: the recorded stored-bytes digest (hex SHA-256) to check first")
	out := fs.String("out", "", "the file to write the recovered artifact to; it must not exist")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	switch {
	case len(operands) != 1:
		return usageError("encryption decrypt: expected exactly one encrypted copy to open")
	case (*bundlePath == "") == (*identityPath == ""):
		return usageError("encryption decrypt: name the key with exactly one of --bundle FILE or --identity FILE")
	case (*sidecarPath == "") == (*plainDigest == ""):
		return usageError("encryption decrypt: give the recorded digest with exactly one of --sidecar FILE or --digest HEX; a copy that is not checked against a digest is not recovered")
	case *sidecarPath != "" && *storedDigest != "":
		return usageError("encryption decrypt: --stored-digest goes with --digest; a sidecar carries its own")
	case *out == "" || *out == "-":
		return usageError("encryption decrypt: --out FILE is required; the recovered artifact is written to a file and never to standard output")
	}
	for flagName, v := range map[string]string{"--digest": *plainDigest, "--stored-digest": *storedDigest} {
		if v != "" && !isHexSHA256(v) {
			return usageError("encryption decrypt: %s is not a hex SHA-256 digest (64 hexadecimal characters)", flagName)
		}
	}
	object := operands[0]

	identity, keyID, suite, err := loadDecryptKey(*bundlePath, *identityPath)
	if err != nil {
		return fail(err)
	}
	defer func() {
		for i := range identity {
			identity[i] = 0
		}
	}()

	src, err := os.Open(object)
	if err != nil {
		return fail(fmt.Errorf("opening the encrypted copy: %w", err))
	}
	defer src.Close()

	wantPlain, wantStored := strings.ToLower(*plainDigest), strings.ToLower(*storedDigest)
	if *sidecarPath != "" {
		// The stored digest it returns is one src was already hashed against.
		wantPlain, wantStored, err = digestsFromSidecar(*sidecarPath, src, keyID)
		if err != nil {
			return fail(err)
		}
	} else if wantStored != "" {
		got, err := hashFile(src)
		if err != nil {
			return fail(fmt.Errorf("reading the encrypted copy: %w", err))
		}
		if got != wantStored {
			return fail(fmt.Errorf("the stored object %s hashes to %s and the recorded stored digest is %s: this is not the copy that was recorded, or it was cut short or altered; nothing was decrypted", object, got, wantStored))
		}
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("reading the encrypted copy: %w", err))
	}

	dir, name := filepath.Split(*out)
	if dir == "" {
		dir = "."
	}
	if _, err := os.Lstat(*out); err == nil {
		return fail(fmt.Errorf("refusing to replace %s: it already exists; choose another --out or move it aside", *out))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("checking %s: %w", *out, err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	written, err := copycrypt.DecryptToFile(ctx, src, suite, identity, wantPlain, dir, name)
	if err != nil {
		return fail(fmt.Errorf("decrypting %s: %w", object, err))
	}
	var size int64
	if info, err := os.Stat(written); err == nil {
		size = info.Size()
	}
	fmt.Printf("decrypted %s to %s (%d bytes)\n", object, written, size)
	fmt.Printf("every chunk authenticated and the plaintext digest equals the recorded one (sha256 %s)\n", wantPlain)
	if wantStored != "" {
		fmt.Printf("the stored object matched its recorded digest (sha256 %s)\n", wantStored)
	}
	return exitOK
}

// loadDecryptKey reads the key from a bundle or an identity file and returns
// the identity, the suite it is a key of and, for a bundle, the key id it
// names. A bundle is read through keycustody.ReadBundle, so an edited one is
// refused by its integrity digest; its format names its suite (ADR 0033 D8),
// a suite this build has no adapter for is refused naming it, and its
// material has to derive the recipient it names under that suite. An
// identity file's suite is the one registered suite whose key it is.
func loadDecryptKey(bundlePath, identityPath string) (identity []byte, keyID string, suite copycrypt.Suite, err error) {
	if bundlePath != "" {
		f, err := os.Open(bundlePath)
		if err != nil {
			return nil, "", nil, fmt.Errorf("opening the bundle: %w", err)
		}
		defer f.Close()
		b, err := keycustody.ReadBundle(f)
		if err != nil {
			return nil, "", nil, err
		}
		suite, ok := copycrypt.Lookup(b.FormatID)
		if !ok {
			return nil, "", nil, fmt.Errorf("the bundle was exported under format %q, which this build cannot open: %w", b.FormatID, copycrypt.ErrUnknownSuite)
		}
		identity = []byte(b.Material)
		if recipient, err := suite.Recipient(identity); err != nil || recipient != b.Recipient {
			return nil, "", nil, fmt.Errorf("the bundle's material is not the key %s names", b.KeyID)
		}
		return identity, b.KeyID, suite, nil
	}

	f, err := os.Open(identityPath)
	if err != nil {
		return nil, "", nil, fmt.Errorf("opening the identity file: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxKeyFile))
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		identity = append([]byte(nil), line...)
		break
	}
	if len(identity) == 0 {
		return nil, "", nil, fmt.Errorf("%s holds no age identity", identityPath)
	}
	// Every suite in the table is asked, in order; a line no suite accepts
	// is refused with the age/v1 refusal, which never quotes the line.
	var firstErr error
	for _, candidate := range copycrypt.Suites() {
		if _, err := candidate.Recipient(identity); err == nil {
			return identity, "", candidate, nil
		} else if firstErr == nil || candidate.ID() == copycrypt.AgeX25519ID {
			firstErr = err
		}
	}
	return nil, "", nil, firstErr
}

// digestsFromSidecar reads the recorded digests of the encrypted copy in src
// from its sidecar. A sidecar can describe several copies of one artifact
// (one per medium), so the entry is the one whose stored digest src hashes
// to; if none does, the copy is not one the sidecar describes and the
// refusal says so, naming the digest src has. When keyID is known it has to
// be the key the sidecar says the copy was written under.
func digestsFromSidecar(path string, src *os.File, keyID string) (plain, stored string, err error) {
	m, err := recovery.ReadManifest(path)
	if err != nil {
		return "", "", err
	}
	var candidates []*recovery.ManifestEncryption
	for _, p := range m.Placements {
		if p.Encryption != nil {
			candidates = append(candidates, p.Encryption)
		}
	}
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("%s records no encrypted copy, so there is no digest to check this one against", path)
	}
	got, err := hashFile(src)
	if err != nil {
		return "", "", fmt.Errorf("reading the encrypted copy: %w", err)
	}
	var enc *recovery.ManifestEncryption
	for _, c := range candidates {
		if strings.EqualFold(c.StoredDigest, got) {
			enc = c
		}
	}
	switch {
	case enc != nil:
	case len(candidates) == 1 && candidates[0].StoredDigest == "":
		enc = candidates[0] // nothing was recorded to check, and there is only one copy it can be
	case len(candidates) == 1:
		return "", "", fmt.Errorf("the stored object hashes to %s and %s records stored digest %s: this is not the copy that was recorded, or it was cut short or altered; nothing was decrypted", got, path, candidates[0].StoredDigest)
	default:
		return "", "", fmt.Errorf("the stored object hashes to %s, which none of the %d encrypted copies in %s records as its stored digest; nothing was decrypted", got, len(candidates), path)
	}
	if enc.PlaintextDigestAlgorithm != "sha256" || (enc.StoredDigest != "" && enc.StoredDigestAlgorithm != "sha256") {
		return "", "", fmt.Errorf("%s records digests under an algorithm this build does not check (%q, %q)", path, enc.PlaintextDigestAlgorithm, enc.StoredDigestAlgorithm)
	}
	if !isHexSHA256(enc.PlaintextDigest) {
		return "", "", fmt.Errorf("%s records no usable plaintext digest", path)
	}
	if keyID != "" && enc.KeyID != keyID {
		return "", "", fmt.Errorf("%s says this copy was written under key %s and this bundle holds key %s", path, enc.KeyID, keyID)
	}
	return strings.ToLower(enc.PlaintextDigest), strings.ToLower(enc.StoredDigest), nil
}

func hashFile(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isHexSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// cmdEncryptionKeyImport is `key import --bundle FILE`. The bundle is read by
// the service, which registers the key and answers with its status, never
// its material.
func cmdEncryptionKeyImport(ctx context.Context, cfgPath, bundlePath string, asJSON bool) int {
	f, err := os.Open(bundlePath)
	if err != nil {
		return fail(fmt.Errorf("opening the bundle: %w", err))
	}
	defer f.Close()

	route, cleanup, err := openEncryptionWrite(ctx, cfgPath, asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	svc, ok := route.(*service.BackupService)
	if !ok {
		return fail(fmt.Errorf("%w: key import writes this host's own key directory and journal, which the serving engine holds; stop it, import, and start it again", errEngineHoldsDeployment))
	}
	got, imported, err := svc.ImportRecoveryBundle(ctx, f)
	if err != nil {
		return fail(err)
	}
	return encryptionAnswer(asJSON, got, func() {
		switch {
		case !imported:
			fmt.Printf("key %s is already registered here with this key; nothing changed\n", got.ID)
		case !got.Resolvable:
			fmt.Printf("registered key %s from the bundle, but it does not resolve yet: %s\n", got.ID, got.Reason)
		default:
			fmt.Printf("registered key %s from the bundle: it opens copies recorded under that id, and new copies keep using the active key\n", got.ID)
		}
	})
}
