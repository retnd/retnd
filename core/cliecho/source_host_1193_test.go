package cliecho

import (
	"strings"
	"testing"
)

// Issue #1193: the terminal echo of a recovery spells the source host with
// its colon, and a staging name the terminal would read as something else
// is written ./NAME, so an echoed line can never turn a staging folder into
// a production write.
func TestEcho1193_SourceAndStagingSpellings(t *testing.T) {
	params := map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"}
	for _, tc := range []struct {
		route, body, want string
	}{
		{"/backups/{source}/{set}/{name}/recovery", `{"target":{"kind":"source","path":"retnd-recovered-x","acknowledged_destination":"sh1-1"}}`,
			"retnd recover api-server/var-backups/dump.tar --to source:retnd-recovered-x --acknowledge-source-write"},
		{"/backups/{source}/{set}/{name}/recovery", `{"target":{"kind":"source","path":"retnd-recovered-x"}}`,
			"retnd recover api-server/var-backups/dump.tar --to source:retnd-recovered-x"},
		{"/backups/{source}/{set}/{name}/recovery", `{"target":{"kind":"staging","path":"source"}}`,
			"retnd recover api-server/var-backups/dump.tar --to source"},
		{"/backups/{source}/{set}/{name}/recovery", `{"target":{"kind":"staging","path":"source:incident"}}`,
			"retnd recover api-server/var-backups/dump.tar --to ./source:incident"},
		{"/backups/{source}/{set}/{name}/recovery", `{"target":{"kind":"staging","path":"medium:x"}}`,
			"retnd recover api-server/var-backups/dump.tar --to ./medium:x"},
		{"/backups/{source}/{set}/{name}/recovery", `{"target":{"kind":"staging","path":"-rf"}}`,
			"retnd recover api-server/var-backups/dump.tar --to ./-rf"},
		{"/recoveries", `{"set":"api-server/var-backups","target":{"kind":"source","path":"retnd-recovered-x","acknowledged_destination":"sh1-1"}}`,
			"retnd recover --set api-server/var-backups --to source:retnd-recovered-x --acknowledge-source-write --all"},
		{"/recoveries", `{"set":"api-server/var-backups","newest_only":true,"target":{"kind":"source","path":"retnd-recovered-x","acknowledged_destination":"sh1-1"}}`,
			"retnd recover --set api-server/var-backups --newest-only --to source:retnd-recovered-x --acknowledge-source-write"},
		{"/recoveries", `{"set":"api-server/var-backups","preflight":true,"target":{"kind":"source","path":"retnd-recovered-x"}}`,
			"retnd recover --set api-server/var-backups --to source:retnd-recovered-x --preflight"},
	} {
		got := Echo(Action{Method: "POST", Route: tc.route, Params: params, Body: []byte(tc.body)}).Shell()
		if got != tc.want {
			t.Errorf("%s %s\n  got  %s\n  want %s", tc.route, tc.body, got, tc.want)
		}
	}
	check := Echo(Action{Method: "POST", Route: "/backup-sets/{source}/{set}/source-host/write-check", Params: map[string]string{"source": "api-server", "set": "var-backups"}}).Shell()
	if check != "retnd recover --set api-server/var-backups --to source: --preflight" {
		t.Errorf("write check echo = %s", check)
	}
	describe := Echo(Action{Method: "GET", Route: "/backup-sets/{source}/{set}/source-host"})
	if describe.Gap == "" || !strings.Contains(describe.GapDetail, "--preflight") {
		t.Errorf("describe = %+v, want a gap that names the preflight", describe)
	}
}
