package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1152, repository health and repository maintenance. The exit code stays
// the verdict (1 when a repository is FAILING) whether the report is text or
// JSON: a monitoring job that switches to --json must not lose its alarm.

func TestRepositoryHealthJSON_IsTheListRepositoriesResponse_1152(t *testing.T) {
	configPath := aDeploymentWithARepository(t)

	out, code := cliJSON(t, "repository", "health", "--json", "--config", configPath)
	var got apicontract.ListRepositoriesResponse
	strictDecode(t, out, &got)
	if got.GeneratedAt == "" {
		t.Errorf("generated_at is empty")
	}
	if len(got.Repositories) != 1 {
		t.Fatalf("repositories = %+v", got.Repositories)
	}
	r := got.Repositories[0]
	if r.Domain != "production" || !r.MayShare {
		t.Errorf("identity wrong: %+v", r)
	}
	if !r.Reachable || !r.Readable || !r.Writable || !r.CredentialsValid {
		t.Errorf("a repository the product just created reads as unhealthy: %+v", r)
	}
	if len(r.BackupSets) != 1 || r.BackupSets[0] != incSet {
		t.Errorf("backup_sets = %v, want [%s]", r.BackupSets, incSet)
	}
	if r.State == "FAILING" && code != exitFailure {
		t.Errorf("a FAILING repository exited %d, want 1", code)
	}
	if r.State != "FAILING" && code != exitOK {
		t.Errorf("state %s exited %d, want 0", r.State, code)
	}

	// clock_skew_seconds is always sent: null when there is nothing durable
	// to compare against, a number otherwise.
	var env struct {
		Repositories []map[string]json.RawMessage `json:"repositories"`
	}
	strictDecode(t, out, &env)
	for _, k := range []string{"domain", "may_share", "state", "reachable", "readable", "writable", "credentials_valid", "clock_sane", "clock_skew_seconds", "maintenance_overdue"} {
		if _, ok := env.Repositories[0][k]; !ok {
			t.Errorf("repository has no %q key", k)
		}
	}
}

func TestRepositoryHealthJSON_AFailingRepositoryStillExitsOneAndStillPrintsTheObject_1152(t *testing.T) {
	// Declared and never created: nothing to open.
	configPath := aDeploymentWithSnapshots(t)

	out, code := cliJSON(t, "repository", "health", "--json", "--config", configPath)
	var got apicontract.ListRepositoriesResponse
	strictDecode(t, out, &got)
	if len(got.Repositories) != 1 {
		t.Fatalf("repositories = %+v", got.Repositories)
	}
	if got.Repositories[0].State != "FAILING" {
		t.Fatalf("a repository that was never created reads %q, want FAILING (the fixture is not the failure it claims)", got.Repositories[0].State)
	}
	if code != exitFailure {
		t.Errorf("exit = %d, want 1: the verdict is the exit code in both forms", code)
	}
}

func TestRepositoryHealthJSON_NoDomainsIsAnEmptyArray_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	out, code := cliJSON(t, "repository", "health", "--json", "--config", configPath)
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(out, "declares no repository domain") {
		t.Errorf("--json printed the sentence instead of the object:\n%s", out)
	}
	var got apicontract.ListRepositoriesResponse
	strictDecode(t, out, &got)
	if got.Repositories == nil || len(got.Repositories) != 0 {
		t.Errorf("repositories = %#v, want an empty array", got.Repositories)
	}
	if !strings.Contains(strings.Join(strings.Fields(out), ""), `"repositories":[]`) {
		t.Errorf("repositories is not an array on the wire:\n%s", out)
	}
}

func TestRepositoryMaintenanceJSON_IsTheRepositoryMaintenanceObject_1152(t *testing.T) {
	configPath := aDeploymentWithARepository(t)

	out, code := cliJSON(t, "repository", "maintenance", "--json", "--config", configPath, "production")
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	var got apicontract.RepositoryMaintenance
	strictDecode(t, out, &got)
	if got.Domain != "production" {
		t.Errorf("domain = %q", got.Domain)
	}
	requireKeys(t, rawKeys(t, out), "domain", "due", "failing", "failures", "overdue", "owner", "reclaimed_bytes", "runs")
}

func TestRepositoryMaintenanceJSON_AnUndeclaredDomainPrintsNothingAndFails_1152(t *testing.T) {
	configPath := aDeploymentWithARepository(t)
	out, code := cliJSON(t, "repository", "maintenance", "--json", "--config", configPath, "nope")
	if code != exitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a refused read wrote to stdout:\n%s", out)
	}
}
