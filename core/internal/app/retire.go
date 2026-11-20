package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/discovery"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1528: retiring a FAILED backup whose source is gone.
//
// A FAILED artifact used to have one way out, retry, and for an artifact whose
// source object the host has since deleted that way only leads back into
// FAILED. The set then read FAILING forever with good backups arriving every
// day. RETIRED is the second way out, and this file is the whole rule for
// taking it, shared by the three callers that can: the operator (CLI and API,
// both through RetireArtifact), an agent whose proposal a person approved
// (the same method, with the acknowledgements refused one layer up), and the
// cycle itself (autoRetire). docs/design/1528-retire-failed-artifacts.md is the
// argument; what follows is the mechanism.
//
// The one rule underneath all three:
//
//   - only a FAILED artifact can be retired. One whose own durable copy sits
//     at its final name (issue #662's shape) is refused while its source is
//     there or unknown, because retry rescues it; with the source confirmed
//     gone it is retired only on the operator's accept_unverified_copy
//     acknowledgement, only when the copy really does not verify, and the
//     file is kept (issue #1594, docs/design/1594-own-copy-source-gone.md);
//   - the source is asked, now: a fresh listing of the set and a Stat of the
//     object's remote path. Only a listing that does not show it AND a Stat
//     classified not_found proves it gone. Anything else is "present" or
//     "unknown", and an operator has to acknowledge either one explicitly;
//   - a newer good backup in the same set has to supersede it, unless the
//     operator acknowledges there is none;
//   - the transition and its retirement record are one journal write; the
//     leftover .partial is removed after it, and swept every cycle.
//
// Nothing here parses a FAILED row's detail text (failure-safety invariant
// 12). The failure reason is carried into the record for the audit and never
// branched on.

// RetireRefusal is a named reason a retirement was not made. Code is the
// wire code the API returns and the preview reports; Sentence is what an
// operator reads, built from fixed text and never from a remote string;
// OverridableWith names the acknowledgement that would let the operator go
// ahead anyway, or is empty when nothing can.
type RetireRefusal struct {
	Code            string
	Sentence        string
	OverridableWith string
}

func (r *RetireRefusal) Error() string { return "app: retire refused (" + r.Code + "): " + r.Sentence }

// AsRetireRefusal reports whether err is, or wraps, a *RetireRefusal.
func AsRetireRefusal(err error) (*RetireRefusal, bool) {
	var r *RetireRefusal
	return r, errors.As(err, &r)
}

// The refusal codes. They are wire codes, so renaming one is a breaking
// change for every client that matches on it.
const (
	RefusalNotFailed          = "ARTIFACT_NOT_FAILED"
	RefusalOwnCopy            = "RETIRE_OWN_COPY_AT_FINAL_NAME"
	RefusalSourcePresent      = "RETIRE_SOURCE_PRESENT"
	RefusalSourceUnconfirmed  = "RETIRE_SOURCE_UNCONFIRMED"
	RefusalNoSuccessor        = "RETIRE_NO_SUCCESSOR"
	RefusalSuccessorInvalid   = "RETIRE_SUCCESSOR_INVALID"
	AckAcceptSourcePresent    = "accept_source_present"
	AckNoSuccessor            = "no_successor"
	AckAcceptUnverifiedCopy   = "accept_unverified_copy"
	retireAutomaticActor      = "engine"
	retireKeyPrefixOperator   = "app:retire:"
	retireKeyPrefixAutomatic  = "app:auto-retire:"
	retireKeyPrefixWayBack    = "app:unretire:"
	retireEffectRecordStays   = "The backup record stays, marked RETIRED, with who retired it, when and why."
	retireEffectStopsCounting = "This backup set stops counting it as a failure. It shows under retired instead."
	retireEffectPartial       = "The leftover partial download for this backup is removed."
	retireEffectNothingElse   = "Nothing on the source is touched and no backup copy is deleted."
	retireEffectWayBack       = "If the source object appears again, the next cycle puts this backup back into the pipeline."
	retireEffectNeverFetched  = "This manager will not fetch this object again."
	retireEffectKeptCopy      = "The file at its final name, %s, is kept where it is. It could not be verified, so it is not counted as a backup, and nothing will prune it."
)

