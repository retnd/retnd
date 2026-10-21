package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/workflow"
)

// The custom validators' script store (#1218): the one place in core/service
// that knows the stage directory exists. Everything the write path, the
// reads, RemoveCustomValidator and RemoveBackupSet do to a script goes through
// a validatorStore, so the rules about that directory (written atomically,
// 0750 and 0640, never deleted, nothing in it the configuration does not name)
// are kept here and tested here without a service.
//
// # How a script is written
//
// To a dot-named temporary file in the stage directory itself, then renamed
// over its name, so a reader never sees a half-written script. It used to go
// in the workflow root, which a container mounts read-only (#1246), so every
// upload there was refused even with the directory itself writable; and a
// rename only works within one mount, which in a container the stage
// directory is. The temporary name is harmless beside the scripts: a
// recovery test looks up the one script it was told to run (DiscoverOne) and
// never lists the directory, the write that made the file holds configMu for
// its whole life, and one left by a crash is not a name the configuration
// declares, so the next write moves it aside (Reconcile).
// The directory is 0750 and the file 0640, both owned by this process, which
// is what discovery's custody rules accept.
//
// # Where it can be written
//
// The stage directory has to be writable by this process and its root does
// not, which is the packaged layout: a read-only /workflows with
// recovery-test-validators and its .superseded sibling mounted writable over
// it. Unwritable says, before anyone uploads, when the directory is not, and
// unavailableWrite is the same sentence for a write the filesystem refused.
//
// # Nothing is deleted
//
// A script that leaves the stage directory (a removal, a set removed with
// its validators, an orphan) is renamed into a sibling directory,
// recovery-test-validators.superseded, under its own name or with a numeric
// suffix when that name is taken. The sibling is not a stage directory, so
// discovery never reads it, and it is not inside the one that is, so it does
// not count against the 64 entry limit. In a container the two are separate
// mounts, where rename(2) fails with EXDEV however close they sit, so a move
// between them copies the script into the sibling, makes the copy durable,
// and only then takes it out of the stage directory. If that last removal
// fails the copy is taken back too, so the script exists exactly once at the
// end either way.
//
// The stage directory is visible on the host, so anyone who can write the
// workflows tree can leave anything in it. The copy is held to what a script
// is: a regular file, opened without following a link and without blocking
// on a FIFO, the same file it was when it was looked at, and no larger than
// an upload may be. Anything else is not moved: Reconcile leaves it where it
// is and names it, rather than refusing every later upload over it, and a
// temporary file a crashed write left behind is removed, since it was never
// a script anybody saved.

// errNotMovable is an entry the store will not move aside: not a regular
// file, or larger than a script may be.
var errNotMovable = errors.New("not a script the store moves")

// tempPrefix and tempSuffix name the store's own temporary files.
const (
	tempPrefix = ".retnd-validator-"
	tempSuffix = ".tmp"
)

// supersededSuffix names the sibling directory scripts are moved into.
const supersededSuffix = ".superseded"

// validatorStore is the stage directory under one workflow root. The zero
// value (no root) stores nothing: reads find nothing and moves are no-ops.
type validatorStore struct {
	root workflow.Root
	// rename is os.Rename when nil. A test sets it to fail the way a
	// rename between two mounts does.
	rename func(src, dst string) error
}

func newValidatorStore(root workflow.Root) validatorStore { return validatorStore{root: root} }

// storedScript is what the store knows about a script file without running or
// returning it.
type storedScript struct {
	// ModTime is when the file was last written; zero when there is no
	// regular file.
	ModTime time.Time
	Size    int64
	// SHA256 is the hex hash of the bytes in the file, "" when the file could
	// not be read within the limit.
	SHA256 string
}

func (s validatorStore) path(file string) string {
	return filepath.Join(s.root.Path(), config.CustomValidatorDir, file)
}

// Describe says whether file is a readable regular file within limit bytes,
// and its time, size and hash. The hash is cached by (path, mtime, size), so
// a list does not re-read every script every time. The returned time is set
// whenever there is a regular file, readable or not.
func (s validatorStore) Describe(file string, limit int64) (storedScript, bool) {
	if s.root.IsZero() {
		return storedScript{}, false
	}
	path := s.path(file)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return storedScript{}, false
	}
	out := storedScript{ModTime: info.ModTime().UTC()}
	sha, ok := cachedHash(path, info, limit)
	if !ok {
		return out, false
	}
	out.SHA256, out.Size = sha, info.Size()
	return out, true
}

