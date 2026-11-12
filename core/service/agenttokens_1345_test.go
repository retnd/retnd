package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

// U1.2 (#1345): the agent token store behind FR-91 and FR-92. Every test here
// is written to be red until the store exists, and each names the mutation
// that has to turn it red.

func pinAgentClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	prev := now
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = prev })
	return &cur
}

func mintForTest(t *testing.T, svc *BackupService, req MintAgentTokenRequest) MintedAgentToken {
	t.Helper()
	if req.Owner == "" {
		req.Owner = "alice"
	}
	got, err := svc.MintAgentToken(context.Background(), req)
	if err != nil {
		t.Fatalf("MintAgentToken(%+v): %v", req, err)
	}
	return got
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Falsify: store or return the plaintext.
func TestATokenIsShownOnceAndStoredAsAHash(t *testing.T) {
	svc := newTestService(t)
	got := mintForTest(t, svc, MintAgentTokenRequest{Name: "triage"})

	if len(got.Token) < 32 {
		t.Fatalf("the minted token is %d characters, too short to be a credential", len(got.Token))
	}
	if got.AgentToken.Prefix != got.Token[:8] {
		t.Errorf("prefix = %q, want the first eight characters of the token", got.AgentToken.Prefix)
	}

	rows, err := svc.journal.ListAgentTokens(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored rows = %d, %v; want exactly one", len(rows), err)
	}
	if rows[0].Hash != sha256Hex(got.Token) {
		t.Errorf("stored hash = %q, want the SHA-256 of the token", rows[0].Hash)
	}
	stored, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), got.Token) {
		t.Error("the stored row carries the plaintext token")
	}

	// Nothing afterwards shows it again: not the list, not the revoke answer.
	listed, err := svc.ListAgentTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := svc.RevokeAgentToken(context.Background(), got.AgentToken.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"list": listed, "revoke": revoked} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), got.Token) || strings.Contains(string(raw), rows[0].Hash) {
			t.Errorf("%s carries the token or its hash: %s", name, raw)
		}
	}
}

// Falsify: cache a token's status past a request, or accept a revoked row.
func TestARevokedTokenIsRefusedOnTheNextRequest(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	got := mintForTest(t, svc, MintAgentTokenRequest{Name: "triage"})

	p, err := svc.AuthenticateAgentToken(ctx, got.Token)
	if err != nil {
		t.Fatalf("a fresh token was refused: %v", err)
	}
	if p.Name != "triage" || p.Scope != "read" || p.TokenID != got.AgentToken.ID {
		t.Errorf("principal = %+v", p)
	}

	if _, err := svc.RevokeAgentToken(ctx, got.AgentToken.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateAgentToken(ctx, got.Token); !errors.Is(err, ErrAgentTokenInvalid) {
		t.Fatalf("the request after a revoke = %v, want ErrAgentTokenInvalid", err)
	}
}

// Falsify: drop the ceiling.
func TestATokenNeverOutlivesAYear(t *testing.T) {
	svc := newTestService(t)
	for i, tc := range []struct {
		name      string
		expiresIn string
		wantErr   bool
	}{
		{"default is ninety days", "", false},
		{"one year exactly", "365d", false},
		{"one day", "24h", false},
		{"past a year", "366d", true},
		{"hours past a year", "8761h", true},
		{"no expiry spelled zero", "0", true},
		{"no expiry spelled never", "never", true},
		{"negative", "-1h", true},
		{"nonsense", "soon", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.MintAgentToken(context.Background(), MintAgentTokenRequest{
				Name: "ceiling-" + string(rune('a'+i)), Owner: "alice", ExpiresIn: tc.expiresIn,
			})
			if tc.wantErr && !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("expires_in %q = %v, want ErrInvalidRequest", tc.expiresIn, err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expires_in %q = %v, want a token", tc.expiresIn, err)
			}
		})
	}
}

func TestADefaultTokenExpiresInNinetyDays(t *testing.T) {
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	pinAgentClock(t, at)
	got := mintForTest(t, newTestService(t), MintAgentTokenRequest{Name: "triage"})
	if want := at.Add(90 * 24 * time.Hour); !got.AgentToken.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %s, want %s", got.AgentToken.ExpiresAt, want)
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock := pinAgentClock(t, at)
	svc := newTestService(t)
	got := mintForTest(t, svc, MintAgentTokenRequest{Name: "triage", ExpiresIn: "24h"})

	*clock = at.Add(23 * time.Hour)
	if _, err := svc.AuthenticateAgentToken(context.Background(), got.Token); err != nil {
		t.Fatalf("a token inside its expiry was refused: %v", err)
	}
	*clock = at.Add(24 * time.Hour)
	if _, err := svc.AuthenticateAgentToken(context.Background(), got.Token); !errors.Is(err, ErrAgentTokenInvalid) {
		t.Errorf("a token at its expiry = %v, want ErrAgentTokenInvalid", err)
	}
}