// RetireRequest is what a caller asks for. The zero value is the
// conservative retirement: the engine picks the successor and refuses
// anything it cannot prove.
type RetireRequest struct {
	// SupersededBy names the successor instead of letting the engine pick
	// the newest good backup. It must be newer, in the same set, and a
	// durable restore point.
	SupersededBy string
	// NoSuccessor acknowledges that no newer good backup replaces this one.
	NoSuccessor bool
	// AcceptSourcePresent acknowledges that the source object is still
	// there, or could not be asked about, and retires it anyway.
	AcceptSourcePresent bool
	// AcceptUnverifiedCopy acknowledges, for issue #1594's shape (the row's
	// own copy at its final name, the source confirmed gone), that the copy
	// could not be verified, and retires the row anyway. The file is kept.
	AcceptUnverifiedCopy bool
	// Note is recorded with the retirement.
	Note string
	// ActorKind is state.RetiredByOperator or state.RetiredByAgent; Actor
	// names who. Empty means an operator nobody named.
	ActorKind string
	Actor     string
}

// SourceCheck is what the source said about one object, just now.
type SourceCheck struct {
	// Result is state.SourceAbsent, state.SourcePresent or state.SourceUnknown.
	Result string
	// Category is the transport category behind an unknown result
	// ("transient", "permission_denied", ...), empty otherwise.
	Category string
	// Sentence is the engine's own sentence for Result.
	Sentence string
}

// Successor is the newer good backup a retirement names.
type Successor struct {
	Name         string
	State        lifecycle.State
	DiscoveredAt time.Time
}

// RetirementPreview is everything the retire dialog shows before anybody
// acts, computed by the same code that acts (issue #1528's UI contract).
type RetirementPreview struct {
	Artifact          model.ArtifactID
	State             lifecycle.State
	FailureReason     string
	SourceCheck       SourceCheck
	ProposedSuccessor *Successor
	// SuccessorCandidates is every newer good backup in the set, newest
	// first: what an operator may name with SupersededBy instead.
	SuccessorCandidates []Successor
	Refusal             *RetireRefusal
	Effects             []string
	// Retirement is the latest record for an artifact that is RETIRED now.
	Retirement *state.Retirement
}

// RetireResult is what a successful retirement did.
type RetireResult struct {
	Retirement     state.Retirement
	PartialRemoved bool
}

// retirePlan is one evaluation of the rule against one artifact.
type retirePlan struct {
	rec           state.Record
	bs            config.BackupSet
	check         SourceCheck
	successor     *Successor
	candidates    []Successor
	partial       string
	partialExists bool
	failureReason string
	refusal       *RetireRefusal
	// keptCopy is the path of issue #1594's unverified own copy, which the
	// retirement leaves where it is; empty for every other row.
	keptCopy string
}

// sourceCheckFor turns the two probes into one verdict. The listing is
// consulted first because it is what discovery itself trusts; Stat is the
// per-object, classified answer, and only its not_found counts as absence.
func sourceCheckFor(remotePath string, listing []transport.RemoteArtifact, listErr error, stat func() error) SourceCheck {
	unknown := func(err error) SourceCheck {
		cat := transport.Unclassified
		if c, ok := transport.CategoryOf(err); ok {
			cat = c
		}
		return SourceCheck{Result: state.SourceUnknown, Category: cat.String(), Sentence: fmt.Sprintf(
			"The source could not be asked about this object (%s), so this manager cannot say whether it is gone.", cat)}
	}
	present := SourceCheck{Result: state.SourcePresent,
		Sentence: "The source object is still there, so this backup can still be fetched. Retry it instead."}
	if listErr != nil {
		return unknown(listErr)
	}
	// An artifact's identity is its basename (discovery builds the id from
	// path.Base), so an object listed anywhere under that name is this
	// artifact as far as discovery is concerned: a producer that moved
	// db.dump into archive/ has not deleted it. Calling that gone would
	// retire it with no way back, because discovery files the moved path as
	// a conflict, never as this artifact reappearing.
	name := path.Base(remotePath)
	for _, a := range listing {
		if a.Path == remotePath || path.Base(a.Path) == name {
			return present
		}
	}
	err := stat()
	if err == nil {
		return present
	}
	if c, ok := transport.CategoryOf(err); ok && c == transport.NotFound {
		return SourceCheck{Result: state.SourceAbsent,
			Sentence: "The source object is gone: this backup set's listing does not show it and the source answered not found for it."}
	}
	return unknown(err)
}

