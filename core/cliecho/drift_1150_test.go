package cliecho

import (
	"strings"
	"testing"
)

// Issue #1150: the echo table said things the code no longer does.

// activity --follow --scope deployment ships (activity.go), so the live
// feed's deployment scope must echo it rather than printing a gap.
func TestDrift1150_DeploymentScopeEchoesTheShippedFlag(t *testing.T) {
	line := Echo(Action{Method: "GET", Route: "/activity/live", Query: mustQuery("scope=deployment")})
	if line.Gap != "" {
		t.Fatalf("scope=deployment still prints a gap: %s", line.GapDetail)
	}
	if got, want := line.Shell(), Binary+" activity --follow --scope deployment"; got != want {
		t.Errorf("scope=deployment prints\n  %s\nwant\n  %s", got, want)
	}

	// The control: the unscoped feed does not grow a --scope.
	plain := Echo(Action{Method: "GET", Route: "/activity/live"})
	if strings.Contains(plain.Shell(), "--scope") {
		t.Errorf("the unscoped live feed echoes a scope: %s", plain.Shell())
	}
}

// The API's filter on GET /backups is setId (handlers_artifacts.go), so
// that is the parameter the echo has to read. A filtered UI view echoing an
// unfiltered command shows an operator more than the panel does.
func TestDrift1150_ArtifactsFilterReadsSetID(t *testing.T) {
	line := Echo(Action{Method: "GET", Route: "/backups", Query: mustQuery("setId=api-server/var-backups")})
	if got, want := line.Shell(), Binary+" artifacts --backup-set api-server/var-backups"; got != want {
		t.Errorf("a setId filter prints\n  %s\nwant\n  %s", got, want)
	}

	// The old spelling is not a parameter the route reads, so it must not
	// be echoed as though it were.
	stale := Echo(Action{Method: "GET", Route: "/backups", Query: mustQuery("backup_set=api-server/var-backups")})
	if strings.Contains(stale.Shell(), "--backup-set") {
		t.Errorf("a query parameter the route does not read is echoed as a filter: %s", stale.Shell())
	}
}

// The /auth/* routes are served by a separate router, so the table never
// saw them. Each needs an entry: a command, or a written reason.
func TestDrift1150_EveryAuthRouteHasAnEntry(t *testing.T) {
	want := []string{
		"POST /auth/login", "POST /auth/enroll", "POST /auth/password", "POST /auth/logout",
		"GET /auth/session", "POST /auth/forgot-password", "POST /auth/reset-password",
		"POST /auth/verify-email", "POST /auth/verify-email/resend",
		"GET /auth/recovery", "PATCH /auth/recovery", "POST /auth/recovery/test",
	}
	have := map[string]bool{}
	for _, r := range Routes() {
		have[r] = true
	}
	for _, route := range want {
		if !have[route] {
			t.Errorf("%s has no entry in the cliecho table", route)
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		line := Echo(Action{Method: method, Route: path})
		if len(line.Command) == 0 && strings.TrimSpace(line.GapDetail) == "" {
			t.Errorf("%s prints a gap with no reason", route)
		}
		if !strings.HasPrefix(line.Route, method+" /api/v1/auth/") {
			t.Errorf("%s names itself %q", route, line.Route)
		}
	}
}

// Routes that sit outside /api/v1 (the two health probes) are not echoed,
// but each carries a recorded reason, so "no entry" is never silent.
func TestDrift1150_OutsideAPIRoutesCarryAReason(t *testing.T) {
	got := RecordedReasons()
	for _, route := range []string{"GET /health/live", "GET /health/ready"} {
		if strings.TrimSpace(got[route]) == "" {
			t.Errorf("%s has no recorded reason", route)
		}
	}
}
