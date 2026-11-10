package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// Re-encrypting retained copies under the active suite and key (#1317, EPIC
// T), the operator surface. `retnd encryption reencrypt` and GET/POST
// /api/v1/encryption/reencrypt land on the methods here, so a refusal has one
// author and the terminal and the browser read the same sentence.
//
// It is #1291's encrypt-existing with a different selection: instead of the
// plaintext copies of one set, every encrypted copy, across the deployment,
// whose suite (--suite), key (--key) or suite status (--deprecated) matches.
// The engine does the work through the same entry point
// (placement.Engine.EncryptExisting with Plan.Reencrypt), so the opt-in row,
// the medium-to-medium rule, the decrypt-verify before the source is retired
// and the two documented cancel states are #1291's, unchanged. The run is
// one operation for the deployment, cancelled through the same assurance
// cancel route from a terminal and from a browser.
//
// Nothing here starts on its own: no scheduler, no policy change and no key
// rotation calls it.

// ErrReencryptRunning is returned when a re-encryption run is already going.
// One at a time for the deployment, so a double click cannot become two
// passes over the same copies.
var ErrReencryptRunning = errors.New("service: a re-encryption run is already running")

const opKindReencrypt = "reencrypt"

// reencryptCopy is one encrypted copy a selector matched, with the plan a run
// would take for it. DestinationMedium is empty when no destination was given
// and no other configured medium exists to propose.
type reencryptCopy struct {
	plan   placement.Plan
	source string
	enc    state.PlacementEncryption
}

// reencryptStatusSelected is the --deprecated selector: a suite whose status
// says new copies should no longer go under it. A retired suite is included,
// because its copies are the ones that most need to move.
func reencryptStatusSelected(suiteID string) bool {
	s, ok := copycrypt.Lookup(suiteID)
	if !ok {
		return false
	}
	st := string(s.Status())
	return st == "deprecated" || st == "retired"
}

