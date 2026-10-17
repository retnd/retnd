package service

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/workflow"
	"github.com/retnd/retnd/core/internal/workflowlint"
)

// Custom recovery-test validators (#1203): the store, the reads and the
// suggestions behind /api/v1/assurance/validators and `retnd assurance
// validator`.
//
// # Where things live
//
// The policy is config.yaml's assurance.validators (name, pattern, optional
// backup set), written through persistConfig like every other assurance
// write, so it is validated with the whole file and hot-reloaded. The script
// is <workflow root>/recovery-test-validators/NAME.local.sh: one flat stage
// directory the recovery test selects from by name (StageRequest.Only).
//
// # How a script is written
//
// To a temporary file in the workflow root and then renamed into the
// directory, so discovery never sees a half-written file or an entry whose
// name is not a script's (it refuses a whole directory for one of those).
// The directory is 0750 and the file 0640, both owned by this process, which
// is what discovery's custody rules accept. The script is held to the same
// lint that gates a workflow save before a byte is written, because a script
// the runner would refuse at 3am is one to refuse now, with the reason.
//
// # Order of the two writes
//
// Script first, then the configuration. A configuration that fails to write
// puts the previous script back (or removes the new one), so the two never
// disagree for longer than this call. Removal goes the other way: the entry
// leaves the configuration first, so a script whose removal fails is an
// inert file nothing selects, never a validator whose script is gone.

// MaxCustomValidatorScriptBytes bounds an uploaded script. A deployment's own
// workflows.max_script_size_bytes binds too when it is smaller.
const MaxCustomValidatorScriptBytes = 256 << 10

// validatorScanHook is a test seam: it is called with "start" before a
// journal scan and "listed" after each set's backups were read.
var validatorScanHook func(stage string, set model.BackupSetID)

// maxPatternSuggestions bounds the suggestions one read returns.
const maxPatternSuggestions = 200

// maxPreviewSample bounds the matching names a preview lists.
const maxPreviewSample = 20

//go:embed recoveryvalidator/default.local.sh
var defaultValidatorScript string

// DefaultValidatorFileName is the file name the default script downloads as.
const DefaultValidatorFileName = "recovery-validator.local.sh"

// Errors the validator routes map onto their own codes.
var (
	ErrValidatorNotFound = errors.New("service: no custom validator has that name")
	ErrValidatorExists   = errors.New("service: a custom validator with that name already exists")
	// ErrValidatorConflict is a pattern that would share an existing
	// artifact with another validator at the same scope.
	ErrValidatorConflict = errors.New("service: the pattern collides with another validator at the same scope")
	// ErrValidatorLimit is a 65th validator.
	ErrValidatorLimit = errors.New("service: this deployment already has as many custom validators as one directory may hold")
	// ErrValidatorsUnavailable is a deployment with nowhere to keep a
	// script: no workflow root, or one this process cannot write.
	ErrValidatorsUnavailable = errors.New("service: custom validators cannot be stored on this deployment")
	// ErrValidatorScriptChanged is an edit that asked to keep a script by its
	// hash when the stored script is no longer that one.
	ErrValidatorScriptChanged = errors.New("service: the stored script is not the one this edit was made against")
)

// ValidatorScript is a script's bytes and identity.
type ValidatorScript struct {
	FileName  string
	SHA256    string
	SizeBytes int64
	Content   string
}

func validatorScriptOf(name string, body []byte) ValidatorScript {
	sum := sha256.Sum256(body)
	return ValidatorScript{FileName: name, SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body)), Content: string(body)}
}

// DefaultCustomValidatorScript is the documented default script an operator
// downloads to read and adapt.
func DefaultCustomValidatorScript() ValidatorScript {
	return validatorScriptOf(DefaultValidatorFileName, []byte(defaultValidatorScript))
}

// CustomValidatorResult is the newest recovery test a validator decided.
type CustomValidatorResult struct {
	State    string
	At       time.Time
	Artifact string
}

// CustomValidator is one custom validator as every surface shows it.
type CustomValidator struct {
	Name        string
	Pattern     string
	BackupSetID string // "" for a global validator
	Scope       string // config.CustomValidatorScope*
	// Anchored says the pattern begins with ^ and ends with $, so it matches
	// whole names. An unanchored pattern is allowed and matches anywhere in
	// the name; a surface may want to say so.
	Anchored bool

	ScriptFile    string
	ScriptPresent bool
	ScriptSHA256  string
	ScriptSize    int64
	UploadedAt    time.Time

	// Matches is how many artifacts in the journal, within the validator's
	// scope, have a name the pattern matches; MatchSets the sets they are
	// in; ExampleName the newest of them.
	Matches     int
	MatchSets   []string
	ExampleName string

	// LastResult is the newest recovery test recorded under this
	// validator's script, nil when none is.
	LastResult *CustomValidatorResult
}

