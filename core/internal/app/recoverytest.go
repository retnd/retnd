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
// configuration into its inputs and hands it a journal. The validator is the
// set's declared restore-test hook (revalidation.command), which is the
// operator's own executable run under the untrusted-subprocess contract
// FR-13 established. Nothing new is executed and nothing new is trusted.

// DefaultRecoveryTestCadence is how often the newest known good copy of a
// set is owed a recovery test until a per-set setting exists. It is a named
// constant with a default, as every budget in this epic is; the per-set key
// (inherit, off, custom) is the configuration work's to add, and this is the
// value it will inherit.
const DefaultRecoveryTestCadence = 7 * 24 * time.Hour

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
	root := s.RecoveryStagingRoot
	if root == "" && bs.LocalPath != "" {
		root = filepath.Join(bs.LocalPath, ".retnd-recovery-test")
	}
	var recoverer recoverytest.Recoverer = recoverytest.LocalRecoverer{Root: root}
	if s.Recoverer != nil {
		recoverer = s.Recoverer
	}

	deps := recoverytest.Deps{
		Journal: journal, Recoverer: recoverer, Mediums: configMediums{s.Config},
		Limits: s.scrubLimitsShared(), Now: s.Now,
	}
	cfg := recoverytest.Config{Command: bs.Revalidation.Command, Cadence: DefaultRecoveryTestCadence}
	res, err := recoverytest.Run(ctx, deps, bs.ID, cfg, recoverytest.Request{
		Artifact: req.Artifact, Scheduled: req.Scheduled, AcknowledgeCost: req.AcknowledgeCost,
	})
	if err == nil {
		s.logRecoveryTest(ctx, res)
	}
	return RecoveryTestResult{Result: res}, err
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
// their cadence into overdue, and runs one unattended test for each artifact
// set that declares a validator and whose newest known good copy is due.
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
			if bs.Disabled || unrunnableEngine(bs.Engine) != nil || bs.Revalidation.Command == nil {
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

var _ = state.RecoveryUnknown
