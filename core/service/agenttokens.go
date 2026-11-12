package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/state"
)

// Agent tokens: the first principal in this tree that is not a person
// (EPIC U, FR-91 and FR-92; ADR 0034 decision 2).
//
// A token is minted by a signed-in operator, shown once, and stored as its
// SHA-256 plus an eight-character prefix, the same custody shape as the
// verification token in apps/common/auth/local. Its status is read from the
// row on every request; nothing here caches a decision, so a revoke takes
// effect on the next request.

// AgentScope words, the closed vocabulary. Read is the only default.
const (
	AgentScopeRead      = "read"
	AgentScopeOperate   = "operate"
	AgentScopeConfigure = "configure"
)

const (
	// DefaultAgentTokenLifetime is how long a token lives when the operator
	// does not say (owner decision D9).
	DefaultAgentTokenLifetime = 90 * 24 * time.Hour
	// MaxAgentTokenLifetime is the ceiling. There is no non-expiring token.
	MaxAgentTokenLifetime = 365 * 24 * time.Hour

	// AgentTokenPrefix is what every agent token starts with. The bearer
	// authenticator treats an Authorization: Bearer value as an agent token
	// only if it carries it; any other value is not ours.
	AgentTokenPrefix = "rat_"
	// agentTokenPrefixLen is the length of the stored prefix: "rat_" and four
	// characters of the secret part.
	agentTokenPrefixLen = 8
)

// ErrAgentTokenInvalid is returned by AuthenticateAgentToken for a token that
// is unknown, expired or revoked. It deliberately does not say which, so a
// caller probing for tokens learns nothing from the difference.
var ErrAgentTokenInvalid = errors.New("service: the agent token is not valid")

// ErrAgentTokenNotFound is returned by RevokeAgentToken for an unknown id.
var ErrAgentTokenNotFound = errors.New("service: no such agent token")

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// MintAgentTokenRequest is what an operator supplies to mint a token. Owner is
// the signed-in user who minted it, taken from the session and never from a
// request body.
type MintAgentTokenRequest struct {
	Name  string
	Scope string
	// ExpiresIn is a Go duration or a whole number of days ("90d"). Empty
	// means DefaultAgentTokenLifetime.
	ExpiresIn string
	Owner     string
}

