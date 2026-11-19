package rclone

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"

	"github.com/retnd/retnd/core/internal/transport"
)

// The compile-time half of the capabilities the recovery service asserts
// for, for the reason sourcewriteprobe.go gives about its own.
var (
	_ transport.SourceUploader      = (*Adapter)(nil)
	_ transport.SourceSpaceReporter = (*Adapter)(nil)
)

// The refusals an upload session reports. Like the write probe's, the
// underlying rclone error is classified and dropped, so no remote path or
// chroot home can travel in the message: the service turns these into its
// own sentences. UploadNotRemovedError is the one exception, on purpose: it
// exists to name the file somebody has to delete.
var (
	errSourceUploadPath     = errors.New("rclone: a source upload goes to folder/name directly under the source's remote path, in a retnd-recovered-* folder")
	errSourceUploadNotSFTP  = errors.New("rclone: a source upload is only ever made over SFTP")
	errSourceUploadExists   = errors.New("rclone: the source already holds something at that path, so nothing was written")
	errSourceUploadCheck    = errors.New("rclone: the source could not say whether something is already at that path")
	errSourceUploadWrite    = errors.New("rclone: the file could not be written onto the source")
	errSourceUploadRename   = errors.New("rclone: the uploaded file could not be renamed into place on the source")
	errSourceUploadSize     = errors.New("rclone: the source reports a different size for the file than was sent")
	errSourceUploadNoRemote = errors.New("rclone: a source upload needs a source with a remote path")
	errSourceUploadNoRename = errors.New("rclone: this backend has no rename, so nothing can be placed without risking a replacement")
	errSourceUploadClaim    = errors.New("rclone: the new folder could not be created on the source")
	errSourceUploadClosed   = errors.New("rclone: the upload session is closed")
	errSourceUploadDigest   = errors.New("rclone: the file on the source does not have the digest that was sent")
)

// uploadCleanupTimeout bounds how long a failed upload spends removing its
// own temporary file. It is not the caller's context: a cancelled or broken
// upload is exactly the one that has something to tidy up.
const uploadCleanupTimeout = 30 * time.Second

// BeginSourceUpload opens one upload session onto a backup source (issue
// #1191), for a whole recovery run.
//
// # Never a replacement, by construction
//
// Every file is written under a temporary name in its folder and then moved
// onto its real name with a plain SFTP rename (SSH_FXP_RENAME). A server
// refuses that rename when anything is already at the target, which is what
// makes "never replaces" a property of the protocol rather than of timing.
// rclone's Move is deliberately not used: it reaches for posix-rename, which
// replaces, or removes the target first where that extension is missing.
// rclone's sftp DirMove is the plain rename, and it is what this calls for a
// file as well as for a folder.
//
// # Nothing half-written under a real name
//
// A write that fails part way leaves only its temporary name, which this
// session then removes, and only that name: never the target, never a file
// somebody else put there.
//
// # SFTP only
//
// The local backend's rename replaces whatever is at the target, so the
// guarantee above would not hold there, and a source-host recovery is only
// ever offered for an SFTP set anyway.
func (a *Adapter) BeginSourceUpload(ctx context.Context, src transport.Source) (transport.SourceUploadSession, error) {
	if src.Type != "sftp" {
		return nil, transport.NewError(transport.Configuration, "begin_source_upload", errSourceUploadNotSFTP)
	}
	if src.Root == "" {
		return nil, transport.NewError(transport.Configuration, "begin_source_upload", errSourceUploadNoRemote)
	}
	release, err := transport.AcquireSourceHost(ctx, src)
	if err != nil {
		return nil, WrapCtx(ctx, "begin_source_upload", err)
	}
	f, err := newSourceUploadFs(oneConnectionAtATime(ctx), src, false)
	if err != nil {
		release()
		return nil, WrapCtx(ctx, "begin_source_upload", err)
	}
	return newUploadSession(src, f, release), nil
}

