package webhost

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// EPIC Q, Q1.4 (#982). The claim these tests exist for is the one the
// retention override flags once failed: a change to the DEPLOYMENT's policy
// has to move a set that inherits it, through the real route, on the next
// pass, with no restart. A unit test of the resolver cannot show that. It
// resolves correctly in isolation and still leaves every set deciding under
// the old policy if nothing re-resolves after the write.
//
// So this drives the real chi router over a real service opened from a real
// config file, with two real sets that have each really run a cycle, and
// reads the schedule back after each write.

const (
	alphaSet = "production/alpha"
	betaSet  = "production/beta"
)

// writeAssuranceRouteConfig is a two-set deployment: alpha inherits the
// assurance policy, beta pins a five hour scrub cadence of its own. Each set
// has its own remote so one set's cycle cannot take the other's file.
func writeAssuranceRouteConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	set := func(name, extra string) string {
		remote := filepath.Join(dir, "remote-"+name)
		if err := os.MkdirAll(remote, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remote, name+".dump"), []byte("payload for "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		return "      - id: " + name + "\n" +
			"        remote:\n          type: local\n" +
			"        remote_path: " + remote + "\n" +
			"        local_path: " + filepath.Join(dir, "local-"+name) + "\n" +
			"        include:\n          - \"*.dump\"\n" +
			"        completion:\n          strategy: rename\n" +
			"        stale_after: 24h\n" + extra
	}
	content := "poll_interval: 15m\n" +
		"state:\n  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n  - id: production\n    backup_sets:\n" +
		set("alpha", "") +
		set("beta", "        assurance:\n          scrub:\n            cadence: 5h\n") +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "run", "--config", path)
	// And each copy has been through a scrub, which is what gives it a
	// schedule of its own; the due list reads that schedule, not a clock
	// recomputed from the artifact.
	runCLI(t, "scrub", "--config", path)
	return path
}

type dueRow struct {
	BackupSetID    string `json:"backup_set_id"`
	ArtifactName   string `json:"artifact_name"`
	LastAssuredAt  string `json:"last_assured_at"`
	NextDueAt      string `json:"next_due_at"`
	CadenceSeconds int64  `json:"cadence_seconds"`
	Due            bool   `json:"due"`
	Overdue        bool   `json:"overdue"`
	Basis          string `json:"basis"`
}

func readDue(t *testing.T, router http.Handler, query string) map[string]dueRow {
	t.Helper()
	code, body := call(t, router, http.MethodGet, "/api/v1/assurance/due"+query, "")
	if code != http.StatusOK {
		t.Fatalf("GET /assurance/due%s = %d: %s", query, code, body)
	}
	var doc struct {
		Items []dueRow `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("decoding the schedule: %v\n%s", err, body)
	}
	out := map[string]dueRow{}
	for _, it := range doc.Items {
		out[it.BackupSetID] = it
	}
	return out
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return ts
}

func TestAssuranceRoute_AGlobalPatchMovesAnInheritingSetAndLeavesAPinnedOneAlone(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()

	before := readDue(t, router, "")
	alpha, beta := before[alphaSet], before[betaSet]
	if alpha.ArtifactName == "" || beta.ArtifactName == "" {
		t.Fatalf("the fixture must hold a scrub-eligible copy in both sets, got %+v", before)
	}
	if alpha.CadenceSeconds != 7*24*3600 || beta.CadenceSeconds != 5*3600 {
		t.Fatalf("starting cadences = %d and %d, want the 7 day default and the pinned 5h", alpha.CadenceSeconds, beta.CadenceSeconds)
	}
	if alpha.Basis == "never" || alpha.LastAssuredAt == "" {
		t.Fatalf("a copy that really ran should have an assured time, got %+v", alpha)
	}

	// The write, through the route, on the same router that read the above.
	code, body := call(t, router, http.MethodPatch, "/api/v1/settings/assurance", `{"scrub_cadence_seconds":43200}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH /settings/assurance = %d: %s", code, body)
	}
	var settings struct {
		Configured bool `json:"configured"`
		Scrub      struct {
			CadenceSeconds int64 `json:"cadence_seconds"`
		} `json:"scrub"`
	}
	_ = json.Unmarshal([]byte(body), &settings)
	if !settings.Configured || settings.Scrub.CadenceSeconds != 43200 {
		t.Errorf("the patch answered %+v, want configured with a 12h cadence", settings)
	}

	after := readDue(t, router, "")
	a2, b2 := after[alphaSet], after[betaSet]
	if a2.CadenceSeconds != 43200 {
		t.Errorf("the inheriting set still decides under %ds after the write; the global patch did not reach it", a2.CadenceSeconds)
	}
	// A shortened cadence applies to a copy that was scheduled under the old
	// one at once: it is owed a look once its last pass is older than the new
	// cadence plus the jitter the schedule could have added (10%).
	if want := mustParse(t, a2.LastAssuredAt).Add(12*time.Hour + 72*time.Minute); !mustParse(t, a2.NextDueAt).Equal(want) {
		t.Errorf("the inheriting set's next due = %s, want last assured + 12h + 10%% = %s", a2.NextDueAt, want)
	}
	if mustParse(t, a2.NextDueAt).Equal(mustParse(t, alpha.NextDueAt)) {
		t.Error("the inheriting set's next due did not move")
	}
	if b2.CadenceSeconds != 5*3600 || b2.NextDueAt != beta.NextDueAt {
		t.Errorf("the pinned set moved with the deployment: %+v -> %+v", beta, b2)
	}
}

