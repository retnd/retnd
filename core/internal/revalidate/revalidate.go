// Package revalidate implements Phase 4's scheduled revalidation
// (docs/EPIC.md): re-checking artifacts that have already reached a
// durable, "already passed" state (COMMITTED, REMOTE_DELETE_PENDING,
// COMPLETE or REMOTE_RETAINED), on a cadence and at a scope an operator
// configures (config.Revalidation) rather than on every cycle. Bit rot
// does not announce itself: a backup that verified six months ago is not
// guaranteed to still verify today, and the only way to find out before a
// restore is actually needed is to ask again.
//
// # Why this is not FR-17 reconciliation running more often
//
// internal/reconcile's checkLocalFinal already re-hashes a durable local
// copy against the checksum FR-13 recorded for it, but only once, at
// startup, for every artifact in the backup set, unconditionally. That is
// the right shape for FR-17's job (bringing the journal back in line with
// reality after a crash) and the wrong shape for this one: a long-running
// process does not restart on any predictable cadence, so FR-17's pass
// might not run again for weeks, and even where it does, re-reading a
// NAS's worth of already-verified data on every invocation has a real I/O
// cost. config.Revalidation.Interval and MaxPerCycle exist to bound
// exactly that cost: only artifacts overdue by Interval are eligible at
// all (SelectDue), and at most MaxPerCycle of those are actually checked
// in one Run call, so a backlog of simultaneously-due artifacts (for
// example right after a large initial backfill) spreads out over several
// calls instead of turning into one unbounded sweep.
//
// This package does not invent new lifecycle edges for what it finds, and
// it does not reimplement reconcile.go's routing logic: a failed recheck
// is routed through the exact same lifecycle.Advance calls reconcile.go
// already makes for "the durable local copy was found invalid after the
// fact" (COMMITTED or REMOTE_DELETE_PENDING -> QUARANTINED, COMPLETE ->
// QUARANTINED_LOST; see machine.go's package doc for why those are the
// only two shapes and why COMPLETE's case is irrecoverable). A successful
// recheck records a same-state pass (Committed -> Committed, and so on),
// which both leaves an audit trail and resets SelectDue's due-ness clock
// for that artifact (see SelectDue's doc for why UpdatedAt is safe to use
// that way here).
//
// # The restore-test hook
//
// config.Revalidation.Command, run through lifecycle.RunRestoreCheck, is
// the stronger form of the same idea Phase 4 asks for: proving an artifact
// still actually restores, not merely that its bytes are unchanged. It is
// independent of the hash tier (config.Revalidation.Hash); a backup set
// can enable either, both, or (once Interval/MaxPerCycle are unset)
// neither.
package revalidate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// Journal is the slice of internal/state that revalidation needs: the two
// methods every lifecycle.Advance call already requires, plus
// ListByBackupSet to enumerate what to consider in the first place. This
// mirrors internal/reconcile.Journal exactly, for the same reason: a test
// can substitute a fake without standing up SQLite, and this package
// cannot reach past this surface into migrations or schema concerns it
// does not own.
type Journal interface {
	lifecycle.Journal
	ListByBackupSet(ctx context.Context, set model.BackupSetID) ([]state.Record, error)

	// The per-copy assurance record (migration 0016). RecordCopyScrub is
	// where a verdict lands; Claim and Release are the lease that makes two
	// scrubbers of one copy impossible, across processes.
	RecordCopyScrub(ctx context.Context, c state.CopyScrub) error
	ClaimCopyScrub(ctx context.Context, a model.ArtifactID, medium, holder string, now time.Time, ttl time.Duration) (bool, error)
	ReleaseCopyScrub(ctx context.Context, a model.ArtifactID, medium, holder string) error
}

// Mediums resolves a placement's medium id into the descriptor a
// MediumStore needs, out of whatever configuration the caller holds.
//
// It is an interface rather than a map because this package must not
// import internal/config: config is where medium truth lives, and a
// package that read it directly would be a second place deciding what a
// medium is. A caller that has no mediums configured supplies nothing,
// and this package then has nothing to check a medium placement with,
// which it reports honestly rather than papering over.
type Mediums interface {
	MediumFor(id string) (transport.Medium, bool)
}

