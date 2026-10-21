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
// To a temporary file in the workflow root and then renamed into the
// directory, so discovery never sees a half-written file or an entry whose
// name is not a script's (it refuses a whole directory for one of those).
// The directory is 0750 and the file 0640, both owned by this process, which
// is what discovery's custody rules accept.
//
// # Nothing is deleted
//
// A script that leaves the stage directory (a removal, a set removed with
// its validators, an orphan) is renamed into a sibling directory,
// recovery-test-validators.superseded, under its own name or with a numeric
// suffix when that name is taken. The sibling is not a stage directory, so
// discovery never reads it, and it is not inside the one that is, so it does
// not count against the 64 entry limit.

// supersededSuffix names the sibling directory scripts are moved into.
const supersededSuffix = ".superseded"

// validatorStore is the stage directory under one workflow root. The zero
// value (no root) stores nothing: reads find nothing and moves are no-ops.
type validatorStore struct {
	root workflow.Root
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
	return moveValidatorAside(s.root, file)
}

// Reconcile moves every entry the configuration does not name out of the
// stage directory. declared holds lower-cased script file names.
func (s validatorStore) Reconcile(declared map[string]bool) error {
	if s.root.IsZero() {
		return nil
	}
	return reconcileValidatorDir(s.root, declared)
}

// EntryCount is how many entries the stage directory holds, the number
// discovery's own limit is about.
func (s validatorStore) EntryCount() int {
	if s.root.IsZero() {
		return 0
	}
	return stageEntryCount(s.root)
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
func moveValidatorAside(root workflow.Root, file string) error {
	src := filepath.Join(root.Path(), config.CustomValidatorDir, file)
	aside := filepath.Join(root.Path(), config.CustomValidatorDir+supersededSuffix)
	if _, err := os.Lstat(src); err != nil {
		return err
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
		if err := renameValidatorFile(src, dst); err != nil {
			return err
		}
		syncDir(aside)
		syncDir(filepath.Dir(src))
		return nil
	}
	return fmt.Errorf("service: %s already has 10000 superseded copies of %s", aside, file)
}

// reconcileValidatorDir moves every entry the configuration does not name out
// of the stage directory. A directory that is not there yet has nothing to
// reconcile.
func reconcileValidatorDir(root workflow.Root, declared map[string]bool) error {
	dir := filepath.Join(root.Path(), config.CustomValidatorDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return unavailableWrite(err, dir)
	}
	for _, e := range entries {
		if declared[strings.ToLower(e.Name())] {
			continue
		}
		if err := moveValidatorAside(root, e.Name()); err != nil {
			return fmt.Errorf("service: moving %s, which config.yaml does not name, out of %s: %w", e.Name(), config.CustomValidatorDir, err)
		}
	}
	return nil
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

	tmp, err := os.CreateTemp(root.Path(), ".retnd-validator-*.tmp")
	if err != nil {
		return nil, unavailableWrite(err, root.Path())
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
		return fail(unavailableWrite(err, root.Path()))
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
		return fmt.Errorf("%w: %s cannot be written by this process (%v); the workflow tree may be mounted read-only, which is the usual container layout, so put the script there yourself and declare it in config.yaml", ErrValidatorsUnavailable, where, err)
	}
	return fmt.Errorf("service: writing a validator script under %s: %w", where, err)
}
