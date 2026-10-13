// Package recoverytest proves a copy is usable, and keeps that claim apart
// from the two beside it (EPIC Q, #980).
//
// There are three claims and they are never one tick. Stored is that a copy
// is somewhere. Content verified is that its bytes were read and still match
// what was recorded (internal/revalidate). Recovery tested, this package's,
// is that the copy was recovered to an isolated place, the recovered bytes
// were read end to end and matched the artifact's recorded digest, and only
// then did the operator's own validator look at them and accept.
//
// # The digest gate is the point
//
// A HEAD, a listing, a signed URL that resolves and a thaw that completes are
// all reachability, and reachability is never a pass. The runner reads the
// recovered bytes itself, hashes them itself and compares the result with the
// artifact's recorded digest BEFORE it runs a validator, and the journal
// refuses to store a pass that did not go through that order
// (state.RecoveryTest). So a validator that exits 0 without ever opening the
// file cannot produce a pass: the pass is recorded against the digest, not
// against the exit code.
//
// # The validator is a workflow script
//
// The operator's validator is a NAME.local.sh script in a stage directory
// under the workflow root (recovery-test/<source>/<set>), run with EPIC L's
// machinery and not with a built-in of this package: the same discovery and
// custody rules, the same bytes read once and hashed, the same shell
// verification that gates a save, the same host runner container, the same
// resolved environment. Validator is the seam; the application implements it
// over app.StageRunner.
//
// What the script is NOT trusted with is the claim. The engine re-reads the
// recovered bytes, checks their digest, and only then asks the script. The
// script's exit status is one input to a verdict this package reaches and the
// journal refuses to store without the digest match, and the hash recorded as
// the validator's identity is the hash of the bytes that actually ran, taken
// by the plan that ran them. A script never writes "Content verified" or
// "Recovery tested". Output is untrusted terminal input, so it leaves this
// package only as OutputRecords whose text has no control byte in it, with a
// truncation marker that is a record of its own kind (see output.go).
//
// # What it does not do
//
// It does not restore an application, boot anything, or run a validator
// against a production path. It tests the newest known good copy only. It
// does not thaw: an archive-class copy needs an explicit cost
// acknowledgement and a scheduled test never has one. It reads the incremental
// engine's achieved level for engine: kopia sets (KopiaClaim) and runs no
// second drill.
//
// Q1.1 (core/internal/recovery) owns how a copy becomes a staged file. This
// package states what it needs of that as the small Recoverer interface and
// is complete without it.
package recoverytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/archive"
	"github.com/retnd/retnd/core/internal/fsopen"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/revalidate"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// Journal is the slice of internal/state a recovery test needs.
type Journal interface {
	Get(ctx context.Context, id model.ArtifactID) (state.Record, error)
	ListByBackupSet(ctx context.Context, set model.BackupSetID) ([]state.Record, error)
	RecordRecoveryTest(ctx context.Context, r state.RecoveryTest) error
	RecordRecoveryEvaluation(ctx context.Context, c state.CopyRef, st string) error
}

// RecoverRequest asks for one copy to be staged.
type RecoverRequest struct {
	Artifact model.ArtifactID
	Copy     state.Placement
	// AcknowledgeCost is the operator's agreement to pay for the read
	// (egress, or a restore of an archived object). A scheduled test never
	// carries it.
	AcknowledgeCost bool

	// Scheduled marks an unattended test. A recoverer that queues for a
	// shared read slot instead of refusing may do so for this one: the
	// scheduler's work is already bounded, and an operator at a terminal
	// would rather be told "busy" than wait.
	Scheduled bool
}

// Staged is a recovered copy sitting in an isolated staging place.
type Staged struct {
	// Path is the recovered bytes. This package opens it read-only and
	// hashes it; it never trusts a recoverer's word that the bytes are right.
	Path string
}

// Recoverer is what Q1.1's staging contract must give this package.
// Recover writes the copy's bytes to a NEW file under an isolated staging
// root and returns it, and writes nowhere else. Remove deletes exactly that
// staged copy. A Recoverer that refuses a read because it costs money and
// the request did not acknowledge it returns an error wrapping ErrCostRefused.
type Recoverer interface {
	Recover(ctx context.Context, req RecoverRequest) (Staged, error)
	Remove(ctx context.Context, s Staged) error
}

