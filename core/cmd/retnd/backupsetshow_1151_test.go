package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// Issue #1151 (EPIC S, S2.1): reading a whole backup set from a terminal,
// the poll interval on both scopes, and the two identity fields a create
// could not name.
//
// Every test drives the real CLI. The oracle for "the wire object" is the
// fake engine's own mirror of the handlers (toContractBackupSet), which is
// itself pinned field by field by TestTheFixtureCarriesEveryFieldTheContractHas,
// so a field the CLI drops cannot hide behind a projection that drops it
// too.

const (
	incrementalSet  = "api/uploads"
	artifactSet     = "production/postgres-primary"
	aSetUUID        = "7d1c5a6e-3b8f-4c2a-9e41-0f6b2d8a5c13"
	incrementalMeta = "source_mount_prefix: /srv\n"
)

// runCLI runs the command and returns the exit code with what it printed
// on each stream, so a test can assert that stdout is data and nothing
// else.
func runCLI(t *testing.T, argv ...string) (code int, stdout, stderr string) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run(argv) })
	})
	return code, stdout, stderr
}

// addRepositoryDomain puts the one thing an incremental set cannot be
// created without in front of an existing configuration file. It is
// aDeploymentWithARepositoryDomain for a file somebody else already wrote,
// which a routed test needs for the engine's own copy.
func addRepositoryDomain(t *testing.T, configPath string) {
	t.Helper()
	passphrase := filepath.Join(filepath.Dir(configPath), "repo.passphrase")
	if err := os.WriteFile(passphrase, []byte("a-passphrase-long-enough-to-be-a-passphrase"), 0o600); err != nil {
		t.Fatalf("writing the repository passphrase file: %v", err)
	}
	existing := readFile(t, configPath)
	const anchor = "sources:\n"
	if !strings.Contains(existing, anchor) {
		t.Fatalf("no sources block to put a repository domain in front of:\n%s", existing)
	}
	domains := "incremental_engine:\n  enabled: true\n" +
		"repository_domains:\n" +
		"  - id: production\n" +
		"    description: Snapshots for this deployment\n" +
		"    isolation: shared\n" +
		"    passphrase:\n" +
		"      file: " + passphrase + "\n"
	if err := os.WriteFile(configPath, []byte(strings.Replace(existing, anchor, domains+anchor, 1)), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// showFixture is a deployment holding the two kinds of set a reader of
// `show` meets: the stock artifact set (inherits the deployment's cadence)
// and an incremental one that sets every optional field there is.
func showFixture(t *testing.T) (configPath string, incremental service.BackupSet) {
	t.Helper()
	configPath = aDeploymentWithARepositoryDomain(t)
	keyPath := writeTestPrivateKey(t)

	code, out, errOut := runCLI(t, incrementalCreateArgs(configPath, keyPath, incrementalSet,
		"--source-consistency", "external_snapshot",
		"--verification-level", "content_sample",
		"--verification-sample-percent", "15",
		"--verification-full-every", "168h",
		"--verification-restore-drill-every", "720h")...)
	if code != exitOK {
		t.Fatalf("seeding the incremental set exited %d\n%s\n%s", code, out, errOut)
	}

	// A mount prefix and a per-set cadence, written through the layers
	// that already carry them so this fixture does not depend on the
	// flags under test.
	raw := readFile(t, configPath)
	marker := "engine: kopia\n"
	at := strings.Index(raw, marker)
	if at < 0 {
		t.Fatalf("the seeded set carries no engine line:\n%s", raw)
	}
	lineStart := strings.LastIndex(raw[:at], "\n") + 1
	indent := raw[lineStart:at]
	raw = raw[:at+len(marker)] + indent + incrementalMeta + raw[at+len(marker):]
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc, closeFn, err := service.Open(t.Context(), configPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	defer func() { _ = closeFn() }()
	every := 20 * time.Minute
	if _, err := svc.UpdateBackupSet(t.Context(), incrementalSet, service.UpdateBackupSetRequest{PollInterval: &every}); err != nil {
		t.Fatalf("giving the incremental set its own cadence: %v", err)
	}
	incremental, err = svc.GetBackupSet(t.Context(), incrementalSet)
	if err != nil {
		t.Fatalf("GetBackupSet: %v", err)
	}

	// A fixture that leaves a field zero cannot catch a command that
	// drops it, so each field the issue names has to be non-zero here.
	for name, ok := range map[string]bool{
		"TrustedHostKeys":           len(incremental.TrustedHostKeys) > 0,
		"SSHKeyID":                  incremental.SSHKeyID != "",
		"ConnectionUnverified":      incremental.ConnectionUnverified,
		"PollInterval":              incremental.PollInterval != nil,
		"Engine":                    incremental.Engine == "kopia",
		"UUID":                      incremental.UUID != "",
		"RepositoryDomain":          incremental.RepositoryDomain != "",
		"SourceConsistency":         incremental.SourceConsistency != "",
		"VerificationLevel":         incremental.VerificationLevel != "",
		"VerificationSamplePercent": incremental.VerificationSamplePercent != 0,
		"VerificationFullEvery":     incremental.VerificationFullEvery != 0,
		"VerificationRestoreDrill":  incremental.VerificationRestoreDrillEvery != 0,
		"SourceMountPrefix":         incremental.SourceMountPrefix != "",
	} {
		if !ok {
			t.Fatalf("the fixture leaves %s empty, so a command that drops it would pass:\n%+v", name, incremental)
		}
	}
	return configPath, incremental
}

// TestBackupSetShowPrintsEveryField_1151: one line per field, each its
// own subtest so a dropped field names itself.
func TestBackupSetShowPrintsEveryField_1151(t *testing.T) {
	configPath, set := showFixture(t)

	code, stdout, stderr := runCLI(t, "backup-set", "--config", configPath, "show", incrementalSet)
	if code != exitOK {
		t.Fatalf("backup-set show exited %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.HasPrefix(stdout, "backup set: "+incrementalSet+"\n") {
		t.Errorf("the output does not open with the set's id:\n%s", stdout)
	}

	key := set.TrustedHostKeys[0]
	want := map[string]string{
		"host":                             set.Host,
		"port":                             "2222",
		"user":                             set.User,
		"remote_path":                      set.RemotePath,
		"ssh_key_id":                       set.SSHKeyID,
		"trusted_host_key":                 key.Algorithm + " " + key.Fingerprint,
		"connection_unverified":            "true",
		"poll_interval":                    "20m0s",
		"effective_poll_interval":          "20m0s",
		"engine":                           "kopia",
		"uuid":                             set.UUID,
		"repository_domain":                "production",
		"source_consistency":               "external_snapshot",
		"verification_level":               "content_sample",
		"verification_sample_percent":      "15",
		"verification_full_every":          "168h0m0s",
		"verification_restore_drill_every": "720h0m0s",
		"source_mount_prefix":              "/srv",
		"retention_is_override":            "false",
		"disabled":                         "false",
		"read_only":                        "false",
	}
	if !set.TrustedHostKeyRecordedAt.IsZero() {
		want["trusted_host_key_recorded_at"] = set.TrustedHostKeyRecordedAt.UTC().Format(time.RFC3339)
	}
	for field, value := range want {
		t.Run(field, func(t *testing.T) {
			line := "\n  " + field + ": " + value + "\n"
			if !strings.Contains(stdout, line) {
				t.Errorf("backup-set show does not print %q\n%s", strings.TrimSpace(line), stdout)
			}
		})
	}
}

// TestBackupSetShowOfAnInheritingSet_1151: the null override is a real
// answer and is said as one, beside the number actually in force; and the
// fields an artifact set cannot have are not printed as if it did.
func TestBackupSetShowOfAnInheritingSet_1151(t *testing.T) {
	configPath, _ := showFixture(t)

	code, stdout, stderr := runCLI(t, "backup-set", "--config", configPath, "show", artifactSet)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	for _, line := range []string{
		"\n  poll_interval: inherited\n",
		"\n  effective_poll_interval: 15m0s\n",
		"\n  engine: artifact\n",
	} {
		if !strings.Contains(stdout, line) {
			t.Errorf("missing %q\n%s", strings.TrimSpace(line), stdout)
		}
	}
	for _, absent := range []string{"  uuid:", "  repository_domain:", "  verification_level:", "  source_mount_prefix:"} {
		if strings.Contains(stdout, absent) {
			t.Errorf("an artifact set printed %q, a field it cannot have", strings.TrimSpace(absent))
		}
	}
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return out
}

// TestBackupSetShowJSONIsTheWireObject_1151: stdout is one JSON document,
// it is the BackupSet the API returns for the same set field for field,
// and every key the contract defines is present.
func TestBackupSetShowJSONIsTheWireObject_1151(t *testing.T) {
	configPath, set := showFixture(t)

	code, stdout, stderr := runCLI(t, "backup-set", "--config", configPath, "show", incrementalSet, "--json")
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON document (%v), so a script cannot pipe it:\n%s", err, stdout)
	}
	want := asMap(t, toContractBackupSet(set))
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("--json is not the wire object.\nCLI printed:\n%s\nAPI shape:\n%s", g, w)
	}

	typ := reflect.TypeOf(apicontract.BackupSet{})
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if _, ok := got[name]; !ok {
			t.Errorf("the contract's BackupSet carries %q and --json does not", name)
		}
	}
}

// TestSourcesJSONIsTheListResponse_1151: `sources --json` is
// ListBackupSetsResponse, not the six columns the text form prints.
func TestSourcesJSONIsTheListResponse_1151(t *testing.T) {
	configPath, incremental := showFixture(t)

	svc, closeFn, err := service.Open(t.Context(), configPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	defer func() { _ = closeFn() }()
	sets, err := svc.ListBackupSets(t.Context())
	if err != nil {
		t.Fatalf("ListBackupSets: %v", err)
	}
	if len(sets) < 2 {
		t.Fatalf("the fixture holds %d sets, want the artifact one and the incremental one", len(sets))
	}
	list := apicontract.ListBackupSetsResponse{}
	for _, s := range sets {
		list.BackupSets = append(list.BackupSets, toContractBackupSet(s))
	}

	code, stdout, stderr := runCLI(t, "sources", "--config", configPath, "--json")
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON document (%v):\n%s", err, stdout)
	}
	if want := asMap(t, list); !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("sources --json is not ListBackupSetsResponse.\nCLI printed:\n%s\nAPI shape:\n%s", g, w)
	}

	// The text form keeps the six columns it always had.
	_, text, _ := runCLI(t, "sources", "--config", configPath)
	if strings.Contains(text, incremental.UUID) || strings.HasPrefix(strings.TrimSpace(text), "{") {
		t.Errorf("plain `sources` changed shape:\n%s", text)
	}
}