// Deps is what Run is handed.
//
// There is still no Transport here, and the reason has not changed:
// revalidation never re-checks a remote SOURCE, which for a COMPLETE
// artifact is already confirmed gone. What EPIC E adds is the other
// direction, a DESTINATION an artifact's durable copy may now live on, and
// that arrives as a MediumStore plus a way to resolve a medium id.
//
// Both are optional, and both being absent is the ordinary case for every
// deployment that configures no medium: with no store, a medium placement
// is reported as not checked rather than as passed, which is exactly the
// checked-versus-passed distinction this package already draws.
type Deps struct {
	// Journal is both the source of what to consider and the destination
	// for what is found. It is the wider interface rather than
	// lifecycle.Journal because this package has to enumerate a backup set
	// before it can decide anything, which lifecycle never needs to do.
	Journal Journal

	// Store reaches storage mediums. Nil means this deployment cannot
	// reach one, which is true of every deployment that configures none.
	Store placement.Store

	// Mediums resolves a placement's medium id. Nil has the same effect
	// as a nil Store.
	Mediums Mediums

	// Now is injectable so a test can control both what a recorded
	// transition's OccurredAt is stamped with and what a copy's next due
	// time is computed from. Nil means time.Now.
	Now func() time.Time

	// Held reports whether a backup set is held for editing
	// (core/service/edithold.go). A held set is not scrubbed, and the skip
	// is reported rather than silent. Nil means nothing is ever held.
	Held func(set model.BackupSetID) bool

	// Bounds are the pacing knobs (jitter, backoff, lease length). The zero
	// value means the documented defaults.
	Bounds Bounds

	// Limits is the shared admission control for reads. Share one across
	// every Run in a process to make the cap global. Nil means unbounded,
	// which is only right for a test.
	Limits *Limits

	// Holder names this process's lease on a copy. It must differ between a
	// scheduler and a manual scrub so they conflict instead of renewing.
	Holder string

	// OnResult, when non-nil, is called with each artifact's finding as it
	// is concluded. It is the live-progress seam: per-copy progress is
	// delivered here and never persisted.
	OnResult func(Finding)
}

// now resolves the clock, in UTC whichever branch it takes.
func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now().UTC()
}

// lifecycleDeps adapts Deps into the lifecycle.Deps shape Advance needs.
func (d Deps) lifecycleDeps() lifecycle.Deps {
	return lifecycle.Deps{Journal: d.Journal, Now: d.Now}
}

func (d Deps) holder() string {
	if d.Holder != "" {
		return d.Holder
	}
	return "revalidate"
}

// Options narrows or forces one Run. The zero value is the unattended,
// scheduled pass: every due copy, bounded by cfg.MaxPerCycle, at the
// automatic class.
type Options struct {
	// Force checks the selected copies whether or not they are due and
	// without the per-cycle bound, for an operator who named them.
	Force bool

	// Only restricts the pass to one artifact, and Medium to one of its
	// copies. Either implies nothing about due-ness: pair them with Force.
	Only   *model.ArtifactID
	Medium string

	// Content asks for placement.Content against a copy on a storage
	// medium: the object is downloaded and re-hashed. It costs egress, so
	// it is refused unless AllowEgress says the cost was acknowledged, and
	// it is never attempted against an archive-class copy at all.
	Content     bool
	AllowEgress bool
}

// CopyResult is what a scrub concluded about one copy.
type CopyResult struct {
	Medium  string
	Verdict state.ScrubVerdict
	// Class is the class that RAN. Empty when nothing ran.
	Class  placement.Class
	Reason string
	// Refused is true when the read was not made because it would have
	// spent money nobody acknowledged. A refusal is reported, not recorded.
	Refused bool
}

