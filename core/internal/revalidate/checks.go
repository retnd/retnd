package revalidate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/retnd/retnd/core/internal/archive"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/fsopen"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// This file is the checking half: given one artifact that scheduling picked,
// go and find out whether its durable copy is still good.
//
// The organising idea is a distinction the rest of the package leans on
// everywhere, between CHECKED and PASSED. Three outcomes are possible, not
// two, and collapsing the third into either of the others causes real harm
// in opposite directions. An artifact nothing was configured to check is not
// a pass: recording one would reset its due-ness clock and it would drift
// for ever, looking freshly verified. It is also not a failure: quarantining
// a backup because nobody asked for a check on it would destroy an operator's
// trust in the feature within a cycle. So runChecks returns checked
// separately, and only checkArtifact's checked branch ever writes to the
// journal.
//
// The second idea is that a verdict is only as strong as what it actually
// did. Since EPIC E a durable copy can be a local file or an object on a
// storage medium, and those are different checks, not one check with a
// different path: re-reading a local file proves the bytes, HEADing an
// object proves an object of the right size is at that key. Both are
// legitimate, and reporting the second as if it were the first would be a
// lie an operator only discovers during a restore. Every path out of here
// carries the class it achieved for that reason.
//
// The third is that money is a correctness concern here. This pass runs
// unattended on a schedule an operator set when everything was free, so
// anything that would download an artifact is refused rather than
// configured, and the refusal is a check on Class.CostsEgress at the moment
// the request is about to be made rather than a rule written in a comment.

// automaticMediumClass is the strongest verification class a scheduled,
// unattended pass will ever run against a copy on a storage medium, and it
// is now the only statement of that rule in the product.
//
// It was a const inside checkMediumPlacements and it is here so a test can
// read it. internal/placement carried a second statement of the same rule,
// AutomaticClass, which nothing consulted. #438 deleted it: it answered
// this same constant for every access state a copy this pass would look at
// can be in, and reaching it would have cost a restore-status probe per
// archive-class copy per cycle to learn nothing that could change what
// runs, because an existence check is a HEAD and a HEAD works on an
// archived object.
//
// Existence rather than Attested, which also costs no egress and is the
// rung between them. An attestation is the endpoint's own word about the
// bytes, which is why it is opt-in per medium, and this function runs ONE
// class against EVERY ACTIVE medium copy without ever reading that opt-in.
// So an automatic pass that attested would be trusting an endpoint on
// behalf of an operator who never said it could, which also means "the
// strongest rung that does not download" is the wrong derivation of this
// constant and always was.
//
// The properties, and their reasons, are asserted by
// TestTheAutomaticCeilingIsAClassThisPassMayRunAgainstEveryMedium; the
// behaviour they describe is asserted against a real pass, by request
// count, in TestRevalidationOfAMediumPlacementIsExistenceAndSaysSo.
const automaticMediumClass = placement.Existence

