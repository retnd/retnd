package webhost

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/core/cliecho"
)

// The one allowlist of root-router routes that deliberately live outside
// /api/v1 is cliecho.RootRoutesOutsideAPI, one line per route with its reason.
// Every route guard in this package asks isListedRootRoute and nothing else, so
// no guard carries an exemption of its own.

func isListedRootRoute(method, route string) bool {
	_, ok := cliecho.RootRoutesOutsideAPI()[method+" "+route]

	return ok
}

// unlistedRootRoutes is every route on router that is neither under /api/v1
// nor in the list.
func unlistedRootRoutes(t *testing.T, router http.Handler) []string {
	t.Helper()
	var out []string
	err := chi.Walk(routableFor(t, router), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") && !isListedRootRoute(method, route) {
			out = append(out, method+" "+route)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}

	return out
}

func TestEveryRootRouteOutsideTheAPIIsInTheOneList(t *testing.T) {
	router := NewRouter(RouterConfig{Platform: noAuthWiredAdapter{}, Backend: newSyncFakeBackend(), Gate: alwaysPassGate{}, BinaryVersion: "test", Commit: "test"})
	if got := unlistedRootRoutes(t, router); len(got) != 0 {
		t.Errorf("routes outside /api/v1 that cliecho.RootRoutesOutsideAPI does not list: %v", got)
	}
	for _, must := range []string{"GET /health/live", "GET /health/ready", "GET /metrics"} {
		if !isListedRootRoute(strings.Fields(must)[0], strings.Fields(must)[1]) {
			t.Errorf("%s is missing from the list", must)
		}
	}
}

// The negative control: a planted route that is neither under /api/v1 nor in
// the list is the one unlisted route the shared check finds, and the list does
// not claim it, so every guard that asks isListedRootRoute treats it as an
// ordinary route rather than an exempt one.
func TestAPlantedRootRouteOutsideTheListIsCaught(t *testing.T) {
	router := NewRouter(RouterConfig{Platform: noAuthWiredAdapter{}, Backend: newSyncFakeBackend(), Gate: alwaysPassGate{}, BinaryVersion: "test", Commit: "test"})
	planted := chi.NewRouter()
	planted.Get("/planted-debug", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	planted.Mount("/", router)
	got := unlistedRootRoutes(t, planted)
	if len(got) != 1 || got[0] != "GET /planted-debug" {
		t.Fatalf("the planted route was not the one unlisted route found: %v", got)
	}
	if isListedRootRoute(http.MethodGet, "/planted-debug") {
		t.Fatal("a planted route is listed")
	}
}