// Read returns the bytes of file, refusing one over limit. Nothing is cached:
// a caller that keeps a script by its hash hashes what this returns.
func (s validatorStore) Read(file string, limit int64) ([]byte, error) {
	if s.root.IsZero() {
		return nil, os.ErrNotExist
	}
	return readBounded(s.path(file), limit)
}

// Write stores body as file and returns how to put things back as they were.
func (s validatorStore) Write(file string, body []byte) (func() error, error) {
	return writeValidatorScript(s.root, file, body)
}

// MoveAside renames file out of the stage directory into the superseded
// sibling. A file that is not there is os.ErrNotExist.
func (s validatorStore) MoveAside(file string) error {
	if s.root.IsZero() {
		return nil
	}
	return moveValidatorAside(s.root, file, s.renamer())
}

func (s validatorStore) renamer() func(src, dst string) error {
	if s.rename != nil {
		return s.rename
	}
	return os.Rename
}

// Reconcile moves every entry the configuration does not name out of the
// stage directory, and returns the ones it left where they are (each as
// "NAME: why") for the caller to report. declared holds lower-cased script
// file names.
func (s validatorStore) Reconcile(declared map[string]bool) ([]string, error) {
	if s.root.IsZero() {
		return nil, nil
	}
	return reconcileValidatorDir(s.root, declared, s.renamer())
}

// EntryCount is how many entries the stage directory holds, the number
// discovery's own limit is about.
func (s validatorStore) EntryCount() int {
	if s.root.IsZero() {
		return 0
	}
	return stageEntryCount(s.root)
}

// Unwritable says why this process cannot write a script into the stage
// directory, "" when it can or there is no root to look under (which
// workflowRootFor has already said). A directory that is not there yet is
// asked of its root, which is where writeValidatorScript would make it.
//
// The .superseded sibling is asked the same question, because a removal
// writes there: a card that offered an upload the next removal could not
// finish would leave a stray script that blocks the upload after.
func (s validatorStore) Unwritable() string {
	if s.root.IsZero() {
		return ""
	}
	for _, name := range []string{config.CustomValidatorDir, config.CustomValidatorDir + supersededSuffix} {
		dir := filepath.Join(s.root.Path(), name)
		target := dir
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			target = s.root.Path()
		}
		if err := unix.Access(target, unix.W_OK); err != nil {
			return unwritableReason(dir, err)
		}
	}
	return ""
}

// Refusal says why the stage directory cannot be used, or "" when it can, is
// not there yet, or there is no root to look under.
func (s validatorStore) Refusal() string {
	if s.root.IsZero() {
		return ""
	}
	if _, err := os.Lstat(filepath.Join(s.root.Path(), config.CustomValidatorDir)); err != nil {
		return ""
	}
	if _, err := s.root.ResolveStage(config.CustomValidatorDir); err != nil {
		return strings.TrimPrefix(err.Error(), "workflow: ")
	}
	return ""
}

// errScriptTooBig is readBounded's refusal of a file over its limit.
var errScriptTooBig = errors.New("larger than its limit")

// readBounded reads path, refusing one over limit bytes. It allocates what the
// file holds, never the limit: a list reads every declared script, and the
// limit is a ceiling and not a size.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is %w (%d bytes)", path, errScriptTooBig, limit)
	}
	return b, nil
}

// scriptHashes caches a script's hash by the identity of the file it was
// computed from, so a list does not re-read every script every time. A file
// rewritten with the same size inside the same timestamp tick would be
// missed; every write this process makes changes the time, and an operator's
// edit changes it too. An edit that means to KEEP a script never trusts this:
// it hashes the bytes it reads.
var scriptHashes = struct {
	sync.Mutex
	m map[string]cachedScriptHash
}{m: map[string]cachedScriptHash{}}

type cachedScriptHash struct {
	mtime int64
	size  int64
	sha   string
}

