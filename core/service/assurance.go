package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
)

// Assurance configuration and the due list (EPIC Q, Q1.4 / #982).
//
// # Why these writes go through persistConfig
//
// A patch to the deployment-wide block has to be followed by config.Validate,
// or every set that inherits it goes on deciding under the old policy. That
// is how the retention override flags were once a silent no-op. persistConfig
// is the shared encode / validate / write / hot-reload tail, and it adopts
// the validated config, so the very next read of any inheriting set already
// sees the new policy, with no restart and no second step for a caller to
// forget.
//
// # What "zero clears" means
//
// Every field in a patch is a pointer, so omitted and cleared are different
// requests. A cleared field (a zero cadence, an empty string, a zero count)
// goes back to inheriting the default, which is what the field help says. It
// is never an error and never written into the file as a literal zero.

// Due bases: where an item's next-due time came from.
const (
	// DueBasisCopyRecord is the copy's own next_due_at (migration 0016), the
	// schedule the scrub driver keeps. It is the only clock there is.
	DueBasisCopyRecord = "copy_next_due_at"
	// DueBasisNever is a copy nobody has scheduled or looked at. It is due,
	// never fine.
	DueBasisNever = "never"
)

// AssurancePolicy is one resolved scrub and recovery-test policy.
type AssurancePolicy struct {
	Scrub        AssuranceScrubPolicy
	RecoveryTest AssuranceRecoveryTestPolicy
}

// AssuranceScrubPolicy is the scrub half.
type AssuranceScrubPolicy struct {
	Cadence  time.Duration
	Strategy string
}

// AssuranceRecoveryTestPolicy is the recovery-test half.
type AssuranceRecoveryTestPolicy struct {
	Mode        string
	Cadence     time.Duration
	ValidatorID string

	// BuiltinCheck is "structure" or "off" (#1111), RESOLVED.
	BuiltinCheck string
}

// AssuranceHostKnob is one global-only setting with its written answer to
// "can a backup set override this".
type AssuranceHostKnob struct {
	Name   string
	Value  string
	PerSet bool
	Reason string
}

// AssuranceSettings is the deployment-wide policy, resolved.
type AssuranceSettings struct {
	// Configured says the file carries an assurance block at all. False
	// means every value below is a default nobody had to set.
	Configured bool

	Policy AssurancePolicy

	StagingRoot            string
	StagingTTL             time.Duration
	MaxConcurrentFullReads int
	MaxPerDestination      int
	// MaxDownloadStreams is the ceiling on browser downloads this process
	// streams at once, as the engine runs it (the platform's where it set
	// one). Read only: a page offering several downloads offers no more.
	MaxDownloadStreams int

	HostKnobs []AssuranceHostKnob
}

// UpdateAssuranceSettingsRequest patches the deployment-wide block. A nil
// field is left alone; a zero value clears the field back to its default.
type UpdateAssuranceSettingsRequest struct {
	ScrubCadence  *time.Duration
	ScrubStrategy *string

	RecoveryTestMode         *string
	RecoveryTestCadence      *time.Duration
	RecoveryTestValidatorID  *string
	RecoveryTestBuiltinCheck *string

	StagingRoot            *string
	StagingTTL             *time.Duration
	MaxConcurrentFullReads *int
	MaxPerDestination      *int
}

func (r UpdateAssuranceSettingsRequest) isEmpty() bool {
	return r == UpdateAssuranceSettingsRequest{}
}

// BackupSetAssurance is one set's resolved policy.
type BackupSetAssurance struct {
	BackupSetID string

	// Override says the set carries its own assurance block. False means
	// every field below follows the deployment's.
	Override bool

	Policy AssurancePolicy

	// ValidatorDeclared is the engine's own answer to whether a recovery test
	// of this set would find a validator, from the dry run the test makes
	// first. It belongs to the set and not to the policy: the deployment-wide
	// policy has no set to look in.
	ValidatorDeclared bool

	// EffectiveCheck is which check a recovery test of this set would run
	// now: "validator", "builtin" or "none" (#1111), and for none the reason
	// in plain words.
	EffectiveCheck       string
	EffectiveCheckReason string
}