// runChecks runs whatever cfg enables against rec's durable copy and
// reports a combined verdict, plus the verification CLASS it achieved.
//
// # Which copy it checks, and why that is a fork rather than a parameter
//
// An artifact's durable copy used to be one thing: a local file. Since
// EPIC E it can be a local file, an object on a storage medium, or both
// at once during a move. The two are not the same check with a different
// path, they are different checks with different costs and different
// strengths, so this function forks on where the copy actually is rather
// than pretending one code path covers both.
//
//   - a local copy is re-read and re-hashed, exactly as before, which is
//     placement.Content;
//   - a medium copy is HEADed, which is placement.Existence, and that is
//     the CEILING for an automatic pass rather than a starting point.
//     Anything stronger costs egress, and silent egress is a surprise
//     bill; FR-31 makes content and attested re-verification of a medium
//     placement operator-initiated, and Class.CostsEgress is what this
//     function refuses on rather than a rule written in a comment.
//
// checked is false only when nothing enabled could actually produce a
// verdict for this specific artifact: cfg.Command is nil and cfg.Hash is
// unset or has no recorded baseline, or the artifact's copy is on a medium
// this deployment cannot reach. That is not a failure and not a pass; see
// checkArtifact, the only caller, for why it must never be turned into a
// same-state "passed" journal write, which would silently reset SelectDue's
// due-ness clock for an artifact nothing here actually re-verified.
//
// err is non-nil only for an infrastructure problem this function cannot
// safely turn into a business verdict for this one artifact: the outer
// context being cancelled or timing out (isCancelled), or the restore-test
// hook failing to even start (a broken or missing executable, not the
// hook's own opinion about the file's contents). Both are the caller's
// (Run's) responsibility to route: a cancellation stops the whole pass,
// an ordinary infrastructure error is recorded in Report.Errors without
// touching this artifact's journal row. Neither is grounds to quarantine a
// possibly-perfectly-good backup over what might be a misconfigured
// operator hook, not a corrupt artifact.
func runChecks(ctx context.Context, deps Deps, cfg config.Revalidation, opts Options, rec state.Record) (res checkResult, err error) {
	// The medium copies are only consulted when there is no active LOCAL
	// placement, which is the ordering FR-31 asks for: local placements
	// keep today's behaviour exactly, and an artifact mid-move that still
	// has its local copy is checked the way it was checked yesterday.
	//
	// state.Record.ActiveMediumPlacements used to be a private loop here,
	// and this package was the only one of FR-29's four swept callers that
	// asked it. The other three read "no readable local path" as "no
	// durable copy" and quarantined every moved artifact; the loop now
	// lives beside ReadableLocalPath so every caller of the one is held
	// to asking the other.
	if _, local := rec.LocalPlacement(); !local {
		if mediums := rec.ActiveMediumPlacements(); len(mediums) > 0 {
			return checkMediumPlacements(ctx, deps, cfg, opts, mediums)
		}
	}
	return checkLocalCopy(ctx, deps, cfg, rec)
}

// checkResult is one artifact's combined verdict plus the per-copy outcomes
// it was built from. The verdict is what the lifecycle acts on (quarantine,
// or nothing) and the outcomes are what the journal records, one per copy,
// because "this artifact passed" and "this copy passed" are different facts
// and a copy is the thing with a due date.
type checkResult struct {
	checked, passed bool
	class           placement.Class
	reason          string
	copies          []copyOutcome
}

// copyOutcome is what one scrub learned about one copy.
type copyOutcome struct {
	// medium names the copy: state.MediumLocal or a medium id.
	medium  string
	verdict state.ScrubVerdict
	// class is the class that RAN. Empty when nothing ran.
	class  placement.Class
	reason string
	// bytesRead is what the check read, or nil when it read nothing or the
	// count is not known.
	bytesRead *int64
	// refused is set when the check was not made because it would have spent
	// money nobody agreed to. A refusal is not a fact about the copy, so it
	// is reported and not recorded.
	refused bool
	// rewritten is set when the verdict was refused because the copy no
	// longer holds the bytes that were read (state.ErrCopyChanged). A failure
	// about bytes that are gone is not a failure of the copy that is there.
	rewritten bool
}

func int64p(n int64) *int64 { return &n }