// AgentToken is a token as every surface after the mint may show it: never the
// hash, and the prefix is all of the token it carries.
type AgentToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      string     `json:"scope"`
	Owner      string     `json:"owner"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	// Status is active, expired or revoked, derived from the row at read time.
	Status string `json:"status"`
}

// Contract is the token as /api/v1 shows it. The web host and the terminal's
// direct door both answer with it, so the two cannot format a time differently.
func (t AgentToken) Contract() apicontract.AgentToken {
	out := apicontract.AgentToken{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scope: t.Scope, Owner: t.Owner, Status: t.Status,
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339), ExpiresAt: t.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if t.LastUsedAt != nil {
		s := t.LastUsedAt.UTC().Format(time.RFC3339)
		out.LastUsedAt = &s
	}
	if t.RevokedAt != nil {
		s := t.RevokedAt.UTC().Format(time.RFC3339)
		out.RevokedAt = &s
	}
	return out
}

// MintedAgentToken is the one answer that carries the plaintext token.
type MintedAgentToken struct {
	Token      string
	AgentToken AgentToken
}

// AgentPrincipal is who an authenticated bearer request is.
type AgentPrincipal struct {
	TokenID string
	Name    string
	Scope   string
}

// AgentRefusal is a refusal the authenticator made, for the audit table.
// TokenID is empty when the presented token matched nothing.
type AgentRefusal struct {
	TokenID       string
	Method        string
	Path          string
	Status        int
	Code          string
	CorrelationID string
}

// MintAgentToken creates a token and returns its plaintext, once.
func (b *BackupService) MintAgentToken(ctx context.Context, req MintAgentTokenRequest) (MintedAgentToken, error) {
	if !agentNamePattern.MatchString(req.Name) {
		return MintedAgentToken{}, fmt.Errorf("%w: a token name is 1 to 64 letters, digits, dots, dashes or underscores", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Owner) == "" {
		return MintedAgentToken{}, fmt.Errorf("%w: a token needs the owner who minted it", ErrInvalidRequest)
	}
	scope := req.Scope
	if scope == "" {
		scope = AgentScopeRead
	}
	switch scope {
	case AgentScopeRead, AgentScopeOperate, AgentScopeConfigure:
	default:
		return MintedAgentToken{}, fmt.Errorf("%w: the scope %q is not one of read, operate or configure", ErrInvalidRequest, req.Scope)
	}
	lifetime, err := parseAgentLifetime(req.ExpiresIn)
	if err != nil {
		return MintedAgentToken{}, err
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return MintedAgentToken{}, fmt.Errorf("service: mint agent token: %w", err)
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return MintedAgentToken{}, fmt.Errorf("service: mint agent token: %w", err)
	}
	token := AgentTokenPrefix + base64.RawURLEncoding.EncodeToString(secret)

	at := now()
	row := state.AgentToken{
		ID:        "agt_" + hex.EncodeToString(idBytes),
		Name:      req.Name,
		Prefix:    token[:agentTokenPrefixLen],
		Hash:      agentTokenHash(token),
		Scope:     scope,
		Owner:     req.Owner,
		CreatedAt: at,
		ExpiresAt: at.Add(lifetime),
	}
	if err := b.journal.InsertAgentToken(ctx, row); err != nil {
		if errors.Is(err, state.ErrAgentTokenNameTaken) {
			return MintedAgentToken{}, fmt.Errorf("%w: a live token is already named %q; revoke it first or pick another name", ErrInvalidRequest, req.Name)
		}
		return MintedAgentToken{}, fmt.Errorf("service: mint agent token: %w", err)
	}
	return MintedAgentToken{Token: token, AgentToken: agentTokenView(row, at)}, nil
}

// ListAgentTokens returns every token, with its status read from the row now.
func (b *BackupService) ListAgentTokens(ctx context.Context) ([]AgentToken, error) {
	rows, err := b.journal.ListAgentTokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: list agent tokens: %w", err)
	}
	at := now()
	out := make([]AgentToken, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentTokenView(row, at))
	}
	return out, nil
}

// RevokeAgentToken ends a token's access; the very next request with it is
// refused.
func (b *BackupService) RevokeAgentToken(ctx context.Context, id string) (AgentToken, error) {
	found, err := b.journal.RevokeAgentToken(ctx, id, now())
	if err != nil {
		return AgentToken{}, fmt.Errorf("service: revoke agent token: %w", err)
	}
	if !found {
		return AgentToken{}, fmt.Errorf("%w: %q", ErrAgentTokenNotFound, id)
	}
	rows, err := b.journal.ListAgentTokens(ctx)
	if err != nil {
		return AgentToken{}, fmt.Errorf("service: revoke agent token: %w", err)
	}
	for _, row := range rows {
		if row.ID == id {
			return agentTokenView(row, now()), nil
		}
	}
	return AgentToken{}, fmt.Errorf("%w: %q", ErrAgentTokenNotFound, id)
}

// AuthenticateAgentToken resolves a presented bearer token to its principal,
// or ErrAgentTokenInvalid. The hash is compared in constant time, and the
// status is read from the row on this call. A success updates last_used_at.
func (b *BackupService) AuthenticateAgentToken(ctx context.Context, presented string) (AgentPrincipal, error) {
	if len(presented) < agentTokenPrefixLen {
		return AgentPrincipal{}, ErrAgentTokenInvalid
	}
	candidates, err := b.journal.AgentTokensByPrefix(ctx, presented[:agentTokenPrefixLen])
	if err != nil {
		return AgentPrincipal{}, fmt.Errorf("service: authenticate agent token: %w", err)
	}
	want := []byte(agentTokenHash(presented))
	var match *state.AgentToken
	for i := range candidates {
		if subtle.ConstantTimeCompare(want, []byte(candidates[i].Hash)) == 1 {
			match = &candidates[i]
		}
	}
	at := now()
	if match == nil || match.RevokedAt != nil || !at.Before(match.ExpiresAt) {
		return AgentPrincipal{}, ErrAgentTokenInvalid
	}
	if err := b.journal.TouchAgentToken(ctx, match.ID, at); err != nil {
		return AgentPrincipal{}, fmt.Errorf("service: authenticate agent token: %w", err)
	}
	return AgentPrincipal{TokenID: match.ID, Name: match.Name, Scope: match.Scope}, nil
}

// RecordAgentRefusal writes a refusal the bearer authenticator made into
// agent_audit. It never carries the presented token.
func (b *BackupService) RecordAgentRefusal(ctx context.Context, r AgentRefusal) error {
	return b.journal.InsertAgentAudit(ctx, state.AgentAudit{
		At:            now(),
		TokenID:       r.TokenID,
		Transport:     "api",
		Arguments:     r.Method + " " + r.Path,
		Status:        r.Status,
		Code:          r.Code,
		CorrelationID: r.CorrelationID,
	})
}

func agentTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func agentTokenView(row state.AgentToken, at time.Time) AgentToken {
	status := "active"
	switch {
	case row.RevokedAt != nil:
		status = "revoked"
	case !at.Before(row.ExpiresAt):
		status = "expired"
	}
	return AgentToken{
		ID: row.ID, Name: row.Name, Prefix: row.Prefix, Scope: row.Scope, Owner: row.Owner,
		CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt,
		Status: status,
	}
}

// parseAgentLifetime reads "", "90d" or a Go duration, and refuses anything
// that is not positive or is past the ceiling.
func parseAgentLifetime(s string) (time.Duration, error) {
	if s == "" {
		return DefaultAgentTokenLifetime, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("%w: %q is not a lifetime; use a number of days such as 90d or a duration such as 720h", ErrInvalidRequest, s)
		}
		if n > int(MaxAgentTokenLifetime/(24*time.Hour)) {
			return 0, fmt.Errorf("%w: a token lives at most 365 days", ErrInvalidRequest)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("%w: %q is not a lifetime; use a number of days such as 90d or a duration such as 720h", ErrInvalidRequest, s)
		}
		d = parsed
	}
	if d <= 0 {
		return 0, fmt.Errorf("%w: a token must expire in the future; there is no non-expiring token", ErrInvalidRequest)
	}
	if d > MaxAgentTokenLifetime {
		return 0, fmt.Errorf("%w: a token lives at most 365 days", ErrInvalidRequest)
	}
	return d, nil
}
