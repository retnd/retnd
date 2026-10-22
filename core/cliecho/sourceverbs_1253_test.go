package cliecho

import (
	"strings"
	"testing"
)

// Issue #1253: three things a browser could do to a backup set's source
// that a terminal could not, each of which the terminal panel used to
// answer with a gap sentence. Each has a verb now, so the panel prints a
// line an operator can copy instead of a reason there is none.

// The candidate mode of the connection check echoes the whole unsaved
// form, which is what `backup-set test-connection --candidate` takes. The
// key is a reference and the trusted line is public, so nothing on the line
// is a credential; the by-id mode is unchanged beside it.
func TestEcho1253_CandidateConnectionTestIsACommand(t *testing.T) {
	line := Echo(Action{Method: "POST", Route: "/backup-sets/test-connection",
		Body: []byte(`{"host":"10.0.0.14","port":2222,"user":"backups","ssh_key_id":"key_1","known_hosts_line":"10.0.0.14 ssh-ed25519 AAAAC3Nz","remote_path":"/var/backups"}`)})
	if line.Gap != "" {
		t.Fatalf("the candidate check is still a gap: %s", line.GapDetail)
	}
	want := "retnd backup-set test-connection --candidate --host 10.0.0.14 --port 2222 --user backups --ssh-key-id key_1 --known-hosts-line '10.0.0.14 ssh-ed25519 AAAAC3Nz' --remote-path /var/backups"
	if got := line.Shell(); got != want {
		t.Errorf("candidate echo\n  got  %s\n  want %s", got, want)
	}

	// The default port is left off, the way the create builder leaves it
	// off: a line printing --port 0 would name a value the request did
	// not make.
	noPort := Echo(Action{Method: "POST", Route: "/backup-sets/test-connection",
		Body: []byte(`{"host":"10.0.0.14","user":"backups","ssh_key_id":"key_1","known_hosts_line":"10.0.0.14 ssh-ed25519 AAAAC3Nz","remote_path":"/var/backups"}`)}).Shell()
	if strings.Contains(noPort, "--port") {
		t.Errorf("a request with no port echoes one: %s", noPort)
	}

	byID := Echo(Action{Method: "POST", Route: "/backup-sets/test-connection", Body: []byte(`{"backup_set_id":"api-server/var-backups"}`)}).Shell()
	if byID != "retnd backup-set test-connection api-server/var-backups" {
		t.Errorf("the by-id mode changed: %s", byID)
	}

	for _, gap := range Gaps() {
		if gap.Route == "POST /backup-sets/test-connection" {
			t.Errorf("the route still declares a gap sentence: %s", gap.Why)
		}
	}
}

// Taking a hold is `backup-set edit-hold <id> --take`, beside the report
// and the release the verb already had.
func TestEcho1253_TakingAnEditHoldIsACommand(t *testing.T) {
	params := map[string]string{"source": "api-server", "set": "var-backups"}
	take := Echo(Action{Method: "POST", Route: "/backup-sets/{source}/{set}/edit-hold", Params: params})
	if take.Gap != "" {
		t.Fatalf("taking a hold is still a gap: %s", take.GapDetail)
	}
	if got := take.Shell(); got != "retnd backup-set edit-hold api-server/var-backups --take" {
		t.Errorf("take echo = %s", got)
	}
	release := Echo(Action{Method: "POST", Route: "/backup-sets/{source}/{set}/edit-hold/release", Params: params}).Shell()
	if release != "retnd backup-set edit-hold api-server/var-backups --release" {
		t.Errorf("release echo changed: %s", release)
	}
	for _, gap := range Gaps() {
		if gap.Route == "POST /backup-sets/{source}/{set}/edit-hold" {
			t.Errorf("the route still declares a gap sentence: %s", gap.Why)
		}
	}
}

// Describing the source host is the read-only preflight, and the write
// check is the same line with --write-check: the browser describes on open
// and probes on a button, and the two lines keep that apart.
func TestEcho1253_SourceHostDescriptionIsTheReadOnlyPreflight(t *testing.T) {
	params := map[string]string{"source": "api-server", "set": "var-backups"}
	describe := Echo(Action{Method: "GET", Route: "/backup-sets/{source}/{set}/source-host", Params: params})
	if describe.Gap != "" {
		t.Fatalf("describing the source host is still a gap: %s", describe.GapDetail)
	}
	if got := describe.Shell(); got != "retnd recover --set api-server/var-backups --to source: --preflight" {
		t.Errorf("describe echo = %s", got)
	}
	named := Echo(Action{Method: "GET", Route: "/backup-sets/{source}/{set}/source-host", Params: params,
		Query: mustQuery("folder=retnd-recovered-20261015-101500-a1b2")}).Shell()
	if named != "retnd recover --set api-server/var-backups --to source:retnd-recovered-20261015-101500-a1b2 --preflight" {
		t.Errorf("describe with a folder = %s", named)
	}

	check := Echo(Action{Method: "POST", Route: "/backup-sets/{source}/{set}/source-host/write-check", Params: params}).Shell()
	if check != "retnd recover --set api-server/var-backups --to source: --preflight --write-check" {
		t.Errorf("write check echo = %s", check)
	}
	for _, gap := range Gaps() {
		if gap.Route == "GET /backup-sets/{source}/{set}/source-host" {
			t.Errorf("the route still declares a gap sentence: %s", gap.Why)
		}
	}
}
