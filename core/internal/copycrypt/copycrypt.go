// Package copycrypt is the byte path of retained-copy encryption (EPIC N,
// N1.3, #991): a streaming encrypt into a staging file and a streaming
// decrypt that proves the plaintext digest, both through a Suite from the
// registry in suite.go (EPIC T, ADR 0033). The registry's suites are age v1
// constructions that ADR 0027 and ADR 0033 chose, and nothing else.
//
// Nothing here designs cryptography. The format, the chunking (64 KiB, a
// property of the age specification and not a value this package defines),
// the header MAC, the final-chunk flag and the per-file payload nonce all
// belong to filippo.io/age. This package decides where the bytes go and
// which digests are measured over which bytes, and which suite a copy is
// written and read under.
//
// # The seam
//
// ADR 0027 chose an encrypted staging pass: the engine encrypts the local
// artifact into a file on the backup set's own disk, hashing the ciphertext
// as it is written, and hands that path to UploadFromLocal unchanged.
// EncryptToStaging is that pass. It writes to a temporary file, fsyncs it,
// and renames it over the final name only after the last chunk was sealed
// and the plaintext digest matched the one the journal recorded, so a name
// in the staging directory is either absent or a complete object, never a
// prefix of one. A retry is always a fresh encryption (fresh file key and
// payload nonce, chosen by age), never a resumed one.
//
// # Two digests
//
// Staged carries both and keeps them apart: PlaintextHash is what the
// artifact hashes to (artifacts.local_hash), StoredHash is what the bytes
// at the staging path hash to. Ciphertext is not deterministic, so a
// StoredHash describes one attempt's object and no other; callers record it
// only from the attempt that reached the durable phase advance and never
// compare it to a later encryption.
//
// # No partial plaintext
//
// age releases plaintext per authenticated chunk, so a reader that streams
// decrypted bytes somewhere before EOF has handed out authenticated but
// possibly truncated data. DecryptVerify therefore sends plaintext only
// into a digest, and DecryptToFile writes to a temporary name and renames
// only after the final chunk authenticated and the digest matched. On any
// failure neither leaves a file behind and neither reports a byte count.
//
// # Keys
//
// A recipient is the suite's public key (a string, not secret) and an
// identity is the suite's secret line as bytes (for age/v1, an X25519
// public key and the AGE-SECRET-KEY-1... line). Resolving them is
// core/internal/keycustody's job (#990): the identity arrives inside a
// Store.WithKey callback, which zeroes it on return, and this package never
// keeps it. An empty or unparseable one
// is ErrKeyUnavailable, which the placement engine maps to its
// ErrClassUnavailable so that an unreadable key can never be recorded as a
// weaker class. No error here carries an identity string.
package copycrypt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// FormatID and FormatVersion are the age/v1 suite's id and version, kept
// under their EPIC N names because tests and docs name them. A copy records
// the id and version of the Suite that produced it (Staged.SuiteID and
// Staged.Version), never these constants (FR-83, ADR 0033 D7.3).
const (
	FormatID      = AgeX25519ID
	FormatVersion = 1
)

// TempPrefix is the prefix of every temporary file this package writes. It
// is artifactstore's own temporary prefix on purpose, so the placement
// engine's sweepStagingTemps (which clears that prefix from the staging
// directory after a crash) covers an interrupted encrypt without a second
// cleanup path.
const TempPrefix = ".artifactstore-"

var (
	// ErrKeyUnavailable means the recipient or identity is empty or is not
	// a valid age X25519 key. It is never a corruption and never a wrong
	// key: nothing was tried.
	ErrKeyUnavailable = errors.New("copycrypt: the key is unavailable")

	// ErrWrongKey means the stream is a well-formed age file that none of
	// the identities handed in can open.
	ErrWrongKey = errors.New("copycrypt: the identity does not open this object")

	// ErrCorrupt means the stored bytes failed authentication: a flipped
	// bit, a truncated tail (including a cut on a chunk boundary), a
	// reordered chunk, trailing data or a header that does not belong.
	ErrCorrupt = errors.New("copycrypt: the stored object failed authentication")

	// ErrDigestMismatch means the plaintext digest is not the one the
	// journal recorded for the artifact.
	ErrDigestMismatch = errors.New("copycrypt: the plaintext digest differs from the recorded artifact digest")
)

