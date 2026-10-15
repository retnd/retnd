package webhost

import (
	"context"

	"github.com/retnd/retnd/core/service"
)

// HostBrowseClient is what GET /api/v1/system/host-directories needs from the
// backend (issue #1179). STUB: the route arrives with the fix.
type HostBrowseClient interface {
	BrowseHostDirectory(ctx context.Context, rel string) (service.HostDirectory, error)
}

var _ HostBrowseClient = (*service.BackupService)(nil)