// newSourceUploadFs is fsFor (or fsForHashing) plus set_modtime=false.
//
// A server that refuses setstat would otherwise fail the upload AFTER every
// byte had landed, rclone would report the write as failed, and the caller
// would remove a complete file and call the host read-only. A recovered file
// carries the time it was put back, which is also the honest one.
func newSourceUploadFs(ctx context.Context, src transport.Source, forHashing bool) (fs.Fs, error) {
	info, err := fs.Find(src.Type)
	if err != nil {
		return nil, fmt.Errorf("backend %q is not registered in this binary: %w", src.Type, err)
	}
	cfg, err := sftpConfig(src)
	if err != nil {
		return nil, err
	}
	if forHashing {
		cfg = withSHA256(cfg)
	}
	cfg.Set("set_modtime", "false")
	f, err := info.NewFs(ctx, src.ID, src.Root, cfg)
	if err != nil {
		// The live-Fs-with-an-error case newFs documents.
		if f != nil {
			shutdownFs(ctx, f)
		}
		return nil, fmt.Errorf("source %q: %w", src.ID, err)
	}
	return f, nil
}

type uploadSession struct {
	src transport.Source

	mu      sync.Mutex
	f       fs.Fs
	hf      fs.Fs // built on the first digest asked for
	release func()
	closed  bool
}

func newUploadSession(src transport.Source, f fs.Fs, release func()) *uploadSession {
	return &uploadSession{src: src, f: f, release: release}
}

