package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/recovery/export"
	"github.com/retnd/retnd/core/internal/transport"
)

// A browser download is streamed, not staged (EPIC Q, owner decisions 3 and
// 4).
//
// # What changed and why
//
// The staged download copied the whole artifact to a file, hashed it there
// and only then sent it. That kept anything wrong from leaving the host and
// cost one staging disk per download, a wait for the whole read before the
// first byte, and a file to clean up when the client walked away. A client
// that stopped, or a server that crashed, could not resume: the next
// attempt staged it all again.
//
// Now the proof comes in front of the stream. Redeeming a download:
//
//  1. re-plans from the journal as it is now (a copy deleted or repointed
//     since the mint is refused);
//  2. runs the pre-flight, which asks the copy questions that cost no
//     transfer (below);
//  3. gets the artifact's chunk index, from disk if a verifying read already
//     made one, otherwise by making it: one read of the copy, hashed chunk by
//     chunk and in whole against the journal, storing a digest per chunk and
//     no content;
//  4. only then returns a reader, and the HTTP layer writes its first header.
//
// The reader (export.VerifiedReader) holds one chunk, checks every chunk
// against the index before releasing any of it, and fails for good at the
// first bad one. A range request opens the copy at its chunk boundary, so a
// resumed download reads what it still needs.
//
// # What the pre-flight proves and does not prove
//
// It proves, before a byte is sent: the artifact has a recorded digest; the
// journal's copy is the one asked for and its medium can be read now (not
// archive-class and unrestored, and, for a remote copy, the provider's own
// checksum does not contradict ours); the file or object exists, is a
// regular file or a stat-able object, and has the recorded size; and a
// verifying read of the whole copy matched the recorded SHA-256 and every
// chunk now has a digest to be held to.
//
// It does not prove the copy will still be intact when the last chunk is
// read: bytes can rot between the preparation read and the stream. That is
// what the per-chunk check is for, and it bounds the loss to the chunks
// already sent (they were good) plus the one that failed (never sent). It
// does not prove the client will stay connected, that the client's disk
// has room, or that a remote provider will keep answering for the duration.
//
// # What the first download of an artifact costs
//
// The preparation read happens once per artifact and is then reused by every
// later download and every resume. For a copy this host holds it is a disk
// read. For a chargeable remote copy it is a second billed read on the first
// download (one to prepare, one to send), which is the price of refusing a
// rotted copy before the client's bandwidth is spent and not after. The mint
// already carries the cost acknowledgement.

// recoveryDownloadState is what a started download keeps on its grant: the
// index its continuations use without a second look at the disk.
type recoveryDownloadState struct {
	index *export.ChunkIndex
}

// RecoveryValidator is what a download offers a client that wants to resume
// it or check it: a strong validator and the digest the journal records.
// The HTTP layer asks a stream for it with a type assertion so a stream
// that cannot say simply sends neither header.
type RecoveryValidator interface {
	// ETag is the strong validator for the artifact's bytes
	// (export.ETag), the same for every grant, copy and process.
	ETag() string

	// SHA256 is the hex whole-content digest the journal recorded and the
	// stream is checked against.
	SHA256() string
}

// RecoveryFailer reports the first reason a stream stopped being trusted,
// so the HTTP layer can tell a body that ended from a body that was cut.
type RecoveryFailer interface {
	Failure() error
}

// RecoveryPrimer verifies the first chunk a request will send before any
// header is written, so a copy that fails on its first chunk is a refusal
// and not a 200 with a broken body.
type RecoveryPrimer interface {
	Prime(start int64) error
}

var (
	_ RecoveryValidator = (*RecoveryDownload)(nil)
	_ RecoveryFailer    = (*RecoveryDownload)(nil)
	_ RecoveryPrimer    = (*RecoveryDownload)(nil)
)

// rangeSource is the pre-flight and the ranged read of one chosen copy.
type rangeSource struct {
	recoverySource
}

// stat answers, with no transfer, whether the copy is there and how big it
// is.
func (s rangeSource) stat(ctx context.Context) (int64, error) {
	if s.store == nil {
		fi, err := os.Lstat(s.local)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return 0, recoveryErr(RecoveryNotReadable, "the local copy is no longer on this host's disk")
		case err != nil:
			return 0, recoveryErr(RecoveryNotReadable, "the local copy cannot be read: %v", unwrapPathError(err))
		case !fi.Mode().IsRegular():
			return 0, recoveryErr(RecoveryNotReadable, "the local copy is not a regular file, so it is not read")
		}
		return fi.Size(), nil
	}
	info, err := s.store.StatObject(ctx, s.medium, s.key)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return 0, cerr
		}
		if cat, ok := transport.CategoryOf(err); ok && cat == transport.NotFound {
			return 0, recoveryErr(RecoveryNotReadable, "the copy on %s is no longer there", s.mediumID())
		}
		return 0, recoveryErr(RecoveryNotReadable, "the destination for the copy on %s did not answer, so nothing was started", s.mediumID())
	}
	return info.Size, nil
}

