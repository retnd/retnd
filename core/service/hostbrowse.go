package service

import (
	"context"
	"errors"
)

// Issue #1179. STUB: the red tests are committed against this before the
// listing exists, so they fail on what they assert and not on a build error.

// HostDirectoryLimit is the most entries one listing returns.
const HostDirectoryLimit = 1000

var (
	ErrHostBrowseInvalid     = errors.New("that is not a folder name under the browsable root")
	ErrHostBrowseNotFound    = errors.New("there is no such folder under the browsable root")
	ErrHostBrowseUnavailable = errors.New("this deployment has no folder to browse")
)

// HostDirectoryEntry is one sub-folder of a listed folder.
type HostDirectoryEntry struct {
	Name string
	Path string
}

// HostDirectory is one folder under the browsable root.
type HostDirectory struct {
	Root      string
	Path      string
	Parent    *string
	Entries   []HostDirectoryEntry
	Truncated bool
}

// BrowseHostDirectory lists the sub-folders of rel under the staging root.
func (b *BackupService) BrowseHostDirectory(ctx context.Context, rel string) (HostDirectory, error) {
	return HostDirectory{}, errors.New("not implemented")
}
