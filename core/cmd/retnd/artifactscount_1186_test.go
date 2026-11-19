package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Issue #1186, and the review of #1187. `retnd artifacts --count` is the
// terminal's side of the sidebar's Backups badge: how many backups this
// deployment holds outside quarantine, the same figure GET /system/health
// serves as retained_count. It prints the number itself, never the badge's
// ">100", because `n=$(retnd artifacts --count)` has to get a number exactly
// when the number is large. The 100 cap is a sidebar-width decision and stays
// in the console (ui/shared/src/layouts/navCounts.ts, BACKUPS_BADGE_CAP).

func TestIssue1186_ArtifactsCountPrintsTheNumber(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	out := artifactsOut(t, 0, "artifacts", "--count", "--config", configPath)
	if strings.TrimSpace(out) != "1" {
		t.Errorf("artifacts --count = %q, want 1", out)
	}
}

func TestIssue1186_ArtifactsCountJSONIsAnObjectWithTheNumber(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	out := artifactsOut(t, 0, "artifacts", "--count", "--json", "--config", configPath)
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("artifacts --count --json is not JSON: %v\n%s", err, out)
	}
	if len(got) != 1 || got["count"] != float64(1) {
		t.Errorf("artifacts --count --json = %s, want {\"count\": 1}", out)
	}
}

func TestIssue1186_ArtifactsCountLeavesQuarantineOut(t *testing.T) {
	configPath, localFinal := oneCommittedArtifact(t)
	writeFileT(t, localFinal, "tampered bytes that do not match the recorded hash")
	_ = run([]string{"run", "--config", configPath}) // the reconcile that quarantines it

	list := artifactsOut(t, 0, "artifacts", "--config", configPath)
	if !strings.Contains(list, "QUARANTINED") || !strings.Contains(list, "1 artifact(s)") {
		t.Fatalf("fixture: the list should hold the one backup, quarantined:\n%s", list)
	}
	if out := artifactsOut(t, 0, "artifacts", "--count", "--config", configPath); strings.TrimSpace(out) != "0" {
		t.Errorf("artifacts --count = %q, want 0: a quarantined backup is the Quarantine badge's, not this one's", out)
	}
	// Asking for a state counts that state, quarantine included.
	q := artifactsOut(t, 0, "artifacts", "--count", "--state", "QUARANTINED", "--config", configPath)
	ql := artifactsOut(t, 0, "artifacts", "--count", "--state", "QUARANTINED_LOST", "--config", configPath)
	if strings.TrimSpace(q)+"+"+strings.TrimSpace(ql) != "1+0" && strings.TrimSpace(q)+"+"+strings.TrimSpace(ql) != "0+1" {
		t.Errorf("--count --state over the two quarantine states = %q and %q, want one of them to be 1", q, ql)
	}
}

func TestIssue1186_ArtifactsCountRefusesAnArtifactArgument(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	checkExitAndStderr(t, []string{"artifacts", "--count", "--config", configPath, assuranceID}, 2, "--count")
}

func TestIssue1186_UsageSaysWhatCountCountsAndTakesState(t *testing.T) {
	text := captureStderr(t, usage)
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "artifacts ") && strings.Contains(l, "--count") {
			line = l
		}
	}
	if !strings.Contains(line, "[--state S]") || !strings.Contains(line, "[--json]") {
		t.Errorf("usage line for --count = %q, want it to offer [--state S] and [--json]", line)
	}
	if !strings.Contains(text, "outside quarantine") {
		t.Errorf("usage() does not say what --count counts:\n%s", text)
	}
	if strings.Contains(line, ">100") {
		t.Errorf("usage still promises the badge's >100 as the terminal's output: %q", line)
	}
}
