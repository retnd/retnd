package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
// The environment is the small one unless the operator opts in (#1221): the
// RECOVERY_TEST_* variables, the product's own RETND_* facts and the baseline
// PATH. The set's workflow variables, secrets included, reach the validator
// only when it is opted in: a custom validator's own env (assurance.validators)
// when it states one, then assurance.recovery_test.validator_env on the set,
// then on the deployment. The set's own script (validator_id, or the one in
// its recovery-test directory) follows the set's value alone.
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
// Which script runs is decided per artifact, most specific first (#1203):
//
//  1. a custom validator scoped to this set whose pattern matches the
//     artifact's name (its script in config.CustomValidatorDir);
//  2. a global custom validator whose pattern matches;
//  3. the set's validator_id (scriptFile), its own or inherited;
//  4. the one script in recovery-test/<source>/<set>.
//
// The built-in structure check is the rung after these, and it is
// recoverytest.Run's to take when Configured says there is nothing here.
//
// A named script that is not there is an error and never "not configured":
// the operator declared a validator, so its absence is the reason the test
// could not run. Two patterns matching one name at the same scope is an
// error for the same reason, and it names both rather than guessing.
type stageValidator struct {
	stages     StageRunner
	bs         config.BackupSet
	scriptFile string
	custom     []config.ResolvedCustomValidator
}

// workflowEnv says whether a script may start with the set's workflow
// environment (#1221). The validator's own env decides when it states one,
// then the set's validator_env (the deployment's already folded in), then
// minimal. Only the exact word workflow opens it: anything else, a stray
// spelling and an empty value included, is the small environment.
func (v stageValidator) workflowEnv(own string) bool {
	return config.ResolveValidatorEnv(own, v.bs.Assurance.RecoveryTest.ValidatorEnv) == config.ValidatorEnvWorkflow
}

// validatorChoice is what runs for one artifact.
type validatorChoice struct {
	req StageRequest
	// custom is the custom validator chosen, nil for the set's own.
	custom *config.ResolvedCustomValidator
	// ambiguous names the validators that matched at one scope.
	ambiguous []string
}

func (v stageValidator) choose(artifact model.ArtifactID) validatorChoice {
	// No copy means no name to match: a pattern is never asked about "".
	var m config.CustomValidatorMatch
	if artifact.Name != "" {
		m = config.MatchCustomValidator(v.custom, v.bs.ID, artifact.Name)
	}
	switch {
	case len(m.Ambiguous) > 0:
		return validatorChoice{ambiguous: m.Ambiguous}
	case m.Validator != nil:
		return validatorChoice{custom: m.Validator, req: StageRequest{
			Set: v.bs, SetDir: config.CustomValidatorDir, LocalOnly: true, Only: m.Validator.ScriptFile,
			InheritWorkflowEnv: v.workflowEnv(m.Validator.Env),
		}}
	}
	req := StageRequest{Set: v.bs, SetDir: RecoveryTestSetDir(v.bs.ID), LocalOnly: true, MaxScripts: 1,
		// Only the exact opt-in opens the workflow environment (#1221). Any
		// other value, an empty one included, is the small environment.
		InheritWorkflowEnv: v.workflowEnv("")}
	if v.scriptFile != "" {
		req.Only = v.scriptFile
		// The directory may hold other validators (one per id); only the
		// named one is read or run.
		req.MaxScripts = 0
	}
	return validatorChoice{req: req}
}

// declares says whether something was declared for this artifact by name,
// so a failure to find it is a finding and not "nothing configured".
func (c validatorChoice) declares() bool {
	return c.custom != nil || len(c.ambiguous) > 0 || c.req.Only != ""
}

func (c validatorChoice) problem(artifact model.ArtifactID) error {
	if len(c.ambiguous) > 0 {
		return fmt.Errorf("%s matches the patterns of %d validators at the same scope (%s), and which one is meant is not something to guess; narrow one of the patterns",
			artifact.Name, len(c.ambiguous), strings.Join(c.ambiguous, ", "))
	}
	return nil
}

func (c validatorChoice) missing() error {
	if c.custom != nil {
		return fmt.Errorf("the custom validator %s (pattern %s) applies, and its script %s was not found in %s",
			c.custom.Name, c.custom.Pattern, c.custom.ScriptFile, config.CustomValidatorDir)
	}
	return fmt.Errorf("the declared validator %s was not found in %s", c.req.Only, c.req.SetDir)
}

