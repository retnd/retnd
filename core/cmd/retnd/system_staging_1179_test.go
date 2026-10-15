package main

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/cliecho"
)

// #1179: `retnd system staging [PATH] [--json]`, the terminal's half of the
// folder browser behind "Folder under the staging root on the host". The folders are on the
// machine the engine runs on, so like system capabilities this asks the engine
// and refuses without a route to it: answering from this host would list the
// wrong machine's file system.

func stagingDirectoriesBody() map[string]any {
	return map[string]any{
		"root": "/srv/staging", "path": "alpha", "parent": "",
		"entries":   []any{map[string]any{"name": "inner", "path": "alpha/inner"}},
		"truncated": false,
	}
}

func TestSystemStaging_1179_RoutedAndJSONIsTheWireObject(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	want := stagingDirectoriesBody()
	var asked string
	e.handle("GET /system/staging-directories", func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("path")
		writeJSON(w, http.StatusOK, want)
	})
	code, out, errOut := runOp(t, cfg, "system", "staging", "alpha", "--json")
	if code != 0 {
		t.Fatalf("system staging alpha --json exited %d\nstderr: %s", code, errOut)
	}
	if asked != "alpha" {
		t.Errorf("the engine was asked for path %q, want alpha", asked)
	}
	if got := decodeJSON(t, out); !reflect.DeepEqual(got, wireOf(t, want)) {
		t.Errorf("--json is not the object the engine served\n got: %v\nwant: %v", got, want)
	}
}

func TestSystemStaging_1179_NoPathAsksForTheRootAndTheTextListsTheFolders(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	var asked = "unset"
	e.handle("GET /system/staging-directories", func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("path")
		writeJSON(w, http.StatusOK, stagingDirectoriesBody())
	})
	code, out, errOut := runOp(t, cfg, "system", "staging")
	if code != 0 {
		t.Fatalf("exited %d\nstderr: %s", code, errOut)
	}
	if asked != "" {
		t.Errorf("asked for %q, want the root (empty)", asked)
	}
	// #1185 review: stdout carries the folder names alone, one per line, so a
	// script can read it; where they are and anything else goes to stderr.
	if out != "alpha/inner\n" {
		t.Errorf("stdout = %q, want only the folder names", out)
	}
	if !strings.Contains(errOut, "/srv/staging") {
		t.Errorf("stderr does not say which root this is:\n%s", errOut)
	}
}

func TestSystemStaging_1179_ATruncatedListingSaysSo(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	body := stagingDirectoriesBody()
	body["truncated"] = true
	e.handle("GET /system/staging-directories", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, body) })
	_, out, errOut := runOp(t, cfg, "system", "staging")
	if !strings.Contains(strings.ToLower(errOut), "more folders") {
		t.Errorf("a cut listing prints as if it were complete:\n%s", errOut)
	}
	if out != "alpha/inner\n" {
		t.Errorf("stdout = %q, want only the folder names even when the listing is cut", out)
	}
}

func TestSystemStaging_1179_AnEngineRefusalIsAFailureNotAnEmptyList(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	e.handle("GET /system/staging-directories", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "that is not a folder name under the browsable root"}})
	})
	code, _, errOut := runOp(t, cfg, "system", "staging", "..")
	if code != exitFailure {
		t.Fatalf("exited %d, want exitFailure", code)
	}
	if !strings.Contains(errOut, "browsable root") {
		t.Errorf("the engine's reason is not shown:\n%s", errOut)
	}
}

func TestSystemStaging_1179_RefusesWithoutARouteToTheEngine(t *testing.T) {
	cfg := writeTestConfig(t)
	code, _, errOut := runOp(t, cfg, "system", "staging")
	if code != exitFailure {
		t.Fatalf("exited %d with nothing serving, want exitFailure", code)
	}
	if !strings.Contains(errOut, "system staging") || !strings.Contains(errOut, apiURLEnv) {
		t.Errorf("the refusal does not name the command and %s:\n%s", apiURLEnv, errOut)
	}
}

func TestSystemStaging_1179_UsageErrors(t *testing.T) {
	cfg := writeTestConfig(t)
	code, _, _ := runOp(t, cfg, "system", "staging", "a", "b")
	if code != exitUsage {
		t.Errorf("two paths exited %d, want the usage code %d", code, exitUsage)
	}
}

func TestSystemStaging_1179_IsInUsageAndHasACliechoEntry(t *testing.T) {
	all := captureStderr(t, usage)
	for _, want := range []string{"system staging", "GET /api/v1/system/staging-directories"} {
		if !strings.Contains(all, want) {
			t.Errorf("usage() does not mention %q", want)
		}
	}
	// recover --to NAME is what the listing is for, so its help says where
	// the names come from.
	if !strings.Contains(recoverUsageBlock(all), "system staging") {
		t.Errorf("the recover lines of usage() do not point at system staging:\n%s", recoverUsageBlock(all))
	}
	line := cliecho.Echo(cliecho.Action{Method: "GET", Route: "/system/staging-directories"})
	if got := strings.Join(line.Command, " "); !strings.Contains(got, "system staging") {
		t.Errorf("cliecho echoed %q (gap: %q), want a system staging command", got, line.Gap)
	}
}

func TestSystemStaging_1179_TheEchoCarriesThePath(t *testing.T) {
	line := cliecho.Echo(cliecho.Action{Method: "GET", Route: "/system/staging-directories", Query: url.Values{"path": {"alpha/inner"}}})
	if got := strings.Join(line.Command, " "); !strings.HasSuffix(got, "system staging alpha/inner") {
		t.Errorf("cliecho echoed %q, want it to end with system staging alpha/inner", got)
	}
}

// recoverUsageBlock is usage()'s recover entries, up to the next command.
func recoverUsageBlock(all string) string {
	var b strings.Builder
	in := false
	for _, l := range strings.Split(all, "\n") {
		t := strings.TrimLeft(l, " ")
		indent := len(l) - len(t)
		if indent == 2 && t != "" {
			in = strings.HasPrefix(t, "recover ")
		}
		if in {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}
