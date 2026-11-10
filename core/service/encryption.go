package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/secretref"
	"github.com/retnd/retnd/core/internal/state"
)

// Retained-copy encryption, the operator surface (#993, FR-59, FR-63, FR-64,
// FR-66). Every CLI verb under `retnd encryption` and every
// /api/v1/encryption route lands on one of the methods here, so a refusal
// has one author and the two surfaces cannot word it differently.
//
// # Shapes
//
// The methods take and return core/apicontract's own types. That is not
// laziness: the CLI's --json prints the same object the API returns, and a
// second Go shape between them is where a field would be renamed on one side
// and not the other.
//
// # The write-only rule
//
// A key reference goes IN as {file | env | command} and comes OUT as a
// description only (secretref.Ref.String, which renders a command as its
// executable without arguments). The reference type has no UnmarshalJSON on
// purpose, so a value read back from a response is a sentence, and the one
// place a reference can be declared stays the request body and config.yaml.
// Nothing here returns key material except ExportRecoveryBundle, which is the
// one explicit carrier the key store defines (keycustody.Bundle).
//
// # What is deliberately not here
//
// No progress stage for encrypting or decrypt-verifying: the stage list is a
// published contract and adding to it is an owner decision. Decrypt-verify
// rides the existing scrub operation (kind scrub, same cancel route), whose
// byte counter is bytes_read.

var (
	// ErrEncryptionKeyUnresolvable refuses a decrypt-verify whose recorded
	// key cannot be resolved right now, before any byte is read from a
	// medium that may bill for egress.
	ErrEncryptionKeyUnresolvable = errors.New("service: the key this copy was encrypted under cannot be resolved")

	// ErrEncryptionRetireUnavailable is the retirement refusal for a journal
	// that does not yet carry the retirement transaction (state.Journal's
	// ActivePlacementsRecordingKey and RetireEncryptionKey).
	ErrEncryptionRetireUnavailable = errors.New("service: this build's journal cannot retire a key yet")
)

var (
	// ErrEncryptionKeyNotFound, ErrEncryptionKeyInUse and ErrEncryptionKeyState
	// classify the key store's and placement's refusals for the API without
	// changing their words: a refusal that carries one of these prints as the
	// underlying error, whole, on the terminal and in the route's body.
	ErrEncryptionKeyNotFound = errors.New("service: no managed key with this id")
	ErrEncryptionKeyInUse    = errors.New("service: the key is still needed")
	ErrEncryptionKeyState    = errors.New("service: the key is not in a state that allows this")
)

// refusal is err under a class, printing as err.
type refusal struct{ kind, err error }

func (r *refusal) Error() string        { return r.err.Error() }
func (r *refusal) Unwrap() error        { return r.err }
func (r *refusal) Is(target error) bool { return target == r.kind }

func classifyEncryption(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keycustody.ErrUnknownKey):
		return &refusal{ErrEncryptionKeyNotFound, err}
	case errors.Is(err, placement.ErrKeyInUse), errors.Is(err, placement.ErrKeyStillActive):
		return &refusal{ErrEncryptionKeyInUse, err}
	case errors.Is(err, keycustody.ErrActiveKeyExists), errors.Is(err, keycustody.ErrNoActiveKey), errors.Is(err, keycustody.ErrNoSuchExport):
		return &refusal{ErrEncryptionKeyState, err}
	}
	return err
}

// configProvider turns a request's provider into the schema's.
func configProvider(p *apicontract.EncryptionKeyProvider) *config.RetainedCopyKeyProvider {
	if p == nil {
		return nil
	}
	out := &config.RetainedCopyKeyProvider{Managed: p.Managed}
	if p.External != nil {
		out.External = &config.SecretSource{File: p.External.File, Env: p.External.Env, Command: append([]string(nil), p.External.Command...)}
	}
	return out
}

