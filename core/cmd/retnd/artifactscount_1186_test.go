package main

import (
	"strings"
	"testing"
)

// Issue #1186: the web console's sidebar Backups badge reads the number of
// retained backups, exact up to 100 and ">100" above it. Every console
// feature has a terminal equivalent, so `retnd artifacts --count` prints that
// same reading from the same list `artifacts --json` and GET /backups serve.

func TestIssue1186_RetainedBadgeIsExactUpToTheCapAndOpenEndedAbove(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, "0"}, {1, "1"}, {40, "40"}, {100, "100"}, {101, ">100"}, {119, ">100"}} {
		if got := retainedBadge(c.n); got != c.want {
			t.Errorf("retainedBadge(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestIssue1186_ArtifactsCountPrintsTheBadgeReading(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	out := artifactsOut(t, 0, "artifacts", "--count", "--config", configPath)
	if strings.TrimSpace(out) != "1" {
		t.Errorf("artifacts --count = %q, want just the reading, 1", out)
	}
	// It agrees with the list it counts.
	list := artifactsOut(t, 0, "artifacts", "--json", "--config", configPath)
	if strings.Count(list, `"backup_set_id"`) != 1 {
		t.Fatalf("control: the list does not hold exactly one backup:\n%s", list)
	}
}

func TestIssue1186_ArtifactsCountHonoursTheFilters(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	out := artifactsOut(t, 0, "artifacts", "--count", "--config", configPath, "--state", "QUARANTINED")
	if strings.TrimSpace(out) != "0" {
		t.Errorf("--count --state QUARANTINED = %q, want 0", out)
	}
}

func TestIssue1186_ArtifactsCountRefusesWhatItCannotMean(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	checkExitAndStderr(t, []string{"artifacts", "--count", "--config", configPath, assuranceID}, 2, "--count")
	checkExitAndStderr(t, []string{"artifacts", "--count", "--json", "--config", configPath}, 2, "--count")
}