// UpdateBackupSetAssuranceRequest patches one set's override. A nil field is
// left alone; a zero value clears that field so it inherits again.
type UpdateBackupSetAssuranceRequest struct {
	ScrubCadence  *time.Duration
	ScrubStrategy *string

	RecoveryTestMode         *string
	RecoveryTestCadence      *time.Duration
	RecoveryTestValidatorID  *string
	RecoveryTestBuiltinCheck *string
}

func (r UpdateBackupSetAssuranceRequest) isEmpty() bool {
	return r == UpdateBackupSetAssuranceRequest{}
}

// AssuranceDueFilter narrows AssuranceDue.
type AssuranceDueFilter struct {
	// BackupSetID is a "source/set" id; empty means every configured set.
	BackupSetID string
	// DueOnly drops copies that are not yet due.
	DueOnly bool
	// OverdueOnly drops copies that are not overdue.
	OverdueOnly bool
}

// AssuranceDueItem is one copy's place in the scrub schedule.
type AssuranceDueItem struct {
	BackupSetID  string
	ArtifactID   string
	ArtifactName string

	// LastAssuredAt is zero for a copy nobody has looked at.
	LastAssuredAt time.Time
	// NextDueAt is when the resolved cadence next calls for a scrub. For a
	// never-assured copy it is the moment of the read.
	NextDueAt time.Time
	Cadence   time.Duration

	// Due is NextDueAt <= now. Overdue is a whole cadence past that, or never
	// assured. Both are computed from the set's RESOLVED policy, so a change
	// to the deployment's cadence moves every inheriting set's NextDueAt on
	// the very next read.
	Due     bool
	Overdue bool
	Basis   string
}

type dueSubject struct {
	ID   string
	Name string
	// LastAssured is when the copy last ran a check to a verdict, or zero.
	LastAssured time.Time
	// NextDue is the copy's own effective due time, or zero when the copy has
	// no schedule (which is due now).
	NextDue time.Time
}

// assuranceDueFor is the pure schedule arithmetic. It reads no clock, and it
// does not compute a due time: the due time is the copy's own, and this only
// classifies it against now and the cadence.
func assuranceDueFor(now time.Time, setID string, s dueSubject, cadence time.Duration) AssuranceDueItem {
	item := AssuranceDueItem{
		BackupSetID:   setID,
		ArtifactID:    s.ID,
		ArtifactName:  s.Name,
		LastAssuredAt: s.LastAssured,
		Cadence:       cadence,
		Basis:         DueBasisCopyRecord,
	}
	if s.NextDue.IsZero() {
		item.Basis = DueBasisNever
		item.NextDueAt = now
		item.Due, item.Overdue = true, true
		return item
	}
	item.NextDueAt = s.NextDue
	item.Due = !now.Before(item.NextDueAt)
	item.Overdue = !now.Before(item.NextDueAt.Add(cadence))
	return item
}

// scrubDueOf is when the artifact's earliest-due scrub target is next owed a
// check, under cadence, read from the copies themselves. The targets are the
// ones the driver checks (revalidate.ScrubTargets): the ACTIVE local copy
// when there is one, otherwise every ACTIVE copy on a medium. A target with
// no schedule makes the artifact due now.
func scrubDueOf(a Artifact, cadence time.Duration, now time.Time) dueSubject {
	subject := dueSubject{ID: a.ID, Name: a.Name}
	var targets []Placement
	for _, p := range a.Placements {
		if p.Status == state.PlacementActive && p.Medium == state.MediumLocal {
			targets = []Placement{p}
			break
		}
	}
	if targets == nil {
		for _, p := range a.Placements {
			if p.Status == state.PlacementActive {
				targets = append(targets, p)
			}
		}
	}
	first := true
	for _, p := range targets {
		var as state.Assurance
		as.AssuranceState = p.ScrubState
		if !p.ScrubNextDueAt.IsZero() {
			t := p.ScrubNextDueAt
			as.NextDueAt = &t
		}
		if !p.ScrubLastCheckedAt.IsZero() {
			t := p.ScrubLastCheckedAt
			as.LastCheckedAt = &t
		}
		at, _ := as.DueUnder(cadence, now)
		if first || at.Before(subject.NextDue) {
			subject.NextDue, subject.LastAssured = at, p.ScrubLastCheckedAt
			first = false
		}
	}
	return subject
}

