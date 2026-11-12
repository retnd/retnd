package agentboundary_test

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/retnd/retnd/core/tests/agentcorpus"
)

// An expired confirmation approved is refused (#1352, FR-95, UV.12). The rows
// are written straight into agent_confirmations through the spec's own schema
// (section 12, migration 0022), so the drill does not depend on how a
// confirmation gets created, only on what approving one that has run out
// does: it is refused with AGENT_CONFIRMATION_EXPIRED, the row never becomes
// approved, and nobody is recorded as having decided it.
//
// Fixtures are chosen against the wrong implementation. One row ran out an
// hour ago and one a single second ago, so an off-by-one on the boundary and a
// refusal that only looks at "long ago" both show; and a row with fourteen
// minutes left is approved too, because a verb that refuses everything would
// pass the first two.
//
// Written against the spec; not run.

type confirmationRow struct {
	id      string
	expires time.Duration // relative to now
}

func seedConfirmations(t *testing.T, bin string, rows []confirmationRow) (config string, db *sql.DB) {
	t.Helper()
	config = agentcorpus.WriteConfig(t)
	// `check` opens the journal, which creates it and runs every migration.
	if code, _, stderr := run(t, bin, nil, "check", "--config", config); code != 0 {
		t.Fatalf("check exited %d: %s", code, stderr)
	}
	path := ""
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if p, ok := strings.CutPrefix(l, "  database: "); ok {
			path = strings.TrimSpace(p)
		}
	}
	if path == "" {
		t.Fatalf("no state database named in %s", config)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	h := make([]byte, 32)
	_, _ = rand.Read(h)
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO agent_tokens (id, name, prefix, hash, scope, owner, created_at, expires_at)
		VALUES ('agt_drill', 'drill', 'rat_dril', ?, 'operate', 'alice', ?, ?)`,
		hex.EncodeToString(h), now.Add(-24*time.Hour), now.Add(24*time.Hour)); err != nil {
		t.Fatalf("seeding the token: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO agent_confirmations
			(id, token_id, tool, route, method, body, command, backup_set, state, created_at, expires_at)
			VALUES (?, 'agt_drill', 'run_cycle', '/operations', 'POST', ?, 'retnd run', '', 'pending', ?, ?)`,
			r.id, []byte(`{}`), now.Add(-time.Hour), now.Add(r.expires)); err != nil {
			t.Fatalf("seeding %s: %v", r.id, err)
		}
	}
	return config, db
}

func decided(t *testing.T, db *sql.DB, id string) (state string, by sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT state, decided_by FROM agent_confirmations WHERE id = ?`, id).Scan(&state, &by); err != nil {
		t.Fatalf("reading %s back: %v", id, err)
	}
	return state, by
}

func TestAnExpiredConfirmationApprovedIsRefused(t *testing.T) {
	bin := agentcorpus.BuildRetnd(t)
	config, db := seedConfirmations(t, bin, []confirmationRow{
		{"agc_hour", -time.Hour},
		{"agc_second", -time.Second},
	})
	for _, id := range []string{"agc_hour", "agc_second"} {
		code, stdout, stderr := run(t, bin, nil, "agent", "confirmations", "approve", id, "--config", config)
		if code == 0 {
			t.Errorf("%s: approving an expired confirmation exited 0\n%s", id, stdout)
		}
		if !strings.Contains(stdout+stderr, "AGENT_CONFIRMATION_EXPIRED") {
			t.Errorf("%s: the refusal does not name AGENT_CONFIRMATION_EXPIRED:\n%s\n%s", id, stdout, stderr)
		}
		if state, by := decided(t, db, id); state == "approved" || by.Valid && by.String != "" {
			t.Errorf("%s: the row reads %q decided by %q after a refused approval", id, state, by.String)
		}
	}
}

// The control: the same verb on a confirmation with time left is not refused
// as expired. It is refused or answered for some other reason (this drill
// carries no step-up), which is the point: the expiry refusal is driven by
// the clock, not handed to every row.
func TestAnExpiredConfirmationApprovedIsRefused_WouldCatchOne(t *testing.T) {
	bin := agentcorpus.BuildRetnd(t)
	config, _ := seedConfirmations(t, bin, []confirmationRow{{"agc_live", 14 * time.Minute}})
	_, stdout, stderr := run(t, bin, nil, "agent", "confirmations", "approve", "agc_live", "--config", config)
	if strings.Contains(stdout+stderr, "AGENT_CONFIRMATION_EXPIRED") {
		t.Errorf("a confirmation with fourteen minutes left was refused as expired:\n%s\n%s", stdout, stderr)
	}
}
