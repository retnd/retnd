package rclone

import (
	"context"
	"errors"

	"github.com/retnd/retnd/core/internal/transport"
)

var _ transport.SourceUploader = (*Adapter)(nil)

// UploadToSource is issue #1191's source-host recovery write. Not written yet.
func (a *Adapter) UploadToSource(ctx context.Context, src transport.Source, localPath, relPath string) error {
	return errors.New("rclone: uploading to a source is not implemented yet")
}
