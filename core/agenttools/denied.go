package agenttools

import (
	"path"
	"strings"
)

// The deny list: the routes a bearer token is refused whatever its scope,
// because they can return key material, a recipient, a bundle, a credential,
// an SSH key, a workflow secret or a recovery grant, or because they are
// authentication, first-run, token minting, a catalog rebuild, a quarantine
// reinstate, a retention apply, or the creation or deletion of a set or a
// medium.
//
// A token works against raw /api/v1, so a list held only by a transport would
// hold nothing back. apps/common/webhost reads this one on the wire, before
// the token is looked at, and the guard in this package holds that no tool row
// sits over a route it matches. Every entry here is also a refusal row in the
// table, which is how the reason is stated.
//
// A pattern is a path below /api/v1. A segment in braces matches any one
// segment, and a trailing /* matches the path before it and everything below
// it. Without the /* a pattern matches exactly that path. A method of "" is
// every method.
var denied = []struct{ method, pattern string }{
	// Authentication, first-run, token minting and recovery grants.
	{"", "/auth/*"},
	{"", "/system/first-run/*"},
	{"", "/recoveries/*"},
	{"POST", "/backups/{source}/{set}/{name}/recovery"},
	{"", "/agent/tokens/*"},

	// Key custody and key providers.
	{"", "/encryption/keys/*"},
	{"", "/encryption/providers/*"},

	// Credentials for a storage medium and the reach of an SSH key.
	{"", "/storage-credentials/*"},
	{"", "/storage-mediums/preflight"},
	{"", "/storage-mediums/{id}/preflight"},
	{"", "/storage-mediums/{id}/configuration/*"},
	{"", "/ssh-keys/*"},
	{"", "/ssh/*"},

	// Workflow secrets, script output and validator scripts.
	{"", "/settings/workflow/environment/*"},
	{"", "/backup-sets/{source}/{set}/workflow/environment/*"},
	{"GET", "/workflow-runs/{run}/steps/{step}/logs"},
	{"GET", "/assurance/validators/{name}/script"},

	// Changes nobody confirms their way into.
	{"POST", "/backup-sets"},
	{"DELETE", "/backup-sets/{source}/{set}"},
	{"POST", "/storage-mediums"},
	{"DELETE", "/storage-mediums/{id}"},
	{"POST", "/catalog/rebuild"},
	{"POST", "/backup-sets/{source}/{set}/retention/apply"},
	{"POST", "/quarantine/{source}/{set}/{name}/reinstate"},
}

// DeniedRoutes reports the deny list as "METHOD pattern" strings, with "*" for
// every method, so a guard can drive each entry through a router.
func DeniedRoutes() []string {
	out := make([]string, 0, len(denied))
	for _, d := range denied {
		m := d.method
		if m == "" {
			m = "*"
		}
		out = append(out, m+" "+d.pattern)
	}
	return out
}

// Denied reports whether a request to a concrete path below /api/v1 is one no
// token may make. The path is cleaned the way the router cleans it, so a dot
// segment or a doubled slash cannot step around an entry.
func Denied(method, p string) bool {
	p = path.Clean("/" + p)
	for _, d := range denied {
		if (d.method == "" || d.method == method) && matches(d.pattern, p) {
			return true
		}
	}
	return false
}

// BearerReadable reports whether a bearer token may make a safe request to the
// route the router resolved it to. pattern is the router's own pattern below
// /api/v1 (what chi reports for the request, "/backup-sets/*" for the one
// catch-all), not the path the client sent, and it is compared whole: a read is
// offered when a read tool sits on exactly that route, so a deeper route that
// shares a tool's prefix is not. Everything else a token does not read.
func BearerReadable(method, pattern string) bool {
	if method != "GET" {
		return false
	}
	for _, r := range rows {
		if r.Tool != "" && r.Method == method && r.Route == pattern {
			return true
		}
	}
	return false
}

// matches reports whether a concrete path fits a route pattern.
func matches(pattern, p string) bool {
	p = path.Clean("/" + p)
	prefix := false
	if base, ok := strings.CutSuffix(pattern, "/*"); ok {
		pattern, prefix = base, true
	}
	want := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(p, "/"), "/")
	if len(got) < len(want) || (!prefix && len(got) != len(want)) {
		return false
	}
	for i, seg := range want {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		if seg != got[i] {
			return false
		}
	}
	return true
}
