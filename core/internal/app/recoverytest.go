package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/recoverytest"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC Q, #980: the recovery-test runner as the application sees it.
//
// internal/recoverytest does the work; this file resolves a backup set's own
// configuration into its inputs and hands it a journal.
//
// # The validator is a workflow script
//
// The operator's validator is a NAME.local.sh script in
// <workflow root>/recovery-test/<source>/<set>, run through the stage runner
// (stage.go) with EPIC L's machinery: custody, naming, bytes read once and
// hashed, shell verification, the host runner's container, the resolved
// environment. This product adds no execution boundary for it.
//
// It is told about the recovered copy through ordinary variables:
//
//	RECOVERY_TEST_FILE       the recovered bytes, a path the runner can read
//	RECOVERY_TEST_ARTIFACT   the artifact id
//	RECOVERY_TEST_MEDIUM     the copy that was recovered ("local" or a medium)
//	RECOVERY_TEST_DIGEST     the SHA-256 the file already matched
//	RECOVERY_TEST_SIZE       its length in bytes
//
// The path is only useful if the runner's container can see it. Hook
// containers get no host path by default (hostrunner's mounts are
// operator-declared), so the staging directory has to be declared to the
// runner with --hook-mount. That is the existing way a hook is given a host
// path and it is deliberately not widened for this: a validator that cannot
// see the file fails, and says so, and is not a pass.
//
// A recovery test's validator must be a .local.sh script. A .remote.sh runs
// on the source host, where the recovered file is not.
//
// The engine keeps the parts a script cannot be trusted with. It re-reads
// the copy and checks the digest before the script runs, runs the script,
// and records the result and the hash of the bytes that ran. A script never
// writes "Content verified" or "Recovery tested".

// DefaultRecoveryTestCadence is how often the newest known good copy of a
// set is owed a recovery test until a per-set setting exists. It is a named
// constant with a default, as every budget in this epic is; the per-set key
// (inherit, off, custom) is the configuration work's to add, and this is the
// value it will inherit.
const DefaultRecoveryTestCadence = 7 * 24 * time.Hour

// RecoveryTestSetDir is a backup set's validator stage directory, relative
// to the workflow root.
func RecoveryTestSetDir(set model.BackupSetID) string {
	return "recovery-test/" + set.Source + "/" + set.Set
}

// RecoveryTestRequest says what to test.
type RecoveryTestRequest struct {
	Set      model.BackupSetID
	Artifact *model.ArtifactID

	// Scheduled marks an unattended run, which can never pay. AcknowledgeCost
	// is the operator's agreement to pay for reading a copy that costs money.
	Scheduled       bool
	AcknowledgeCost bool
}

// RecoveryTestResult is a test's outcome. For an incremental (engine: kopia)
// set nothing is run: ReadOnly is true and Claim is read from the achieved
// level the snapshot catalog already holds.
type RecoveryTestResult struct {
	Result   recoverytest.Result
	Claim    recoverytest.Claim
	ReadOnly bool
}

// ErrRecoveryTestUnavailable is returned when this Service has no journal
// that can hold the claim.
var ErrRecoveryTestUnavailable = errors.New("app: this journal cannot record a recovery test")

// stageValidator is recoverytest.Validator over the stage runner.
type stageValidator struct {
	stages StageRunner
	bs     config.BackupSet
}

func (v stageValidator) request() StageRequest {
	return StageRequest{Set: v.bs, SetDir: RecoveryTestSetDir(v.bs.ID), LocalOnly: true, MaxScripts: 1}
}

func (v stageValidator) Configured(ctx context.Context, _ model.BackupSetID) (bool, error) {
	req := v.request()
	req.DryRun = true
	res, err := v.stages.RunStage(ctx, req)
	if err != nil {
		if isNotConfigured(err) {
			return false, nil
		}

		return false, err
	}

	return res.Configured && len(res.Scripts) > 0, nil
}

func (v stageValidator) Validate(ctx context.Context, in recoverytest.ValidatorInput) (recoverytest.ValidatorOutcome, error) {
	req := v.request()
	req.Vars = map[string]string{
		"RECOVERY_TEST_FILE":     in.File,
		"RECOVERY_TEST_ARTIFACT": stageValue(in.Artifact.String(), 400),
		"RECOVERY_TEST_MEDIUM":   stageValue(in.Medium, 120),
		"RECOVERY_TEST_DIGEST":   in.Digest,
		"RECOVERY_TEST_SIZE":     fmt.Sprint(in.Size),
	}
	res, err := v.stages.RunStage(ctx, req)
	if err != nil {
		return recoverytest.ValidatorOutcome{}, err
	}
	if !res.Configured || len(res.Scripts) != 1 {
		return recoverytest.ValidatorOutcome{}, fmt.Errorf("the validator directory held %d scripts when the test ran, and a recovery test has exactly one", len(res.Scripts))
	}
	sc := res.Scripts[0]

	return recoverytest.ValidatorOutcome{
		ID: sc.Name, Hash: sc.SHA256, Disposition: sc.Disposition, ExitCode: sc.ExitCode,
		Termination: sc.Termination, Output: sc.Output, Truncated: sc.OutputTruncated, Detail: sc.Detail,
	}, nil
}

