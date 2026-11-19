package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The chunk index is what lets a download be streamed instead of staged.
//
// # Why it exists
//
// A download used to be staged: the whole copy went to a file, was hashed
// there, and only then was any of it sent. That guarantees nothing wrong
// leaves the host, and it costs the staging disk (one whole copy per
// download), a wait for the whole read before the first byte, and a file
// that has to be cleaned up when the client walks away.
//
// Streaming straight through a hash cannot give the same guarantee: by the
// time the last byte shows the digest is wrong, the first gigabyte has gone.
// So the proof is moved in front of the stream rather than behind it. One
// verifying read of the copy, which stores nothing but a digest per chunk,
// produces a ChunkIndex; the stream that follows is checked chunk by chunk
// against that index, and stops at the first chunk that does not match.
//
// # What it does and does not prove
//
// The index is built only from a read whose whole-content SHA-256 and byte
// count equalled the journal's (BuildIndex refuses otherwise), so it is a
// statement about bytes that were, at that moment, the bytes the journal
// recorded. A stream checked against it proves each chunk it sends equals
// the chunk that was good then. It does not prove the copy is good now
// beyond the chunks actually sent, and it cannot see a chunk the client
// never asks for. Both are the same limit a staged copy has (it proves the
// bytes at staging time), only the window is a chunk wide instead of a
// whole file.

const (
	// DefaultChunkSize is the verification and memory unit of a streamed
	// download: the most a stream ever holds, however large the copy.
	DefaultChunkSize int64 = 4 << 20

	minChunkSize int64 = 4 << 10
	maxChunkSize int64 = 64 << 20

	indexMagic = "RCVIDX1\n"
)

// ChunkIndex is one copy's per-chunk SHA-256 list, plus the size and
// whole-content digest it was built to prove.
type ChunkIndex struct {
	ChunkSize int64
	Size      int64

	// SHA256 is the hex whole-content digest the build matched against the
	// journal.
	SHA256 string

	Chunks [][sha256.Size]byte
}

// ErrBadIndex: the stored index failed its own checks. It is a cache, so the
// caller rebuilds; it is never a reason to refuse a recovery.
var ErrBadIndex = errors.New("recovery: the stored chunk index is not usable")

// ClampChunkSize returns n if it is a sane chunk size and the default if it
// is zero, and pulls anything else into range.
func ClampChunkSize(n int64) int64 {
	switch {
	case n <= 0:
		return DefaultChunkSize
	case n < minChunkSize:
		return minChunkSize
	case n > maxChunkSize:
		return maxChunkSize
	}
	return n
}

// Count is the number of chunks.
func (ix *ChunkIndex) Count() int { return len(ix.Chunks) }

// ChunkLen is the byte length of chunk i: ChunkSize except for the last.
func (ix *ChunkIndex) ChunkLen(i int) int64 {
	start := int64(i) * ix.ChunkSize
	rest := ix.Size - start
	if rest > ix.ChunkSize {
		return ix.ChunkSize
	}
	return rest
}

// Matches reports whether the index is about the bytes the journal records.
func (ix *ChunkIndex) Matches(exp Expect) bool {
	if !strings.EqualFold(ix.SHA256, exp.Hash) {
		return false
	}
	return exp.Size == nil || *exp.Size == ix.Size
}

// ETag is the strong validator for the copy's bytes: derived from the
// artifact id and the digest the journal recorded and BuildIndex matched.
//
// It names the representation and nothing else. It does not depend on which
// copy is read, on the chunk size, on the grant or on the process, so a
// download interrupted by a crash and resumed against a new grant, or a
// restarted server, or the artifact's other copy, presents the same ETag,
// and If-Range works across all of them. It changes if and only if the
// artifact's recorded digest does, which is exactly when a resumed range
// would otherwise splice two different files together.
func ETag(artifactID, sha256Hex string) string {
	h := sha256.New()
	h.Write([]byte("retnd recovery etag v1\x00"))
	h.Write([]byte(artifactID))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(sha256Hex)))
	return `"rcv1-` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// BuildIndex reads r once, to its end, holding one chunk in memory, and