// sourceListing is one fresh listing of a set's source, taken at most once
// and shared by every artifact the same pass asks about. The automatic pass
// would otherwise walk the whole source once per FAILED row, every cycle,
// against hosts that cap connections.
type sourceListing struct {
	taken bool
	items []transport.RemoteArtifact
	err   error
}

// probe asks the source about one object: the listing (fresh, or the one this
// pass already took when listing is non-nil) and a Stat. A missing transport
// is an unknown, never an absence.
func (s *Service) probe(ctx context.Context, src config.Source, bs config.BackupSet, remotePath string, listing *sourceListing) SourceCheck {
	if s.Transport == nil {
		return sourceCheckFor(remotePath, nil, errors.New("no transport is configured"), nil)
	}
	source := sourceFor(s.Config, src, bs)
	if listing == nil {
		listing = &sourceListing{}
	}
	if !listing.taken {
		listing.items, listing.err = s.Transport.List(ctx, source)
		listing.taken = true
	}
	return sourceCheckFor(remotePath, listing.items, listing.err, func() error {
		_, err := s.Transport.Stat(ctx, source, remotePath)
		return err
	})
}

// newerGood reports whether candidate supersedes failed: another row in the
// set, a durable restore point, discovered strictly later, and, when both
// rows recorded a remote modification time, modified strictly later too.
func newerGood(failed, candidate state.Record) bool {
	if candidate.Artifact == failed.Artifact || !lifecycle.IsDurableRestorePoint(lifecycle.State(candidate.State)) {
		return false
	}
	if !candidate.DiscoveredAt.After(failed.DiscoveredAt) {
		return false
	}
	if failed.Remote.ModTime != nil && candidate.Remote.ModTime != nil && !candidate.Remote.ModTime.After(*failed.Remote.ModTime) {
		return false
	}
	return true
}

// successorsOf is every row that supersedes failed, newest first by
// discovery time and then name: the choices the retire dialog offers.
func successorsOf(failed state.Record, records []state.Record) []Successor {
	var out []Successor
	for _, r := range records {
		if newerGood(failed, r) {
			out = append(out, Successor{Name: r.Artifact.Name, State: lifecycle.State(r.State), DiscoveredAt: r.DiscoveredAt})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DiscoveredAt.Equal(out[j].DiscoveredAt) {
			return out[i].DiscoveredAt.After(out[j].DiscoveredAt)
		}
		return out[i].Name > out[j].Name
	})
	return out
}

// pickSuccessor is the newest row that supersedes failed, by discovery time
// and then name, or nil.
func pickSuccessor(failed state.Record, records []state.Record) *Successor {
	var best *state.Record
	for i := range records {
		r := records[i]
		if !newerGood(failed, r) {
			continue
		}
		if best == nil || r.DiscoveredAt.After(best.DiscoveredAt) ||
			(r.DiscoveredAt.Equal(best.DiscoveredAt) && r.Artifact.Name > best.Artifact.Name) {
			best = &records[i]
		}
	}
	if best == nil {
		return nil
	}
	return &Successor{Name: best.Artifact.Name, State: lifecycle.State(best.State), DiscoveredAt: best.DiscoveredAt}
}