// ErrCostRefused is what a Recoverer wraps when it declines a read because it
// would cost money and the request did not acknowledge that.
var ErrCostRefused = errors.New("recoverytest: the read costs money and was not acknowledged")

// ErrCorruptCopy is what a Recoverer wraps when it read the copy and the bytes
// did not match the digest recorded for the artifact. It is the digest gate
// speaking from inside the recoverer (the export engine verifies before it
// hands anything over), so Run records it as a failed copy, exactly as a
// mismatch it finds itself, and never as an attempt that could not complete.
var ErrCorruptCopy = errors.New("recoverytest: the recovered bytes do not match the recorded digest")

// ErrNoKnownGoodCopy is returned when a set has nothing to test.
var ErrNoKnownGoodCopy = errors.New("recoverytest: this backup set has no known good copy to test")

// Validator is the operator's declared validator, as the application resolves
// it. Configured says whether one exists for set at all, without running
// anything, so a set without one reads "not configured" before a copy is
// asked for. Validate runs it against a recovered file.
type Validator interface {
	Configured(ctx context.Context, set model.BackupSetID) (bool, error)
	Validate(ctx context.Context, in ValidatorInput) (ValidatorOutcome, error)
}

// ValidatorInput is what a validator is given about the recovered copy.
type ValidatorInput struct {
	Set      model.BackupSetID
	Artifact model.ArtifactID
	Medium   string

	// File is the recovered copy. Digest is its SHA-256 as this package
	// computed it, which has already matched the artifact's recorded digest:
	// a validator is only ever handed bytes that passed the gate. Size is
	// its length.
	File   string
	Digest string
	Size   int64
}

// ValidatorOutcome is what running the validator showed.
type ValidatorOutcome struct {
	// ID is the validator's name (its script's file name, never a path) and
	// Hash the SHA-256 of the exact bytes that ran.
	ID   string
	Hash string

	// Disposition is what became of the process: "exited", "timed_out",
	// "canceled", "signaled", "transport_lost" or "not_attempted". Only
	// "exited" carries an ExitCode, and the others are never read as one.
	Disposition string
	ExitCode    *int

	// Termination is what was PROVED about a termination this product
	// requested: "", "confirmed" or "unconfirmed".
	Termination string

	// Output is the redacted, bounded capture: untrusted terminal input.
	Output    []byte
	Truncated bool

	// Detail is a short note from the executor, never an environment value.
	Detail string
}

// Deps is what a Run needs.
type Deps struct {
	Journal   Journal
	Recoverer Recoverer
	// Mediums resolves a medium id, to tell a free copy from a chargeable one.
	Mediums revalidate.Mediums
	// Validator is the operator's validator. Nil means none is declared.
	Validator Validator
	// Limits is the shared admission control for full reads. Nil is unbounded.
	Limits *revalidate.Limits
	Now    func() time.Time

	// Stage, when set, is told the id of each stage Run enters, using the
	// names of obs.RecoveryStages and in their order. Complete is reported
	// only for a run that recorded a pass. The application turns
	// it into the stage lines the CLI's --follow and the browser render. Nil
	// reports nothing.
	Stage func(id string)

	// Builtin is the built-in structure checker (#1111). Nil means the
	// product's own (StructureCheck); a test swaps in a spy.
	Builtin BuiltinChecker
}

func (d Deps) stage(id string) {
	if d.Stage != nil {
		d.Stage(id)
	}
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now().UTC()
}

// Config is one set's recovery-test settings, already resolved.
type Config struct {
	// Cadence is how often the newest known good copy is owed a test.
	Cadence time.Duration

	// BuiltinCheck is the set's resolved builtin_check: "structure" (or empty,
	// which resolves the same) lets the built-in run when no validator is
	// declared, "off" refuses instead.
	BuiltinCheck string
}

// Request narrows one Run.
type Request struct {
	// Artifact tests this artifact instead of the newest known good one.
	Artifact *model.ArtifactID

	// Scheduled marks an unattended run. It can never pay: AcknowledgeCost
	// is ignored when it is set.
	Scheduled       bool
	AcknowledgeCost bool
}

