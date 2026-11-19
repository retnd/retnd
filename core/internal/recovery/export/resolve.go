package export

import (
	"sort"

	"github.com/retnd/retnd/core/internal/archive"
	"github.com/retnd/retnd/core/internal/placement"
)

// Resolve picks the copy a request names, or the best readable one when it
// names none (FR-68).
//
// # What "best" means
//
// The strongest verification class the journal has recorded for the copy,
// because that is the copy whose bytes somebody has most recently had a
// reason to believe. A class is what a copy actually achieved, never what
// was configured, so a copy that has never been verified ranks below one
// that has. A tie goes to the copy that costs nothing to read, then to the
// one verified more recently, then to the lower medium id so that two runs
// over the same journal always agree.
//
// # What it will not do
//
// It never reads a copy the journal no longer wants (a delete pending or
// done), never treats a copy that is mid-restore or unreachable as
// readable, and never pays for anything on its own. A retrieval that bills
// egress (placement.Class.CostsEgress, the same test revalidation applies
// before it spends) is refused unless the request carries the explicit
// acknowledgement, and a copy that needs the provider to restore it first
// is refused on the same terms and asks for exactly one thaw when
// acknowledged. The refusal is a return value: it starts nothing.
func Resolve(copies []Copy, req ResolveRequest) (Resolution, error) {
	active := make([]Copy, 0, len(copies))
	for _, c := range copies {
		if c.Active {
			active = append(active, c)
		}
	}
	if len(active) == 0 {
		return Resolution{}, ErrNoCopy
	}

	named := req.Copy != "" && req.Copy != BestAvailable
	if named {
		for _, c := range active {
			if c.Medium == req.Copy {
				return decide(c, req)
			}
		}
		return Resolution{}, ErrCopyNotFound
	}

	sort.SliceStable(active, func(i, j int) bool { return better(active[i], active[j]) })

	// Best readable copy this request is allowed to pay for. Without an
	// acknowledgement that is the free ones only; the first chargeable one
	// is remembered so the refusal can name what was declined.
	var declined *Copy
	for i := range active {
		c := active[i]
		if c.Access != archive.Immediate {
			continue
		}
		if chargeable(c) && !req.AcknowledgeCost {
			if declined == nil {
				declined = &active[i]
			}
			continue
		}
		return Resolution{Copy: c}, nil
	}
	if declined != nil {
		return Resolution{}, costRefusal(*declined)
	}

	// Nothing readable. An archive copy is the one case money can fix.
	for _, c := range active {
		if c.Access == archive.RequiresRestore {
			if !req.AcknowledgeCost {
				return Resolution{}, costRefusal(c)
			}
			return Resolution{Copy: c, NeedsThaw: true}, nil
		}
	}
	return Resolution{}, ErrNotReadable
}

// decide applies the readability and cost rules to one explicitly named
// copy.
func decide(c Copy, req ResolveRequest) (Resolution, error) {
	switch c.Access {
	case archive.Immediate:
		if chargeable(c) && !req.AcknowledgeCost {
			return Resolution{}, costRefusal(c)
		}
		return Resolution{Copy: c}, nil
	case archive.RequiresRestore:
		if !req.AcknowledgeCost {
			return Resolution{}, costRefusal(c)
		}
		return Resolution{Copy: c, NeedsThaw: true}, nil
	default:
		return Resolution{}, ErrNotReadable
	}
}

func chargeable(c Copy) bool {
	return !c.Local && c.Chargeable && placement.Content.CostsEgress()
}

func costRefusal(c Copy) error {
	detail := placement.Content.Cost()
	if c.Access == archive.RequiresRestore {
		detail = "a provider restore first, which is billed and takes hours, then " + detail
	}
	return &CostRefusal{Medium: c.Medium, Detail: detail}
}

// better orders copies for best-available.
func better(a, b Copy) bool {
	ca, cb := placement.Class(a.VerificationClass), placement.Class(b.VerificationClass)
	if ca != cb {
		return ca.Stronger(cb)
	}
	if a.Local != b.Local {
		return a.Local
	}
	if a.Chargeable != b.Chargeable {
		return !a.Chargeable
	}
	switch {
	case a.VerifiedAt != nil && b.VerifiedAt == nil:
		return true
	case a.VerifiedAt == nil && b.VerifiedAt != nil:
		return false
	case a.VerifiedAt != nil && b.VerifiedAt != nil && !a.VerifiedAt.Equal(*b.VerifiedAt):
		return a.VerifiedAt.After(*b.VerifiedAt)
	}
	return a.Medium < b.Medium
}
