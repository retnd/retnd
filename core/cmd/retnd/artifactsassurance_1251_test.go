package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// The review of #1256, O6. The Backups page narrows its list to one assurance
// bucket (?assurance=due|overdue|degraded|untested), and the terminal had no
// way to list the same backups in the same form, so the page could print no
// "Same list from the terminal" line for it. `artifacts --assurance BUCKET`
// is that line. It is not a second classification: it asks
// service.ListAssurance, the port of the page's own claims that `assurance
// list --bucket` prints, so the two verbs must name exactly the same backups
// for every bucket, and this file checks that they do on a fresh deployment
// and on one with a rotted copy.

func assuranceListIDs(t *testing.T, configPath, bucket string) []string {
	t.Helper()
	out := artifactsOut(t, 0, "assurance", "list", "--json", "--bucket", bucket, "--config", configPath)
	var got struct {
		Items []struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("assurance list --json: %v\n%s", err, out)
	}
	ids := []string{}
	for _, it := range got.Items {
		ids = append(ids, it.ArtifactID)
	}
	sort.Strings(ids)
	return ids
}

func artifactsIDs(t *testing.T, configPath string, args ...string) []string {
	t.Helper()
	out := artifactsOut(t, 0, append([]string{"artifacts", "--json", "--config", configPath}, args...)...)
	var got apicontract.ListArtifactsResponse
	strictDecode(t, out, &got)
	ids := []string{}
	for _, a := range got.Artifacts {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestIssue1251_ArtifactsAssuranceListsWhatAssuranceListLists(t *testing.T) {
	for name, configPath := range map[string]string{
		"three fresh backups": receivedRangeDeployment(t),
		"a rotted copy":       scrubbedRottedCopy(t),
	} {
		for _, bucket := range []string{"due", "overdue", "degraded", "untested"} {
			want := assuranceListIDs(t, configPath, bucket)
			got := artifactsIDs(t, configPath, "--assurance", bucket)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s, %s: artifacts --assurance = %v, assurance list --bucket = %v", name, bucket, got, want)
			}
			text := artifactsOut(t, 0, "artifacts", "--assurance", bucket, "--config", configPath)
			if !strings.Contains(text, strconv.Itoa(len(want))+" artifact(s)") {
				t.Errorf("%s, %s: the text list does not count %d:\n%s", name, bucket, len(want), text)
			}
		}
	}
}

func TestIssue1251_ArtifactsAssuranceCombinesWithTheRangeAndTheCount(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	untested := assuranceListIDs(t, configPath, "untested")
	if len(untested) == 0 {
		t.Fatal("fixture: a fresh backup with no recovery test should be in untested")
	}
	inRange := map[string]bool{}
	for _, id := range artifactsIDs(t, configPath, "--until", "2026-03-15") {
		inRange[id] = true
	}
	want := []string{}
	for _, id := range untested {
		if inRange[id] {
			want = append(want, id)
		}
	}
	got := artifactsIDs(t, configPath, "--assurance", "untested", "--until", "2026-03-15")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("--assurance untested --until 2026-03-15 = %v, want %v", got, want)
	}
	out := artifactsOut(t, 0, "artifacts", "--count", "--assurance", "untested", "--until", "2026-03-15", "--config", configPath)
	if strings.TrimSpace(out) != strconv.Itoa(len(want)) {
		t.Errorf("--count with --assurance and a range = %q, want %d", out, len(want))
	}
}

func TestIssue1251_ArtifactsAssuranceRefusals(t *testing.T) {
	configPath := receivedRangeDeployment(t)
	checkExitAndStderr(t, []string{"artifacts", "--assurance", "worrying", "--config", configPath}, exitUsage, "--assurance")
	checkExitAndStderr(t, []string{"artifacts", "--assurance", "untested", "--config", configPath, recoverID}, exitUsage, "--assurance")
}

func TestIssue1251_ArtifactsUsageOffersAssurance(t *testing.T) {
	text := captureStderr(t, usage)
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "  artifacts ") && strings.Contains(l, "--assurance") {
			line = l
		}
	}
	if !strings.Contains(line, "[--assurance due|overdue|degraded|untested]") {
		t.Errorf("usage() has no artifacts synopsis offering --assurance; found %q", line)
	}
}
