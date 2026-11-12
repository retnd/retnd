package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/retnd/retnd/core/agentmcp"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/apiclient"
)

// `retnd agent serve --stdio --token-file PATH` (#1347). Written red first.

const serveTestToken = "rat_serve_test_token_0000000000000000000000"

func tokenFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.token")
	if err := os.WriteFile(path, []byte(serveTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// Falsify: add a --token flag, or accept a file readable by others.
func TestTheTokenNeverTravelsOnTheCommandLine(t *testing.T) {
	fs, stdio, tokenFileFlag := agentServeFlags()
	if stdio == nil || tokenFileFlag == nil {
		t.Fatal("the flag set lost --stdio or --token-file")
	}
	var names []string
	fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	for _, name := range names {
		if name != "stdio" && name != "token-file" {
			t.Errorf("agent serve has a flag --%s; only --stdio and --token-file may exist, and none may take a token", name)
		}
		if strings.Contains(name, "token") && name != "token-file" {
			t.Errorf("--%s looks like a flag that takes a token", name)
		}
	}

	if code, _, stderr := runCLI(t, "agent", "serve", "--stdio", "--token", serveTestToken); code != exitUsage {
		t.Errorf("--token exited %d, want %d (a flag that does not exist)\n%s", code, exitUsage, stderr)
	}

	usage := captureStderr(t, usage)
	start := strings.Index(usage, "agent serve --stdio")
	end := strings.Index(usage, "ssh host-key-probe")
	if start < 0 || end < start || strings.Contains(usage[start:end], "--token ") {
		t.Errorf("the usage entry for agent serve is missing or documents a --token flag")
	}
}

func TestTokenFileMustBePrivate(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		path := tokenFile(t, mode)
		if _, err := readAgentToken(path, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "readable by users other than its owner") {
			t.Errorf("mode %04o: want a refusal about the file's mode, got %v", mode, err)
		}
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		got, err := readAgentToken(tokenFile(t, mode), func(string) string { return "" })
		if err != nil || got != serveTestToken {
			t.Errorf("mode %04o: got %q, %v, want the token", mode, got, err)
		}
	}
}

func TestTheTokenCanComeFromTheEnvironmentAndNothingElse(t *testing.T) {
	env := func(k string) string {
		if k == agentTokenEnv {
			return "  " + serveTestToken + "\n"
		}
		return ""
	}
	if got, err := readAgentToken("", env); err != nil || got != serveTestToken {
		t.Errorf("env: got %q, %v", got, err)
	}
	if _, err := readAgentToken("", func(string) string { return "" }); err == nil {
		t.Error("with no file and no environment there is no token, and that must be refused")
	}
	for _, bad := range []string{"not-a-token", "Bearer " + serveTestToken, "rat_two words", ""} {
		file := filepath.Join(t.TempDir(), "t")
		if err := os.WriteFile(file, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := readAgentToken(file, func(string) string { return "" })
		if err == nil {
			t.Errorf("%q was accepted as a token", bad)
		} else if strings.Contains(err.Error(), bad) && bad != "" {
			t.Errorf("the refusal echoes the contents of the file: %v", err)
		}
	}
}

func TestServeNeedsAnEngineAndTheStdioFlag(t *testing.T) {
	clearInheritedRouteSettings()
	path := tokenFile(t, 0o600)
	if code, _, stderr := runCLI(t, "agent", "serve", "--token-file", path); code != exitUsage || !strings.Contains(stderr, "--stdio") {
		t.Errorf("no --stdio exited %d: %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, "agent", "serve", "--stdio", "--token-file", path); code != exitUsage || !strings.Contains(stderr, apiURLEnv) {
		t.Errorf("no engine address exited %d: %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, "agent", "serve", "--stdio", "--token-file", tokenFile(t, 0o644)); code != exitUsage || strings.Contains(stderr, serveTestToken) {
		t.Errorf("a readable token file exited %d: %s", code, stderr)
	}
}

// toolsOver is a server over the real engine caller, pointed at an engine
// that answers what the test says.
func toolsOver(t *testing.T, engine http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	client, err := apiclient.New(apiclient.Config{BaseURL: srv.URL, BearerToken: serveTestToken})
	if err != nil {
		t.Fatal(err)
	}
	server := agentmcp.New(agentmcp.Config{
		Tools:   []agentmcp.Tool{{Name: "activity", Method: "GET", Route: "/activity", Description: "Recent events."}},
		Project: func(v any) any { return v },
		Caller:  engineCaller{client: client},
		Version: "test",
	})
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolResult(t *testing.T, cs *mcp.ClientSession) agentmcp.Result {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "activity"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out agentmcp.Result
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTheStdioServerCallsTheEngineAsTheBearer(t *testing.T) {
	var gotAuth, gotPath string
	cs := toolsOver(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	})
	got := toolResult(t, cs)
	if !got.OK || gotAuth != "Bearer "+serveTestToken || gotPath != "/api/v1/activity" {
		t.Fatalf("result %+v, auth %q, path %q", got, gotAuth, gotPath)
	}
}

// Every refusal an engine can answer a bearer with, put through the CLI's own
// exit mapping and through the tool result: code, message, correlation id and
// exit code must be the same, because they are the same words.
//
// Falsify: map one refusal to a different exit code in agentmcp.ExitCode.
func TestAToolResultAnswersAsTheCLIWould(t *testing.T) {
	codes := append([]apicontract.ErrorCode{}, apicontract.WireErrorCodes...)
	codes = append(codes, "AGENT_RATE_LIMITED", "AGENT_HTTP_TRANSPORT_OFF", "AGENT_TOKEN_INVALID", "AGENT_SCOPE_INSUFFICIENT")
	for _, code := range codes {
		t.Run(string(code), func(t *testing.T) {
			cs := toolsOver(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Correlation-Id", "cid_parity")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": string(code), "message": "words for " + string(code)}})
			})
			got := toolResult(t, cs)

			// What the CLI does with the same refusal, built the way the
			// apiclient builds it.
			refusal := &apiclient.Error{Operation: "listActivity", Method: "GET", Path: "/activity", Status: 401, Code: code, Message: "words for " + string(code), CorrelationID: "cid_parity"}
			wantExit, _ := engineRefusalExit(refusal)

			// An undeclared code is the apiclient's contract violation, which
			// is exit 1 on both sides and has no code of its own.
			if got.Code != string(code) {
				if got.ExitCode != 1 || wantExit != 1 {
					t.Fatalf("%s: result %+v, CLI exit %d", code, got, wantExit)
				}
				return
			}
			if got.OK || got.ExitCode != wantExit || got.Message != refusal.Message || got.CorrelationID != refusal.CorrelationID {
				t.Errorf("%s: result code %q exit %d message %q correlation %q; CLI exit %d message %q correlation %q",
					code, got.Code, got.ExitCode, got.Message, got.CorrelationID, wantExit, refusal.Message, refusal.CorrelationID)
			}
		})
	}
}

func TestWithNoEngineEveryToolAnswersUnreachableExitOne(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	client, err := apiclient.New(apiclient.Config{BaseURL: url, BearerToken: serveTestToken})
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := engineCaller{client: client}.Call(context.Background(), agentmcp.Call{Method: "GET", Route: "/activity"})
	if callErr == nil {
		t.Fatal("a stopped engine answered")
	}
	if code, _ := engineRefusalExit(callErr); code != 1 {
		t.Errorf("the CLI exits %d for an unreachable engine, want 1", code)
	}
}
