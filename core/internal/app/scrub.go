package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/revalidate"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// EPIC Q, #978: the scrub driver.
//
// internal/revalidate re-checks the durable copies of a backup set, bounded
// and cost-aware, and for a long time nothing ran it. This file is what does:
// one entry point, Scrub, that the scheduler's tick and the operator's
// `retnd scrub` both call, so there is one scrub and not a scheduled one and
// a manual one that drift apart.
//
// # What it deliberately does not do
//
// It runs no second scrub: every check is revalidate's. It has no
// unattended class above existence against a storage medium, and it never
// starts a restore. It writes no per-copy row to the activity feed: the
// verdict lands on the copy, the pass lands as one row of counts
// (state.ScrubPass), and the only feed rows are the quarantines a failed
// artifact causes.
//
// # How it relates to the cycle
//
// A scrub tick is separate from the processing cycle for the reason the
// alerting tick is: a cycle wedged on one slow transfer must not stop copies
// being checked, and a long content scrub must not hold up a backup. So it
// does not hold the single-flight lock the cycle and API-submitted
// operations share. It is still polite to it: the caller (core/service)
// skips a tick that lands while an operation is running, and the copy-level
// lease plus the re-read before any quarantine (revalidate.checkArtifact)
// are what make an overlap with a cycle safe rather than merely unlikely.

// ScrubRequest says what a scrub is asked to cover.
type ScrubRequest struct {
	// Trigger is state.ScrubBySchedule or state.ScrubByManual.
	Trigger string

	// Set, Artifact and Medium narrow the scrub. All three empty means "every
	// due copy in every enabled set", which is what the scheduler asks. Naming
	// a set, an artifact or a copy is an operator saying "now", so it forces
	// the check whether or not the copy is due and without the per-cycle bound.
	Set      *model.BackupSetID
	Artifact *model.ArtifactID
	Medium   string

	// Content asks for the bytes of a copy on a storage medium to be read.
	// AllowEgress is the operator's acknowledgement that doing so costs
	// money; without it the read is refused, and with it an archive-class
	// copy is still never read.
	Content     bool
	AllowEgress bool
}

func (r ScrubRequest) forced() bool { return r.Set != nil || r.Artifact != nil }

// ScrubSetReport is one backup set's part of a scrub.
type ScrubSetReport struct {
	Set    model.BackupSetID
	Report revalidate.Report
	Err    error
}

// ScrubSummary is what a scrub did, as counts plus the per-set detail the
// CLI prints.
type ScrubSummary struct {
	Trigger string
	Scope   string
	Sets    []ScrubSetReport

	Selected, Checked, Passed, Failed, CouldNotComplete, RequiresRestore int
	SkippedHeld, SkippedLeased, Refused                                  int
	BytesRead                                                            int64

	// Unknown counts objects destinations hold at keys the journal does not
	// know (QC-04), and Unlisted the destinations a pass could not enumerate.
	// Neither is a copy outcome: an unknown object is never Passed, and
	// neither adds to Failed or to the scrub_failure lines.
	Unknown, Unlisted int

	// PassID is the durable pass row, or 0 when the pass had nothing to
	// record (an idle tick writes no row, or a quiet deployment would grow
	// an append-only table by one row per poll for ever).
	PassID int64
}

// ErrScrubUnavailable is returned when this Service has no journal that can
// hold a per-copy record, which is true of a test double and of nothing in
// production.
var ErrScrubUnavailable = errors.New("app: this journal cannot record a per-copy scrub")

// scrubJournal is what a scrub needs of the journal beyond app.Journal. It is
// asserted rather than added to Journal so that the many test doubles of
// that interface keep compiling, and a deployment's real journal is the one
// type that satisfies it.
type scrubJournal interface {
	revalidate.Journal
	RecordScrubPass(ctx context.Context, p state.ScrubPass) (int64, error)
}

// scrubSeq makes every call's lease holder distinct, so two scrubs in one
// process conflict on a copy instead of renewing each other's lease.
var scrubSeq atomic.Uint64

type scrubLimitsState struct {
	once   sync.Once
	limits *revalidate.Limits
}

