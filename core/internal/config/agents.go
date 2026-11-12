package config

import "time"

// The `agents:` block (EPIC U, U1.4, FR-96; ADR 0034). Global only: there is
// no per-set form, because a token is not scoped to a set and the one thing
// an operator turns on here (the HTTP transport) is a property of the whole
// deployment.
//
// A configuration that never writes the key resolves the five defaults below
// and a deployment with no token has no agent access, so behaviour is
// byte-identical to the build before EPIC U. omitempty on the block keeps a
// re-marshal from injecting the key, which an older build would refuse under
// Load's KnownFields(true); U2.5's CHANGELOG names the version that door
// opens in.

// The words http_transport takes, and the defaults of the other four keys.
const (
	AgentsHTTPTransportOff = "off"
	AgentsHTTPTransportOn  = "on"

	DefaultAgentRateLimitPerMinute = 60
	DefaultAgentConfirmationTTL    = 15 * time.Minute
	DefaultAgentAuditRetentionDays = 90
	DefaultMaxPendingConfirmations = 5

	// MinAgentConfirmationTTL and MaxAgentConfirmationTTL bound how long a
	// pending change may wait for a person. Under a minute is not a window
	// anyone can act in, and over a day is an approval nobody remembers
	// asking for.
	MinAgentConfirmationTTL = time.Minute
	MaxAgentConfirmationTTL = 24 * time.Hour
)

// AgentsBlock is the `agents:` block as the operator wrote it. Nil on Config
// means they wrote nothing. The pointer fields are how an explicit zero is
// told from an absent key, so "rate_limit_per_minute: 0" is refused rather
// than read as "use the default".
type AgentsBlock struct {
	HTTPTransport           string    `yaml:"http_transport,omitempty"`
	RateLimitPerMinute      *int      `yaml:"rate_limit_per_minute,omitempty"`
	ConfirmationTTL         *Duration `yaml:"confirmation_ttl,omitempty"`
	AuditRetentionDays      *int      `yaml:"audit_retention_days,omitempty"`
	MaxPendingConfirmations *int      `yaml:"max_pending_confirmations,omitempty"`
}

// Agents is the resolved block, filled by Validate. Every consumer reads this
// and never the pointer on Config.
type Agents struct {
	// HTTPTransport is "off" or "on". Off is the default: a long-lived bearer
	// token on a plain-HTTP stream is a worse exposure than a session cookie
	// that expires, and the operator who has a TLS terminator turns it on in
	// one line (D1).
	HTTPTransport string
	// RateLimitPerMinute is how many API calls one token may make in a minute.
	RateLimitPerMinute int
	// ConfirmationTTL is how long a pending change waits for a person.
	ConfirmationTTL time.Duration
	// AuditRetentionDays is how long an agent_audit row is kept.
	AuditRetentionDays int
	// MaxPendingConfirmations caps the changes one token may have waiting.
	MaxPendingConfirmations int
}

// HTTPTransportOn reports whether /api/v1/agent/mcp answers.
func (a Agents) HTTPTransportOn() bool { return a.HTTPTransport == AgentsHTTPTransportOn }

// IsZero reports whether the operator wrote nothing worth keeping, so a
// writer that rebuilds the block can drop it.
func (b AgentsBlock) IsZero() bool {
	return b.HTTPTransport == "" && b.RateLimitPerMinute == nil && b.ConfirmationTTL == nil &&
		b.AuditRetentionDays == nil && b.MaxPendingConfirmations == nil
}

// resolveAgents is phase 2 of Validate for the block.
func (c *Config) resolveAgents(v *validator) {
	var b AgentsBlock
	if c.AgentsBlock != nil {
		b = *c.AgentsBlock
	}
	const p = "agents"
	out := Agents{
		HTTPTransport:           AgentsHTTPTransportOff,
		RateLimitPerMinute:      DefaultAgentRateLimitPerMinute,
		ConfirmationTTL:         DefaultAgentConfirmationTTL,
		AuditRetentionDays:      DefaultAgentAuditRetentionDays,
		MaxPendingConfirmations: DefaultMaxPendingConfirmations,
	}

	switch b.HTTPTransport {
	case "":
	case AgentsHTTPTransportOff, AgentsHTTPTransportOn:
		out.HTTPTransport = b.HTTPTransport
	default:
		v.addf("%s.http_transport: %q is not one of %q or %q", p, b.HTTPTransport, AgentsHTTPTransportOff, AgentsHTTPTransportOn)
	}
	positive := func(key string, set *int, dst *int) {
		if set == nil {
			return
		}
		if *set < 1 {
			v.addf("%s.%s: must be at least 1 (got %d)", p, key, *set)
			return
		}
		*dst = *set
	}
	positive("rate_limit_per_minute", b.RateLimitPerMinute, &out.RateLimitPerMinute)
	positive("audit_retention_days", b.AuditRetentionDays, &out.AuditRetentionDays)
	positive("max_pending_confirmations", b.MaxPendingConfirmations, &out.MaxPendingConfirmations)
	if b.ConfirmationTTL != nil {
		ttl := b.ConfirmationTTL.Duration()
		if ttl < MinAgentConfirmationTTL || ttl > MaxAgentConfirmationTTL {
			v.addf("%s.confirmation_ttl: %s is outside %s to %s", p, ttl, MinAgentConfirmationTTL, MaxAgentConfirmationTTL)
		} else {
			out.ConfirmationTTL = ttl
		}
	}
	c.Agents = out
}

// ResolveAgents runs the agents phase on its own, for a caller that builds a
// Config by hand.
func (c *Config) ResolveAgents() error {
	v := &validator{}
	c.resolveAgents(v)
	return v.err()
}
