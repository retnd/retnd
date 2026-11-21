package webhost

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// GET /metrics, the authenticated Prometheus scrape endpoint (EPIC O, O1.1,
// #899; FR-99).
//
// # Where it is registered, and why that matters
//
// On the router's root, beside /health/live and /health/ready, and OUTSIDE the
// /api/v1 group. The group carries recordActions across all of it, so a
// 15 second scrape inside it would write about 5,760 action-log rows a day
// into an append-only, cursor-paged feed; it also carries CSRF, the destructive
// gate and (since EPIC U) auditAgentCalls, none of which a scrape has any
// business near. This route has its own checks and they are authentication and
// nothing else. The engine serves it on its own listener; web-ui forwards only
// /health/* and /api/v1/* and this does not change that (decision D1).
//
// # What an unauthenticated stranger learns
//
// Disabled, it answers 404 and nothing else, so a disabled feature is not an
// unauthenticated fingerprint. Enabled, a missing or wrong credential is a 401
// with an empty body. An address that has been refused ten times in a minute is
// answered 429 before the credential is even looked at, so a limited address
// holding a valid token is limited too: the limit bounds what a stranger can
// make this process do, and a real scraper never reaches it because it is never
// refused.
//
// # The credential
//
// Authorization: Bearer <token>, or HTTP basic with the token in the password
// position (Prometheus's basic_auth). The username is ignored and is never
// logged. The token is the rsc_ scrape credential and nothing else: an agent
// rat_ token is not accepted here, and the scrape credential is not accepted by
// /api/v1 (authMiddleware ignores a bearer that is not rat_).

// ScrapeAccess is what the endpoint and the mint route need from core/service.
// *service.BackupService satisfies it; NewRouter finds it by asserting it on the
// backend, the way it finds AgentAccess, so a backend that cannot keep a
// credential serves no /metrics at all.
type ScrapeAccess interface {
	ScrapeEnabled() bool
	VerifyScrapeToken(presented string) bool
	HealthExposition(ctx context.Context, binaryVersion, commit string) (string, error)
	WorkflowMetrics() string
	ObserveScrapeRejected(reason string)
	ScrapeRejectedMetrics() string
	MintScrapeToken(ctx context.Context) (service.MintedScrapeToken, error)
}

// scrapeRefusalsPerMinute is how many refusals one address may cause in a
// minute before it is answered 429, the ceiling agentGuard uses for invalid
// token rows.
const scrapeRefusalsPerMinute = invalidTokenRowsPerMinute

// The refusal reasons, from the closed vocabulary core/internal/metrics holds.
const (
	scrapeRejectedDisabled        = "disabled"
	scrapeRejectedUnauthenticated = "unauthenticated"
	scrapeRejectedMethod          = "method"
)

func newScrapeLimiter() *local.RateLimiter {
	return local.NewRateLimiter(scrapeRefusalsPerMinute, time.Minute)
}

// scrapeCredential reads the presented credential: a bearer token, or the
// password of HTTP basic. The username plays no part.
func scrapeCredential(r *http.Request) (string, bool) {
	scheme, rest, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		return rest, rest != ""
	case strings.EqualFold(scheme, "Basic"):
		decoded, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return "", false
		}
		_, password, found := strings.Cut(string(decoded), ":")

		return password, found && password != ""
	}

	return "", false
}

// scrapeClientAddress is the address the refusal limit is keyed on: the peer,
// and only the peer. TrustForwardedHeaders does not apply here. It is safe on
// /api/v1 because web-ui is the peer in front of it and rewrites
// X-Forwarded-For, but web-ui never forwards /metrics (ADR 0035, decision 5),
// so a forwarded header on a scrape was written by whoever sent it. Keyed on
// it, a stranger could name the real scraper's address and lock it out, or
// rotate the value and never be limited.
func scrapeClientAddress(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}

	return r.RemoteAddr
}

// metrics is GET /metrics.
func (h *handlers) metrics(w http.ResponseWriter, r *http.Request) {
	// Every answer, the 200 and each refusal, so a cache or a browser history
	// entry never holds the exposition.
	w.Header().Set("Cache-Control", "no-store")

	scrape := h.scrape
	if scrape == nil {
		w.WriteHeader(http.StatusNotFound)

		return
	}
	if !scrape.ScrapeEnabled() {
		scrape.ObserveScrapeRejected(scrapeRejectedDisabled)
		w.WriteHeader(http.StatusNotFound)

		return
	}

	address := scrapeClientAddress(r)
	if wait := h.scrapeLimiter.RetryAfter(address); wait > 0 {
		// Before the credential is looked at, so a valid token from a limited
		// address is limited too. Counted as unauthenticated: the closed
		// vocabulary does not grow for it.
		scrape.ObserveScrapeRejected(scrapeRejectedUnauthenticated)
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		w.WriteHeader(http.StatusTooManyRequests)

		return
	}

	presented, ok := scrapeCredential(r)
	if !ok || !scrape.VerifyScrapeToken(presented) {
		h.scrapeLimiter.Allow(address)
		scrape.ObserveScrapeRejected(scrapeRejectedUnauthenticated)
		w.WriteHeader(http.StatusUnauthorized)

		return
	}

	if r.Method != http.MethodGet {
		scrape.ObserveScrapeRejected(scrapeRejectedMethod)
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	health, err := scrape.HealthExposition(r.Context(), h.binaryVersion, h.commit)
	if err != nil {
		h.internalError(w, r, "INTERNAL", "failed to build the metrics exposition", err)

		return
	}

	var body strings.Builder
	body.WriteString(health)
	// The workflow half is absent when WorkflowMetrics is "", never zero: "no
	// workflow has failed here" and "this process runs no workflows" are
	// different statements and a dashboard cannot tell them apart from numbers.
	if workflow := scrape.WorkflowMetrics(); workflow != "" {
		body.WriteString("\n" + workflow)
	}
	body.WriteString("\n" + scrape.ScrapeRejectedMetrics())

	w.Header().Set("Content-Type", service.ScrapeContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body.String()))
}

// mintScrapeToken is POST /api/v1/settings/observability/scrape-token.
//
// It returns the plaintext once, in this response and nowhere else, and the
// response is Cache-Control: no-store. Minting again replaces the earlier
// credential at once. Over plain HTTP from another machine the route needs the
// acknowledgement an agent-token mint does (requireMintTransportAck, in the
// router), and it is on the agent deny list, so no token can reach it.
func (h *handlers) mintScrapeToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.scrape == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "this deployment cannot keep a scrape credential")

		return
	}
	minted, err := h.scrape.MintScrapeToken(r.Context())
	switch {
	case errors.Is(err, service.ErrConfigNotFileBacked):
		writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "this deployment has no configuration file to keep a scrape credential beside")
	case err != nil:
		h.internalError(w, r, "INTERNAL", "failed to mint a scrape credential", err)
	default:
		created := minted.State.CreatedAt.UTC().Format(time.RFC3339)
		writeJSON(w, http.StatusCreated, apicontract.MintedScrapeToken{
			Token:       minted.Token,
			ScrapeToken: apicontract.ScrapeTokenState{Exists: true, Prefix: minted.State.Prefix, CreatedAt: &created},
		})
	}
}
