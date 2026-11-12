package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/apicontract"
)

// `retnd agent tools [--json]` (#1346): the catalogue from the terminal. It is
// the object GET /api/v1/agent/tools answers (apps/common/webhost holds the
// other half of that equality against the same function), and it needs no
// engine, no token and no configuration.

// Falsify: drop one tool from this surface.
func TestAgentToolsJSONIsTheCatalogueDocument(t *testing.T) {
	code, stdout, stderr := runCLI(t, "agent", "tools", "--json")
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	var got apicontract.AgentToolsResponse
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("--json is not the API's object: %v\n%s", err, stdout)
	}
	if want := agenttools.Document(); !reflect.DeepEqual(got, want) {
		t.Errorf("agent tools --json is not agenttools.Document():\n got %+v\nwant %+v", got, want)
	}
	if len(got.Tools) != 30 {
		t.Errorf("the catalogue prints %d tools, the spec names 30", len(got.Tools))
	}
}

func TestAgentToolsTextNamesEachToolItsScopeRouteTwinAndUntrustedFields(t *testing.T) {
	code, stdout, stderr := runCLI(t, "agent", "tools")
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	for _, tool := range agenttools.Document().Tools {
		var line string
		for _, l := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(l, tool.Name+" ") {
				line = l
			}
		}
		if line == "" {
			t.Errorf("no line for %s:\n%s", tool.Name, stdout)
			continue
		}
		for _, want := range append([]string{tool.Scope, tool.Method + " " + tool.Route, tool.Twin}, tool.Untrusted...) {
			if !strings.Contains(line, want) {
				t.Errorf("the line for %s lacks %q: %s", tool.Name, want, line)
			}
		}
	}
	for _, want := range []string{"retnd://status", "triage-health", "POST /encryption/keys/{id}/export"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the text lacks %q", want)
		}
	}
}

func TestAgentToolsNeedsNoEngineConfigurationOrToken(t *testing.T) {
	t.Setenv("RETND_AGENT_TOKEN", "")
	if code, _, stderr := runCLI(t, "agent", "tools", "--config", "/nonexistent/retnd.yaml"); code != exitOK {
		t.Errorf("exit %d with no configuration: %s", code, stderr)
	}
}

func TestAgentToolsRefusesOperandsAndAFlagThatTakesASecret(t *testing.T) {
	if code, _, _ := runCLI(t, "agent", "tools", "extra"); code != exitUsage {
		t.Errorf("an operand exited %d, want usage", code)
	}
	if code, _, _ := runCLI(t, "agent", "tools", "--token", "rat_x"); code != exitUsage {
		t.Errorf("a --token flag exited %d, want usage", code)
	}
}

// A refusal's reason is a claim about this binary, and the catalogue's
// sentences are held to the same rule cliecho's are: none names a verb the
// binary dispatches.
//
// Falsify: write a reason that quotes `retnd status`.
func TestNoCatalogueReasonClaimsAVerbThisBinaryShips(t *testing.T) {
	checked := 0
	for _, r := range agenttools.Rows() {
		if r.Tool != "" {
			continue
		}
		checked++
		if verbs := shippedVerbsNamedIn(r.Why); len(verbs) != 0 {
			t.Errorf("the reason for %s names %v, which this binary dispatches: %s", r.Key(), verbs, r.Why)
		}
	}
	if checked == 0 {
		t.Fatal("the catalogue holds no refusal, so this checked nothing")
	}
	if got := shippedVerbsNamedIn("an agent reads this with `retnd status` instead"); len(got) == 0 {
		t.Error("the reader no longer sees a quoted shipped verb, so this test could never fail")
	}
}