func TestAssuranceRoute_PatchResetAndTheSetsOwnWordSurviveAGlobalChange(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()

	code, body := call(t, router, http.MethodPatch, "/api/v1/backup-sets/production/alpha/assurance", `{"scrub_cadence_seconds":7200}`)
	if code != http.StatusOK || !strings.Contains(body, `"is_override":true`) {
		t.Fatalf("PATCH set = %d: %s", code, body)
	}
	if code, body = call(t, router, http.MethodPatch, "/api/v1/settings/assurance", `{"scrub_cadence_seconds":86400}`); code != http.StatusOK {
		t.Fatalf("PATCH global = %d: %s", code, body)
	}
	if got := readDue(t, router, "")[alphaSet].CadenceSeconds; got != 7200 {
		t.Errorf("a set's own cadence was overwritten by a global change: %d", got)
	}

	code, body = call(t, router, http.MethodDelete, "/api/v1/backup-sets/production/alpha/assurance", "")
	if code != http.StatusOK || !strings.Contains(body, `"is_override":false`) {
		t.Fatalf("DELETE set = %d: %s", code, body)
	}
	if got := readDue(t, router, "")[alphaSet].CadenceSeconds; got != 86400 {
		t.Errorf("after a reset the set decides under %ds, want the deployment's 86400", got)
	}
	if code, _ = call(t, router, http.MethodDelete, "/api/v1/backup-sets/production/alpha/assurance", ""); code != http.StatusOK {
		t.Errorf("resetting a set that already inherits = %d, want 200", code)
	}
}

