package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/apps/common/platform/capabilities"
	"github.com/retnd/retnd/apps/common/webhost"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/service"
)

// #1568: the mount guard and the root routes that live outside /api/v1.
//
// The engine's mux is where a root route becomes reachable, so the guard has
// to know which root routes are deliberate. It reads the one list,
// cliecho.RootRoutesOutsideAPI, the same one every route guard in
// apps/common/webhost reads, rather than carrying an exemption of its own.

// The engine composition mounts /metrics, with the API router, so the route
// the router registers is one the real binary serves.
func TestEngineMounts_1568_MountMetricsWithTheAPIRouter(t *testing.T) {
	api, auth := &sentinel{"api"}, &sentinel{"auth"}
	for _, withAuth := range []http.Handler{auth, nil} {
		engine := engineMounts(api, withAuth)
		i := slices.IndexFunc(engine, func(m mount) bool { return m.pattern == "/metrics" })
		if i < 0 {
			t.Fatalf("the engine composition (auth routes %v) does not mount /metrics, so GET /metrics answers 404 on the real engine however the router registers it", withAuth != nil)
		}
		if m := engine[i]; m.kind != mountAPIRouter || !sameHandler(m.handler, api) || m.strip != "" {
			t.Errorf("the engine mounts /metrics as %+v, want the API router itself, unstripped, which is what registers GET /metrics and what the route guard walks", m)
		}
	}
	if _, ok := cliecho.RootRoutesOutsideAPI()["GET /metrics"]; !ok {
		t.Fatal("GET /metrics is not in cliecho.RootRoutesOutsideAPI, so this test is not checking the list the guard reads")
	}
}

// ADR 0035 decision 5: the UI composition never forwards /metrics, and answers
// it 404 itself rather than letting the static bundle's fallback hand a
// scraper index.html with a 200.
func TestUIMounts_1568_DoNotForwardMetrics(t *testing.T) {
	proxy, downloads, static := &sentinel{"proxy"}, &sentinel{"downloads"}, &sentinel{"static"}
	ui := uiMounts(proxy, downloads, static)
	i := slices.IndexFunc(ui, func(m mount) bool { return m.pattern == "/metrics" })
	if i < 0 {
		t.Fatal("the UI composition has no mount for /metrics, so its static fallback answers a scraper with index.html and a 200")
	}
	if m := ui[i]; sameHandler(m.handler, proxy) || sameHandler(m.handler, downloads) || sameHandler(m.handler, static) {
		t.Errorf("the UI answers /metrics with %v; it must be neither forwarded to the engine (ADR 0035 decision 5) nor the static bundle", m.handler)
	}
}

// Each mutation is the real lists with one thing wrong about the root routes,
// and the guard has to say what, not merely object to something else.
func TestMountGuard_1568_RootRouteMutationsAreCaught(t *testing.T) {
	api, auth := &sentinel{"api"}, &sentinel{"auth"}
	proxy, downloads, static := &sentinel{"proxy"}, &sentinel{"downloads"}, &sentinel{"static"}
	other := &sentinel{"other"}
	engine := engineMounts(api, auth)
	ui := uiMounts(proxy, downloads, static)
	withMetrics := func(ms []mount) []mount {
		if slices.ContainsFunc(ms, func(m mount) bool { return m.pattern == "/metrics" }) {
			return slices.Clone(ms)
		}
		return append(slices.Clone(ms), mount{pattern: "/metrics", handler: api, kind: mountAPIRouter})
	}
	without := func(ms []mount, pattern string) []mount {
		return slices.DeleteFunc(slices.Clone(ms), func(m mount) bool { return m.pattern == pattern })
	}
	replace := func(ms []mount, with mount) []mount {
		return append(without(ms, with.pattern), with)
	}

	for _, tc := range []struct {
		name       string
		engine, ui []mount
		want       string
	}{
		{"the engine mounting the router at a root path the list does not name",
			append(withMetrics(engine), mount{pattern: "/debug/vars", handler: api, kind: mountAPIRouter}), ui,
			"RootRoutesOutsideAPI"},
		{"the engine mounting a root subtree under a listed route",
			append(withMetrics(engine), mount{pattern: "/metrics/", handler: api, kind: mountAPIRouter}), ui,
			"RootRoutesOutsideAPI"},
		{"the engine answering /metrics with something other than the API router",
			replace(withMetrics(engine), mount{pattern: "/metrics", handler: other, kind: mountAPIRouter}), ui,
			"isn't"},
		{"the UI forwarding /metrics to the engine",
			withMetrics(engine), replace(ui, mount{pattern: "/metrics", handler: proxy, kind: mountEngineProxy}),
			"ADR 0035"},
		{"the UI leaving /metrics to the static bundle",
			withMetrics(engine), without(ui, "/metrics"),
			"the UI"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mountProblems(tc.engine, tc.ui, api, auth)
			if len(got) == 0 {
				t.Fatal("not reported")
			}
			if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Errorf("reported, but no problem says %q: %q", tc.want, got)
			}
		})
	}
}

