package webhost

import (
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/core/service"
)

// The bounds on agent traffic (EPIC U, U1.4, FR-96): a limit per token, a
// limit on the audit rows an unauthenticated caller can write, and a cap on
// the event streams one token holds open. They live in one value because they
// share one lifetime and one policy, the agents: block as it resolved when the
// router was built.

const (
	// invalidTokenRowsPerMinute is how many audit rows one address may write
	// with requests that name no valid token. The refusal itself is never
	// withheld; only the row is, past the line, so ten thousand garbage
	// tokens from one address cannot grow agent_audit without bound
	// (the #1357 review).
	invalidTokenRowsPerMinute = 10

	// maxStreamsPerToken is how many GET /agent/mcp event streams one token
	// may hold open at once.
	maxStreamsPerToken = 2
)

// agentGuard holds the limiters. A nil *agentGuard limits nothing, which is
// what a router with no token store and the tests that predate the limits get.
type agentGuard struct {
	perToken    *local.RateLimiter
	perTokenMax int
	invalidByIP *local.RateLimiter
	trustFwd    bool

	mu      sync.Mutex
	streams map[string]int
}

func newAgentGuard(policy service.AgentPolicy, trustFwd bool) *agentGuard {
	return &agentGuard{
		perToken:    local.NewRateLimiter(policy.RateLimitPerMinute, time.Minute),
		perTokenMax: policy.RateLimitPerMinute,
		invalidByIP: local.NewRateLimiter(invalidTokenRowsPerMinute, time.Minute),
		trustFwd:    trustFwd,
		streams:     map[string]int{},
	}
}

// clientAddress is the address a request's limits are keyed on: its own peer,
// or the first X-Forwarded-For entry when the deployment says the peer in
// front of it is the project's own proxy.
func (g *agentGuard) clientAddress(r *http.Request) string {
	if g.trustFwd {
		if first := firstForwardedEntry(r.Header.Get("X-Forwarded-For")); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// allowAuditRow reports whether a refusal that names no valid token may write
// its audit row.
func (g *agentGuard) allowAuditRow(r *http.Request) bool {
	return g == nil || g.invalidByIP.Allow(g.clientAddress(r))
}

// allowCall counts one API call against a token's minute and, when it is over,
// says how long to wait.
func (g *agentGuard) allowCall(tokenID string) (bool, time.Duration) {
	if g == nil {
		return true, 0
	}
	if g.perToken.Allow(tokenID) {
		return true, 0
	}
	return false, g.perToken.RetryAfter(tokenID)
}

// openStream takes one of a token's stream slots, and reports false when it
// holds as many as it may. The returned func gives the slot back.
func (g *agentGuard) openStream(tokenID string) (func(), bool) {
	if g == nil {
		return func() {}, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.streams[tokenID] >= maxStreamsPerToken {
		return nil, false
	}
	g.streams[tokenID]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.streams[tokenID]--; g.streams[tokenID] <= 0 {
			delete(g.streams, tokenID)
		}
	}, true
}

// refuseRateLimited writes AGENT_RATE_LIMITED with the wait, and returns the
// correlation id of the refusal.
func (g *agentGuard) refuseRateLimited(w http.ResponseWriter, wait time.Duration) string {
	seconds := max(int((wait+time.Second-1)/time.Second), 1)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	return writeError(w, http.StatusTooManyRequests, "AGENT_RATE_LIMITED",
		"this token has made "+strconv.Itoa(g.perTokenMax)+" calls in the last minute, which is the most it may; wait "+strconv.Itoa(seconds)+" seconds and ask again")
}

// agentMCPPath is the one route the MCP transport lives at, below /api/v1.
const agentMCPPath = "/agent/mcp"

// isAgentMCP reports whether a request is for the MCP endpoint itself. Its
// calls into /api/v1 are what the limiter counts, and what the scope check
// reads; the JSON-RPC frame that carries them is neither.
func isAgentMCP(r *http.Request) bool {
	return strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/api/v1") == agentMCPPath
}
