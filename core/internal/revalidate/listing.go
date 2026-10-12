package revalidate

import (
	"context"
	"sort"
	"strings"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
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

// listDestinations is the listing half of QC-04. It asks each storage medium
// that holds, or ever held, a copy for this set what it actually has under the
// set's namespace (<medium prefix>/<source>/<set>, the layout
// transport.MediumKey writes), and compares that with the journal.
//
// The journal is the only authority on what is a copy. An object whose key no
// placement of any artifact in the set names, in any status, is reported as
// Unknown and nothing else: it is not statted, hashed or counted as a copy, it
// never satisfies an expected copy that is missing at its own key, and no
// action is taken on it. "Any status" is deliberate: a gone placement whose
// object is still there is an orphan an operator wants to hear about, and an
// upload that finished a moment before its placement was recorded is a
// finding that settles itself on the next pass.
//
// A medium it cannot list is reported in Unlisted with the reason (no listing
// capability on the store, not configured, or the listing failed) and is never
// a copy failure: not being able to look says nothing about what is there.
//
// A scheduled pass that selected nothing does not list (a listing is a billed
// request per medium per poll); a forced or manual one always does. Only a
// pass over the whole set lists. One narrowed to an artifact is about
// that artifact, and a set-wide comparison inside it would be a side effect
// the operator did not ask for.
func listDestinations(ctx context.Context, deps Deps, set model.BackupSetID, records []state.Record, opts Options, selected int) (unknown []UnknownObject, unlisted []UnlistedMedium) {
	if deps.Store == nil || deps.Mediums == nil || opts.Only != nil {
		return nil, nil
	}
	if selected == 0 && !opts.Force && !opts.ListDestinations {
		return nil, nil
	}
	known := map[string]map[string]bool{}
	for _, rec := range records {
		for _, p := range rec.Placements {
			if p.Medium == state.MediumLocal {
				continue
			}
			if known[p.Medium] == nil {
				known[p.Medium] = map[string]bool{}
			}
			known[p.Medium][p.Location] = true
		}
	}
	ids := make([]string, 0, len(known))
	for id := range known {
		if opts.Medium != "" && id != opts.Medium {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	lister, canList := deps.Store.(Lister)
	for _, id := range ids {
		if ctx.Err() != nil {
			return unknown, unlisted
		}
		medium, ok := deps.Mediums.MediumFor(id)
		if !ok {
			unlisted = append(unlisted, UnlistedMedium{Medium: id, Reason: "the medium is not configured"})
			continue
		}
		if !canList {
			unlisted = append(unlisted, UnlistedMedium{Medium: id, Reason: "the medium store cannot list objects"})
			continue
		}
		objects, err := lister.ListObjects(ctx, medium, namespaceFor(medium, set))
		if err != nil {
			if ctx.Err() != nil {
				return unknown, unlisted
			}
			unlisted = append(unlisted, UnlistedMedium{Medium: id, Reason: err.Error()})
			continue
		}
		for _, o := range objects {
			if known[id][o.Key] || strings.HasSuffix(o.Key, "/") {
				continue
			}
			unknown = append(unknown, UnknownObject{Medium: id, Key: o.Key, Size: o.Size})
		}
	}
	sort.Slice(unknown, func(i, j int) bool {
		if unknown[i].Medium != unknown[j].Medium {
			return unknown[i].Medium < unknown[j].Medium
		}
		return unknown[i].Key < unknown[j].Key
	})
	return unknown, unlisted
}

// namespaceFor is the key prefix a set's objects live under on a medium, with
// no trailing slash: the leading segments of transport.MediumKey.
func namespaceFor(medium transport.Medium, set model.BackupSetID) string {
	parts := make([]string, 0, 3)
	if medium.Prefix != "" {
		parts = append(parts, medium.Prefix)
	}
	parts = append(parts, set.Source, set.Set)
	return strings.Join(parts, "/")
}
