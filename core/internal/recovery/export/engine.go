package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"syscall"
)

// Origin says where a copy came from, so a corrupt local copy and a corrupt
// remote copy are different facts in the record. One means this host's disk
// is suspect; the other means the destination returned something it should
// not have, and a provider may owe an explanation.
type Origin string

const (
	OriginLocal  Origin = "local"
	OriginRemote Origin = "remote"
)

var (
	// ErrIntegrity: the bytes read do not match what the journal recorded.
	// Nothing was delivered anywhere.
	ErrIntegrity = errors.New("recovery: the copy's bytes do not match what was recorded")

	// ErrNoSpace: the staging disk filled. The partial is gone; nothing
	// that could be mistaken for a complete copy remains.
	ErrNoSpace = errors.New("recovery: the staging disk is full")

	// ErrNoDigest: the journal records no SHA-256 for this artifact, so
	// there is nothing to verify the bytes against. Recovery refuses
	// rather than hand over bytes nobody can vouch for.
	ErrNoDigest = errors.New("recovery: the journal has no recorded SHA-256 for this artifact, so its bytes cannot be verified")
)

// IntegrityKind says how the copy failed verification.
type IntegrityKind string

const (
	// KindSize: the copy had more or fewer bytes than recorded.
	KindSize IntegrityKind = "size"

	// KindDigest: the right number of bytes, the wrong bytes. The shape
	// that a size check and an existence check both pass.
	KindDigest IntegrityKind = "digest"
)

// IntegrityError is ErrIntegrity with which copy, and how.
type IntegrityError struct {
	Origin Origin
	Medium string
	Kind   IntegrityKind
}

func (e *IntegrityError) Error() string {
	return fmt.Sprintf("recovery: the %s copy on %s failed verification (%s mismatch against the journal); nothing was delivered",
		e.Origin, e.Medium, e.Kind)
}

func (e *IntegrityError) Is(t error) bool { return t == ErrIntegrity }

// Source is one copy's bytes. Open is called at most once per Stage.
type Source interface {
	Open(ctx context.Context) (io.ReadCloser, error)
}

// Expect is what the journal recorded for the artifact, plus where the copy
// being read lives.
type Expect struct {
	Size    *int64
	HashAlg string
	Hash    string
	Origin  Origin
	Medium  string
}

// Staged is a fully written, fully verified file in the staging area. Only
// a Staged is ever handed to a target.
type Staged struct {
	Path   string
	Size   int64
	Digest string

	staging *Staging
}

// Open opens the staged bytes for reading, seekable so a download can
// answer range requests.
func (s *Staged) Open() (*os.File, error) { return os.Open(s.Path) }

// Discard removes the staged file.
func (s *Staged) Discard() error {
	if err := s.staging.remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &CleanupError{Failures: []CleanupFailure{{Path: s.Path, Err: err}}}
	}
	return nil
}

// DefaultBuffer is the copy buffer: the whole of the per-stream memory this
// engine needs, however large the artifact.
const DefaultBuffer = 256 << 10

// Engine stages and verifies. It holds no per-recovery state.
type Engine struct {
	Staging *Staging

	// Buffer is the copy buffer in bytes. Zero means DefaultBuffer.
	Buffer int
}

// Stage reads src through one fixed buffer into a new staged file, hashing
// as it goes, and returns it only when the byte count and the SHA-256 both
// equal what the journal recorded.
//
// # Why it stages first
//
// "Fails before a single byte reaches the client or the target" cannot be
// met by streaming straight through a hash: by the time the last byte
// shows the digest is wrong, the first gigabyte has left. So the bytes go
// to a file nobody can see, are verified there, and only then become
// something a target can have. The cost is the staging disk, which is
// budgeted (Staging.Reserve) and refused rather than hoped for, and it buys
// one read of the source, which matters when that read is billed.
//
// # What a failure leaves
//
// Nothing. Every error path removes the partial. If the removal itself
// fails the returned error carries a *CleanupError beside the recovery
// failure, because "you did not get your bytes" and "the disk is filling"
// are different things to know.
func (e *Engine) Stage(ctx context.Context, src Source, exp Expect) (staged *Staged, err error) {
	if !strings.EqualFold(exp.HashAlg, "sha256") || !validSHA256(exp.Hash) {
		return nil, ErrNoDigest
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var need int64
	if exp.Size != nil {
		need = *exp.Size
	}
	f, err := e.Staging.Begin(need)
	if err != nil {
		return nil, err
	}

	closed := false
	defer func() {
		if err == nil {
			return
		}
		if !closed {
			_ = f.Close()
		}
		if rmErr := e.Staging.remove(f.Name()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			err = errors.Join(err, &CleanupError{Failures: []CleanupFailure{{Path: f.Name(), Err: rmErr}}})
		}
	}()

	rc, err := src.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	buf := make([]byte, e.bufferSize())
	h := sha256.New()
	n, err := copyHashed(ctx, f, rc, h, buf, exp.Size)
	if err != nil {
		var sizeErr sizeExceeded
		if errors.As(err, &sizeErr) {
			return nil, &IntegrityError{Origin: exp.Origin, Medium: exp.Medium, Kind: KindSize}
		}
		return nil, err
	}
	if exp.Size != nil && n != *exp.Size {
		return nil, &IntegrityError{Origin: exp.Origin, Medium: exp.Medium, Kind: KindSize}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(sum, exp.Hash) {
		return nil, &IntegrityError{Origin: exp.Origin, Medium: exp.Medium, Kind: KindDigest}
	}
	if err := f.Sync(); err != nil {
		return nil, wrapWrite(err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return nil, wrapWrite(err)
	}
	return &Staged{Path: f.Name(), Size: n, Digest: sum, staging: e.Staging}, nil
}

func (e *Engine) bufferSize() int {
	switch {
	case e.Buffer <= 0:
		return DefaultBuffer
	case e.Buffer < 4<<10:
		return 4 << 10
	case e.Buffer > 4<<20:
		return 4 << 20
	}
	return e.Buffer
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

type sizeExceeded struct{}

func (sizeExceeded) Error() string { return "more bytes than recorded" }

// copyHashed is io.CopyBuffer with three differences: it checks the context
// between reads, it hashes what it writes, and it stops the moment the
// source exceeds the recorded size instead of staging an unbounded stream.
func copyHashed(ctx context.Context, dst io.Writer, src io.Reader, h hash.Hash, buf []byte, limit *int64) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		nr, rerr := src.Read(buf)
		if nr > 0 {
			if limit != nil && total+int64(nr) > *limit {
				return total, sizeExceeded{}
			}
			h.Write(buf[:nr])
			nw, werr := dst.Write(buf[:nr])
			total += int64(nw)
			if werr != nil {
				return total, wrapWrite(werr)
			}
			if nw != nr {
				return total, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			if cerr := ctx.Err(); cerr != nil {
				return total, cerr
			}
			return total, rerr
		}
	}
}

func wrapWrite(err error) error {
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return fmt.Errorf("%w: %v", ErrNoSpace, err)
	}
	return err
}
