package webhost

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/retnd/retnd/core/service"
)

// Issue #1179: GET /api/v1/system/host-directories, the folder listing behind
// "Folder under the staging root on the host".
//
// # What this route is, and is not
//
// It is a read of the machine the engine runs on, which is why it is held to
// more than an ordinary read. The service lists only the folders below the
// recovery staging root, never follows a symbolic link, and refuses a name
// that climbs out (core/service/hostbrowse.go has the whole argument). This
// file adds the three things that belong to the HTTP layer:
//
//   - It needs a session, like everything under /api/v1.
//   - It has no write verb. Nothing is registered but the GET, so a POST,
//     PUT, PATCH or DELETE is the router's own 405.
//   - It stands outside the destructive gate and outside CSRF. It reads, it
//     deletes nothing and overwrites nothing, so it is in the same tier as
//     GET /system/storage, and the gate (shut on every deployment this
//     repository builds) is not the reason a person can or cannot browse.
//
// The response carries names relative to the root and the root itself, which
// GET /settings/assurance already reports. A refusal never carries a path:
// the service's sentences name none, and an unclassified error is logged and
// answered with this package's own words.

// HostBrowseClient is what the route needs from the backend. It is separate
// from BackupServiceClient for the reason RecoveryClient is: a backend that
// cannot browse is a 501 on this one route and not a compile error in every
// fake.
type HostBrowseClient interface {
	BrowseHostDirectory(ctx context.Context, rel string) (service.HostDirectory, error)
}

var _ HostBrowseClient = (*service.BackupService)(nil)

// maxHostBrowseNameBytes bounds the query value before the backend sees it.
const maxHostBrowseNameBytes = 4096

type hostDirectoryEntryBody struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type hostDirectoryResponse struct {
	Root string `json:"root"`
	Path string `json:"path"`
	// Parent is null at the root: "" is a real answer there (the root is the
	// parent of a first-level folder), so the two must stay distinguishable.
	Parent    *string                  `json:"parent"`
	Entries   []hostDirectoryEntryBody `json:"entries"`
	Truncated bool                     `json:"truncated"`
}

func (h *handlers) listHostDirectory(w http.ResponseWriter, r *http.Request) {
	hb, ok := h.backend.(HostBrowseClient)
	if !ok {
		writeError(w, http.StatusNotImplemented, "RECOVERY_UNAVAILABLE", "this deployment cannot list folders on the host")
		return
	}
	rel := r.URL.Query().Get("path")
	if len(rel) > maxHostBrowseNameBytes || strings.ContainsAny(rel, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", service.ErrHostBrowseInvalid.Error())
		return
	}
	dir, err := hb.BrowseHostDirectory(r.Context(), rel)
	switch {
	case err == nil:
	case errors.Is(err, service.ErrHostBrowseInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", service.ErrHostBrowseInvalid.Error())
		return
	case errors.Is(err, service.ErrHostBrowseNotFound):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", service.ErrHostBrowseNotFound.Error())
		return
	case errors.Is(err, service.ErrHostBrowseUnavailable):
		writeError(w, http.StatusServiceUnavailable, "RECOVERY_UNAVAILABLE", service.ErrHostBrowseUnavailable.Error())
		return
	default:
		h.internalError(w, r, "INTERNAL", "could not list that folder", err)
		return
	}
	resp := hostDirectoryResponse{Root: dir.Root, Path: dir.Path, Parent: dir.Parent, Truncated: dir.Truncated, Entries: []hostDirectoryEntryBody{}}
	for _, e := range dir.Entries {
		resp.Entries = append(resp.Entries, hostDirectoryEntryBody{Name: e.Name, Path: e.Path})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}
