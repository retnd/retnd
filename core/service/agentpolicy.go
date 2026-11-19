package service

import "github.com/retnd/retnd/core/internal/config"

// AgentPolicy is what the agents: block of config.yaml resolved to, as the
// HTTP host needs it (EPIC U, U1.4). The block is read from the file at
// start; a change to it takes effect on the next start, like every other
// global setting that is written in the file.
type AgentPolicy struct {
	// HTTPTransport is whether /api/v1/agent/mcp answers. It is off unless the
	// operator wrote agents.http_transport: on.
	HTTPTransport bool
	// RateLimitPerMinute is how many API calls one token may make a minute.
	RateLimitPerMinute int
}

// AgentPolicy reports the resolved agents: block. A configuration whose block
// was never resolved (one built by hand) answers the defaults.
func (b *BackupService) AgentPolicy() AgentPolicy {
	agents := b.state.Load().inner.Config.Agents
	policy := AgentPolicy{HTTPTransport: agents.HTTPTransportOn(), RateLimitPerMinute: agents.RateLimitPerMinute}
	if policy.RateLimitPerMinute < 1 {
		policy.RateLimitPerMinute = config.DefaultAgentRateLimitPerMinute
	}
	return policy
}
