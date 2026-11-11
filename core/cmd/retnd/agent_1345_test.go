package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// `retnd agent token create|list|revoke` (#1345). The verbs are written to be
// red until they exist, and each runs over both doors: with nothing serving,
// and attached to the (fake) engine whose routes are the session-only ones.

// both runs a test body once per door, so a verb that works on one and not the
// other cannot pass.
func bothDoors(t *testing.T, body func(t *testing.T, configPath string)) {
	t.Helper()
	t.Run("direct", func(t *testing.T) { body(t, writeTestConfig(t)) })
	t.Run("through the engine", func(t *testing.T) {
		configPath := writeTestConfig(t)
		startFakeEngineFor(t, configPath).attach(t)
		body(t, configPath)
	})
}

// Falsify: print anything but the token on the token's line, or print it
// anywhere but standard output.
func TestCreatePrintsTheTokenAloneOnStandardOutput(t *testing.T) {
	bothDoors(t, func(t *testing.T, configPath string) {
		code, stdout, stderr := runCLI(t, "agent", "token", "create", "triage", "--config", configPath)
		if code != exitOK {
			t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
		}
		lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "rat_") || strings.ContainsAny(lines[0], " \t") {
			t.Fatalf("stdout = %q, want the token alone on one line", stdout)
		}
		if strings.Contains(stderr, lines[0]) {
			t.Errorf("the token is on standard error too: %s", stderr)
		}
		if !strings.Contains(stderr, "shown once") {
			t.Errorf("standard error does not tell the operator the token is shown once: %s", stderr)
		}
	})
}

func TestListNeverShowsTheTokenOrItsHash(t *testing.T) {
	bothDoors(t, func(t *testing.T, configPath string) {
		_, minted, _ := runCLI(t, "agent", "token", "create", "triage", "--scope", "read", "--expires-in", "30d", "--config", configPath)
		token := strings.TrimSpace(minted)
		if token == "" {
			t.Fatal("nothing was minted")
		}

		code, text, stderr := runCLI(t, "agent", "token", "list", "--config", configPath)
		if code != exitOK {
			t.Fatalf("list exit %d\n%s", code, stderr)
		}
		for _, want := range []string{"triage", token[:8], "read", "active"} {
			if !strings.Contains(text, want) {
				t.Errorf("the list lacks %q:\n%s", want, text)
			}
		}

		code, raw, stderr := runCLI(t, "agent", "token", "list", "--json", "--config", configPath)
		if code != exitOK {
			t.Fatalf("list --json exit %d\n%s", code, stderr)
		}
		for name, out := range map[string]string{"text": text, "json": raw} {
			if strings.Contains(out, token) || strings.Contains(strings.ToLower(out), "hash") {
				t.Errorf("%s list carries the token or its hash:\n%s", name, out)
			}
		}
		var got apicontract.ListAgentTokensResponse
		if err := json.Unmarshal([]byte(raw), &got); err != nil || len(got.Tokens) != 1 || got.Tokens[0].Name != "triage" || got.Tokens[0].Prefix != token[:8] {
			t.Errorf("--json = %s (%v), want the API's own object with one token", raw, err)
		}
	})
}

func TestRevokeEndsTheTokenAndSaysSo(t *testing.T) {
	bothDoors(t, func(t *testing.T, configPath string) {
		_, _, _ = runCLI(t, "agent", "token", "create", "triage", "--config", configPath)
		_, raw, _ := runCLI(t, "agent", "token", "list", "--json", "--config", configPath)
		var listed apicontract.ListAgentTokensResponse
		if err := json.Unmarshal([]byte(raw), &listed); err != nil || len(listed.Tokens) != 1 {
			t.Fatalf("list = %s (%v)", raw, err)
		}

		code, stdout, stderr := runCLI(t, "agent", "token", "revoke", listed.Tokens[0].ID, "--config", configPath)
		if code != exitOK {
			t.Fatalf("revoke exit %d\n%s\n%s", code, stdout, stderr)
		}
		_, after, _ := runCLI(t, "agent", "token", "list", "--json", "--config", configPath)
		var got apicontract.ListAgentTokensResponse
		if err := json.Unmarshal([]byte(after), &got); err != nil || got.Tokens[0].Status != "revoked" {
			t.Errorf("after the revoke: %s (%v)", after, err)
		}
	})
}

func TestTheVerbsRefuseTheWayTheServiceDoes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"no name", []string{"agent", "token", "create"}, exitUsage},
		{"two names", []string{"agent", "token", "create", "a", "b"}, exitUsage},
		{"no id to revoke", []string{"agent", "token", "revoke"}, exitUsage},
		{"no subcommand", []string{"agent", "token"}, exitUsage},
		{"no token verb", []string{"agent"}, exitUsage},
		{"unknown scope", []string{"agent", "token", "create", "x", "--scope", "admin"}, exitFailure},
		{"past a year", []string{"agent", "token", "create", "x", "--expires-in", "366d"}, exitFailure},
		{"never", []string{"agent", "token", "create", "x", "--expires-in", "never"}, exitFailure},
		{"unknown id", []string{"agent", "token", "revoke", "agt_nope"}, exitFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(tc.args, "--config", writeTestConfig(t))
			if code, stdout, stderr := runCLI(t, args...); code != tc.code {
				t.Errorf("exit %d, want %d\n%s\n%s", code, tc.code, stdout, stderr)
			}
		})
	}
}

// Falsify: add a --token flag, or any flag that takes a secret.
func TestNoVerbTakesATokenOnItsCommandLine(t *testing.T) {
	for _, sub := range [][]string{{"agent", "token", "create"}, {"agent", "token", "list"}, {"agent", "token", "revoke"}} {
		args := append(append([]string{}, sub...), "--token", "rat_whatever", "x")
		if code, _, _ := runCLI(t, args...); code != exitUsage {
			t.Errorf("%v accepted a --token flag (exit %d)", sub, code)
		}
	}
}
