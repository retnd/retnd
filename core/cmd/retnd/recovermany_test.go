package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `retnd recover --set` and `retnd recover <id> <id> ...` (#1106, #1107): the
// terminal form of recovering a whole backup set, or several backups, to one
// destination, through the same service call the HTTP route uses.

const recoverSetID = "production/postgres-primary"

func TestRecoverManyFlagsAreDiscoverableFromTheCommandLine(t *testing.T) {
	out := captureStderr(t, usage)
	for _, want := range []string{"--set", "--newest-only", "--since", "--preflight"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage() does not mention %s", want)
		}
	}
}

func TestRecoverManyIsRefusedBeforeAnythingIsOpened(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"set and an artifact", []string{"--set", recoverSetID, recoverID, "--to", "out"}, "either --set or backup ids"},
		{"set with standard output", []string{"--set", recoverSetID, "--to", "-"}, "cannot go to standard output"},
		{"several ids with standard output", []string{recoverID, recoverID + "2", "--to", "-"}, "cannot go to standard output"},
		{"set that is not a set id", []string{"--set", "nonsense", "--to", "out"}, "source/backup-set"},
		{"newest-only without a set", []string{recoverID, "--newest-only", "--to", "out"}, "--newest-only, --since and --until need --set"},
		{"bad since", []string{"--set", recoverSetID, "--since", "yesterday", "--to", "out"}, "--since"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				if code := cmdRecover(tc.args); code != exitUsage {
					t.Errorf("cmdRecover = %d, want %d", code, exitUsage)
				}
			})
			if !strings.Contains(out, tc.says) {
				t.Errorf("stderr %q does not say %q", strings.TrimSpace(out), tc.says)
			}
		})
	}
}

func TestRecoverSetToStagingPlacesEveryFileAndWritesTheManifest(t *testing.T) {
	configPath, local := committedArtifact(t)
	want, _ := os.ReadFile(local)
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"recover", "--config", configPath, "--set", recoverSetID, "--to", "restored", "--json"})
	})
	if code != 0 {
		t.Fatalf("recover --set = %d, output %q", code, out)
	}
	var res struct {
		Set    string `json:"set"`
		Totals struct {
			Recovered int `json:"recovered"`
			Failed    int `json:"failed"`
		} `json:"totals"`
		Items []struct {
			Status string `json:"status"`
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"items"`
		ManifestPath string `json:"manifest_path"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("--json output is not JSON: %q", out)
	}
	if res.Set != recoverSetID || res.Totals.Recovered != 1 || res.Totals.Failed != 0 || len(res.Items) != 1 {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(res.Items[0].Path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("placed file wrong: %v", err)
	}
	if !strings.Contains(filepath.ToSlash(res.Items[0].Path), "/restored/production/postgres-primary/") {
		t.Fatalf("not under the per-set folder: %s", res.Items[0].Path)
	}
	if _, err := os.Stat(res.ManifestPath); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestRecoverSetPrintsAPreflightAndProgressForHumans(t *testing.T) {
	configPath, _ := committedArtifact(t)
	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"recover", "--config", configPath, "--set", recoverSetID, "--to", "restored"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, output %q", code, stdout)
	}
	for _, want := range []string{"pre-flight", "backup.dump", "recovered: 1", "manifest:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
}

func TestRecoverSetPreflightAloneCopiesNothing(t *testing.T) {
	configPath, _ := committedArtifact(t)
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"recover", "--config", configPath, "--set", recoverSetID, "--to", "restored", "--preflight", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, output %q", code, out)
	}
	var plan struct {
		Totals struct {
			Ready int `json:"ready"`
		} `json:"totals"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil || plan.Totals.Ready != 1 {
		t.Fatalf("plan = %q, %v", out, err)
	}
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(configPath), "recovery", "*"))
	if len(entries) != 0 {
		t.Fatalf("a pre-flight wrote %v", entries)
	}
}

func TestRecoverSetWithACorruptCopyExitsPartialAndPlacesNothingForIt(t *testing.T) {
	configPath, local := committedArtifact(t)
	data, _ := os.ReadFile(local)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(local, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	captureStderr(t, func() {
		captureStdout(t, func() {
			code = run([]string{"recover", "--config", configPath, "--set", recoverSetID, "--to", "restored"})
		})
	})
	if code != exitRecoverPartial {
		t.Fatalf("exit = %d, want %d (some files were not recovered)", code, exitRecoverPartial)
	}
	var found []string
	_ = filepath.Walk(filepath.Join(filepath.Dir(configPath), "recovery"), func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && strings.HasSuffix(p, "backup.dump") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Fatalf("the corrupt copy reached the destination: %v", found)
	}
}

func TestRecoverSeveralIdsWithAnUnknownOneIsRefusedBeforeAnythingIsCopied(t *testing.T) {
	configPath, _ := committedArtifact(t)
	var code int
	captureStderr(t, func() {
		code = run([]string{"recover", "--config", configPath, recoverID, "production/postgres-primary/nope", "--to", "sel"})
	})
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(configPath), "recovery", "*"))
	if len(entries) != 0 {
		t.Fatalf("a refused request wrote %v", entries)
	}
}