func (v stageValidator) Configured(ctx context.Context, _ model.BackupSetID, artifact model.ArtifactID) (bool, error) {
	c := v.choose(artifact)
	if err := c.problem(artifact); err != nil {
		return false, err
	}
	req := c.req
	req.DryRun = true
	res, err := v.stages.RunStage(ctx, req)
	if err != nil {
		if errors.Is(err, ErrStageScriptMissing) && c.declares() {
			return false, c.missing()
		}
		if isNotConfigured(err) {
			if c.declares() {
				return false, c.missing()
			}
			return false, nil
		}

		return false, err
	}
	if c.req.Only != "" && (!res.Configured || len(res.Scripts) != 1 || res.Scripts[0].Name != c.req.Only) {
		return false, c.missing()
	}

	return res.Configured && len(res.Scripts) > 0, nil
}

func (v stageValidator) Validate(ctx context.Context, in recoverytest.ValidatorInput) (recoverytest.ValidatorOutcome, error) {
	c := v.choose(in.Artifact)
	if err := c.problem(in.Artifact); err != nil {
		return recoverytest.ValidatorOutcome{}, err
	}
	req := c.req
	req.Vars = map[string]string{
		"RECOVERY_TEST_FILE":     in.File,
		"RECOVERY_TEST_ARTIFACT": stageValue(in.Artifact.String(), 400),
		"RECOVERY_TEST_NAME":     stageValue(in.Artifact.Name, 255),
		"RECOVERY_TEST_MEDIUM":   stageValue(in.Medium, 120),
		"RECOVERY_TEST_DIGEST":   in.Digest,
		"RECOVERY_TEST_SIZE":     fmt.Sprint(in.Size),
	}
	if c.custom != nil {
		req.Vars["RECOVERY_TEST_VALIDATOR"] = c.custom.Name
		req.Vars["RECOVERY_TEST_PATTERN"] = stageValue(c.custom.Pattern, config.MaxCustomValidatorPatternBytes)
		req.Vars["RECOVERY_TEST_SCOPE"] = c.custom.Scope()
	}
	res, err := v.stages.RunStage(ctx, req)
	if err != nil {
		if errors.Is(err, ErrStageScriptMissing) && c.declares() {
			return recoverytest.ValidatorOutcome{}, c.missing()
		}
		return recoverytest.ValidatorOutcome{}, err
	}
	if !res.Configured || len(res.Scripts) != 1 {
		return recoverytest.ValidatorOutcome{}, fmt.Errorf("the validator directory held %d scripts when the test ran, and a recovery test has exactly one", len(res.Scripts))
	}
	sc := res.Scripts[0]
	if c.req.Only != "" && sc.Name != c.req.Only {
		return recoverytest.ValidatorOutcome{}, c.missing()
	}

	return recoverytest.ValidatorOutcome{
		ID: sc.Name, Hash: sc.SHA256, Disposition: sc.Disposition, ExitCode: sc.ExitCode,
		Termination: sc.Termination, Output: sc.Output, Truncated: sc.OutputTruncated, Detail: sc.Detail,
	}, nil
}

// validatorFor resolves a set's policy into the validator its tests use.
func (s *Service) validatorFor(bs config.BackupSet) (stageValidator, error) {
	v := stageValidator{stages: s.Stages, bs: bs}
	if s.Config != nil {
		v.custom = s.Config.Assurance.Validators
	}
	if id := bs.Assurance.RecoveryTest.ValidatorID; id != "" {
		file, err := config.RecoveryTestScriptName(id)
		if err != nil {
			return v, fmt.Errorf("the recovery-test validator_id of %s is not usable: %w", bs.ID, err)
		}
		v.scriptFile = file
	}

	return v, nil
}

// newestArtifact is the artifact a test of set would pick when none is
// named, or the zero id when the set has no known good copy (or no journal
// that can say).
func (s *Service) newestArtifact(ctx context.Context, set model.BackupSetID) model.ArtifactID {
	id, _, _ := s.newestArtifactErr(ctx, set)
	return id
}

// newestArtifactErr is newestArtifact with the reason it found none. A set
// with no known good copy is the zero id and no error; ok is false when the
// journal cannot say at all, so a caller can tell "nothing yet" from "cannot
// look".
func (s *Service) newestArtifactErr(ctx context.Context, set model.BackupSetID) (id model.ArtifactID, ok bool, err error) {
	journal, can := s.Journal.(recoverytest.Journal)
	if !can {
		return model.ArtifactID{}, false, nil
	}
	rec, _, err := recoverytest.Newest(ctx, journal, set)
	switch {
	case errors.Is(err, recoverytest.ErrNoKnownGoodCopy):
		return model.ArtifactID{}, true, nil
	case err != nil:
		return model.ArtifactID{}, true, err
	}
	return rec.Artifact, true, nil
}

