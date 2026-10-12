package revalidate

import (
	"context"

	"github.com/retnd/retnd/core/internal/transport"
)

// Lister is the destination-listing capability (QC-04): what a medium
// actually holds under a prefix, so a pass can compare it with the journal.
// transport.MediumStore has it as ListObjects; Deps.Store is asserted to it
// rather than widened, so a read-only double without a listing stays valid.
type Lister interface {
	ListObjects(ctx context.Context, medium transport.Medium, prefix string) ([]transport.ObjectInfo, error)
}

// UnknownObject is one object at a key the journal does not know.
type UnknownObject struct {
	Medium string
	Key    string
	Size   int64
}

// UnlistedMedium is a destination a pass did not enumerate.
type UnlistedMedium struct {
	Medium string
	Reason string
}