// RecoveryTest runs a recovery test as req describes.
func (s *Service) RecoveryTest(ctx context.Context, req RecoveryTestRequest) (RecoveryTestResult, error) {
	bs, ok := s.backupSet(req.Set)
	if !ok {
		return RecoveryTestResult{}, fmt.Errorf("app: backup set %s is not configured", req.Set)
	}

	// An incremental set already has a ladder whose top rung is a restore
	// drill, with a cadence and an achieved level recorded per run. This
	// reads that, and runs no second drill: two producers of one set's
	// assurance would disagree.
	if bs.Engine == model.EngineKopia {
		claim, err := s.kopiaRecoveryClaim(ctx, bs)
		return RecoveryTestResult{Claim: claim, ReadOnly: true}, err
	}

	journal, ok := s.Journal.(recoverytest.Journal)
	if !ok {
		return RecoveryTestResult{}, ErrRecoveryTestUnavailable
	}

	// The default staging place is a subdirectory of the backup set's own
	// root, as the move engine's staging copy is: that disk is the one the
	// artifact already fits on, ScanManifests and discovery never look in a
	// subdirectory, and the free-space check is made against the volume the
	// backups are written to. The system temporary directory is often memory.
	root := s.recoveryStagingRoot(bs)
	var recoverer recoverytest.Recoverer = recoverytest.LocalRecoverer{Root: root}
	if s.Recoverer != nil {
		recoverer = s.Recoverer
	}

	deps := recoverytest.Deps{
		Journal: journal, Recoverer: recoverer, Mediums: configMediums{s.Config},
		Limits: s.scrubLimitsShared(), Now: s.Now,
	}
	if s.Stages != nil {
		deps.Validator = stageValidator{stages: s.Stages, bs: bs}
	}
	res, err := recoverytest.Run(ctx, deps, bs.ID, recoverytest.Config{Cadence: DefaultRecoveryTestCadence}, recoverytest.Request{
		Artifact: req.Artifact, Scheduled: req.Scheduled, AcknowledgeCost: req.AcknowledgeCost,
	})
	if err == nil {
		s.logRecoveryTest(ctx, res)
		s.settleRecoveryEpisode(ctx, bs, res)
	}
	return RecoveryTestResult{Result: res}, err
}

// settleRecoveryEpisode announces a recovery test failing, and later passing
// again, to the operator's assurance scripts. Only a verdict opens or closes
// an episode: a refusal for cost and an attempt that could not complete say
// nothing about whether the copy is usable, and a script told that a copy
// failed because a scheduled test could not afford to read it would be told
// something untrue.
func (s *Service) settleRecoveryEpisode(ctx context.Context, bs config.BackupSet, r recoverytest.Result) {
	switch r.State {
	case state.RecoveryFailed:
		if s.assurance.transition(bs.ID, "recovery", true) {
			s.notifyAssurance(ctx, bs, assuranceFacts{
				Event:    AssuranceRecoveryTestFailed,
				Artifact: r.Artifact.String(),
				Summary: fmt.Sprintf("the newest known good copy of %s did not pass its recovery test: %s",
					bs.ID, recoverytest.SanitizeLabel(r.Reason, 200)),
			})
		}
	case state.RecoveryPassed:
		if s.assurance.transition(bs.ID, "recovery", false) {
			s.notifyAssurance(ctx, bs, assuranceFacts{
				Event:    AssuranceRecoveryTestRecovered,
				Artifact: r.Artifact.String(),
				Summary:  fmt.Sprintf("the newest known good copy of %s passes its recovery test again", bs.ID),
			})
		}
	}
}

// logRecoveryTest emits the one line a recovery test writes. It names the
// validator by id and the digest gate by outcome, and never the validator's
// path or its output.
func (s *Service) logRecoveryTest(ctx context.Context, r recoverytest.Result) {
	digest := "not_compared"
	if r.DigestMatched != nil {
		digest = "mismatched"
		if *r.DigestMatched {
			digest = "matched"
		}
	}
	s.logger().RecoveryTest(ctx, obs.RecoveryTestFacts{
		Artifact: r.Artifact.String(), Medium: r.Medium, PlacementID: r.PlacementID, State: r.State, DigestMatched: digest,
		ValidatorID: r.ValidatorID, Termination: r.Termination, Cleanup: r.CleanupState,
		OutputTruncated: r.OutputTruncated, Reason: r.Reason,
	})
}

