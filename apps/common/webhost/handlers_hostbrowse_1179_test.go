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
	"time"

	"github.com/retnd/retnd/apps/common/platform/capabilities"
	"github.com/retnd/retnd/core/service"
)

// Issue #1179: GET /api/v1/system/staging-directories, the read behind the
// staging folder browser. It is a server-side read of the host's file system, so the route tier is part of what is held here: a
// session is required, it is read-only (no write verb exists), and it does
// not stand behind the destructive gate, which is shut on every deployment
// this repository builds.

type fakeBrowseBackend struct {
	*syncFakeBackend
	mu    sync.Mutex
	asked []string
	dir   service.StagingDirectory
	err   error
	// deadline is how long the handler gave the last listing, zero for none.
	deadline time.Duration
}

func (f *fakeBrowseBackend) ListStagingDirectory(ctx context.Context, rel string) (service.StagingDirectory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, rel)
	f.deadline = 0
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	return f.dir, f.err
}

func browseRouter(t *testing.T, auth capabilities.Authenticator, gate DestructiveGate) (http.Handler, *fakeBrowseBackend) {
	t.Helper()
	be := &fakeBrowseBackend{syncFakeBackend: newSyncFakeBackend()}
	parent := ""
	be.dir = service.StagingDirectory{
		Root: "/srv/staging", Path: "alpha", Parent: &parent,
		Entries: []service.StagingDirectoryEntry{{Name: "inner", Path: "alpha/inner"}},
	}
	return NewRouter(RouterConfig{
		Platform: fakePlatformAdapter{caps: capabilities.PlatformCapabilities{NativeAuth: true}, auth: auth},
		Backend:  be, Gate: gate, BinaryVersion: "t", Commit: "t",
	}), be
}

const browsePath = "/api/v1/system/staging-directories"

func signedIn() capabilities.Authenticator {
	return fakeAuthenticator{authenticated: true, username: "alice"}
}

func TestStagingDirectories_1179_NeedsASession(t *testing.T) {
	router, be := browseRouter(t, fakeAuthenticator{authenticated: false}, NotYetImplementedGate{})
	rec := doGet(t, router, browsePath)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if len(be.asked) != 0 {
		t.Error("an unauthenticated request reached the backend")
	}
}

func TestStagingDirectories_1179_AnswersTheListingUnderTheShutGate(t *testing.T) {
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

func TestStagingDirectories_1179_TheRootHasNoParent(t *testing.T) {
	router, be := browseRouter(t, signedIn(), NotYetImplementedGate{})
	be.dir = service.StagingDirectory{Root: "/srv/staging"}
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

func TestStagingDirectories_1179_RefusalsHaveTheirOwnStatusAndNeverCarryAPath(t *testing.T) {
	leak := "/srv/staging/secret-place"
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: %s", service.ErrStagingBrowseInvalid, leak), http.StatusBadRequest, "INVALID_REQUEST"},
		// #1185 review: a folder that is not there is the not-found class,
		// and a folder the host may not read is not a malformed name.
		{fmt.Errorf("%w: %s", service.ErrStagingBrowseNotFound, leak), http.StatusNotFound, "DIRECTORY_NOT_FOUND"},
		{fmt.Errorf("%w: %s", service.ErrStagingBrowseNotReadable, leak), http.StatusConflict, "DIRECTORY_NOT_READABLE"},
		{fmt.Errorf("%w: %s", service.ErrStagingBrowseUnavailable, leak), http.StatusServiceUnavailable, "RECOVERY_UNAVAILABLE"},
		{fmt.Errorf("listing %s: %w", leak, context.DeadlineExceeded), http.StatusServiceUnavailable, "RECOVERY_UNAVAILABLE"},
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

// The name is checked in one place, the service, so the handler passes it on
// as it came and answers the service's refusal (#1185 review). The service's
// own tests and fuzz targets hold what a refusal is.
func TestStagingDirectories_1179_TheNameIsCheckedByTheServiceAlone(t *testing.T) {
	router, be := browseRouter(t, signedIn(), NotYetImplementedGate{})
	be.err = service.ErrStagingBrowseInvalid
	for _, q := range []string{strings.Repeat("a", 5000), "a%00b", "a%0ab"} {
		rec := doGet(t, router, browsePath+"?path="+q)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"INVALID_REQUEST"`) {
			t.Errorf("%.20q: status %d body %s, want 400 INVALID_REQUEST", q, rec.Code, rec.Body.String())
		}
	}
	if len(be.asked) != 3 {
		t.Errorf("backend was asked %d times, want 3: the handler keeps no second copy of the rules", len(be.asked))
	}
}

// A listing on a slow mount must not hold a request open for as long as the
// mount likes: the handler gives the service a deadline.
func TestStagingDirectories_1179_TheListingRunsUnderADeadline(t *testing.T) {
	router, be := browseRouter(t, signedIn(), NotYetImplementedGate{})
	rec := doGet(t, router, browsePath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if be.deadline <= 0 || be.deadline > stagingListTimeout {
		t.Errorf("the listing ran with %v left, want a deadline no later than %v", be.deadline, stagingListTimeout)
	}
}

func TestStagingDirectories_1179_OnlyReadsExist(t *testing.T) {
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

func TestStagingDirectories_1179_ABackendWithoutItIs501(t *testing.T) {
	router := NewRouter(RouterConfig{
		Platform: fakePlatformAdapter{caps: capabilities.PlatformCapabilities{NativeAuth: true}, auth: signedIn()},
		Backend:  newSyncFakeBackend(), Gate: NotYetImplementedGate{}, BinaryVersion: "t", Commit: "t",
	})
	rec := doGet(t, router, browsePath)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 (the route exists; this backend cannot serve it)", rec.Code)
	}
}
