package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
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

	// OperationID, when set, is the id the start, stage and end events carry,
	// so a caller that returned it to somebody (a route) can be followed on
	// the activity feed. Empty means a fresh one.
	OperationID string
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
//
// scriptFile is the validator_id turned into the file name it selects
// (config.RecoveryTestScriptName), or "" when the policy names none, in which
// case the directory must hold exactly one script and that one is the
// validator. A named script that is not there is an error and never "not
// configured": the operator declared a validator, so its absence is the
// reason the test could not run.
type stageValidator struct {
	stages     StageRunner
	bs         config.BackupSet
	scriptFile string
}

func (v stageValidator) request() StageRequest {
	req := StageRequest{Set: v.bs, SetDir: RecoveryTestSetDir(v.bs.ID), LocalOnly: true, MaxScripts: 1}
	if v.scriptFile != "" {
		req.Only = v.scriptFile
		// The directory may hold other validators (one per id); only the
		// named one is read or run.
		req.MaxScripts = 0
	}
	return req
}

func (v stageValidator) missing() error {
	return fmt.Errorf("the declared validator %s was not found in %s", v.scriptFile, RecoveryTestSetDir(v.bs.ID))
}

func (v stageValidator) Configured(ctx context.Context, _ model.BackupSetID) (bool, error) {
	req := v.request()
	req.DryRun = true
	res, err := v.stages.RunStage(ctx, req)
	if err != nil {
		if errors.Is(err, ErrStageScriptMissing) && v.scriptFile != "" {
			return false, v.missing()
		}
		if isNotConfigured(err) {
			if v.scriptFile != "" {
				return false, v.missing()
			}
			return false, nil
		}

		return false, err
	}
	if v.scriptFile != "" && !(res.Configured && len(res.Scripts) == 1 && res.Scripts[0].Name == v.scriptFile) {
		return false, v.missing()
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
	if v.scriptFile != "" && sc.Name != v.scriptFile {
		return recoverytest.ValidatorOutcome{}, v.missing()
	}

	return recoverytest.ValidatorOutcome{
		ID: sc.Name, Hash: sc.SHA256, Disposition: sc.Disposition, ExitCode: sc.ExitCode,
		Termination: sc.Termination, Output: sc.Output, Truncated: sc.OutputTruncated, Detail: sc.Detail,
	}, nil
}

// validatorFor resolves a set's policy into the validator its tests use.
func (s *Service) validatorFor(bs config.BackupSet) (stageValidator, error) {
	v := stageValidator{stages: s.Stages, bs: bs}
	if id := bs.Assurance.RecoveryTest.ValidatorID; id != "" {
		file, err := config.RecoveryTestScriptName(id)
		if err != nil {
			return v, fmt.Errorf("the recovery-test validator_id of %s is not usable: %w", bs.ID, err)
		}
		v.scriptFile = file
	}

	return v, nil
}

// RecoveryTestValidatorDeclared answers, without running anything, whether a
// recovery test of set would find a validator to run. It makes the dry run
// RecoveryTest makes first, through the same validatorFor, so a button built
// on this answer and the test's own "not_configured" refusal cannot disagree.
//
// Only a clean "there is none" is false. A validator the operator declared by
// name but that cannot be found or examined is true, because the test then
// ends could_not_complete with that reason and not not_configured.
func (s *Service) RecoveryTestValidatorDeclared(ctx context.Context, set model.BackupSetID) (bool, error) {
	bs, ok := s.backupSet(set)
	if !ok {
		return false, fmt.Errorf("app: backup set %s is not configured", set)
	}
	if s.Stages == nil {
		return false, nil
	}
	validator, err := s.validatorFor(bs)
	if err != nil {
		return true, nil
	}
	configured, err := validator.Configured(ctx, set)
	if err != nil {
		return true, nil
	}
	return configured, nil
}

// RecoveryTest runs a recovery test as req describes.
//
// The cadence and the validator come from the set's resolved assurance
// policy (bs.Assurance), the same value the settings surfaces report, so what
// an operator is shown is what runs. A manual request runs whatever the mode
// says: asking is the operator's decision. Only the scheduled tick honours
// the mode (RecoveryTestTick).
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

	// The staging place is the deployment's one staging root
	// (assurance.staging_root), the same directory an operator's recovery
	// writes in. Fixtures and hand-built configs that name none fall back to
	// a subdirectory of the backup set's own root, so a copy is never staged
	// in memory.
	root := s.recoveryStagingRoot(bs)
	var recoverer recoverytest.Recoverer = recoverytest.LocalRecoverer{Root: root}
	if s.Recoverer != nil {
		recoverer = s.Recoverer
	}

	validator, verr := s.validatorFor(bs)
	opID := req.OperationID
	if opID == "" {
		opID = uuid.NewString()
	}
	started := s.now()
	validatorID := bs.Assurance.RecoveryTest.ValidatorID
	artifactLabel := ""
	if req.Artifact != nil {
		artifactLabel = req.Artifact.String()
	}
	s.logger().RecoveryTestStart(ctx, opID, bs.ID.String(), artifactLabel, validatorID)

	deps := recoverytest.Deps{
		Journal: journal, Recoverer: recoverer, Mediums: configMediums{s.Config},
		Limits: s.scrubLimitsShared(), Now: s.Now,
		Stage: func(id string) { s.logger().RecoveryStage(ctx, opID, bs.ID.String(), id) },
	}
	if s.Stages != nil && verr == nil {
		deps.Validator = validator
	}
	var res recoverytest.Result
	var err error
	if verr != nil {
		err = verr
	} else {
		res, err = recoverytest.Run(ctx, deps, bs.ID, recoverytest.Config{Cadence: bs.Assurance.RecoveryTestInterval()}, recoverytest.Request{
			Artifact: req.Artifact, Scheduled: req.Scheduled, AcknowledgeCost: req.AcknowledgeCost,
		})
	}
	if res.Artifact.Name != "" {
		artifactLabel = res.Artifact.String()
	}
	s.logger().RecoveryTestEnd(ctx, opID, bs.ID.String(), artifactLabel, validatorID, s.now().Sub(started), recoveryTestResult(res, err), recoveryTestReason(res, err))
	if err == nil {
		s.keepRecoveryOutput(ctx, journal, opID, bs.ID, res)
		s.logRecoveryTest(ctx, res)
		s.settleRecoveryEpisode(ctx, bs, res)
	}
	return RecoveryTestResult{Result: res}, err
}

