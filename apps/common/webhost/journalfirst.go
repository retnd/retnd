package webhost

import "context"

// JournalHolder is optionally implemented by a backend that can say how many
// artifacts its journal holds. The settings read uses it so a deployment
// whose configuration lost every set, but whose journal still holds backups,
// is not described as a first run (retnd#1049). A backend without it keeps
// the old reading.
type JournalHolder interface {
	JournalArtifactCount(ctx context.Context) (int64, error)
}
