package export

import (
	"errors"
	"time"

	"github.com/retnd/retnd/core/internal/archive"
)

// BestAvailable is the copy selector that asks the resolver to choose.
const BestAvailable = "best-available"

// Copy is one retained copy of an artifact as the resolver sees it. The
// service builds these from placements rows, so the work list is always the
// journal and never a listing of a destination.
type Copy struct {
	// Medium is "local" or the id of a configured storage medium. Together
	// with the artifact it names the copy: the journal keeps one placement
	// per artifact per medium.
	Medium string
	Local  bool

	// Active is false for a copy the journal records as pending deletion or
	// gone. Only an active copy is ever read.
	Active bool

	Size              *int64
	Hash, HashAlg     string
	VerificationClass string
	VerifiedAt        *time.Time

	// Access is what the copy's storage class and restore state allow,
	// derived the way internal/archive derives it.
	Access archive.State

	// Chargeable is true when reading this copy bills egress (an object
	// store), and false for a copy on this host's own disk or a directory
	// standing in for a bucket.
	Chargeable bool

	// StorageClass is the class the medium stores the copy with, for the
	// sentence a refusal prints. It is never a price.
	StorageClass string
}

// ResolveRequest is what an operator asked for.
type ResolveRequest struct {
	// Copy is a medium id, or BestAvailable (also the meaning of "").
	Copy string

	// AcknowledgeCost is the explicit yes to a chargeable retrieval.
	AcknowledgeCost bool
}

// Resolution is the resolver's answer.
type Resolution struct {
	Copy Copy

	// NeedsThaw is true when the chosen copy is archive-class and has to be
	// restored by the provider before anything can be read. The caller
	// starts exactly one provider job for it and reports that the recovery
	// has not happened yet.
	NeedsThaw bool
}

var (
	// ErrNoCopy: the artifact has no active copy anywhere.
	ErrNoCopy = errors.New("recovery: the artifact has no retained copy to recover")

	// ErrCopyNotFound: the named copy is not one of the artifact's.
	ErrCopyNotFound = errors.New("recovery: the artifact has no such copy")

	// ErrNotReadable: nothing the request allows can be read right now.
	ErrNotReadable = errors.New("recovery: no copy is readable right now")

	// ErrCostNotAcknowledged: the retrieval is chargeable and the request
	// did not say it accepts that.
	ErrCostNotAcknowledged = errors.New("recovery: this retrieval is chargeable and was not acknowledged")
)

// CostRefusal is ErrCostNotAcknowledged with the sentence an operator needs
// to decide: which copy, and what reading it takes.
type CostRefusal struct {
	Medium string
	Detail string
}

func (e *CostRefusal) Error() string {
	return "recovery: reading the copy on " + e.Medium + " is chargeable (" + e.Detail + "); pass the cost acknowledgement to accept that"
}

func (e *CostRefusal) Is(target error) bool { return target == ErrCostNotAcknowledged }