// CustomValidatorResolution is which validator a test of one artifact
// would run (?artifact= on the list).
type CustomValidatorResolution struct {
	ArtifactID string
	// Validator is the custom validator's name, "" when no pattern applies.
	Validator string
	// Ambiguous names the validators whose patterns collide on this name.
	Ambiguous []string
	// Fallback says what decides when no pattern does: "validator_id",
	// "set_directory", "builtin" or "none", as the set's policy reads.
	Fallback string
}

// CustomValidators is the list read.
type CustomValidators struct {
	Validators []CustomValidator
	// Directory is the stage directory relative to the workflow root, and
	// WorkflowRoot the root itself ("" when none is configured).
	Directory    string
	WorkflowRoot string
	// Available says a validator can be uploaded here; Reason says why not.
	Available         bool
	UnavailableReason string
	// DirectoryRefused says why the stage directory cannot be used, "" when
	// it can or is not there yet.
	DirectoryRefused string
	RunnerConfigured bool
	// Ambiguities are backups in scope that two patterns match at one scope.
	Ambiguities []CustomValidatorAmbiguity

	MaxValidators   int
	MaxScriptBytes  int64
	MaxPatternBytes int

	Resolution *CustomValidatorResolution
}

// CustomValidatorsFilter narrows the list.
type CustomValidatorsFilter struct {
	// BackupSetID keeps the validators that apply to one set (its own and
	// the global ones), in the order they would be tried.
	BackupSetID string
	// ArtifactID ("source/set/name") adds which validator would run for it.
	ArtifactID string
}

// CreateCustomValidatorRequest uploads a validator, or (Update) replaces
// one. An update is a whole replacement: it carries either the new Script or
// the ScriptSHA256 of the stored one it means to keep, and never neither.
type CreateCustomValidatorRequest struct {
	Name        string
	Pattern     string
	BackupSetID string
	Script      string
	// ScriptSHA256 (update only) keeps the stored script, provided it is
	// still the one with this hash.
	ScriptSHA256 string
}

// CustomValidatorAmbiguity is one backup in the journal that more than one
// validator's pattern matches at the same scope.
type CustomValidatorAmbiguity struct {
	BackupSetID string
	Validators  []string
	Example     string
}

// CustomValidatorPreview is what a pattern would do, without saving it.
type CustomValidatorPreview struct {
	Valid     bool
	Error     string
	Anchored  bool
	Matches   int
	MatchSets []string
	Sample    []string
	// Conflicts names the validators at the same scope that already match
	// one of the same artifacts, with an example.
	Conflicts []CustomValidatorConflict
}

// CustomValidatorConflict is one collision a preview or an upload found.
type CustomValidatorConflict struct {
	Validator string
	Example   string
}

// CustomValidatorPattern is one suggested pattern.
type CustomValidatorPattern struct {
	Pattern   string
	Example   string
	Matches   int
	MatchSets []string
	// Validator is the custom validator whose pattern is exactly this one in
	// the requested scope, "" when none is.
	Validator string
}

// CustomValidatorPatterns is the suggestions read.
type CustomValidatorPatterns struct {
	BackupSetID string
	Patterns    []CustomValidatorPattern
}

// digitRun is a maximal run of ASCII digits, which \d means in RE2.
var digitRun = regexp.MustCompile(`[0-9]+`)

// SuggestArtifactPattern generalises an artifact name into the anchored RE2
// pattern an operator would otherwise write by hand: every run of digits
// becomes \d{n} and everything else is matched literally, so
// MANIFEST-20260902T153521Z.txt becomes ^MANIFEST-\d{8}T\d{6}Z\.txt$ and
// db-2026-09-02.sql.gz becomes ^db-\d{4}-\d{2}-\d{2}\.sql\.gz$.
func SuggestArtifactPattern(name string) string {
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, loc := range digitRun.FindAllStringIndex(name, -1) {
		b.WriteString(regexp.QuoteMeta(name[last:loc[0]]))
		fmt.Fprintf(&b, `\d{%d}`, loc[1]-loc[0])
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(name[last:]))
	b.WriteString("$")
	return b.String()
}

