package service

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
// (see validatorStore.Reconcile); the second is the window that pinning
// script_sha256 in the configuration would close, and is a follow-up.
//
// The script store itself (how a file is written, where it goes when it leaves)
// is validatorstore.go, the reads are validatorreads.go, and the rules about
// which pattern matches which name are core/internal/config.

// MaxCustomValidatorScriptBytes bounds an uploaded script. A deployment's own
// workflows.max_script_size_bytes binds too when it is smaller.
const MaxCustomValidatorScriptBytes = 256 << 10

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

// SuggestArtifactPattern generalises an artifact name into the anchored RE2
// pattern an operator would otherwise write by hand (config.SuggestArtifactPattern).
func SuggestArtifactPattern(name string) string { return config.SuggestArtifactPattern(name) }

// DefaultCustomValidatorScript is the documented default script an operator
// downloads to read and adapt.
func DefaultCustomValidatorScript() ValidatorScript {
	return validatorScriptOf(DefaultValidatorFileName, []byte(defaultValidatorScript))
}

// The states a validator's script is in, as the list reports them (#1222).
const (
	// ScriptStatePinned: the file hashes to what config.yaml pins. This is
	// the state every upload and edit leaves.
	ScriptStatePinned = "pinned"
	// ScriptStateUnpinned: config.yaml pins nothing, because the entry was
	// written before pinning or by hand. The script runs as it always did and
	// the next save of the validator pins it.
	ScriptStateUnpinned = "unpinned"
	// ScriptStateEdited: config.yaml pins a hash and the file no longer has
	// it. A recovery test that would use this validator ends could_not_complete
	// without running the script, and an edit cannot keep it by hash: sending
	// the script is the way to approve what is on disk.
	ScriptStateEdited = "edited_on_disk"
	// ScriptStateMissing: the file is not there (or cannot be read).
	ScriptStateMissing = "missing"
)

// scriptStateOf is the state of a script given what is on disk and what the
// entry pins. found is "" when the file is not there.
func scriptStateOf(present bool, found, pinned string) string {
	switch {
	case !present:
		return ScriptStateMissing
	case pinned == "":
		return ScriptStateUnpinned
	case found == pinned:
		return ScriptStatePinned
	default:
		return ScriptStateEdited
	}
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
	// ScriptSHA256 is the hash of the bytes in the file now.
	ScriptSHA256 string
	ScriptSize   int64
	UploadedAt   time.Time

	// ScriptPinnedSHA256 is the hash config.yaml pins for this validator
	// (script_sha256, #1222), "" for an entry that has none.
	ScriptPinnedSHA256 string
	// ScriptState is ScriptStatePinned, ScriptStateUnpinned,
	// ScriptStateEdited or ScriptStateMissing.
	ScriptState string

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
	store := newValidatorStore(root)

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
	c, err := config.FindCollisions(ctx, running.Assurance.Validators, re, set, req.Name, setNames(scope))
	if err != nil {
		return CustomValidator{}, err
	}
	if len(c) > 0 {
		return CustomValidator{}, fmt.Errorf("%w: %s also matches %s, and two validators at one scope for the same backup is an ambiguity; narrow one of the patterns", ErrValidatorConflict, c[0].Validator, c[0].Example)
	}
	// What config.yaml pins for this name now, before the entry is replaced.
	previousPin := ""
	if idx >= 0 {
		previousPin = blk.Validators[idx].ScriptSHA256
	}
	pin := ""
	if req.Script != "" {
		pin = validatorScriptOf(file, []byte(req.Script)).SHA256
	}
	if update && req.ScriptSHA256 != "" {
		// Keeping a script by its hash means the script now stored is that
		// one. The bytes are read and hashed here, never taken from the
		// list's cache: the cache is a convenience for a display, and this
		// is the check that stops an edit from keeping somebody else's code.
		stored, err := store.Read(file, running.EffectiveMaxScriptSize())
		var found string
		if err == nil {
			found = validatorScriptOf(file, stored).SHA256
		}
		switch {
		case errors.Is(err, os.ErrNotExist):
			return CustomValidator{}, fmt.Errorf("%w: no script is stored for %q to keep; send the script", ErrValidatorScriptChanged, req.Name)
		case err != nil:
			return CustomValidator{}, fmt.Errorf("service: reading the stored script of %q: %w", req.Name, err)
		case previousPin != "" && found != previousPin:
			// The list reports the hash of what is on disk, so a client that
			// echoes it back would approve a script nobody has read. Keeping
			// is for the script that was approved; approving a different one
			// means sending it.
			return CustomValidator{}, fmt.Errorf("%w: %q was edited on disk (config.yaml pins sha256 %s and the file holds %s); keeping it would approve code nobody has read, so send the script to approve it", ErrValidatorScriptChanged, req.Name, previousPin, found)
		case found != req.ScriptSHA256:
			return CustomValidator{}, fmt.Errorf("%w: %q now holds a different script than the one this edit was made against; read it again and decide", ErrValidatorScriptChanged, req.Name)
		}
		pin = found
	}

	entry := config.CustomValidator{Name: req.Name, Pattern: req.Pattern, ScriptSHA256: pin}
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
		if err := store.Reconcile(declaredScriptFiles(blk.Validators)); err != nil {
			return CustomValidator{}, err
		}
		if !update {
			// Files, not declarations: this is the bound discovery enforces.
			if n := store.EntryCount(); n >= config.MaxCustomValidators {
				return CustomValidator{}, fmt.Errorf("%w (%d): the stage directory already holds %d files", ErrValidatorLimit, config.MaxCustomValidators, n)
			}
		}
		if undo, err = store.Write(file, []byte(req.Script)); err != nil {
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
	out, err := describeValidator(context.WithoutCancel(ctx), now, store, cv, scope)
	if err != nil {
		return CustomValidator{}, err
	}
	event := eventValidatorUploaded
	msg := "custom validator uploaded"
	if update {
		event, msg = eventValidatorUpdated, "custom validator replaced"
	}
	b.logger.Event(ctx, obs.LevelInfo, event, msg, append(validatorEventAttrs(out),
		slog.Bool("script_replaced", req.Script != ""), slog.String("previous_pinned_sha256", previousPin))...)
	return out, nil
}

// validatorEventAttrs is what a write records about a validator: who it is,
// where it applies and which script, by hash and size and never by body.
func validatorEventAttrs(v CustomValidator) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("name", v.Name), slog.String("scope", v.Scope), slog.String("pattern", v.Pattern),
		slog.String("script_sha256", v.ScriptSHA256), slog.Int64("script_size_bytes", v.ScriptSize),
		slog.String("pinned_sha256", v.ScriptPinnedSHA256), slog.String("script_state", v.ScriptState),
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
	removed, err := describeValidator(context.WithoutCancel(ctx), running, newValidatorStore(root), cv, nil)
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
		if err := newValidatorStore(root).MoveAside(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			b.logger.Event(ctx, obs.LevelWarn, "validator_script_not_moved", "a removed validator's script could not be moved out of the stage directory",
				slog.String("script_file", f), slog.String("error", err.Error()))
		}
	}
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