var assuranceNow = time.Now

// effectiveScrubCadence guards the schedule against a Config that was built
// by hand and never validated: its resolved cadence is zero, and a zero must
// not read as "due continuously". The product default stands in.
func effectiveScrubCadence(resolved time.Duration) time.Duration {
	if resolved <= 0 {
		return config.DefaultScrubCadence
	}
	return resolved
}

// ValidateStagingRoot is the custody check on assurance.staging_root. The
// recovery engine creates and removes files under it, so a settings write
// can no longer point it just anywhere it can be written to: the path must be
// absolute and already clean, must not be a symbolic link or a file, and an
// existing directory must not be writable by anyone but its owner. A path
// that does not exist yet is accepted when the nearest ancestor that does is
// a directory, and the engine creates it 0700.
//
// Only the last component is held to the symlink rule. A mount point the
// operator declared is routinely a link higher up (a NAS volume), the same
// asymmetry the workflow root documents, and what the engine writes through
// is the directory itself. An empty path is the default and always valid.
func ValidateStagingRoot(p string) error {
	if p == "" {
		return nil
	}
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("%q must be an absolute, clean path (no .. and no trailing slash)", p)
	}
	info, err := os.Lstat(p)
	switch {
	case err == nil:
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%q is a symbolic link, and recovered copies are not staged through one", p)
		case !info.IsDir():
			return fmt.Errorf("%q exists and is not a directory", p)
		case info.Mode().Perm()&0o022 != 0:
			return fmt.Errorf("%q has permissions %04o, which lets another account swap a staged copy; use chmod go-w", p, info.Mode().Perm())
		}
		return nil
	case !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
		return fmt.Errorf("%q cannot be examined", p)
	}
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		ai, aerr := os.Stat(dir)
		if aerr == nil {
			if !ai.IsDir() {
				return fmt.Errorf("%q is under %q, which is not a directory", p, dir)
			}
			return nil
		}
		if dir == filepath.Dir(dir) {
			return fmt.Errorf("%q has no existing ancestor", p)
		}
	}
}

// scrubEligibleStates are the lifecycle states with a durable local copy to
// re-read, the same four internal/revalidate treats as known good.
var scrubEligibleStates = map[string]bool{
	string(lifecycle.Committed):           true,
	string(lifecycle.RemoteDeletePending): true,
	string(lifecycle.RemoteRetained):      true,
	string(lifecycle.Complete):            true,
}

// assuranceHostKnobs is the written "no". These are properties of one
// filesystem and one HTTP process, so a per-set override could not be
// honoured by either.
func assuranceHostKnobs(d config.AssuranceDefaults) []AssuranceHostKnob {
	const onePlace = "is a property of the one staging filesystem this process writes to; a per-set override could not be honoured by it"
	const oneProcess = "bounds one process on one host; a per-set value could not be enforced across sets that share it"
	// The engine builds its staging area and its gates once, the first time
	// this process needs them, and does not resize them under live grants.
	// Saying so here is the difference between a setting and a promise.
	const atStart = ". It is read once, when this process first needs it, so a change takes effect when the serving process next starts"
	return []AssuranceHostKnob{
		{Name: "staging_root", Value: d.StagingRoot, Reason: "the staging root " + onePlace + atStart},
		{Name: "staging_ttl", Value: d.StagingTTL.String(), Reason: "the staging TTL decides when leftovers in that one staging root are swept; " + onePlace + atStart},
		{Name: "max_concurrent_full_reads", Value: fmt.Sprint(d.MaxConcurrentFullReads), Reason: "the concurrency ceiling " + oneProcess + atStart},
		{Name: "max_per_destination", Value: fmt.Sprint(d.MaxPerDestination), Reason: "the per-destination ceiling " + oneProcess + atStart},
	}
}

