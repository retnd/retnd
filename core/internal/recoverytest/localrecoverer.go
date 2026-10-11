package recoverytest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/capacity"
)

// LocalRecoverer stages a copy that lives on local disk by copying its bytes
// into a fresh, private directory under Root. It is the Recoverer this
// package can honestly provide on its own: reading a local file costs
// nothing, needs no credential and starts no provider job.
//
// A copy on a storage medium needs the recovery domain's resolver (Q1.1),
// which owns how bytes come back from a bucket, what that costs and how a
// restore of an archived object is asked for. LocalRecoverer refuses those
// with ErrUnsupportedCopy instead of approximating them, so wiring in the
// real resolver later is replacing this value and not unpicking a half
// implementation.
type LocalRecoverer struct {
	// Root is the staging root. Empty means the system temporary directory,
	// which on many hosts is memory; the application passes a directory on
	// the backup set's own disk instead.
	Root string

	// Reserve is the free space that must remain after the copy. Zero means
	// DefaultStagingReserve.
	Reserve uint64

	// Available reports the bytes an unprivileged writer can still use on
	// the filesystem holding path. Nil means the real statfs. It is a seam
	// because a test cannot have a full disk.
	Available func(path string) (uint64, error)
}

// DefaultStagingReserve is the free space left over after a staged copy. A
// recovery test is background work and must never be the thing that fills
// the volume backups are written to.
const DefaultStagingReserve uint64 = 1 << 30

// ErrUnsupportedCopy is returned for a copy this recoverer does not stage.
var ErrUnsupportedCopy = errors.New("recoverytest: this recoverer stages local copies only")

const stagingPrefix = "retnd-recovery-test-"

func (r LocalRecoverer) root() string {
	if r.Root != "" {
		return r.Root
	}
	return os.TempDir()
}

// Recover copies the local copy into a new private directory and returns the
// file. The directory name is unique and unguessable, the file is written
// with mode 0600, and a failure part-way removes what was written, so a
// short file never survives looking like a recovered copy.
func (r LocalRecoverer) Recover(ctx context.Context, req RecoverRequest) (Staged, error) {
	if !req.Copy.IsLocal() {
		return Staged{}, fmt.Errorf("%w: the copy is on %q", ErrUnsupportedCopy, req.Copy.Medium)
	}
	src, err := os.Open(req.Copy.Location)
	if err != nil {
		return Staged{}, fmt.Errorf("the local copy could not be opened: %w", pathless(err))
	}
	defer src.Close()

	if err := os.MkdirAll(r.root(), 0o700); err != nil {
		return Staged{}, fmt.Errorf("the staging root could not be created: %w", pathless(err))
	}
	if err := r.roomFor(src, req.Copy.Size); err != nil {
		return Staged{}, err
	}
	dir, err := os.MkdirTemp(r.root(), stagingPrefix+"*")
	if err != nil {
		return Staged{}, fmt.Errorf("a staging directory could not be created: %w", pathless(err))
	}
	dst := filepath.Join(dir, "artifact")
	if err := copyFile(ctx, dst, src); err != nil {
		_ = os.RemoveAll(dir)
		return Staged{}, err
	}
	return Staged{Path: dst}, nil
}

// roomFor refuses a copy that would not fit with the reserve to spare. The
// size that has to fit is the larger of the recorded one and the file's own,
// so a stale record cannot understate it.
func (r LocalRecoverer) roomFor(src *os.File, recorded *int64) error {
	need := uint64(0)
	if recorded != nil && *recorded > 0 {
		need = uint64(*recorded)
	}
	if fi, err := src.Stat(); err == nil && fi.Size() > 0 && uint64(fi.Size()) > need {
		need = uint64(fi.Size())
	}
	reserve := r.Reserve
	if reserve == 0 {
		reserve = DefaultStagingReserve
	}
	avail := r.Available
	if avail == nil {
		avail = func(path string) (uint64, error) {
			st, err := capacity.StatPath(path)
			return st.AvailableBytes, err
		}
	}
	have, err := avail(r.root())
	if err != nil {
		return fmt.Errorf("the staging filesystem could not be measured, so nothing can say whether a copy fits: %w", pathless(err))
	}
	if have < need+reserve {
		return fmt.Errorf("a staged copy needs %d bytes and a %d byte reserve, and the staging filesystem has %d available", need, reserve, have)
	}
	return nil
}

func copyFile(ctx context.Context, dst string, src io.Reader) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("the staged copy could not be created: %w", pathless(err))
	}
	if _, err := io.Copy(f, &ctxReader{ctx: ctx, r: src}); err != nil {
		_ = f.Close()
		return fmt.Errorf("the staged copy could not be written: %w", pathless(err))
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("the staged copy could not be flushed: %w", pathless(err))
	}
	return f.Close()
}

// Remove deletes the staged copy and the private directory it sits in. It
// refuses anything that is not a directory this recoverer created under its
// root, so a Staged value that was tampered with, or a bug upstream, cannot
// turn cleanup into a delete of something else.
func (r LocalRecoverer) Remove(_ context.Context, s Staged) error {
	dir := filepath.Dir(s.Path)
	root, err := filepath.Abs(r.root())
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if filepath.Dir(abs) != root || !strings.HasPrefix(filepath.Base(abs), stagingPrefix) || filepath.Base(s.Path) != "artifact" {
		return fmt.Errorf("refusing to remove %q: it is not a staging directory this recoverer created", filepath.Base(abs))
	}
	return os.RemoveAll(abs)
}

// pathless strips the file path out of an *os.PathError, which would
// otherwise carry the artifact's real location into a stored reason.
func pathless(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// Sweep removes the staging directories this recoverer created under Root
// that are older than olderThan, and returns how many it removed.
//
// A crash between staging a copy and removing it leaves a directory that
// nothing refers to, and a recovery test stages a whole artifact, so left
// alone a few crashes fill the volume backups are written to. It touches
// only a directory whose name carries this recoverer's prefix, so a Root
// that is shared with anything else loses nothing else, and only a directory
// (not a file that happens to carry the prefix). A directory that cannot be
// removed is reported and the sweep carries on, because one stuck entry must
// not stop the others being cleared.
func (r LocalRecoverer) Sweep(ctx context.Context, olderThan time.Duration) (int, error) {
	entries, err := os.ReadDir(r.root())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("the staging root could not be read: %w", pathless(err))
	}
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	var firstErr error
	for _, e := range entries {
		if ctx.Err() != nil {
			return removed, ctx.Err()
		}
		if !e.IsDir() || !strings.HasPrefix(e.Name(), stagingPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(r.root(), e.Name())); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("an old staging directory could not be removed: %w", pathless(err))
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}