// checkMediumPlacements runs the automatic ceiling, placement.Existence,
// against every ACTIVE copy the artifact has on a storage medium.
//
// The class is not configurable here and that is deliberate. cfg.Hash asks
// for a content check, and honouring it against a medium would download
// the artifact on a schedule the operator set for something that used to
// be free. FR-31 says so directly, and the assertion below turns it from a
// rule into a fact: whatever class this function is about to run, it
// refuses if that class costs egress.
//
// cfg.Command is skipped for the same reason and said out loud for a
// different one. A restore test opens the artifact, so running one against
// a bucket means downloading the artifact, which is the egress this
// function refuses. But an operator who configured a restore test and gets
// back a green pass has been told less than they asked for, and a check
// that quietly stops running is how a safety feature becomes decorative.
// So the pass names the tier that did not run.
//
// A failing check is a verdict about ONE placement, and quarantine is a
// verdict about the artifact. FR-31 keeps them apart: the artifact enters
// QUARANTINED only when no other ACTIVE verified placement remains. So a
// pass here needs one placement to pass, and a failure needs every one of
// them to have been asked AND to have failed. A placement that could not
// be asked leaves the question open, which is an error rather than a
// verdict: an unreachable bucket is not evidence that a backup is gone.
func checkMediumPlacements(ctx context.Context, deps Deps, cfg config.Revalidation, opts Options, ps []state.Placement) (checkResult, error) {
	if deps.Store == nil || deps.Mediums == nil {
		return checkResult{
			passed: true,
			reason: fmt.Sprintf("this artifact's durable copies are on storage mediums (%s), and this deployment has no way to reach one, so nothing was checked", mediumIDs(ps)),
			copies: notChecked(ps, "this deployment has no way to reach a storage medium"),
		}, nil
	}

	// The class this pass runs against a medium. Unattended it is the
	// automatic ceiling and nothing else. An operator who asked for content
	// (retnd scrub --content) has initiated the spend, which is the only
	// thing FR-31 accepts as authority for a read that costs egress.
	want := automaticMediumClass
	if opts.Content {
		want = placement.Content
	}
	if want.CostsEgress() && !opts.Content {
		// Unreachable today, and here on purpose: this is the line that
		// has to fail if somebody ever raises the automatic ceiling, so
		// the decision is made by editing this refusal rather than by
		// changing a constant and discovering the bill later.
		return checkResult{}, fmt.Errorf(
			"revalidate: the automatic class %s costs egress, and FR-31 makes anything that costs egress operator-initiated", want)
	}

	var (
		details       []string
		anyPassed     bool
		didNotAnswer  error
		notConfigured string
		copies        []copyOutcome
	)
	for _, p := range ps {
		medium, ok := deps.Mediums.MediumFor(p.Medium)
		if !ok {
			if notConfigured == "" {
				notConfigured = p.Medium
			}
			copies = append(copies, copyOutcome{
				medium: p.Medium, verdict: state.ScrubNotChecked,
				reason: fmt.Sprintf("the storage medium %q is not in the configuration", p.Medium),
			})
			continue
		}

		// An archive-class copy cannot be read without a restore, and this
		// product never starts one on its own. So a content scrub of one is
		// not attempted at all: no GET, no thaw, and the copy is recorded as
		// requiring a restore. The refusal is made here, before a request is
		// spent, for placement.VerifyWithAccess's reason: asking S3 for an
		// archived object costs a request and answers InvalidObjectState,
		// and a caller that read that as a verification failure would
		// quarantine a good backup for the crime of being cheap to store.
		archived := archive.IsArchive(medium.StorageClass)
		if want == placement.Content && archived {
			details = append(details, fmt.Sprintf("%s on %s is in an archive storage class, so its bytes cannot be read until a restore is requested, and no restore was started", p.Location, p.Medium))
			copies = append(copies, copyOutcome{
				medium: p.Medium, verdict: state.ScrubRequiresRestore,
				reason: "archive storage class: the bytes cannot be read without a restore, and no restore was started",
			})
			continue
		}
		if want.CostsEgress() && !opts.AllowEgress {
			// The refusal that fires at the moment of spend. It is a check
			// on Class.CostsEgress against what the request carries, not a
			// rule in a comment.
			details = append(details, fmt.Sprintf("%s on %s was not read: %s", p.Location, p.Medium, errEgressNotAcknowledged))
			copies = append(copies, copyOutcome{
				medium: p.Medium, refused: true,
				reason: errEgressNotAcknowledged.Error(),
			})
			continue
		}

		release, limitErr := deps.Limits.Acquire(ctx, p.Medium)
		if limitErr != nil {
			return checkResult{}, limitErr
		}
		started := deps.now()
		result, verifyErr := placement.Verify(ctx, deps.Store, medium, p, want, started)
		release()
		if verifyErr != nil {
			if isCancelled(verifyErr) {
				return checkResult{}, verifyErr
			}
			// A class that could not be attempted is not a verdict about
			// the artifact, and it is not a configuration fact either: the
			// medium was there to ask and did not answer. So it is routed
			// the way this package already routes a restore-test hook that
			// fails to start, as a per-artifact ERROR rather than as an
			// unchecked finding.
			//
			// The distinction is worth the extra branch. An unchecked
			// finding says "nothing here was configured to check", which an
			// operator reads past; an error says "this backup could not be
			// checked and somebody should find out why", which is the true
			// statement when a bucket does not answer. Either way the
			// journal's artifact row is untouched, and the COPY records that
			// it could not be completed, with a backoff, so it is retried
			// sooner than a healthy copy and never looks freshly verified.
			if didNotAnswer == nil {
				didNotAnswer = fmt.Errorf("medium %q: %w", p.Medium, verifyErr)
			}
			copies = append(copies, copyOutcome{
				medium: p.Medium, verdict: state.ScrubCouldNotComplete,
				reason: fmt.Sprintf("medium %q could not be asked: %v", p.Medium, verifyErr),
			})
			continue
		}
		details = append(details, result.Detail)
		out := copyOutcome{medium: p.Medium, class: result.Class, reason: result.Detail}
		switch {
		case !result.Passed:
			out.verdict = state.ScrubFailed
		case archived:
			// The object is there, at the right size, and that is all a
			// HEAD can say about an object whose bytes need a restore.
			// Recording it as a plain pass would put a green tick on a copy
			// nobody can read.
			out.verdict = state.ScrubRequiresRestore
			out.reason += "; the storage class is an archive tier, so the bytes cannot be read without a restore"
		default:
			out.verdict = state.ScrubPassed
		}
		if result.Class == placement.Content {
			out.bytesRead = copySize(p)
		}
		copies = append(copies, out)
		if result.Passed {
			anyPassed = true
		}
	}

	// One good copy is enough to say the artifact is still there, and
	// saying so does not depend on the placements that could not be asked.
	// Everything below is the case where none passed, where the two ways a
	// placement can go unasked matter, because each of them leaves "no
	// verified copy remains" unproven and quarantine is what that would
	// otherwise mean.
	if !anyPassed {
		switch {
		case len(details) > 0 && (didNotAnswer != nil || notConfigured != ""):
			// A copy was asked and FAILED, and another copy could not be
			// asked at all. Neither existing branch tells that truthfully
			// (issue #435 found it while writing the operator-triggered
			// twin of these checks): the unconfigured branch below said
			// "none of them is in the configuration, so nothing was
			// checked", which is two false statements at once when a
			// medium answered and its copy is gone.
			//
			// Quarantine is still out of the question, and that part was
			// always right: the copy nobody could ask may be perfectly
			// fine, and FR-31 quarantines only when no other ACTIVE
			// verified placement remains. What changes is where this
			// lands and what it says. A copy proved missing is the
			// definition of "somebody should find out why", so it goes to
			// the error channel, naming the copy that is gone AND the
			// copy nobody could ask, rather than to an unchecked finding
			// an operator reads past.
			open := append([]string(nil), details...)
			if didNotAnswer != nil {
				open = append(open, didNotAnswer.Error())
			}
			if notConfigured != "" {
				open = append(open, fmt.Sprintf(
					"the copy on storage medium %q was not checked, because that medium is not in the configuration", notConfigured))
			}
			return checkResult{copies: copies}, fmt.Errorf(
				"no copy of this artifact could be verified, and not every copy could be asked, so it is not established that no verified copy remains: %s",
				strings.Join(open, "; "))
		case didNotAnswer != nil:
			// A medium that was there to ask and did not answer: an error,
			// because somebody should find out why.
			return checkResult{copies: copies}, didNotAnswer
		case notConfigured != "":
			// A medium this deployment was never configured to reach, with
			// nothing asked anywhere, so there is no evidence in either
			// direction: a configuration fact rather than a backup nobody
			// could check, and an unchecked finding rather than an error.
			return checkResult{
				passed: true, copies: copies,
				reason: fmt.Sprintf("this artifact's durable copies are on storage mediums (%s), and none of them is in the configuration, so nothing was checked", mediumIDs(ps)),
			}, nil
		}
		if allRefusedOrArchived(copies) {
			// Every copy was either refused for cost or is archived: nothing
			// was checked, and nothing failed. That is not a pass.
			return checkResult{passed: true, copies: copies, reason: strings.Join(details, "; ")}, nil
		}
	}

	detail := strings.Join(details, "; ")
	// A pass that another copy carried has to say which copies it did not
	// hear from, or an operator reads a green tick and never learns that
	// one of their two buckets went quiet. The pass itself stands: a copy
	// is there and was asked. What it must not do is imply that every copy
	// was.
	if didNotAnswer != nil {
		detail += "; " + didNotAnswer.Error() + ", so that copy was not checked"
	}
	if notConfigured != "" {
		detail += fmt.Sprintf("; the copy on storage medium %q was not checked, because that medium is not in the configuration", notConfigured)
	}
	if cfg.Command != nil {
		detail += "; the restore-test hook did not run, because opening this artifact means downloading it and FR-31 makes anything that costs egress operator-initiated"
	}
	return checkResult{checked: true, passed: anyPassed, class: want, reason: detail, copies: copies}, nil
}