// Result is what one recovery test concluded, in a form safe to render.
type Result struct {
	Artifact model.ArtifactID
	Medium   string

	// PlacementID is the copy's row id, so a result names the artifact, the
	// copy and the validator it was made against.
	PlacementID int64

	// State is one of the state.Recovery* values.
	State string

	// DigestMatched is the gate's outcome; nil means the digest was never
	// compared.
	DigestMatched *bool

	ValidatorID   string
	ValidatorHash string
	Termination   string
	ExitCode      int

	// Output is the validator's captured output as sanitised records.
	Output          []OutputRecord
	OutputTruncated bool

	// CleanupState is state.RecoveryCleanupRemoved or RecoveryCleanupFailed,
	// empty when nothing was staged. CleanupErr says why, safely.
	CleanupState string
	CleanupErr   string

	// Reason is a short sanitised sentence for a failure or a refusal.
	Reason string

	// Check says which check produced the verdict: "builtin" for the
	// product's structure check, "" for the operator's validator (or when
	// nothing ran). CheckFormat is the one-line account of what the built-in
	// read, e.g. "zstd, 1 frame, content checksum verified, 48.2 GiB decoded
	// and discarded".
	Check       string
	CheckFormat string

	BytesRead int64
	Duration  time.Duration
}

// digestMatches is the digest gate's comparison. It is a variable only so
// the gate's own mutation self-test can replace it with a comparison that
// accepts everything and prove the guard around it turns red; nothing else
// assigns it.
var digestMatches = strings.EqualFold

// retryAfter is when a copy whose test failed or could not run is tried
// again, before the cadence clamps it. A recovery test reads a whole
// artifact, so it does not hammer.
const retryAfter = 6 * time.Hour