func TestAnUnknownOrMalformedTokenIsRefused(t *testing.T) {
	svc := newTestService(t)
	got := mintForTest(t, svc, MintAgentTokenRequest{Name: "triage"})
	for name, presented := range map[string]string{
		"empty":               "",
		"unknown":             "rat_" + strings.Repeat("A", 43),
		"right prefix only":   got.Token[:8],
		"one character off":   got.Token[:len(got.Token)-1] + "x",
		"the hash, not token": sha256Hex(got.Token),
		"trailing whitespace": got.Token + " ",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.AuthenticateAgentToken(context.Background(), presented); !errors.Is(err, ErrAgentTokenInvalid) {
				t.Errorf("%q = %v, want ErrAgentTokenInvalid", name, err)
			}
		})
	}
}

func TestEverySuccessfulAuthenticationUpdatesLastUsed(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock := pinAgentClock(t, at)
	svc := newTestService(t)
	got := mintForTest(t, svc, MintAgentTokenRequest{Name: "triage"})
	if got.AgentToken.LastUsedAt != nil {
		t.Fatal("a token never used reports a last-used time")
	}

	*clock = at.Add(time.Hour)
	if _, err := svc.AuthenticateAgentToken(ctx, got.Token); err != nil {
		t.Fatal(err)
	}
	listed, err := svc.ListAgentTokens(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %v, %v", listed, err)
	}
	if listed[0].LastUsedAt == nil || !listed[0].LastUsedAt.Equal(*clock) {
		t.Errorf("last_used_at = %v, want %s", listed[0].LastUsedAt, *clock)
	}
}

func TestScopeIsAClosedVocabularyAndReadIsTheDefault(t *testing.T) {
	svc := newTestService(t)
	if got := mintForTest(t, svc, MintAgentTokenRequest{Name: "default"}); got.AgentToken.Scope != "read" {
		t.Errorf("an unstated scope minted %q, want read", got.AgentToken.Scope)
	}
	for _, scope := range []string{"read", "operate", "configure"} {
		if got := mintForTest(t, svc, MintAgentTokenRequest{Name: "s-" + scope, Scope: scope}); got.AgentToken.Scope != scope {
			t.Errorf("scope %q minted %q", scope, got.AgentToken.Scope)
		}
	}
	for _, scope := range []string{"admin", "READ", "write", "read,operate", " read"} {
		if _, err := svc.MintAgentToken(context.Background(), MintAgentTokenRequest{Name: "bad", Owner: "alice", Scope: scope}); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("scope %q = %v, want ErrInvalidRequest", scope, err)
		}
	}
}