func policyOf(a config.Assurance) AssurancePolicy {
	return AssurancePolicy{
		Scrub: AssuranceScrubPolicy{Cadence: a.Scrub.Cadence.Duration(), Strategy: a.Scrub.Strategy},
		RecoveryTest: AssuranceRecoveryTestPolicy{
			Mode: a.RecoveryTest.Mode, Cadence: a.RecoveryTest.Cadence.Duration(), ValidatorID: a.RecoveryTest.ValidatorID,
			BuiltinCheck: a.RecoveryTest.BuiltinCheck,
		},
	}
}

func assuranceSettingsOf(cfg *config.Config) AssuranceSettings {
	d := cfg.Assurance
	return AssuranceSettings{
		Configured:             cfg.AssuranceBlock != nil,
		Policy:                 policyOf(d.Assurance),
		StagingRoot:            d.StagingRoot,
		StagingTTL:             d.StagingTTL,
		MaxConcurrentFullReads: d.MaxConcurrentFullReads,
		MaxPerDestination:      d.MaxPerDestination,
		HostKnobs:              assuranceHostKnobs(d),
	}
}

// AssuranceSettings reports the resolved deployment-wide policy.
func (b *BackupService) AssuranceSettings(_ context.Context) (AssuranceSettings, error) {
	return b.withRecoveryCeilings(assuranceSettingsOf(b.state.Load().inner.Config)), nil
}

// UpdateAssuranceSettings patches the deployment-wide block and hot-reloads
// this service, so every inheriting set decides under the new policy on its
// next read.
func (b *BackupService) UpdateAssuranceSettings(_ context.Context, req UpdateAssuranceSettingsRequest) (AssuranceSettings, error) {
	if b.configPath == "" {
		return AssuranceSettings{}, ErrConfigNotFileBacked
	}
	if req.isEmpty() {
		return AssuranceSettings{}, fmt.Errorf("%w: this patch changes nothing; name at least one assurance setting", ErrInvalidRequest)
	}

	b.configMu.Lock()
	defer b.configMu.Unlock()

	cfg, err := config.Load(b.configPath)
	if err != nil {
		return AssuranceSettings{}, fmt.Errorf("service: re-reading configuration: %w", err)
	}
	blk := cfg.AssuranceBlock
	if blk == nil {
		blk = &config.AssuranceBlock{}
	}
	if req.ScrubCadence != nil {
		blk.Scrub.Cadence = config.Duration(*req.ScrubCadence)
	}
	if req.ScrubStrategy != nil {
		blk.Scrub.Strategy = strings.TrimSpace(*req.ScrubStrategy)
	}
	if req.RecoveryTestMode != nil {
		blk.RecoveryTest.Mode = strings.TrimSpace(*req.RecoveryTestMode)
	}
	if req.RecoveryTestCadence != nil {
		blk.RecoveryTest.Cadence = config.Duration(*req.RecoveryTestCadence)
	}
	if req.RecoveryTestValidatorID != nil {
		blk.RecoveryTest.ValidatorID = strings.TrimSpace(*req.RecoveryTestValidatorID)
	}
	if req.RecoveryTestBuiltinCheck != nil {
		blk.RecoveryTest.BuiltinCheck = strings.TrimSpace(*req.RecoveryTestBuiltinCheck)
	}
	if req.StagingRoot != nil {
		blk.StagingRoot = strings.TrimSpace(*req.StagingRoot)
		if err := ValidateStagingRoot(blk.StagingRoot); err != nil {
			return AssuranceSettings{}, fmt.Errorf("%w: assurance.staging_root: %v", ErrInvalidRequest, err)
		}
	}
	if req.StagingTTL != nil {
		blk.StagingTTL = config.Duration(*req.StagingTTL)
	}
	if req.MaxConcurrentFullReads != nil {
		blk.MaxConcurrentFullReads = *req.MaxConcurrentFullReads
	}
	if req.MaxPerDestination != nil {
		blk.MaxPerDestination = *req.MaxPerDestination
	}
	// A block that now says nothing is removed rather than written empty,
	// so clearing the last field returns the file to a config that never
	// mentioned assurance.
	if *blk == (config.AssuranceBlock{}) {
		cfg.AssuranceBlock = nil
	} else {
		cfg.AssuranceBlock = blk
	}

	if err := b.persistConfig(cfg); err != nil {
		return AssuranceSettings{}, err
	}
	return b.withRecoveryCeilings(assuranceSettingsOf(b.state.Load().inner.Config)), nil
}