// The refusals, with the codes the CLI has to match.
func TestAssuranceRoute_RefusalsLeaveTheFileAloneAndNameTheirCode(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()
	before, _ := os.ReadFile(configPath)

	cases := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"cadence under the floor", http.MethodPatch, "/api/v1/settings/assurance", `{"scrub_cadence_seconds":60}`, 400, "INVALID_REQUEST"},
		{"an empty patch", http.MethodPatch, "/api/v1/settings/assurance", `{}`, 400, "INVALID_REQUEST"},
		{"required with no validator", http.MethodPatch, "/api/v1/backup-sets/production/alpha/assurance", `{"recovery_test_mode":"required"}`, 400, "INVALID_REQUEST"},
		{"an unknown mode", http.MethodPatch, "/api/v1/settings/assurance", `{"recovery_test_mode":"sometimes"}`, 400, "INVALID_REQUEST"},
		{"an empty set patch", http.MethodPatch, "/api/v1/backup-sets/production/alpha/assurance", `{}`, 400, "INVALID_REQUEST"},
		{"an unknown set", http.MethodGet, "/api/v1/backup-sets/production/nope/assurance", "", 404, "BACKUP_SET_NOT_FOUND"},
		{"an unknown set write", http.MethodPatch, "/api/v1/backup-sets/production/nope/assurance", `{"scrub_cadence_seconds":7200}`, 404, "BACKUP_SET_NOT_FOUND"},
		{"an unknown set in the schedule", http.MethodGet, "/api/v1/assurance/due?backup_set=production/nope", "", 404, "BACKUP_SET_NOT_FOUND"},
		{"a bad status", http.MethodGet, "/api/v1/assurance/due?status=bogus", "", 400, "INVALID_REQUEST"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := call(t, router, tc.method, tc.path, tc.body)
			if code != tc.status || !strings.Contains(body, tc.code) {
				t.Errorf("= %d %s, want %d %s", code, body, tc.status, tc.code)
			}
		})
	}
	if after, _ := os.ReadFile(configPath); string(after) != string(before) {
		t.Error("a refused write changed the configuration file")
	}
}