// Run tests the newest known good copy of set (or req.Artifact) and records
// the outcome on that copy. An error means the test could not even be
// attempted (nothing to test, a cancelled context); every other outcome,
// including every kind of not-a-pass, is a Result.
func Run(ctx context.Context, deps Deps, set model.BackupSetID, cfg Config, req Request) (Result, error) {
	if deps.Journal == nil || deps.Recoverer == nil {
		return Result{}, errors.New("recoverytest: Deps needs a Journal and a Recoverer")
	}
	if set.IsZero() {
		return Result{}, errors.New("recoverytest: needs a backup set id")
	}
	rec, copy_, err := pickCopy(ctx, deps, set, req)
	if err != nil {
		return Result{}, err
	}
	ref := state.CopyRef{Artifact: rec.Artifact, Medium: copy_.Medium}
	res := Result{Artifact: rec.Artifact, Medium: copy_.Medium, PlacementID: copy_.ID}
	now := deps.now()
	// What the verdict will be about: the bytes this copy holds right now. A
	// copy rewritten while the test runs is refused by the journal.
	identity := copy_.Identity()
	deps.stage("resolving_copy")

	configured := false
	if deps.Validator != nil {
		var cerr error
		configured, cerr = deps.Validator.Configured(ctx, set)
		if cerr != nil {
			res.State = state.RecoveryCouldNotComplete
			res.Reason = "the validator could not be examined: " + safeError(cerr)
			rt := state.RecoveryTest{Copy: ref, State: res.State, At: now, NextDue: now.Add(retryAfter), Reason: res.Reason, Expect: &identity}
			if err := deps.Journal.RecordRecoveryTest(context.WithoutCancel(ctx), rt); err != nil {
				return res, err
			}
			return res, nil
		}
	}
	if !configured {
		res.State = state.RecoveryNotConfigured
		res.Reason = "no recovery-test validator is declared for this backup set"
		if err := deps.Journal.RecordRecoveryEvaluation(ctx, ref, state.RecoveryNotConfigured); err != nil && !errors.Is(err, state.ErrRecoveryHistory) {
			return res, err
		}
		return res, nil
	}
	cadence := cfg.Cadence
	retry := retryAfter
	if cadence > 0 && cadence < retry {
		retry = cadence
	}

	finish := func(r Result, rt state.RecoveryTest) (Result, error) {
		rt.Copy = ref
		rt.Expect = &identity
		rt.At = now
		rt.Duration = deps.now().Sub(now)
		if rt.NextDue.IsZero() {
			rt.NextDue = now.Add(retry)
		}
		rt.Reason = r.Reason
		rt.State = r.State
		r.Duration = rt.Duration
		if err := deps.Journal.RecordRecoveryTest(context.WithoutCancel(ctx), rt); err != nil {
			return r, fmt.Errorf("recording the recovery test of %s: %w", ref.Artifact, err)
		}
		// Complete is the stage of a run that reached a recorded pass. A
		// run that was refused, failed or could not finish stops on the
		// last stage it entered, as a failed recovery does.
		if r.State == state.RecoveryPassed {
			deps.stage("complete")
		}
		return r, nil
	}

	// The cost gate comes first, before a single byte is asked for. A
	// scheduled test never pays, and an unattended read that costs money is
	// refused here, at the moment of spend, rather than described in a doc.
	ack := req.AcknowledgeCost && !req.Scheduled
	if why := costOf(deps, copy_); why != "" && !ack {
		res.State = state.RecoveryRefusedForCost
		res.Reason = "refused: " + why + "; a scheduled test never pays, and this request did not acknowledge the cost"
		return finish(res, state.RecoveryTest{})
	}

	expected, alg := recordedDigest(rec, copy_)
	if expected == "" || !strings.EqualFold(alg, string(transport.SHA256)) {
		res.State = state.RecoveryCouldNotComplete
		res.Reason = "this artifact has no recorded sha256 digest, so there is nothing for the recovered bytes to be matched against"
		return finish(res, state.RecoveryTest{})
	}

	release, err := deps.Limits.Acquire(ctx, copy_.Medium)
	if err != nil {
		return res, err
	}
	defer release()

	deps.stage("reading")
	staged, err := deps.Recoverer.Recover(ctx, RecoverRequest{Artifact: rec.Artifact, Copy: copy_, AcknowledgeCost: ack, Scheduled: req.Scheduled})
	if err != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("recoverytest: cancelled: %w", ctx.Err())
		}
		if errors.Is(err, ErrCorruptCopy) {
			matched := false
			res.DigestMatched = &matched
			res.State = state.RecoveryFailed
			res.Reason = "the recovered bytes do not match the digest recorded for this artifact, so none were handed to the validator"
			return finish(res, state.RecoveryTest{DigestMatched: &matched})
		}
		if errors.Is(err, ErrCostRefused) {
			res.State = state.RecoveryRefusedForCost
			res.Reason = "refused: the recoverer declined a read that costs money"
		} else {
			res.State = state.RecoveryCouldNotComplete
			res.Reason = "the copy could not be recovered to staging: " + safeError(err)
		}
		return finish(res, state.RecoveryTest{})
	}

	// From here a staged copy exists, and it is removed on every path out.
	cleanup := func() {
		deps.stage("cleaning_temporary_files")
		if rerr := deps.Recoverer.Remove(context.WithoutCancel(ctx), staged); rerr != nil {
			res.CleanupState = state.RecoveryCleanupFailed
			res.CleanupErr = safeError(rerr)
			return
		}
		res.CleanupState = state.RecoveryCleanupRemoved
	}

	deps.stage("verifying")
	sum, n, readErr := hashFile(ctx, staged.Path)
	res.BytesRead = n
	if readErr != nil {
		cleanup()
		if ctx.Err() != nil {
			return res, fmt.Errorf("recoverytest: cancelled: %w", ctx.Err())
		}
		res.State = state.RecoveryCouldNotComplete
		res.Reason = "the recovered copy could not be read back: " + safeError(readErr)
		return finish(res, state.RecoveryTest{CleanupState: res.CleanupState, BytesRead: &n})
	}

	matched := digestMatches(sum, expected)
	res.DigestMatched = &matched
	if !matched {
		cleanup()
		res.State = state.RecoveryFailed
		res.Reason = fmt.Sprintf("the recovered bytes hash to %s, but the digest recorded for this artifact is %s", sum, expected)
		return finish(res, state.RecoveryTest{DigestMatched: &matched, CleanupState: res.CleanupState, BytesRead: &n})
	}

	// The gate passed. Only now does the validator mean anything.
	deps.stage("validating")
	out, vErr := deps.Validator.Validate(ctx, ValidatorInput{
		Set: set, Artifact: rec.Artifact, Medium: copy_.Medium, File: staged.Path, Digest: sum, Size: n,
	})
	cleanup()
	// A cancelled test has no result, however the runner reported the script
	// it killed. It reports that as an outcome (disposition canceled) and not
	// as an error, so this has to look at the context itself: classified like
	// any other outcome, a cancel would be recorded as a failure of a copy
	// nobody finished testing.
	if ctx.Err() != nil {
		return res, fmt.Errorf("recoverytest: cancelled: %w", ctx.Err())
	}
	if vErr != nil {
		res.State = state.RecoveryCouldNotComplete
		res.Reason = "the validator could not be run: " + safeError(vErr)
		return finish(res, state.RecoveryTest{CleanupState: res.CleanupState, BytesRead: &n})
	}

	// Bounded to what the journal will store, so the result a surface shows
	// and the record the journal keeps are the same records.
	var cut bool
	res.Output, cut = BoundOutput(SanitizeOutput(string(out.Output), out.Truncated))
	res.OutputTruncated = out.Truncated || cut

	rt := state.RecoveryTest{
		DigestMatched: &matched, OutputTruncated: res.OutputTruncated, CleanupState: res.CleanupState, BytesRead: &n,
	}
	termination, ran := terminationOf(out)
	switch {
	case !ran:
		// The script never produced a verdict: the runner refused it, the
		// connection to the runner failed, or it was cancelled before it
		// started. That is a fact about the machinery and not about the copy.
		res.State = state.RecoveryCouldNotComplete
		res.Reason = "the validator could not be run: " + SanitizeLabel(out.Disposition+" "+out.Detail, 240)
		return finish(res, state.RecoveryTest{CleanupState: res.CleanupState, BytesRead: &n})
	case out.ID == "" || len(out.Hash) != 64:
		res.State = state.RecoveryCouldNotComplete
		res.Reason = "the validator ran but its identity was not reported, so the result cannot be attributed to anything"
		return finish(res, state.RecoveryTest{CleanupState: res.CleanupState, BytesRead: &n})
	}

	res.ValidatorID, res.ValidatorHash = SanitizeLabel(out.ID, 128), out.Hash
	res.Termination = termination
	if out.ExitCode != nil {
		res.ExitCode = *out.ExitCode
	}
	rt.ValidatorID, rt.ValidatorHash, rt.Termination = res.ValidatorID, res.ValidatorHash, termination

	switch {
	case out.Disposition == "exited" && out.ExitCode != nil && *out.ExitCode == 0 && termination == state.RecoveryTerminationExited:
		res.State = state.RecoveryPassed
		rt.NextDue = now.Add(cadenceOr(cadence))
	case out.Disposition == "timed_out":
		res.State = state.RecoveryFailed
		res.Reason = fmt.Sprintf("the validator did not finish inside its timeout and was terminated (%s)", termination)
	case termination == state.RecoveryTerminationUnconfirmed:
		res.State = state.RecoveryFailed
		res.Reason = "the validator's termination could not be confirmed, so its verdict was not relied on"
	case out.Disposition == "signaled":
		res.State = state.RecoveryFailed
		res.Reason = "the validator was killed by a signal"
	default:
		res.State = state.RecoveryFailed
		res.Reason = fmt.Sprintf("the validator exited %d", res.ExitCode)
	}
	return finish(res, rt)
}