// scrubLimits is this process's shared admission control for scrub reads.
// It is created once, so the scheduler tick and a manual scrub in the same
// process draw on one global cap.
//
// The two ceilings are the operator's own (assurance.max_concurrent_full_reads
// and assurance.max_per_destination), as the process first scrubbed. They are
// read once because a limiter that is resized while reads hold its slots
// would admit more than either number allows, so a change to them applies
// from the next start. A hand-built Config that never ran Validate carries
// zeros, which the limiter fills with the same defaults.
func (s *Service) scrubLimitsShared() *revalidate.Limits {
	s.scrub.once.Do(func() {
		s.scrub.limits = revalidate.NewLimits(revalidate.Bounds{
			GlobalConcurrency:         s.Config.Assurance.MaxConcurrentFullReads,
			PerDestinationConcurrency: s.Config.Assurance.MaxPerDestination,
		})
	})
	return s.scrub.limits
}

type configMediums struct{ cfg *config.Config }

func (m configMediums) MediumFor(id string) (transport.Medium, bool) {
	medium, _, err := MediumFor(m.cfg, id)
	return medium, err == nil
}

// Scrub runs one scrub pass as req describes and returns what it did.
//
// A non-nil error means the pass could not run at all (a journal that cannot
// hold the record, a set that is not configured, a cancelled context). A
// copy that failed is a count in the summary, never an error.
func (s *Service) Scrub(ctx context.Context, req ScrubRequest) (ScrubSummary, error) {
	journal, ok := s.Journal.(scrubJournal)
	if !ok {
		return ScrubSummary{}, ErrScrubUnavailable
	}
	if req.Trigger != state.ScrubBySchedule && req.Trigger != state.ScrubByManual {
		return ScrubSummary{}, fmt.Errorf("app: %q is not a scrub trigger", req.Trigger)
	}

	started := s.now()
	sum := ScrubSummary{Trigger: req.Trigger, Scope: scrubScope(req)}

	sets, err := s.scrubTargets(req)
	if err != nil {
		return sum, err
	}

	holds := BackupSetHoldsFrom(ctx)
	deps := revalidate.Deps{
		Journal: journal,
		Mediums: configMediums{s.Config},
		Now:     s.Now,
		Held: func(set model.BackupSetID) bool {
			return holds != nil && holds.Held(set.String())
		},
		Limits: s.scrubLimitsShared(),
		Holder: fmt.Sprintf("%s:%d:%d", req.Trigger, os.Getpid(), scrubSeq.Add(1)),
	}
	if s.MediumStore != nil {
		deps.Store = s.MediumStore
	}

	for _, bs := range sets {
		if err := ctx.Err(); err != nil {
			return s.finishScrub(ctx, journal, sum, started), fmt.Errorf("app: scrub cancelled: %w", err)
		}
		// The scrub's configuration comes from the set's RESOLVED assurance
		// policy, so a default install scrubs and assurance.scrub.cadence is
		// the schedule the driver actually keeps. An explicit, enabled
		// `revalidation:` block still wins (config.Assurance.Revalidation).
		cfg := bs.Assurance.Revalidation(bs.Revalidation)
		if req.forced() {
			// An operator who names a copy has asked for the bytes to be
			// read, and re-reading a local file costs nothing, so the hash
			// tier is on whatever the set configured for its schedule.
			cfg.Hash = true
		}
		setCtx, cancel, wasHeld := withHoldCancellation(ctx, bs.ID.String())
		opts := revalidate.Options{
			Force: req.forced(), Only: req.Artifact, Medium: req.Medium,
			Content: req.Content, AllowEgress: req.AllowEgress,
			// An operator who ran a scrub asked to be told what is there,
			// even on a day nothing is due. The scheduler lists only when
			// it checked something, so an idle poll costs no listing.
			ListDestinations: req.Trigger == state.ScrubByManual,
		}
		report, runErr := revalidate.RunWith(setCtx, deps, bs.ID, cfg, opts)
		cancel()
		if runErr != nil && wasHeld() {
			// A hold that fired mid-pass stopped the set, which is the
			// same visible skip as a set that was already held.
			report.Skipped = append(report.Skipped, revalidate.Skip{Reason: revalidate.SkipHeld})
			runErr = nil
		}
		sum.Sets = append(sum.Sets, ScrubSetReport{Set: bs.ID, Report: report, Err: runErr})
		s.tally(ctx, &sum, bs.ID, report)
		if runErr != nil && ctx.Err() != nil {
			return s.finishScrub(ctx, journal, sum, started), fmt.Errorf("app: scrub cancelled: %w", runErr)
		}
	}
	out := s.finishScrub(ctx, journal, sum, started)
	// After the verdicts are durable, so the script is told what is true.
	// Only for sets this pass actually looked at: an episode can only open
	// or close because a copy was checked, and a quiet tick that listed
	// every set's journal just to find nothing changed would be a read per
	// set per poll for no reason.
	var touched []config.BackupSet
	for i, bs := range sets {
		if i < len(sum.Sets) && sum.Sets[i].Report.Selected > 0 {
			touched = append(touched, bs)
		}
	}
	s.settleScrubEpisodes(ctx, touched)

	return out, nil
}