func TestATokenNeedsANameAnOwnerAndNamesStayUnique(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	for _, req := range []MintAgentTokenRequest{
		{Name: "", Owner: "alice"},
		{Name: "has space", Owner: "alice"},
		{Name: "colon:inside", Owner: "alice"},
		{Name: strings.Repeat("a", 65), Owner: "alice"},
		{Name: "ok", Owner: ""},
	} {
		if _, err := svc.MintAgentToken(ctx, req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%+v = %v, want ErrInvalidRequest", req, err)
		}
	}
	first := mintForTest(t, svc, MintAgentTokenRequest{Name: "triage"})
	if _, err := svc.MintAgentToken(ctx, MintAgentTokenRequest{Name: "triage", Owner: "alice"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a live duplicate name = %v, want ErrInvalidRequest", err)
	}
	if _, err := svc.RevokeAgentToken(ctx, first.AgentToken.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MintAgentToken(ctx, MintAgentTokenRequest{Name: "triage", Owner: "alice"}); err != nil {
		t.Errorf("a name whose token was revoked can be reused: %v", err)
	}
}

func TestRevokingAnUnknownTokenSaysSo(t *testing.T) {
	if _, err := newTestService(t).RevokeAgentToken(context.Background(), "agt_nope"); !errors.Is(err, ErrAgentTokenNotFound) {
		t.Errorf("revoke of an unknown id = %v, want ErrAgentTokenNotFound", err)
	}
}

func TestTheListReportsStatusFromTheRow(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock := pinAgentClock(t, at)
	svc := newTestService(t)
	mintForTest(t, svc, MintAgentTokenRequest{Name: "live"})
	gone := mintForTest(t, svc, MintAgentTokenRequest{Name: "gone"})
	mintForTest(t, svc, MintAgentTokenRequest{Name: "short", ExpiresIn: "1h"})
	if _, err := svc.RevokeAgentToken(ctx, gone.AgentToken.ID); err != nil {
		t.Fatal(err)
	}
	*clock = at.Add(2 * time.Hour)

	listed, err := svc.ListAgentTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tok := range listed {
		got[tok.Name] = tok.Status
	}
	for name, status := range map[string]string{"live": "active", "gone": "revoked", "short": "expired"} {
		if got[name] != status {
			t.Errorf("%s status = %q, want %q", name, got[name], status)
		}
	}
}

// Falsify: skip the audit write for a refusal.
func TestARefusalIsRecordedInTheAuditTable(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	got := mintForTest(t, svc, MintAgentTokenRequest{Name: "triage"})
	if err := svc.RecordAgentRefusal(ctx, AgentRefusal{
		TokenID: got.AgentToken.ID, Method: "GET", Path: "/api/v1/system/health",
		Status: 401, Code: "AGENT_TOKEN_INVALID", CorrelationID: "cid_test",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.journal.ListAgentAudit(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows = %d, %v; want one", len(rows), err)
	}
	if rows[0].Code != "AGENT_TOKEN_INVALID" || rows[0].CorrelationID != "cid_test" || rows[0].TokenID != got.AgentToken.ID {
		t.Errorf("audit row = %+v", rows[0])
	}
	if raw, _ := json.Marshal(rows[0]); strings.Contains(string(raw), got.Token) {
		t.Error("the audit row carries the plaintext token")
	}
}

// Falsify: store the refused path whole. A refusal can come from a caller
// that never authenticated, so its row has to be bounded.
func TestARefusalRowDoesNotGrowWithTheURL(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	if err := svc.RecordAgentRefusal(ctx, AgentRefusal{
		Method: "GET", Path: "/api/v1/" + strings.Repeat("a", 64<<10), Status: 401, Code: "AGENT_TOKEN_INVALID",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.journal.ListAgentAudit(ctx, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows = %d, %v; want one", len(rows), err)
	}
	if n := len(rows[0].Arguments); n > 600 {
		t.Errorf("the refusal row keeps %d bytes of the URL", n)
	}
}

// Q16 (EPIC U): the mint dialog routes a refusal to its field by the first
// word of the message, so every INVALID_REQUEST a mint can raise from the
// request starts with the JSON field name and a colon, once the sentinel's own
// prefix is taken off the way the handler does. The owner is not in the list:
// it is the signed-in user, never a field of the body.
// Falsify: reword any one of the mint messages without its field prefix.
func TestEveryMintRefusalStartsWithTheFieldItIsAbout(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	mintForTest(t, svc, MintAgentTokenRequest{Name: "taken"})
	fieldPrefix := regexp.MustCompile(`^(name|scope|expires_in): `)
	for label, req := range map[string]MintAgentTokenRequest{
		"empty name":         {Name: "", Owner: "alice"},
		"bad characters":     {Name: "has space", Owner: "alice"},
		"too long":           {Name: strings.Repeat("a", 65), Owner: "alice"},
		"live duplicate":     {Name: "taken", Owner: "alice"},
		"unknown scope":      {Name: "a", Owner: "alice", Scope: "admin"},
		"not a lifetime":     {Name: "b", Owner: "alice", ExpiresIn: "soon"},
		"not a day count":    {Name: "c", Owner: "alice", ExpiresIn: "xd"},
		"past the ceiling d": {Name: "d", Owner: "alice", ExpiresIn: "366d"},
		"past the ceiling h": {Name: "e", Owner: "alice", ExpiresIn: "9000h"},
		"zero":               {Name: "f", Owner: "alice", ExpiresIn: "0h"},
		"negative":           {Name: "g", Owner: "alice", ExpiresIn: "-5h"},
	} {
		_, err := svc.MintAgentToken(ctx, req)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v, want ErrInvalidRequest", label, err)
			continue
		}
		msg := strings.TrimPrefix(err.Error(), ErrInvalidRequest.Error()+": ")
		if !fieldPrefix.MatchString(msg) {
			t.Errorf("%s: message %q does not start with name:, scope: or expires_in:", label, msg)
		}
	}
}