// errEgressNotAcknowledged is the refusal a read that costs egress gets
// when nobody agreed to the cost.
var errEgressNotAcknowledged = errors.New("this read costs egress and the request did not acknowledge the cost")

// allRefusedOrArchived reports whether no copy produced a verdict of any
// kind: each was refused for cost or needs a restore.
func allRefusedOrArchived(copies []copyOutcome) bool {
	if len(copies) == 0 {
		return false
	}
	for _, c := range copies {
		if !c.refused && c.verdict != state.ScrubRequiresRestore {
			return false
		}
	}
	return true
}

func copySize(p state.Placement) *int64 {
	if p.Size == nil {
		return nil
	}
	return int64p(*p.Size)
}

// notChecked builds the outcomes for copies nothing could be asked about.
func notChecked(ps []state.Placement, why string) []copyOutcome {
	out := make([]copyOutcome, 0, len(ps))
	for _, p := range ps {
		out = append(out, copyOutcome{medium: p.Medium, verdict: state.ScrubNotChecked, reason: why})
	}
	return out
}

// mediumIDs names the mediums a set of placements sits on, for the two
// messages an operator reads when nothing could be checked at all. It
// names every one of them rather than the first, because "your backup is
// on a medium I cannot reach" is a sentence somebody has to act on and the
// medium's id is the only part of it that says where to look.
func mediumIDs(ps []state.Placement) string {
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, strconv.Quote(p.Medium))
	}
	return strings.Join(ids, ", ")
}