// validRecoveredFolderName is the adapter's own copy of the folder rule, so
// a caller that got it wrong cannot write anywhere else: one segment of
// letters, digits, '.', '_' and '-', carrying the prefix, with something
// after it and no trailing dot.
func validRecoveredFolderName(folder string) bool {
	if !strings.HasPrefix(folder, transport.RecoveredFolderPrefix) || len(folder) == len(transport.RecoveredFolderPrefix) {
		return false
	}
	if strings.HasSuffix(folder, ".") {
		return false
	}
	for _, r := range folder {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// splitUploadRelPath checks an upload's path: exactly folder/name, the
// folder a recovered-folder name, the name a plain file name that is not
// hidden and cannot be mistaken for one of this session's temporary names.
func splitUploadRelPath(rel string) (folder, name string, err error) {
	folder, name, ok := strings.Cut(rel, "/")
	switch {
	case !ok, !validRecoveredFolderName(folder), name == "",
		strings.ContainsAny(name, "/\\\x00"), strings.HasPrefix(name, "."),
		strings.Contains(name, transport.UploadTempMarker), path.Clean(rel) != rel:
		return "", "", errSourceUploadPath
	}
	return folder, name, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *uploadSession) liveFs() (fs.Fs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.f == nil {
		return nil, errSourceUploadClosed
	}
	return s.f, nil
}

// rename is the plain SFTP rename (see BeginSourceUpload's doc).
func rename(ctx context.Context, f fs.Fs, from, to string) error {
	mover, ok := f.(fs.DirMover)
	if !ok {
		return errSourceUploadNoRename
	}
	return mover.DirMove(ctx, f, from, to)
}

// present reports whether anything at all is at rel: a file, a folder, or a
// symlink, dangling or not. It lists the parent rather than asking for the
// object, because a stat follows a symlink and a dangling one would read as
// nothing there. A missing parent means nothing is there.
func present(ctx context.Context, f fs.Fs, rel string) (bool, error) {
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	entries, err := f.List(ctx, dir)
	if errors.Is(err, fs.ErrorDirNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Remote() == rel {
			return true, nil
		}
	}
	return false, nil
}

// presentDir is present for a folder that is a real directory: a dangling
// symlink lists as an object, not a directory.
func presentDir(ctx context.Context, f fs.Fs, rel string) (dir, any bool, err error) {
	entries, err := f.List(ctx, "")
	if errors.Is(err, fs.ErrorDirNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	for _, e := range entries {
		if e.Remote() == rel {
			_, isDir := e.(fs.Directory)
			return isDir, true, nil
		}
	}
	return false, false, nil
}

func existsErr(op string) error {
	return transport.NewError(transport.Permanent, op, errors.Join(transport.ErrSourceObjectExists, errSourceUploadExists))
}

// ClaimFolder creates a new folder directly under the source's root, never
// reusing one that is there unless reuse says so (and then only a real
// folder). It is a rename of a freshly made, uniquely named folder onto the
// name asked for, so two claims of one name cannot both win.
//
// One gap I could not close through rclone: a symlink that points at a
// folder lists as a folder (rclone's sftp backend has no lstat), so reuse
// cannot tell the two apart. A fresh claim is not affected, because the
// rename refuses anything at the name, symlinks included.
func (s *uploadSession) ClaimFolder(ctx context.Context, folder string, reuse bool) error {
	const op = "claim_source_folder"
	if !validRecoveredFolderName(folder) {
		return transport.NewError(transport.Configuration, op, errSourceUploadPath)
	}
	f, err := s.liveFs()
	if err != nil {
		return transport.NewError(transport.Configuration, op, err)
	}
	ctx = oneConnectionAtATime(ctx)
	isDir, there, err := presentDir(ctx, f, folder)
	if err != nil {
		return transport.NewError(ClassifyCtx(ctx, err), op, errSourceUploadCheck)
	}
	if there {
		if reuse && isDir {
			return nil
		}
		return existsErr(op)
	}
	suffix, err := randomHex(8)
	if err != nil {
		return transport.NewError(transport.Configuration, op, err)
	}
	tmp := transport.RecoveredFolderPrefix + ".claim-" + suffix
	if err := f.Mkdir(ctx, tmp); err != nil {
		return transport.NewError(ClassifyCtx(ctx, err), op, errSourceUploadClaim)
	}
	if err := rename(ctx, f, tmp, folder); err != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadCleanupTimeout)
		defer cancel()
		_ = f.Rmdir(cctx, tmp)
		if errors.Is(err, fs.ErrorDirExists) || errors.Is(err, fs.ErrorIsFile) {
			return existsErr(op)
		}
		if there, perr := present(cctx, f, folder); perr == nil && there {
			return existsErr(op)
		}
		return transport.NewError(ClassifyCtx(ctx, err), op, errSourceUploadClaim)
	}
	return nil
}

// Upload places one file at relPath through a temporary name; see
// BeginSourceUpload.
func (s *uploadSession) Upload(ctx context.Context, localPath, relPath, wantSHA256 string) (transport.UploadVerification, error) {
	const op = "upload_to_source"
	folder, name, err := splitUploadRelPath(relPath)
	if err != nil {
		return "", transport.NewError(transport.Configuration, op, err)
	}
	f, err := s.liveFs()
	if err != nil {
		return "", transport.NewError(transport.Configuration, op, err)
	}
	local, err := os.Open(localPath)
	if err != nil {
		return "", transport.NewError(transport.Configuration, op, fmt.Errorf("opening the recovered file: %w", err))
	}
	defer local.Close()
	info, err := local.Stat()
	if err != nil {
		return "", transport.NewError(transport.Configuration, op, fmt.Errorf("reading the recovered file: %w", err))
	}
	ctx = oneConnectionAtATime(ctx)

	// The fast refusal, before a byte is sent. The rename below is what
	// actually guarantees nothing is replaced; this only saves uploading a
	// large file to be told so.
	switch _, err := f.NewObject(ctx, relPath); {
	case err == nil, errors.Is(err, fs.ErrorIsDir):
		return "", existsErr(op)
	case !errors.Is(err, fs.ErrorObjectNotFound):
		return "", transport.NewError(ClassifyCtx(ctx, err), op, errSourceUploadCheck)
	}

	suffix, err := randomHex(4)
	if err != nil {
		return "", transport.NewError(transport.Configuration, op, err)
	}
	tmp := folder + "/." + name + transport.UploadTempMarker + suffix
	fail := func(cat transport.Category, cause error) error {
		if lerr := s.removeTemp(ctx, f, tmp); lerr != nil {
			return transport.NewError(cat, op, errors.Join(cause, lerr))
		}
		return transport.NewError(cat, op, cause)
	}

	objInfo := object.NewStaticObjectInfo(tmp, info.ModTime(), info.Size(), true, nil, f)
	written, err := f.Put(ctx, local, objInfo)
	if err != nil {
		return "", fail(ClassifyCtx(ctx, err), errSourceUploadWrite)
	}
	if written.Size() != info.Size() {
		return "", fail(transport.Permanent, errSourceUploadSize)
	}
	if err := rename(ctx, f, tmp, relPath); err != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadCleanupTimeout)
		there, perr := present(cctx, f, relPath)
		cancel()
		if errors.Is(err, fs.ErrorDirExists) || errors.Is(err, fs.ErrorIsFile) || (perr == nil && there) {
			return "", fail(transport.Permanent, errors.Join(transport.ErrSourceObjectExists, errSourceUploadExists))
		}
		return "", fail(ClassifyCtx(ctx, err), errSourceUploadRename)
	}

	if wantSHA256 == "" {
		return transport.VerifiedSize, nil
	}
	got, ok := s.digest(ctx, relPath)
	if !ok {
		return transport.VerifiedSize, nil
	}
	if !strings.EqualFold(got, wantSHA256) {
		// The one time this session removes a file under its real name: it
		// is the file this call just placed, and it is not the backup.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadCleanupTimeout)
		defer cancel()
		cause := errors.Join(transport.ErrSourceDigestMismatch, errSourceUploadDigest)
		if o, oerr := f.NewObject(cctx, relPath); oerr == nil {
			if rerr := o.Remove(cctx); rerr != nil {
				cause = errors.Join(cause, &transport.UploadNotRemovedError{Path: path.Join(s.src.Root, relPath), Err: rerr})
			}
		}
		return "", transport.NewError(transport.Permanent, op, cause)
	}
	return transport.VerifiedSHA256, nil
}