// terminationOf maps what the workflow executor proved onto the recorded
// termination vocabulary, and says whether a verdict-bearing process ran at
// all.
//
// "exited" is a process that ended on its own and whose status was observed.
// A timeout or cancellation this product requested is "killed_group" when the
// executor PROVED the process gone and "unconfirmed" when it could not;
// anything the executor describes as unconfirmed is unconfirmed whatever the
// disposition, so a pass can never be built on a process nobody saw die.
func terminationOf(o ValidatorOutcome) (term string, ran bool) {
	switch o.Disposition {
	case "exited", "signaled":
		term = state.RecoveryTerminationExited
	case "timed_out", "canceled":
		term = state.RecoveryTerminationKilledGroup
	default:
		return "", false
	}
	if o.Termination == "unconfirmed" {
		term = state.RecoveryTerminationUnconfirmed
	} else if (o.Disposition == "timed_out" || o.Disposition == "canceled") && o.Termination != "confirmed" {
		term = state.RecoveryTerminationUnconfirmed
	}

	return term, true
}

func cadenceOr(c time.Duration) time.Duration {
	if c <= 0 {
		return 24 * time.Hour
	}
	return c
}

var knownGood = map[string]bool{
	"COMMITTED": true, "REMOTE_DELETE_PENDING": true, "COMPLETE": true, "REMOTE_RETAINED": true,
}

