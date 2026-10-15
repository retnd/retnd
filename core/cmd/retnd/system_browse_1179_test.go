package main

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/cliecho"
)

// #1179: `retnd system browse [PATH] [--json]`, the terminal's half of the
// folder browser behind "Folder under the staging root on the host". The folders are on the
// machine the engine runs on, so like system capabilities this asks the engine
// and refuses without a route to it: answering from this host would list the
// wrong machine's file system.

func hostDirectoriesBody() map[string]any {
	return map[string]any{
		"root": "/srv/staging", "path": "alpha", "parent": "",
		"entries":   []any{map[string]any{"name": "inner", "path": "alpha/inner"}},
		"truncated": false,
	}
}

func TestSystemBrowse_1179_RoutedAndJSONIsTheWireObject(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	want := hostDirectoriesBody()
	var asked string
	e.handle("GET /system/host-directories", func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("path")
		writeJSON(w, http.StatusOK, want)
	})
	code, out, errOut := runOp(t, cfg, "system", "browse", "alpha", "--json")
	if code != 0 {
		t.Fatalf("system browse alpha --json exited %d\nstderr: %s", code, errOut)
	}
	if asked != "alpha" {
		t.Errorf("the engine was asked for path %q, want alpha", asked)
	}
	if got := decodeJSON(t, out); !reflect.DeepEqual(got, wireOf(t, want)) {
		t.Errorf("--json is not the object the engine served\n got: %v\nwant: %v", got, want)
	}
}

func TestSystemBrowse_1179_NoPathAsksForTheRootAndTheTextListsTheFolders(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	var asked = "unset"
	e.handle("GET /system/host-directories", func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("path")
		writeJSON(w, http.StatusOK, hostDirectoriesBody())
	})
	code, out, errOut := runOp(t, cfg, "system", "browse")
	if code != 0 {
		t.Fatalf("exited %d\nstderr: %s", code, errOut)
	}
	if asked != "" {
		t.Errorf("asked for %q, want the root (empty)", asked)
	}
	for _, want := range []string{"/srv/staging", "alpha/inner"} {
		if !strings.Contains(out, want) {
			t.Errorf("the text does not show %q:\n%s", want, out)
		}
	}
}

func TestSystemBrowse_1179_ATruncatedListingSaysSo(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	body := hostDirectoriesBody()
	body["truncated"] = true
	e.handle("GET /system/host-directories", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, body) })
	_, out, _ := runOp(t, cfg, "system", "browse")
	if !strings.Contains(strings.ToLower(out), "truncated") && !strings.Contains(strings.ToLower(out), "more folders") {
		t.Errorf("a cut listing prints as if it were complete:\n%s", out)
	}
}

func TestSystemBrowse_1179_AnEngineRefusalIsAFailureNotAnEmptyList(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	e.handle("GET /system/host-directories", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "that is not a folder name under the browsable root"}})
	})
	code, _, errOut := runOp(t, cfg, "system", "browse", "..")
	if code != exitFailure {
		t.Fatalf("exited %d, want exitFailure", code)
	}
	if !strings.Contains(errOut, "browsable root") {
		t.Errorf("the engine's reason is not shown:\n%s", errOut)
	}
}

func TestSystemBrowse_1179_RefusesWithoutARouteToTheEngine(t *testing.T) {
	cfg := writeTestConfig(t)
	code, _, errOut := runOp(t, cfg, "system", "browse")
	if code != exitFailure {
		t.Fatalf("exited %d with nothing serving, want exitFailure", code)
	}
	if !strings.Contains(errOut, "system browse") || !strings.Contains(errOut, apiURLEnv) {
		t.Errorf("the refusal does not name the command and %s:\n%s", apiURLEnv, errOut)
	}
}

func TestSystemBrowse_1179_UsageErrors(t *testing.T) {
	cfg := writeTestConfig(t)
	code, _, _ := runOp(t, cfg, "system", "browse", "a", "b")
	if code != exitUsage {
		t.Errorf("two paths exited %d, want the usage code %d", code, exitUsage)
	}
}

func TestSystemBrowse_1179_IsInUsageAndHasACliechoEntry(t *testing.T) {
	all := captureStderr(t, usage)
	for _, want := range []string{"system browse", "GET /api/v1/system/host-directories"} {
		if !strings.Contains(all, want) {
			t.Errorf("usage() does not mention %q", want)
		}
	}
	line := cliecho.Echo(cliecho.Action{Method: "GET", Route: "/system/host-directories"})
	if got := strings.Join(line.Command, " "); !strings.Contains(got, "system browse") {
		t.Errorf("cliecho echoed %q (gap: %q), want a system browse command", got, line.Gap)
	}
}

func TestSystemBrowse_1179_TheEchoCarriesThePath(t *testing.T) {
	line := cliecho.Echo(cliecho.Action{Method: "GET", Route: "/system/host-directories", Query: url.Values{"path": {"alpha/inner"}}})
	if got := strings.Join(line.Command, " "); !strings.HasSuffix(got, "system browse alpha/inner") {
		t.Errorf("cliecho echoed %q, want it to end with system browse alpha/inner", got)
	}
}