func cachedHash(path string, info os.FileInfo, limit int64) (sha string, ok bool) {
	key := info.ModTime().UnixNano()
	scriptHashes.Lock()
	c, hit := scriptHashes.m[path]
	scriptHashes.Unlock()
	if hit && c.mtime == key && c.size == info.Size() {
		return c.sha, true
	}
	body, err := readBounded(path, limit)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(body)
	sha = hex.EncodeToString(sum[:])
	scriptHashes.Lock()
	if len(scriptHashes.m) >= 4*config.MaxCustomValidators {
		clear(scriptHashes.m)
	}
	scriptHashes.m[path] = cachedScriptHash{mtime: key, size: int64(len(body)), sha: sha}
	scriptHashes.Unlock()
	return sha, true
}

// moveValidatorAside renames one file out of the stage directory into its
// .superseded sibling, under its own name or the first free numeric suffix.
// An entry that is not a regular file is errNotMovable.
func moveValidatorAside(root workflow.Root, file string, rename func(src, dst string) error) error {
	src := filepath.Join(root.Path(), config.CustomValidatorDir, file)
	aside := filepath.Join(root.Path(), config.CustomValidatorDir+supersededSuffix)
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is a %s, not a script, so it is left where it is; move it yourself", errNotMovable, file, fileKindOf(info.Mode()))
	}
	if err := os.MkdirAll(aside, 0o750); err != nil {
		return unavailableWrite(err, aside)
	}
	for n := 0; n < 10000; n++ {
		dst := filepath.Join(aside, file)
		if n > 0 {
			dst += "." + strconv.Itoa(n)
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := rename(src, dst); err != nil {
			if !errors.Is(err, syscall.EXDEV) {
				return err
			}
			if err := copyValidatorAside(src, dst, info); err != nil {
				return err
			}
		}
		syncDir(aside)
		syncDir(filepath.Dir(src))
		return nil
	}
	return fmt.Errorf("service: %s already has 10000 superseded copies of %s", aside, file)
}

// copyValidatorAside is the move for two directories on different mounts:
// copy src to dst (created exclusively, so nothing already superseded is
// overwritten), make the copy durable, then remove src. seen is what Lstat
// found at src, and the open has to be that same regular file.
//
// Any failure leaves src where it was and dst gone, including a removal of
// src that fails after a good copy, so the script is never in both places.
func copyValidatorAside(src, dst string, seen os.FileInfo) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("%w: %s cannot be opened as a script (%v), so it is left where it is", errNotMovable, filepath.Base(src), err)
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(seen, opened) {
		return fmt.Errorf("%w: %s changed while it was being moved, so it is left where it is", errNotMovable, filepath.Base(src))
	}
	if opened.Size() > MaxCustomValidatorScriptBytes {
		return fmt.Errorf("%w: %s is %d bytes, over the %d a script may be, so it is left where it is; move it yourself", errNotMovable, filepath.Base(src), opened.Size(), MaxCustomValidatorScriptBytes)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return unavailableWrite(err, filepath.Dir(dst))
	}
	n, err := io.Copy(out, io.LimitReader(in, MaxCustomValidatorScriptBytes+1))
	if err == nil && n > MaxCustomValidatorScriptBytes {
		err = fmt.Errorf("%w: %s grew past %d bytes while it was being copied", errNotMovable, filepath.Base(src), MaxCustomValidatorScriptBytes)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		if errors.Is(err, errNotMovable) {
			return err
		}
		return fmt.Errorf("service: copying %s into %s: %w", filepath.Base(src), filepath.Dir(dst), err)
	}
	syncDir(filepath.Dir(dst))
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		syncDir(filepath.Dir(dst))
		return fmt.Errorf("service: %s was copied into %s and could not then be removed (%w), so the copy was taken back and it is where it was", filepath.Base(src), filepath.Dir(dst), err)
	}
	return nil
}

// fileKindOf names what was found where a script was expected.
func fileKindOf(m os.FileMode) string {
	switch {
	case m&os.ModeSymlink != 0:
		return "symbolic link"
	case m.IsDir():
		return "directory"
	case m&os.ModeNamedPipe != 0:
		return "named pipe"
	case m&os.ModeSocket != 0:
		return "socket"
	case m&os.ModeDevice != 0:
		return "device"
	default:
		return "special file"
	}
}

