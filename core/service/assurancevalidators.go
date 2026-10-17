package service

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
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
// disagree for longer than this call, and a rollback that itself fails is
// said so and logged, never swallowed. Removal goes the other way: the entry
// leaves the configuration first, so a script whose removal fails is an
// inert file nothing selects, never a validator whose script is gone.
//
// A crash between the script's rename and the configuration write leaves a
// script the configuration does not name (an orphan) or, on an edit, a new
// script under the old pattern. The first is moved aside by the next write
// (see reconcileValidatorDir); the second is the window that pinning
// script_sha256 in the configuration would close, and is a follow-up.
//
// # Nothing is deleted
//
// A script that leaves the stage directory (a removal, a set removed with
// its validators, an orphan) is renamed into a sibling directory,
// recovery-test-validators.superseded, under its own name or with a numeric
// suffix when that name is taken. The sibling is not a stage directory, so
// discovery never reads it, and it is not inside the one that is, so it does
// not count against the 64 entry limit.
//
// # What a read costs
//
// A list or a preview matches every artifact name in scope against every
// pattern. The patterns are bounded where they are accepted
// (config.CompileValidatorPattern); the scan checks the request's context as
// it goes; script hashes are cached by (path, modification time, size); and
// a write scans the journal before it takes the configuration lock, so a slow
// scan never holds up another configuration change.

// MaxCustomValidatorScriptBytes bounds an uploaded script. A deployment's own
// workflows.max_script_size_bytes binds too when it is smaller.
const MaxCustomValidatorScriptBytes = 256 << 10

// validatorScanHook is a test seam: it is called with "start" before a
// journal scan and "listed" after each set's backups were read.
var validatorScanHook func(stage string, set model.BackupSetID)

// supersededSuffix names the sibling directory scripts are moved into.
const supersededSuffix = ".superseded"

// The audit lines the writes leave. Never a script's body: its hash and size
// say which script, and the body is not for a log.
const (
	eventValidatorUploaded = "validator_uploaded"
	eventValidatorUpdated  = "validator_updated"
	eventValidatorRemoved  = "validator_removed"
)

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

	// Env is the environment a recovery test gives the script (#1221):
	// minimal or workflow, the validator's own setting when it has one and
	// otherwise what its set, then the deployment, then the default says.
	Env string

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
	// Env is minimal or workflow (#1221), the environment the script starts
	// with. Empty is minimal, on an update too: an update replaces the
	// validator whole, so leaving it out never keeps what was there.
	Env string
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
			va := validatorArtifacts{set: bs.ID, last: map[string]CustomValidatorResult{}}
			if b.journal != nil {
				records, err := b.journal.ListByBackupSet(ctx, bs.ID)
				if err != nil {
					return nil, fmt.Errorf("service: reading %s's backups: %w", bs.ID, err)
				}
				if validatorScanHook != nil {
					validatorScanHook("listed", bs.ID)
				}
				if err := ctx.Err(); err != nil {
					return nil, err
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

func (b *BackupService) describeValidator(ctx context.Context, cfg *config.Config, root workflow.Root, cv config.ResolvedCustomValidator, scope []validatorArtifacts) (CustomValidator, error) {
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
			if sha, ok := cachedHash(path, info, maxValidatorScriptBytes(cfg)); ok {
				v.ScriptPresent, v.ScriptSHA256, v.ScriptSize = true, sha, info.Size()
			}
		}
	}
	for _, va := range scope {
		if !cv.AppliesTo(va.set) {
			continue
		}
		n := 0
		for i, name := range va.names {
			if i&0xff == 0 {
				if err := ctx.Err(); err != nil {
					return CustomValidator{}, err
				}
			}
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
	return v, nil
}

// ambiguitiesIn finds the backups in scope that more than one validator
// matches at one scope, one entry per (set, validators), with the newest
// backup as the example. A recovery test of such a backup refuses to choose,
// and creation cannot have seen it coming when the backup landed later.
func ambiguitiesIn(ctx context.Context, list []config.ResolvedCustomValidator, scope []validatorArtifacts) ([]CustomValidatorAmbiguity, error) {
	out := []CustomValidatorAmbiguity{}
	if len(list) < 2 {
		return out, nil
	}
	seen := map[string]bool{}
	for _, va := range scope {
		for i, name := range va.names {
			if i&0xff == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			m := config.MatchCustomValidator(list, va.set, name)
			if len(m.Ambiguous) == 0 {
				continue
			}
			key := va.set.String() + "|" + strings.Join(m.Ambiguous, ",")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, CustomValidatorAmbiguity{BackupSetID: va.set.String(), Validators: m.Ambiguous, Example: va.set.String() + "/" + name})
		}
	}
	return out, nil
}

// errScriptTooBig is readBounded's refusal of a file over its limit.
var errScriptTooBig = errors.New("larger than its limit")

// readBounded reads path, refusing one over limit bytes. It allocates what the
// file holds, never the limit: a list reads every declared script, and the
// limit is a ceiling and not a size.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is %w (%d bytes)", path, errScriptTooBig, limit)
	}
	return b, nil
}