// keepRecoveryOutput stores what the test printed, replacing the set's older
// output. Only a test that was recorded on a copy has output to keep: a set
// with no validator reads not_configured and ran nothing, so the older
// output stays the newest there is.
//
// The records are the runner's own (sanitised and bounded in
// internal/recoverytest) and the journal refuses anything else, so what is
// stored here is what every surface may show. A failure to store it is logged
// and is not the test's failure: the verdict is already on the copy.
func (s *Service) keepRecoveryOutput(ctx context.Context, journal recoverytest.Journal, opID string, set model.BackupSetID, r recoverytest.Result) {
	switch r.State {
	case state.RecoveryPassed, state.RecoveryFailed, state.RecoveryRefusedForCost, state.RecoveryCouldNotComplete:
	default:
		return
	}
	keeper, ok := journal.(interface {
		RecordRecoveryOutput(ctx context.Context, o state.RecoveryOutput) error
	})
	if !ok {
		return
	}
	records := make([]state.RecoveryOutputRecord, 0, len(r.Output))
	for _, rec := range r.Output {
		records = append(records, state.RecoveryOutputRecord{Kind: string(rec.Kind), Text: rec.Text})
	}
	err := keeper.RecordRecoveryOutput(context.WithoutCancel(ctx), state.RecoveryOutput{
		Set: set, OperationID: opID, Artifact: r.Artifact.String(), Medium: r.Medium, State: r.State,
		RecordedAt: s.now(), Truncated: r.OutputTruncated, Records: records,
	})
	if err != nil {
		s.logger().Error(ctx, "recovery-test-output", err)
	}
}

// recoveryTestResult maps a test's outcome onto the result a completion line
// states. Only a pass is a success: a refusal for cost or an attempt that
// could not complete is a warning (nothing is known about the copy), and a
// failure is an error.
func recoveryTestResult(r recoverytest.Result, err error) obs.Result {
	switch {
	case errors.Is(err, context.Canceled):
		// Stopped on request (or by shutdown): nothing is known about the
		// copy, which is what a warning says and an error does not.
		return obs.ResultWarn
	case err != nil:
		return obs.ResultError
	case r.State == state.RecoveryPassed:
		return obs.ResultSuccess
	case r.State == state.RecoveryFailed:
		return obs.ResultError
	default:
		return obs.ResultWarn
	}
}

func recoveryTestReason(r recoverytest.Result, err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled before it finished; no result was recorded"
	}
	if err != nil {
		return recoverytest.SanitizeLabel(err.Error(), 200)
	}
	if r.State == state.RecoveryPassed {
		return ""
	}
	return r.Reason
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