func (s rangeSource) mediumID() string { return s.medium.ID }

// unwrapPathError drops the path from an *os.PathError so a response never
// carries one.
func unwrapPathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// OpenAt implements export.RangeSource.
func (s rangeSource) OpenAt(ctx context.Context, off int64) (io.ReadCloser, error) {
	if s.store == nil {
		rc, err := s.Open(ctx)
		if err != nil {
			return nil, err
		}
		if off > 0 {
			if f, ok := rc.(*os.File); ok {
				if _, err := f.Seek(off, io.SeekStart); err != nil {
					_ = rc.Close()
					return nil, err
				}
			}
		}
		return rc, nil
	}
	if ro, ok := s.store.(transport.RangeOpener); ok {
		return ro.OpenObjectRange(ctx, s.medium, s.key, off)
	}
	// A store that cannot start at an offset is read from its start and the
	// prefix thrown away. Correct, and as wasteful as off is large.
	rc, err := s.store.OpenObject(ctx, s.medium, s.key)
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if _, err := io.CopyN(io.Discard, rc, off); err != nil {
			_ = rc.Close()
			return nil, err
		}
	}
	return rc, nil
}

// indexBuild is one verifying read in progress, shared by everyone who asks
// for the same artifact meanwhile.
type indexBuild struct {
	done chan struct{}
	ix   *export.ChunkIndex
	err  error
}

// downloadIndexes is the host's chunk-index cache and the single-flight in
// front of making one.
type downloadIndexes struct {
	store *export.IndexStore

	mu       sync.Mutex
	building map[string]*indexBuild
}