// The class #1568 belongs to, held directly: every route the API router
// registers, configured and unconfigured, and every route the auth router
// registers, is one the engine's mux hands to that router. A route the router
// tests can reach and the real engine cannot is exactly what shipped.
func TestEngineMounts_1568_EveryRegisteredRouteIsReachable(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := "poll_interval: 1h\nstate:\n  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n  - id: production\n    backup_sets:\n      - id: alpha\n        remote:\n          type: local\n" +
		"        remote_path: " + filepath.Join(dir, "remote") + "\n        local_path: " + filepath.Join(dir, "local") + "\n" +
		"        include:\n          - \"*.dump\"\n        completion:\n          strategy: rename\n        stale_after: 24h\n"
	if err := os.MkdirAll(filepath.Join(dir, "remote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	backend, cleanup, err := service.Open(t.Context(), configPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	authSvc, err := local.New(local.Config{StorePath: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}

	fresh := t.TempDir()
	firstRun, err := service.NewFirstRun(service.FirstRunDefaults{
		ConfigPath:    filepath.Join(fresh, "config", "config.yaml"),
		StateDatabase: filepath.Join(fresh, "state", "state.db"),
	})
	if err != nil {
		t.Fatalf("service.NewFirstRun: %v", err)
	}

	platform := walkPlatform1568{}
	routers := map[string]http.Handler{
		"the configured":   webhost.NewRouter(webhost.RouterConfig{Platform: platform, Backend: backend, BinaryVersion: "test", Commit: "test"}),
		"the unconfigured": webhost.NewRouter(webhost.RouterConfig{Platform: platform, FirstRun: firstRun, BinaryVersion: "test", Commit: "test"}),
	}
	authRoutes := authSvc.Handler()
	param := regexp.MustCompile(`\{[^}]*\}`)

	reach := func(name string, walked chi.Routes, prefix string, mux *http.ServeMux) int {
		n := 0
		err := chi.Walk(walked, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			n++
			path := prefix + strings.ReplaceAll(param.ReplaceAllString(route, "x"), "*", "x")
			req := httptest.NewRequest(method, path, nil)
			if _, pattern := mux.Handler(req); pattern == "" {
				t.Errorf("%s router registers %s %s and the engine's mux answers %s with its own 404, so the real engine never serves it", name, method, prefix+route, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("chi.Walk: %v", err)
		}
		return n
	}
	for name, router := range routers {
		mux := newMux(engineMounts(router, authRoutes))
		routes, ok := router.(chi.Routes)
		if !ok {
			t.Fatalf("%s router is %T, which chi.Walk cannot read", name, router)
		}
		if n := reach(name, routes, "", mux); n == 0 {
			t.Fatalf("%s router walked no routes, so this test would pass without covering it", name)
		}
		walked, ok := authRoutes.(chi.Routes)
		if !ok {
			t.Fatalf("the auth router is %T, which chi.Walk cannot read", authRoutes)
		}
		reach("the auth", walked, authPrefix, mux)
	}
}

type walkPlatform1568 struct {
	capabilities.BasePlatformAdapter
}

func (walkPlatform1568) ID() capabilities.PlatformID { return capabilities.PlatformGeneric }
func (walkPlatform1568) Capabilities() capabilities.PlatformCapabilities {
	return capabilities.PlatformCapabilities{}
}
func (walkPlatform1568) PlatformInfo(context.Context) (capabilities.PlatformInfo, error) {
	return capabilities.PlatformInfo{ID: capabilities.PlatformGeneric, Name: "test"}, nil
}
