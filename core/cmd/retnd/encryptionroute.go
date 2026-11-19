package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// encryptionRoute is the encryption verbs' half of configWriteRoute (#993):
// every write, and the key-provider test, in the shape core/service's own
// methods have, so *service.BackupService satisfies it when nothing serves
// the deployment and engineRoute satisfies it through the API when
// something does. It is wider than "config writes" because the key ops
// change the journal and the key directory of the process that holds them.
type encryptionRoute interface {
	PatchEncryptionPolicy(ctx context.Context, req apicontract.EncryptionPolicyPatch) (apicontract.EncryptionPolicy, error)
	ResetEncryptionPolicy(ctx context.Context) (apicontract.EncryptionPolicy, error)
	SetBackupSetEncryption(ctx context.Context, id string, o apicontract.EncryptionOverride) (apicontract.EncryptionSetPolicy, error)
	ClearBackupSetEncryption(ctx context.Context, id string) (apicontract.EncryptionSetPolicy, error)
	CreateEncryptionKey(ctx context.Context, suite string) (apicontract.EncryptionKeyStatus, error)
	RotateEncryptionKey(ctx context.Context, suite string) (apicontract.EncryptionKeyStatus, error)
	RetireEncryptionKey(ctx context.Context, id string) (apicontract.EncryptionKeyStatus, error)
	ExportRecoveryBundleBytes(ctx context.Context, id string) ([]byte, error)
	ConfirmRecoveryBundle(ctx context.Context, id, digest string) (apicontract.EncryptionKeyStatus, error)
	TestEncryptionProvider(ctx context.Context, req apicontract.EncryptionProviderTestRequest) (apicontract.EncryptionProviderProbe, error)
	StartDecryptVerify(ctx context.Context, req apicontract.StartDecryptVerifyRequest) (service.ScrubStarted, error)
	StartEncryptExisting(ctx context.Context, req apicontract.StartEncryptExistingRequest) (service.ScrubStarted, error)
	StartReencrypt(ctx context.Context, req apicontract.StartReencryptRequest) (service.ScrubStarted, error)
}

func (r *engineRoute) PatchEncryptionPolicy(ctx context.Context, req apicontract.EncryptionPolicyPatch) (apicontract.EncryptionPolicy, error) {
	return r.client.PatchEncryptionPolicy(ctx, req)
}

func (r *engineRoute) ResetEncryptionPolicy(ctx context.Context) (apicontract.EncryptionPolicy, error) {
	return r.client.ResetEncryptionPolicy(ctx)
}

func (r *engineRoute) SetBackupSetEncryption(ctx context.Context, id string, o apicontract.EncryptionOverride) (apicontract.EncryptionSetPolicy, error) {
	source, set, ok := splitBackupSetID(id)
	if !ok {
		return apicontract.EncryptionSetPolicy{}, fmt.Errorf("%q is not a backup set id; a backup set id is exactly source/name", id)
	}
	return r.client.SetBackupSetEncryption(ctx, source, set, o)
}

func (r *engineRoute) ClearBackupSetEncryption(ctx context.Context, id string) (apicontract.EncryptionSetPolicy, error) {
	source, set, ok := splitBackupSetID(id)
	if !ok {
		return apicontract.EncryptionSetPolicy{}, fmt.Errorf("%q is not a backup set id; a backup set id is exactly source/name", id)
	}
	return r.client.ClearBackupSetEncryption(ctx, source, set)
}

func (r *engineRoute) CreateEncryptionKey(ctx context.Context, suite string) (apicontract.EncryptionKeyStatus, error) {
	return r.client.CreateEncryptionKey(ctx, suite)
}

func (r *engineRoute) RotateEncryptionKey(ctx context.Context, suite string) (apicontract.EncryptionKeyStatus, error) {
	return r.client.RotateEncryptionKey(ctx, suite)
}

func (r *engineRoute) RetireEncryptionKey(ctx context.Context, id string) (apicontract.EncryptionKeyStatus, error) {
	return r.client.RetireEncryptionKey(ctx, id)
}

// ExportRecoveryBundleBytes re-encodes the bundle the API answered with. It
// is the caller's to write to a file and zero; nothing here keeps a copy.
func (r *engineRoute) ExportRecoveryBundleBytes(ctx context.Context, id string) ([]byte, error) {
	b, err := r.client.ExportRecoveryBundle(ctx, id)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("encoding the bundle: %w", err)
	}
	return append(raw, '\n'), nil
}

func (r *engineRoute) ConfirmRecoveryBundle(ctx context.Context, id, digest string) (apicontract.EncryptionKeyStatus, error) {
	return r.client.ConfirmRecoveryBundle(ctx, id, apicontract.ConfirmRecoveryBundleRequest{Digest: digest})
}

func (r *engineRoute) TestEncryptionProvider(ctx context.Context, req apicontract.EncryptionProviderTestRequest) (apicontract.EncryptionProviderProbe, error) {
	return r.client.TestEncryptionProvider(ctx, req)
}

func (r *engineRoute) StartDecryptVerify(ctx context.Context, req apicontract.StartDecryptVerifyRequest) (service.ScrubStarted, error) {
	got, err := r.client.StartDecryptVerify(ctx, req)
	if err != nil {
		return service.ScrubStarted{}, err
	}
	return service.ScrubStarted{BackupSetID: got.BackupSetID, OperationID: got.OperationID}, nil
}

func (r *engineRoute) StartEncryptExisting(ctx context.Context, req apicontract.StartEncryptExistingRequest) (service.ScrubStarted, error) {
	got, err := r.client.StartEncryptExisting(ctx, req)
	if err != nil {
		return service.ScrubStarted{}, err
	}
	return service.ScrubStarted{BackupSetID: got.BackupSetID, OperationID: got.OperationID}, nil
}

func (r *engineRoute) StartReencrypt(ctx context.Context, req apicontract.StartReencryptRequest) (service.ScrubStarted, error) {
	got, err := r.client.StartReencrypt(ctx, req)
	if err != nil {
		return service.ScrubStarted{}, err
	}
	return service.ScrubStarted{BackupSetID: got.BackupSetID, OperationID: got.OperationID}, nil
}