// checkLocalCopy is exactly the check this package always did, against the
// artifact's local copy, reported as the class it has always achieved.
func checkLocalCopy(ctx context.Context, deps Deps, cfg config.Revalidation, rec state.Record) (checkResult, error) {
	var (
		reasons   []string
		checked   bool
		hashRan   bool
		class     placement.Class
		bytesRead *int64
	)
	passed := true

	localPath, hasLocal := rec.ReadableLocalPath()

	// The baseline is the hash recorded for THIS copy when the copy has a
	// placement row that carries one, and the artifact row's own otherwise.
	// They are the same value for anything ingestion wrote; they differ for
	// a copy a rebuild or a move wrote a row for, where the placement is
	// the record that is about the bytes on the disk being read.
	baseHash, baseAlg := rec.LocalHash, rec.LocalHashAlg
	if lp, ok := rec.LocalPlacement(); ok && lp.Hash != "" && lp.HashAlg != "" {
		baseHash, baseAlg = lp.Hash, lp.HashAlg
	}

	release, limitErr := deps.Limits.Acquire(ctx, state.MediumLocal)
	if limitErr != nil {
		return checkResult{}, limitErr
	}
	defer release()

	if cfg.Hash && baseAlg == string(transport.SHA256) && baseHash != "" {
		checked = true
		hashRan = true
		class = placement.Content
		switch {
		case !hasLocal:
			passed = false
			reasons = append(reasons, "no local copy of this artifact is recorded, so its content cannot be re-read")
		default:
			sum, n, readErr := recomputeLocalHash(ctx, localPath)
			if n > 0 || readErr == nil {
				bytesRead = int64p(n)
			}
			switch {
			case readErr != nil && isCancelled(readErr):
				return checkResult{}, readErr
			case readErr != nil:
				passed = false
				reasons = append(reasons, fmt.Sprintf("local final file %s could not be read: %v", localPath, readErr))
			case !strings.EqualFold(sum, baseHash):
				passed = false
				reasons = append(reasons, fmt.Sprintf(
					"local final file %s now hashes to %s, but the %s hash recorded at verification was %s",
					localPath, sum, baseAlg, baseHash,
				))
			default:
				reasons = append(reasons, "recomputed hash still matches the hash recorded at verification")
			}
		}
	}

	if cfg.Command != nil {
		result, hookErr := lifecycle.RunRestoreCheck(ctx, *cfg.Command, localPath)
		if hookErr != nil {
			if isCancelled(hookErr) {
				return checkResult{}, hookErr
			}
			return checkResult{}, fmt.Errorf("restore-test hook: %w", hookErr)
		}
		checked = true
		// A restore-test hook opens the artifact and proves it restores,
		// which is at least as strong a statement about the bytes as
		// re-hashing them. It does not upgrade a class it did not reach,
		// so it only sets one where the hash tier did not already.
		if class == "" {
			class = placement.Content
		}
		if !result.Passed {
			passed = false
			reasons = append(reasons, "restore-test hook failed: "+result.Detail)
		} else {
			reasons = append(reasons, "restore-test hook passed")
		}
	}

	if !checked {
		why := fmt.Sprintf(
			"nothing to check for this artifact: no recorded hash baseline (local_hash_alg=%q) and no restore-test hook configured",
			baseAlg,
		)
		return checkResult{
			passed: true, reason: why,
			copies: []copyOutcome{{medium: state.MediumLocal, verdict: state.ScrubNotChecked, reason: why}},
		}, nil
	}

	reason := strings.Join(reasons, "; ")
	if passed && !hashRan {
		// The hook accepted the copy, and nothing re-read and hashed its
		// bytes: there was no baseline to compare against. An executable the
		// operator wrote may have opened the file or may have exited 0
		// without looking, and a content claim is only ever made by a check
		// that read the bytes and compared them. So the pass is reported and
		// the copy is left unknown; the recovery test is where "a validator
		// accepted these bytes" is a claim, and it is gated on the digest.
		note := reason + "; no hash baseline was available, so the bytes were not re-hashed and no content claim is made for this copy"
		return checkResult{
			checked: true, passed: true, reason: note,
			copies: []copyOutcome{{medium: state.MediumLocal, verdict: state.ScrubNotChecked, reason: note}},
		}, nil
	}
	verdict := state.ScrubPassed
	if !passed {
		verdict = state.ScrubFailed
	}
	return checkResult{
		checked: true, passed: passed, class: class, reason: reason,
		copies: []copyOutcome{{medium: state.MediumLocal, verdict: verdict, class: class, reason: reason, bytesRead: bytesRead}},
	}, nil
}

// recomputeLocalHash reads path in full and returns its SHA-256, in hex.
//
// This duplicates a small (open, io.Copy into a hasher, hex-encode)
// utility that already exists, independently, in both
// internal/lifecycle's verify.go (readAndHashLocal) and
// internal/reconcile's localcheck.go (sha256File): each package that has
// ever needed this has kept its own copy rather than reach across a
// package boundary for ten lines, and this package follows that same,
// already-established convention rather than introduce a new shared
// dependency for it.
//
// It opens through fsopen.OpenRegular, not os.Open: a FIFO placed at the
// copy's path would otherwise block this call for ever, holding the copy's
// lease and a read slot with it, in a way no context can interrupt.
func recomputeLocalHash(ctx context.Context, path string) (sum string, n int64, err error) {
	f, err := fsopen.OpenRegular(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err = io.Copy(h, &ctxReader{ctx: ctx, r: f})
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ctxReader stops a long read when its context ends. A content scrub of a
// large local file is minutes of disk reads, and a held backup set or a
// shutting-down daemon must be able to stop it between chunks rather than
// wait the file out.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