// Staged describes one finished encryption pass.
type Staged struct {
	// Path is the finished object in the staging directory.
	Path string
	// StoredHash and StoredSize describe the bytes at Path: header and
	// ciphertext. They describe this attempt's object and no other.
	StoredHash string
	StoredSize int64
	// PlaintextHash and PlaintextSize describe the artifact that went in.
	PlaintextHash string
	PlaintextSize int64
	// SuiteID and Version name the suite that produced the bytes at Path.
	// They are what placement_encryption records for the copy, taken from
	// the suite itself rather than from config (FR-83).
	SuiteID string
	Version int
}

// sticky remembers the first error an underlying reader or writer returned,
// so a failure of the caller's own I/O or a cancellation is never reported
// as the stored object being corrupt.
type sticky struct{ err error }

func (s *sticky) note(err error) error {
	if err != nil && !errors.Is(err, io.EOF) && s.err == nil {
		s.err = err
	}
	return err
}

type source struct {
	ctx context.Context
	r   io.Reader
	sticky
}

func (s *source) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, s.note(err)
	}
	n, err := s.r.Read(p)
	return n, s.note(err)
}

type sink struct {
	w io.Writer
	sticky
}

func (s *sink) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	return n, s.note(err)
}

func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || name != strings.TrimSpace(name) {
		return fmt.Errorf("copycrypt: %q is not a plain file name", name)
	}
	return nil
}

// finish makes tmp the object at dir/name: fsync the file, rename it into
// place, fsync the directory. The temporary file is removed on any failure.
func finish(f *os.File, dir, name string) (string, error) {
	tmp := f.Name()
	final := filepath.Join(dir, name)
	err := f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, final)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	d, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return "", err
	}
	return final, nil
}