// prepareReencrypt is every refusal a request has before anything is planned,
// in the order it has them: not exactly one selector, a suite this build does
// not know, a destination that is not a configured medium, a run with no
// destination. It returns one entry per ACTIVE encrypted copy on a medium
// that the selector matches, across every configured backup set the move
// engine operates on. The engine says per copy whether a run would rewrite
// it, and why not.
func (b *BackupService) prepareReencrypt(ctx context.Context, req apicontract.StartReencryptRequest, run bool) ([]reencryptCopy, error) {
	selectors := 0
	for _, on := range []bool{req.SuiteID != "", req.KeyID != "", req.Deprecated} {
		if on {
			selectors++
		}
	}
	if selectors != 1 {
		return nil, fmt.Errorf("%w: name exactly one of a suite, a key, or deprecated, so it is clear which copies are meant", ErrInvalidRequest)
	}
	if req.SuiteID != "" {
		if _, ok := copycrypt.Lookup(req.SuiteID); !ok {
			var known []string
			for _, s := range copycrypt.Suites() {
				known = append(known, s.ID())
			}
			return nil, fmt.Errorf("%w: %q is not a suite this build knows (known: %s)", ErrInvalidRequest, req.SuiteID, strings.Join(known, ", "))
		}
	}

	inner := b.state.Load().inner
	var mediums []string
	for _, m := range inner.Config.StorageMediums {
		mediums = append(mediums, m.ID)
	}
	if req.DestinationMedium != "" {
		found := false
		for _, id := range mediums {
			if id == req.DestinationMedium {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %q is not a configured storage medium (configured: %s)", ErrInvalidRequest, req.DestinationMedium, strings.Join(mediums, ", "))
		}
	} else if run {
		return nil, fmt.Errorf("%w: a destination medium is required for a run, so a medium is never chosen for you; the dry run proposes one per copy", ErrInvalidRequest)
	}

	var out []reencryptCopy
	for _, src := range inner.Config.Sources {
		for _, bs := range src.BackupSets {
			if bs.Engine == model.EngineKopia || bs.Disabled {
				// Kopia keeps its own encrypted repository and a disabled
				// set is left untouched; neither has copies this verb moves.
				continue
			}
			recs, err := b.journal.ListByBackupSet(ctx, bs.ID)
			if err != nil {
				return nil, fmt.Errorf("service: listing the backups of %s: %w", bs.ID, err)
			}
			for _, rec := range recs {
				if rec.State != string(lifecycle.Complete) {
					continue
				}
				for _, p := range rec.Placements {
					if p.Status != state.PlacementActive || p.Medium == state.MediumLocal {
						continue
					}
					enc, ok, err := b.journal.PlacementEncryption(ctx, rec.Artifact, p.Medium)
					if err != nil {
						return nil, fmt.Errorf("service: reading the encryption of %s on %s: %w", rec.Artifact, p.Medium, err)
					}
					if !ok {
						continue
					}
					switch {
					case req.SuiteID != "" && enc.FormatID != req.SuiteID,
						req.KeyID != "" && enc.KeyID != req.KeyID,
						req.Deprecated && !reencryptStatusSelected(enc.FormatID):
						continue
					}
					dest := req.DestinationMedium
					if dest == "" {
						// The dry run's proposal: the first configured medium,
						// in configuration order, that is not the copy's own.
						for _, id := range mediums {
							if id != p.Medium {
								dest = id
								break
							}
						}
					}
					out = append(out, reencryptCopy{
						plan:   placement.Plan{Artifact: rec.Artifact, DestinationMedium: dest, Reencrypt: true},
						source: p.Medium,
						enc:    enc,
					})
				}
			}
		}
	}
	return out, nil
}

// PlanReencrypt is GET /encryption/reencrypt, the dry run: every encrypted
// copy the selector matches, grouped by suite and then by key, each with the
// destination a run would use and, when a run would not rewrite it, the
// refusal and its reason. It asks the engine the question a run asks, so the
// table and the run cannot disagree, and it plans nothing, reads no object
// and writes nothing.
func (b *BackupService) PlanReencrypt(ctx context.Context, req apicontract.StartReencryptRequest) (apicontract.ReencryptPlan, error) {
	copies, err := b.prepareReencrypt(ctx, req, false)
	if err != nil {
		return apicontract.ReencryptPlan{}, err
	}
	var plans []placement.Plan
	var asked []int
	for i, c := range copies {
		if c.plan.DestinationMedium != "" {
			plans = append(plans, c.plan)
			asked = append(asked, i)
		}
	}
	verdicts := make([]error, len(copies))
	if len(plans) > 0 {
		checks, err := b.state.Load().inner.CheckEncryptExisting(ctx, plans)
		if err != nil {
			return apicontract.ReencryptPlan{}, err
		}
		for k, i := range asked {
			verdicts[i] = checks[k]
		}
	}

	out := apicontract.ReencryptPlan{Groups: []apicontract.ReencryptGroup{}}
	index := map[[2]string]int{}
	for i, c := range copies {
		cand := apicontract.ReencryptCandidate{
			ArtifactID: c.plan.Artifact.String(), SourceMedium: c.source,
			DestinationMedium: c.plan.DestinationMedium, Verdict: "would_reencrypt",
		}
		switch {
		case c.plan.DestinationMedium == "":
			cand.Verdict, cand.Refusal = "refused", "no_destination"
			cand.Reason = fmt.Sprintf("%s is on %q and no other storage medium is configured, so there is nowhere to write the new copy (the journal holds one copy per medium)", cand.ArtifactID, c.source)
			out.Refused++
		case verdicts[i] != nil:
			code, ok := encryptExistingRefusal(verdicts[i])
			if !ok {
				return apicontract.ReencryptPlan{}, fmt.Errorf("service: checking %s: %w", c.plan.Artifact, verdicts[i])
			}
			cand.Verdict, cand.Refusal, cand.Reason = "refused", code, verdicts[i].Error()
			out.Refused++
		default:
			out.WouldReencrypt++
		}
		k := [2]string{c.enc.FormatID, c.enc.KeyID}
		g, ok := index[k]
		if !ok {
			g = len(out.Groups)
			index[k] = g
			out.Groups = append(out.Groups, apicontract.ReencryptGroup{SuiteID: c.enc.FormatID, KeyID: c.enc.KeyID, Candidates: []apicontract.ReencryptCandidate{}})
		}
		out.Groups[g].Candidates = append(out.Groups[g].Candidates, cand)
	}
	sort.SliceStable(out.Groups, func(i, j int) bool {
		if out.Groups[i].SuiteID != out.Groups[j].SuiteID {
			return out.Groups[i].SuiteID < out.Groups[j].SuiteID
		}
		return out.Groups[i].KeyID < out.Groups[j].KeyID
	})
	for _, g := range out.Groups {
		sort.SliceStable(g.Candidates, func(i, j int) bool { return g.Candidates[i].ArtifactID < g.Candidates[j].ArtifactID })
	}
	return out, nil
}

func reencryptPlans(copies []reencryptCopy) []placement.Plan {
	plans := make([]placement.Plan, 0, len(copies))
	for _, c := range copies {
		plans = append(plans, c.plan)
	}
	return plans
}

// RunReencrypt is the terminal's run when no engine is serving: the same
// refusals, then the same engine run, to the end in this process, with ctx as
// the only way to stop it.
func (b *BackupService) RunReencrypt(ctx context.Context, req apicontract.StartReencryptRequest) (EncryptExistingResult, error) {
	copies, err := b.prepareReencrypt(ctx, req, true)
	if err != nil {
		return EncryptExistingResult{}, err
	}
	// The cycle's single-flight guard: a normal move cycle must not drive the
	// same moves while this runs.
	if !b.runOnce.TryLock() {
		return EncryptExistingResult{}, fmt.Errorf("%w: a backup cycle is in progress", ErrOperationAlreadyRunning)
	}
	defer b.runOnce.Unlock()
	report, err := b.state.Load().inner.EncryptExisting(ctx, reencryptPlans(copies))
	if err != nil {
		return EncryptExistingResult{}, err
	}
	out := EncryptExistingResult{
		Planned: report.Planned, Resumed: report.Resumed, Completed: report.Completed,
		Abandoned: report.Abandoned, Refused: report.Refused, Outcomes: []EncryptExistingOutcome{},
	}
	for _, o := range report.Outcomes {
		line := EncryptExistingOutcome{ArtifactID: o.Artifact.String(), Phase: string(o.Phase), Reason: o.Refused}
		if code, ok := encryptExistingRefusal(o.Err); ok && o.Err != nil {
			line.Refusal = code
		}
		out.Outcomes = append(out.Outcomes, line)
	}
	return out, nil
}

// StartReencrypt is POST /encryption/reencrypt: the same run, started in the
// background under the operation registry a scrub uses, so the cancel route
// is the same route from a terminal and from a browser. The operation belongs
// to the deployment rather than to one backup set, so its set is empty.
func (b *BackupService) StartReencrypt(ctx context.Context, req apicontract.StartReencryptRequest) (ScrubStarted, error) {
	copies, err := b.prepareReencrypt(ctx, req, true)
	if err != nil {
		return ScrubStarted{}, err
	}
	plans := reencryptPlans(copies)

	opID := uuid.NewString()
	opCtx, cancel := context.WithCancelCause(b.ctx)
	b.ops.mu.Lock()
	if b.ops.running(opKindReencrypt, "") != nil {
		b.ops.mu.Unlock()
		cancel(nil)
		return ScrubStarted{}, ErrReencryptRunning
	}
	// The cycle's single-flight guard, held until the goroutine ends.
	if !b.runOnce.TryLock() {
		b.ops.mu.Unlock()
		cancel(nil)
		return ScrubStarted{}, fmt.Errorf("%w: a backup cycle is in progress", ErrOperationAlreadyRunning)
	}
	b.ops.add(&assuranceOp{id: opID, kind: opKindReencrypt, cancel: cancel})
	b.ops.mu.Unlock()

	inner := b.state.Load().inner
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer b.runOnce.Unlock()
		defer b.ops.finish(opID)
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error(context.Background(), "reencrypt-panic", fmt.Errorf("recovered panic: %v", r))
			}
		}()
		if _, err := inner.EncryptExisting(opCtx, plans); err != nil && opCtx.Err() == nil {
			b.logger.Error(context.Background(), "reencrypt", err)
		}
	}()
	return ScrubStarted{OperationID: opID}, nil
}
