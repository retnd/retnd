package webhost

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
)

// The route-to-verb guard (issue #1150).
//
// TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap walks the router for
// /api/v1 routes only. It skipped the two health probes by prefix, and it
// never saw the twelve /auth routes, which a separate router serves. The
// audit found both holes by reading. This guard walks everything that is
// registered, on every router a deployment mounts, and holds each route to
// the same rule: a cliecho entry, or a written reason it has none.
//
// The comparison is a pure function (unanswered, missingFromSpec) so the
// mutation tests below can hand it a route that nobody answered and watch
// it fail. A guard whose failure nobody has seen is a guard that may have
// stopped guarding.

// routeKey strips the /api/v1 prefix the way cliecho keys its table, and
// leaves a route outside /api/v1 (the health probes) exactly as registered.
func routeKey(method, route string) string {
	route = strings.TrimSuffix(route, "/")
	if rest, ok := strings.CutPrefix(route, "/api/v1"); ok {
		return method + " " + rest
	}
	return method + " " + route
}

func walkAll(t *testing.T, r chi.Routes, prefix string, into map[string]bool) {
	t.Helper()
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		into[routeKey(method, prefix+route)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
}

// everyRegisteredRoute is the union of the configured router, the
// first-run router, and the auth router at the prefix serve mounts it.
func everyRegisteredRoute(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	configured := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: newSyncFakeBackend(),
		Gate: alwaysPassGate{}, BinaryVersion: "test", Commit: "test",
	})
	unconfigured := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), FirstRun: &fakeFirstRun{},
		BinaryVersion: "test", Commit: "test",
	})
	walkAll(t, routableFor(t, configured), "", out)
	walkAll(t, routableFor(t, unconfigured), "", out)

	svc, err := local.New(local.Config{StorePath: filepath.Join(t.TempDir(), "auth.json")})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	authRoutes, ok := svc.Handler().(chi.Routes)
	if !ok {
		t.Fatalf("auth Handler() is %T, which chi.Walk cannot read", svc.Handler())
	}
	// serve mounts it at /api/v1/auth/ with the prefix stripped.
	walkAll(t, authRoutes, "/api/v1/auth", out)
	return out
}

// unanswered lists the registered routes that have neither a cliecho
// entry nor a non-blank recorded reason.
func unanswered(registered, answered map[string]bool, reasons map[string]string) []string {
	var out []string
	for route := range registered {
		if answered[route] {
			continue
		}
		if strings.TrimSpace(reasons[route]) != "" {
			continue
		}
		out = append(out, route)
	}
	sort.Strings(out)
	return out
}

// missingFromSpec lists the registered routes outside /api/v1 that the
// spec does not declare and that carry no recorded reason. Routes under
// /api/v1 are held to the spec by TestContract_RouterAndContractDeclare
// TheSameOperations, which resolves the catch-all patterns properly.
func missingFromSpec(registered, inSpec map[string]bool, reasons map[string]string) []string {
	var out []string
	for route := range registered {
		_, path, _ := strings.Cut(route, " ")
		if !strings.HasPrefix(path, "/health/") {
			continue
		}
		if inSpec[route] || strings.TrimSpace(reasons[route]) != "" {
			continue
		}
		out = append(out, route)
	}
	sort.Strings(out)
	return out
}

func answeredSet() map[string]bool {
	out := map[string]bool{}
	for _, r := range cliecho.Routes() {
		out[r] = true
	}
	return out
}

func specSet() map[string]bool {
	out := map[string]bool{}
	for _, e := range apicontract.Endpoints {
		out[e.Method+" "+e.Path] = true
	}
	return out
}

func TestRouteVerbGuard1150_EveryRegisteredRouteIsAnsweredOrExplained(t *testing.T) {
	registered := everyRegisteredRoute(t)

	// Vacuity: the two sets this guard exists to cover must be in view.
	for _, must := range []string{"GET /health/live", "GET /health/ready", "POST /auth/login", "GET /auth/session", "PATCH /auth/recovery"} {
		if !registered[must] {
			t.Fatalf("the walk did not see %s, so this guard would pass without covering it", must)
		}
	}

	for _, route := range unanswered(registered, answeredSet(), cliecho.RecordedReasons()) {
		t.Errorf("%s is registered and has neither a core/cliecho entry nor a recorded reason. Add an entry that builds the command, or one that says what verb would have to exist (or, for a probe outside /api/v1, a line in cliecho.RecordedReasons).", route)
	}
	for _, route := range missingFromSpec(registered, specSet(), cliecho.RecordedReasons()) {
		t.Errorf("%s is registered but is not in api/v1/openapi.json and has no recorded reason for being left out", route)
	}
}

// And the other direction: a recorded reason for a route nobody registers
// is a stale excuse.
func TestRouteVerbGuard1150_NoReasonOutlivesItsRoute(t *testing.T) {
	registered := everyRegisteredRoute(t)
	for route := range cliecho.RecordedReasons() {
		if !registered[route] {
			t.Errorf("cliecho.RecordedReasons explains %q and no router registers it", route)
		}
	}
}

// ---------------------------------------------------- mutation self-test ---

func TestRouteVerbGuard1150_MutationANewRouteWithNoAnswerIsCaught(t *testing.T) {
	registered := everyRegisteredRoute(t)
	registered["POST /a-route-nobody-answered"] = true
	got := unanswered(registered, answeredSet(), cliecho.RecordedReasons())
	if !contains(got, "POST /a-route-nobody-answered") {
		t.Errorf("a registered route with no entry and no reason was not reported; got %v", got)
	}
}

func TestRouteVerbGuard1150_MutationADroppedAuthEntryIsCaught(t *testing.T) {
	registered := everyRegisteredRoute(t)
	answered := answeredSet()
	delete(answered, "POST /auth/login")
	got := unanswered(registered, answered, cliecho.RecordedReasons())
	if !contains(got, "POST /auth/login") {
		t.Errorf("dropping the /auth/login entry was not reported; got %v", got)
	}
}

func TestRouteVerbGuard1150_MutationAProbeWithoutAReasonIsCaught(t *testing.T) {
	registered := everyRegisteredRoute(t)
	got := unanswered(registered, answeredSet(), map[string]string{"GET /health/live": "   "})
	for _, want := range []string{"GET /health/live", "GET /health/ready"} {
		if !contains(got, want) {
			t.Errorf("%s with a blank or missing reason was not reported; got %v", want, got)
		}
	}
	if spec := missingFromSpec(registered, specSet(), nil); !contains(spec, "GET /health/live") || !contains(spec, "GET /health/ready") {
		t.Errorf("the health probes are not in the spec and carry no reason, and the spec check did not say so; got %v", spec)
	}
}

func TestRouteVerbGuard1150_ControlAnAnsweredRouteIsNotReported(t *testing.T) {
	registered := map[string]bool{"POST /a-route": true, "GET /health/live": true}
	if got := unanswered(registered, map[string]bool{"POST /a-route": true}, map[string]string{"GET /health/live": "probe"}); len(got) != 0 {
		t.Errorf("answered routes were reported: %v", got)
	}
	if got := missingFromSpec(registered, nil, map[string]string{"GET /health/live": "probe"}); len(got) != 0 {
		t.Errorf("a recorded reason did not satisfy the spec check: %v", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