// returns the index only when the byte count and the whole-content SHA-256
// both equal what the journal recorded. Nothing is written anywhere.
func BuildIndex(ctx context.Context, r io.Reader, exp Expect, chunkSize int64) (*ChunkIndex, error) {
	if err := CheckExpect(exp); err != nil {
		return nil, err
	}
	chunkSize = ClampChunkSize(chunkSize)
	ix := &ChunkIndex{ChunkSize: chunkSize}
	whole := sha256.New()
	buf := make([]byte, chunkSize)
	var total int64
	bad := func(k IntegrityKind) error {
		return &IntegrityError{Origin: exp.Origin, Medium: exp.Medium, Kind: k}
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if exp.Size != nil && total+int64(n) > *exp.Size {
				return nil, bad(KindSize)
			}
			total += int64(n)
			whole.Write(buf[:n])
			ix.Chunks = append(ix.Chunks, sha256.Sum256(buf[:n]))
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return nil, err
		}
	}
	if exp.Size != nil && total != *exp.Size {
		return nil, bad(KindSize)
	}
	sum := hex.EncodeToString(whole.Sum(nil))
	if !strings.EqualFold(sum, exp.Hash) {
		return nil, bad(KindDigest)
	}
	ix.Size, ix.SHA256 = total, sum
	return ix, nil
}

// MarshalBinary writes the index with a trailing SHA-256 of everything
// before it, so a torn or damaged file is recognised as such.
func (ix *ChunkIndex) MarshalBinary() ([]byte, error) {
	raw, err := hex.DecodeString(ix.SHA256)
	if err != nil || len(raw) != sha256.Size {
		return nil, fmt.Errorf("%w: digest", ErrBadIndex)
	}
	var b bytes.Buffer
	b.WriteString(indexMagic)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(ix.ChunkSize))
	b.Write(n[:])
	binary.BigEndian.PutUint64(n[:], uint64(ix.Size))
	b.Write(n[:])
	b.Write(raw)
	for _, c := range ix.Chunks {
		b.Write(c[:])
	}
	sum := sha256.Sum256(b.Bytes())
	b.Write(sum[:])
	return b.Bytes(), nil
}

// UnmarshalChunkIndex is the inverse, and refuses anything inconsistent:
// the trailer, the magic, a chunk size out of range, a count that does not
// follow from the size.
func UnmarshalChunkIndex(p []byte) (*ChunkIndex, error) {
	head := len(indexMagic) + 8 + 8 + sha256.Size
	if len(p) < head+sha256.Size || string(p[:len(indexMagic)]) != indexMagic {
		return nil, fmt.Errorf("%w: header", ErrBadIndex)
	}
	body, trailer := p[:len(p)-sha256.Size], p[len(p)-sha256.Size:]
	if sum := sha256.Sum256(body); !bytes.Equal(sum[:], trailer) {
		return nil, fmt.Errorf("%w: checksum", ErrBadIndex)
	}
	off := len(indexMagic)
	cs := int64(binary.BigEndian.Uint64(body[off:]))
	size := int64(binary.BigEndian.Uint64(body[off+8:]))
	if cs < minChunkSize || cs > maxChunkSize || size < 0 {
		return nil, fmt.Errorf("%w: sizes", ErrBadIndex)
	}
	ix := &ChunkIndex{ChunkSize: cs, Size: size, SHA256: hex.EncodeToString(body[off+16 : off+16+sha256.Size])}
	rest := body[head:]
	want := (size + cs - 1) / cs
	if int64(len(rest)) != want*sha256.Size {
		return nil, fmt.Errorf("%w: chunk count", ErrBadIndex)
	}
	for i := int64(0); i < want; i++ {
		var c [sha256.Size]byte
		copy(c[:], rest[i*sha256.Size:])
		ix.Chunks = append(ix.Chunks, c)
	}
	return ix, nil
}