// EncryptToStaging encrypts src to recipient under suite into dir/name and
// returns both digests and the suite's id and version. src must hash to wantPlaintextHash (hex sha256, any case), the
// digest the journal recorded for the artifact; otherwise nothing is left
// behind and the error is ErrDigestMismatch, which also catches a source
// that changed after it was verified.
//
// An existing object at dir/name is replaced only by a complete new one.
func EncryptToStaging(ctx context.Context, dir, name string, src io.Reader, suite Suite, recipient, wantPlaintextHash string) (Staged, error) {
	if err := checkName(name); err != nil {
		return Staged{}, err
	}
	if suite == nil {
		return Staged{}, fmt.Errorf("%w: no suite was named for the encryption", ErrUnknownSuite)
	}
	// Seal parses the recipient before anything is written, so a recipient
	// that is not a key of this suite is refused here, before the staging
	// directory or a temporary file exists.
	if _, err := suite.Seal(io.Discard, recipient); err != nil {
		return Staged{}, err
	}
	if err := ctx.Err(); err != nil {
		return Staged{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Staged{}, err
	}
	f, err := os.CreateTemp(dir, TempPrefix+"*")
	if err != nil {
		return Staged{}, err
	}
	fail := func(err error) (Staged, error) {
		f.Close()
		os.Remove(f.Name())
		return Staged{}, err
	}

	stored, plain := sha256.New(), sha256.New()
	out := &sink{w: io.MultiWriter(f, stored)}
	in := &source{ctx: ctx, r: src}

	aw, err := suite.Seal(out, recipient)
	if err != nil {
		return fail(err)
	}
	n, err := io.Copy(io.MultiWriter(aw, plain), in)
	if err != nil {
		return fail(err)
	}
	if err := aw.Close(); err != nil {
		return fail(err)
	}
	if got := hex.EncodeToString(plain.Sum(nil)); !strings.EqualFold(got, wantPlaintextHash) {
		return fail(fmt.Errorf("%w: the source hashes to %s, the journal recorded %q", ErrDigestMismatch, got, wantPlaintextHash))
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return fail(err)
	}
	storedHash := hex.EncodeToString(stored.Sum(nil))
	path, err := finish(f, dir, name)
	if err != nil {
		return Staged{}, err
	}
	return Staged{
		Path:          path,
		StoredHash:    storedHash,
		StoredSize:    size,
		PlaintextHash: hex.EncodeToString(plain.Sum(nil)),
		PlaintextSize: n,
		SuiteID:       suite.ID(),
		Version:       suite.Version(),
	}, nil
}

// decrypt authenticates every chunk of src, writes the plaintext to dst and
// returns its length, requiring its digest to equal want.
//
// progress, when non-nil, is told the size of each run of plaintext that
// authenticated, as an increment. It sees lengths only, never bytes, and the
// sum of what it is told never passes the plaintext length (#1303).
func decrypt(ctx context.Context, src io.Reader, suite Suite, identity []byte, dst io.Writer, want string, progress func(int64)) (int64, error) {
	if suite == nil {
		return 0, fmt.Errorf("%w: no suite was named for the decryption", ErrUnknownSuite)
	}
	// The identity is checked before a byte of src is read, so a key that
	// is not a key of this suite is ErrKeyUnavailable and never a
	// corruption. The suite's own error is dropped: it can quote the input.
	if _, err := suite.Recipient(identity); err != nil {
		return 0, fmt.Errorf("%w: the identity is not a valid %s secret key", ErrKeyUnavailable, suite.ID())
	}
	in := &source{ctx: ctx, r: src}
	classify := func(err error, out *sink) error {
		switch {
		case in.err != nil:
			return in.err
		case out != nil && out.err != nil:
			return out.err
		}
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return ErrWrongKey
		}
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}

	r, err := suite.Open(identity, in)
	if err != nil {
		return 0, classify(err, nil)
	}
	h := sha256.New()
	w := io.MultiWriter(h, dst)
	if progress != nil {
		w = io.MultiWriter(w, progressWriter(progress))
	}
	out := &sink{w: w}
	n, err := io.Copy(out, r)
	if err != nil {
		return 0, classify(err, out)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return 0, fmt.Errorf("%w: the plaintext hashes to %s, the journal recorded %q", ErrDigestMismatch, got, want)
	}
	return n, nil
}

// DecryptVerify streams src through age, sending the plaintext only into a
// digest, and succeeds only if every chunk authenticated, the final chunk
// was present and the digest equals wantPlaintextHash (hex sha256, any
// case). It returns the plaintext length on success and 0 on any error.
// This is what verification_class: content means for an encrypted copy.
//
// suite is the one the copy's record names (journal or sidecar), never the
// policy's: a copy is read under the suite it was written under.
func DecryptVerify(ctx context.Context, src io.Reader, suite Suite, identity []byte, wantPlaintextHash string) (int64, error) {
	return decrypt(ctx, src, suite, identity, io.Discard, wantPlaintextHash, nil)
}

// DecryptVerifyProgress is DecryptVerify that also tells progress, as the
// plaintext streams past, how many more bytes have authenticated (an
// increment, never zero). A nil progress is DecryptVerify (#1303).
func DecryptVerifyProgress(ctx context.Context, src io.Reader, suite Suite, identity []byte, wantPlaintextHash string, progress func(int64)) (int64, error) {
	return decrypt(ctx, src, suite, identity, io.Discard, wantPlaintextHash, progress)
}

// progressWriter counts what is written to it and discards it.
type progressWriter func(int64)

func (f progressWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		f(int64(len(p)))
	}
	return len(p), nil
}

// DecryptToFile is DecryptVerify that also keeps the plaintext: it writes to
// a temporary file in dir and renames it to dir/name only after the final
// chunk authenticated and the digest matched. On any error nothing is left
// in dir and the returned path is empty.
func DecryptToFile(ctx context.Context, src io.Reader, suite Suite, identity []byte, wantPlaintextHash, dir, name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, TempPrefix+"*") // 0600
	if err != nil {
		return "", err
	}
	if _, err := decrypt(ctx, src, suite, identity, f, wantPlaintextHash, nil); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	return finish(f, dir, name)
}
