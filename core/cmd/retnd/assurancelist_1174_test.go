package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// #1174: `retnd assurance list` is the terminal's twin of the Dashboard's
// Assurance tile. The classification is pinned in core/service; these pin the
// verb: its forms, its refusals and its JSON shape.

func TestAssuranceList_EmptyDeploymentSaysSoInTextAndJSON(t *testing.T) {
	configPath := writeTestConfig(t)
	var code int
	text := captureStdout(t, func() { code = run([]string{"assurance", "list", "--config", configPath}) })
	if code != exitOK || !strings.Contains(text, "nothing is due, overdue or degraded") {
		t.Fatalf("text: exit %d, %q", code, text)
	}
	out := captureStdout(t, func() { code = run([]string{"assurance", "list", "--json", "--config", configPath}) })
	var doc struct {
		GeneratedAt string `json:"generated_at"`
		Items       []any  `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != exitOK || doc.Items == nil || doc.GeneratedAt == "" {
		t.Fatalf("json: exit %d, %q (%v)", code, out, err)
	}
}

func TestAssuranceList_RefusesWhatItCannotAnswer(t *testing.T) {
	configPath := writeTestConfig(t)
	for name, argv := range map[string][]string{
		"a bucket that does not exist": {"assurance", "list", "--bucket", "stale"},
		"a set that is not an id":      {"assurance", "list", "--backup-set", "nope"},
		"a stray operand":              {"assurance", "list", "extra"},
		"list flags on cancel":         {"assurance", "cancel", "op-1", "--bucket", "due"},
		"no form at all":               {"assurance"},
	} {
		if code := run(append(argv, "--config", configPath)); code != exitUsage {
			t.Errorf("%s exited %d, want usage (%d)", name, code, exitUsage)
		}
	}
}

func TestAssuranceList_UsageNamesTheVerbAndItsTwin(t *testing.T) {
	text := captureStderr(t, usage)
	for _, want := range []string{"assurance list [--backup-set S] [--bucket due|overdue|degraded|untested] [--json]", "Assurance tile"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}