func patternAnchored(p string) bool {
	return strings.HasPrefix(p, "^") && strings.HasSuffix(p, "$") && !strings.HasSuffix(p, `\$`)
}

// validatorArtifacts is one set's artifact names, newest first.
type validatorArtifacts struct {
	set   model.BackupSetID
	names []string
	// last maps a validator script file to the newest test recorded under it.
	last map[string]CustomValidatorResult
}

// artifactsInScope reads the journal for every configured set, or one.
func (b *BackupService) artifactsInScope(ctx context.Context, cfg *config.Config, only model.BackupSetID) ([]validatorArtifacts, error) {
	var out []validatorArtifacts
	for _, src := range cfg.Sources {
		for _, bs := range src.BackupSets {
			if !only.IsZero() && bs.ID != only {
				continue
			}
			va := validatorArtifacts{set: bs.ID, last: map[string]CustomValidatorResult{}}
			if b.journal != nil {
				records, err := b.journal.ListByBackupSet(ctx, bs.ID)
				if err != nil {
					return nil, fmt.Errorf("service: reading %s's backups: %w", bs.ID, err)
				}
				sort.SliceStable(records, func(i, j int) bool { return records[i].DiscoveredAt.After(records[j].DiscoveredAt) })
				for _, r := range records {
					va.names = append(va.names, r.Artifact.Name)
					for _, p := range r.Placements {
						if p.RecoveryValidatorID == "" || p.RecoveryTestedAt == nil {
							continue
						}
						if cur, ok := va.last[p.RecoveryValidatorID]; !ok || p.RecoveryTestedAt.After(cur.At) {
							va.last[p.RecoveryValidatorID] = CustomValidatorResult{State: p.RecoveryState, At: p.RecoveryTestedAt.UTC(), Artifact: r.Artifact.String()}
						}
					}
				}
			}
			out = append(out, va)
		}
	}
	return out, nil
}

// workflowRootFor is the canonical workflow root, or why there is none.
func workflowRootFor(cfg *config.Config) (workflow.Root, string) {
	if cfg.Workflows.Root == "" {
		return workflow.Root{}, "no workflow root is configured (workflows.root), so there is nowhere to keep a validator script; set one under Settings > Workflow"
	}
	root, err := workflow.NewRoot(cfg.Workflows.Root)
	if err != nil {
		return workflow.Root{}, fmt.Sprintf("the workflow root cannot be used: %v", err)
	}
	return root, ""
}