// evaluate applies the rule to one artifact. It returns a plan with a
// refusal rather than an error for every reason the rule says no, so the
// preview and the action report the same thing; an error is something
// broken (the journal could not be read).
func (s *Service) evaluate(ctx context.Context, rec state.Record, src config.Source, bs config.BackupSet, req RetireRequest, listing *sourceListing) (retirePlan, error) {
	id := rec.Artifact
	p := retirePlan{rec: rec, bs: bs}

	if detail, _, found, err := s.Journal.LastEnteredDetail(ctx, id, string(lifecycle.Failed)); err == nil && found {
		p.failureReason = detail
	}
	if partial, err := lifecycle.PartialArtifactPath(bs.LocalPath, id); err == nil {
		p.partial = partial
		if info, err := os.Lstat(partial); err == nil && info.Mode().IsRegular() {
			p.partialExists = true
		}
	}

	if lifecycle.State(rec.State) != lifecycle.Failed {
		p.refusal = &RetireRefusal{Code: RefusalNotFailed, Sentence: fmt.Sprintf(
			"This backup is %s, not FAILED. Only a failed backup can be retired.", rec.State)}
		return p, nil
	}

	// Issue #662's shape: the row's own durable copy at its final name.
	// #1528 refused it outright because retry rescues it, by comparing the
	// copy with the remote object. Issue #1594: with the source gone that
	// rescue is gone too, so the answer depends on the source and, only when
	// the operator acknowledges it, on the copy.
	ownCopy := ownCopyAtFinalName(rec, bs)
	if ownCopy {
		p.check = s.probe(ctx, src, bs, rec.RemotePath, listing) // the own copy's source (#1594)
		if refusal, err := s.ownCopyRefusal(ctx, rec, bs, p.check, req); err != nil || refusal != nil {
			p.refusal = refusal
			return p, err
		}
		p.keptCopy = rec.LocalPath
	}

	records, err := s.Journal.ListByBackupSet(ctx, id.Set)
	if err != nil {
		return p, fmt.Errorf("app: retire: listing %s: %w", id.Set, err)
	}
	p.candidates = successorsOf(rec, records)
	if req.SupersededBy != "" {
		var named *state.Record
		for i := range records {
			if records[i].Artifact.Name == req.SupersededBy {
				named = &records[i]
			}
		}
		if named == nil || !newerGood(rec, *named) {
			p.refusal = &RetireRefusal{Code: RefusalSuccessorInvalid, Sentence: "The named successor is not a newer, good backup " +
				"in this backup set. Name one that is COMMITTED, COMPLETE or retained and was discovered after this one, or leave it out."}
			return p, nil
		}
		p.successor = &Successor{Name: named.Artifact.Name, State: lifecycle.State(named.State), DiscoveredAt: named.DiscoveredAt}
	} else {
		p.successor = pickSuccessor(rec, records)
	}

	if !ownCopy {
		p.check = s.probe(ctx, src, bs, rec.RemotePath, listing)
	}
	switch {
	case p.check.Result == state.SourcePresent && !req.AcceptSourcePresent:
		p.refusal = &RetireRefusal{Code: RefusalSourcePresent, Sentence: p.check.Sentence, OverridableWith: AckAcceptSourcePresent}
	case p.check.Result == state.SourceUnknown && !req.AcceptSourcePresent:
		p.refusal = &RetireRefusal{Code: RefusalSourceUnconfirmed, Sentence: p.check.Sentence, OverridableWith: AckAcceptSourcePresent}
	case p.successor == nil && !req.NoSuccessor:
		p.refusal = &RetireRefusal{Code: RefusalNoSuccessor, Sentence: "No newer good backup in this backup set replaces this one. " +
			"Acknowledge that there is no replacement to retire it anyway.", OverridableWith: AckNoSuccessor}
	}
	return p, nil
}

