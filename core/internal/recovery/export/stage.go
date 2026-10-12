package export

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrBelowReserve: staging this copy would take the staging disk under
	// its free-space reserve. A refusal at the moment of spend, before the
	// copy is opened, so nothing was read and nothing was billed.
	ErrBelowReserve = errors.New("recovery: staging this copy would take the staging disk below its free-space reserve")

	// ErrTargetTooLong: the requested target name is longer than any real
	// path needs to be.
	ErrTargetTooLong = errors.New("recovery: target name is too long")

	// ErrTargetOutsideRoot: the requested target name does not stay under
	// the recovery staging root.
	ErrTargetOutsideRoot = errors.New("recovery: target is outside the recovery staging root")
)

// maxTargetNameLen bounds a requested target name. Real paths under a
// staging root are short; this stops a name built to exhaust path length, or
// to make the directory it creates absurdly deep, before anything is made.
const maxTargetNameLen = 512

// The two shapes of file this package creates, and the only two it will
// ever remove. Everything else in the staging root is somebody else's.
const (
	partialPrefix = ".rcv-"
	partialSuffix = ".partial"
	outputPrefix  = "rcv-"
)

// File is the slice of *os.File the stager writes through. It is an
// interface so a test can make the disk fill up.
type File interface {
	io.Writer
	io.Closer
	Name() string
	Sync() error
}

// Staging is the directory recovered bytes pass through on their way to a
// target, and the rules for it.
//
// # Names
//
// A work-in-progress file is `.rcv-<random>.partial`, created exclusively,
// so two recoveries can never share one and nothing that looks finished
// exists until Publish renames it. A finished output is
// `rcv-<random>-<name>`: the operator's requested name survives at the end
// and the random part makes a collision impossible rather than unlikely.
//
// # Sweeping
//
// Only files in those two shapes are ever removed, and only by Sweep (past
// a TTL) or Reconcile (at startup, when no recovery of this process can
// own a partial). A cleanup that fails is reported in the SweepReport and
// is a different fact from a recovery that failed: one means "you did not
// get your bytes", the other means "the disk is filling".
type Staging struct {
	// Root is the recovery staging root.
	Root string

	// PartialTTL is how long an in-flight partial may sit before Sweep
	// takes it. OutputTTL is the same for a finished output.
	PartialTTL time.Duration
	OutputTTL  time.Duration

	// Reserve is the free space that must remain after a copy is staged.
	Reserve int64

	// Now is the clock; nil means time.Now.
	Now func() time.Time

	// FreeBytes reports free bytes on the filesystem holding dir. Nil
	// means the operating system's own answer, and a platform that cannot
	// give one refuses rather than guessing.
	FreeBytes func(dir string) (int64, error)

	// OpenFile creates path exclusively for writing. Nil means os.OpenFile.
	OpenFile func(path string) (File, error)

	// Remove deletes one path. Nil means os.Remove.
	Remove func(path string) error

	// mu guards inflight: the bytes that recoveries already admitted still
	// intend to write. Free space is read off the filesystem, which does
	// not know about a file nobody has written to yet, so two recoveries
	// checking at the same moment would each see all of it.
	mu       sync.Mutex
	inflight int64
}

// CleanupFailure is one path a sweep or a discard could not remove.
type CleanupFailure struct {
	Path string
	Err  error
}

// SweepReport says what a sweep did, including what it could not do.
type SweepReport struct {
	Removed []string
	Failed  []CleanupFailure
}

// CleanupError carries cleanup failures alongside, and separately from,
// the failure of the recovery that was being cleaned up after.
type CleanupError struct{ Failures []CleanupFailure }

func (e *CleanupError) Error() string {
	var b strings.Builder
	b.WriteString("recovery: staging cleanup failed, the staging disk may be filling:")
	for _, f := range e.Failures {
		fmt.Fprintf(&b, " %s (%v);", f.Path, f.Err)
	}
	return b.String()
}

func (s *Staging) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Staging) remove(p string) error {
	if s.Remove != nil {
		return s.Remove(p)
	}
	return os.Remove(p)
}

