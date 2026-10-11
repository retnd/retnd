package main

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/apps/common/platform/capabilities"
	"github.com/retnd/retnd/core/service"
)

type recordingEnder struct {
	mu   sync.Mutex
	seen []service.RecoverySession
}

func (r *recordingEnder) EndRecoverySession(s service.RecoverySession) {
	r.mu.Lock()
	r.seen = append(r.seen, s)
	r.mu.Unlock()
}

func (r *recordingEnder) sessions() []service.RecoverySession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]service.RecoverySession(nil), r.seen...)
}

// EPIC Q panel finding C6 (#976): a logout through the real handler reaches
// the backup service, with the session in the exact shape a grant was bound
// to when it was minted from the same cookie.
func TestLogoutRevokesTheRecoveryGrantsOfThatSession(t *testing.T) {
	store := filepath.Join(t.TempDir(), "auth.json")
	if _, err := local.CreateAdmin(local.CreateAdminConfig{StorePath: store, Username: "bm-admin", Password: "correct-horse-battery"}); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	svc, err := local.New(local.Config{StorePath: store, Notice: io.Discard, Log: io.Discard})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	ender := &recordingEnder{}
	endRecoverySessionsOnLogout(svc, ender)

	mux := http.NewServeMux()
	mux.Handle("/api/v1/auth/", http.StripPrefix("/api/v1/auth", svc.Handler()))
	srv := httptest.NewServer(local.EnsureCSRFCookie(false)(mux))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base, _ := url.Parse(srv.URL)

	if r, err := client.Get(srv.URL + "/api/v1/auth/session"); err != nil {
		t.Fatal(err)
	} else {
		r.Body.Close()
	}
	cookieOf := func(name string) string {
		for _, c := range jar.Cookies(base) {
			if c.Name == name {
				return c.Value
			}
		}
		return ""
	}
	post := func(path, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(local.CSRFHeaderName, cookieOf(local.CSRFCookieName))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post("/api/v1/auth/login", `{"username":"bm-admin","password":"correct-horse-battery"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login status %d", resp.StatusCode)
	}

	cookie := local.SessionCookieName + "=" + cookieOf(local.SessionCookieName)
	authCtx, err := svc.Authenticator().Authenticate(context.Background(), capabilities.AuthRequest{Headers: map[string][]string{"Cookie": {cookie}}})
	if err != nil || !authCtx.Authenticated {
		t.Fatalf("setup: %+v %v", authCtx, err)
	}
	exp, err := time.Parse(time.RFC3339, authCtx.SessionExpiresAt)
	if err != nil {
		t.Fatalf("setup: SessionExpiresAt %q: %v", authCtx.SessionExpiresAt, err)
	}

	resp := post("/api/v1/auth/logout", `{}`)
	got := ender.sessions()
	if len(got) != 1 {
		t.Fatalf("logout ended %d recovery sessions, want 1 (status %d)", len(got), resp.StatusCode)
	}
	if got[0].Actor != "bm-admin" || !got[0].Expires.Equal(exp) {
		t.Errorf("logout ended session %+v, want {bm-admin %s}: a different pair would match no grant", got[0], exp)
	}
}