// removeTemp removes the temporary name this call wrote, and nothing else,
// on its own short deadline. A name that is not there is fine: the write
// may have failed before creating it.
func (s *uploadSession) removeTemp(ctx context.Context, f fs.Fs, tmp string) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadCleanupTimeout)
	defer cancel()
	full := path.Join(s.src.Root, tmp)
	o, err := f.NewObject(cctx, tmp)
	if errors.Is(err, fs.ErrorObjectNotFound) {
		return nil
	}
	if err == nil {
		err = o.Remove(cctx)
	}
	if err != nil {
		fs.Errorf(nil, "retnd: a failed source upload could not remove its temporary file: %v", err)
		return &transport.UploadNotRemovedError{Path: full, Err: err}
	}
	return nil
}

// digest is the host's own SHA-256 of rel, or false where the host cannot
// compute one (no hashing backend, a shell-less account, a failed command).
func (s *uploadSession) digest(ctx context.Context, rel string) (string, bool) {
	s.mu.Lock()
	hf := s.hf
	if hf == nil && !s.closed {
		var err error
		hf, err = newSourceUploadFs(ctx, s.src, true)
		if err != nil {
			s.mu.Unlock()
			return "", false
		}
		s.hf = hf
	}
	s.mu.Unlock()
	if hf == nil || !hf.Hashes().Contains(hash.SHA256) {
		return "", false
	}
	o, err := hf.NewObject(ctx, rel)
	if err != nil {
		return "", false
	}
	sum, err := o.Hash(ctx, hash.SHA256)
	if err != nil || sum == "" {
		return "", false
	}
	return sum, true
}

// Close hangs up both connections and gives the host's write slot back. It
// is safe to call more than once.
func (s *uploadSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	ctx := context.Background()
	if s.f != nil {
		shutdownFs(ctx, s.f)
	}
	if s.hf != nil && s.hf != s.f {
		shutdownFs(ctx, s.hf)
	}
	if s.release != nil {
		s.release()
	}
	return nil
}

// SourceFreeSpace asks the source for the free space on the volume its root
// is on (statvfs@openssh.com on an OpenSSH server). A server that cannot say
// is not an error: known is false, and the caller says so in words.
func (a *Adapter) SourceFreeSpace(ctx context.Context, src transport.Source) (int64, bool, error) {
	if src.Root == "" {
		return 0, false, transport.NewError(transport.Configuration, "source_free_space", errSourceUploadNoRemote)
	}
	ctx = oneConnectionAtATime(ctx)
	f, err := a.fsFor(ctx, src)
	if err != nil {
		return 0, false, WrapCtx(ctx, "source_free_space", err)
	}
	defer shutdownFs(ctx, f)
	about := f.Features().About
	if about == nil {
		return 0, false, nil
	}
	u, err := about(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, WrapCtx(ctx, "source_free_space", err)
		}
		// The server could not answer (no statvfs extension and no shell to
		// run df with, or a root that is not there yet): unknown, not
		// broken.
		return 0, false, nil
	}
	if u == nil || u.Free == nil {
		return 0, false, nil
	}
	return *u.Free, true, nil
}
