package serve_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/apps/common/webhost/serve"
	"github.com/retnd/retnd/core/service"
)

// #1568: GET /metrics through the compositions this package ships, not
// through webhost.NewRouter on its own.
//
// #899 registered the route on the router and tested it there, and the
// engine's serve mux never mounted it, so the real binary answered 404 for a
// scrape the router tests said worked. These drive NewEngine and NewUI
// exactly as retnd-web's serve and serve-ui build them, over a real service
// opened from a real config file with the metrics block on, holding a
// credential minted by that service.

const metricsBlock1568 = "observability:\n  metrics:\n    enabled: true\n    min_rebuild_interval: 1s\n"

type metricsEngine1568 struct {
	server *httptest.Server
	token  string
}

func newMetricsEngine1568(t *testing.T) *metricsEngine1568 {
	t.Helper()
	configPath := writeTestConfig(t)
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(metricsBlock1568); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	backend, cleanup, err := service.Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	minted, err := backend.MintScrapeToken(context.Background())
	if err != nil {
		t.Fatalf("MintScrapeToken: %v", err)
	}

	authSvc, err := local.New(local.Config{StorePath: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	srv := httptest.NewServer(serve.NewEngine(serve.EngineConfig{
		Platform:              testPlatformAdapter{auth: authSvc},
		AuthRoutes:            authSvc.Handler(),
		TrustForwardedHeaders: authSvc.TrustForwardedHeaders(),
		Backend:               backend,
		BinaryVersion:         "test",
		Commit:                "testcommit",
	}))
	t.Cleanup(srv.Close)

	return &metricsEngine1568{server: srv, token: minted.Token}
}

func scrape1568(t *testing.T, method, target, authorization string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, resp.Header, string(body)
}

func TestEngine_1568_MetricsWithoutACredentialIs401(t *testing.T) {
	e := newMetricsEngine1568(t)

	for _, auth := range []string{"", "Bearer rsc_not-the-minted-one", "Basic cHJvbWV0aGV1czp3cm9uZw=="} {
		code, _, body := scrape1568(t, http.MethodGet, e.server.URL+"/metrics", auth)
		if code != http.StatusUnauthorized {
			t.Errorf("GET /metrics on the engine with Authorization %q = %d, want 401 (the scrape endpoint refusing a missing or wrong credential); body=%q", auth, code, body)
		}
		if body != "" {
			t.Errorf("GET /metrics with Authorization %q: a refusal carries an empty body, got %q", auth, body)
		}
	}
}

func TestEngine_1568_MetricsWithTheMintedCredentialIsTheExposition(t *testing.T) {
	e := newMetricsEngine1568(t)

	for name, auth := range map[string]string{
		"bearer":     "Bearer " + e.token,
		"basic_auth": "Basic " + base64.StdEncoding.EncodeToString([]byte("prometheus:"+e.token)),
	} {
		code, header, body := scrape1568(t, http.MethodGet, e.server.URL+"/metrics", auth)
		if code != http.StatusOK {
			t.Errorf("%s: GET /metrics on the engine with the minted credential = %d, want 200; body=%q", name, code, body)
			continue
		}
		if ct := header.Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q, want the 0.0.4 exposition type", name, ct)
		}
		if !strings.Contains(body, "\nretnd_process_info{") && !strings.HasPrefix(body, "retnd_process_info{") {
			t.Errorf("%s: the body carries no retnd_process_info sample, so it is not this product's exposition:\n%s", name, body)
		}
	}

	// The wrong method still reaches the scrape handler (counted, not a bare
	// 405) through the engine's mount, which is a mux pattern with no method.
	if code, _, _ := scrape1568(t, http.MethodPost, e.server.URL+"/metrics", "Bearer "+e.token); code == http.StatusNotFound {
		t.Errorf("POST /metrics on the engine = 404; the mount must hand every method to the router, which answers it")
	}
}

// ADR 0035 decision 5: the published web-ui port never forwards /metrics, and
// a scraper pointed at it is told 404, not handed the UI's index.html with a
// 200 it would then fail to parse.
func TestUI_1568_MetricsIsNotForwardedAndAnswers404(t *testing.T) {
	e := newMetricsEngine1568(t)
	upstream, err := url.Parse(e.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html><body>generic retnd UI shell</body></html>")}}
	ui := httptest.NewServer(serve.NewUI(serve.UIConfig{Upstream: upstream, StaticFS: staticFS}))
	t.Cleanup(ui.Close)

	// The engine answers it, so a 404 from the UI is the UI declining to
	// forward, not the engine having nothing there.
	if code, _, _ := scrape1568(t, http.MethodGet, e.server.URL+"/metrics", "Bearer "+e.token); code != http.StatusOK {
		t.Fatalf("precondition: the engine itself answers GET /metrics with %d, want 200", code)
	}

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
		code, _, body := scrape1568(t, method, ui.URL+"/metrics", "Bearer "+e.token)
		if code != http.StatusNotFound {
			t.Errorf("%s /metrics on the UI = %d, want 404; body=%q", method, code, body)
		}
		if strings.Contains(body, "retnd_") {
			t.Errorf("%s /metrics on the UI carried exposition; web-ui must never forward the scrape endpoint", method)
		}
		if strings.Contains(body, "UI shell") {
			t.Errorf("%s /metrics on the UI served the static bundle; a scraper there must get a plain 404", method)
		}
	}

	// The SPA fallback still answers every other client-side route.
	if code, _, body := scrape1568(t, http.MethodGet, ui.URL+"/settings", ""); code != http.StatusOK || !strings.Contains(body, "UI shell") {
		t.Errorf("GET /settings on the UI = %d %q, want the static bundle", code, body)
	}
}
