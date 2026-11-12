package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/retnd/retnd/core/agentmcp"
	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/service"
)

// The streamable HTTP transport (EPIC U, U1.4, #1347; FR-94 and FR-96):
// POST, GET and DELETE /api/v1/agent/mcp.
//
// It is mounted inside the /api/v1 group, so RequestScope, authMiddleware and
// recordActions apply to it unchanged and so does the serve-ui proxy rule for
// /api/v1. It adds no listener and no process. It is off unless the operator
// wrote agents.http_transport: on, because a long-lived bearer token on a
// stream that may be plain HTTP is a worse exposure than a session cookie that
// expires (ADR 0034 D1).
//
// It is bearer-only. authMiddleware has already decided who the caller is; a
// request that got here on a cookie session is refused, because the cookie is
// a credential a browser sends by itself and an MCP endpoint is not a page.
//
// The handler raises markSelfAudited, so one JSON-RPC frame is not an action
// log row. Each tool call is a request of its own into /api/v1, and that is
// where it is authenticated, scope-checked, rate limited and audited, exactly
// as it would be from stdio.

// agentToolRows are the rows the endpoint offers as tools. Nil means the
// catalogue's own read tools; it is a variable so a test can offer a table of
// its own.
var agentToolRows []agenttools.Row

// mcpEndpoint answers /agent/mcp. root is the router that serves the whole
// /api/v1 surface; a tool call re-enters it.
func (h *handlers) mcpEndpoint(root http.Handler, policy service.AgentPolicy, guard *agentGuard) http.HandlerFunc {
	var served http.Handler
	var build sync.Once
	return func(w http.ResponseWriter, r *http.Request) {
		markSelfAudited(r.Context())
		if !policy.HTTPTransport {
			writeError(w, http.StatusForbidden, "AGENT_HTTP_TRANSPORT_OFF",
				"the streamable HTTP transport is off; an operator turns it on with agents.http_transport: on in config.yaml, and `retnd agent serve --stdio` works without it")
			return
		}
		principal, ok := agentPrincipalFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "AGENT_TOKEN_INVALID",
				"this endpoint takes an agent token and nothing else; a signed-in session is not one")
			return
		}
		if r.Method == http.MethodGet {
			release, ok := guard.openStream(principal.TokenID)
			if !ok {
				writeError(w, http.StatusTooManyRequests, "AGENT_RATE_LIMITED",
					"this token already holds as many event streams as it may; close one and ask again")
				return
			}
			defer release()
			// A revocation takes effect on the next request (FR-91), and an
			// open event stream makes none, so the stream checks its own
			// token while it is open and ends when the token does.
			if presented, ok := bearerToken(r); ok && h.agents != nil {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				go endStreamWithToken(ctx, cancel, h.agents, presented)
				r = r.WithContext(ctx)
			}
		}
		// The peer travels in a header the transport hands back with each
		// call, and a client cannot choose it: whatever one sent is replaced.
		r.Header.Set(mcpPeerHeader, r.RemoteAddr)
		build.Do(func() {
			srv := agentmcp.New(agentmcp.Config{Tools: agentToolRows, Caller: routerCaller{root: root}, Version: h.binaryVersion})
			served = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: true})
		})
		served.ServeHTTP(w, r)
	}
}

// routerCaller makes a tool call by serving a request through the /api/v1
// router with the Authorization header of the frame that carried the call, so
// the token is checked again, per call, by the same code that checks it for
// every other client.
type routerCaller struct{ root http.Handler }