func TestBackupSetShowRefusals_1151(t *testing.T) {
	configPath, _ := showFixture(t)

	for _, tc := range []struct {
		name string
		argv []string
		code int
		in   string
	}{
		{"no operand", []string{"backup-set", "--config", configPath, "show"}, 2, "show"},
		{"not an id", []string{"backup-set", "--config", configPath, "show", "justaname"}, 2, "source/name"},
		{"a flag of another verb", []string{"backup-set", "--config", configPath, "show", artifactSet, "--host", "x"}, 2, ""},
		{"a set that is not there", []string{"backup-set", "--config", configPath, "show", "nope/missing"}, 1, "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, tc.argv...)
			if code == exitOK || (tc.code == 2 && code != 2) {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.code, stdout, stderr)
			}
			if tc.in != "" && !strings.Contains(strings.ToLower(stderr), tc.in) {
				t.Errorf("stderr does not mention %q:\n%s", tc.in, stderr)
			}
			if stdout != "" {
				t.Errorf("a refused show printed on stdout:\n%s", stdout)
			}
		})
	}
}

// Poll interval, per backup set.

func patchPoll(t *testing.T, configPath string, extra ...string) (int, string, string) {
	t.Helper()
	argv := append([]string{"backup-set", "--config", configPath, "patch", artifactSet}, extra...)
	return runCLI(t, argv...)
}