// tally folds one set's report into the pass counts and emits the one
// per-copy line a pass writes: for a copy that failed or could not be
// checked.
//
// Every copy that does not pass gets exactly one scrub_failure line, and the
// count of those lines is the pass's Failed plus CouldNotComplete. Objects at
// unknown keys and destinations that could not be listed are counted and
// logged on their own events and never as failures. That holds
// for an artifact the pass could not finish too: its error carries the
// outcomes of the copies that were asked, and each is reported as itself, so a
// copy the journal records as failed is never folded into its neighbour's
// "could not be checked".
func (s *Service) tally(ctx context.Context, sum *ScrubSummary, set model.BackupSetID, r revalidate.Report) {
	sum.Selected += r.Selected
	sum.BytesRead += r.BytesRead
	for _, f := range r.Findings {
		for _, c := range f.Copies {
			s.tallyCopy(ctx, sum, f.Artifact, c)
		}
	}
	for _, e := range r.Errors {
		reported := false
		for _, c := range e.Copies {
			if s.tallyCopy(ctx, sum, e.Artifact, c) {
				reported = true
			}
		}
		if reported {
			continue
		}
		// The problem came before any copy was asked, or every copy that was
		// asked passed and the trouble was elsewhere (a hook that would not
		// start). The artifact could not be checked, and it is said once.
		sum.CouldNotComplete++
		s.logger().ScrubFailure(ctx, e.Artifact.String(), "", "", e.Err.Error())
	}
	// QC-04: what a destination holds that the journal does not know, and the
	// destinations that could not be asked. Findings, not copy outcomes: none
	// of it touches Passed, Failed or the scrub_failure lines.
	for _, u := range r.Unknown {
		sum.Unknown++
		s.logger().ScrubUnknownObject(ctx, set.String(), u.Medium, u.Key, u.Size)
	}
	for _, u := range r.Unlisted {
		sum.Unlisted++
		s.logger().ScrubUnlisted(ctx, set.String(), u.Medium, u.Reason)
	}
	for _, sk := range r.Skipped {
		switch sk.Reason {
		case revalidate.SkipHeld:
			sum.SkippedHeld++
		case revalidate.SkipLeased:
			sum.SkippedLeased++
		}
	}
}

// tallyCopy counts one copy's outcome and writes its scrub_failure line when
// it failed or could not be checked. It reports whether it wrote one.
func (s *Service) tallyCopy(ctx context.Context, sum *ScrubSummary, a model.ArtifactID, c revalidate.CopyResult) bool {
	switch {
	case c.Refused:
		sum.Refused++
	case c.Verdict == state.ScrubPassed:
		sum.Checked++
		sum.Passed++
	case c.Verdict == state.ScrubRequiresRestore:
		sum.Checked++
		sum.RequiresRestore++
	case c.Verdict == state.ScrubFailed:
		sum.Checked++
		sum.Failed++
		s.logger().ScrubFailure(ctx, a.String(), c.Medium, string(c.Class), c.Reason)
		return true
	case c.Verdict == state.ScrubCouldNotComplete:
		sum.CouldNotComplete++
		s.logger().ScrubFailure(ctx, a.String(), c.Medium, string(c.Class), c.Reason)
		return true
	}
	return false
}