// ownCopyRefusal is issue #1594's part of the rule, for a row whose own copy
// sits at its final name. It returns nil when the retirement may go on: the
// source is confirmed gone, the operator acknowledged an unverified copy, and
// the copy really does not verify.
//
// The copy is read only when the acknowledgement is there. The backup page
// asks for the plain preview every time it shows a FAILED backup, and hashing
// a large dump on every page load is not a cost a preview gets to spend; the
// plain answer says what the acknowledged one would check.
func (s *Service) ownCopyRefusal(ctx context.Context, rec state.Record, bs config.BackupSet, check SourceCheck, req RetireRequest) (*RetireRefusal, error) {
	if check.Result != state.SourceAbsent {
		return &RetireRefusal{Code: RefusalOwnCopy, Sentence: "This backup's own copy is already at its final name. " +
			"Retry it instead: retry checks that copy against the source and keeps it."}, nil
	}
	if !req.AcceptUnverifiedCopy {
		return &RetireRefusal{Code: RefusalOwnCopy, OverridableWith: AckAcceptUnverifiedCopy, Sentence: "This backup's source is gone " +
			"from the host, but its own copy is at its final name. Retry it: retry keeps that copy if it still matches the checksum " +
			"recorded when it was verified. If it does not, it can only be retired with the acknowledgement that the copy could not " +
			"be verified, and the file is kept where it is."}, nil
	}
	checks, err := s.runValidationChecks(ctx, rec, bs.Validation, ValidateOptions{})
	if err != nil {
		return nil, fmt.Errorf("app: retire: checking %s's own copy at %s: %w", rec.Artifact, rec.LocalPath, err)
	}
	if checks.Passed && lifecycle.CheckReinstatementEvidence(rec, checks.evidence()) == nil {
		return &RetireRefusal{Code: RefusalOwnCopy, Sentence: "This backup's own copy at its final name still matches the checksum " +
			"recorded when it was verified, so it is a good backup and is not retired. Retry it: with the source gone, retry keeps " +
			"that copy."}, nil
	}
	return nil, nil
}

func (p retirePlan) effects(req RetireRequest) []string {
	out := []string{retireEffectRecordStays, retireEffectStopsCounting}
	if p.partialExists {
		out = append(out, retireEffectPartial)
	}
	if p.keptCopy != "" {
		out = append(out, fmt.Sprintf(retireEffectKeptCopy, p.keptCopy))
	}
	out = append(out, retireEffectNothingElse)
	switch {
	case p.check.Result == state.SourceAbsent:
		out = append(out, retireEffectWayBack)
	case req.AcceptSourcePresent:
		out = append(out, retireEffectNeverFetched)
	}
	return out
}

// lookupForRetire is the part of every entry point that finds the row and
// its set's configuration, refusing a set the configuration no longer names
// the way every other artifact action does (issue #391).
func (s *Service) lookupForRetire(ctx context.Context, id model.ArtifactID) (state.Record, config.Source, config.BackupSet, error) {
	rec, err := s.Journal.Get(ctx, id)
	if err != nil {
		return state.Record{}, config.Source{}, config.BackupSet{}, fmt.Errorf("app: retire: %w", err)
	}
	src, bs, ok := s.backupSetConfigFor(id.Set)
	if !ok {
		return state.Record{}, config.Source{}, config.BackupSet{}, fmt.Errorf("app: retire: %w", unconfiguredSet(id.Set))
	}
	return rec, src, bs, nil
}

// PreviewRetirement answers what RetireArtifact would do with req, without
// doing it: the source check, the successor, the refusal and the effects. It
// runs the same probe the action runs, so it reaches the source.
func (s *Service) PreviewRetirement(ctx context.Context, id model.ArtifactID, req RetireRequest) (RetirementPreview, error) {
	rec, src, bs, err := s.lookupForRetire(ctx, id)
	if err != nil {
		return RetirementPreview{}, err
	}
	p, err := s.evaluate(ctx, rec, src, bs, req, nil)
	if err != nil {
		return RetirementPreview{}, err
	}
	out := RetirementPreview{
		Artifact: id, State: lifecycle.State(rec.State), FailureReason: p.failureReason,
		SourceCheck: p.check, ProposedSuccessor: p.successor, SuccessorCandidates: p.candidates, Refusal: p.refusal,
	}
	if p.refusal == nil {
		out.Effects = p.effects(req)
	}
	if out.State == lifecycle.Retired {
		if r, found, err := s.Journal.LatestRetirement(ctx, id); err == nil && found {
			out.Retirement = &r
		}
	}
	return out, nil
}