// Due reports whether the copy a Run would test for set is owed a test at
// now: it has never had a verdict-bearing attempt scheduled, or its next-due
// time has arrived. ErrNoKnownGoodCopy means there is nothing to be due.
func Due(ctx context.Context, j Journal, set model.BackupSetID, now time.Time) (bool, error) {
	_, p, err := pickCopy(ctx, Deps{Journal: j}, set, Request{})
	if err != nil {
		return false, err
	}
	return p.RecoveryNextDueAt == nil || !p.RecoveryNextDueAt.After(now), nil
}

// pickCopy chooses the artifact (the newest known good one, unless named) and
// the copy of it to test: a local copy when there is one, because reading it
// costs nothing, and otherwise the medium copy with the strongest recorded
// class.
func pickCopy(ctx context.Context, deps Deps, set model.BackupSetID, req Request) (state.Record, state.Placement, error) {
	var candidates []state.Record
	if req.Artifact != nil {
		rec, err := deps.Journal.Get(ctx, *req.Artifact)
		if err != nil {
			return state.Record{}, state.Placement{}, err
		}
		candidates = []state.Record{rec}
	} else {
		recs, err := deps.Journal.ListByBackupSet(ctx, set)
		if err != nil {
			return state.Record{}, state.Placement{}, fmt.Errorf("recoverytest: listing %s: %w", set, err)
		}
		candidates = recs
	}
	var usable []state.Record
	for _, r := range candidates {
		if !knownGood[r.State] {
			continue
		}
		if len(revalidate.ScrubTargets(r)) == 0 {
			continue
		}
		usable = append(usable, r)
	}
	if len(usable) == 0 {
		return state.Record{}, state.Placement{}, ErrNoKnownGoodCopy
	}
	sort.SliceStable(usable, func(i, j int) bool {
		if !usable[i].UpdatedAt.Equal(usable[j].UpdatedAt) {
			return usable[i].UpdatedAt.After(usable[j].UpdatedAt)
		}
		return usable[i].Artifact.String() < usable[j].Artifact.String()
	})
	rec := usable[0]
	targets := revalidate.ScrubTargets(rec)
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if a.IsLocal() != b.IsLocal() {
			return a.IsLocal()
		}
		if ra, rb := classRank(a.VerificationClass), classRank(b.VerificationClass); ra != rb {
			return ra > rb
		}
		return a.Medium < b.Medium
	})
	return rec, targets[0], nil
}

func classRank(c string) int {
	switch c {
	case state.VerificationContent:
		return 3
	case state.VerificationAttested:
		return 2
	case state.VerificationExistence:
		return 1
	}
	return 0
}

// costOf says what reading this copy would cost, or "" when it is free. A
// local copy is free. A copy on a storage medium is egress, and one in an
// archive class is a restore as well.
func costOf(deps Deps, p state.Placement) string {
	if p.IsLocal() {
		return ""
	}
	if deps.Mediums != nil {
		if m, ok := deps.Mediums.MediumFor(p.Medium); ok {
			if archive.IsArchive(m.StorageClass) {
				return fmt.Sprintf("the copy on %q is in an archive storage class, so reading it means a restore and a download", p.Medium)
			}
			// A directory on a disk this host can reach is not a download:
			// nothing is billed for reading it, so an unattended test may.
			// This is the same question the recovery resolver asks
			// (Chargeable), answered the same way.
			if m.Type == transport.MediumTypeLocalDir {
				return ""
			}
		}
	}
	return fmt.Sprintf("reading the copy on %q is a download from a storage medium", p.Medium)
}

func recordedDigest(rec state.Record, p state.Placement) (digest, alg string) {
	if p.Hash != "" && p.HashAlg != "" {
		return p.Hash, p.HashAlg
	}
	return rec.LocalHash, rec.LocalHashAlg
}

func hashFile(ctx context.Context, path string) (sum string, n int64, err error) {
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

// safeError renders an error for a field an operator reads: single line,
// control bytes removed, bounded. It deliberately does not try to scrub
// provider bodies, because it never includes them: callers pass an error
// from a filesystem or a Recoverer, and the Recoverer owns keeping provider
// text out of its errors.
func safeError(err error) string {
	return SanitizeLabel(err.Error(), 240)
}
