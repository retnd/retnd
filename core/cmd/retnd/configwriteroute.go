package main

import (
	"context"
	"fmt"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// The engine half of workflowRetentionRoute (#1148): PATCH, PUT and DELETE
// on the eight routes whose verbs used to be refused beside a serving
// engine, plus the projections both ways.
//
// # Two directions, and why both live here
//
// wire to service is what a routed write needs, so the verb can go on
// printing through the same functions the direct path uses. service to
// wire is what --json needs: the issue's rule is that --json emits the
// object the API returns for the same call, and a service type has no
// json tags, so printing one would put Go field names on stdout that no
// API client has ever seen. Both routes print through the second, which
// is what makes the two worlds one shape.
//
// Every field crosses with its nil/non-nil meaning intact. A sparse patch
// distinguishes "leave this alone" from "clear this", and the clearing
// request (--before-dir "", --script-timeout 0) is a real operation that a
// mapping through plain values would turn into "not mentioned".

// UpdateWorkflowSettings is PATCH /settings/workflow.
func (r *engineRoute) UpdateWorkflowSettings(ctx context.Context, req service.UpdateWorkflowSettingsRequest) (service.WorkflowSettings, error) {
	timeout, err := assuranceSecondsPtr(req.ScriptTimeout, "--script-timeout")
	if err != nil {
		return service.WorkflowSettings{}, err
	}
	resp, err := r.client.UpdateWorkflowSettings(ctx, apicontract.UpdateWorkflowSettingsRequest{
		Root:                 req.Root,
		Disabled:             req.Disabled,
		BeforeDir:            req.BeforeDir,
		AfterDir:             req.AfterDir,
		ScriptTimeoutSeconds: timeout,
		MaxScriptSizeBytes:   req.MaxScriptSizeBytes,
	})
	if err != nil {
		return service.WorkflowSettings{}, err
	}
	return workflowSettingsFromWire(resp), nil
}

// UpdateBackupSetWorkflow is PATCH /backup-sets/{source}/{set}/workflow.
func (r *engineRoute) UpdateBackupSetWorkflow(ctx context.Context, id string, req service.UpdateBackupSetWorkflowRequest) (service.BackupSetWorkflow, error) {
	source, set, ok := splitBackupSetID(id)
	if !ok {
		return service.BackupSetWorkflow{}, fmt.Errorf("%q is not a backup set id; a backup set id is exactly source/name", id)
	}
	timeout, err := assuranceSecondsPtr(req.ScriptTimeout, "--script-timeout")
	if err != nil {
		return service.BackupSetWorkflow{}, err
	}
	resp, err := r.client.UpdateBackupSetWorkflow(ctx, source, set, apicontract.UpdateBackupSetWorkflowRequest{
		BeforeDir:               req.BeforeDir,
		AfterDir:                req.AfterDir,
		ScriptTimeoutSeconds:    timeout,
		RemoteExecConnectionRef: req.RemoteExecConnectionRef,
	})
	if err != nil {
		return service.BackupSetWorkflow{}, err
	}
	return backupSetWorkflowFromWire(resp), nil
}

// SetWorkflowEnv is PUT .../environment/{name} at the deployment scope
// (an empty backupSetID) or at one set's. The name is the last path
// segment and not a body field, so the body carries the source alone.
func (r *engineRoute) SetWorkflowEnv(ctx context.Context, backupSetID string, v service.WorkflowEnvVar) ([]service.WorkflowEnvVar, error) {
	body := apicontract.WorkflowEnvironmentVariableRequest{
		Secret: apicontract.WorkflowSecretReference{File: v.Secret.File, Env: v.Secret.Env, Command: v.Secret.Command},
	}
	if v.HasValue {
		value := v.Value
		body.Value = &value
	}

	var (
		resp apicontract.ListWorkflowEnvironmentResponse
		err  error
	)
	if backupSetID == "" {
		resp, err = r.client.SetWorkflowEnvironment(ctx, v.Name, body)
	} else {
		source, set, ok := splitBackupSetID(backupSetID)
		if !ok {
			return nil, fmt.Errorf("%q is not a backup set id; a backup set id is exactly source/name", backupSetID)
		}
		resp, err = r.client.SetBackupSetWorkflowEnvironment(ctx, source, set, v.Name, body)
	}
	if err != nil {
		return nil, err
	}
	return workflowEnvFromWire(resp.Variables), nil
}

// UnsetWorkflowEnv is DELETE .../environment/{name}, at either scope.
func (r *engineRoute) UnsetWorkflowEnv(ctx context.Context, backupSetID, name string) ([]service.WorkflowEnvVar, error) {
	var (
		resp apicontract.ListWorkflowEnvironmentResponse
		err  error
	)
	if backupSetID == "" {
		resp, err = r.client.UnsetWorkflowEnvironment(ctx, name)
	} else {
		source, set, ok := splitBackupSetID(backupSetID)
		if !ok {
			return nil, fmt.Errorf("%q is not a backup set id; a backup set id is exactly source/name", backupSetID)
		}
		resp, err = r.client.UnsetBackupSetWorkflowEnvironment(ctx, source, set, name)
	}
	if err != nil {
		return nil, err
	}
	return workflowEnvFromWire(resp.Variables), nil
}

// SetBackupSetRetention is PUT /backup-sets/{source}/{set}/retention: the
// body is the whole policy, never a merge.
func (r *engineRoute) SetBackupSetRetention(ctx context.Context, id string, o service.RetentionOverride) (service.BackupSetRetention, error) {
	source, set, ok := splitBackupSetID(id)
	if !ok {
		return service.BackupSetRetention{}, fmt.Errorf("%q is not a backup set id; a backup set id is exactly source/name", id)
	}
	resp, err := r.client.SetBackupSetRetention(ctx, source, set, retentionOverrideToWire(o))
	if err != nil {
		return service.BackupSetRetention{}, err
	}
	return backupSetRetentionFromWire(resp), nil
}

// ClearBackupSetRetention is DELETE /backup-sets/{source}/{set}/retention.
func (r *engineRoute) ClearBackupSetRetention(ctx context.Context, id string) (service.BackupSetRetention, error) {
	source, set, ok := splitBackupSetID(id)
	if !ok {
		return service.BackupSetRetention{}, fmt.Errorf("%q is not a backup set id; a backup set id is exactly source/name", id)
	}
	resp, err := r.client.ClearBackupSetRetention(ctx, source, set)
	if err != nil {
		return service.BackupSetRetention{}, err
	}
	return backupSetRetentionFromWire(resp), nil
}

// ---- wire to service ---------------------------------------------------

func workflowEnvFromWire(in []apicontract.WorkflowEnvironmentVariable) []service.WorkflowEnvVar {
	out := make([]service.WorkflowEnvVar, 0, len(in))
	for _, v := range in {
		out = append(out, service.WorkflowEnvVar{
			Name:     v.Name,
			Value:    v.Value,
			HasValue: v.HasValue,
			Secret:   service.WorkflowSecretRef{File: v.Secret.File, Env: v.Secret.Env, Command: v.Secret.Command},
		})
	}
	return out
}

func workflowSettingsFromWire(w apicontract.WorkflowSettingsResponse) service.WorkflowSettings {
	return service.WorkflowSettings{
		Configured:              w.Configured,
		Root:                    w.Root,
		RootDefault:             w.RootDefault,
		Disabled:                w.Disabled,
		BeforeDir:               w.BeforeDir,
		AfterDir:                w.AfterDir,
		ScriptTimeout:           time.Duration(w.ScriptTimeoutSeconds) * time.Second,
		ScriptTimeoutConfigured: w.ScriptTimeoutConfigured,
		MaxScriptSizeBytes:      w.MaxScriptSizeBytes,
		Environment:             workflowEnvFromWire(w.Environment),
		ExecConnections:         append([]string(nil), w.ExecConnections...),
		Runner:                  service.WorkflowRunnerSettings{Configured: w.Runner.Configured, Socket: w.Runner.Socket, TokenFile: w.Runner.TokenFile},
	}
}

func backupSetWorkflowFromWire(w apicontract.BackupSetWorkflowResponse) service.BackupSetWorkflow {
	out := service.BackupSetWorkflow{
		BackupSetID:              w.BackupSetID,
		Configured:               w.Configured,
		BeforeDir:                w.BeforeDir,
		AfterDir:                 w.AfterDir,
		ScriptTimeout:            time.Duration(w.ScriptTimeoutSeconds) * time.Second,
		EffectiveScriptTimeout:   time.Duration(w.EffectiveScriptTimeoutSeconds) * time.Second,
		RemoteExecConnectionRef:  w.RemoteExecConnectionRef,
		Environment:              workflowEnvFromWire(w.Environment),
		ResolvedEnvironmentNames: append([]string(nil), w.ResolvedEnvironmentNames...),
	}
	for _, s := range w.Stages {
		out.Stages = append(out.Stages, service.WorkflowStage{Scope: s.Scope, Phase: s.Phase, Dir: s.Dir})
	}
	return out
}

func retentionTiersFromWire(in []apicontract.RetentionTier) []service.RetentionTier {
	out := make([]service.RetentionTier, 0, len(in))
	for _, t := range in {
		out = append(out, service.RetentionTier{
			Name: t.Name, Granularity: t.Granularity, PeriodDays: t.PeriodDays,
			Keep: t.Keep, WindowUnit: t.WindowUnit, Medium: t.Medium,
		})
	}
	return out
}

func retentionSettingsFromWire(s apicontract.RetentionSettings) service.RetentionSettings {
	return service.RetentionSettings{
		Timezone: s.Timezone, WeekStartsOn: s.WeekStartsOn,
		ProtectLastKnownGood: s.ProtectLastKnownGood, Tiers: retentionTiersFromWire(s.Tiers),
	}
}

func backupSetRetentionFromWire(w apicontract.BackupSetRetention) service.BackupSetRetention {
	out := service.BackupSetRetention{
		BackupSetID: w.BackupSetID,
		IsOverride:  w.IsOverride,
		Effective:   retentionSettingsFromWire(w.Effective),
		Deployment:  retentionSettingsFromWire(w.Deployment),
	}
	if w.Override != nil {
		o := service.RetentionOverride{
			Timezone: w.Override.Timezone, WeekStartsOn: w.Override.WeekStartsOn,
			DailyDays: w.Override.DailyDays, WeeklyMonths: w.Override.WeeklyMonths, MonthlyMonths: w.Override.MonthlyMonths,
		}
		if w.Override.Tiers != nil {
			o.Tiers = retentionTiersFromWire(w.Override.Tiers)
		}
		if w.Override.ProtectLastKnownGood != nil {
			p := *w.Override.ProtectLastKnownGood
			o.ProtectLastKnownGood = &p
		}
		out.Override = &o
	}
	return out
}

// ---- service to wire ---------------------------------------------------

func retentionOverrideToWire(o service.RetentionOverride) apicontract.RetentionOverride {
	w := apicontract.RetentionOverride{
		Timezone: o.Timezone, WeekStartsOn: o.WeekStartsOn,
		DailyDays: o.DailyDays, WeeklyMonths: o.WeeklyMonths, MonthlyMonths: o.MonthlyMonths,
		AcknowledgeMediumDisclosure: o.AcknowledgeMediumDisclosure,
	}
	// A non-nil empty chain stays non-nil and empty: it is the difference
	// between "I did not touch the chain" and "I removed every tier", and
	// core/service refuses the second by name.
	if o.Tiers != nil {
		w.Tiers = make([]apicontract.RetentionTier, 0, len(o.Tiers))
		for _, t := range o.Tiers {
			w.Tiers = append(w.Tiers, retentionTierToWire(t))
		}
	}
	if o.ProtectLastKnownGood != nil {
		p := *o.ProtectLastKnownGood
		w.ProtectLastKnownGood = &p
	}
	return w
}

func retentionSettingsToWire(s service.RetentionSettings) apicontract.RetentionSettings {
	tiers := make([]apicontract.RetentionTier, 0, len(s.Tiers))
	for _, t := range s.Tiers {
		tiers = append(tiers, retentionTierToWire(t))
	}
	return apicontract.RetentionSettings{
		Timezone: s.Timezone, WeekStartsOn: s.WeekStartsOn,
		ProtectLastKnownGood: s.ProtectLastKnownGood, Tiers: tiers,
	}
}

func backupSetRetentionToWire(r service.BackupSetRetention) apicontract.BackupSetRetention {
	out := apicontract.BackupSetRetention{
		BackupSetID: r.BackupSetID,
		IsOverride:  r.IsOverride,
		Effective:   retentionSettingsToWire(r.Effective),
		Deployment:  retentionSettingsToWire(r.Deployment),
	}
	if r.Override != nil {
		// The consent is request-only: the response's override never
		// carries it, exactly as the handler's projection leaves it.
		o := retentionOverrideToWire(*r.Override)
		o.AcknowledgeMediumDisclosure = false
		out.Override = &o
	}
	return out
}

func workflowEnvToWire(in []service.WorkflowEnvVar) []apicontract.WorkflowEnvironmentVariable {
	out := make([]apicontract.WorkflowEnvironmentVariable, 0, len(in))
	for _, v := range in {
		// Copied field by field, as the handler does, so a field added to
		// the service type cannot reach stdout unreviewed. A resolved
		// secret has no field here to arrive in.
		out = append(out, apicontract.WorkflowEnvironmentVariable{
			Name: v.Name, Value: v.Value, HasValue: v.HasValue,
			Secret: apicontract.WorkflowSecretReference{File: v.Secret.File, Env: v.Secret.Env, Command: v.Secret.Command},
		})
	}
	return out
}

func workflowEnvListToWire(backupSetID string, vars []service.WorkflowEnvVar) apicontract.ListWorkflowEnvironmentResponse {
	return apicontract.ListWorkflowEnvironmentResponse{BackupSetID: backupSetID, Variables: workflowEnvToWire(vars)}
}

func workflowSettingsToWire(s service.WorkflowSettings) apicontract.WorkflowSettingsResponse {
	return apicontract.WorkflowSettingsResponse{
		Configured:              s.Configured,
		Root:                    s.Root,
		RootDefault:             s.RootDefault,
		Disabled:                s.Disabled,
		BeforeDir:               s.BeforeDir,
		AfterDir:                s.AfterDir,
		ScriptTimeoutSeconds:    int64(s.ScriptTimeout / time.Second),
		ScriptTimeoutConfigured: s.ScriptTimeoutConfigured,
		MaxScriptSizeBytes:      s.MaxScriptSizeBytes,
		Environment:             workflowEnvToWire(s.Environment),
		ExecConnections:         append([]string{}, s.ExecConnections...),
		Runner:                  apicontract.WorkflowRunnerSettings{Configured: s.Runner.Configured, Socket: s.Runner.Socket, TokenFile: s.Runner.TokenFile},
	}
}

func backupSetWorkflowToWire(s service.BackupSetWorkflow) apicontract.BackupSetWorkflowResponse {
	stages := make([]apicontract.WorkflowStage, 0, len(s.Stages))
	for _, st := range s.Stages {
		stages = append(stages, apicontract.WorkflowStage{Scope: st.Scope, Phase: st.Phase, Dir: st.Dir})
	}
	return apicontract.BackupSetWorkflowResponse{
		BackupSetID:                   s.BackupSetID,
		Configured:                    s.Configured,
		BeforeDir:                     s.BeforeDir,
		AfterDir:                      s.AfterDir,
		ScriptTimeoutSeconds:          int64(s.ScriptTimeout / time.Second),
		EffectiveScriptTimeoutSeconds: int64(s.EffectiveScriptTimeout / time.Second),
		RemoteExecConnectionRef:       s.RemoteExecConnectionRef,
		Environment:                   workflowEnvToWire(s.Environment),
		ResolvedEnvironmentNames:      append([]string{}, s.ResolvedEnvironmentNames...),
		Stages:                        stages,
	}
}
