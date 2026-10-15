package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1152: version --engine, validators, system capabilities, system first-run
// and the first-run steps `status` shows on a fresh install.
//
// The engine-only ones ask the serving process because the answer is a fact
// about it (its configuration revision, the platform it runs on, whether its
// administrator exists) that nothing in this process can know. The fake
// engine serves /system/version off a real service; the two routes it does
// not serve are added here, in front of it, so the shared fixture stays as it
// is.

// serveAlso answers extra GET routes (by "METHOD /path" under /api/v1) with
// the given bodies, behind the same session check the engine applies, and
// leaves everything else to the engine.
func serveAlso(e *fakeEngine, routes map[string]any) {
	e.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + strings.TrimPrefix(r.URL.Path, apicontract.BasePath)
		body, ok := routes[key]
		if !ok {
			e.ServeHTTP(w, r)
			return
		}
		e.issueCSRF(w, r)
		if !e.signedIn(r) {
			refuse(w, http.StatusUnauthorized, apicontract.ErrorCodeUnauthenticated, "no session")
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
}

func TestVersionEngineJSON_IsTheVersionResponse_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	e := startFakeEngine(t, configPath)
	e.attach(t)

	out, code := cliJSON(t, "version", "--engine", "--json")
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	var got apicontract.VersionResponse
	strictDecode(t, out, &got)
	if got.APIVersion != apicontract.Version || got.EngineVersion == "" || got.CoreVersion == "" {
		t.Errorf("versions = %+v", got)
	}
	if got.ConfigRevision != e.svc.ConfigRevision() || got.ConfigRevision == "" {
		t.Errorf("config_revision = %q, want the engine's %q", got.ConfigRevision, e.svc.ConfigRevision())
	}
	if got.DeploymentID != e.svc.DeploymentID() || got.DeploymentID == "" {
		t.Errorf("deployment_id = %q, want the engine's", got.DeploymentID)
	}
	if !got.Ready || !got.Configured {
		t.Errorf("ready=%v configured=%v for an engine that is serving a configuration", got.Ready, got.Configured)
	}
	// adopted_paths is an array on the contract even when the engine adopted
	// nothing: one iterates and the other has to be guarded.
	if !strings.Contains(strings.Join(strings.Fields(out), ""), `"adopted_paths":[]`) {
		t.Errorf("adopted_paths is not an empty array on the wire:\n%s", out)
	}
	requireKeys(t, rawKeys(t, out), "api_version", "engine_version", "core_version", "commit", "go_version", "config_revision", "deployment_id", "ready", "configured", "adopted_paths")
}

func TestVersionEngineText_PrintsTheBinaryThenTheEngine_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	e := startFakeEngine(t, configPath)
	e.attach(t)

	out, code := cliJSON(t, "version", "--engine")
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 5 || !strings.HasPrefix(lines[0], "retnd ") || !strings.HasPrefix(lines[1], "rclone ") || !strings.HasPrefix(lines[2], "go ") || !strings.HasPrefix(lines[3], "commit ") {
		t.Errorf("the four lines `version` prints are not first and unchanged:\n%s", out)
	}
	for _, want := range []string{"engine_version:", "config_revision: " + e.svc.ConfigRevision(), "deployment_id: " + e.svc.DeploymentID(), "ready: true", "configured: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("the engine block has no %q:\n%s", want, out)
		}
	}
}

func TestVersionEngine_WithNoRouteSaysSoAndPrintsNoDocument_1152(t *testing.T) {
	t.Setenv("RETND_API_URL", "")
	var out string
	var code int
	stderr := captureStderr(t, func() { out, code = cliJSON(t, "version", "--engine", "--json") })
	if code != exitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout is not empty:\n%s", out)
	}
	if !strings.Contains(stderr, "RETND_API_URL") {
		t.Errorf("the refusal does not say which variable names the engine:\n%s", stderr)
	}
}

