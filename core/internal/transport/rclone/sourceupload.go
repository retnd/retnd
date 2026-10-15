package rclone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/object"

	"github.com/retnd/retnd/core/internal/transport"
)

// The compile-time half of the capability the recovery service asserts for,
// for the reason sourcewriteprobe.go gives about its own.
var _ transport.SourceUploader = (*Adapter)(nil)

// The refusals UploadToSource reports. Like the write probe's, the underlying
// rclone error is classified and dropped, so no remote path or chroot home
// can travel in the message: the service turns these into its own sentences.
var (
	errSourceUploadPath     = errors.New("rclone: a source upload needs a clean relative path under the source's remote path")
	errSourceUploadExists   = errors.New("rclone: the source already holds a file at that path, so nothing was written")
	errSourceUploadCheck    = errors.New("rclone: the source could not say whether a file is already at that path")
	errSourceUploadWrite    = errors.New("rclone: the file could not be written onto the source")
	errSourceUploadSize     = errors.New("rclone: the source reports a different size for the file than was sent")
	errSourceUploadNoRemote = errors.New("rclone: a source upload needs a source with a remote path")
)

// UploadToSource writes one recovered file back onto a backup source
// (issue #1191), at relPath under the source's remote path.
//
// # Never a replacement
//
// It looks first and refuses when anything is at relPath. SFTP has no
// exclusive create that rclone exposes, so there is a window between the
// look and the write in which another writer could put a file there; the
// caller narrows it to nothing in practice by always writing into a folder
// it has just named (a new retnd-recovered-<time> folder), which nobody else
// writes to.
//
// # Nothing half-written left behind
//
// A write that fails part way, or that lands at a size other than the local
// file's, is removed again before the error is returned, because a short file
// sitting under a recovered name is worse than no file. The removal is best
// effort and only ever touches the path this call created.
func (a *Adapter) UploadToSource(ctx context.Context, src transport.Source, localPath, relPath string) error {
	if src.Root == "" {
		return transport.NewError(transport.Configuration, "upload_to_source", errSourceUploadNoRemote)
	}
	if relPath == "" || path.IsAbs(relPath) || path.Clean(relPath) != relPath || relPath == ".." || strings.HasPrefix(relPath, "../") {
		return transport.NewError(transport.Configuration, "upload_to_source", errSourceUploadPath)
	}
	local, err := os.Open(localPath)
	if err != nil {
		return transport.NewError(transport.Configuration, "upload_to_source", fmt.Errorf("opening the recovered file: %w", err))
	}
	defer local.Close()
	info, err := local.Stat()
	if err != nil {
		return transport.NewError(transport.Configuration, "upload_to_source", fmt.Errorf("reading the recovered file: %w", err))
	}

	ctx = oneConnectionAtATime(ctx)
	f, err := a.fsFor(ctx, src)
	if err != nil {
		return WrapCtx(ctx, "upload_to_source", err)
	}
	defer shutdownFs(ctx, f)

	switch _, err := f.NewObject(ctx, relPath); {
	case err == nil:
		return transport.NewError(transport.Permanent, "upload_to_source", errors.Join(transport.ErrSourceObjectExists, errSourceUploadExists))
	case !errors.Is(err, fs.ErrorObjectNotFound) && !errors.Is(err, fs.ErrorIsDir):
		return transport.NewError(ClassifyCtx(ctx, err), "upload_to_source", errSourceUploadCheck)
	}

	objInfo := object.NewStaticObjectInfo(relPath, info.ModTime(), info.Size(), true, nil, f)
	written, err := f.Put(ctx, local, objInfo)
	if err != nil {
		a.removeHalfWritten(ctx, f, relPath)
		return transport.NewError(ClassifyCtx(ctx, err), "upload_to_source", errSourceUploadWrite)
	}
	if written.Size() != info.Size() {
		a.removeHalfWritten(ctx, f, relPath)
		return transport.NewError(transport.Permanent, "upload_to_source", errSourceUploadSize)
	}
	return nil
}

// removeHalfWritten removes what a failed upload may have left at relPath.
// The caller checked that nothing was there before it wrote, so whatever is
// there now is its own.
func (a *Adapter) removeHalfWritten(ctx context.Context, f fs.Fs, relPath string) {
	ctx = context.WithoutCancel(ctx)
	if o, err := f.NewObject(ctx, relPath); err == nil {
		_ = o.Remove(ctx)
	}
}
