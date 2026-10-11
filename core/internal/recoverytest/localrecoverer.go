package recoverytest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	// Root is the staging root. Empty means the system temporary directory.
	Root string
}

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
