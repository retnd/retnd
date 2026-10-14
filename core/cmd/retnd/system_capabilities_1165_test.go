package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/cliecho"
)

// #1165: `retnd system capabilities [--json]`, so the terminal can ask what
// the serving engine reports, including whether its destructive gate is open.
//
// The gate is the engine's, not the CLI's. The CLI itself has no gate (the
// verbs the web UI sends people to run without one), so the only meaningful
// answer to "is destructive_operations open" comes from the process serving
// the deployment, which is why this verb is routed and refuses without a
// route, like operation list.

func (e *fakeEngine) getCapabilities(w http.ResponseWriter, _ *http.Request) {
	e.capsMu.Lock()
	defer e.capsMu.Unlock()
	writeJSON(w, http.StatusOK, e.caps)
}

func (e *fakeEngine) holdCapabilities(caps map[string]any) {
	e.capsMu.Lock()
	defer e.capsMu.Unlock()
	e.caps = caps
}

func shutCapabilities() map[string]any {
	return map[string]any{
		"platform": "generic", "native_auth": false, "native_notifications": false,
		"storage_picker": false, "embedded_window": false, "app_store_packaging": false,
		"destructive_operations": map[string]any{"state": "shut", "reason": "gate shut until #92"},
	}
}

func TestSystemCapabilities_RoutedAndJSONIsTheWireObject(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)
	want := shutCapabilities()
	e.holdCapabilities(want)

	code, out, errOut := runOp(t, cfg, "system", "capabilities", "--json")
	if code != 0 {
		t.Fatalf("system capabilities --json exited %d\nstderr: %s", code, errOut)
	}
	if got := decodeJSON(t, out); !reflect.DeepEqual(got, wireOf(t, want)) {
		t.Errorf("--json is not the object the engine served\n got: %v\nwant: %v", got, want)
	}
}

// What the field says is what is printed, in both states: a CLI that printed
// "shut" for everything would pass the first row and fail the second.
func TestSystemCapabilities_TheTextShowsTheGateStateAndItsReason(t *testing.T) {
	cfg := writeTestConfig(t)
	e := startFakeEngine(t, cfg)
	e.attach(t)

	for _, tc := range []struct {
		state, reason string
		wantLine      string
	}{
		{"shut", "gate shut until #92", "destructive operations: shut (gate shut until #92)"},
		{"open", "", "destructive operations: open"},
	} {
		caps := shutCapabilities()
		caps["destructive_operations"] = map[string]any{"state": tc.state, "reason": tc.reason}
		e.holdCapabilities(caps)
		code, out, errOut := runOp(t, cfg, "system", "capabilities")
		if code != 0 {
			t.Fatalf("%s: exited %d\nstderr: %s", tc.state, code, errOut)
		}
		if !strings.Contains(out, tc.wantLine) {
			t.Errorf("%s: output does not say %q:\n%s", tc.state, tc.wantLine, out)
		}
		if tc.state == "open" && strings.Contains(out, "shut") {
			t.Errorf("an open gate printed the word shut:\n%s", out)
		}
		if !strings.Contains(out, "platform: generic") {
			t.Errorf("%s: output has no platform line:\n%s", tc.state, out)
		}
	}
}

func TestSystemCapabilities_RefusesWithoutARouteToTheEngine(t *testing.T) {
	cfg := writeTestConfig(t)
	code, _, errOut := runOp(t, cfg, "system", "capabilities")
	if code != exitFailure {
		t.Fatalf("exited %d with nothing serving, want exitFailure (a refusal, not a usage error): the gate's state is the engine's to report, and there is no engine", code)
	}
	if !strings.Contains(errOut, "system capabilities") || !strings.Contains(errOut, apiURLEnv) {
		t.Errorf("the refusal does not say which command it is about and name %s:\n%s", apiURLEnv, errOut)
	}
}

func TestSystemCapabilities_UsageErrors(t *testing.T) {
	cfg := writeTestConfig(t)
	for _, args := range [][]string{{"system"}, {"system", "bogus"}, {"system", "capabilities", "extra"}} {
		code, _, _ := runOp(t, cfg, args...)
		if code != exitUsage {
			t.Errorf("%v exited %d, want the usage code %d", args, code, exitUsage)
		}
	}
}

func TestSystemCapabilities_IsInUsageAndHasACliechoEntry(t *testing.T) {
	all := captureStderr(t, usage)
	for _, want := range []string{"system capabilities", "GET /api/v1/system/capabilities", "destructive_operations"} {
		if !strings.Contains(all, want) {
			t.Errorf("usage() does not mention %q", want)
		}
	}
	line := cliecho.Echo(cliecho.Action{Method: "GET", Route: "/system/capabilities"})
	if got := strings.Join(line.Command, " "); !strings.Contains(got, "system capabilities") {
		t.Errorf("cliecho for GET /system/capabilities echoed %q (gap: %q), want a system capabilities command", got, line.Gap)
	}
}
