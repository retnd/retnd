package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
)

// Panel finding C7 on retnd#1050. An empty cycle is a normal INFO line on a
// fresh install, but the same line over a journal that already holds backups
// is a deployment that lost its sets, and that has to stay as loud as it was
// before (issue #412).

func emptyCycleLines(t *testing.T, populated bool) []string {
	t.Helper()
	ctx := context.Background()
	journal := openJournal(t)
	if populated {
		bs := testBackupSet(t, t.TempDir())
		seedMovableArtifact(t, ctx, journal, bs, "old.dump", retentionTestNow.Add(-48*time.Hour))
	}
	var buf bytes.Buffer
	svc := New(testConfig(t), journal, newFakeTransport(), obs.New(&buf, obs.LevelInfo))
	svc.Now = fixedNow(retentionTestNow)
	svc.RunCycle(ctx)
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(line, "no backup sets are configured") {
			out = append(out, line)
		}
	}
	return out
}

func TestEmptyCycleOverAPopulatedJournalIsAnError(t *testing.T) {
	lines := emptyCycleLines(t, true)
	if len(lines) != 1 {
		t.Fatalf("got %d lines about the empty cycle, want 1: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"level":"ERROR"`) {
		t.Errorf("the journal holds backups and the configuration declares none, but the cycle says so below ERROR:\n%s", lines[0])
	}
	if !strings.Contains(lines[0], "journal") {
		t.Errorf("the line does not say the journal holds backups:\n%s", lines[0])
	}
}

func TestEmptyCycleOverAnEmptyJournalStaysInfo(t *testing.T) {
	lines := emptyCycleLines(t, false)
	if len(lines) != 1 || !strings.Contains(lines[0], `"level":"INFO"`) {
		t.Fatalf("a fresh install's empty cycle must stay one INFO line, got: %v", lines)
	}
}