// Finding is one artifact Run examined.
type Finding struct {
	// Artifact is which one was examined.
	Artifact model.ArtifactID

	// From and To are the states either side of what this pass recorded.
	// They are equal for a pass and for a not-checked artifact alike, so
	// they are not on their own a way to tell those two apart: Checked is.
	// To is read back out of the journal's own answer rather than assumed
	// from the requested edge, so a Finding reports where the artifact
	// actually ended up.
	From lifecycle.State
	To   lifecycle.State

	// Checked is false when nothing cfg enables could actually produce a
	// verdict for this specific artifact (see runChecks's doc). When
	// false, Passed is meaningless and no verdict was recorded on any copy.
	Checked bool
	// Passed is this artifact's combined verdict across every tier cfg
	// enabled. Meaningless when Checked is false.
	Passed bool
	// Reason is a short, human-readable explanation, suitable for a log
	// line or an audit trail.
	Reason string

	// Class is the verification class this pass actually ACHIEVED, never
	// the strongest one configured (EPIC E, FR-31). It is empty when
	// Checked is false.
	//
	// It exists because "revalidated" stopped being one thing. An
	// artifact whose durable copy is a local file is re-read and
	// re-hashed, which is placement.Content. An artifact whose durable
	// copy is on a storage medium is HEADed, which is
	// placement.Existence, proves nothing about the bytes, and must never
	// be reported to an operator as the artifact having been revalidated
	// in the sense the local check means. Carrying the class is what lets
	// every surface downstream say which one happened.
	Class placement.Class

	// Copies is one entry per copy the pass looked at.
	Copies []CopyResult
}

// ArtifactError is a per-artifact problem that stopped Run from reaching a
// verdict for exactly one artifact, without aborting the rest of the pass:
// the same convention internal/reconcile.ArtifactError already
// established.
type ArtifactError struct {
	// Artifact names which one could not be given a verdict. Without it a
	// batch of these is unactionable.
	Artifact model.ArtifactID

	// Err is the infrastructure problem, never a verdict. A check that ran
	// and said "this artifact is corrupt" is a Finding with Passed false;
	// only a check that could not run at all lands here.
	Err error
}

// Error renders as "artifact: reason".
func (e ArtifactError) Error() string { return fmt.Sprintf("%s: %v", e.Artifact, e.Err) }

// Unwrap keeps the cause reachable through errors.Is. Run uses that itself,
// through isCancelled, to tell a pass that was stopped from a pass where one
// artifact went wrong, and a caller reading a Report needs the same ability
// for the same reason.
func (e ArtifactError) Unwrap() error { return e.Err }

// SkipReason says why a due copy was not scrubbed. A skip is always visible:
// a copy that was due and silently not looked at is the same failure as one
// that was never scheduled.
type SkipReason string

const (
	// SkipHeld means the copy's backup set is held for editing.
	SkipHeld SkipReason = "held"
	// SkipLeased means another scrubber holds the copy right now.
	SkipLeased SkipReason = "leased"
)

// Skip is one copy a pass did not scrub, and why.
type Skip struct {
	Artifact model.ArtifactID
	// Medium is empty when the whole artifact (or set) was skipped.
	Medium string
	Reason SkipReason
}

// Report is everything one Run call found and did.
type Report struct {
	// Findings is one entry per artifact that reached a conclusion,
	// including the not-checked ones. A not-checked artifact belongs here
	// rather than in Errors because nothing went wrong: the configuration
	// simply enabled nothing that could produce a verdict for it, and
	// reporting that as an error every cycle would train an operator to
	// ignore the list.
	Findings []Finding

	// Errors is one entry per artifact that could not be given a verdict
	// at all. An empty Errors with a short Findings list is the shape of a
	// pass bounded by MaxPerCycle, not the shape of a pass that failed.
	Errors []ArtifactError

	// Skipped is every copy or set the pass was due to scrub and did not.
	Skipped []Skip

	// Selected is how many artifacts the pass set out to look at.
	Selected int

	// BytesRead is the total read off storage by checks that read bytes.
	BytesRead int64
}