// providerState is a provider as it is read back: its kind and its source
// description, nothing a reference could be rebuilt from.
func providerState(p config.RetainedCopyKeyProvider) apicontract.EncryptionKeyProviderState {
	switch p.Kind() {
	case config.KeyProviderManaged:
		return apicontract.EncryptionKeyProviderState{Kind: config.KeyProviderManaged}
	case config.KeyProviderExternal:
		return apicontract.EncryptionKeyProviderState{Kind: config.KeyProviderExternal, Source: p.ExternalRef().String()}
	default:
		return apicontract.EncryptionKeyProviderState{Kind: "none"}
	}
}

func setPolicyOf(sourceName string, bs config.BackupSet) apicontract.EncryptionSetPolicy {
	out := apicontract.EncryptionSetPolicy{
		BackupSetID:          sourceName + "/" + bs.Name,
		Mode:                 config.RetainedCopyEncryptionInherit,
		EffectiveMode:        bs.RetainedCopyEncryption.Mode,
		IsOverride:           bs.RetainedCopyEncryptionIsOverride(),
		KeyProvider:          providerState(config.RetainedCopyKeyProvider{}),
		EffectiveKeyProvider: providerState(bs.RetainedCopyEncryption.KeyProvider),
	}
	if o := bs.RetainedCopyEncryptionConfig; o != nil {
		if o.Mode != "" {
			out.Mode = o.Mode
		}
		if o.KeyProvider != nil {
			out.KeyProvider = providerState(*o.KeyProvider)
		}
	}
	return out
}

func policyOf(cfg *config.Config) apicontract.EncryptionPolicy {
	out := apicontract.EncryptionPolicy{
		Mode:        cfg.RetainedCopyEncryption.Mode,
		KeyProvider: providerState(config.RetainedCopyKeyProvider{}),
		Sets:        []apicontract.EncryptionSetPolicy{},
	}
	if blk := cfg.RetainedCopyEncryptionBlock; blk != nil && blk.KeyProvider != nil {
		out.KeyProvider = providerState(*blk.KeyProvider)
	}
	for _, src := range cfg.Sources {
		for _, bs := range src.BackupSets {
			out.Sets = append(out.Sets, setPolicyOf(src.Name, bs))
		}
	}
	return out
}

// EncryptionPolicy is GET /encryption/policy: the deployment's mode and key
// provider, and what every backup set resolves to and why.
func (b *BackupService) EncryptionPolicy(_ context.Context) (apicontract.EncryptionPolicy, error) {
	return policyOf(b.state.Load().inner.Config), nil
}

// PatchEncryptionPolicy is PATCH /encryption/policy: the named fields change
// and the rest stay. It runs the same config.Validate a hand-edited
// config.yaml goes through at boot, so `required` with no provider is
// refused with the words Load would use.
func (b *BackupService) PatchEncryptionPolicy(_ context.Context, req apicontract.EncryptionPolicyPatch) (apicontract.EncryptionPolicy, error) {
	if req.Mode == "" && req.KeyProvider == nil {
		return apicontract.EncryptionPolicy{}, fmt.Errorf("%w: the patch names neither a mode nor a key provider", ErrInvalidRequest)
	}
	cfg, err := b.writeEncryptionConfig(func(cfg *config.Config) error {
		blk := config.RetainedCopyEncryptionBlock{}
		if cfg.RetainedCopyEncryptionBlock != nil {
			blk = *cfg.RetainedCopyEncryptionBlock
		}
		if req.Mode != "" {
			blk.Mode = req.Mode
		}
		if req.KeyProvider != nil {
			blk.KeyProvider = configProvider(req.KeyProvider)
		}
		cfg.RetainedCopyEncryptionBlock = &blk
		return nil
	})
	if err != nil {
		return apicontract.EncryptionPolicy{}, err
	}
	return policyOf(cfg), nil
}

// ResetEncryptionPolicy is POST /encryption/policy/reset: the deployment's
// block is removed, which resolves to off with no provider and writes the
// bytes a configuration that never mentioned the key would have.
func (b *BackupService) ResetEncryptionPolicy(_ context.Context) (apicontract.EncryptionPolicy, error) {
	cfg, err := b.writeEncryptionConfig(func(cfg *config.Config) error {
		cfg.RetainedCopyEncryptionBlock = nil
		return nil
	})
	if err != nil {
		return apicontract.EncryptionPolicy{}, err
	}
	return policyOf(cfg), nil
}

