package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const agentsFixtureDir = "../../tests/compat/testdata/configs"

// A config that writes nothing about agents resolves the five defaults, and
// the HTTP transport is off (D1).
func TestAConfigWritingNothingResolvesTheAgentDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(agentsFixtureDir, "02-no-retention-block.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	want := Agents{HTTPTransport: "off", RateLimitPerMinute: 60, ConfirmationTTL: 15 * time.Minute, AuditRetentionDays: 90, MaxPendingConfirmations: 5}
	if cfg.Agents != want {
		t.Fatalf("resolved %+v, want %+v", cfg.Agents, want)
	}
	if cfg.Agents.HTTPTransportOn() {
		t.Fatal("the HTTP transport must be off by default")
	}
}

func TestTheAgentAccessFixtureLoadsAndResolvesWhatItWrites(t *testing.T) {
	cfg, err := Load(filepath.Join(agentsFixtureDir, "12-agent-access.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	want := Agents{HTTPTransport: "on", RateLimitPerMinute: 120, ConfirmationTTL: 10 * time.Minute, AuditRetentionDays: 30, MaxPendingConfirmations: 3}
	if cfg.Agents != want {
		t.Fatalf("resolved %+v, want %+v", cfg.Agents, want)
	}
}

func TestAnUnknownKeyInTheAgentsBlockFailsLoad(t *testing.T) {
	_, err := Load(filepath.Join(agentsFixtureDir, "66-invalid-agent-unknown-scope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("Load must refuse the unknown key by name, got %v", err)
	}
}

func TestTheAgentsBlockRefusesValuesOutsideItsLines(t *testing.T) {
	zero, big := 0, 5
	short, long := Duration(time.Second), Duration(48*time.Hour)
	cases := map[string]AgentsBlock{
		"http_transport":            {HTTPTransport: "maybe"},
		"rate_limit_per_minute":     {RateLimitPerMinute: &zero},
		"audit_retention_days":      {AuditRetentionDays: &zero},
		"max_pending_confirmations": {MaxPendingConfirmations: &zero},
		"confirmation_ttl":          {ConfirmationTTL: &short},
	}
	for key, block := range cases {
		cfg := &Config{AgentsBlock: &block}
		if err := cfg.ResolveAgents(); err == nil || !strings.Contains(err.Error(), "agents."+key) {
			t.Errorf("%s: want a refusal naming the key, got %v", key, err)
		}
	}
	cfg := &Config{AgentsBlock: &AgentsBlock{ConfirmationTTL: &long, MaxPendingConfirmations: &big}}
	if err := cfg.ResolveAgents(); err == nil || !strings.Contains(err.Error(), "agents.confirmation_ttl") {
		t.Errorf("a day and a bit must be refused, got %v", err)
	}
}