// Run performs one scheduled-revalidation pass over set: it loads set's
// journal records, selects which are due for a fresh check (SelectDue, at
// most cfg.MaxPerCycle of them), and re-checks each one against cfg's
// configured tiers.
//
// cfg.MaxPerCycle <= 0 means revalidation is disabled for this backup set
// (config.Validate's own rule is that this is the only way MaxPerCycle can
// be non-positive in a validated config; see validateRevalidation): Run
// returns an empty Report and does not even list the journal, so calling
// it unconditionally every cycle for every backup set is always safe and
// cheap when a set has not opted in.
//
// A non-nil error means an infrastructure problem (a missing Journal, a
// bad backup set id, a listing failure, or the context being cancelled
// partway through): a business outcome ("this artifact failed
// revalidation") is reported through the returned Report, never through
// error. A per-artifact problem that is not itself a verdict (see
// checkArtifact) lands in Report.Errors instead of aborting the rest of
// the pass, the same convention internal/reconcile.Reconcile already
// uses.
func Run(ctx context.Context, deps Deps, set model.BackupSetID, cfg config.Revalidation) (Report, error) {
	return RunWith(ctx, deps, set, cfg, Options{})
}

// RunWith is Run with an operator's narrowing and forcing. The unattended
// scheduler never passes anything but the zero Options.
func RunWith(ctx context.Context, deps Deps, set model.BackupSetID, cfg config.Revalidation, opts Options) (Report, error) {
	if deps.Journal == nil {
		return Report{}, fmt.Errorf("revalidate: Deps needs a Journal")
	}
	if set.IsZero() {
		return Report{}, fmt.Errorf("revalidate: needs a backup set id")
	}
	if cfg.MaxPerCycle <= 0 && !opts.Force {
		return Report{}, nil
	}

	var report Report

	// An edit hold stops a set's scrub exactly as it stops its cycle, and
	// the skip is a line in the report: a hold must never read as "nothing
	// was due".
	if deps.Held != nil && deps.Held(set) {
		report.Skipped = append(report.Skipped, Skip{Reason: SkipHeld})
		return report, nil
	}

	records, err := deps.Journal.ListByBackupSet(ctx, set)
	if err != nil {
		return Report{}, fmt.Errorf("revalidate: listing %s: %w", set, err)
	}

	now := deps.now()
	var due []state.Record
	if opts.Force {
		due = SelectEligible(records)
	} else {
		due = SelectDue(records, cfg, now)
	}
	if opts.Only != nil {
		kept := due[:0:0]
		for _, rec := range due {
			if rec.Artifact == *opts.Only {
				kept = append(kept, rec)
			}
		}
		due = kept
	}
	report.Selected = len(due)

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		stopErr error
	)
	workers := deps.Bounds.withDefaults().GlobalConcurrency
	if workers > len(due) {
		workers = len(due)
	}
	jobs := make(chan state.Record)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rec := range jobs {
				finding, skips, bytes, err := scrubArtifact(ctx, deps, cfg, opts, set, rec, now)
				mu.Lock()
				report.Skipped = append(report.Skipped, skips...)
				report.BytesRead += bytes
				switch {
				case err != nil && isCancelled(err):
					if stopErr == nil {
						stopErr = fmt.Errorf("revalidate: cancelled: %w", err)
					}
				case err != nil:
					report.Errors = append(report.Errors, ArtifactError{Artifact: rec.Artifact, Err: err})
				case finding != nil:
					report.Findings = append(report.Findings, *finding)
				}
				mu.Unlock()
				if finding != nil && deps.OnResult != nil {
					deps.OnResult(*finding)
				}
			}
		}()
	}
