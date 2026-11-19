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
)

// RangeSource is one copy's bytes, openable at an offset. A stream that is
// stopped and resumed, or a client that asks for the tail first, costs one
// open at the chunk that holds the first byte it wants, and no more.
type RangeSource interface {
	// OpenAt returns a reader positioned at off. The caller closes it.
	OpenAt(ctx context.Context, off int64) (io.ReadCloser, error)
}

// VerifiedReader serves a copy's bytes as an io.ReadSeeker, one verified
// chunk at a time.
//
// # The guarantees
//
//   - Memory is one chunk, whatever the size of the copy.
//   - No byte of a chunk is returned until that whole chunk has matched its
//     digest in the index. A chunk that does not match is never returned,
//     in part or whole, so the bytes already sent are all good and the
//     wasted transfer is bounded by the chunks before the bad one.
//   - A failure is sticky. After the first error every Read returns it, and
//     none of the chunks after it is returned, so a caller that ignores an
//     error and reads on cannot be handed good-looking bytes past a gap.
//   - Deterministic: the bytes at an offset are the bytes the index
//     recorded, whatever order or how many times they are asked for. A
//     resumed range is therefore byte-identical to the same range of the
//     first attempt, or it is an error.
//   - When the chunks are read in order from the start (a plain download),
//     the whole-content SHA-256 is computed on the way and compared with the
//     index's before the LAST chunk is released. A mismatch there stops the
//     stream one chunk short, so a short body is what the client sees and
//     it is never mistaken for a complete one.
//
// A VerifiedReader is for one goroutine at a time, like any io.ReadSeeker.
type VerifiedReader struct {
	ctx context.Context
	src RangeSource
	ix  *ChunkIndex

	origin Origin
	medium string

	pos    int64
	buf    []byte
	loaded int

	stream   io.ReadCloser
	streamAt int64

	err    error
	closed bool

	whole     hash.Hash
	wholeNext int
	wholeOK   bool
	wholeDone bool

	chunks      int
	sourceBytes int64
}

// NewVerifiedReader returns a reader over src, checked against ix. origin and
// medium say which copy it is, for the error a mismatch produces.
func NewVerifiedReader(ctx context.Context, src RangeSource, ix *ChunkIndex, origin Origin, medium string) *VerifiedReader {
	return &VerifiedReader{
		ctx: ctx, src: src, ix: ix, origin: origin, medium: medium,
		buf: make([]byte, ix.ChunkSize), loaded: -1,
		whole: sha256.New(),
		// An empty copy has no chunk to read, and BuildIndex already matched
		// its digest against the journal's.
		wholeDone: ix.Size == 0,
	}
}

// Size is the length of the verified copy.
func (r *VerifiedReader) Size() int64 { return r.ix.Size }

// Err is the first failure, or nil.
func (r *VerifiedReader) Err() error { return r.err }

// WholeVerified reports whether the whole-content SHA-256 was computed over
// every byte, in order, and matched.
func (r *VerifiedReader) WholeVerified() bool { return r.wholeDone }

// Stats says how many chunks have been verified and how many bytes were
// read from the source for it (which is more than was returned whenever a
// chunk was read and only part of it was wanted).
func (r *VerifiedReader) Stats() (chunks int, sourceBytes int64) { return r.chunks, r.sourceBytes }

func (r *VerifiedReader) fail(err error) error {
	if r.err == nil {
		r.err = err
	}
	return r.err
}

func (r *VerifiedReader) integrity(k IntegrityKind) error {
	return &IntegrityError{Origin: r.origin, Medium: r.medium, Kind: k}
}

// Prime loads and verifies the chunk that holds off, so a first failure
// happens before a response header is written rather than after it.
func (r *VerifiedReader) Prime(off int64) error {
	if r.err != nil {
		return r.err
	}
	if off < 0 || off >= r.ix.Size {
		return nil
	}
	i := int(off / r.ix.ChunkSize)
	if r.loaded == i {
		return nil
	}
	return r.load(i)
}

// Read implements io.Reader.
func (r *VerifiedReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.closed {
		return 0, os.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.pos >= r.ix.Size {
		return 0, io.EOF
	}
	i := int(r.pos / r.ix.ChunkSize)
	if r.loaded != i {
		if err := r.load(i); err != nil {
			return 0, err
		}
	}
	start := r.pos - int64(i)*r.ix.ChunkSize
	n := copy(p, r.buf[start:r.ix.ChunkLen(i)])
	r.pos += int64(n)
	return n, nil
}

// load reads chunk i from the source, checks it, and holds it.
func (r *VerifiedReader) load(i int) error {
	r.loaded = -1
	if err := r.ctx.Err(); err != nil {
		return r.fail(err)
	}
	start, n := int64(i)*r.ix.ChunkSize, r.ix.ChunkLen(i)
	if r.stream == nil || r.streamAt != start {
		r.closeStream()
		rc, err := r.src.OpenAt(r.ctx, start)
		if err != nil {
			return r.fail(err)
		}
		r.stream, r.streamAt = rc, start
	}
	got, err := io.ReadFull(r.stream, r.buf[:n])
	r.streamAt += int64(got)
	r.sourceBytes += int64(got)
	if err != nil {
		if cerr := r.ctx.Err(); cerr != nil {
			return r.fail(cerr)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return r.fail(r.integrity(KindSize))
		}
		return r.fail(err)
	}
	if sum := sha256.Sum256(r.buf[:n]); sum != r.ix.Chunks[i] {
		return r.fail(r.integrity(KindDigest))
	}
	last := i == r.ix.Count()-1
	if last {
		// More bytes after the recorded end are as wrong as fewer.
		var one [1]byte
		if m, _ := r.stream.Read(one[:]); m > 0 {
			return r.fail(r.integrity(KindSize))
		}
	}
	if i == 0 {
		r.whole.Reset()
		r.wholeNext, r.wholeOK, r.wholeDone = 0, true, false
	}
	switch {
	case r.wholeOK && i == r.wholeNext:
		r.whole.Write(r.buf[:n])
		r.wholeNext++
		if last {
			if !strings.EqualFold(hex.EncodeToString(r.whole.Sum(nil)), r.ix.SHA256) {
				return r.fail(r.integrity(KindDigest))
			}
			r.wholeDone = true
		}
	case i != r.wholeNext:
		r.wholeOK = false
	}
	r.chunks++
	r.loaded = i
	return nil
}

// Seek implements io.Seeker. It reads nothing.
func (r *VerifiedReader) Seek(off int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = off
	case io.SeekCurrent:
		np = r.pos + off
	case io.SeekEnd:
		np = r.ix.Size + off
	default:
		return 0, fmt.Errorf("recovery: bad seek whence %d", whence)
	}
	if np < 0 {
		return 0, errors.New("recovery: seek before the start of the copy")
	}
	r.pos = np
	return np, nil
}

func (r *VerifiedReader) closeStream() {
	if r.stream != nil {
		_ = r.stream.Close()
		r.stream = nil
	}
}

// Close releases the source and the chunk buffer. It is safe to call twice.
func (r *VerifiedReader) Close() error {
	r.closed = true
	r.closeStream()
	r.buf = nil
	return nil
}
