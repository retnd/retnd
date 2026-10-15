package webhost

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/retnd/retnd/core/service"
)

// Issue #1179: GET /api/v1/system/staging-directories, the folder listing behind
// "Folder under the staging root on the host".
//
// # What this route is, and is not
//
// It is a read of the machine the engine runs on, which is why it is held to
// more than an ordinary read. It is named for the one root it lists (#1185
// review), so nobody is tempted to grow a ?root= on it. The service lists only
// the folders below the recovery staging root, never leaves it or lists
// through a symbolic link, refuses a name that climbs out, and bounds the
// work one listing does (core/service/hostbrowse.go has the whole argument).
// This file adds the four things that belong to the HTTP layer:
//
//   - It needs a session, like everything under /api/v1.
//   - It has no write verb. Nothing is registered but the GET, so a POST,
//     PUT, PATCH or DELETE is the router's own 405.
//   - It runs under a deadline (stagingListTimeout).
//   - It stands outside the destructive gate and outside CSRF. It reads, it
//     deletes nothing and overwrites nothing, so it is in the same tier as
//     GET /system/storage, and the gate (shut on every deployment this
//     repository builds) is not the reason a person can or cannot browse.
//
// The response carries names relative to the root and the root itself, which
// GET /settings/assurance already reports. A refusal never carries a path:
// the service's sentences name none, and an unclassified error is logged and
// answered with this package's own words.

// StagingBrowseClient is what the route needs from the backend. It is separate
// from BackupServiceClient for the reason RecoveryClient is: a backend that
// cannot browse is a 501 on this one route and not a compile error in every
// fake.
type StagingBrowseClient interface {
	ListStagingDirectory(ctx context.Context, rel string) (service.StagingDirectory, error)
}

var _ StagingBrowseClient = (*service.BackupService)(nil)

// stagingListTimeout bounds one listing. The service reads in batches and
// stops at a scan budget, and this is the wall-clock half of the same bound:
// a staging root can be a slow network mount, and a GET anyone with a session
// can repeat must not hold a request open for as long as the mount likes.
const stagingListTimeout = 10 * time.Second

type stagingDirectoryEntryBody struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type stagingDirectoryResponse struct {
	Root string `json:"root"`
	Path string `json:"path"`
	// Parent is null at the root: "" is a real answer there (the root is the
	// parent of a first-level folder), so the two must stay distinguishable.
	Parent    *string                     `json:"parent"`
	Entries   []stagingDirectoryEntryBody `json:"entries"`
	Truncated bool                        `json:"truncated"`
}

func (h *handlers) listStagingDirectories(w http.ResponseWriter, r *http.Request) {
	hb, ok := h.backend.(StagingBrowseClient)
	if !ok {
		writeError(w, http.StatusNotImplemented, "RECOVERY_UNAVAILABLE", "this deployment cannot list folders on the host")
		return
	}
	// The name is checked by the service alone (one copy of the rules), so
	// it goes on as it came.
	rel := r.URL.Query().Get("path")
	ctx, cancel := context.WithTimeout(r.Context(), stagingListTimeout)
	defer cancel()
	dir, err := hb.ListStagingDirectory(ctx, rel)
	switch {
	case err == nil:
	case errors.Is(err, service.ErrStagingBrowseInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", service.ErrStagingBrowseInvalid.Error())
		return
	case errors.Is(err, service.ErrStagingBrowseNotFound):
		// A well-formed name that is not a folder (any more): the
		// not-found class, so a page can step back up a level.
		writeError(w, http.StatusNotFound, "DIRECTORY_NOT_FOUND", service.ErrStagingBrowseNotFound.Error())
		return
	case errors.Is(err, service.ErrStagingBrowseNotReadable):
		writeError(w, http.StatusConflict, "DIRECTORY_NOT_READABLE", service.ErrStagingBrowseNotReadable.Error())
		return
	case errors.Is(err, service.ErrStagingBrowseUnavailable):
		writeError(w, http.StatusServiceUnavailable, "RECOVERY_UNAVAILABLE", service.ErrStagingBrowseUnavailable.Error())
		return
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusServiceUnavailable, "RECOVERY_UNAVAILABLE", "the host took too long to list that folder")
		return
	default:
		h.internalError(w, r, "INTERNAL", "could not list that folder", err)
		return
	}
	resp := stagingDirectoryResponse{Root: dir.Root, Path: dir.Path, Parent: dir.Parent, Truncated: dir.Truncated, Entries: []stagingDirectoryEntryBody{}}
	for _, e := range dir.Entries {
		resp.Entries = append(resp.Entries, stagingDirectoryEntryBody{Name: e.Name, Path: e.Path})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}
