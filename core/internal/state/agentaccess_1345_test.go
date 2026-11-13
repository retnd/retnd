package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Migration 0022 and the agent token rows (EPIC U, U1.2, #1345).

func TestMigration0022IsAdditiveAndOnlyAddsTheThreeAgentTables(t *testing.T) {
	m := migrationSQL(t, 22)
	if !strings.HasPrefix(m.filename, "0022_") {
		t.Fatalf("migration 22 is %s", m.filename)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", m.filename))
	if err != nil {
		t.Fatal(err)
	}
	stripped := regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(string(raw), "")
	created := map[string]bool{}
	for _, stmt := range strings.Split(stripped, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		up := strings.ToUpper(stmt)
		switch {
		case strings.HasPrefix(up, "CREATE TABLE AGENT_TOKENS"):
			created["agent_tokens"] = true
		case strings.HasPrefix(up, "CREATE TABLE AGENT_CONFIRMATIONS"):
			created["agent_confirmations"] = true
		case strings.HasPrefix(up, "CREATE TABLE AGENT_AUDIT"):
			created["agent_audit"] = true
		case strings.HasPrefix(up, "CREATE INDEX"), strings.HasPrefix(up, "CREATE UNIQUE INDEX"):
			if !strings.Contains(strings.ToLower(stmt), "agent_") {
				t.Errorf("0022 may only index its own tables; it also does: %.80s", stmt)
			}
		default:
			t.Errorf("0022 may only create the agent tables and their indexes; it also does: %.80s", stmt)
		}
	}
	for _, table := range []string{"agent_tokens", "agent_confirmations", "agent_audit"} {
		if !created[table] {
			t.Errorf("0022 does not create %s", table)
		}
	}
}

func agentTokenForTest(id, name string) AgentToken {
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	return AgentToken{
		ID: id, Name: name, Prefix: "rat_abcd", Hash: strings.Repeat("a", 64), Scope: "read",
		Owner: "alice", CreatedAt: at, ExpiresAt: at.Add(90 * 24 * time.Hour),
	}
}

func TestAnAgentTokenRoundTripsAndNeverGainsAPlaintextColumn(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	want := agentTokenForTest("agt_1", "triage")
	if err := j.InsertAgentToken(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := j.ListAgentTokens(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %v, %v", got, err)
	}
	if got[0].ID != want.ID || got[0].Hash != want.Hash || !got[0].ExpiresAt.Equal(want.ExpiresAt) || got[0].LastUsedAt != nil || got[0].RevokedAt != nil {
		t.Errorf("round trip = %+v, want %+v", got[0], want)
	}

	rows, err := j.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('agent_tokens')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want10 := "id,name,prefix,hash,scope,owner,created_at,expires_at,last_used_at,revoked_at"
	if strings.Join(columns, ",") != want10 {
		t.Errorf("agent_tokens columns = %v, want %s", columns, want10)
	}
}

func TestAgentTokenNamesAreUniqueOnlyAmongLiveTokens(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	if err := j.InsertAgentToken(ctx, agentTokenForTest("agt_1", "triage")); err != nil {
		t.Fatal(err)
	}
	if err := j.InsertAgentToken(ctx, agentTokenForTest("agt_2", "triage")); !errors.Is(err, ErrAgentTokenNameTaken) {
		t.Fatalf("a live duplicate = %v, want ErrAgentTokenNameTaken", err)
	}
	found, err := j.RevokeAgentToken(ctx, "agt_1", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if err != nil || !found {
		t.Fatalf("revoke = %v, %v", found, err)
	}
	if err := j.InsertAgentToken(ctx, agentTokenForTest("agt_2", "triage")); err != nil {
		t.Errorf("a name whose token was revoked: %v", err)
	}
}

func TestRevokingTwiceKeepsTheFirstTimeAndAnUnknownIDIsNotFound(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	if err := j.InsertAgentToken(ctx, agentTokenForTest("agt_1", "triage")); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if _, err := j.RevokeAgentToken(ctx, "agt_1", first); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RevokeAgentToken(ctx, "agt_1", first.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := j.ListAgentTokens(ctx)
	if err != nil || got[0].RevokedAt == nil || !got[0].RevokedAt.Equal(first) {
		t.Errorf("revoked_at = %v, %v; want the first revoke time %s", got[0].RevokedAt, err, first)
	}
	if found, err := j.RevokeAgentToken(ctx, "agt_nope", first); err != nil || found {
		t.Errorf("unknown id = %v, %v; want not found", found, err)
	}
}

func TestAgentAuditRowsKeepTheRefusalWithoutAToken(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if err := j.InsertAgentAudit(ctx, AgentAudit{At: at, Transport: "api", Arguments: "GET /system/health", Status: 401, Code: "AGENT_TOKEN_INVALID", CorrelationID: "cid_1"}); err != nil {
		t.Fatalf("an unknown token has no id to name: %v", err)
	}
	rows, err := j.ListAgentAudit(ctx, 10)
	if err != nil || len(rows) != 1 || rows[0].TokenID != "" || rows[0].Code != "AGENT_TOKEN_INVALID" {
		t.Errorf("audit = %+v, %v", rows, err)
	}
}