func (b *BackupService) describeValidator(cfg *config.Config, root workflow.Root, cv config.ResolvedCustomValidator, scope []validatorArtifacts) CustomValidator {
	v := CustomValidator{
		Name: cv.Name, Pattern: cv.Pattern, Scope: cv.Scope(), Anchored: patternAnchored(cv.Pattern),
		ScriptFile: cv.ScriptFile, MatchSets: []string{},
	}
	if !cv.Global() {
		v.BackupSetID = cv.BackupSet.String()
	}
	if !root.IsZero() {
		path := filepath.Join(root.Path(), config.CustomValidatorDir, cv.ScriptFile)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			v.UploadedAt = info.ModTime().UTC()
			if body, err := readBounded(path, cfg.EffectiveMaxScriptSize()); err == nil {
				s := validatorScriptOf(cv.ScriptFile, body)
				v.ScriptPresent, v.ScriptSHA256, v.ScriptSize = true, s.SHA256, s.SizeBytes
			}
		}
	}
	for _, va := range scope {
		if !cv.AppliesTo(va.set) {
			continue
		}
		n := 0
		for _, name := range va.names {
			if cv.Regexp.MatchString(name) {
				if v.ExampleName == "" {
					v.ExampleName = name
				}
				n++
			}
		}
		if n > 0 {
			v.Matches += n
			v.MatchSets = append(v.MatchSets, va.set.String())
		}
		if r, ok := va.last[cv.ScriptFile]; ok && (v.LastResult == nil || r.At.After(v.LastResult.At)) {
			rr := r
			v.LastResult = &rr
		}
	}
	return v
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, limit+1)
	n, err := f.Read(buf)
	for err == nil && n < len(buf) {
		var m int
		m, err = f.Read(buf[n:])
		n += m
	}
	if int64(n) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return buf[:n], nil
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
	}

	root, why := workflowRootFor(cfg)
	out := CustomValidators{
		Directory: config.CustomValidatorDir, WorkflowRoot: cfg.Workflows.Root,
		Available: why == "" && b.configPath != "", UnavailableReason: why,
		RunnerConfigured: cfg.Workflows.Runner.Configured(),
		MaxValidators:    config.MaxCustomValidators, MaxScriptBytes: maxValidatorScriptBytes(cfg),
		MaxPatternBytes: config.MaxCustomValidatorPatternBytes,
		Validators:      []CustomValidator{},
	}
	if b.configPath == "" && why == "" {
		out.UnavailableReason = "this deployment has no configuration file to keep a validator in"
	}
	scope, err := b.artifactsInScope(ctx, cfg, only)
	if err != nil {
		return CustomValidators{}, err
	}
	list := cfg.Assurance.Validators
	// Set-scoped first, then global: the order a test of that set tries.
	ordered := make([]config.ResolvedCustomValidator, 0, len(list))
	for _, global := range []bool{false, true} {
		for _, cv := range list {
			if cv.Global() == global && (only.IsZero() || cv.AppliesTo(only)) {
				ordered = append(ordered, cv)
			}
		}
	}
	for _, cv := range ordered {
		out.Validators = append(out.Validators, b.describeValidator(cfg, root, cv, scope))
	}

	if filter.ArtifactID != "" {
		res := &CustomValidatorResolution{ArtifactID: artifact.String()}
		m := config.MatchCustomValidator(list, artifact.Set, artifact.Name)
		if m.Validator != nil {
			res.Validator = m.Validator.Name
		}
		res.Ambiguous = m.Ambiguous
		if !m.Found() {
			res.Fallback = b.fallbackFor(ctx, artifact.Set)
		}
		out.Resolution = res
	}
	return out, nil
}

// fallbackFor names what decides a set's test when no pattern does.
func (b *BackupService) fallbackFor(ctx context.Context, set model.BackupSetID) string {
	bs, err := lookupConfiguredBackupSet(b.state.Load().inner.Config, set.String())
	if err != nil {
		return "none"
	}
	if bs.Assurance.RecoveryTest.ValidatorID != "" {
		return "validator_id"
	}
	check, _, err := b.state.Load().inner.RecoveryTestEffectiveCheck(ctx, set)
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
	path := filepath.Join(root.Path(), config.CustomValidatorDir, cv.ScriptFile)
	body, err := readBounded(path, cfg.EffectiveMaxScriptSize())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ValidatorScript{}, fmt.Errorf("%w: %s is declared and its script %s is not in %s", ErrValidatorNotFound, name, cv.ScriptFile, config.CustomValidatorDir)
		}
		return ValidatorScript{}, fmt.Errorf("service: reading %s: %w", cv.ScriptFile, err)
	}
	return validatorScriptOf(cv.ScriptFile, body), nil
}

func findResolvedValidator(cfg *config.Config, name string) (config.ResolvedCustomValidator, bool) {
	for _, cv := range cfg.Assurance.Validators {
		if cv.Name == name {
			return cv, true
		}
	}
	return config.ResolvedCustomValidator{}, false
}

func maxValidatorScriptBytes(cfg *config.Config) int64 {
	if m := cfg.EffectiveMaxScriptSize(); m < MaxCustomValidatorScriptBytes {
		return m
	}
	return MaxCustomValidatorScriptBytes
}

// checkValidatorScript refuses a script the runner would refuse, before it
// is written anywhere.
func checkValidatorScript(ctx context.Context, cfg *config.Config, file, script string) error {
	if strings.TrimSpace(script) == "" {
		return fmt.Errorf("%w: the script is empty", ErrInvalidRequest)
	}
	if limit := maxValidatorScriptBytes(cfg); int64(len(script)) > limit {
		return fmt.Errorf("%w: the script is %d bytes and a validator may be at most %d", ErrInvalidRequest, len(script), limit)
	}
	if !utf8.ValidString(script) || strings.ContainsRune(script, 0) {
		return fmt.Errorf("%w: the script is not UTF-8 text (or holds a NUL byte); a validator is a bash script", ErrInvalidRequest)
	}
	if report := workflowlint.Report(ctx, file, []byte(script)); report.Blocks() {
		return fmt.Errorf("%w: the script was refused by the same check that gates saving a workflow: %s", ErrInvalidRequest, describeBlocking(report))
	}
	return nil
}