func TestVersionJSON_IsTheEnginesObjectAndNothingElse_1152(t *testing.T) {
	var out string
	var code int
	stderr := captureStderr(t, func() { out, code = cliJSON(t, "version", "--json") })
	if code != exitUsage || strings.TrimSpace(out) != "" {
		t.Errorf("version --json: exit=%d stdout=%q, want a usage error (--json is the engine's object)", code, out)
	}
	if !strings.Contains(stderr, "--engine") {
		t.Errorf("the refusal does not point at --engine:\n%s", stderr)
	}
}

func TestVersionWithoutFlags_IsUnchanged_1152(t *testing.T) {
	out, code := cliJSON(t, "version")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != exitOK || len(lines) != 4 {
		t.Errorf("`version` printed %d lines (exit %d); it is pinned at four:\n%s", len(lines), code, out)
	}
}

// --- validators ----------------------------------------------------------

func TestValidators_ListsTheRegisteredCatalog_1152(t *testing.T) {
	out, code := cliJSON(t, "validators")
	if code != exitOK {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	for _, v := range service.RegisteredValidators() {
		if !strings.Contains(out, string(v.ID)) || !strings.Contains(out, v.Summary) {
			t.Errorf("the text has no %q / %q:\n%s", v.ID, v.Summary, out)
		}
	}

	jsonOut, code := cliJSON(t, "validators", "--json")
	if code != exitOK {
		t.Fatalf("--json code = %d", code)
	}
	var got apicontract.ListValidatorsResponse
	strictDecode(t, jsonOut, &got)
	want := service.RegisteredValidators()
	if len(got.Validators) != len(want) || len(want) == 0 {
		t.Fatalf("validators = %+v, want the catalog %+v", got.Validators, want)
	}
	for i, v := range want {
		if got.Validators[i].ID != string(v.ID) || got.Validators[i].Summary != v.Summary {
			t.Errorf("validators[%d] = %+v, want %+v", i, got.Validators[i], v)
		}
	}
}

func TestValidators_NeedsNoDeployment_1152(t *testing.T) {
	// The catalog is the binary's own, so asking for it must not open a
	// configuration that may not exist yet: it is how an operator learns which
	// id to put on `backup-set create --validator-id`.
	out, code := cliJSON(t, "validators", "--json", "--config", t.TempDir()+"/nowhere.yaml")
	if code != exitOK || !strings.Contains(out, `"validators"`) {
		t.Errorf("exit=%d out=%q", code, out)
	}
	stderr := captureStderr(t, func() { code = run([]string{"validators", "extra"}) })
	if code != exitUsage {
		t.Errorf("an operand exited %d, want %d; stderr: %s", code, exitUsage, stderr)
	}
}

// --- system capabilities / first-run -------------------------------------

func TestSystemCapabilitiesAndFirstRun_AskTheEngine_1152(t *testing.T) {
	configPath := writeTestConfig(t)
	e := startFakeEngine(t, configPath)
	e.attach(t)
	serveAlso(e, map[string]any{
		"GET /system/capabilities": apicontract.CapabilitiesResponse{Platform: "generic", NativeAuth: true, StoragePicker: true},
		"GET /system/first-run": apicontract.FirstRunStatusResponse{
			Configured: false, NextStep: "email",
			Steps:            apicontract.FirstRunSteps{Administrator: "done", Email: "pending", BackupSet: "pending"},
			ConfigDir:        "/etc/retnd/config",
			ConfigDirProblem: "the configuration directory is read-only",
		},
	})

	out, code := cliJSON(t, "system", "capabilities", "--json")
	if code != exitOK {
		t.Fatalf("capabilities --json = %d:\n%s", code, out)
	}
	var caps apicontract.CapabilitiesResponse
	strictDecode(t, out, &caps)
	if caps.Platform != "generic" || !caps.NativeAuth || !caps.StoragePicker || caps.EmbeddedWindow {
		t.Errorf("capabilities = %+v", caps)
	}
	requireKeys(t, rawKeys(t, out), "platform", "native_auth", "native_notifications", "storage_picker", "embedded_window", "app_store_packaging")

	text, code := cliJSON(t, "system", "capabilities")
	if code != exitOK || !strings.Contains(text, "platform: generic") || !strings.Contains(text, "native_auth: true") || !strings.Contains(text, "embedded_window: false") {
		t.Errorf("capabilities text: exit=%d\n%s", code, text)
	}

	out, code = cliJSON(t, "system", "first-run", "--json")
	if code != exitOK {
		t.Fatalf("first-run --json = %d:\n%s", code, out)
	}
	var fr apicontract.FirstRunStatusResponse
	strictDecode(t, out, &fr)
	if fr.Configured || fr.NextStep != "email" || fr.Steps.Administrator != "done" || fr.Steps.Email != "pending" || fr.ConfigDirProblem == "" {
		t.Errorf("first-run = %+v", fr)
	}

	text, code = cliJSON(t, "system", "first-run")
	for _, want := range []string{"configured: false", "next_step: email", "administrator: done", "email: pending", "backup_set: pending", "read-only"} {
		if !strings.Contains(text, want) {
			t.Errorf("first-run text has no %q:\n%s", want, text)
		}
	}
	if code != exitOK {
		t.Errorf("exit = %d", code)
	}
}

func TestSystem_RefusesWhatItCannotAskWithoutAnEngine_1152(t *testing.T) {
	t.Setenv("RETND_API_URL", "")
	for _, argv := range [][]string{{"system", "capabilities"}, {"system", "first-run", "--json"}} {
		var out string
		var code int
		stderr := captureStderr(t, func() { out, code = cliJSON(t, argv...) })
		if code != exitFailure || strings.TrimSpace(out) != "" || !strings.Contains(stderr, "RETND_API_URL") {
			t.Errorf("%v: exit=%d stdout=%q; want 1, nothing on stdout, and the variable named", argv, code, out)
		}
	}
	var code int
	captureStderr(t, func() { code = run([]string{"system"}) })
	if code != exitUsage {
		t.Errorf("`system` alone exited %d, want %d", code, exitUsage)
	}
	captureStderr(t, func() { code = run([]string{"system", "frobnicate"}) })
	if code != exitUsage {
		t.Errorf("an unknown subcommand exited %d, want %d", code, exitUsage)
	}
}

// --- status on a fresh install -------------------------------------------

func TestStatusFreshInstall_ShowsTheEnginesFirstRunSteps_1152(t *testing.T) {
	// The engine here serves some other, configured deployment: what matters
	// is only that it answers GET /system/first-run, as an unconfigured one
	// does, and that `status` says whose answer the steps are.
	e := startFakeEngine(t, writeTestConfig(t))
	e.attach(t)
	serveAlso(e, map[string]any{
		"GET /system/first-run": apicontract.FirstRunStatusResponse{
			NextStep: "backup_set",
			Steps:    apicontract.FirstRunSteps{Administrator: "done", Email: "skipped", BackupSet: "pending"},
		},
	})

	code, stdout, stderr := runOnAFreshInstall(t, "status")
	if code != exitOK {
		t.Fatalf("status exited %d on a fresh install:\n%s\n%s", code, stdout, stderr)
	}
	for _, want := range []string{"first-run steps", e.baseURL(), "administrator: done", "email: skipped", "backup_set: pending", "next step: backup_set"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status has no %q:\n%s", want, stdout)
		}
	}
	// And the paragraph that was there is still there.
	if !saysFirstRun.MatchString(stdout) || !namesTheNextStep.MatchString(stdout) {
		t.Errorf("the fresh-install notice changed:\n%s", stdout)
	}
}

func TestStatusFreshInstall_WithNoEngineIsExactlyWhatItWas_1152(t *testing.T) {
	t.Setenv("RETND_API_URL", "")
	code, stdout, stderr := runOnAFreshInstall(t, "status")
	if code != exitOK || strings.Contains(stdout, "first-run steps") || stderr != "" {
		t.Errorf("exit=%d; with no route the notice is unchanged and nothing is printed on stderr\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}
