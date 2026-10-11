package webhost

import (
	"context"

	"github.com/retnd/retnd/core/service"
)

// The BackupServiceClient doubles for EPIC Q's assurance surface (Q1.4,
// #982). The route-level behaviour is proved against the REAL service in
// assurance_route_test.go; these exist so the two fake backends keep
// satisfying the interface and the contract tests can drive the handlers.

func (f *syncFakeBackend) AssuranceSettings(context.Context) (service.AssuranceSettings, error) {
	return service.AssuranceSettings{}, nil
}

func (f *syncFakeBackend) UpdateAssuranceSettings(context.Context, service.UpdateAssuranceSettingsRequest) (service.AssuranceSettings, error) {
	return service.AssuranceSettings{}, nil
}

func (f *syncFakeBackend) BackupSetAssurance(_ context.Context, id string) (service.BackupSetAssurance, error) {
	return service.BackupSetAssurance{BackupSetID: id}, nil
}

func (f *syncFakeBackend) UpdateBackupSetAssurance(_ context.Context, id string, _ service.UpdateBackupSetAssuranceRequest) (service.BackupSetAssurance, error) {
	return service.BackupSetAssurance{BackupSetID: id}, nil
}

func (f *syncFakeBackend) ResetBackupSetAssurance(_ context.Context, id string) (service.BackupSetAssurance, error) {
	return service.BackupSetAssurance{BackupSetID: id}, nil
}

func (f *syncFakeBackend) AssuranceDue(context.Context, service.AssuranceDueFilter) ([]service.AssuranceDueItem, error) {
	return nil, nil
}

func (f *asyncFakeBackend) AssuranceSettings(context.Context) (service.AssuranceSettings, error) {
	return service.AssuranceSettings{}, nil
}

func (f *asyncFakeBackend) UpdateAssuranceSettings(context.Context, service.UpdateAssuranceSettingsRequest) (service.AssuranceSettings, error) {
	return service.AssuranceSettings{}, nil
}

func (f *asyncFakeBackend) BackupSetAssurance(_ context.Context, id string) (service.BackupSetAssurance, error) {
	return service.BackupSetAssurance{BackupSetID: id}, nil
}

func (f *asyncFakeBackend) UpdateBackupSetAssurance(_ context.Context, id string, _ service.UpdateBackupSetAssuranceRequest) (service.BackupSetAssurance, error) {
	return service.BackupSetAssurance{BackupSetID: id}, nil
}

func (f *asyncFakeBackend) ResetBackupSetAssurance(_ context.Context, id string) (service.BackupSetAssurance, error) {
	return service.BackupSetAssurance{BackupSetID: id}, nil
}

func (f *asyncFakeBackend) AssuranceDue(context.Context, service.AssuranceDueFilter) ([]service.AssuranceDueItem, error) {
	return nil, nil
}

func (f *syncFakeBackend) StartRecoveryTest(_ context.Context, id string, _ service.RecoveryTestOptions) (service.RecoveryTestStarted, error) {
	return service.RecoveryTestStarted{BackupSetID: id, OperationID: "op"}, nil
}

func (f *asyncFakeBackend) StartRecoveryTest(_ context.Context, id string, _ service.RecoveryTestOptions) (service.RecoveryTestStarted, error) {
	return service.RecoveryTestStarted{BackupSetID: id, OperationID: "op"}, nil
}