// finishScrub writes the durable pass row, when the pass did anything worth
// a row, and the one pass event. Both happen even when the pass was cut
// short: counts of what did run are still true.
func (s *Service) finishScrub(ctx context.Context, journal scrubJournal, sum ScrubSummary, started time.Time) ScrubSummary {
	finished := s.now()
	if sum.Selected > 0 || sum.SkippedHeld > 0 || sum.SkippedLeased > 0 || sum.CouldNotComplete > 0 {
		id, err := journal.RecordScrubPass(context.WithoutCancel(ctx), state.ScrubPass{
			TriggeredBy: sum.Trigger, Scope: sum.Scope, StartedAt: started, FinishedAt: finished,
			Selected: sum.Selected, Checked: sum.Checked, Passed: sum.Passed, Failed: sum.Failed,
			CouldNotComplete: sum.CouldNotComplete, RequiresRestore: sum.RequiresRestore,
			SkippedHeld: sum.SkippedHeld, SkippedLeased: sum.SkippedLeased, BytesRead: sum.BytesRead,
		})
		if err != nil {
			s.logger().Error(ctx, "scrub-pass-record", err)
		}
		sum.PassID = id
		s.logger().ScrubPass(ctx, obs.ScrubPassCounts{
			Trigger: sum.Trigger, Scope: sum.Scope, Selected: sum.Selected, Checked: sum.Checked,
			Passed: sum.Passed, Failed: sum.Failed, CouldNotComplete: sum.CouldNotComplete,
			RequiresRestore: sum.RequiresRestore, SkippedHeld: sum.SkippedHeld,
			SkippedLeased: sum.SkippedLeased, BytesRead: sum.BytesRead,
		})
	}
	return sum
}

func scrubScope(r ScrubRequest) string {
	switch {
	case r.Artifact != nil && r.Medium != "":
		return fmt.Sprintf("copy %s on %s", r.Artifact, r.Medium)
	case r.Artifact != nil:
		return fmt.Sprintf("artifact %s", r.Artifact)
	case r.Set != nil:
		return fmt.Sprintf("set %s", r.Set)
	default:
		return "all due"
	}
}

// scrubTargets resolves the sets a request covers. Disabled sets are left
// alone (an operator who switched a set off asked for it to be untouched),
// and so are sets on the incremental engine, which keep no artifacts and
// have a verification ladder of their own that this does not fork.
func (s *Service) scrubTargets(req ScrubRequest) ([]config.BackupSet, error) {
	want := req.Set
	if want == nil && req.Artifact != nil {
		set := req.Artifact.Set
		want = &set
	}
	var out []config.BackupSet
	found := false
	for _, src := range s.Config.Sources {
		for _, bs := range src.BackupSets {
			if want != nil && bs.ID != *want {
				continue
			}
			found = true
			if bs.Disabled || unrunnableEngine(bs.Engine) != nil {
				continue
			}
			out = append(out, bs)
		}
	}
	if want != nil && !found {
		return nil, fmt.Errorf("app: backup set %s is not configured", want)
	}
	return out, nil
}

// ScrubTick is one scheduled pass. It is what a timer calls, and what the
// CLI daemon and the generic web host both run beside their alert tick.
func (s *Service) ScrubTick(ctx context.Context) {
	if _, err := s.Scrub(ctx, ScrubRequest{Trigger: state.ScrubBySchedule}); err != nil && ctx.Err() == nil {
		s.logger().Error(ctx, "scrub-tick", err)
	}
}

// runScrubTicks repeats ScrubTick every interval until ctx is done, with the
// timer shape the alert ticks use: one timer, reset only after the pass
// returns, so a slow pass makes the next late and never overlaps it.
func (s *Service) runScrubTicks(ctx context.Context, interval time.Duration) {
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.scrubTickRecovered(ctx)
	}
}

// scrubTickRecovered keeps a panic in a scrub from taking the process down:
// the CLI daemon and the web host both run this beside the thing they exist
// for.
func (s *Service) scrubTickRecovered(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger().Error(context.Background(), "scrub-tick-panic", fmt.Errorf("recovered panic: %v", r))
		}
	}()
	s.ScrubTick(ctx)
}