func (d *downloadIndexes) build(ctx context.Context, key string, fn func() (*export.ChunkIndex, error)) (*export.ChunkIndex, error) {
	for attempt := 0; ; attempt++ {
		d.mu.Lock()
		if d.building == nil {
			d.building = map[string]*indexBuild{}
		}
		if c, ok := d.building[key]; ok {
			d.mu.Unlock()
			select {
			case <-c.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// The reader we waited on may have been cancelled by its own
			// client, which says nothing about this request.
			if c.err != nil && errors.Is(c.err, context.Canceled) && ctx.Err() == nil && attempt < 2 {
				continue
			}
			return c.ix, c.err
		}
		c := &indexBuild{done: make(chan struct{})}
		d.building[key] = c
		d.mu.Unlock()

		c.ix, c.err = fn()

		d.mu.Lock()
		delete(d.building, key)
		d.mu.Unlock()
		close(c.done)
		return c.ix, c.err
	}
}

// preflightDownload is everything that can be learned about a download
// without sending it. It returns the plan, the source and the chunk index,
// or a coded refusal; it has written nothing to a client.
func (b *BackupService) preflightDownload(ctx context.Context, h *recoveryHost, g export.Grant, attached *export.ChunkIndex) (recoveryPlan, rangeSource, *export.ChunkIndex, error) {
	// The grant already carries the cost acknowledgement: a chargeable copy
	// could not have been minted without it.
	plan, err := b.planRecovery(ctx, g.Artifact, g.Copy, true)
	if err != nil {
		return recoveryPlan{}, rangeSource{}, nil, err
	}
	if plan.resolved.NeedsThaw {
		return recoveryPlan{}, rangeSource{}, nil, recoveryErr(RecoveryNotReadable, "the copy on %s needs a provider restore before it can be read", g.Copy)
	}
	if err := export.CheckExpect(plan.expect); err != nil {
		return recoveryPlan{}, rangeSource{}, nil, mapRecoveryError(err)
	}
	st := b.state.Load()
	raw, err := b.sourceFor(st, plan)
	if err != nil {
		return recoveryPlan{}, rangeSource{}, nil, err
	}
	src := rangeSource{recoverySource: raw.(recoverySource)}

	// The copy is there, a regular file or a stat-able object, and the size
	// the journal recorded. Metadata only.
	size, err := src.stat(ctx)
	if err != nil {
		return recoveryPlan{}, rangeSource{}, nil, err
	}
	if plan.expect.Size != nil && size != *plan.expect.Size {
		return recoveryPlan{}, rangeSource{}, nil, &export.IntegrityError{Origin: plan.expect.Origin, Medium: plan.expect.Medium, Kind: export.KindSize}
	}

	key := export.IndexKey(g.Artifact, plan.expect.Hash, recoveryChunkSize)
	if attached != nil && attached.Matches(plan.expect) && attached.Size == size {
		return plan, src, attached, nil
	}
	if ix, ok := h.indexes.store.Load(key, plan.expect); ok && ix.Size == size {
		return plan, src, ix, nil
	}
	ix, err := h.indexes.build(ctx, key, func() (*export.ChunkIndex, error) {
		rc, oerr := src.OpenAt(ctx, 0)
		if oerr != nil {
			return nil, oerr
		}
		defer func() { _ = rc.Close() }()
		built, berr := export.BuildIndex(ctx, rc, plan.expect, recoveryChunkSize)
		if berr != nil {
			return nil, berr
		}
		// A cache that cannot be written costs the next download another
		// read, not this one.
		if serr := h.indexes.store.Save(key, built); serr != nil {
			b.logger.Error(ctx, "recovery_index_save", serr)
		}
		return built, nil
	})
	if err != nil {
		return recoveryPlan{}, rangeSource{}, nil, err
	}
	return plan, src, ix, nil
}

// redeemDownload is RedeemRecovery for a download target: pre-flight, then a
// verifying reader. Every request on the grant, the first or a range after
// it, runs the same checks, so a copy that disappears between two ranges is
// a refusal on the second and not a hang.
func (b *BackupService) redeemDownload(ctx context.Context, h *recoveryHost, sess RecoverySession, token string, red export.Redemption, ev recoveryEvent) (RecoveryStream, error) {
	g := red.Grant
	var attached *export.ChunkIndex
	if st, ok := red.Attached.(*recoveryDownloadState); ok && st != nil {
		attached = st.index
	}
	plan, src, ix, err := b.preflightDownload(ctx, h, g, attached)
	if err != nil {
		// A refusal before the first byte spends the grant: whatever it
		// found is not going to be different on a second try with the same
		// link, and a new one is one click.
		h.grants.Close(token)
		b.auditRecovery(ctx, &ev, err)
		return nil, mapRecoveryError(err)
	}
	if attached == nil {
		h.grants.Attach(token, &recoveryDownloadState{index: ix})
	}
	reader := export.NewVerifiedReader(ctx, src, ix, plan.expect.Origin, plan.expect.Medium)
	return &RecoveryDownload{
		name: filepath.Base(g.Artifact), size: ix.Size, content: reader, index: ix,
		etag: export.ETag(g.Artifact, ix.SHA256),
		b:    b, token: token, actor: sess.Actor, ev: ev,
	}, nil
}

// ETag implements RecoveryValidator.
func (d *RecoveryDownload) ETag() string { return d.etag }

// SHA256 implements RecoveryValidator.
func (d *RecoveryDownload) SHA256() string { return d.index.SHA256 }

// Failure implements RecoveryFailer.
func (d *RecoveryDownload) Failure() error { return d.content.Err() }

// Prime implements RecoveryPrimer.
func (d *RecoveryDownload) Prime(start int64) error { return d.content.Prime(start) }

// recordVerified writes the end-of-download row: the server's own statement
// of whether the artifact it just finished sending verified, and against
// what. It is written once, when the ranges served add up to the whole
// artifact.
func (d *RecoveryDownload) recordVerified() {
	how := fmt.Sprintf("every chunk served matched its digest in the index built from the journal's SHA-256 (%d chunks of up to %d bytes)", d.index.Count(), d.index.ChunkSize)
	if d.content.WholeVerified() {
		how += "; the whole-content SHA-256 was also computed while streaming and matched before the last chunk was released"
	}
	ev := d.ev
	ev.op = "verified"
	ev.bytes = d.size
	ev.rng = ""
	ev.note = "sha256 " + d.index.SHA256 + ", " + strconv.FormatInt(d.size, 10) + " bytes: " + how
	d.b.auditRecovery(context.Background(), &ev, nil)
	d.b.recoveryHost().noteVerified(d.ev.artifact, recoveryVerification{
		At: time.Now().UTC(), Copy: d.ev.copy, Bytes: d.size, SHA256: d.index.SHA256, ETag: d.etag,
		WholeStream: d.content.WholeVerified(), Chunks: d.index.Count(),
	})
}

// recoveryVerification is what the server remembers about the last download
// of an artifact that ran to the end and verified.
type recoveryVerification struct {
	At          time.Time
	Copy        string
	Bytes       int64
	SHA256      string
	ETag        string
	WholeStream bool
	Chunks      int
}

const maxRememberedVerifications = 256

type verifiedLog struct {
	mu   sync.Mutex
	last map[string]recoveryVerification
}

func (h *recoveryHost) noteVerified(artifact string, v recoveryVerification) {
	h.verified.mu.Lock()
	defer h.verified.mu.Unlock()
	if h.verified.last == nil {
		h.verified.last = map[string]recoveryVerification{}
	}
	if _, ok := h.verified.last[artifact]; !ok && len(h.verified.last) >= maxRememberedVerifications {
		var oldest string
		var at time.Time
		for k, x := range h.verified.last {
			if oldest == "" || x.At.Before(at) {
				oldest, at = k, x.At
			}
		}
		delete(h.verified.last, oldest)
	}
	h.verified.last[artifact] = v
}
