package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/retnd/retnd/core/agentmcp"
	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/apiclient"
)

// The two refusals that have an exit of their own (50, the destructive gate
// shut, and 52, plain http without the acknowledgement) are not declared by
// listActivity, which TestAToolResultAnswersAsTheCLIWould drives, so that test
// takes its "undeclared, so exit 1" branch for both and never compares them
// (#1378). They are driven here through POST /operations, which declares both,
// and the tool result's exit is held to the CLI's mapping of the same refusal.
//
// Falsify: map DESTRUCTIVE_OPERATIONS_DISABLED to exit 1 in agentmcp.ExitCode,
// or INSECURE_TRANSPORT_ACK_REQUIRED to exit 1 in engineRefusalExit.
func TestAToolResultAnswersAsTheCLIWouldOnTheGateAndTransportExits_1378(t *testing.T) {
	for _, tc := range []struct {
		code apicontract.ErrorCode
		exit int
	}{
		{apicontract.ErrorCodeDestructiveOperationsDisabled, exitEngineGateShut},
		{apicontract.ErrorCodeInsecureTransportAckRequired, exitInsecureTransport},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Correlation-Id", "cid_1378")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": string(tc.code), "message": "words for " + string(tc.code)}})
			}))
			t.Cleanup(srv.Close)
			client, err := apiclient.New(apiclient.Config{BaseURL: srv.URL, BearerToken: serveTestToken})
			if err != nil {
				t.Fatal(err)
			}
			server := agentmcp.New(agentmcp.Config{
				Tools:   []agenttools.Row{{Tool: "run_cycle", Method: "POST", Route: "/operations", Scope: agenttools.ScopeOperate, Description: "Run a cycle."}},
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
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run_cycle"})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(res.StructuredContent)
			var got agentmcp.Result
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}

			refusal := &apiclient.Error{Operation: "submitOperation", Method: "POST", Path: "/operations", Status: 403, Code: tc.code, Message: "words for " + string(tc.code), CorrelationID: "cid_1378"}
			cliExit, _ := engineRefusalExit(refusal)
			if got.OK || got.Code != string(tc.code) {
				t.Fatalf("result %+v, want the refusal %s", got, tc.code)
			}
			if got.ExitCode != cliExit || got.ExitCode != tc.exit {
				t.Errorf("%s: the tool result exits %d, the CLI exits %d, want both %d", tc.code, got.ExitCode, cliExit, tc.exit)
			}
		})
	}
}