// RetireArtifact retires one FAILED artifact on an operator's (or an
// approved agent's) request. A refusal comes back as a *RetireRefusal, or
// ErrNotFailed for a row that is not FAILED.
func (s *Service) RetireArtifact(ctx context.Context, id model.ArtifactID, req RetireRequest) (RetireResult, error) {
	rec, src, bs, err := s.lookupForRetire(ctx, id)
	if err != nil {
		return RetireResult{}, err
	}
	p, err := s.evaluate(ctx, rec, src, bs, req, nil)
	if err != nil {
		return RetireResult{}, err
	}
	if p.refusal != nil {
		if p.refusal.Code == RefusalNotFailed {
			return RetireResult{}, fmt.Errorf("%w: %s is %s: %w", ErrNotFailed, id, rec.State, p.refusal)
		}
		return RetireResult{}, p.refusal
	}
	kind, actor := req.ActorKind, req.Actor
	if kind == "" {
		kind = state.RetiredByOperator
	}
	return s.commitRetirement(ctx, p, retireKeyPrefixOperator, state.RetirementUpdate{
		ActorKind: kind, Actor: actor, SourceCheck: p.check.Result, Note: req.Note,
	}, false)
}

// commitRetirement makes the one journal write and then removes the
// .partial. The From check inside the write is the compare-and-swap: a retry
// that won the race leaves this write refused, and nothing is removed.
func (s *Service) commitRetirement(ctx context.Context, p retirePlan, keyPrefix string, r state.RetirementUpdate, automatic bool) (RetireResult, error) {
	id := p.rec.Artifact
	if p.successor != nil {
		r.SupersededBy = p.successor.Name
	}
	r.FailureReason = p.failureReason
	now := s.now()

	detail := fmt.Sprintf("retired by %s (source %s)", r.ActorKind, r.SourceCheck)
	if r.SupersededBy != "" {
		detail += ", superseded by " + r.SupersededBy
	} else {
		detail += ", no successor acknowledged"
	}
	if p.keptCopy != "" {
		detail += "; its own copy at " + p.keptCopy + " could not be verified and was kept where it is"
	}
	if r.Note != "" {
		detail += ": " + r.Note
	}
	if _, err := lifecycle.Advance(ctx, s.lifecycleDeps(), state.Transition{
		Artifact: id, Key: keyPrefix + id.String() + ":" + now.Format(time.RFC3339Nano),
		From: string(lifecycle.Failed), To: string(lifecycle.Retired), Detail: detail, Retirement: &r,
	}); err != nil {
		if errors.Is(err, state.ErrStateMismatch) {
			return RetireResult{}, fmt.Errorf("%w: %s changed state while it was being retired", ErrNotFailed, id)
		}
		return RetireResult{}, fmt.Errorf("app: retire: recording the retirement of %s: %w", id, err)
	}

	removed := false
	if p.partialExists {
		removed = s.removePartial(ctx, p.partial)
	}
	if s.Logger != nil {
		s.logger().ArtifactRetired(ctx, id.String(), r.ActorKind, r.Actor, r.SourceCheck, r.SupersededBy, automatic)
	}
	rec, found, err := s.Journal.LatestRetirement(ctx, id)
	if err != nil || !found {
		rec = state.Retirement{Artifact: id, RetiredAt: now, ActorKind: r.ActorKind, Actor: r.Actor, SourceCheck: r.SourceCheck,
			SupersededBy: r.SupersededBy, FailureReason: r.FailureReason, Note: r.Note}
	}
	return RetireResult{Retirement: rec, PartialRemoved: removed}, nil
}

// removePartial removes one regular file and reports whether it did.
// Missing is fine; anything else is logged and left for the next sweep.
func (s *Service) removePartial(ctx context.Context, partial string) bool {
	info, err := os.Lstat(partial)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if err := os.Remove(partial); err != nil && !errors.Is(err, os.ErrNotExist) {
		if s.Logger != nil {
			s.logger().Error(ctx, "retire", fmt.Errorf("removing the leftover partial %s: %w", partial, err))
		}
		return false
	}
	return true
}