func (c routerCaller) Call(ctx context.Context, call agentmcp.Call) (agentmcp.Response, error) {
	authorization := agentmcp.RequestHeader(ctx).Get("Authorization")
	if authorization == "" {
		return agentmcp.Response{}, &agentmcp.Refusal{Status: http.StatusUnauthorized, Code: "AGENT_TOKEN_INVALID", Message: "the agent token is not valid"}
	}
	target, err := routeURL(call)
	if err != nil {
		return agentmcp.Response{}, &agentmcp.Refusal{Status: http.StatusBadRequest, Code: "INVALID_REQUEST", Message: err.Error()}
	}
	// A request of its own: it keeps the caller's cancellation and nothing
	// else of the frame's context, in particular not the flag that says the
	// frame is audited.
	inner, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	req, err := http.NewRequestWithContext(inner, call.Method, target, nil)
	if err != nil {
		return agentmcp.Response{}, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "retnd-mcp-http")
	// Said again here and never copied from the frame: the audit row for this
	// request names the tool and the transport that really made it.
	req.Header.Set(service.AgentToolHeader, call.Tool)
	req.Header.Set(service.AgentTransportHeader, "http")
	if h := agentmcp.RequestHeader(ctx); h != nil {
		if fwd := h.Get("X-Forwarded-For"); fwd != "" {
			req.Header.Set("X-Forwarded-For", fwd)
		}
	}
	req.RemoteAddr = agentmcp.RequestHeader(ctx).Get(mcpPeerHeader)

	rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
	c.root.ServeHTTP(rec, req)
	if rec.status >= 200 && rec.status < 300 {
		return agentmcp.Response{Body: rec.body.Bytes(), CorrelationID: rec.header.Get("X-Correlation-Id")}, nil
	}
	return agentmcp.Response{}, refusalFrom(rec)
}

// mcpPeerHeader carries the address of the peer of the frame a call arrived
// on. The per-address bounds key on it, so a call re-entering the router does
// not look like it came from nowhere.
const mcpPeerHeader = "X-Retnd-Mcp-Peer"

// routeURL builds /api/v1 + the route with its placeholders filled, each one
// escaped as a value, and the query.
func routeURL(call agentmcp.Call) (string, error) {
	var b strings.Builder
	b.WriteString("/api/v1")
	rest, used := call.Route, 0
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			b.WriteString(rest)
			break
		}
		end := strings.IndexByte(rest[open:], '}')
		if end < 0 || used >= len(call.PathArgs) {
			return "", errors.New("the route's placeholders and the arguments do not match")
		}
		arg := call.PathArgs[used]
		if arg == "" || arg == "." || arg == ".." {
			return "", errors.New("a path argument is empty or a relative segment")
		}
		b.WriteString(rest[:open])
		b.WriteString(url.PathEscape(arg))
		used++
		rest = rest[open+end+1:]
	}
	if used != len(call.PathArgs) {
		return "", errors.New("the route's placeholders and the arguments do not match")
	}
	if len(call.Query) > 0 {
		b.WriteString("?" + call.Query.Encode())
	}
	return b.String(), nil
}

// refusalFrom reads the API's error envelope off a buffered answer.
func refusalFrom(rec *bufferedResponse) error {
	var envelope errorResponse
	if err := json.Unmarshal(rec.body.Bytes(), &envelope); err != nil || envelope.Error.Code == "" {
		return errors.New("the engine answered " + http.StatusText(rec.status) + " with a body that is not the API's error envelope")
	}
	return &agentmcp.Refusal{Status: rec.status, Code: envelope.Error.Code, Message: envelope.Error.Message, CorrelationID: rec.header.Get("X-Correlation-Id")}
}

// bufferedResponse is a ResponseWriter that keeps the answer in memory, capped
// at what one tool result could ever carry.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

// maxBufferedBody bounds what one in-process call may hold, the same 32 MiB
// the CLI's client reads at most from one answer. The tool result is bounded
// far below that, by paging; this only keeps a runaway answer out of memory.
const maxBufferedBody = 32 << 20

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(status int) {
	b.status = status
}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.body.Len()+len(p) > maxBufferedBody {
		return 0, io.ErrShortWrite
	}
	return b.body.Write(p)
}

// agentStreamRecheck is how often an open event stream checks its token.
const agentStreamRecheck = time.Second

// endStreamWithToken cancels an event stream once its token no longer
// authenticates (revoked or expired), and returns when the stream ends.
func endStreamWithToken(ctx context.Context, cancel context.CancelFunc, agents AgentAccess, presented string) {
	tick := time.NewTicker(agentStreamRecheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, err := agents.AuthenticateAgentToken(ctx, presented); errors.Is(err, service.ErrAgentTokenInvalid) {
				cancel()
				return
			}
		}
	}
}