func TestBackupSetPatchPollInterval_1151(t *testing.T) {
	clearInheritedRouteSettings()
	configPath := writeTestConfig(t)

	read := func() service.BackupSet {
		svc, closeFn, err := service.Open(t.Context(), configPath)
		if err != nil {
			t.Fatalf("service.Open: %v", err)
		}
		defer func() { _ = closeFn() }()
		s, err := svc.GetBackupSet(t.Context(), artifactSet)
		if err != nil {
			t.Fatalf("GetBackupSet: %v", err)
		}
		return s
	}

	code, stdout, stderr := patchPoll(t, configPath, "--poll-interval", "45m")
	if code != exitOK {
		t.Fatalf("--poll-interval 45m exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	got := read()
	if got.PollInterval == nil || *got.PollInterval != 45*time.Minute || got.EffectivePollInterval != 45*time.Minute {
		t.Errorf("the set's cadence is %v (effective %v), want an override of 45m", got.PollInterval, got.EffectivePollInterval)
	}
	if !strings.Contains(stdout, "\n  poll_interval: 45m0s\n") {
		t.Errorf("the confirmation does not say what the interval is now:\n%s", stdout)
	}

	// Zero inherits: it is a request and not an omission.
	code, stdout, stderr = patchPoll(t, configPath, "--poll-interval", "0")
	if code != exitOK {
		t.Fatalf("--poll-interval 0 exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	got = read()
	if got.PollInterval != nil || got.EffectivePollInterval != 15*time.Minute {
		t.Errorf("after 0 the set still pins %v (effective %v), want it inheriting the deployment's 15m", got.PollInterval, got.EffectivePollInterval)
	}
	if !strings.Contains(stdout, "poll_interval: inherited") {
		t.Errorf("clearing the override does not say the set now inherits:\n%s", stdout)
	}

	// Below the floor is refused by the rule that already exists, with
	// the file as it was.
	before := readFile(t, configPath)
	code, _, stderr = patchPoll(t, configPath, "--poll-interval", "1s")
	if code == exitOK {
		t.Error("a 1s poll interval was accepted; the floor is a minute")
	}
	if !strings.Contains(stderr, "poll") {
		t.Errorf("the refusal does not name the poll interval:\n%s", stderr)
	}
	if readFile(t, configPath) != before {
		t.Error("a refused patch changed config.yaml")
	}
}

func TestBackupSetPatchPollIntervalIsRoutedToTheEngine_1151(t *testing.T) {
	cliConfig := writeTestConfig(t)
	engine := startFakeEngineFor(t, cliConfig)
	engine.attach(t)
	before := readFile(t, cliConfig)

	code, stdout, stderr := patchPoll(t, cliConfig, "--poll-interval", "30m")
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !containsRequest(engine.requests(), "PATCH /backup-sets/"+artifactSet) {
		t.Errorf("the engine never saw the patch; it saw %v", engine.requests())
	}
	got, err := engine.svc.GetBackupSet(t.Context(), artifactSet)
	if err != nil {
		t.Fatalf("GetBackupSet: %v", err)
	}
	if got.PollInterval == nil || *got.PollInterval != 30*time.Minute {
		t.Errorf("the engine holds a cadence of %v, want 30m", got.PollInterval)
	}
	if readFile(t, cliConfig) != before {
		t.Error("the CLI's own config.yaml changed as well as the engine's")
	}
	if !strings.Contains(stdout, "\n  poll_interval: 30m0s\n") {
		t.Errorf("the routed confirmation does not carry the interval:\n%s", stdout)
	}
}

func TestPollIntervalIsAPatchOnlyFlagOfBackupSet_1151(t *testing.T) {
	configPath := aDeploymentWithARepositoryDomain(t)
	keyPath := writeTestPrivateKey(t)
	before := readFile(t, configPath)

	code, _, stderr := runCLI(t, createArgs(configPath, keyPath, "api/new", "--poll-interval", "10m")...)
	if code != 2 {
		t.Errorf("create --poll-interval exited %d, want 2", code)
	}
	if !strings.Contains(stderr, "--poll-interval is not a create flag") {
		t.Errorf("the refusal does not say whose flag it is:\n%s", stderr)
	}
	if readFile(t, configPath) != before {
		t.Error("a refused create wrote the file")
	}
}

// Poll interval, deployment-wide.

func TestSettingsPatchPollInterval_1151(t *testing.T) {
	clearInheritedRouteSettings()
	configPath := writeTestConfig(t)

	code, stdout, stderr := runCLI(t, "settings", "--config", configPath, "patch", "--poll-interval", "30m")
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "service:\n  poll_interval: 30m0s\n") {
		t.Errorf("settings patch does not print the interval it set:\n%s", stdout)
	}
	if !strings.Contains(readFile(t, configPath), "poll_interval: 30m") {
		t.Errorf("the file does not carry the new interval:\n%s", readFile(t, configPath))
	}

	// The plain read prints it too.
	_, stdout, _ = runCLI(t, "settings", "--config", configPath)
	if !strings.Contains(stdout, "service:\n  poll_interval: 30m0s\n") {
		t.Errorf("`settings` does not print the deployment's poll interval:\n%s", stdout)
	}

	// A set that inherits shows the new number as its effective one.
	_, stdout, _ = runCLI(t, "backup-set", "--config", configPath, "show", artifactSet)
	if !strings.Contains(stdout, "\n  effective_poll_interval: 30m0s\n") {
		t.Errorf("an inheriting set does not follow the deployment's interval:\n%s", stdout)
	}
}

func TestSettingsPollIntervalWithoutPatchIsRefused_1151(t *testing.T) {
	configPath := writeTestConfig(t)
	before := readFile(t, configPath)

	code, _, stderr := runCLI(t, "settings", "--config", configPath, "--poll-interval", "30m")
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr, "poll-interval") || !strings.Contains(stderr, "patch") {
		t.Errorf("the refusal does not point at `settings patch`:\n%s", stderr)
	}
	if readFile(t, configPath) != before {
		t.Error("the file changed")
	}
}

func TestSettingsPatchPollIntervalBelowTheFloorIsRefused_1151(t *testing.T) {
	clearInheritedRouteSettings()
	configPath := writeTestConfig(t)
	before := readFile(t, configPath)

	code, _, stderr := runCLI(t, "settings", "--config", configPath, "patch", "--poll-interval", "0")
	if code == exitOK {
		t.Error("a zero deployment poll interval was accepted; zero only means inherit on a backup set")
	}
	if !strings.Contains(stderr, "poll") {
		t.Errorf("the refusal does not name the poll interval:\n%s", stderr)
	}
	if readFile(t, configPath) != before {
		t.Error("a refused patch changed config.yaml")
	}
}

func TestSettingsPatchPollIntervalIsRoutedToTheEngine_1151(t *testing.T) {
	cliConfig := writeTestConfig(t)
	engine := startFakeEngineFor(t, cliConfig)
	engine.attach(t)
	before := readFile(t, cliConfig)

	code, stdout, stderr := runCLI(t, "settings", "--config", cliConfig, "patch", "--poll-interval", "2h")
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !containsRequest(engine.requests(), "PATCH /settings") {
		t.Errorf("the engine never saw the patch; it saw %v", engine.requests())
	}
	settings, err := engine.svc.Settings(t.Context())
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.Service.PollInterval != 2*time.Hour {
		t.Errorf("the engine holds %v, want 2h", settings.Service.PollInterval)
	}
	if readFile(t, cliConfig) != before {
		t.Error("the CLI's own config.yaml changed as well as the engine's")
	}
	if !strings.Contains(stdout, "service:\n  poll_interval: 2h0m0s\n") {
		t.Errorf("the routed confirmation does not carry the interval:\n%s", stdout)
	}
}

// Identity on create.

func TestBackupSetCreateTakesTheLineageUUIDAndMountPrefix_1151(t *testing.T) {
	clearInheritedRouteSettings()
	configPath := aDeploymentWithARepositoryDomain(t)
	keyPath := writeTestPrivateKey(t)

	code, stdout, stderr := runCLI(t, incrementalCreateArgs(configPath, keyPath, incrementalSet,
		"--uuid", aSetUUID, "--source-mount-prefix", "/srv")...)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	written := readFile(t, configPath)
	for _, want := range []string{"uuid: " + aSetUUID, "source_mount_prefix: /srv"} {
		if !strings.Contains(written, want) {
			t.Errorf("the configuration does not carry %q, so the flag reached a request and not the file:\n%s", want, written)
		}
	}
	if !strings.Contains(stdout, "\n  uuid: "+aSetUUID+"\n") {
		t.Errorf("the confirmation does not print the lineage key:\n%s", stdout)
	}
	if !strings.Contains(stdout, "\n  source_mount_prefix: /srv\n") {
		t.Errorf("the confirmation does not print the mount prefix:\n%s", stdout)
	}
}

// Without --uuid the service still mints one, and now the operator is
// told which, because that is what they need to re-declare the set.
func TestBackupSetCreateWithoutUUIDStillMintsAndSaysWhich_1151(t *testing.T) {
	clearInheritedRouteSettings()
	configPath := aDeploymentWithARepositoryDomain(t)
	keyPath := writeTestPrivateKey(t)

	code, stdout, stderr := runCLI(t, incrementalCreateArgs(configPath, keyPath, incrementalSet)...)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	svc, closeFn, err := service.Open(t.Context(), configPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	defer func() { _ = closeFn() }()
	got, err := svc.GetBackupSet(t.Context(), incrementalSet)
	if err != nil || got.UUID == "" {
		t.Fatalf("no minted uuid on the written set: %v %+v", err, got)
	}
	if !strings.Contains(stdout, "\n  uuid: "+got.UUID+"\n") {
		t.Errorf("the create does not print the uuid it minted:\n%s", stdout)
	}
}

func TestBackupSetCreateUUIDIsRoutedToTheEngine_1151(t *testing.T) {
	cliConfig := aDeploymentWithARepositoryDomain(t)
	engineConfig := writeTestConfigFor(t, cliConfig)
	addRepositoryDomain(t, engineConfig)
	engine := startFakeEngine(t, engineConfig)
	engine.attach(t)
	keyPath := writeTestPrivateKey(t)
	before := readFile(t, cliConfig)

	code, stdout, stderr := runCLI(t, incrementalCreateArgs(cliConfig, keyPath, incrementalSet,
		"--uuid", aSetUUID, "--source-mount-prefix", "/srv")...)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !containsRequest(engine.requests(), "POST /backup-sets") {
		t.Errorf("the engine never saw the create; it saw %v", engine.requests())
	}
	got, err := engine.svc.GetBackupSet(t.Context(), incrementalSet)
	if err != nil {
		t.Fatalf("the engine does not hold the set: %v", err)
	}
	if got.UUID != aSetUUID {
		t.Errorf("the engine minted its own lineage key %q instead of the one asked for, which forks the history", got.UUID)
	}
	if got.SourceMountPrefix != "/srv" {
		t.Errorf("the engine's mount prefix is %q, want /srv", got.SourceMountPrefix)
	}
	if readFile(t, cliConfig) != before {
		t.Error("the CLI's own config.yaml changed as well as the engine's")
	}
	if !strings.Contains(stdout, "\n  uuid: "+aSetUUID+"\n") {
		t.Errorf("the routed confirmation does not print the lineage key:\n%s", stdout)
	}
}

func TestIdentityFlagsAreCreateOnly_1151(t *testing.T) {
	configPath := writeTestConfig(t)
	before := readFile(t, configPath)

	for _, flag := range []string{"--uuid", "--source-mount-prefix"} {
		t.Run(flag, func(t *testing.T) {
			value := aSetUUID
			if flag == "--source-mount-prefix" {
				value = "/srv"
			}
			code, _, stderr := runCLI(t, "backup-set", "--config", configPath, "patch", artifactSet, flag, value)
			if code != 2 {
				t.Errorf("patch %s exited %d, want 2", flag, code)
			}
			if !strings.Contains(stderr, flag+" is not a patch flag") {
				t.Errorf("the refusal does not say whose flag it is:\n%s", stderr)
			}
			if readFile(t, configPath) != before {
				t.Error("the file changed")
			}
		})
	}
}

func TestUUIDOnAnArtifactSetIsRefusedByTheRuleThatAlreadyExists_1151(t *testing.T) {
	clearInheritedRouteSettings()
	configPath := writeTestConfig(t)
	keyPath := writeTestPrivateKey(t)
	before := readFile(t, configPath)

	code, _, stderr := runCLI(t, createArgs(configPath, keyPath, "api/new", "--uuid", aSetUUID)...)
	if code == exitOK {
		t.Fatal("an artifact set was created with a lineage key")
	}
	if strings.Contains(stderr, "not defined") {
		t.Errorf("the flag does not exist, so this is not the validator speaking:\n%s", stderr)
	}
	if !strings.Contains(strings.ToLower(stderr), "uuid") {
		t.Errorf("the refusal does not name the uuid:\n%s", stderr)
	}
	if readFile(t, configPath) != before {
		t.Error("a refused create wrote the file")
	}
}

func TestUsageNamesTheNewSurface_1151(t *testing.T) {
	out := captureStderr(t, usage)
	for _, want := range []string{
		"backup-set show <source/backup-set> [--json]",
		"sources [--json]",
		"--poll-interval",
		"--uuid",
		"--source-mount-prefix",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage() does not mention %q", want)
		}
	}
}
