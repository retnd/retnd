package state

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Migration 0021 and NoteDefaultSuite (EPIC T, T1.4, #1314).

func TestMigration0021IsAdditiveAndOnlyAddsTheDefaultSuiteRecord(t *testing.T) {
	m := migrationSQL(t, 21)
	if !strings.HasPrefix(m.filename, "0021_") {
		t.Fatalf("migration 21 is %s", m.filename)
	}
	sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", m.filename))
	if err != nil {
		t.Fatal(err)
	}
	stripped := regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(string(sql), "")
	n := 0
	for _, stmt := range strings.Split(stripped, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		n++
		if !strings.HasPrefix(strings.ToUpper(stmt), "CREATE TABLE ENCRYPTION_DEFAULT_SUITE") {
			t.Errorf("0021 may only create encryption_default_suite; it also does: %.80s", stmt)
		}
	}
	if n == 0 {
		t.Fatal("0021 parsed into no statements")
	}
}

func TestNoteDefaultSuiteAnnouncesEachChangeOnce(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	// No key ever: recorded silently.
	if prev, changed, err := j.NoteDefaultSuite(ctx, "age-pq/v1", at); err != nil || changed || prev != "" {
		t.Fatalf("a deployment with no key history = %q, %v, %v; want nothing to announce", prev, changed, err)
	}
	if _, changed, _ := j.NoteDefaultSuite(ctx, "age-pq/v1", at); changed {
		t.Fatal("the same default noted twice was announced")
	}
	// A later build moves it again.
	prev, changed, err := j.NoteDefaultSuite(ctx, "age/v1", at.Add(time.Hour))
	if err != nil || !changed || prev != "age-pq/v1" {
		t.Fatalf("a moved default = %q, %v, %v; want age-pq/v1 and changed", prev, changed, err)
	}
	if _, changed, _ := j.NoteDefaultSuite(ctx, "age/v1", at.Add(2*time.Hour)); changed {
		t.Fatal("a steady build was announced")
	}
}

func TestAnEPICNDeploymentMeetsTheNewDefaultAsAChangeFromAgeV1(t *testing.T) {
	ctx := context.Background()
	j, _ := openJournal(t)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := j.AddEncryptionKey(ctx, EncryptionKey{ID: "k1", Provider: KeyProviderManaged, CreatedAt: at}, ""); err != nil {
		t.Fatal(err)
	}
	prev, changed, err := j.NoteDefaultSuite(ctx, "age-pq/v1", at)
	if err != nil || !changed || prev != "age/v1" {
		t.Fatalf("an EPIC N deployment's first note = %q, %v, %v; want a change from age/v1", prev, changed, err)
	}
}