// RecoveryTestValidatorDeclared answers, without running anything, whether a
// recovery test of set would find a validator to run. It makes the dry run
// RecoveryTest makes first, through the same validatorFor and for the same
// newest copy, so a button built on this answer and the test's own
// "not_configured" refusal cannot disagree.
//
// Only a clean "there is none" is false. A validator the operator declared by
// name (or by a pattern that matches the newest copy) but that cannot be
// found or examined is true, because the test then ends could_not_complete
// with that reason and not not_configured.
func (s *Service) RecoveryTestValidatorDeclared(ctx context.Context, set model.BackupSetID) (bool, error) {
	return s.validatorDeclaredFor(ctx, set, s.newestArtifact(ctx, set))
}

// validatorDeclaredFor is RecoveryTestValidatorDeclared for a copy the caller
// has already picked, so a caller that needs several answers about one test
// resolves the newest copy once and asks them all about the same name.
func (s *Service) validatorDeclaredFor(ctx context.Context, set model.BackupSetID, artifact model.ArtifactID) (bool, error) {
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
	configured, err := validator.Configured(ctx, set, artifact)
	if err != nil {
		return true, nil
	}
	return configured, nil
}

// RecoveryTestValidatorFor names the custom validator that would run for
// artifact in set, or "" when none of the patterns apply (the set's own
// validator, the built-in or nothing decides then). ambiguous lists the
// validators whose patterns collide at one scope.
func (s *Service) RecoveryTestValidatorFor(set model.BackupSetID, artifactName string) (name string, ambiguous []string) {
	// No copy means no name, and a pattern is never asked about "": ^$ and .*
	// match it, and a set that has not been backed up yet would resolve to
	// whichever validator happened to say so.
	if s.Config == nil || artifactName == "" {
		return "", nil
	}
	m := config.MatchCustomValidator(s.Config.Assurance.Validators, set, artifactName)
	if m.Validator != nil {
		return m.Validator.Name, nil
	}
	return "", m.Ambiguous
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
	// The copy this test is about, resolved once: the feed's line, the
	// effective check and the run itself are all about this one name. Asking
	// the journal separately for each would let a backup that lands in
	// between make the feed name one validator while another runs.
	var target model.ArtifactID
	var targetErr error
	if req.Artifact != nil {
		artifactLabel = req.Artifact.String()
		target = *req.Artifact
	} else {
		target, _, targetErr = s.newestArtifactErr(ctx, bs.ID)
	}
	// A custom validator whose pattern matches the copy is the one that
	// runs (#1203), so it is the one the feed names.
	matched, _ := s.RecoveryTestValidatorFor(bs.ID, target.Name)
	if matched != "" {
		validatorID = matched
	}
	// The feed names the check that is about to run: the reserved identity
	// when it is the built-in, so a surface can word the line from it.
	if check, _, cerr := s.effectiveCheckFor(ctx, bs.ID, target, targetErr); cerr == nil && check == CheckBuiltin {
		validatorID = recoverytest.BuiltinValidatorID
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
		pick := req.Artifact
		if pick == nil && target.Name != "" {
			// The copy the feed was worded for, not whichever is newest by
			// the time Run asks again.
			pick = &target
		}
		res, err = recoverytest.Run(ctx, deps, bs.ID, recoverytest.Config{Cadence: bs.Assurance.RecoveryTestInterval(), BuiltinCheck: bs.Assurance.RecoveryTest.BuiltinCheck}, recoverytest.Request{
			Artifact: pick, Scheduled: req.Scheduled, AcknowledgeCost: req.AcknowledgeCost,
		})
	}
	if matched != "" && (verr != nil || deps.Validator == nil || res.Check == recoverytest.CheckBuiltin) {
		// A pattern matched this copy and the test did not run its script.
		// The verdict says what happened; this leaves the reason where an
		// operator looking for the downgrade will find it.
		s.logger().RecoveryTestValidatorFallback(ctx, opID, bs.ID.String(), target.String(), matched, ranInstead(verr, res))
	}
	if res.Artifact.Name != "" {
		artifactLabel = res.Artifact.String()
	}
	s.logger().RecoveryTestEnd(ctx, opID, bs.ID.String(), artifactLabel, validatorID, s.now().Sub(started), recoveryTestResult(res, err), recoveryTestReason(ctx, res, err), cancelledOnRequest(ctx) && errors.Is(err, context.Canceled))
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

// ranInstead says what stood in for a matched custom validator that did not
// run.
func ranInstead(verr error, res recoverytest.Result) string {
	switch {
	case verr != nil:
		return "nothing: " + recoverytest.SanitizeLabel(verr.Error(), 200)
	case res.Check == recoverytest.CheckBuiltin:
		return "the built-in structure check"
	default:
		return "nothing: no stage runner is wired"
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

// RecoveryTestCancelledReason and RecoveryTestInterruptedReason are the
// words of an end line for a test cut short: stopped on request, or
// interrupted by anything else (the service shutting down). The words are for
// a reader; which of the two it was is the line's cancelled field (#1200).
const (
	RecoveryTestCancelledReason   = "cancelled before it finished; no result was recorded"
	RecoveryTestInterruptedReason = "interrupted before it finished; no result was recorded"
)

func recoveryTestReason(ctx context.Context, r recoverytest.Result, err error) string {
	if errors.Is(err, context.Canceled) {
		if cancelledOnRequest(ctx) {
			return RecoveryTestCancelledReason
		}
		return RecoveryTestInterruptedReason
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
// tests (mode required or custom, or mode builtin for the product's own
// structure check) and whose newest known good copy is due.
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
			// Mode required or custom schedules the operator's validator; mode
			// builtin schedules the product's structure check and needs no
			// script runner. Mode off and inherit schedule nothing, whatever
			// sits in the validator directory.
			builtinMode := bs.Assurance.RecoveryTest.Mode == config.RecoveryTestBuiltin
			if !bs.Assurance.RecoveryTestScheduled() || (!builtinMode && s.Stages == nil) {
				continue
			}
			if spent() {
				// The tick's bound is spent: no new test starts.
				continue
			}
			if builtinMode {
				// A set whose newest copy the built-in has no reader for would
				// be refused on every tick; say nothing and run nothing.
				if check, _, err := s.RecoveryTestEffectiveCheck(ctx, bs.ID); err != nil || check == CheckNone {
					continue
				}
			} else {
				validator, verr := s.validatorFor(bs)
				if verr != nil {
					s.logger().Error(ctx, "recovery-test-validator", verr)
					continue
				}
				newest := s.newestArtifact(ctx, bs.ID)
				if configured, err := validator.Configured(ctx, bs.ID, newest); err != nil {
					// A declared validator that is not there (or two patterns
					// that collide) is a finding the test itself records
					// (could_not_complete); let it run once per cadence like
					// any other so the claim says why.
					if !validator.choose(newest).declares() {
						continue
					}
				} else if !configured {
					continue
				}
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

// RecoveryTestEffectiveCheck says which check a recovery test of set would
// run now (#1111): "validator", "builtin" or "none", and for none the reason
// in plain words. It makes the decisions Run makes first, in Run's order, and
// reads nothing: the newest known good copy is judged by its name alone, so a
// button built on this cannot disagree with the refusal the test would give.
func (s *Service) RecoveryTestEffectiveCheck(ctx context.Context, set model.BackupSetID) (check, reason string, err error) {
	target, _, nerr := s.newestArtifactErr(ctx, set)
	return s.effectiveCheckFor(ctx, set, target, nerr)
}

// effectiveCheckFor is RecoveryTestEffectiveCheck for a copy the caller has
// already resolved (nerr is what resolving it reported), so a test that needs
// this answer and the copy resolves the copy once and cannot name one
// validator while running another.
func (s *Service) effectiveCheckFor(ctx context.Context, set model.BackupSetID, target model.ArtifactID, nerr error) (check, reason string, err error) {
	bs, ok := s.backupSet(set)
	if !ok {
		return "", "", fmt.Errorf("app: backup set %s is not configured", set)
	}
	declared, err := s.validatorDeclaredFor(ctx, set, target)
	if err != nil {
		return "", "", err
	}
	if declared {
		return CheckValidator, "", nil
	}
	if bs.Assurance.RecoveryTest.BuiltinCheck == config.BuiltinCheckOff {
		return CheckNone, recoverytest.BuiltinOffReason, nil
	}
	if _, ok := s.Journal.(recoverytest.Journal); !ok {
		return CheckBuiltin, "", nil
	}
	switch {
	case nerr != nil:
		return "", "", nerr
	case target.Name == "":
		// Nothing to judge by yet; the test itself will say there is no copy.
		return CheckBuiltin, "", nil
	}
	if _, applies := (recoverytest.StructureCheck{}).Applies(target.Name); !applies {
		return CheckNone, recoverytest.NoReaderReason(target.Name), nil
	}
	return CheckBuiltin, "", nil
}

// The three answers RecoveryTestEffectiveCheck gives.
const (
	CheckValidator = "validator"
	CheckBuiltin   = "builtin"
	CheckNone      = "none"
)