func (s *Staging) free() (int64, error) {
	if s.FreeBytes != nil {
		return s.FreeBytes(s.Root)
	}
	return freeBytes(s.Root)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not something to continue past: a
		// predictable name is exactly what exclusive creation guards
		// against.
		panic("recovery: no randomness available: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Begin creates a new exclusive partial for a copy of need bytes (0 when
// the size is not known) and refuses first if that would breach the
// reserve.
func (s *Staging) Begin(need int64) (File, error) {
	if s.Root == "" {
		return nil, errors.New("recovery: no staging root is configured")
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, fmt.Errorf("recovery: preparing the staging root: %w", err)
	}
	free, err := s.free()
	if err != nil {
		return nil, fmt.Errorf("recovery: cannot tell how much staging space is free, so not staging: %w", err)
	}
	// Check and claim under one lock, so the claim is part of the answer
	// the next caller sees.
	s.mu.Lock()
	if free-s.inflight-need < s.Reserve {
		s.mu.Unlock()
		return nil, ErrBelowReserve
	}
	s.inflight += need
	s.mu.Unlock()

	f, err := s.create()
	if err != nil {
		s.Release(need)
		return nil, err
	}
	return f, nil
}

// create makes the exclusive partial.
func (s *Staging) create() (File, error) {
	name := filepath.Join(s.Root, partialPrefix+randomHex(12)+partialSuffix)
	if s.OpenFile != nil {
		return s.OpenFile(name)
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Release gives back the claim a Begin of need bytes made. A recovery calls
// it when it is finished with the space, successful or not: by then the
// bytes it wrote are on the disk and the free-space reading says so.
func (s *Staging) Release(need int64) {
	s.mu.Lock()
	s.inflight -= need
	if s.inflight < 0 {
		s.inflight = 0
	}
	s.mu.Unlock()
}

func isPartial(name string) bool {
	return strings.HasPrefix(name, partialPrefix) && strings.HasSuffix(name, partialSuffix)
}

func isOutput(name string) bool { return strings.HasPrefix(name, outputPrefix) }

// owned walks the staging root without following symlinks and returns the
// files this package created.
func (s *Staging) owned() (partials, outputs []ownedFile, err error) {
	return walkOwned(s.Root)
}

// Entries lists every file under the root this package created.
func (s *Staging) Entries() ([]string, error) {
	ps, outs, err := s.owned()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ps {
		out = append(out, e.Path)
	}
	for _, e := range outs {
		out = append(out, e.Path)
	}
	return out, nil
}

// Sweep removes owned entries older than their TTL.
func (s *Staging) Sweep() (SweepReport, error) {
	ps, outs, err := s.owned()
	if err != nil {
		return SweepReport{}, err
	}
	now := s.now()
	var rep SweepReport
	for _, e := range ps {
		if s.PartialTTL > 0 && now.Sub(e.ModTime) > s.PartialTTL {
			s.removeInto(&rep, e.Path)
		}
	}
	for _, e := range outs {
		if s.OutputTTL > 0 && now.Sub(e.ModTime) > s.OutputTTL {
			s.removeInto(&rep, e.Path)
		}
	}
	return rep, nil
}

// Reconcile is the startup pass: nothing this process started can still be
// in flight, so every partial is an orphan of a process that died. Finished
// outputs are left for their TTL, because they are what the operator asked
// for.
func (s *Staging) Reconcile() (SweepReport, error) {
	ps, _, err := s.owned()
	if err != nil {
		return SweepReport{}, err
	}
	var rep SweepReport
	for _, e := range ps {
		s.removeInto(&rep, e.Path)
	}
	return rep, nil
}

func (s *Staging) removeInto(rep *SweepReport, p string) {
	if err := s.remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		rep.Failed = append(rep.Failed, CleanupFailure{Path: p, Err: err})
		return
	}
	rep.Removed = append(rep.Removed, p)
}

// Publish turns a finished partial into an output under the root. rel is
// the operator's requested name; it must stay under the root, may name
// subdirectories, and may not pass through a symlink. The output gets a
// unique name, so Publish never replaces anything.
func (s *Staging) Publish(from, rel string) (string, error) {
	if filepath.Dir(from) != filepath.Clean(s.Root) || !isPartial(filepath.Base(from)) {
		return "", errors.New("recovery: refusing to publish a file the staging area did not create")
	}
	dir, base, err := s.cleanTarget(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("recovery: preparing the target directory: %w", err)
	}
	// Re-check after creating: MkdirAll followed no symlink we did not
	// already refuse, and this catches one appearing in between.
	if err := s.noSymlinksBelowRoot(dir); err != nil {
		return "", err
	}
	final := filepath.Join(dir, outputPrefix+randomHex(8)+"-"+base)
	if err := os.Rename(from, final); err != nil {
		return "", fmt.Errorf("recovery: placing the recovered file: %w", err)
	}
	syncDir(dir)
	return final, nil
}

// ValidateName reports whether rel is an acceptable target name under the
// root, without creating anything. A mint calls it so a bad name is refused
// before a grant exists.
func (s *Staging) ValidateName(rel string) error {
	_, _, err := s.cleanTarget(rel)
	return err
}

// cleanTarget validates rel and returns the directory and sanitised base
// name the output goes under.
func (s *Staging) cleanTarget(rel string) (dir, base string, err error) {
	if len(rel) > maxTargetNameLen {
		return "", "", ErrTargetTooLong
	}
	if rel == "" || strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return "", "", ErrTargetOutsideRoot
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", ErrTargetOutsideRoot
	}
	subdir, name := path.Split(clean)
	if name == "" {
		return "", "", ErrTargetOutsideRoot
	}
	dir = filepath.Join(s.Root, filepath.FromSlash(subdir))
	if err := s.noSymlinksBelowRoot(dir); err != nil {
		return "", "", err
	}
	return dir, safeName(name), nil
}

// noSymlinksBelowRoot walks dir's components below the root and refuses a
// symlink among those that exist.
func (s *Staging) noSymlinksBelowRoot(dir string) error {
	root := filepath.Clean(s.Root)
	rel, err := filepath.Rel(root, filepath.Clean(dir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrTargetOutsideRoot
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return ErrTargetOutsideRoot
		}
	}
	return nil
}

// safeName keeps a requested file name usable and boring.
func safeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	return out
}

// publishCopy places a copy of from under the root as rel and leaves from
// where it is. It links when it can, which is instant and costs no space, and
// copies where the filesystem cannot link (a USB share, an SMB mount).
func (s *Staging) publishCopy(from, rel string) (string, error) {
	if filepath.Dir(from) != filepath.Clean(s.Root) || !isPartial(filepath.Base(from)) {
		return "", errors.New("recovery: refusing to publish a file the staging area did not create")
	}
	dir, base, err := s.cleanTarget(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("recovery: preparing the target directory: %w", err)
	}
	if err := s.noSymlinksBelowRoot(dir); err != nil {
		return "", err
	}
	final := filepath.Join(dir, outputPrefix+randomHex(8)+"-"+base)
	if err := os.Link(from, final); err == nil {
		syncDir(dir)
		return final, nil
	}
	if err := copyFileExclusive(from, final); err != nil {
		return "", fmt.Errorf("recovery: placing a copy of the recovered file: %w", err)
	}
	syncDir(dir)
	return final, nil
}

func copyFileExclusive(from, to string) (err error) {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(to)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