// scriptHashes caches a script's hash by the identity of the file it was
// computed from, so a list does not re-read every script every time. A file
// rewritten with the same size inside the same timestamp tick would be
// missed; every write this process makes changes the time, and an operator's
// edit changes it too. An edit that means to KEEP a script never trusts this:
// it hashes the bytes it reads.
var scriptHashes = struct {
	sync.Mutex
	m map[string]cachedScriptHash
}{m: map[string]cachedScriptHash{}}

type cachedScriptHash struct {
	mtime int64
	size  int64
	sha   string
}

func cachedHash(path string, info os.FileInfo, limit int64) (sha string, ok bool) {
	key := info.ModTime().UnixNano()
	scriptHashes.Lock()
	c, hit := scriptHashes.m[path]
	scriptHashes.Unlock()
	if hit && c.mtime == key && c.size == info.Size() {
		return c.sha, true
	}
	body, err := readBounded(path, limit)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(body)
	sha = hex.EncodeToString(sum[:])
	scriptHashes.Lock()
	if len(scriptHashes.m) >= 4*config.MaxCustomValidators {
		clear(scriptHashes.m)
	}
	scriptHashes.m[path] = cachedScriptHash{mtime: key, size: int64(len(body)), sha: sha}
	scriptHashes.Unlock()
	return sha, true
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
	out.DirectoryRefused = directoryRefusal(root)
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
		v, err := b.describeValidator(ctx, cfg, root, cv, scope)
		if err != nil {
			return CustomValidators{}, err
		}
		out.Validators = append(out.Validators, v)
	}
	if out.Ambiguities, err = ambiguitiesIn(ctx, ordered, scope); err != nil {
		return CustomValidators{}, err
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

// directoryRefusal says why the stage directory cannot be used, or "" when it
// can, is not there yet, or there is no root to look under.
func directoryRefusal(root workflow.Root) string {
	if root.IsZero() {
		return ""
	}
	if _, err := os.Lstat(filepath.Join(root.Path(), config.CustomValidatorDir)); err != nil {
		return ""
	}
	if _, err := root.ResolveStage(config.CustomValidatorDir); err != nil {
		return strings.TrimPrefix(err.Error(), "workflow: ")
	}
	return ""
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
func collisions(ctx context.Context, list []config.ResolvedCustomValidator, re *regexp.Regexp, set model.BackupSetID, skip string, scope []validatorArtifacts) ([]CustomValidatorConflict, error) {
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
			for i, name := range va.names {
				if i&0xff == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
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
	return out, nil
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
		for i, name := range va.names {
			if i&0xff == 0 {
				if err := ctx.Err(); err != nil {
					return CustomValidatorPreview{}, err
				}
			}
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
	c, err := collisions(ctx, cfg.Assurance.Validators, re, set, replacing, scope)
	if err != nil {
		return CustomValidatorPreview{}, err
	}
	if c != nil {
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
		for i, name := range va.names {
			if i&0xff == 0 {
				if err := ctx.Err(); err != nil {
					return CustomValidatorPatterns{}, err
				}
			}
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

// UpdateCustomValidator replaces a validator whole: its pattern, its scope
// and its script, which the request either carries (Script) or says to keep
// by hash (ScriptSHA256). Nothing is inferred from a field left out.
func (b *BackupService) UpdateCustomValidator(ctx context.Context, req CreateCustomValidatorRequest) (CustomValidator, error) {
	return b.writeCustomValidator(ctx, req, true)
}

func invalidValidator(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, args...)...)
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// declaredScriptFiles is the set of script file names the configuration
// names, folded for comparison on a case-insensitive volume.
func declaredScriptFiles(list []config.CustomValidator) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, cv := range list {
		if f, err := config.RecoveryTestScriptName(cv.Name); err == nil {
			out[strings.ToLower(f)] = true
		}
	}
	return out
}

func (b *BackupService) writeCustomValidator(ctx context.Context, req CreateCustomValidatorRequest, update bool) (CustomValidator, error) {
	if b.configPath == "" {
		return CustomValidator{}, ErrConfigNotFileBacked
	}
	req.Name = strings.TrimSpace(req.Name)
	req.BackupSetID = strings.TrimSpace(req.BackupSetID)
	req.ScriptSHA256 = strings.ToLower(strings.TrimSpace(req.ScriptSHA256))
	file, err := config.RecoveryTestScriptName(req.Name)
	if err != nil {
		return CustomValidator{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	re, err := config.CompileValidatorPattern(req.Pattern)
	if err != nil {
		return CustomValidator{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if update {
		switch {
		case req.Script == "" && req.ScriptSHA256 == "":
			return CustomValidator{}, invalidValidator("an edit replaces the whole validator, so it has to say what becomes of the script: send the new script, or script_sha256 to keep the stored one")
		case req.Script != "" && req.ScriptSHA256 != "":
			return CustomValidator{}, invalidValidator("send the new script or script_sha256 to keep the stored one, not both")
		case req.ScriptSHA256 != "" && !isSHA256Hex(req.ScriptSHA256):
			return CustomValidator{}, invalidValidator("script_sha256 is not a SHA-256 in hex (64 characters)")
		}
	}

	// Everything that reads, and can be slow, happens before the
	// configuration lock: the scope, the script lint and the journal scan. A
	// scan of a large journal under configMu would hold up every other
	// configuration change for as long as it took. The scan is judged
	// against the configuration as it was read here; the checks that must
	// be true at the moment of the write are repeated under the lock.
	running := b.state.Load().inner.Config
	set, err := b.parseScopeSet(running, req.BackupSetID)
	if err != nil {
		return CustomValidator{}, err
	}
	if _, why := workflowRootFor(running); why != "" {
		return CustomValidator{}, fmt.Errorf("%w: %s", ErrValidatorsUnavailable, why)
	}
	if !update || req.Script != "" {
		if err := checkValidatorScript(ctx, running, file, req.Script); err != nil {
			return CustomValidator{}, err
		}
	}
	scope, err := b.artifactsInScope(ctx, running, set)
	if err != nil {
		return CustomValidator{}, err
	}

	b.configMu.Lock()
	defer b.configMu.Unlock()

	cfg, err := config.Load(b.configPath)
	if err != nil {
		return CustomValidator{}, fmt.Errorf("service: re-reading configuration: %w", err)
	}
	// The running config has every id resolved; the file read above does
	// not, so scope and collisions are judged against the running one.
	running = b.state.Load().inner.Config
	if set, err = b.parseScopeSet(running, req.BackupSetID); err != nil {
		return CustomValidator{}, err
	}
	root, why := workflowRootFor(running)
	if why != "" {
		return CustomValidator{}, fmt.Errorf("%w: %s", ErrValidatorsUnavailable, why)
	}

	blk := cfg.AssuranceBlock
	if blk == nil {
		blk = &config.AssuranceBlock{}
	}
	idx, sameFolded := -1, ""
	for i, cv := range blk.Validators {
		switch {
		case cv.Name == req.Name:
			idx = i
		case strings.EqualFold(cv.Name, req.Name):
			sameFolded = cv.Name
		}
	}
	switch {
	case update && idx < 0:
		return CustomValidator{}, fmt.Errorf("%w: %q", ErrValidatorNotFound, req.Name)
	case !update && idx >= 0:
		return CustomValidator{}, fmt.Errorf("%w: %q; edit it, or remove it first", ErrValidatorExists, req.Name)
	case !update && sameFolded != "":
		return CustomValidator{}, fmt.Errorf("%w: %q differs from the existing %q only by case, and on a case-insensitive volume they would be one script; pick another name", ErrValidatorExists, req.Name, sameFolded)
	case !update && len(blk.Validators) >= config.MaxCustomValidators:
		return CustomValidator{}, fmt.Errorf("%w (%d)", ErrValidatorLimit, config.MaxCustomValidators)
	}
	for _, other := range running.Assurance.Validators {
		if other.Name != req.Name && other.BackupSet == set && other.Pattern == req.Pattern {
			return CustomValidator{}, fmt.Errorf("%w: %s already has exactly this pattern at this scope", ErrValidatorConflict, other.Name)
		}
	}
	c, err := collisions(ctx, running.Assurance.Validators, re, set, req.Name, scope)
	if err != nil {
		return CustomValidator{}, err
	}
	if len(c) > 0 {
		return CustomValidator{}, fmt.Errorf("%w: %s also matches %s, and two validators at one scope for the same backup is an ambiguity; narrow one of the patterns", ErrValidatorConflict, c[0].Validator, c[0].Example)
	}
	if update && req.ScriptSHA256 != "" {
		// Keeping a script by its hash means the script now stored is that
		// one. The bytes are read and hashed here, never taken from the
		// list's cache: the cache is a convenience for a display, and this
		// is the check that stops an edit from keeping somebody else's code.
		stored, err := readBounded(filepath.Join(root.Path(), config.CustomValidatorDir, file), running.EffectiveMaxScriptSize())
		switch {
		case errors.Is(err, os.ErrNotExist):
			return CustomValidator{}, fmt.Errorf("%w: no script is stored for %q to keep; send the script", ErrValidatorScriptChanged, req.Name)
		case err != nil:
			return CustomValidator{}, fmt.Errorf("service: reading the stored script of %q: %w", req.Name, err)
		case validatorScriptOf(file, stored).SHA256 != req.ScriptSHA256:
			return CustomValidator{}, fmt.Errorf("%w: %q now holds a different script than the one this edit was made against; read it again and decide", ErrValidatorScriptChanged, req.Name)
		}
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
		// Anything in the stage directory that the configuration does not
		// name goes aside first (an interrupted removal, an editor's backup,
		// a hand copy): it counts against discovery's 64 entry limit and,
		// for the other stages that read the directory whole, one stray file
		// refuses everything in it.
		if err := reconcileValidatorDir(root, declaredScriptFiles(blk.Validators)); err != nil {
			return CustomValidator{}, err
		}
		if !update {
			// Files, not declarations: this is the bound discovery enforces.
			if n := stageEntryCount(root); n >= config.MaxCustomValidators {
				return CustomValidator{}, fmt.Errorf("%w (%d): the stage directory already holds %d files", ErrValidatorLimit, config.MaxCustomValidators, n)
			}
		}
		if undo, err = writeValidatorScript(root, file, []byte(req.Script)); err != nil {
			return CustomValidator{}, err
		}
	}
	if err := b.persistConfig(cfg); err != nil {
		if undo != nil {
			if uerr := undo(); uerr != nil {
				b.logger.Event(ctx, obs.LevelError, "validator_rollback_failed", "a custom validator's script could not be put back after its configuration write failed",
					slog.String("name", req.Name), slog.String("script_file", file), slog.String("error", uerr.Error()))
				return CustomValidator{}, fmt.Errorf("%w; and the script written for it could not be put back (%v), so %s may not be the script config.yaml expects", err, uerr, file)
			}
		}
		return CustomValidator{}, err
	}
	now := b.state.Load().inner.Config
	cv, _ := findResolvedValidator(now, req.Name)
	// The write has happened: a client that went away must not turn it into
	// an error, so describing the result no longer listens to ctx.
	out, err := b.describeValidator(context.WithoutCancel(ctx), now, root, cv, scope)
	if err != nil {
		return CustomValidator{}, err
	}
	event := eventValidatorUploaded
	msg := "custom validator uploaded"
	if update {
		event, msg = eventValidatorUpdated, "custom validator replaced"
	}
	b.logger.Event(ctx, obs.LevelInfo, event, msg, append(validatorEventAttrs(out), slog.Bool("script_replaced", req.Script != ""))...)
	return out, nil
}

// validatorEventAttrs is what a write records about a validator: who it is,
// where it applies and which script, by hash and size and never by body.
func validatorEventAttrs(v CustomValidator) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("name", v.Name), slog.String("scope", v.Scope), slog.String("pattern", v.Pattern),
		slog.String("script_sha256", v.ScriptSHA256), slog.Int64("script_size_bytes", v.ScriptSize),
	}
	if v.BackupSetID != "" {
		attrs = append(attrs, slog.String("backup_set", v.BackupSetID))
	}
	return attrs
}

// RemoveCustomValidator takes a validator out of the configuration and then
// moves its script aside (recovery-test-validators.superseded); nothing is
// deleted.
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
	removed, err := b.describeValidator(context.WithoutCancel(ctx), running, root, cv, nil)
	if err != nil {
		return CustomValidator{}, err
	}

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
	b.moveValidatorScriptsAside(ctx, root, cv.ScriptFile)
	b.logger.Event(ctx, obs.LevelInfo, eventValidatorRemoved, "custom validator removed", validatorEventAttrs(removed)...)
	return removed, nil
}

// dropValidatorsForRemovedSet is RemoveBackupSet's half: a validator scoped
// to a set that no longer exists would make the file refuse to load, so it
// goes with the set. It returns the script files to move aside once the
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

// moveValidatorScriptsAside moves scripts the configuration no longer names
// out of the stage directory. A failure leaves an inert file nothing selects,
// so it is not an error to the caller, but it is not silent either.
func (b *BackupService) moveValidatorScriptsAside(ctx context.Context, root workflow.Root, files ...string) {
	if root.IsZero() {
		return
	}
	for _, f := range files {
		if err := moveValidatorAside(root, f); err != nil && !errors.Is(err, os.ErrNotExist) {
			b.logger.Event(ctx, obs.LevelWarn, "validator_script_not_moved", "a removed validator's script could not be moved out of the stage directory",
				slog.String("script_file", f), slog.String("error", err.Error()))
		}
	}
}

// moveValidatorAside renames one file out of the stage directory into its
// .superseded sibling, under its own name or the first free numeric suffix.
func moveValidatorAside(root workflow.Root, file string) error {
	src := filepath.Join(root.Path(), config.CustomValidatorDir, file)
	aside := filepath.Join(root.Path(), config.CustomValidatorDir+supersededSuffix)
	if _, err := os.Lstat(src); err != nil {
		return err
	}
	if err := os.MkdirAll(aside, 0o750); err != nil {
		return unavailableWrite(err, aside)
	}
	for n := 0; n < 10000; n++ {
		dst := filepath.Join(aside, file)
		if n > 0 {
			dst += "." + strconv.Itoa(n)
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
		syncDir(aside)
		syncDir(filepath.Dir(src))
		return nil
	}
	return fmt.Errorf("service: %s already has 10000 superseded copies of %s", aside, file)
}

// reconcileValidatorDir moves every entry the configuration does not name out
// of the stage directory. A directory that is not there yet has nothing to
// reconcile.
func reconcileValidatorDir(root workflow.Root, declared map[string]bool) error {
	dir := filepath.Join(root.Path(), config.CustomValidatorDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return unavailableWrite(err, dir)
	}
	for _, e := range entries {
		if declared[strings.ToLower(e.Name())] {
			continue
		}
		if err := moveValidatorAside(root, e.Name()); err != nil {
			return fmt.Errorf("service: moving %s, which config.yaml does not name, out of %s: %w", e.Name(), config.CustomValidatorDir, err)
		}
	}
	return nil
}

// stageEntryCount is how many entries the stage directory holds, the number
// discovery's own limit is about.
func stageEntryCount(root workflow.Root) int {
	entries, err := os.ReadDir(filepath.Join(root.Path(), config.CustomValidatorDir))
	if err != nil {
		return 0
	}
	return len(entries)
}

// writeValidatorScript writes body as dir/file atomically and returns how to
// put things back as they were. The put-back reports its failure: a rollback
// that did not happen is something the caller has to say.
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
	previous, prevErr := readBounded(target, MaxCustomValidatorScriptBytes)
	if errors.Is(prevErr, errScriptTooBig) {
		return nil, invalidValidator("the stored %s is over %d bytes, so it cannot be kept to put back if the configuration write fails; remove the validator and add it again", file, MaxCustomValidatorScriptBytes)
	}

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
			_, err := writeValidatorScript(root, file, previous)
			return err
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
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