// RecoveryTestTick is the scheduled half: it ages passes that have outlived
// their cadence into overdue, sweeps staging a crash left behind, and runs
// one unattended test for each artifact set that declares a validator and
// whose newest known good copy is due.
//
// An unattended test never pays. A set whose newest copy is on a storage
// medium reads refused_for_cost every time this runs, which is the honest
// answer and is recorded as one; it does not thaw anything.
func (s *Service) RecoveryTestTick(ctx context.Context) {
	journal, ok := s.Journal.(interface {
		recoverytest.Journal
		MarkRecoveryOverdue(ctx context.Context, now time.Time) (int, error)
	})
	if !ok {
		return
	}
	if _, err := journal.MarkRecoveryOverdue(ctx, s.now()); err != nil {
		s.logger().Error(ctx, "recovery-test-overdue", err)
	}
	holds := BackupSetHoldsFrom(ctx)
	for _, src := range s.Config.Sources {
		for _, bs := range src.BackupSets {
			if ctx.Err() != nil {
				return
			}
			if bs.Disabled || unrunnableEngine(bs.Engine) != nil {
				continue
			}
			// A crash mid-test leaves a staged copy nothing refers to. The
			// sweep runs for every set, validator or not, because the copy
			// outlives a configuration change that removed the validator.
			s.sweepRecoveryStaging(ctx, bs)
			if s.Stages == nil {
				continue
			}
			if configured, err := (stageValidator{stages: s.Stages, bs: bs}).Configured(ctx, bs.ID); err != nil || !configured {
				continue
			}
			if holds != nil && holds.Held(bs.ID.String()) {
				continue
			}
			due, err := recoverytest.Due(ctx, journal, bs.ID, s.now())
			if err != nil {
				if !errors.Is(err, recoverytest.ErrNoKnownGoodCopy) {
					s.logger().Error(ctx, "recovery-test-due", err)
				}
				continue
			}
			if !due {
				continue
			}
			if _, err := s.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID, Scheduled: true}); err != nil && ctx.Err() == nil {
				s.logger().Error(ctx, "recovery-test-tick", err)
			}
		}
	}
}

// staleStagingAge is how old a staging directory has to be before the sweep
// treats it as abandoned. A test in progress touches its directory when it
// creates it and holds it for as long as the validator's timeout, so a day
// is far past anything live.
const staleStagingAge = 24 * time.Hour

func (s *Service) sweepRecoveryStaging(ctx context.Context, bs config.BackupSet) {
	root := s.recoveryStagingRoot(bs)
	if root == "" {
		return
	}
	if _, ok := s.Recoverer.(recoverytest.LocalRecoverer); s.Recoverer != nil && !ok {
		return // somebody else's recoverer owns its own cleanup
	}
	if n, err := (recoverytest.LocalRecoverer{Root: root}).Sweep(ctx, staleStagingAge); err != nil {
		s.logger().Error(ctx, "recovery-staging-sweep", err)
	} else if n > 0 {
		s.logger().Event(ctx, obs.LevelWarn, "recovery_staging_swept",
			fmt.Sprintf("removed %d abandoned recovery-test staging director%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]))
	}
}

// recoveryStagingRoot is where a set's recovery tests stage: the configured
// root when there is one, and otherwise a subdirectory of the set's own root.
func (s *Service) recoveryStagingRoot(bs config.BackupSet) string {
	if s.RecoveryStagingRoot != "" {
		return s.RecoveryStagingRoot
	}
	if bs.LocalPath != "" {
		return filepath.Join(bs.LocalPath, ".retnd-recovery-test")
	}
	return ""
}

func (s *Service) backupSet(id model.BackupSetID) (config.BackupSet, bool) {
	for _, src := range s.Config.Sources {
		for _, bs := range src.BackupSets {
			if bs.ID == id {
				return bs, true
			}
		}
	}
	return config.BackupSet{}, false
}

func (s *Service) kopiaRecoveryClaim(ctx context.Context, bs config.BackupSet) (recoverytest.Claim, error) {
	drill := bs.VerificationRestoreDrillEvery.Duration()
	catalog, ok := s.Journal.(snapshotCatalog)
	if !ok {
		return recoverytest.KopiaClaim(nil, drill, s.now()), nil
	}
	runs, err := catalog.ListSnapshotRuns(ctx, snapshotLineage(bs), maxSnapshotListing)
	if err != nil {
		return recoverytest.Claim{}, fmt.Errorf("reading %s's snapshot history: %w", bs.ID, err)
	}
	return recoverytest.KopiaClaim(runs, drill, s.now()), nil
}
