package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// The eight configuration writes #1148 routes, served by the fake engine
// off the real BackupService behind it (fakeengine_test.go says why).
//
// This file is the one place the fake engine learns these paths, and
// serveConfigWrite is the single hook fakeengine_test.go's api() calls
// ahead of its generic /backup-sets/ cases: a workflow patch ends in
// /workflow and an env unset or a retention clear is a DELETE under
// /backup-sets/, so they would otherwise be read as a set edit or a
// removal.
//
// The projections to the wire are mirrored from apps/common/webhost
// (core may not import apps), and every answer's bytes are kept in
// e.lastBody so a test can diff what the CLI printed against what the
// engine actually returned for the same call.

// serveConfigWrite reports whether it answered the request.
func (e *fakeEngine) serveConfigWrite(w http.ResponseWriter, r *http.Request, path string) bool {
	rec := &recordingWriter{ResponseWriter: w}
	handled := e.dispatchConfigWrite(rec, r, path)
	if handled {
		e.mu.Lock()
		if e.lastBody == nil {
			e.lastBody = map[string][]byte{}
		}
		e.lastBody[r.Method+" "+path] = append([]byte(nil), rec.buf.Bytes()...)
		e.mu.Unlock()
	}
	return handled
}

type recordingWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (r *recordingWriter) Write(p []byte) (int, error) {
	r.buf.Write(p)
	return r.ResponseWriter.Write(p)
}

// answered is the body this engine last returned for "METHOD path".
func (e *fakeEngine) answered(key string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastBody[key]
}

func (e *fakeEngine) dispatchConfigWrite(w http.ResponseWriter, r *http.Request, path string) bool {
	const ws = "/settings/workflow"
	switch {
	case path == ws && r.Method == http.MethodPatch:
		e.fakePatchWorkflowSettings(w, r)
		return true
	case strings.HasPrefix(path, ws+"/environment/") && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		e.fakeWorkflowEnv(w, r, "", strings.TrimPrefix(path, ws+"/environment/"))
		return true
	}

	rest, ok := strings.CutPrefix(path, "/backup-sets/")
	if !ok {
		return false
	}
	if id, ok := strings.CutSuffix(rest, "/workflow"); ok && r.Method == http.MethodPatch {
		e.fakePatchBackupSetWorkflow(w, r, id)
		return true
	}
	if i := strings.Index(rest, "/workflow/environment/"); i >= 0 && (r.Method == http.MethodPut || r.Method == http.MethodDelete) {
		e.fakeWorkflowEnv(w, r, rest[:i], rest[i+len("/workflow/environment/"):])
		return true
	}
	if id, ok := strings.CutSuffix(rest, "/retention"); ok && (r.Method == http.MethodPut || r.Method == http.MethodDelete) {
		e.fakeRetention(w, r, id)
		return true
	}
	return false
}

func secondsPtr(v *int64) *time.Duration {
	if v == nil {
		return nil
	}
	d := time.Duration(*v) * time.Second
	return &d
}

func (e *fakeEngine) fakePatchWorkflowSettings(w http.ResponseWriter, r *http.Request) {
	var body apicontract.UpdateWorkflowSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}
	got, err := e.svc.UpdateWorkflowSettings(r.Context(), service.UpdateWorkflowSettingsRequest{
		Root: body.Root, BeforeDir: body.BeforeDir, AfterDir: body.AfterDir,
		ScriptTimeout: secondsPtr(body.ScriptTimeoutSeconds), MaxScriptSizeBytes: body.MaxScriptSizeBytes,
	})
	if err != nil {
		refuseServiceError(w, "updateWorkflowSettings", err)
		return
	}
	writeJSON(w, http.StatusOK, fakeWorkflowSettingsBody(got))
}

func (e *fakeEngine) fakePatchBackupSetWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	var body apicontract.UpdateBackupSetWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}
	got, err := e.svc.UpdateBackupSetWorkflow(r.Context(), id, service.UpdateBackupSetWorkflowRequest{
		BeforeDir: body.BeforeDir, AfterDir: body.AfterDir,
		ScriptTimeout: secondsPtr(body.ScriptTimeoutSeconds), RemoteExecConnectionRef: body.RemoteExecConnectionRef,
	})
	if err != nil {
		refuseServiceError(w, "updateBackupSetWorkflow", err)
		return
	}
	writeJSON(w, http.StatusOK, fakeBackupSetWorkflowBody(got))
}

func (e *fakeEngine) fakeWorkflowEnv(w http.ResponseWriter, r *http.Request, setID, name string) {
	var (
		vars []service.WorkflowEnvVar
		err  error
	)
	if r.Method == http.MethodDelete {
		vars, err = e.svc.UnsetWorkflowEnv(r.Context(), setID, name)
	} else {
		var body apicontract.WorkflowEnvironmentVariableRequest
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return
		}
		v := service.WorkflowEnvVar{Name: name, Secret: service.WorkflowSecretRef{
			File: body.Secret.File, Env: body.Secret.Env, Command: body.Secret.Command,
		}}
		if body.Value != nil {
			v.Value, v.HasValue = *body.Value, true
		}
		vars, err = e.svc.SetWorkflowEnv(r.Context(), setID, v)
	}
	if err != nil {
		refuseServiceError(w, "workflowEnvironment", err)
		return
	}
	writeJSON(w, http.StatusOK, envListOf(setID, vars))
}