// DefaultRecoveryTickBound is how long one RecoveryTestTick may keep starting
// tests. A recovery test reads a whole artifact and runs an operator's
// script, the tick walks every set one after another, and it runs behind the
// scrub on the same timer, so an unbounded tick lets one slow validator hold
// up the next scrub pass for as long as it likes. When the bound is spent the
// tick stops starting new tests; what is still due waits for the next tick.
// A test that is already running is not cut short by the bound, only by its
// own validator timeout.
const DefaultRecoveryTickBound = 30 * time.Minute

// RecoveryTestTick is the scheduled half: it ages passes that have outlived
// their cadence into overdue, sweeps staging a crash left behind, and runs
// one unattended test for each set whose resolved policy asks for recovery
// tests (mode required or custom) and whose newest known good copy is due.
//
// A set under mode off is left alone even when a validator script sits in
// its directory: the directory is a convention, the mode is the decision. The
// cadence is the set's resolved cadence, and a named validator_id selects the
// script by file name.
//
// An unattended test never pays. A set whose newest copy is on a storage
// medium reads refused_for_cost every time this runs when the cost gate
// stops it, which is the honest answer and is recorded as one; it does not
// thaw anything.
func (s *Service) RecoveryTestTick(ctx context.Context) {
	journal, ok := s.Journal.(interface {
		recoverytest.Journal
		MarkRecoveryOverdue(ctx context.Context, now time.Time) (int, error)
	})
	if !ok {
		return
	}
	// The bound is wall-clock work, so it uses the real clock and not the
	// injectable one the journal's timestamps use. Zero is the default and a
	// negative value is unbounded.
	bound := s.RecoveryTickBound
	if bound == 0 {
		bound = DefaultRecoveryTickBound
	}
	started := time.Now()
	spent := func() bool { return bound > 0 && time.Since(started) >= bound }

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
			// sweep runs for every set, policy or not, because the copy
			// outlives a configuration change that turned the policy off.
			s.sweepRecoveryStaging(ctx, bs)
			if !bs.Assurance.RecoveryTestEnabled() || s.Stages == nil {
				continue
			}
			if spent() {
				// The tick's bound is spent: no new test starts.
				continue
			}
			validator, verr := s.validatorFor(bs)
			if verr != nil {
				s.logger().Error(ctx, "recovery-test-validator", verr)
				continue
			}
			if configured, err := validator.Configured(ctx, bs.ID); err != nil {
				// A declared validator that is not there is a finding the
				// test itself records (could_not_complete); let it run once
				// per cadence like any other so the claim says why.
				if validator.scriptFile == "" {
					continue
				}
			} else if !configured {
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
			if _, err := s.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID, Scheduled: true}); err != nil && ctx.Err() == nil && !errors.Is(err, state.ErrCopyChanged) {
				s.logger().Error(ctx, "recovery-test-tick", err)
			}
		}
	}
}

// stagingTTL is how old a staging directory has to be before the sweep treats
// it as abandoned: assurance.staging_ttl, which is how long a staged leftover
// survives. A test in progress holds its directory for as long as the
// validator's timeout, far inside the default day.
func (s *Service) stagingTTL() time.Duration {
	if s.Config != nil && s.Config.Assurance.StagingTTL > 0 {
		return s.Config.Assurance.StagingTTL
	}
	return config.DefaultStagingTTL
}

func (s *Service) sweepRecoveryStaging(ctx context.Context, bs config.BackupSet) {
	root := s.recoveryStagingRoot(bs)
	if root == "" {
		return
	}
	if _, ok := s.Recoverer.(recoverytest.LocalRecoverer); s.Recoverer != nil && !ok {
		return // somebody else's recoverer owns its own cleanup
	}
	if n, err := (recoverytest.LocalRecoverer{Root: root}).Sweep(ctx, s.stagingTTL()); err != nil {
		s.logger().Error(ctx, "recovery-staging-sweep", err)
	} else if n > 0 {
		s.logger().Event(ctx, obs.LevelWarn, "recovery_staging_swept",
			fmt.Sprintf("removed %d abandoned recovery-test staging director%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]))
	}
}

// recoveryStagingRoot is where a recovery test stages: the one staging root
// the deployment configured (assurance.staging_root, which defaults beside the
// state database), and, for a configuration that was built by hand and names
// none, a subdirectory of the set's own root.
func (s *Service) recoveryStagingRoot(bs config.BackupSet) string {
	if s.RecoveryStagingRoot != "" {
		return s.RecoveryStagingRoot
	}
	if s.Config != nil && s.Config.Assurance.StagingRoot != "" {
		return s.Config.Assurance.StagingRoot
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