// reconcileValidatorDir moves every entry the configuration does not name out
// of the stage directory, removes the store's own temporary files a crashed
// write left behind, and leaves anything it will not move (errNotMovable)
// where it is, returning those as "NAME: why". A directory that is not there
// yet has nothing to reconcile.
func reconcileValidatorDir(root workflow.Root, declared map[string]bool, rename func(src, dst string) error) ([]string, error) {
	dir := filepath.Join(root.Path(), config.CustomValidatorDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, unavailableWrite(err, dir)
	}
	var skipped []string
	for _, e := range entries {
		name := e.Name()
		if declared[strings.ToLower(name)] {
			continue
		}
		if strings.HasPrefix(name, tempPrefix) && strings.HasSuffix(name, tempSuffix) && e.Type().IsRegular() {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return skipped, unavailableWrite(err, dir)
			}
			continue
		}
		if err := moveValidatorAside(root, name, rename); err != nil {
			if errors.Is(err, errNotMovable) {
				skipped = append(skipped, name+": "+err.Error())
				continue
			}
			return skipped, fmt.Errorf("service: moving %s, which config.yaml does not name, out of %s: %w", name, config.CustomValidatorDir, err)
		}
	}
	return skipped, nil
}

// stageEntryCount is how many entries the stage directory holds, the number
// discovery's own limit is about.
func stageEntryCount(root workflow.Root) int {
	entries, err := os.ReadDir(filepath.Join(root.Path(), config.CustomValidatorDir))
	if err != nil {
		return 0
	}
	return len(entries)
}

// writeValidatorScript writes body as dir/file atomically and returns how to
// put things back as they were. The put-back reports its failure: a rollback
// that did not happen is something the caller has to say.
func writeValidatorScript(root workflow.Root, file string, body []byte) (func() error, error) {
	dir := filepath.Join(root.Path(), config.CustomValidatorDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, unavailableWrite(err, dir)
	}
	// Created above or by an operator: either way it must be one discovery
	// will accept, or the upload would be a script nothing can run.
	if _, err := root.ResolveStage(config.CustomValidatorDir); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidatorsUnavailable, err)
	}
	target := filepath.Join(dir, file)
	previous, prevErr := readBounded(target, MaxCustomValidatorScriptBytes)
	if errors.Is(prevErr, errScriptTooBig) {
		return nil, invalidValidator("the stored %s is over %d bytes, so it cannot be kept to put back if the configuration write fails; remove the validator and add it again", file, MaxCustomValidatorScriptBytes)
	}

	tmp, err := os.CreateTemp(dir, tempPrefix+"*"+tempSuffix)
	if err != nil {
		return nil, unavailableWrite(err, dir)
	}
	tmpName := tmp.Name()
	fail := func(err error) (func() error, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, err
	}
	if err := tmp.Chmod(0o640); err != nil {
		return fail(fmt.Errorf("service: writing %s: %w", file, err))
	}
	if _, err := tmp.Write(body); err != nil {
		return fail(unavailableWrite(err, dir))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("service: writing %s: %w", file, err))
	}
	if err := tmp.Close(); err != nil {
		return fail(fmt.Errorf("service: writing %s: %w", file, err))
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("service: writing %s: %w", file, err)
	}
	syncDir(dir)
	return func() error {
		if prevErr == nil {
			_, err := writeValidatorScript(root, file, previous)
			return err
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}, nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// unavailableWrite turns a write the filesystem refused into the reason this
// deployment cannot keep validators, when that is what it is.
func unavailableWrite(err error, where string) error {
	if errors.Is(err, syscall.EROFS) || errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w: %s", ErrValidatorsUnavailable, unwritableReason(where, err))
	}
	return fmt.Errorf("service: writing a validator script under %s: %w", where, err)
}

// unwritableReason is the sentence for a validator directory this process
// cannot write (#1246). It says what was tried and what fixes it, because
// the usual cause is a layout and not a mistake: the hook tree is mounted
// read-only on purpose, and these two directories need mounts of their own.
func unwritableReason(where string, err error) string {
	return fmt.Sprintf("%s is not writable by this process (%v). The workflow tree is normally mounted read-only, and custom validators need %s and %s mounted writable over it, from the same directories inside the host's workflows tree; the packaged compose stacks and the Docker host installer do this, so an older stack needs those two mounts added", where, err, config.CustomValidatorDir, config.CustomValidatorDir+supersededSuffix)
}

// scriptsDirOf is the absolute directory an upload lands in, "" with no root.
// One place joins the two, so no client has to guess which shape Directory
// has (#1246).
func scriptsDirOf(r WorkflowRootState) string {
	if r.Path == "" {
		return ""
	}
	return filepath.Join(r.Path, config.CustomValidatorDir)
}