func (e *fakeEngine) fakeRetention(w http.ResponseWriter, r *http.Request, id string) {
	var (
		got service.BackupSetRetention
		err error
	)
	if r.Method == http.MethodDelete {
		got, err = e.svc.ClearBackupSetRetention(r.Context(), id)
	} else {
		var body apicontract.RetentionOverride
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
			return
		}
		o := service.RetentionOverride{
			Timezone: body.Timezone, WeekStartsOn: body.WeekStartsOn,
			DailyDays: body.DailyDays, WeeklyMonths: body.WeeklyMonths, MonthlyMonths: body.MonthlyMonths,
			ProtectLastKnownGood: body.ProtectLastKnownGood, AcknowledgeMediumDisclosure: body.AcknowledgeMediumDisclosure,
		}
		if body.Tiers != nil {
			o.Tiers = []service.RetentionTier{}
			for _, t := range body.Tiers {
				o.Tiers = append(o.Tiers, service.RetentionTier{Name: t.Name, Granularity: t.Granularity,
					PeriodDays: t.PeriodDays, Keep: t.Keep, WindowUnit: t.WindowUnit, Medium: t.Medium})
			}
		}
		got, err = e.svc.SetBackupSetRetention(r.Context(), id, o)
	}
	if err != nil {
		refuseServiceError(w, "backupSetRetention", err)
		return
	}
	writeJSON(w, http.StatusOK, fakeRetentionBody(got))
}

func fakeTierBodies(in []service.RetentionTier) []apicontract.RetentionTier {
	out := make([]apicontract.RetentionTier, 0, len(in))
	for _, t := range in {
		out = append(out, apicontract.RetentionTier{Name: t.Name, Granularity: t.Granularity,
			PeriodDays: t.PeriodDays, Keep: t.Keep, WindowUnit: t.WindowUnit, Medium: t.Medium})
	}
	return out
}

func fakeRetentionSettingsBody(s service.RetentionSettings) apicontract.RetentionSettings {
	return apicontract.RetentionSettings{
		Timezone: s.Timezone, WeekStartsOn: s.WeekStartsOn,
		ProtectLastKnownGood: s.ProtectLastKnownGood, Tiers: fakeTierBodies(s.Tiers),
	}
}

func fakeEnvVarBody(v service.WorkflowEnvVar) apicontract.WorkflowEnvironmentVariable {
	return apicontract.WorkflowEnvironmentVariable{
		Name: v.Name, Value: v.Value, HasValue: v.HasValue,
		Secret: apicontract.WorkflowSecretReference{File: v.Secret.File, Env: v.Secret.Env, Command: v.Secret.Command},
	}
}

func fakeWorkflowSettingsBody(s service.WorkflowSettings) apicontract.WorkflowSettingsResponse {
	out := apicontract.WorkflowSettingsResponse{
		Configured: s.Configured, Root: s.Root, BeforeDir: s.BeforeDir, AfterDir: s.AfterDir,
		ScriptTimeoutSeconds: int64(s.ScriptTimeout / time.Second), ScriptTimeoutConfigured: s.ScriptTimeoutConfigured,
		MaxScriptSizeBytes: s.MaxScriptSizeBytes,
		Environment:        []apicontract.WorkflowEnvironmentVariable{},
		ExecConnections:    append([]string{}, s.ExecConnections...),
		Runner:             apicontract.WorkflowRunnerSettings{Configured: s.Runner.Configured, Socket: s.Runner.Socket, TokenFile: s.Runner.TokenFile},
	}
	for _, v := range s.Environment {
		out.Environment = append(out.Environment, fakeEnvVarBody(v))
	}
	return out
}

func fakeBackupSetWorkflowBody(s service.BackupSetWorkflow) apicontract.BackupSetWorkflowResponse {
	out := apicontract.BackupSetWorkflowResponse{
		BackupSetID: s.BackupSetID, Configured: s.Configured, BeforeDir: s.BeforeDir, AfterDir: s.AfterDir,
		ScriptTimeoutSeconds:          int64(s.ScriptTimeout / time.Second),
		EffectiveScriptTimeoutSeconds: int64(s.EffectiveScriptTimeout / time.Second),
		RemoteExecConnectionRef:       s.RemoteExecConnectionRef,
		Environment:                   []apicontract.WorkflowEnvironmentVariable{},
		ResolvedEnvironmentNames:      append([]string{}, s.ResolvedEnvironmentNames...),
		Stages:                        []apicontract.WorkflowStage{},
	}
	for _, v := range s.Environment {
		out.Environment = append(out.Environment, fakeEnvVarBody(v))
	}
	for _, st := range s.Stages {
		out.Stages = append(out.Stages, apicontract.WorkflowStage{Scope: st.Scope, Phase: st.Phase, Dir: st.Dir})
	}
	return out
}

func envListOf(setID string, vars []service.WorkflowEnvVar) apicontract.ListWorkflowEnvironmentResponse {
	out := apicontract.ListWorkflowEnvironmentResponse{BackupSetID: setID, Variables: []apicontract.WorkflowEnvironmentVariable{}}
	for _, v := range vars {
		out.Variables = append(out.Variables, fakeEnvVarBody(v))
	}
	return out
}

func fakeRetentionBody(got service.BackupSetRetention) apicontract.BackupSetRetention {
	out := apicontract.BackupSetRetention{
		BackupSetID: got.BackupSetID, IsOverride: got.IsOverride,
		Effective: fakeRetentionSettingsBody(got.Effective), Deployment: fakeRetentionSettingsBody(got.Deployment),
	}
	if got.Override != nil {
		o := apicontract.RetentionOverride{
			Timezone: got.Override.Timezone, WeekStartsOn: got.Override.WeekStartsOn,
			DailyDays: got.Override.DailyDays, WeeklyMonths: got.Override.WeeklyMonths, MonthlyMonths: got.Override.MonthlyMonths,
			ProtectLastKnownGood: got.Override.ProtectLastKnownGood,
		}
		if got.Override.Tiers != nil {
			o.Tiers = fakeTierBodies(got.Override.Tiers)
		}
		out.Override = &o
	}
	return out
}
