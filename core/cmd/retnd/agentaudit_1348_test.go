package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// `retnd agent audit` (#1348). Written red first; each test names the
// mutation that has to turn it red. Every verb runs over both doors.

// seedAudit writes the same three calls into a deployment, the way a
// transport would: a read, a mutation and a refusal.
func seedAudit(t *testing.T, svc *service.BackupService) {
	t.Helper()
	ctx := context.Background()
	for _, c := range []service.AgentCall{
		{TokenID: "agt_a", TokenName: "triage", Tool: "status", Transport: "stdio", Status: 200, CorrelationID: "cid_1"},
		{TokenID: "agt_a", TokenName: "triage", Tool: "run_cycle", Transport: "stdio", Mutating: true, Status: 202, CorrelationID: "cid_2"},
		{TokenID: "agt_b", TokenName: "other", Transport: "http", Refused: true, Status: 403, Code: "AGENT_SCOPE_INSUFFICIENT", CorrelationID: "cid_3"},
	} {
		if err := svc.RecordAgentCall(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
}

// auditDoors is bothDoors with the three calls already in the trail.
func auditDoors(t *testing.T, body func(t *testing.T, configPath string)) {
	t.Helper()
	t.Run("direct", func(t *testing.T) {
		configPath := writeTestConfig(t)
		svc, closeFn, err := service.Open(t.Context(), configPath)
		if err != nil {
			t.Fatal(err)
		}
		seedAudit(t, svc)
		if err := closeFn(); err != nil {
			t.Fatal(err)
		}
		body(t, configPath)
	})
	t.Run("through the engine", func(t *testing.T) {
		configPath := writeTestConfig(t)
		e := startFakeEngineFor(t, configPath)
		seedAudit(t, e.svc)
		e.attach(t)
		body(t, configPath)
	})
}

func auditJSON(t *testing.T, args ...string) apicontract.ListAgentAuditResponse {
	t.Helper()
	code, stdout, stderr := runCLI(t, append([]string{"agent", "audit", "--json"}, args...)...)
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	var got apicontract.ListAgentAuditResponse
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("--json is not the API's object: %v\n%s", err, stdout)
	}
	return got
}

// Falsify: skip reads, or print oldest first.
func TestAuditPrintsEveryCallNewestFirst(t *testing.T) {
	auditDoors(t, func(t *testing.T, configPath string) {
		code, text, stderr := runCLI(t, "agent", "audit", "--config", configPath)
		if code != exitOK {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		iRefused, iRun, iStatus := strings.Index(text, "AGENT_SCOPE_INSUFFICIENT"), strings.Index(text, "run_cycle"), strings.Index(text, "status")
		if iRefused < 0 || iRun < 0 || iStatus < 0 || iRefused >= iRun || iRun >= iStatus {
			t.Fatalf("the trail is not the three calls newest first (refused %d, run_cycle %d, status %d):\n%s", iRefused, iRun, iStatus, text)
		}
	})
}

// Falsify: ignore --token, or accept the token's own value.
func TestAuditFiltersByTokenId(t *testing.T) {
	auditDoors(t, func(t *testing.T, configPath string) {
		got := auditJSON(t, "--token", "agt_a", "--config", configPath)
		if len(got.Entries) != 2 {
			t.Fatalf("%d entries for agt_a, want 2", len(got.Entries))
		}
		for _, e := range got.Entries {
			if e.TokenID != "agt_a" {
				t.Errorf("entry for %s in agt_a's page", e.TokenID)
			}
		}
	})
}

// Falsify: drop the cursor, or print none when the page is full.
func TestAuditPagesByCursorOnBothDoors(t *testing.T) {
	auditDoors(t, func(t *testing.T, configPath string) {
		first := auditJSON(t, "--limit", "2", "--config", configPath)
		if len(first.Entries) != 2 || first.NextCursor == "" {
			t.Fatalf("first page = %d entries, cursor %q", len(first.Entries), first.NextCursor)
		}
		second := auditJSON(t, "--limit", "2", "--before", first.NextCursor, "--config", configPath)
		if len(second.Entries) != 1 || second.NextCursor != "" || second.Entries[0].CorrelationID != "cid_1" {
			t.Fatalf("second page = %+v", second)
		}
		_, _, stderr := runCLI(t, "agent", "audit", "--limit", "2", "--config", configPath)
		if !strings.Contains(stderr, "--before "+first.NextCursor) {
			t.Errorf("a full page does not say how to read the next one: %q", stderr)
		}
	})
}

// Falsify: format either door's entry differently. The page for the same
// cursor is the same bytes from both, time aside.
func TestAuditJSONIsTheSamePageFromBothDoors(t *testing.T) {
	var pages [2]string
	i := 0
	auditDoors(t, func(t *testing.T, configPath string) {
		got := auditJSON(t, "--limit", "2", "--config", configPath)
		for k := range got.Entries {
			got.Entries[k].At = ""
		}
		raw, _ := json.Marshal(got)
		pages[i] = string(raw)
		i++
	})
	if pages[0] == "" || pages[0] != pages[1] {
		t.Fatalf("the two doors disagree:\ndirect: %s\nengine: %s", pages[0], pages[1])
	}
}

// Falsify: accept a token as the filter. A secret on a command line lands in
// a shell history.
func TestAuditRefusesATokenAsTheFilter(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "audit", "--token", "rat_whatever"},
		{"agent", "audit", "--limit", "-1"},
		{"agent", "audit", "extra"},
	} {
		if code, _, _ := runCLI(t, args...); code != exitUsage {
			t.Errorf("%v exited %d, want %d", args, code, exitUsage)
		}
	}
}

// Falsify: let a since that is no time through to an empty page.
func TestAuditRefusesASinceThatIsNoTime(t *testing.T) {
	auditDoors(t, func(t *testing.T, configPath string) {
		if code, _, _ := runCLI(t, "agent", "audit", "--since", "banana", "--config", configPath); code != exitFailure {
			t.Errorf("exit %d, want %d", code, exitFailure)
		}
	})
}
