package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/capabilities"
	"github.com/retnd/retnd/core/service"
)

// Issue #1179: GET /api/v1/system/host-directories, the read behind the
// folder browser on "Folder under the staging root on the host". It is a server-side read of
// the host's file system, so the route tier is part of what is held here: a
// session is required, it is read-only (no write verb exists), and it does
// not stand behind the destructive gate, which is shut on every deployment
// this repository builds.

type fakeBrowseBackend struct {
	*syncFakeBackend
	mu    sync.Mutex
	asked []string
	dir   service.HostDirectory
	err   error
}

func (f *fakeBrowseBackend) BrowseHostDirectory(_ context.Context, rel string) (service.HostDirectory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, rel)
	return f.dir, f.err
}

func browseRouter(t *testing.T, auth capabilities.Authenticator, gate DestructiveGate) (http.Handler, *fakeBrowseBackend) {
	t.Helper()
	be := &fakeBrowseBackend{syncFakeBackend: newSyncFakeBackend()}
	parent := ""
	be.dir = service.HostDirectory{
		Root: "/srv/staging", Path: "alpha", Parent: &parent,
		Entries: []service.HostDirectoryEntry{{Name: "inner", Path: "alpha/inner"}},
	}
	return NewRouter(RouterConfig{
		Platform: fakePlatformAdapter{caps: capabilities.PlatformCapabilities{NativeAuth: true}, auth: auth},
		Backend:  be, Gate: gate, BinaryVersion: "t", Commit: "t",
	}), be
}

const browsePath = "/api/v1/system/host-directories"

func signedIn() capabilities.Authenticator {
	return fakeAuthenticator{authenticated: true, username: "alice"}
}

func TestHostDirectories_1179_NeedsASession(t *testing.T) {
	router, be := browseRouter(t, fakeAuthenticator{authenticated: false}, NotYetImplementedGate{})
	rec := doGet(t, router, browsePath)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if len(be.asked) != 0 {
		t.Error("an unauthenticated request reached the backend")
	}
}

func TestHostDirectories_1179_AnswersTheListingUnderTheShutGate(t *testing.T) {
	router, be := browseRouter(t, signedIn(), NotYetImplementedGate{})
	rec := doGet(t, router, browsePath+"?path="+url.QueryEscape("alpha"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a read is not behind the destructive gate): %s", rec.Code, rec.Body.String())
	}
	if len(be.asked) != 1 || be.asked[0] != "alpha" {
		t.Errorf("backend was asked %v, want [alpha]", be.asked)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["root"] != "/srv/staging" || body["path"] != "alpha" || body["parent"] != "" || body["truncated"] != false {
		t.Errorf("body = %v", body)
	}
	entries, _ := body["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v", body["entries"])
	}
	first, _ := entries[0].(map[string]any)
	if first["name"] != "inner" || first["path"] != "alpha/inner" {
		t.Errorf("entry = %v", first)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store: a directory listing is not for a shared cache", cc)
	}
}

func TestHostDirectories_1179_TheRootHasNoParent(t *testing.T) {
	router, be := browseRouter(t, signedIn(), NotYetImplementedGate{})
	be.dir = service.HostDirectory{Root: "/srv/staging"}
	rec := doGet(t, router, browsePath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if v, ok := body["parent"]; !ok || v != nil {
		t.Errorf("parent = %v (present=%v), want an explicit null at the root", v, ok)
	}
	if e, ok := body["entries"].([]any); !ok || len(e) != 0 {
		t.Errorf("entries = %v, want an empty array and not null", body["entries"])
	}
}

func TestHostDirectories_1179_RefusalsHaveTheirOwnStatusAndNeverCarryAPath(t *testing.T) {
	leak := "/srv/staging/secret-place"
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: %s", service.ErrHostBrowseInvalid, leak), http.StatusBadRequest, "INVALID_REQUEST"},
		{fmt.Errorf("%w: %s", service.ErrHostBrowseNotFound, leak), http.StatusBadRequest, "INVALID_REQUEST"},
		{fmt.Errorf("%w: %s", service.ErrHostBrowseUnavailable, leak), http.StatusServiceUnavailable, "RECOVERY_UNAVAILABLE"},
		{errors.New("open " + leak + ": permission denied"), http.StatusInternalServerError, "INTERNAL"},
	} {
		router, be := browseRouter(t, signedIn(), NotYetImplementedGate{})
		be.err = tc.err
		rec := doGet(t, router, browsePath+"?path=x")
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%v: status %d body %s, want %d %s", tc.err, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("%v: the response carries the host path: %s", tc.err, rec.Body.String())
		}
	}
}

func TestHostDirectories_1179_AnOverlongOrBinaryNameNeverReachesTheBackend(t *testing.T) {
	router, be := browseRouter(t, signedIn(), NotYetImplementedGate{})
	for _, q := range []string{strings.Repeat("a", 5000), "a%00b", "a%0ab"} {
		rec := doGet(t, router, browsePath+"?path="+q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%.20q: status %d, want 400", q, rec.Code)
		}
	}
	if len(be.asked) != 0 {
		t.Errorf("backend was asked %v", be.asked)
	}
}

func TestHostDirectories_1179_OnlyReadsExist(t *testing.T) {
	router, be := browseRouter(t, signedIn(), NotYetImplementedGate{})
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(m, browsePath, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		attachValidCSRF(req)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 405 or 404: this resource has no write verb", m, rec.Code)
		}
	}
	if len(be.asked) != 0 {
		t.Errorf("a write verb reached the browser: %v", be.asked)
	}
}

func TestHostDirectories_1179_ABackendWithoutItIs501(t *testing.T) {
	router := NewRouter(RouterConfig{
		Platform: fakePlatformAdapter{caps: capabilities.PlatformCapabilities{NativeAuth: true}, auth: signedIn()},
		Backend:  newSyncFakeBackend(), Gate: NotYetImplementedGate{}, BinaryVersion: "t", Commit: "t",
	})
	rec := doGet(t, router, browsePath)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 (the route exists; this backend cannot serve it)", rec.Code)
	}
}
