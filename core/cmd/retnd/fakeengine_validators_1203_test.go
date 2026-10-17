package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1203's three routed writes, served by the fake engine off the real
// BackupService behind it, the way #1148's eight are. The step-up header
// each request carried is kept, because the CLI answering the engine's
// password check is part of what a routed upload is.

var stepUpSeen sync.Map // "METHOD path" -> the header value

func (e *fakeEngine) serveCustomValidator(w http.ResponseWriter, r *http.Request, path string) bool {
	name, isOne := strings.CutPrefix(path, "/assurance/validators/")
	switch {
	case path == "/assurance/validators" && r.Method == http.MethodPost:
		stepUpSeen.Store(r.Method+" "+path, r.Header.Get("X-Retnd-Step-Up-Password"))
		var body apicontract.CreateCustomValidatorRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return true
		}
		v, err := e.svc.CreateCustomValidator(r.Context(), service.CreateCustomValidatorRequest{Name: body.Name, Pattern: body.Pattern, BackupSetID: body.BackupSetID, Script: body.Script})
		if err != nil {
			refuseServiceError(w, "createCustomValidator", err)
			return true
		}
		writeJSON(w, http.StatusCreated, service.CustomValidatorWire(v))
		return true
	case isOne && r.Method == http.MethodPut:
		stepUpSeen.Store(r.Method+" "+path, r.Header.Get("X-Retnd-Step-Up-Password"))
		var body apicontract.UpdateCustomValidatorRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return true
		}
		v, err := e.svc.UpdateCustomValidator(r.Context(), service.CreateCustomValidatorRequest{Name: name, Pattern: body.Pattern, BackupSetID: body.BackupSetID, Script: body.Script, ScriptSHA256: body.ScriptSha256})
		if err != nil {
			refuseServiceError(w, "updateCustomValidator", err)
			return true
		}
		writeJSON(w, http.StatusOK, service.CustomValidatorWire(v))
		return true
	case path == "/assurance/validators" && r.Method == http.MethodGet:
		q := r.URL.Query()
		stepUpSeen.Store("GET "+path, r.URL.RawQuery)
		l, err := e.svc.ListCustomValidators(r.Context(), service.CustomValidatorsFilter{BackupSetID: q.Get("backup_set"), ArtifactID: q.Get("artifact")})
		if err != nil {
			refuseServiceError(w, "listCustomValidators", err)
			return true
		}
		writeJSON(w, http.StatusOK, service.CustomValidatorsWire(l))
		return true
	case isOne && r.Method == http.MethodGet && strings.HasSuffix(name, "/script"):
		stepUpSeen.Store("GET "+path, "read")
		s, err := e.svc.CustomValidatorScript(r.Context(), strings.TrimSuffix(name, "/script"))
		if err != nil {
			refuseServiceError(w, "getCustomValidatorScript", err)
			return true
		}
		writeJSON(w, http.StatusOK, service.ValidatorScriptWire(s))
		return true
	case path == "/assurance/validator-patterns" && r.Method == http.MethodGet:
		stepUpSeen.Store("GET "+path, "read")
		p, err := e.svc.CustomValidatorPatterns(r.Context(), r.URL.Query().Get("backup_set"))
		if err != nil {
			refuseServiceError(w, "listCustomValidatorPatterns", err)
			return true
		}
		writeJSON(w, http.StatusOK, service.CustomValidatorPatternsWire(p))
		return true
	case path == "/assurance/validator-preview" && r.Method == http.MethodPost:
		stepUpSeen.Store("POST "+path, "read")
		var body apicontract.PreviewCustomValidatorRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return true
		}
		p, err := e.svc.PreviewCustomValidator(r.Context(), body.Pattern, body.BackupSetID, body.Replacing)
		if err != nil {
			refuseServiceError(w, "previewCustomValidator", err)
			return true
		}
		writeJSON(w, http.StatusOK, service.CustomValidatorPreviewWire(p))
		return true
	case isOne && r.Method == http.MethodDelete:
		stepUpSeen.Store(r.Method+" "+path, r.Header.Get("X-Retnd-Step-Up-Password"))
		v, err := e.svc.RemoveCustomValidator(r.Context(), name)
		if err != nil {
			refuseServiceError(w, "removeCustomValidator", err)
			return true
		}
		writeJSON(w, http.StatusOK, service.CustomValidatorWire(v))
		return true
	}
	return false
}

// routedValidatorFixture is routedFixture with a workflow root discovery's
// custody rules accept on the engine's side: a script is written there, and a
// root under a world-writable /tmp (what t.TempDir gives on Linux) is refused.
func routedValidatorFixture(t *testing.T) (cliConfig string, engine *fakeEngine) {
	t.Helper()
	cliConfig, _ = workflowFixture(t)
	engineConfig := writeTestConfigFor(t, cliConfig)
	raw, err := os.ReadFile(engineConfig)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	block := "workflows:\n  root: " + validatorWorkflowRoot(t) + "\n"
	if err := os.WriteFile(engineConfig, append(raw, []byte(block)...), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	engine = startFakeEngine(t, engineConfig)
	engine.attach(t)
	return cliConfig, engine
}

func TestAssuranceValidator_RoutedWritesLandThroughTheEngineWithTheStepUp(t *testing.T) {
	cliConfig, engine := routedValidatorFixture(t)
	script := writeScriptFixture(t, goodCLIValidator)
	beforeCLI := readFile(t, cliConfig)

	for _, argv := range [][]string{
		{"assurance", "validators", "add", "routed", "--pattern", "^R-", "--script", script},
		{"assurance", "validators", "update", "routed", "--pattern", "^S-", "--backup-set", cliSet},
	} {
		var code int
		stderr := captureStderr(t, func() {
			code = captureStdoutCode(t, func() int { return run(withConfig(argv, cliConfig)) })
		})
		if code != exitOK {
			t.Fatalf("%v beside a serving engine exited %d\nstderr: %s", argv, code, stderr)
		}
	}
	vs := engine.svc.ListCustomValidators
	got, err := vs(t.Context(), service.CustomValidatorsFilter{})
	if err != nil || len(got.Validators) != 1 || got.Validators[0].Pattern != "^S-" || got.Validators[0].BackupSetID != cliSet {
		t.Fatalf("the engine holds %+v, %v", got.Validators, err)
	}
	for _, key := range []string{"POST /assurance/validators", "PUT /assurance/validators/routed"} {
		if v, _ := stepUpSeen.Load(key); v == nil || v.(string) == "" {
			t.Errorf("%s did not carry the step-up password", key)
		}
	}
	if readFile(t, cliConfig) != beforeCLI {
		t.Error("the CLI's own config.yaml changed as well as the engine's")
	}

	if code := captureStdoutCode(t, func() int { return run(withConfig([]string{"assurance", "validators", "remove", "routed"}, cliConfig)) }); code != exitOK {
		t.Fatalf("routed remove exited %d", code)
	}
	if v, _ := stepUpSeen.Load("DELETE /assurance/validators/routed"); v != nil && v.(string) != "" {
		t.Error("a remove carried the password, which only the upload and the edit ask for")
	}
	engineRoot := engine.svc.ListCustomValidators
	after, _ := engineRoot(t.Context(), service.CustomValidatorsFilter{})
	if len(after.Validators) != 0 {
		t.Errorf("the engine still holds %+v", after.Validators)
	}
	if _, err := os.Stat(filepath.Join(after.WorkflowRoot, "recovery-test-validators", "routed.local.sh")); !os.IsNotExist(err) {
		t.Errorf("the engine's script survived: %v", err)
	}
}
