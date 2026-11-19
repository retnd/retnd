package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// #1174: `retnd assurance list` is the terminal's twin of the Dashboard's
// Assurance tile. The classification is pinned in core/service; these pin the
// verb: its forms, its refusals and its JSON shape.

func TestAssuranceList_EmptyDeploymentSaysSoInTextAndJSON(t *testing.T) {
	configPath := writeTestConfig(t)
	var code int
	text := captureStdout(t, func() { code = run([]string{"assurance", "list", "--config", configPath}) })
	if code != exitOK || !strings.Contains(text, "nothing is due, overdue or degraded") {
		t.Fatalf("text: exit %d, %q", code, text)
	}
	out := captureStdout(t, func() { code = run([]string{"assurance", "list", "--json", "--config", configPath}) })
	var doc struct {
		GeneratedAt string `json:"generated_at"`
		Items       []any  `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != exitOK || doc.Items == nil || doc.GeneratedAt == "" {
		t.Fatalf("json: exit %d, %q (%v)", code, out, err)
	}
}

func TestAssuranceList_RefusesWhatItCannotAnswer(t *testing.T) {
	configPath := writeTestConfig(t)
	for name, argv := range map[string][]string{
		"a bucket that does not exist": {"assurance", "list", "--bucket", "stale"},
		"a set that is not an id":      {"assurance", "list", "--backup-set", "nope"},
		"a stray operand":              {"assurance", "list", "extra"},
		"list flags on cancel":         {"assurance", "cancel", "op-1", "--bucket", "due"},
		"no form at all":               {"assurance"},
	} {
		if code := run(append(argv, "--config", configPath)); code != exitUsage {
			t.Errorf("%s exited %d, want usage (%d)", name, code, exitUsage)
		}
	}
}

func TestAssuranceList_UsageNamesTheVerbAndItsTwin(t *testing.T) {
	text := captureStderr(t, usage)
	for _, want := range []string{"assurance list [--backup-set S] [--bucket due|overdue|degraded|untested] [--json]", "Assurance tile"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}

// A set whose recovery-test policy can't be read is not a set with tests on.
// Here the set is renamed in config.yaml after a backup of it was committed,
// so its backup is still in the journal and the policy read for the old name
// fails. The listing has to say so on stderr and call the recovery claim
// unknown, not "never run", which would count it due on no evidence.
func TestAssuranceList_AnUnreadablePolicyIsUnknownAndSaidOnStderr(t *testing.T) {
	configPath, _ := oneCommittedArtifact(t)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(string(raw), "      - id: postgres-primary\n", "      - id: postgres-renamed\n", 1)
	if renamed == string(raw) {
		t.Fatal("the fixture no longer names postgres-primary the way this test renames it")
	}
	writeFileT(t, configPath, renamed)

	var code int
	var out string
	errText := captureStderr(t, func() {
		out = captureStdout(t, func() {
			code = run([]string{"assurance", "list", "--json", "--bucket", "due,untested", "--config", configPath})
		})
	})
	if code != exitOK {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errText)
	}
	if !strings.Contains(errText, "could not read production/postgres-primary's recovery-test policy") {
		t.Errorf("stderr does not say the policy read failed: %q", errText)
	}
	var doc struct {
		Items []struct {
			ArtifactID string   `json:"artifact_id"`
			Buckets    []string `json:"buckets"`
			Recovery   struct {
				State string `json:"state"`
			} `json:"recovery"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	found := false
	for _, it := range doc.Items {
		if it.ArtifactID != assuranceID {
			continue
		}
		found = true
		if it.Recovery.State != "unknown" {
			t.Errorf("recovery = %q, want unknown when the policy could not be read", it.Recovery.State)
		}
	}
	if !found {
		t.Fatalf("%s is not listed: %s", assuranceID, out)
	}
}

func TestAssuranceList_BucketTakesACommaList(t *testing.T) {
	configPath := writeTestConfig(t)
	if code := run([]string{"assurance", "list", "--bucket", "due,degraded", "--config", configPath}); code != exitOK {
		t.Errorf("a comma list of buckets exited %d", code)
	}
	if code := run([]string{"assurance", "list", "--bucket", "due,stale", "--config", configPath}); code != exitUsage {
		t.Errorf("a list with an unknown bucket exited %d, want usage", code)
	}
}

func TestAssuranceCancel_JSONHelpStillNamesTheAPIObject(t *testing.T) {
	text := captureStderr(t, func() { run([]string{"assurance", "cancel", "-h"}) })
	if !strings.Contains(text, "the same object the API serves") {
		t.Errorf("cancel --json lost its help: %q", text)
	}
	if strings.Contains(text, "-bucket") {
		t.Errorf("cancel's flags carry list's --bucket: %q", text)
	}
}