// bringBackReappeared is the way back (issue #1528). A RETIRED artifact that
// discovery has just listed and proven complete again goes back to
// DISCOVERED, but only when its retirement recorded the source as confirmed
// absent: that retirement was made on evidence that is no longer true. A
// retirement an operator made over a present or unreachable source stays.
func (s *Service) bringBackReappeared(ctx context.Context, res discovery.Result) {
	for _, rec := range res.AlreadyKnown {
		if lifecycle.State(rec.State) != lifecycle.Retired {
			continue
		}
		r, found, err := s.Journal.LatestRetirement(ctx, rec.Artifact)
		if err != nil {
			s.logger().Error(ctx, "unretire", fmt.Errorf("reading %s's retirement: %w", rec.Artifact, err))
			continue
		}
		if !found || r.SourceCheck != state.SourceAbsent {
			continue
		}
		// A new attempt, counted like every other way back into the
		// pipeline (RetryFailed, a quarantine release). The count is what
		// the walk's attempt key is built from: left alone, the next transfer
		// would present the failed attempt's own key, the journal would read
		// it as a replay, and the row would sit at DISCOVERED for good.
		if _, err := lifecycle.Advance(ctx, s.lifecycleDeps(), state.Transition{
			Artifact: rec.Artifact, Key: retireKeyPrefixWayBack + rec.Artifact.String() + ":" + s.now().Format(time.RFC3339Nano),
			From: string(lifecycle.Retired), To: string(lifecycle.Discovered),
			Detail: "the source object a retirement recorded as gone is listed again, so it re-enters the pipeline",
			Retry:  &state.RetryUpdate{Count: rec.RetryCount + 1, LastError: r.FailureReason},
		}); err != nil {
			s.logger().Error(ctx, "unretire", fmt.Errorf("bringing %s back: %w", rec.Artifact, err))
			continue
		}
		s.logger().ArtifactUnretired(ctx, rec.Artifact.String())
	}
}

// autoRetire is the cycle's conservative pass over one artifact-engine set
// (predicate P1 to P7 in the design). It runs after the per-artifact walk,
// so a FAILED row it looks at is not being worked on, and it sweeps the
// leftover .partial of every RETIRED row on the way past.
//
// It asks the source only for rows that already have a successor: a FAILED
// row with nothing newer cannot be retired automatically whatever the source
// says, so the listing and the Stat are spent only where they can matter.
//
// It reports how many rows it retired, so the cycle does not go on counting
// them as failures it just dealt with.
func (s *Service) autoRetire(ctx context.Context, src config.Source, bs config.BackupSet, records []state.Record) (retired int) {
	var candidates []state.Record
	for _, rec := range records {
		switch lifecycle.State(rec.State) {
		case lifecycle.Retired:
			if partial, err := lifecycle.PartialArtifactPath(bs.LocalPath, rec.Artifact); err == nil {
				s.removePartial(ctx, partial)
			}
		case lifecycle.Failed:
			// An own copy at the final name is never retired by the cycle
			// (issue #1594): whether a copy nobody can verify is worth
			// keeping counted is a person's call, and the pass must not
			// read every such file every cycle to find out.
			if pickSuccessor(rec, records) != nil && !ownCopyAtFinalName(rec, bs) {
				candidates = append(candidates, rec)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Artifact.Name < candidates[j].Artifact.Name })
	listing := &sourceListing{}
	for _, rec := range candidates {
		if ctx.Err() != nil {
			return retired
		}
		p, err := s.evaluate(ctx, rec, src, bs, RetireRequest{}, listing)
		if err != nil {
			s.logger().Error(ctx, "auto-retire", err)
			continue
		}
		if p.refusal != nil || p.check.Result != state.SourceAbsent || p.successor == nil {
			continue
		}
		if _, err := s.commitRetirement(ctx, p, retireKeyPrefixAutomatic, state.RetirementUpdate{
			ActorKind: state.RetiredAutomatically, Actor: retireAutomaticActor, SourceCheck: state.SourceAbsent,
		}, true); err != nil {
			if !errors.Is(err, ErrNotFailed) {
				s.logger().Error(ctx, "auto-retire", err)
			}
			continue
		}
		retired++
	}
	return retired
}