// SetBackupSetEncryption is PUT /backup-sets/{source}/{set}/encryption: this
// set's own mode (inherit, off or required) and, for required, its own
// provider. A whole replace, like the retention override.
func (b *BackupService) SetBackupSetEncryption(_ context.Context, id string, o apicontract.EncryptionOverride) (apicontract.EncryptionSetPolicy, error) {
	return b.writeBackupSetEncryption(id, func(bs *config.BackupSet) {
		bs.RetainedCopyEncryptionConfig = &config.RetainedCopyEncryptionOverride{Mode: o.Mode, KeyProvider: configProvider(o.KeyProvider)}
	})
}

// ClearBackupSetEncryption is DELETE /backup-sets/{source}/{set}/encryption:
// the set follows the deployment's policy again.
func (b *BackupService) ClearBackupSetEncryption(_ context.Context, id string) (apicontract.EncryptionSetPolicy, error) {
	return b.writeBackupSetEncryption(id, func(bs *config.BackupSet) { bs.RetainedCopyEncryptionConfig = nil })
}

func (b *BackupService) writeBackupSetEncryption(id string, mutate func(*config.BackupSet)) (apicontract.EncryptionSetPolicy, error) {
	sourceName, setName, ok := splitBackupSetID(id)
	if !ok {
		return apicontract.EncryptionSetPolicy{}, fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
	}
	cfg, err := b.writeEncryptionConfig(func(cfg *config.Config) error {
		found := false
		for i := range cfg.Sources {
			if cfg.Sources[i].Name != sourceName {
				continue
			}
			for j := range cfg.Sources[i].BackupSets {
				if cfg.Sources[i].BackupSets[j].Name == setName {
					mutate(&cfg.Sources[i].BackupSets[j])
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
		}
		return nil
	})
	if err != nil {
		return apicontract.EncryptionSetPolicy{}, err
	}
	bs, _ := lookupBackupSet(cfg, sourceName, setName)
	return setPolicyOf(sourceName, bs), nil
}

// writeEncryptionConfig is the recipe writeBackupSetRetention uses: re-read
// the file, apply the change, encode BEFORE validating (so the resolved
// fields never reach the file), validate with the boot-time rules, write
// atomically, adopt. It returns the validated config the engine now holds.
func (b *BackupService) writeEncryptionConfig(mutate func(*config.Config) error) (*config.Config, error) {
	if b.configPath == "" {
		return nil, ErrConfigNotFileBacked
	}
	b.configMu.Lock()
	defer b.configMu.Unlock()

	cfg, err := config.Load(b.configPath)
	if err != nil {
		return nil, fmt.Errorf("service: re-reading configuration: %w", err)
	}
	if err := mutate(cfg); err != nil {
		return nil, err
	}
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("service: encoding configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	applyValidators, err := planValidatorCatalog(cfg)
	if err != nil {
		return nil, err
	}
	if err := writeConfigBytesAtomically(b.configPath, encoded); err != nil {
		return nil, fmt.Errorf("service: persisting configuration: %w", err)
	}
	applyValidators()
	b.adoptConfig(cfg)
	return cfg, nil
}

// bundleConfirmations maps key id to the newest confirmation time.
func (b *BackupService) bundleConfirmations(ctx context.Context) (map[string]time.Time, error) {
	cs, err := b.keys.registry().Confirmations(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: reading bundle confirmations: %w", err)
	}
	at := map[string]time.Time{}
	for _, c := range cs {
		if prev, ok := at[c.KeyID]; !ok || c.ConfirmedAt.After(prev) {
			at[c.KeyID] = c.ConfirmedAt
		}
	}
	return at, nil
}

func (b *BackupService) keyStatus(ctx context.Context, store *keycustody.Store, rec keycustody.KeyRecord, confirmed map[string]time.Time) apicontract.EncryptionKeyStatus {
	out := apicontract.EncryptionKeyStatus{
		ID:        rec.ID,
		Status:    string(rec.Status),
		CreatedAt: rec.Created.UTC().Format(time.RFC3339),
	}
	if rec.Retired != nil {
		out.RetiredAt = rec.Retired.UTC().Format(time.RFC3339)
	}
	if at, ok := confirmed[rec.ID]; ok {
		out.BundleConfirmed = true
		out.BundleConfirmedAt = at.UTC().Format(time.RFC3339)
	}
	probe := store.Probe(ctx, rec.ID)
	out.Resolvability = probe.State.String()
	out.Resolvable = probe.State == keycustody.Resolves
	out.Reason = probe.Reason
	return out
}

// ListEncryptionKeys is GET /encryption/keys: id, status, timestamps,
// resolvability and bundle confirmation for every managed key. Never bytes,
// never a recipient or a fingerprint: those are functions of the key.
func (b *BackupService) ListEncryptionKeys(ctx context.Context) (apicontract.ListEncryptionKeysResponse, error) {
	out := apicontract.ListEncryptionKeysResponse{Keys: []apicontract.EncryptionKeyStatus{}}
	recs, err := b.keys.Keys(ctx)
	if err != nil {
		return out, fmt.Errorf("service: reading the key registry: %w", err)
	}
	if len(recs) == 0 {
		return out, nil
	}
	store, err := b.keys.Store(ctx)
	if err != nil {
		return out, err
	}
	confirmed, err := b.bundleConfirmations(ctx)
	if err != nil {
		return out, err
	}
	for _, rec := range recs {
		out.Keys = append(out.Keys, b.keyStatus(ctx, store, rec, confirmed))
	}
	return out, nil
}

func (b *BackupService) oneKeyStatus(ctx context.Context, store *keycustody.Store, id string) (apicontract.EncryptionKeyStatus, error) {
	recs, err := store.Keys(ctx)
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	confirmed, err := b.bundleConfirmations(ctx)
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	for _, rec := range recs {
		if rec.ID == id {
			return b.keyStatus(ctx, store, rec, confirmed), nil
		}
	}
	return apicontract.EncryptionKeyStatus{}, fmt.Errorf("%w: %s", keycustody.ErrUnknownKey, id)
}

// CreateEncryptionKey is POST /encryption/keys: the first managed key.
func (b *BackupService) CreateEncryptionKey(ctx context.Context) (_ apicontract.EncryptionKeyStatus, err error) {
	defer func() { err = classifyEncryption(err) }()
	store, err := b.keys.Store(ctx)
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	rec, err := store.Create(ctx, now())
	b.logger.KeyCreated(ctx, rec.ID, resultOfErr(err))
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	return b.oneKeyStatus(ctx, store, rec.ID)
}

// RotateEncryptionKey is POST /encryption/keys/rotate: a new active key, the
// old one superseded. New copies use the new key; every old copy still
// decrypts through the id recorded against it.
func (b *BackupService) RotateEncryptionKey(ctx context.Context) (_ apicontract.EncryptionKeyStatus, err error) {
	defer func() { err = classifyEncryption(err) }()
	store, err := b.keys.Store(ctx)
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	previous, _, _ := b.keys.Active(ctx)
	rec, err := store.Rotate(ctx, now())
	b.logger.KeyRotated(ctx, previous.ID, rec.ID, resultOfErr(err))
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	return b.oneKeyStatus(ctx, store, rec.ID)
}

// RetireEncryptionKey is POST /encryption/keys/{id}/retire. It is
// placement.RetireKey, which refuses naming the copies while an ACTIVE copy
// still records the key. A journal without the retirement transaction
// refuses with ErrEncryptionRetireUnavailable instead of retiring without
// the check.
func (b *BackupService) RetireEncryptionKey(ctx context.Context, id string) (_ apicontract.EncryptionKeyStatus, err error) {
	defer func() { err = classifyEncryption(err) }()
	kj := b.retireJournal
	if kj == nil {
		var ok bool
		if kj, ok = any(b.journal).(placement.KeyJournal); !ok {
			return apicontract.EncryptionKeyStatus{}, ErrEncryptionRetireUnavailable
		}
	}
	if err := placement.RetireKey(ctx, kj, b.logger, id, now()); err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	store, err := b.keys.Store(ctx)
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	return b.oneKeyStatus(ctx, store, id)
}

// ExportRecoveryBundle writes the disaster-recovery bundle for key id to w
// and records the export. It is the one operation in this file that lets key
// material leave the deployment, it needs a deliberate call, and what it
// returns is the receipt (id, digest, time), not the bundle.
func (b *BackupService) ExportRecoveryBundle(ctx context.Context, id string, w io.Writer) (_ apicontract.RecoveryBundleReceipt, err error) {
	defer func() { err = classifyEncryption(err) }()
	store, err := b.keys.Store(ctx)
	if err != nil {
		return apicontract.RecoveryBundleReceipt{}, err
	}
	r, err := store.Export(ctx, id, w, now())
	b.logger.KeyBundleExported(ctx, id, r.Digest, resultOfErr(err))
	if err != nil {
		return apicontract.RecoveryBundleReceipt{}, err
	}
	return apicontract.RecoveryBundleReceipt{KeyID: r.KeyID, Digest: r.Digest, ExportedAt: r.ExportedAt.UTC().Format(time.RFC3339)}, nil
}

// ExportRecoveryBundleBytes is ExportRecoveryBundle into memory, for the
// route that answers with the file. The caller is responsible for the bytes
// (no-store headers, zeroing).
func (b *BackupService) ExportRecoveryBundleBytes(ctx context.Context, id string) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := b.ExportRecoveryBundle(ctx, id, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ConfirmRecoveryBundle is POST /encryption/keys/{id}/confirm. A statement,
// not an upload: with no digest it confirms the newest export recorded for
// the key, with one it must match an export. It clears the durable degraded
// state (health.Report.KeyCustody) and is refused when nothing was exported.
func (b *BackupService) ConfirmRecoveryBundle(ctx context.Context, id, digest string) (_ apicontract.EncryptionKeyStatus, err error) {
	defer func() { err = classifyEncryption(err) }()
	store, err := b.keys.Store(ctx)
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	if digest == "" {
		exports, err := b.keys.registry().Exports(ctx, id)
		if err != nil {
			return apicontract.EncryptionKeyStatus{}, err
		}
		if len(exports) == 0 {
			b.logger.KeyBundleConfirmed(ctx, id, resultOfErr(keycustody.ErrNoSuchExport))
			return apicontract.EncryptionKeyStatus{}, fmt.Errorf("%w: key %s has no exported bundle to confirm", keycustody.ErrNoSuchExport, id)
		}
		newest := exports[0]
		for _, e := range exports {
			if e.ExportedAt.After(newest.ExportedAt) {
				newest = e
			}
		}
		digest = newest.Digest
	}
	err = store.Confirm(ctx, id, digest, now())
	b.logger.KeyBundleConfirmed(ctx, id, resultOfErr(err))
	if err != nil {
		return apicontract.EncryptionKeyStatus{}, err
	}
	return b.oneKeyStatus(ctx, store, id)
}

// TestEncryptionProvider is POST /encryption/providers/test: whether the
// named provider resolves right now, and why not. The reference is read from
// the request and answered as its description. The material is discarded as
// soon as it is known to exist.
func (b *BackupService) TestEncryptionProvider(ctx context.Context, req apicontract.EncryptionProviderTestRequest) (_ apicontract.EncryptionProviderProbe, err error) {
	defer func() { err = classifyEncryption(err) }()
	var p keycustody.Provider
	if req.ManagedKeyID != "" {
		p.Managed = req.ManagedKeyID
	}
	if req.External != nil {
		p.External = secretref.Ref{File: req.External.File, Env: req.External.Env, Command: append([]string(nil), req.External.Command...)}
		if err := p.External.Validate(); err != nil {
			return apicontract.EncryptionProviderProbe{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
	}
	if p.Managed == "" && p.External.IsZero() {
		return apicontract.EncryptionProviderProbe{}, fmt.Errorf("%w: name a managed key id or an external reference to test", ErrInvalidRequest)
	}
	var probe keycustody.Probe
	if p.Managed != "" && p.External.IsZero() {
		store, err := b.keys.Store(ctx)
		if err != nil {
			return apicontract.EncryptionProviderProbe{}, err
		}
		probe = store.Probe(ctx, p.Managed)
	} else if p.Managed == "" {
		probe = keycustody.ProbeExternal(ctx, p.External)
	} else {
		store, err := b.keys.Store(ctx)
		if err != nil {
			return apicontract.EncryptionProviderProbe{}, err
		}
		probe = store.Resolvability(ctx, p)
	}
	return apicontract.EncryptionProviderProbe{Source: probe.Source, State: probe.State.String(), Reason: probe.Reason}, nil
}

// EncryptionCopies is GET /encryption/copies?artifact=: for each copy of one
// backup, whether it is encrypted, under which key and format, and whether
// that key resolves. A plaintext copy says Encrypted false and nothing else.
func (b *BackupService) EncryptionCopies(ctx context.Context, artifactID string) (apicontract.ListEncryptionCopiesResponse, error) {
	a, err := b.GetArtifact(ctx, artifactID)
	if err != nil {
		return apicontract.ListEncryptionCopiesResponse{}, err
	}
	id, err := app.ParseArtifactID(artifactID)
	if err != nil {
		return apicontract.ListEncryptionCopiesResponse{}, fmt.Errorf("%w: %s", ErrArtifactNotFound, artifactID)
	}
	out := apicontract.ListEncryptionCopiesResponse{ArtifactID: a.ID, Copies: []apicontract.EncryptionCopy{}}
	for _, p := range a.Placements {
		c := apicontract.EncryptionCopy{Medium: p.Medium}
		enc, ok, err := b.journal.PlacementEncryption(ctx, id, p.Medium)
		if err != nil {
			return out, fmt.Errorf("service: reading the encryption of %s on %s: %w", artifactID, p.Medium, err)
		}
		if ok {
			c.Encrypted = true
			c.Format = enc.FormatID
			c.FormatVersion = enc.FormatVersion
			c.KeyID = enc.KeyID
			c.StoredSize = enc.StoredSize
			c.StoredDigestAlgorithm = enc.StoredHashAlg
			resolvable := b.keyResolves(ctx, enc.KeyID)
			c.KeyResolvable = &resolvable
		}
		out.Copies = append(out.Copies, c)
	}
	return out, nil
}

func (b *BackupService) keyResolves(ctx context.Context, keyID string) bool {
	store, err := b.keys.Store(ctx)
	if err != nil {
		return false
	}
	return store.Probe(ctx, keyID).State == keycustody.Resolves
}

// prepareDecryptVerify is every refusal a decrypt-verify has, in the order it
// has them and before anything is read: a bad id, a set that cannot be
// scrubbed, a backup with no encrypted copy, and a copy whose recorded key
// does not resolve (ErrEncryptionKeyUnresolvable). The route, the engine's
// background start and the terminal's own run all come through here, which is
// why the refusal reads the same on each.
func (b *BackupService) prepareDecryptVerify(ctx context.Context, req apicontract.StartDecryptVerifyRequest) (model.BackupSetID, model.ArtifactID, error) {
	id, err := app.ParseArtifactID(req.ArtifactID)
	if err != nil {
		return model.BackupSetID{}, model.ArtifactID{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	set, err := b.assuranceSet(id.Set.String())
	if err != nil {
		return model.BackupSetID{}, model.ArtifactID{}, err
	}
	a, err := b.GetArtifact(ctx, req.ArtifactID)
	if err != nil {
		return model.BackupSetID{}, model.ArtifactID{}, err
	}
	var encrypted int
	for _, p := range a.Placements {
		if req.Medium != "" && p.Medium != req.Medium {
			continue
		}
		enc, ok, err := b.journal.PlacementEncryption(ctx, id, p.Medium)
		if err != nil {
			return model.BackupSetID{}, model.ArtifactID{}, fmt.Errorf("service: reading the encryption of %s on %s: %w", req.ArtifactID, p.Medium, err)
		}
		if !ok {
			continue
		}
		encrypted++
		if !b.keyResolves(ctx, enc.KeyID) {
			return model.BackupSetID{}, model.ArtifactID{}, fmt.Errorf("%w: %s on %s was written under key %s", ErrEncryptionKeyUnresolvable, req.ArtifactID, p.Medium, enc.KeyID)
		}
	}
	if encrypted == 0 {
		return model.BackupSetID{}, model.ArtifactID{}, fmt.Errorf("%w: %s has no encrypted copy to decrypt-verify", ErrInvalidRequest, req.ArtifactID)
	}
	return set, id, nil
}

func decryptVerifyScrub(id model.ArtifactID, req apicontract.StartDecryptVerifyRequest, opID string) app.ScrubRequest {
	return app.ScrubRequest{
		Trigger: state.ScrubByManual, Artifact: &id, Medium: req.Medium,
		Content: true, AllowEgress: req.AcknowledgeEgress, OpID: opID,
	}
}

// RunDecryptVerify is the terminal's decrypt-verify when no engine is
// serving: the same refusals, then the same content scrub, run to the end in
// this process, with ctx as the only way to stop it.
func (b *BackupService) RunDecryptVerify(ctx context.Context, req apicontract.StartDecryptVerifyRequest) (apicontract.ScrubResult, error) {
	_, id, err := b.prepareDecryptVerify(ctx, req)
	if err != nil {
		return apicontract.ScrubResult{}, err
	}
	sum, err := b.state.Load().inner.Scrub(app.WithBackupSetHolds(ctx, b.holds), decryptVerifyScrub(id, req, ""))
	if err != nil {
		return apicontract.ScrubResult{}, err
	}
	return sum.Wire(), nil
}

// StartDecryptVerify is POST /encryption/verify: a content scrub of one
// backup's copy on a medium, which for an encrypted copy is a streaming
// decrypt whose plaintext digest has to equal the recorded one. It starts
// under the same operation registry as a scrub, so the cancel route is the
// same route from a terminal and from a browser (N1.20).
//
// Without the egress acknowledgement the scrub refuses a read the medium
// would bill for, which is the existing refusal and not a second one.
func (b *BackupService) StartDecryptVerify(ctx context.Context, req apicontract.StartDecryptVerifyRequest) (ScrubStarted, error) {
	set, id, err := b.prepareDecryptVerify(ctx, req)
	if err != nil {
		return ScrubStarted{}, err
	}
	opID := uuid.NewString()
	opCtx, cancel := context.WithCancelCause(b.ctx)
	b.ops.mu.Lock()
	if b.ops.running(opKindScrub, set.String()) != nil {
		b.ops.mu.Unlock()
		cancel(nil)
		return ScrubStarted{}, ErrScrubRunning
	}
	b.ops.add(&assuranceOp{id: opID, kind: opKindScrub, set: set.String(), cancel: cancel})
	b.ops.mu.Unlock()

	inner := b.state.Load().inner
	scrub := decryptVerifyScrub(id, req, opID)
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer b.ops.finish(opID)
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error(context.Background(), "decrypt-verify-panic", fmt.Errorf("recovered panic: %v", r))
			}
		}()
		if _, err := inner.Scrub(app.WithBackupSetHolds(opCtx, b.holds), scrub); err != nil && opCtx.Err() == nil {
			b.logger.Error(context.Background(), "decrypt-verify", err)
		}
	}()
	return ScrubStarted{BackupSetID: set.String(), OperationID: opID}, nil
}

// resultOfErr is the obs Result an event for err carries.
func resultOfErr(err error) obs.Result {
	if err != nil {
		return obs.ResultError
	}
	return obs.ResultSuccess
}
