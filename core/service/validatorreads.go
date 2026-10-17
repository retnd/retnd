package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
)

// The custom validators' reads (#1218): the journal scan behind list, preview
// and patterns, and the services that answer them. The rules they apply live
// in core/internal/config (assurancevalidatorsmatch.go) and take names, not a
// journal, so a scan here is "gather the names, hand them over".
//
// # What a read costs
//
// A list or a preview matches every artifact name in scope against every
// pattern. The patterns are bounded where they are accepted
// (config.CompileValidatorPattern); the scan checks the request's context as
// it goes; script hashes are cached by (path, modification time, size); and
// a write scans the journal before it takes the configuration lock, so a slow
// scan never holds up another configuration change.

// validatorScanHook is a test seam: it is called with "start" before a
// journal scan and "listed" after each set's backups were read.
var validatorScanHook func(stage string, set model.BackupSetID)

// validatorArtifacts is one set's artifact names, newest first, and what the
// journal says about the tests run under each validator.
type validatorArtifacts struct {
	config.SetNames
	// last maps a validator script file to the newest test recorded under it.
	last map[string]CustomValidatorResult
}

// artifactsInScope reads the journal for every configured set, or one. It
// stops as soon as ctx is done, and it takes no lock: a caller that holds
// configMu has already made the mistake this comment is about.
func (b *BackupService) artifactsInScope(ctx context.Context, cfg *config.Config, only model.BackupSetID) ([]validatorArtifacts, error) {
	if validatorScanHook != nil {
		validatorScanHook("start", model.BackupSetID{})
	}
	var out []validatorArtifacts
	for _, src := range cfg.Sources {
		for _, bs := range src.BackupSets {
			if !only.IsZero() && bs.ID != only {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			va := validatorArtifacts{SetNames: config.SetNames{Set: bs.ID}, last: map[string]CustomValidatorResult{}}
			if b.journal != nil {
				// Names and the newest test per validator, not the records:
				// a scan reads one name and a couple of placement fields from
				// each artifact, and the whole record is far more than that.
				scan, err := b.journal.NamesAndLastRecoveryTests(ctx, bs.ID)
				if err != nil {
					return nil, fmt.Errorf("service: reading %s's backups: %w", bs.ID, err)
				}
				if validatorScanHook != nil {
					validatorScanHook("listed", bs.ID)
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				va.Names = scan.Names
				for validator, t := range scan.LastTests {
					va.last[validator] = CustomValidatorResult{State: t.State, At: t.At, Artifact: t.Artifact.String()}
				}
			}
			out = append(out, va)
		}
	}
	return out, nil
}

// setNames is the config package's view of a scan: the names alone.
func setNames(scope []validatorArtifacts) []config.SetNames {
	out := make([]config.SetNames, 0, len(scope))
	for _, va := range scope {
		out = append(out, va.SetNames)
	}
	return out
}

func describeValidator(ctx context.Context, cfg *config.Config, store validatorStore, cv config.ResolvedCustomValidator, scope []validatorArtifacts) (CustomValidator, error) {
	v := CustomValidator{
		Name: cv.Name, Pattern: cv.Pattern, Scope: cv.Scope(), Anchored: config.PatternAnchored(cv.Pattern),
		ScriptFile: cv.ScriptFile, MatchSets: []string{},
	}
	if !cv.Global() {
		v.BackupSetID = cv.BackupSet.String()
	}
	info, present := store.Describe(cv.ScriptFile, maxValidatorScriptBytes(cfg))
	v.UploadedAt = info.ModTime
	if present {
		v.ScriptPresent, v.ScriptSHA256, v.ScriptSize = true, info.SHA256, info.Size
	}
	m, err := config.ScanValidatorMatches(ctx, cv, setNames(scope))
	if err != nil {
		return CustomValidator{}, err
	}
	v.Matches, v.MatchSets, v.ExampleName = m.Count, m.Sets, m.Example
	for _, va := range scope {
		if !cv.AppliesTo(va.Set) {
			continue
		}
		if r, ok := va.last[cv.ScriptFile]; ok && (v.LastResult == nil || r.At.After(v.LastResult.At)) {
			rr := r
			v.LastResult = &rr
		}
	}
	return v, nil
}

func (b *BackupService) parseScopeSet(cfg *config.Config, id string) (model.BackupSetID, error) {
	if id == "" {
		return model.BackupSetID{}, nil
	}
	bs, err := lookupConfiguredBackupSet(cfg, id)
	if err != nil {
		return model.BackupSetID{}, err
	}
	return bs.ID, nil
}

// ListCustomValidators lists the custom validators, or (filter) the ones a
// set would try, in order, and which one an artifact would get.
//
// An artifact narrows the answer to that artifact's backup set exactly as a
// backup set does (and the two must agree): the resolution is about one name
// in one set, and reading the whole deployment's journal to answer it was the
// cost the review found.
func (b *BackupService) ListCustomValidators(ctx context.Context, filter CustomValidatorsFilter) (CustomValidators, error) {
	st := b.state.Load()
	cfg := st.inner.Config
	only, err := b.parseScopeSet(cfg, filter.BackupSetID)
	if err != nil {
		return CustomValidators{}, err
	}
	var artifact model.ArtifactID
	if filter.ArtifactID != "" {
		artifact, err = parseArtifactID(filter.ArtifactID)
		if err != nil {
			return CustomValidators{}, fmt.Errorf("%w: %q is not an artifact id (source/set/name)", ErrInvalidRequest, filter.ArtifactID)
		}
		if _, err := lookupConfiguredBackupSet(cfg, artifact.Set.String()); err != nil {
			return CustomValidators{}, err
		}
		if !only.IsZero() && only != artifact.Set {
			return CustomValidators{}, fmt.Errorf("%w: backup_set %s is not the set of artifact %s", ErrInvalidRequest, only, artifact)
		}
		only = artifact.Set
	}

	root, why := workflowRootFor(cfg)
	store := newValidatorStore(root)
	out := CustomValidators{
		Directory: config.CustomValidatorDir, WorkflowRoot: cfg.Workflows.Root,
		Available: why == "" && b.configPath != "", UnavailableReason: why,
		RunnerConfigured: cfg.Workflows.Runner.Configured(),
		MaxValidators:    config.MaxCustomValidators, MaxScriptBytes: maxValidatorScriptBytes(cfg),
		MaxPatternBytes: config.MaxCustomValidatorPatternBytes,
		Validators:      []CustomValidator{},
		Ambiguities:     []CustomValidatorAmbiguity{},
	}
	if b.configPath == "" && why == "" {
		out.UnavailableReason = "this deployment has no configuration file to keep a validator in"
	}
	out.DirectoryRefused = store.Refusal()
	scope, err := b.artifactsInScope(ctx, cfg, only)
	if err != nil {
		return CustomValidators{}, err
	}
	list := cfg.Assurance.Validators
	// Set-scoped first, then global: the order a test of that set tries.
	ordered := config.OrderValidatorsFor(list, only)
	for _, cv := range ordered {
		v, err := describeValidator(ctx, cfg, store, cv, scope)
		if err != nil {
			return CustomValidators{}, err
		}
		out.Validators = append(out.Validators, v)
	}
	ambiguities, err := config.FindAmbiguities(ctx, ordered, setNames(scope))
	if err != nil {
		return CustomValidators{}, err
	}
	for _, a := range ambiguities {
		out.Ambiguities = append(out.Ambiguities, CustomValidatorAmbiguity{BackupSetID: a.Set, Validators: a.Validators, Example: a.Example})
	}

	if filter.ArtifactID != "" {
		res := &CustomValidatorResolution{ArtifactID: artifact.String()}
		m := config.MatchCustomValidator(list, artifact.Set, artifact.Name)
		if m.Validator != nil {
			res.Validator = m.Validator.Name
		}
		res.Ambiguous = m.Ambiguous
		if !m.Found() {
			res.Fallback = b.fallbackFor(ctx, st, artifact.Set)
		}
		out.Resolution = res
	}
	return out, nil
}

// fallbackFor names what decides a set's test when no pattern does. It reads
// the configuration state once, so the answer is about one configuration.
func (b *BackupService) fallbackFor(ctx context.Context, st *configState, set model.BackupSetID) string {
	bs, err := lookupConfiguredBackupSet(st.inner.Config, set.String())
	if err != nil {
		return "none"
	}
	if bs.Assurance.RecoveryTest.ValidatorID != "" {
		return "validator_id"
	}
	check, _, err := st.inner.RecoveryTestEffectiveCheck(ctx, set)
	switch {
	case err != nil:
		return "none"
	case check == "validator":
		return "set_directory"
	default:
		return check
	}
}

// CustomValidatorScript reads one uploaded script back.
func (b *BackupService) CustomValidatorScript(_ context.Context, name string) (ValidatorScript, error) {
	cfg := b.state.Load().inner.Config
	cv, ok := findResolvedValidator(cfg, name)
	if !ok {
		return ValidatorScript{}, fmt.Errorf("%w: %q", ErrValidatorNotFound, name)
	}
	root, why := workflowRootFor(cfg)
	if why != "" {
		return ValidatorScript{}, fmt.Errorf("%w: %s", ErrValidatorsUnavailable, why)
	}
	body, err := newValidatorStore(root).Read(cv.ScriptFile, cfg.EffectiveMaxScriptSize())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ValidatorScript{}, fmt.Errorf("%w: %s is declared and its script %s is not in %s", ErrValidatorNotFound, name, cv.ScriptFile, config.CustomValidatorDir)
		}
		return ValidatorScript{}, fmt.Errorf("service: reading %s: %w", cv.ScriptFile, err)
	}
	return validatorScriptOf(cv.ScriptFile, body), nil
}