// withRecoveryCeilings adds what only the running engine knows: the download
// ceiling the platform imposed, or the engine's default.
func (b *BackupService) withRecoveryCeilings(s AssuranceSettings) AssuranceSettings {
	s.MaxDownloadStreams = b.RecoveryLimits().MaxDownloadStreams
	return s
}

func backupSetAssuranceOf(id string, bs config.BackupSet) BackupSetAssurance {
	return BackupSetAssurance{BackupSetID: id, Override: bs.AssuranceIsOverride(), Policy: policyOf(bs.Assurance)}
}

// BackupSetAssurance reports one set's resolved policy.
func (b *BackupService) BackupSetAssurance(ctx context.Context, id string) (BackupSetAssurance, error) {
	st := b.state.Load()
	bs, err := lookupConfiguredBackupSet(st.inner.Config, id)
	if err != nil {
		return BackupSetAssurance{}, err
	}
	out := backupSetAssuranceOf(id, bs)
	out.ValidatorDeclared, err = st.inner.RecoveryTestValidatorDeclared(ctx, bs.ID)
	if err != nil {
		return BackupSetAssurance{}, err
	}
	out.EffectiveCheck, out.EffectiveCheckReason, err = st.inner.RecoveryTestEffectiveCheck(ctx, bs.ID)
	if err != nil {
		return BackupSetAssurance{}, err
	}
	return out, nil
}

// UpdateBackupSetAssurance patches one set's override and hot-reloads.
func (b *BackupService) UpdateBackupSetAssurance(ctx context.Context, id string, req UpdateBackupSetAssuranceRequest) (BackupSetAssurance, error) {
	if b.configPath == "" {
		return BackupSetAssurance{}, ErrConfigNotFileBacked
	}
	if req.isEmpty() {
		return BackupSetAssurance{}, fmt.Errorf("%w: this patch changes nothing; name at least one of scrub cadence, scrub strategy, recovery-test mode, cadence or validator", ErrInvalidRequest)
	}

	b.configMu.Lock()
	defer b.configMu.Unlock()

	cfg, err := config.Load(b.configPath)
	if err != nil {
		return BackupSetAssurance{}, fmt.Errorf("service: re-reading configuration: %w", err)
	}
	bs, err := findBackupSetForWrite(cfg, id)
	if err != nil {
		return BackupSetAssurance{}, err
	}
	o := bs.AssuranceConfig
	if o == nil {
		o = &config.AssuranceOverride{}
	}
	if req.ScrubCadence != nil {
		o.Scrub.Cadence = config.Duration(*req.ScrubCadence)
	}
	if req.ScrubStrategy != nil {
		o.Scrub.Strategy = strings.TrimSpace(*req.ScrubStrategy)
	}
	if req.RecoveryTestMode != nil {
		o.RecoveryTest.Mode = strings.TrimSpace(*req.RecoveryTestMode)
	}
	if req.RecoveryTestCadence != nil {
		o.RecoveryTest.Cadence = config.Duration(*req.RecoveryTestCadence)
	}
	if req.RecoveryTestValidatorID != nil {
		o.RecoveryTest.ValidatorID = strings.TrimSpace(*req.RecoveryTestValidatorID)
	}
	if req.RecoveryTestBuiltinCheck != nil {
		o.RecoveryTest.BuiltinCheck = strings.TrimSpace(*req.RecoveryTestBuiltinCheck)
	}
	// Clearing the last named field is a reset: config.Validate refuses an
	// empty block in words, so it is removed rather than written.
	if *o == (config.AssuranceOverride{}) {
		bs.AssuranceConfig = nil
	} else {
		bs.AssuranceConfig = o
	}

	if err := b.persistConfig(cfg); err != nil {
		return BackupSetAssurance{}, err
	}
	return b.BackupSetAssurance(ctx, id)
}