feed:
	for _, rec := range due {
		select {
		case jobs <- rec:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if stopErr == nil {
		if err := ctx.Err(); err != nil {
			stopErr = fmt.Errorf("revalidate: cancelled: %w", err)
		}
	}
	// Concurrent workers finish in no particular order. Sorting makes the
	// report, and everything rendered from it, a function of the input.
	sort.Slice(report.Findings, func(i, j int) bool {
		return report.Findings[i].Artifact.String() < report.Findings[j].Artifact.String()
	})
	sort.Slice(report.Errors, func(i, j int) bool { return report.Errors[i].Artifact.String() < report.Errors[j].Artifact.String() })
	sort.Slice(report.Skipped, func(i, j int) bool {
		a, b := report.Skipped[i], report.Skipped[j]
		if a.Artifact.String() != b.Artifact.String() {
			return a.Artifact.String() < b.Artifact.String()
		}
		return a.Medium < b.Medium
	})
	return report, stopErr
}

// scrubArtifact is one artifact's whole scrub: adopt a legacy row, take the
// leases, check, record each copy, and route a failure through the
// lifecycle. A nil finding with a nil error means every copy was skipped.
func scrubArtifact(ctx context.Context, deps Deps, cfg config.Revalidation, opts Options, set model.BackupSetID, rec state.Record, now time.Time) (finding *Finding, skips []Skip, bytes int64, err error) {
	if deps.Held != nil && deps.Held(set) {
		return nil, []Skip{{Artifact: rec.Artifact, Reason: SkipHeld}}, 0, nil
	}

	rec, err = adoptLegacyCopy(ctx, deps, rec)
	if err != nil {
		return nil, nil, 0, err
	}

	// Take the lease on every copy this pass would look at. A copy another
	// scrubber holds is skipped and reported; if that leaves nothing, so is
	// the artifact. The check below is run against the record as read, so a
	// leased copy is also removed from it, otherwise the artifact verdict
	// would be built on a copy this pass never read.
	holder := deps.holder()
	bounds := deps.Bounds.withDefaults()
	var claimed []string
	filtered := rec
	filtered.Placements = nil
	for _, p := range rec.Placements {
		target := p.Status == state.PlacementActive && (opts.Medium == "" || p.Medium == opts.Medium)
		if !target {
			filtered.Placements = append(filtered.Placements, p)
			continue
		}
		if isScrubTarget(rec, p) {
			ok, claimErr := deps.Journal.ClaimCopyScrub(ctx, rec.Artifact, p.Medium, holder, now, bounds.LeaseTTL)
			if claimErr != nil {
				releaseAll(deps, rec.Artifact, claimed, holder)
				return nil, skips, 0, claimErr
			}
			if !ok {
				skips = append(skips, Skip{Artifact: rec.Artifact, Medium: p.Medium, Reason: SkipLeased})
				continue
			}
			claimed = append(claimed, p.Medium)
		}
		filtered.Placements = append(filtered.Placements, p)
	}
	if len(claimed) == 0 {
		return nil, skips, 0, nil
	}
	// Whatever happens next, a lease this call still holds is given back.
	// RecordCopyScrub releases the ones it writes; this covers an error or
	// a cancellation before it got there.
	defer releaseAll(deps, rec.Artifact, claimed, holder)

	f, bytes, err := checkArtifact(ctx, deps, cfg, opts, filtered, now)
	if err != nil {
		return nil, skips, bytes, err
	}
	return &f, skips, bytes, nil
}

// isScrubTarget reports whether p is one of the copies a scrub of rec
// checks, as opposed to an ACTIVE copy the local-first fork leaves alone.
func isScrubTarget(rec state.Record, p state.Placement) bool {
	for _, t := range ScrubTargets(rec) {
		if t.Medium == p.Medium {
			return true
		}
	}
	return false
}

func releaseAll(deps Deps, a model.ArtifactID, mediums []string, holder string) {
	// A cancelled context must not strand a lease, so the release uses its
	// own short-lived one.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, m := range mediums {
		_ = deps.Journal.ReleaseCopyScrub(ctx, a, m, holder)
	}
}

// adoptLegacyCopy gives a durable artifact that has no placement row one,
// so there is somewhere to record a verdict against.
//
// Migration 0007 backfilled a local placement for every artifact that had a
// durable copy, and lifecycle's Commit records one for every artifact
// committed since, so this is for the row neither reached. It is derived
// exactly as the backfill derived it, from the artifact's own recorded
// path and hash, and it claims no verification class: adopting a row is
// not checking it. Without this, such an artifact would sort as never
// scheduled on every pass and spend its slot without any result able to
// land.
func adoptLegacyCopy(ctx context.Context, deps Deps, rec state.Record) (state.Record, error) {
	if len(rec.Placements) > 0 || rec.LocalPath == "" {
		return rec, nil
	}
	cur := lifecycle.State(rec.State)
	upd := &state.PlacementUpdate{
		Medium: state.MediumLocal, Location: rec.LocalPath,
		Hash: rec.LocalHash, HashAlg: rec.LocalHashAlg,
		Status: state.PlacementActive,
	}
	if rec.Transfer != nil {
		n := rec.Transfer.BytesTransferred
		upd.Size = &n
	}
	if _, err := lifecycle.Advance(ctx, deps.lifecycleDeps(), state.Transition{
		Artifact:  rec.Artifact,
		Key:       fmt.Sprintf("revalidate:%s:adopt-local-placement", rec.Artifact),
		From:      string(cur),
		To:        string(cur),
		Detail:    "scrub: recorded the local copy this artifact had no placement row for",
		Placement: upd,
	}); err != nil {
		return rec, fmt.Errorf("adopting the local copy of %s: %w", rec.Artifact, err)
	}
	return deps.Journal.Get(ctx, rec.Artifact)
}