// PreviewCustomValidator says what a pattern would match, at a scope,
// without saving anything. It compiles with Go's RE2, which is the engine
// that will match it, so a browser's own regular expressions are never the
// judge. It needs only a CSRF token, so it is the route a hostile request
// would use to make the engine work: the pattern is bounded where it is
// compiled and the scan stops when the request does.
func (b *BackupService) PreviewCustomValidator(ctx context.Context, pattern, backupSetID, replacing string) (CustomValidatorPreview, error) {
	cfg := b.state.Load().inner.Config
	set, err := b.parseScopeSet(cfg, backupSetID)
	if err != nil {
		return CustomValidatorPreview{}, err
	}
	out := CustomValidatorPreview{Anchored: config.PatternAnchored(pattern), MatchSets: []string{}, Sample: []string{}, Conflicts: []CustomValidatorConflict{}}
	re, err := config.CompileValidatorPattern(pattern)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.Valid = true
	scope, err := b.artifactsInScope(ctx, cfg, set)
	if err != nil {
		return CustomValidatorPreview{}, err
	}
	pm, err := config.ScanPreview(ctx, re, setNames(scope), maxPreviewSample)
	if err != nil {
		return CustomValidatorPreview{}, err
	}
	out.Matches, out.MatchSets, out.Sample = pm.Count, pm.Sets, pm.Sample
	c, err := config.FindCollisions(ctx, cfg.Assurance.Validators, re, set, replacing, setNames(scope))
	if err != nil {
		return CustomValidatorPreview{}, err
	}
	for _, x := range c {
		out.Conflicts = append(out.Conflicts, CustomValidatorConflict{Validator: x.Validator, Example: x.Example})
	}
	return out, nil
}

// CustomValidatorPatterns suggests a pattern for every shape of artifact
// name in the journal, for one set or all of them, most common first.
func (b *BackupService) CustomValidatorPatterns(ctx context.Context, backupSetID string) (CustomValidatorPatterns, error) {
	cfg := b.state.Load().inner.Config
	set, err := b.parseScopeSet(cfg, backupSetID)
	if err != nil {
		return CustomValidatorPatterns{}, err
	}
	scope, err := b.artifactsInScope(ctx, cfg, set)
	if err != nil {
		return CustomValidatorPatterns{}, err
	}
	suggestions, err := config.SuggestPatterns(ctx, cfg.Assurance.Validators, set, setNames(scope), maxPatternSuggestions)
	if err != nil {
		return CustomValidatorPatterns{}, err
	}
	out := CustomValidatorPatterns{BackupSetID: backupSetID, Patterns: make([]CustomValidatorPattern, 0, len(suggestions))}
	for _, p := range suggestions {
		out.Patterns = append(out.Patterns, CustomValidatorPattern{Pattern: p.Pattern, Example: p.Example, Matches: p.Matches, MatchSets: p.Sets, Validator: p.Validator})
	}
	return out, nil
}
