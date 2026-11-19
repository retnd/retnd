package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1387: a run's confirmation shows the configuration revision it was
// recorded against, in the text a person reads as well as in --json, and a
// config_revision an agent put into any other body is not shown as one.
//
// Falsify: drop the line from `show`, or read config_revision from every body.
func TestShowNamesTheRevisionARunWasRecordedAgainstAndNoOther(t *testing.T) {
	for name, door := range map[string]func(t *testing.T) (string, *service.BackupService, func()){
		"direct": func(t *testing.T) (string, *service.BackupService, func()) {
			configPath := writeTestConfig(t)
			svc, closeSvc := openSeedService(t, configPath)
			return configPath, svc, closeSvc
		},
		"through the engine": func(t *testing.T) (string, *service.BackupService, func()) {
			configPath := writeTestConfig(t)
			e := startFakeEngineFor(t, configPath)
			e.attach(t)
			return configPath, e.svc, func() {}
		},
	} {
		t.Run(name, func(t *testing.T) {
			configPath, svc, done := door(t)
			run := askToRun(t, svc, "run_cycle", "/operations", `{"action":"run_cycle","config_revision":"rev-1387"}`)
			ctx := context.Background()
			minted, err := svc.MintAgentToken(ctx, service.MintAgentTokenRequest{Name: "helper", Scope: service.AgentScopeConfigure, Owner: "alice"})
			if err != nil {
				t.Fatalf("mint: %v", err)
			}
			principal, err := svc.AuthenticateAgentToken(ctx, minted.Token)
			if err != nil {
				t.Fatalf("authenticate: %v", err)
			}
			other, err := svc.CreateAgentConfirmation(ctx, service.CreateAgentConfirmationRequest{
				Principal: principal, Scope: service.AgentScopeConfigure, Tool: "backup_set_enabled", Method: "POST",
				Route: "/backup-sets/production/postgres/enabled", Body: []byte(`{"enabled":false,"config_revision":"rev-planted"}`),
				Command: "retnd backup-set enabled production/postgres off",
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			done()

			code, text, stderr := runCLI(t, "agent", "confirmations", "show", run.ID, "--config", configPath)
			if code != exitOK || !strings.Contains(text, "recorded against configuration revision rev-1387") {
				t.Errorf("show of a run, exit %d, does not name its revision:\n%s\n%s", code, text, stderr)
			}
			code, raw, _ := runCLI(t, "agent", "confirmations", "show", run.ID, "--json", "--config", configPath)
			var got apicontract.AgentConfirmation
			if err := json.Unmarshal([]byte(raw), &got); code != exitOK || err != nil || got.ConfigRevision != "rev-1387" {
				t.Errorf("show --json of a run = %s (%v), want config_revision rev-1387", raw, err)
			}

			code, text, _ = runCLI(t, "agent", "confirmations", "show", other.ID, "--config", configPath)
			if code != exitOK || strings.Contains(text, "rev-planted") || strings.Contains(text, "configuration revision") {
				t.Errorf("show of a non-run change, exit %d, names a revision the agent wrote:\n%s", code, text)
			}
			_, raw, _ = runCLI(t, "agent", "confirmations", "show", other.ID, "--json", "--config", configPath)
			if strings.Contains(raw, "rev-planted") {
				t.Errorf("show --json of a non-run change carries the agent's config_revision: %s", raw)
			}
		})
	}
}
