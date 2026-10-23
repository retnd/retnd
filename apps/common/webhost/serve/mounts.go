package serve

import (
	"net/http"
	"reflect"
	"strings"
)

// Every serve mux this package builds, as data (#1253).
//
// The route-to-verb guard in apps/common/webhost walks the chi routers
// (webhost.NewRouter, configured and unconfigured, and the auth router at
// /api/v1/auth) and holds each route to cliecho's table. Those routers
// reach a client through an http.ServeMux composed here, and a handler
// mounted on that mux beside them would be a set of routes the guard never
// sees. So the compositions are lists of mounts, each saying what kind of
// handler it is, built into a mux by newMux and nothing else
// (TestOnlyNewMuxBuildsAServeMux), and mountProblems holds the lists to
// the rule: anything under /api or /health is a router the guard walks,
// or a proxy to an engine whose own mounts are.

// mountKind is what answers a mount, in the guard's terms.
type mountKind int

const (
	// mountAPIRouter is webhost.NewRouter, which the guard walks.
	mountAPIRouter mountKind = iota + 1
	// mountAuthRouter is the auth router, which the guard walks at
	// authPrefix.
	mountAuthRouter
	// mountEngineProxy forwards to an engine composed by engineMounts.
	mountEngineProxy
	// mountStatic is the UI bundle, with no API route behind it.
	mountStatic
)

// authPrefix is where the auth router is mounted, with that prefix
// stripped, which is also where the guard walks it.
const authPrefix = "/api/v1/auth"

type mount struct {
	pattern string
	handler http.Handler
	kind    mountKind
	// strip is a prefix taken off the path before handler sees it.
	strip string
}

// newMux is the one place a serve mux is built.
func newMux(mounts []mount) *http.ServeMux {
	mux := http.NewServeMux()
	for _, m := range mounts {
		h := m.handler
		if m.strip != "" {
			h = http.StripPrefix(m.strip, h)
		}
		mux.Handle(m.pattern, h)
	}
	return mux
}

// engineMounts is the engine's composition: the auth router when there is
// one, and the API router for the health probes and everything else under
// /api/v1.
func engineMounts(apiRouter, authRoutes http.Handler) []mount {
	var out []mount
	if authRoutes != nil {
		out = append(out, mount{pattern: authPrefix + "/", handler: authRoutes, kind: mountAuthRouter, strip: authPrefix})
	}
	return append(out,
		mount{pattern: "/health/", handler: apiRouter, kind: mountAPIRouter},
		mount{pattern: "/api/v1/", handler: apiRouter, kind: mountAPIRouter},
	)
}

// uiMounts is the UI container's composition: everything under /api and
// /health is forwarded to the engine, and the rest is the static bundle.
func uiMounts(proxy, downloads, static http.Handler) []mount {
	return []mount{
		{pattern: "/health/", handler: proxy, kind: mountEngineProxy},
		{pattern: "/api/v1/", handler: proxy, kind: mountEngineProxy},
		// EPIC Q (#976): downloads get their own proxy with a rolling write
		// deadline; see NewUI.
		{pattern: "/api/v1/recoveries/", handler: downloads, kind: mountEngineProxy},
		// #1106: POST /api/v1/recoveries, the exact path. Registered apart
		// because the subtree pattern above would otherwise answer it with
		// a redirect, which turns the POST into a GET.
		{pattern: "/api/v1/recoveries", handler: downloads, kind: mountEngineProxy},
		{pattern: "/", handler: static, kind: mountStatic},
	}
}

// isAPIPattern says whether a pattern is in the guard's territory.
func isAPIPattern(pattern string) bool {
	p := pattern
	if _, rest, ok := strings.Cut(p, " "); ok {
		p = strings.TrimSpace(rest)
	}
	if i := strings.Index(p, "/"); i > 0 {
		p = p[i:]
	}
	return p == "/api" || strings.HasPrefix(p, "/api/") || p == "/health" || strings.HasPrefix(p, "/health/")
}

// sameHandler compares two handlers without panicking on a func type.
func sameHandler(a, b http.Handler) bool {
	if a == nil || b == nil {
		return a == b
	}
	if reflect.TypeOf(a) != reflect.TypeOf(b) || !reflect.TypeOf(a).Comparable() {
		return false
	}
	return a == b
}

// mountProblems is every way the two compositions break the rule, given
// the API and auth routers the engine was built with.
func mountProblems(engine, ui []mount, apiRouter, authRoutes http.Handler) []string {
	var out []string
	unique := func(name string, ms []mount) {
		seen := map[string]bool{}
		for _, m := range ms {
			if seen[m.pattern] {
				out = append(out, name+" mounts "+m.pattern+" twice")
			}
			seen[m.pattern] = true
		}
	}
	unique("the engine", engine)
	unique("the UI", ui)

	for _, m := range engine {
		switch {
		case m.kind == mountAPIRouter && !sameHandler(m.handler, apiRouter):
			out = append(out, "the engine's "+m.pattern+" says it is the API router and isn't")
		case m.kind == mountAPIRouter && m.strip != "":
			out = append(out, "the engine's "+m.pattern+" strips "+m.strip+" before the API router, which the guard walks unstripped")
		case m.kind == mountAuthRouter && (!sameHandler(m.handler, authRoutes) || m.strip != authPrefix || m.pattern != authPrefix+"/"):
			out = append(out, "the engine's "+m.pattern+" is not the auth router at "+authPrefix+", which is where the guard walks it")
		case m.kind != mountAPIRouter && m.kind != mountAuthRouter:
			out = append(out, "the engine mounts "+m.pattern+" with something other than a router the guard walks")
		case !isAPIPattern(m.pattern):
			out = append(out, "the engine mounts a router at "+m.pattern+", outside /api and /health, where it would answer paths the guard never asks about")
		}
	}

	covered := func(pattern string) bool {
		for _, e := range engine {
			if strings.HasSuffix(e.pattern, "/") && strings.HasPrefix(pattern, e.pattern) || e.pattern == pattern {
				return true
			}
		}
		return false
	}
	for _, m := range ui {
		switch {
		case isAPIPattern(m.pattern) && m.kind != mountEngineProxy:
			out = append(out, "the UI mounts "+m.pattern+" with something other than the proxy to the engine")
		case isAPIPattern(m.pattern) && !covered(m.pattern):
			out = append(out, "the UI forwards "+m.pattern+" and no engine mount answers it")
		case !isAPIPattern(m.pattern) && m.kind != mountStatic:
			out = append(out, "the UI mounts "+m.pattern+", outside /api and /health, with something other than the static bundle")
		}
	}
	return out
}