// checkArtifact runs runChecks against rec, records one verdict per copy,
// and only when the artifact as a whole failed routes the same COMMITTED /
// REMOTE_DELETE_PENDING -> QUARANTINED and COMPLETE -> QUARANTINED_LOST
// edges reconcile.go already uses for "found invalid after the fact".
//
// A passed artifact writes nothing to state_transitions. It used to write a
// same-state COMPLETE -> COMPLETE row so the artifact row's clock moved;
// the clock is the copy's now, and a row per passed copy per pass is the
// feed volume the epic rules out.
//
// A non-nil error here means runChecks hit an infrastructure problem for
// this one artifact (see that function's doc): the artifact's lifecycle is
// never touched, and the caller (Run) decides whether that is a per-artifact
// error or a cancellation. The copies that could not be asked still record
// that, with a backoff.
func checkArtifact(ctx context.Context, deps Deps, cfg config.Revalidation, opts Options, rec state.Record, now time.Time) (Finding, int64, error) {
	cur := lifecycle.State(rec.State)
	started := deps.now()

	res, err := runChecks(ctx, deps, cfg, opts, rec)
	if err != nil {
		if isCancelled(err) {
			return Finding{}, 0, err
		}
		// The per-copy outcomes that exist (a bucket that did not answer)
		// are recorded before the error is reported.
		if rerr := recordCopies(ctx, deps, cfg, rec, res.copies, started); rerr != nil {
			err = fmt.Errorf("%w (and recording the copies failed: %v)", err, rerr)
		}
		return Finding{}, 0, err
	}

	bytes, err := func() (int64, error) {
		if err := recordCopies(ctx, deps, cfg, rec, res.copies, started); err != nil {
			return 0, err
		}
		var n int64
		for _, c := range res.copies {
			if c.bytesRead != nil {
				n += *c.bytesRead
			}
		}
		return n, nil
	}()
	if err != nil {
		return Finding{}, bytes, err
	}

	finding := Finding{
		Artifact: rec.Artifact, From: cur, To: cur,
		Checked: res.checked, Passed: res.passed, Reason: res.reason, Class: res.class,
		Copies: copyResults(res.copies),
	}
	if !res.checked || res.passed {
		return finding, bytes, nil
	}

	// A failed recheck: route through the exact same edges reconcile.go
	// already established for "the durable local copy was found invalid
	// after the fact" (see machine.go and this package's own doc). Only
	// COMPLETE routes to QUARANTINED_LOST; every other eligible state,
	// REMOTE_RETAINED (issue #315) included, routes to the ordinary,
	// recoverable QUARANTINED: unlike COMPLETE, none of them has confirmed
	// the remote object gone, so the remote is presumptively still there
	// to recover from.
	to := lifecycle.Quarantined
	if cur == lifecycle.Complete {
		to = lifecycle.QuarantinedLost
	}
	out, err := lifecycle.Advance(ctx, deps.lifecycleDeps(), state.Transition{
		Artifact: rec.Artifact,
		Key:      revalidateKey(rec.Artifact, "fail", cur, to, rec.UpdatedAt),
		From:     string(cur),
		To:       string(to),
		Detail:   fmt.Sprintf("Phase 4: scheduled revalidation at %s class found the artifact's durable copy invalid: %s", res.class, res.reason),
	})
	if err != nil {
		return Finding{}, bytes, fmt.Errorf("quarantining %s after a failed revalidation: %w", rec.Artifact, err)
	}
	finding.To = lifecycle.State(out.Record.State)
	return finding, bytes, nil
}