func TestAssuranceRoute_EveryHostScopedKnobCarriesItsWrittenNo(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()

	code, body := call(t, router, http.MethodGet, "/api/v1/settings/assurance", "")
	if code != http.StatusOK {
		t.Fatalf("= %d: %s", code, body)
	}
	var doc struct {
		Configured bool `json:"configured"`
		HostKnobs  []struct {
			Name   string `json:"name"`
			PerSet bool   `json:"per_set"`
			Reason string `json:"reason"`
		} `json:"host_knobs"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Configured {
		t.Error("a file with no top-level assurance block must report configured=false")
	}
	names := map[string]bool{}
	for _, k := range doc.HostKnobs {
		names[k.Name] = true
		if k.PerSet || len(strings.Fields(k.Reason)) < 6 {
			t.Errorf("%s: per_set=%v reason=%q, want a no with a real reason", k.Name, k.PerSet, k.Reason)
		}
	}
	for _, want := range []string{"staging_root", "staging_ttl", "max_concurrent_full_reads", "max_per_destination"} {
		if !names[want] {
			t.Errorf("no written answer for %s", want)
		}
	}
}

// A write must not freeze the defaults of everything it did not touch into
// the operator's file.
func TestAssuranceRoute_APatchWritesOnlyWhatItNamed(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()
	if code, body := call(t, router, http.MethodPatch, "/api/v1/settings/assurance", `{"scrub_strategy":"content"}`); code != http.StatusOK {
		t.Fatalf("= %d: %s", code, body)
	}
	raw, _ := os.ReadFile(configPath)
	s := string(raw)
	if !strings.Contains(s, "strategy: content") {
		t.Errorf("the patch was not persisted:\n%s", s)
	}
	for _, leaked := range []string{"staging_ttl", "max_concurrent_full_reads", "recovery_test"} {
		if strings.Contains(s, leaked) {
			t.Errorf("the write froze %s into the file:\n%s", leaked, s)
		}
	}
}

// The browser and the terminal refuse the same thing with the same sentence
// and the same class of failure: the route answers 400 INVALID_REQUEST and
// the CLI exits 1 (the ordinary failure) printing the same validator text.
// Both go through core/service, so the sentence is generated once.
func TestAssuranceRoute_TheCLIAndTheRouteRefuseTheSameInputTheSameWay(t *testing.T) {
	cases := []struct {
		name string
		body string
		args []string
		want string
	}{
		{"cadence under the floor", `{"scrub_cadence_seconds":60}`, []string{"settings", "assurance", "patch", "--scrub-cadence", "1m"}, "below the floor"},
		{"required with no validator", `{"recovery_test_mode":"required"}`, []string{"settings", "assurance", "patch", "--recovery-test-mode", "required"}, "needs a validator"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routeCfg := writeAssuranceRouteConfig(t)
			router, closeRouter := gatedBoundaryRouter(t, routeCfg)
			defer closeRouter()
			code, body := call(t, router, http.MethodPatch, "/api/v1/settings/assurance", tc.body)
			if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Fatalf("route = %d %s, want 400 containing %q", code, body, tc.want)
			}

			cliCfg := writeAssuranceRouteConfig(t)
			cmd := exec.Command(backupManagerCLI(t), append(tc.args, "--config", cliCfg)...)
			out, err := cmd.CombinedOutput()
			exit := 0
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			}
			if exit != 1 || !strings.Contains(string(out), tc.want) {
				t.Errorf("cli exit %d: %s, want exit 1 containing %q", exit, out, tc.want)
			}
		})
	}
}

// A write through the route is what the terminal then reads, and the JSON a
// script gets from either is the same body.
func TestAssuranceRoute_TheCLIReadsWhatTheRouteWroteAndPrintsTheSameBody(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	code, routeBody := call(t, router, http.MethodPatch, "/api/v1/settings/assurance", `{"scrub_cadence_seconds":43200}`)
	closeRouter()
	if code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", code, routeBody)
	}
	cli := runCLI(t, "settings", "assurance", "--json", "--config", configPath)
	var a, b any
	if err := json.Unmarshal([]byte(routeBody), &a); err != nil {
		t.Fatal(err)
	}
	// The CLI may print startup events before the object on a write, never on a read.
	if err := json.Unmarshal([]byte(cli), &b); err != nil {
		t.Fatalf("cli output is not one JSON object: %v\n%s", err, cli)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("the route and the CLI disagree:\nroute %s\ncli   %s", routeBody, cli)
	}
}

// Panel finding C2: nothing could start a recovery test. The route answers at
// once, because a test reads a whole artifact and runs the operator's script,
// and hands back the operation id the activity feed follows it by.
func TestAssuranceRoute_StartRecoveryTestAnswers202WithAnOperationId(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()

	code, body := call(t, router, http.MethodPost, "/api/v1/backup-sets/production/alpha/recovery-test", `{}`)
	if code != http.StatusAccepted {
		t.Fatalf("= %d: %s", code, body)
	}
	var doc struct {
		BackupSetID string `json:"backup_set_id"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("decoding: %v\n%s", err, body)
	}
	if doc.BackupSetID != alphaSet || len(doc.OperationID) < 8 {
		t.Errorf("answer = %+v, want the set and an operation id", doc)
	}

	// The body is optional.
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, body = call(t, router, http.MethodPost, "/api/v1/backup-sets/production/beta/recovery-test", "")
		if code == http.StatusAccepted {
			break
		}
		if code != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("an empty body = %d: %s", code, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAssuranceRoute_StartRecoveryTestRefusalsHaveTheirOwnCodes(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, configPath)
	defer closeRouter()

	cases := []struct {
		name, path, body string
		status           int
		code             string
	}{
		{"an unknown set", "/api/v1/backup-sets/production/nope/recovery-test", `{}`, 404, "BACKUP_SET_NOT_FOUND"},
		{"a malformed artifact", "/api/v1/backup-sets/production/alpha/recovery-test", `{"artifact_id":"not-an-artifact"}`, 400, "INVALID_REQUEST"},
		{"an artifact in another set", "/api/v1/backup-sets/production/alpha/recovery-test", `{"artifact_id":"production/beta/beta.dump"}`, 400, "INVALID_REQUEST"},
		{"an artifact nobody journalled", "/api/v1/backup-sets/production/alpha/recovery-test", `{"artifact_id":"production/alpha/ghost.dump"}`, 404, "ARTIFACT_NOT_FOUND"},
		{"an unknown field", "/api/v1/backup-sets/production/alpha/recovery-test", `{"force":true}`, 400, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := call(t, router, http.MethodPost, tc.path, tc.body)
			if code != tc.status || !strings.Contains(body, tc.code) {
				t.Errorf("= %d %s, want %d %s", code, body, tc.status, tc.code)
			}
		})
	}
}
