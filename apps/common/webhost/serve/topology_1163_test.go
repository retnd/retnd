package serve_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/apps/common/webhost"
	"github.com/retnd/retnd/apps/common/webhost/serve"
	"github.com/retnd/retnd/core/service"
)

// Issue #1163's transport rule, on every topology retnd ships.
//
// Every shipped deployment puts serve-ui in front of the engine, and the
// engine only ever sees serve-ui as its peer: over loopback in the Synology
// package (the engine listens on 127.0.0.1, forwarded headers not trusted),
// and over a container network everywhere else, with forwarded headers
// trusted on the compose profiles and not on Unraid. None of those peers says
// anything about the browser. So each topology is built here as it ships,
// serve-ui proxying to a real engine whose peer address is what that
// deployment's engine would see, and the browser's own connection is varied in
// front of serve-ui.
//
// The table comes from distribution/packaging/canonical.json, the file
// distribution/packaging/canonical.go loads, so a platform added there is a
// platform tested here. The .spk is not in that file (its row is the
// Container Manager path); its topology is the extra row below, and
// apps/synology/spk pins the script it describes.

type shippedTopology struct {
	name       string
	profile    string
	trustFwd   bool
	enginePeer string
}

func shippedTopologies(t *testing.T) []shippedTopology {
	t.Helper()
	path := filepath.Join(repoRootFrom(t), "distribution", "packaging", "canonical.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Platforms map[string]struct {
			Profile               string `json:"profile"`
			TrustForwardedHeaders bool   `json:"trustForwardedHeaders"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(doc.Platforms) == 0 {
		t.Fatalf("%s lists no platforms", path)
	}
	var out []shippedTopology
	for name, p := range doc.Platforms {
		// Every canonical platform runs the two containers on a container
		// network, so the engine's peer is serve-ui's container address.
		out = append(out, shippedTopology{name: name, profile: p.Profile, trustFwd: p.TrustForwardedHeaders, enginePeer: "172.18.0.3:41000"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	// apps/synology/spk/assets/scripts/start-stop-status: the engine on
	// 127.0.0.1:8478 without --trust-forwarded-headers, serve-ui on the LAN
	// port proxying to it.
	return append(out, shippedTopology{name: "synology-spk", profile: "synology", trustFwd: false, enginePeer: "127.0.0.1:41000"})
}

func repoRootFrom(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work above the test's directory")
		}
		dir = parent
	}
}

// topologyStack is serve-ui in front of a real engine.
type topologyStack struct {
	ui      http.Handler
	engine  *url.URL
	cookies []*http.Cookie
	csrf    string
}

func newTopologyStack(t *testing.T, top shippedTopology) *topologyStack {
	t.Helper()
	backend, cleanup, err := service.Open(context.Background(), writeTestConfig(t))
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	authSvc, err := local.New(local.Config{StorePath: filepath.Join(t.TempDir(), "auth.json"), TrustForwardedHeaders: top.trustFwd})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	p := profileFor(t, top.profile)
	adapter, err := p.Adapter(profile.AdapterConfig{LocalAuth: authSvc.Authenticator()})
	if err != nil {
		t.Fatalf("Adapter: %v", err)
	}
	engine := serve.NewEngine(serve.EngineConfig{
		Platform: adapter, Backend: backend, Gate: webhost.NewProfileGate(p.ID), StepUp: authSvc,
		AuthRoutes: authSvc.Handler(), TrustForwardedHeaders: top.trustFwd, BinaryVersion: "test", Commit: "test",
	})
	// What this deployment's engine sees as its peer: serve-ui, at the
	// address that topology gives it.
	peer := top.enginePeer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = peer
		engine.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	engineURL, _ := url.Parse(srv.URL)

	// Sign in at the engine; the cookies then ride every browser request
	// sent to serve-ui.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	enrollAndLogIn(t, &engineHarness{auth: authSvc}, client, srv.URL)
	s := &topologyStack{
		ui:      serve.NewUI(serve.UIConfig{Upstream: engineURL, StaticFS: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}}),
		engine:  engineURL,
		cookies: jar.Cookies(engineURL),
	}
	for _, c := range s.cookies {
		if c.Name == local.CSRFCookieName {
			s.csrf = c.Value
		}
	}
	if s.csrf == "" {
		t.Fatal("no CSRF cookie after signing in")
	}
	return s
}

type browser struct {
	addr    string
	tls     bool
	headers map[string]string
}

func (s *topologyStack) send(t *testing.T, b browser, method, path, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://nas.lan:8477"+path, rd)
	req.RemoteAddr = b.addr
	if b.tls {
		req.TLS = &tls.ConnectionState{}
	}
	for _, c := range s.cookies {
		req.AddCookie(c)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "topology-1163-"+b.addr)
		req.Header.Set(local.CSRFHeaderName, s.csrf)
	}
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ui.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func (s *topologyStack) ackRequired(t *testing.T, b browser) bool {
	t.Helper()
	status, body := s.send(t, b, http.MethodGet, "/api/v1/system/capabilities", "")
	if status != http.StatusOK {
		t.Fatalf("capabilities through serve-ui: %d %s", status, body)
	}
	var caps struct {
		D struct {
			Ack bool `json:"insecure_transport_ack_required"`
		} `json:"destructive_operations"`
	}
	if err := json.Unmarshal(body, &caps); err != nil {
		t.Fatal(err)
	}
	return caps.D.Ack
}

func (s *topologyStack) runRefusedForTransport(t *testing.T, b browser) bool {
	t.Helper()
	_, body := s.send(t, b, http.MethodPost, "/api/v1/operations", `{"action":"run_cycle","config_revision":"x"}`)
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	return env.Error.Code == "INSECURE_TRANSPORT_ACK_REQUIRED"
}

func TestTransport1163_EveryShippedTopologyAsksExactlyWhenTheBrowserIsOnPlainHTTPFromElsewhere(t *testing.T) {
	lan := browser{addr: "192.168.1.50:51000"}
	for _, top := range shippedTopologies(t) {
		t.Run(top.name, func(t *testing.T) {
			s := newTopologyStack(t, top)
			for _, tc := range []struct {
				name string
				b    browser
				want bool
			}{
				{"a browser on the LAN over plain HTTP", lan, true},
				{"a browser on the NAS itself", browser{addr: "127.0.0.1:51000"}, false},
				{"a browser over TLS", browser{addr: "192.168.1.50:51000", tls: true}, false},
				{"a LAN browser forging the verdict and the forwarding headers", browser{addr: "192.168.1.50:51000", headers: map[string]string{
					"X-Retnd-Browser-Transport": "https", "X-Forwarded-Proto": "https", "X-Forwarded-For": "127.0.0.1",
				}}, true},
			} {
				if got := s.ackRequired(t, tc.b); got != tc.want {
					t.Errorf("%s: capabilities say ack required = %v, want %v", tc.name, got, tc.want)
				}
				if got := s.runRefusedForTransport(t, tc.b); got != tc.want {
					t.Errorf("%s: run_cycle refused for transport = %v, want %v", tc.name, got, tc.want)
				}
			}
			acked := lan
			acked.headers = map[string]string{webhost.InsecureTransportHeader: webhost.InsecureTransportAcknowledged}
			if s.runRefusedForTransport(t, acked) {
				t.Error("an acknowledged run from the LAN was still refused for transport")
			}
		})
	}
}