// collisions finds the existing validators at cv's scope that would share an
// artifact with it. skip is the name being replaced.
func collisions(list []config.ResolvedCustomValidator, re *regexp.Regexp, set model.BackupSetID, skip string, scope []validatorArtifacts) []CustomValidatorConflict {
	var out []CustomValidatorConflict
	seen := map[string]bool{}
	for _, other := range list {
		if other.Name == skip || other.BackupSet != set || seen[other.Name] {
			continue
		}
		for _, va := range scope {
			if !set.IsZero() && va.set != set {
				continue
			}
			for _, name := range va.names {
				if re.MatchString(name) && other.Regexp.MatchString(name) {
					out = append(out, CustomValidatorConflict{Validator: other.Name, Example: va.set.String() + "/" + name})
					seen[other.Name] = true
					break
				}
			}
			if seen[other.Name] {
				break
			}
		}
	}
	return out
}

// PreviewCustomValidator says what a pattern would match, at a scope,
// without saving anything. It compiles with Go's RE2, which is the engine
// that will match it, so a browser's own regular expressions are never the
// judge.
func (b *BackupService) PreviewCustomValidator(ctx context.Context, pattern, backupSetID, replacing string) (CustomValidatorPreview, error) {
	cfg := b.state.Load().inner.Config
	set, err := b.parseScopeSet(cfg, backupSetID)
	if err != nil {
		return CustomValidatorPreview{}, err
	}
	out := CustomValidatorPreview{Anchored: patternAnchored(pattern), MatchSets: []string{}, Sample: []string{}, Conflicts: []CustomValidatorConflict{}}
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
	for _, va := range scope {
		n := 0
		for _, name := range va.names {
			if re.MatchString(name) {
				n++
				if len(out.Sample) < maxPreviewSample {
					out.Sample = append(out.Sample, va.set.String()+"/"+name)
				}
			}
		}
		if n > 0 {
			out.Matches += n
			out.MatchSets = append(out.MatchSets, va.set.String())
		}
	}
	if c := collisions(cfg.Assurance.Validators, re, set, replacing, scope); c != nil {
		out.Conflicts = c
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
	type acc struct {
		p    CustomValidatorPattern
		sets map[string]bool
	}
	byPattern := map[string]*acc{}
	for _, va := range scope {
		for _, name := range va.names {
			p := SuggestArtifactPattern(name)
			a, ok := byPattern[p]
			if !ok {
				a = &acc{p: CustomValidatorPattern{Pattern: p, Example: name}, sets: map[string]bool{}}
				byPattern[p] = a
			}
			a.p.Matches++
			a.sets[va.set.String()] = true
		}
	}
	out := CustomValidatorPatterns{BackupSetID: backupSetID, Patterns: []CustomValidatorPattern{}}
	for _, a := range byPattern {
		a.p.MatchSets = make([]string, 0, len(a.sets))
		for s := range a.sets {
			a.p.MatchSets = append(a.p.MatchSets, s)
		}
		sort.Strings(a.p.MatchSets)
		for _, cv := range cfg.Assurance.Validators {
			if cv.Pattern == a.p.Pattern && cv.BackupSet == set {
				a.p.Validator = cv.Name
			}
		}
		out.Patterns = append(out.Patterns, a.p)
	}
	sort.Slice(out.Patterns, func(i, j int) bool {
		if out.Patterns[i].Matches != out.Patterns[j].Matches {
			return out.Patterns[i].Matches > out.Patterns[j].Matches
		}
		return out.Patterns[i].Pattern < out.Patterns[j].Pattern
	})
	if len(out.Patterns) > maxPatternSuggestions {
		out.Patterns = out.Patterns[:maxPatternSuggestions]
	}
	return out, nil
}

// CreateCustomValidator uploads a new validator. A name that is taken is
// ErrValidatorExists: replacing one is UpdateCustomValidator.
func (b *BackupService) CreateCustomValidator(ctx context.Context, req CreateCustomValidatorRequest) (CustomValidator, error) {
	return b.writeCustomValidator(ctx, req, false)
}

// UpdateCustomValidator replaces a validator's pattern, scope and (when
// Script is not empty) script.
func (b *BackupService) UpdateCustomValidator(ctx context.Context, req CreateCustomValidatorRequest) (CustomValidator, error) {
	return b.writeCustomValidator(ctx, req, true)
}

func (b *BackupService) writeCustomValidator(ctx context.Context, req CreateCustomValidatorRequest, update bool) (CustomValidator, error) {
	if b.configPath == "" {
		return CustomValidator{}, ErrConfigNotFileBacked
	}
	req.Name = strings.TrimSpace(req.Name)
	req.BackupSetID = strings.TrimSpace(req.BackupSetID)
	file, err := config.RecoveryTestScriptName(req.Name)
	if err != nil {
		return CustomValidator{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	re, err := config.CompileValidatorPattern(req.Pattern)
	if err != nil {
		return CustomValidator{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	b.configMu.Lock()
	defer b.configMu.Unlock()

	cfg, err := config.Load(b.configPath)
	if err != nil {
		return CustomValidator{}, fmt.Errorf("service: re-reading configuration: %w", err)
	}
	// The running config has every id resolved; the file read above does
	// not, so scope and collisions are judged against the running one.
	running := b.state.Load().inner.Config
	set, err := b.parseScopeSet(running, req.BackupSetID)
	if err != nil {
		return CustomValidator{}, err
	}
	root, why := workflowRootFor(running)
	if why != "" {
		return CustomValidator{}, fmt.Errorf("%w: %s", ErrValidatorsUnavailable, why)
	}
	if !update || req.Script != "" {
		if err := checkValidatorScript(ctx, running, file, req.Script); err != nil {
			return CustomValidator{}, err
		}
	}

	blk := cfg.AssuranceBlock
	if blk == nil {
		blk = &config.AssuranceBlock{}
	}
	idx := -1
	for i, cv := range blk.Validators {
		if cv.Name == req.Name {
			idx = i
		}
	}
	switch {
	case update && idx < 0:
		return CustomValidator{}, fmt.Errorf("%w: %q", ErrValidatorNotFound, req.Name)
	case !update && idx >= 0:
		return CustomValidator{}, fmt.Errorf("%w: %q; edit it, or remove it first", ErrValidatorExists, req.Name)
	case !update && len(blk.Validators) >= config.MaxCustomValidators:
		return CustomValidator{}, fmt.Errorf("%w (%d)", ErrValidatorLimit, config.MaxCustomValidators)
	}
	scope, err := b.artifactsInScope(ctx, running, set)
	if err != nil {
		return CustomValidator{}, err
	}
	for _, other := range running.Assurance.Validators {
		if other.Name != req.Name && other.BackupSet == set && other.Pattern == req.Pattern {
			return CustomValidator{}, fmt.Errorf("%w: %s already has exactly this pattern at this scope", ErrValidatorConflict, other.Name)
		}
	}
	if c := collisions(running.Assurance.Validators, re, set, req.Name, scope); len(c) > 0 {
		return CustomValidator{}, fmt.Errorf("%w: %s also matches %s, and two validators at one scope for the same backup is an ambiguity; narrow one of the patterns", ErrValidatorConflict, c[0].Validator, c[0].Example)
	}

	entry := config.CustomValidator{Name: req.Name, Pattern: req.Pattern}
	if !set.IsZero() {
		entry.BackupSet = set.String()
	}
	if idx >= 0 {
		blk.Validators[idx] = entry
	} else {
		blk.Validators = append(blk.Validators, entry)
	}
	cfg.AssuranceBlock = blk

	var undo func() error
	if req.Script != "" {
		undo, err = writeValidatorScript(root, file, []byte(req.Script))
		if err != nil {
			return CustomValidator{}, err
		}
	}
	if err := b.persistConfig(cfg); err != nil {
		if undo != nil {
			_ = undo()
		}
		return CustomValidator{}, err
	}
	now := b.state.Load().inner.Config
	cv, _ := findResolvedValidator(now, req.Name)
	return b.describeValidator(now, root, cv, scope), nil
}

// RemoveCustomValidator takes a validator out of the configuration and then
// removes its script.
func (b *BackupService) RemoveCustomValidator(ctx context.Context, name string) (CustomValidator, error) {
	if b.configPath == "" {
		return CustomValidator{}, ErrConfigNotFileBacked
	}
	b.configMu.Lock()
	defer b.configMu.Unlock()

	cfg, err := config.Load(b.configPath)
	if err != nil {
		return CustomValidator{}, fmt.Errorf("service: re-reading configuration: %w", err)
	}
	running := b.state.Load().inner.Config
	cv, ok := findResolvedValidator(running, name)
	if !ok || cfg.AssuranceBlock == nil {
		return CustomValidator{}, fmt.Errorf("%w: %q", ErrValidatorNotFound, name)
	}
	root, _ := workflowRootFor(running)
	removed := b.describeValidator(running, root, cv, nil)

	kept := cfg.AssuranceBlock.Validators[:0:0]
	for _, e := range cfg.AssuranceBlock.Validators {
		if e.Name != name {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		kept = nil
	}
	cfg.AssuranceBlock.Validators = kept
	if cfg.AssuranceBlock.IsZero() {
		cfg.AssuranceBlock = nil
	}
	if err := b.persistConfig(cfg); err != nil {
		return CustomValidator{}, err
	}
	removeValidatorScripts(root, cv.ScriptFile)
	return removed, nil
}

// dropValidatorsForRemovedSet is RemoveBackupSet's half: a validator scoped
// to a set that no longer exists would make the file refuse to load, so it
// goes with the set. It returns the script files to remove once the
// configuration is written.
func dropValidatorsForRemovedSet(cfg *config.Config, id string) []string {
	dropped := cfg.AssuranceBlock.DropCustomValidatorsFor(id)
	if cfg.AssuranceBlock != nil && cfg.AssuranceBlock.IsZero() {
		cfg.AssuranceBlock = nil
	}
	files := make([]string, 0, len(dropped))
	for _, name := range dropped {
		if f, err := config.RecoveryTestScriptName(name); err == nil {
			files = append(files, f)
		}
	}
	return files
}

// removeValidatorScripts removes scripts the configuration no longer names.
// A failure leaves an inert file nothing selects, so it is not an error.
func removeValidatorScripts(root workflow.Root, files ...string) {
	if root.IsZero() {
		return
	}
	for _, f := range files {
		_ = os.Remove(filepath.Join(root.Path(), config.CustomValidatorDir, f))
	}
}

// writeValidatorScript writes body as dir/file atomically and returns how to
// put things back as they were.
func writeValidatorScript(root workflow.Root, file string, body []byte) (func() error, error) {
	dir := filepath.Join(root.Path(), config.CustomValidatorDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, unavailableWrite(err, dir)
	}
	// Created above or by an operator: either way it must be one discovery
	// will accept, or the upload would be a script nothing can run.
	if _, err := root.ResolveStage(config.CustomValidatorDir); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidatorsUnavailable, err)
	}
	target := filepath.Join(dir, file)
	previous, prevErr := os.ReadFile(target)

	tmp, err := os.CreateTemp(root.Path(), ".retnd-validator-*.tmp")
	if err != nil {
		return nil, unavailableWrite(err, root.Path())
	}
	tmpName := tmp.Name()
	fail := func(err error) (func() error, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, err
	}
	if err := tmp.Chmod(0o640); err != nil {
		return fail(fmt.Errorf("service: writing %s: %w", file, err))
	}
	if _, err := tmp.Write(body); err != nil {
		return fail(unavailableWrite(err, root.Path()))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("service: writing %s: %w", file, err))
	}
	if err := tmp.Close(); err != nil {
		return fail(fmt.Errorf("service: writing %s: %w", file, err))
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("service: writing %s: %w", file, err)
	}
	syncDir(dir)
	return func() error {
		if prevErr == nil {
			_, _ = writeValidatorScript(root, file, previous)
			return nil
		}
		_ = os.Remove(target)
		return nil
	}, nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// unavailableWrite turns a write the filesystem refused into the reason this
// deployment cannot keep validators, when that is what it is.
func unavailableWrite(err error, where string) error {
	if errors.Is(err, syscall.EROFS) || errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w: %s cannot be written by this process (%v); the workflow tree may be mounted read-only, which is the usual container layout, so put the script there yourself and declare it in config.yaml", ErrValidatorsUnavailable, where, err)
	}
	return fmt.Errorf("service: writing a validator script under %s: %w", where, err)
}

// parseArtifactID reads "source/set/name". The name may hold no "/", and
// neither may the set's two halves, so the split is unambiguous.
func parseArtifactID(s string) (model.ArtifactID, error) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 {
		return model.ArtifactID{}, fmt.Errorf("%q is not source/set/name", s)
	}
	set, err := model.NewBackupSetID(parts[0], parts[1])
	if err != nil {
		return model.ArtifactID{}, err
	}
	return model.NewArtifactID(set, parts[2])
}