// ResetBackupSetAssurance removes the set's override so it inherits the
// deployment's policy again. Resetting a set that already inherits is not an
// error: the end state is what was asked for.
func (b *BackupService) ResetBackupSetAssurance(ctx context.Context, id string) (BackupSetAssurance, error) {
	if b.configPath == "" {
		return BackupSetAssurance{}, ErrConfigNotFileBacked
	}
	b.configMu.Lock()
	defer b.configMu.Unlock()

	cfg, err := config.Load(b.configPath)
	if err != nil {
		return BackupSetAssurance{}, fmt.Errorf("service: re-reading configuration: %w", err)
	}
	bs, err := findBackupSetForWrite(cfg, id)
	if err != nil {
		return BackupSetAssurance{}, err
	}
	bs.AssuranceConfig = nil
	if err := b.persistConfig(cfg); err != nil {
		return BackupSetAssurance{}, err
	}
	return b.BackupSetAssurance(ctx, id)
}

// AssuranceDue lists every artifact's place in the scrub schedule, most
// overdue first.
//
// The due time is read from the copies themselves (their own next_due_at, the
// schedule the scrub driver keeps), judged under each set's RESOLVED cadence.
// It used to be recomputed from the newest verified_at or the artifact row's
// updated_at, which is a second clock the driver never followed and that
// migration 0016 exists to retire.
func (b *BackupService) AssuranceDue(ctx context.Context, filter AssuranceDueFilter) ([]AssuranceDueItem, error) {
	cfg := b.state.Load().inner.Config
	now := assuranceNow()

	var ids []string
	for _, src := range cfg.Sources {
		for _, bs := range src.BackupSets {
			id := src.Name + "/" + bs.Name
			if filter.BackupSetID == "" || filter.BackupSetID == id {
				ids = append(ids, id)
			}
		}
	}
	if filter.BackupSetID != "" && len(ids) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrBackupSetNotFound, filter.BackupSetID)
	}

	out := []AssuranceDueItem{}
	for _, id := range ids {
		bs, err := lookupConfiguredBackupSet(cfg, id)
		if err != nil {
			return nil, err
		}
		arts, err := b.ListArtifacts(ctx, ArtifactFilter{BackupSetID: id})
		if err != nil {
			return nil, err
		}
		for _, a := range arts {
			if !scrubEligibleStates[a.State] {
				continue
			}
			cadence := effectiveScrubCadence(bs.Assurance.Scrub.Cadence.Duration())
			item := assuranceDueFor(now, id, scrubDueOf(a, cadence, now), cadence)
			if (filter.DueOnly && !item.Due) || (filter.OverdueOnly && !item.Overdue) {
				continue
			}
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].NextDueAt.Equal(out[j].NextDueAt) {
			return out[i].NextDueAt.Before(out[j].NextDueAt)
		}
		return out[i].ArtifactID < out[j].ArtifactID
	})
	return out, nil
}
