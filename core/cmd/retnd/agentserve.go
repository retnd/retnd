package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/retnd/retnd/core/agentmcp"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/service"
)

// `retnd agent serve --stdio --token-file PATH` (EPIC U, U1.4, #1347; FR-94
// and FR-96).
//
// It speaks the Model Context Protocol on standard input and output and does
// nothing but call a serving engine's /api/v1 with an agent token as a
// bearer. It starts no engine, opens no database and reads no config.yaml:
// the engine is the one at $RETND_API_URL, and with none answering every tool
// answers the apiclient's unreachable refusal, exit 1, and nothing else.
//
// # The token never travels on the command line
//
// Standard input is the protocol, so the token cannot arrive there, and a
// secret on a command line lands in a shell history and a process listing.
// So the verb takes a file (--token-file, mode 0600 or tighter, refused
// otherwise) or the environment ($RETND_AGENT_TOKEN, for the agent hosts that
// can pass only one). There is no --token flag, and
// TestTheTokenNeverTravelsOnTheCommandLine reads the flag set to say so.
//
// # Standard output is the protocol
//
// Nothing else is written there. Logs, and every refusal of this verb itself,
// go to standard error.

// agentTokenEnv is the alternative to --token-file.
const agentTokenEnv = "RETND_AGENT_TOKEN"

// agentServeFlags builds the flag set of `agent serve`, in one place so the
// guard that no flag takes a token reads the same set the verb parses.
func agentServeFlags() (fs *flag.FlagSet, stdio *bool, tokenFile *string) {
	fs = flag.NewFlagSet("agent serve", flag.ContinueOnError)
	stdio = fs.Bool("stdio", false, "serve MCP on standard input and output (the only transport this verb has)")
	tokenFile = fs.String("token-file", "", "a file holding the agent token, readable by its owner only (mode 0600)")
	return fs, stdio, tokenFile
}

// readAgentToken reads the token from the file when one is named and from
// $RETND_AGENT_TOKEN otherwise. A file readable by anyone but its owner is
// refused before a byte of it is read, and the token itself is never named in
// an error.
func readAgentToken(path string, getenv func(string) string) (string, error) {
	var raw string
	switch {
	case path != "":
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("--token-file: %w", err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return "", fmt.Errorf("--token-file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("--token-file %s is not a regular file", path)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("--token-file %s is readable by users other than its owner (mode %04o), so the token in it is not private; run chmod 600 on it", path, info.Mode().Perm())
		}
		b, err := io.ReadAll(io.LimitReader(f, 4096))
		if err != nil {
			return "", fmt.Errorf("--token-file: %w", err)
		}
		raw = string(b)
	case getenv(agentTokenEnv) != "":
		raw = getenv(agentTokenEnv)
	default:
		return "", fmt.Errorf("no agent token: name a file with --token-file, or set $%s", agentTokenEnv)
	}
	token := strings.TrimSpace(raw)
	if !strings.HasPrefix(token, service.AgentTokenPrefix) || strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("that is not an agent token: one starts with %s and is a single word; `retnd agent token create` mints one", service.AgentTokenPrefix)
	}
	return token, nil
}

// engineCaller makes the catalogue's calls through the apiclient, as the
// token's principal.
type engineCaller struct{ client *apiclient.Client }

func (c engineCaller) Call(ctx context.Context, call agentmcp.Call) (agentmcp.Response, error) {
	body, err := c.client.CallRoute(ctx, call.Method, call.Route, call.PathArgs, call.Query, nil)
	var refused *apiclient.Error
	if errors.As(err, &refused) {
		return agentmcp.Response{}, &agentmcp.Refusal{Status: refused.Status, Code: string(refused.Code), Message: refused.Message, CorrelationID: refused.CorrelationID}
	}
	return agentmcp.Response{Body: body}, err
}

func agentServe(args []string) int {
	fs, stdio, tokenFile := agentServeFlags()
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("agent serve: takes no operands; the token is named with --token-file or $%s", agentTokenEnv)
	}
	if !*stdio {
		return usageError("agent serve: --stdio is the only transport this verb serves; the streamable HTTP transport is the engine's own, at /api/v1/agent/mcp")
	}
	token, err := readAgentToken(*tokenFile, os.Getenv)
	if err != nil {
		return usageError("agent serve: %v", err)
	}
	urlEnv, base := routeAddress()
	if base == "" {
		return usageError("agent serve: $%s is not set; it names the engine this serves for, such as http://127.0.0.1:8080", urlEnv)
	}
	transport, err := routeHTTPClient()
	if err != nil {
		return fail(fmt.Errorf("$%s could not be used: %w", apiCAFileEnv, err))
	}
	client, err := apiclient.New(apiclient.Config{
		BaseURL:                      base,
		HTTPClient:                   transport,
		AcknowledgeInsecureTransport: insecureTransportAcknowledged(),
		BearerToken:                  token,
		UserAgent:                    fmt.Sprintf("retnd-agent/%s (api %s)", version, apicontract.Version),
	})
	if err != nil {
		return fail(err)
	}

	server := agentmcp.New(agentmcp.Config{Caller: engineCaller{client: client}, Version: version})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		return fail(fmt.Errorf("agent serve: %w", err))
	}
	return exitOK
}