func copyResults(cs []copyOutcome) []CopyResult {
	out := make([]CopyResult, 0, len(cs))
	for _, c := range cs {
		out = append(out, CopyResult{Medium: c.medium, Verdict: c.verdict, Class: c.class, Reason: c.reason, Refused: c.refused})
	}
	return out
}

// recordCopies writes each copy's outcome onto its placements row, with the
// next due time the pacing rules give it. A refused copy writes nothing: a
// refusal to spend is not a fact about the copy, and recording it would
// overwrite the last real answer with a non-answer.
func recordCopies(ctx context.Context, deps Deps, cfg config.Revalidation, rec state.Record, copies []copyOutcome, started time.Time) error {
	bounds := deps.Bounds.withDefaults()
	interval := cfg.Interval.Duration()
	finished := deps.now()
	byMedium := map[string]state.Placement{}
	for _, p := range rec.Placements {
		byMedium[p.Medium] = p
	}
	for _, c := range copies {
		if c.refused {
			continue
		}
		prior := byMedium[c.medium]
		var next time.Time
		switch c.verdict {
		case state.ScrubFailed, state.ScrubCouldNotComplete:
			next = bounds.NextDueAfterFailure(finished, interval, prior.ConsecutiveFailures+1)
		default:
			next = bounds.NextDueAfterCheck(finished, interval)
		}
		// The scrub record must be written even when ctx is already done:
		// the bytes were read, and throwing the verdict away would send the
		// next pass to read them again.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := deps.Journal.RecordCopyScrub(wctx, state.CopyScrub{
			Artifact: rec.Artifact, Medium: c.medium, Verdict: c.verdict, Class: string(c.class),
			Reason: c.reason, At: finished, NextDue: next, BytesRead: c.bytesRead,
			Duration: finished.Sub(started),
		})
		cancel()
		if err != nil {
			return fmt.Errorf("recording the scrub of %s on %s: %w", rec.Artifact, c.medium, err)
		}
	}
	return nil
}

// revalidateKey mirrors internal/reconcile's own reconcileKey exactly: an
// idempotency key derived from the artifact, a tag distinguishing which of
// this package's writes it is for, the edge, and the journal snapshot's
// own UpdatedAt. Deriving it from UpdatedAt, rather than from wall-clock
// time or a counter, is what makes two calls against the same stale
// snapshot (the shape of a crash between reading the record and writing
// the result) idempotent, while a later call against a snapshot whose
// UpdatedAt has actually moved on gets a fresh key, exactly like
// reconcile.go's own reasoning.
func revalidateKey(artifact model.ArtifactID, tag string, from, to lifecycle.State, updatedAt time.Time) string {
	return fmt.Sprintf("revalidate:%s:%s:%s->%s@%s", artifact, tag, from, to, updatedAt.UTC().Format(time.RFC3339Nano))
}

// isCancelled reports whether err represents this call being stopped
// externally rather than a check failing on its own terms. Mirrors
// verify.go's isCancellation for the same reason: retry.Do and this
// package's own context handling both need a way to tell "the caller
// asked us to stop" apart from every other kind of failure, which must
// still fall through to a real business verdict.
//
// It mirrors that function's ordering too, and for the reason spelled out
// there: a transport.Error keeps its cause reachable through Unwrap, so an
// error already classified as anything other than Cancelled can still
// answer errors.Is(err, context.DeadlineExceeded), and a connect timeout
// rclone imposed on itself is that exact shape (issue #388). Nothing on
// this package's current call graph hands it one, because runChecks
// reaches the local filesystem and the restore-test hook rather than a
// Transport, but the two predicates disagreeing is not a difference worth
// leaving here for the first remote check to discover.
func isCancelled(err error) bool {
	if err == nil {
		return false
	}
	if category, ok := transport.CategoryOf(err); ok {
		return category == transport.Cancelled
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
