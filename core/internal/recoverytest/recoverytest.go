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
// # What it reuses, and what it adds no more of
//
// The validator runs through lifecycle.RunRestoreCheckOutcome, which is
// lifecycle.RunRestoreCheck's contract (fixed environment, its own process
// group, bounded capture, fail-closed timeout) plus the one thing a recovery
// test records that a plain verdict does not: how the process ended and
// whether the group was observed gone. No new execution boundary, no shell,
// no privilege. Output is untrusted terminal input, so it leaves this
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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/archive"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
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

// ErrNoKnownGoodCopy is returned when a set has nothing to test.
var ErrNoKnownGoodCopy = errors.New("recoverytest: this backup set has no known good copy to test")

// Validator runs the operator's validator against a staged file. The default
// is lifecycle.RunRestoreCheckOutcome.
type Validator func(ctx context.Context, cmd config.Command, path string) (lifecycle.RestoreCheckOutcome, error)

// Deps is what a Run needs.
type Deps struct {
	Journal   Journal
	Recoverer Recoverer
	// Mediums resolves a medium id, to tell a free copy from a chargeable one.
	Mediums revalidate.Mediums
	// Validator is the seam over the subprocess. Nil means the real one.
	Validator Validator
	// Limits is the shared admission control for full reads. Nil is unbounded.
	Limits *revalidate.Limits
	Now    func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now().UTC()
}

// Config is one set's recovery-test settings, already resolved.
type Config struct {
	// Command is the operator's declared validator. Nil means none is
	// declared, and the claim reads not configured.
	Command *config.Command

	// ValidatorID is the name the operator gave the validator. Empty means
	// the base name of the executable, never its path.
	ValidatorID string

	// Cadence is how often the newest known good copy is owed a test.
	Cadence time.Duration
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

// maxValidatorBytes bounds how much of a validator's executable is read to
// identify it. A validator larger than this is not a script an operator
// wrote, and an unbounded read of an arbitrary path is not something a
// scheduled job does.
const maxValidatorBytes = 256 << 20

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

	if cfg.Command == nil {
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

	id, hash, err := validatorIdentity(cfg)
	if err != nil {
		res.State = state.RecoveryCouldNotComplete
		res.Reason = "the validator could not be identified: " + err.Error()
		return finish(res, state.RecoveryTest{})
	}

	release, err := deps.Limits.Acquire(ctx, copy_.Medium)
	if err != nil {
		return res, err
	}
	defer release()

	staged, err := deps.Recoverer.Recover(ctx, RecoverRequest{Artifact: rec.Artifact, Copy: copy_, AcknowledgeCost: ack})
	if err != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("recoverytest: cancelled: %w", ctx.Err())
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
		if rerr := deps.Recoverer.Remove(context.WithoutCancel(ctx), staged); rerr != nil {
			res.CleanupState = state.RecoveryCleanupFailed
			res.CleanupErr = safeError(rerr)
			return
		}
		res.CleanupState = state.RecoveryCleanupRemoved
	}

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
	run := deps.Validator
	if run == nil {
		run = lifecycle.RunRestoreCheckOutcome
	}
	out, vErr := run(ctx, *cfg.Command, staged.Path)
	cleanup()
	if vErr != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("recoverytest: cancelled: %w", ctx.Err())
		}
		res.State = state.RecoveryCouldNotComplete
		res.Reason = "the validator could not be run: " + safeError(vErr)
		return finish(res, state.RecoveryTest{CleanupState: res.CleanupState, BytesRead: &n})
	}

	res.ValidatorID, res.ValidatorHash = id, hash
	res.Termination, res.ExitCode = out.Termination, out.ExitCode
	res.Output = SanitizeOutput(out.Output, out.Truncated)
	res.OutputTruncated = out.Truncated

	rt := state.RecoveryTest{
		DigestMatched: &matched, ValidatorID: id, ValidatorHash: hash, Termination: out.Termination,
		OutputTruncated: out.Truncated, CleanupState: res.CleanupState, BytesRead: &n,
	}
	switch {
	case out.Passed && out.Termination == lifecycle.TerminationExited:
		res.State = state.RecoveryPassed
		rt.NextDue = now.Add(cadenceOr(cadence))
	case out.TimedOut:
		res.State = state.RecoveryFailed
		res.Reason = fmt.Sprintf("the validator did not finish inside its %s timeout and was terminated (%s)", cfg.Command.Timeout.Duration(), out.Termination)
	case out.Termination == lifecycle.TerminationUnconfirmed:
		res.State = state.RecoveryFailed
		res.Reason = "the validator's process group could not be observed gone, so its verdict was not relied on"
	default:
		res.State = state.RecoveryFailed
		res.Reason = fmt.Sprintf("the validator exited %d", out.ExitCode)
	}
	return finish(res, rt)
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
		if m, ok := deps.Mediums.MediumFor(p.Medium); ok && archive.IsArchive(m.StorageClass) {
			return fmt.Sprintf("the copy on %q is in an archive storage class, so reading it means a restore and a download", p.Medium)
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

// validatorIdentity names the validator and hashes what would run. The id is
// the operator's name for it or the executable's base name, never a path; the
// hash is of the executable's bytes, so replacing the validator is a
// different recorded identity and an old pass is not silently inherited.
func validatorIdentity(cfg Config) (id, hash string, err error) {
	id = cfg.ValidatorID
	if id == "" {
		id = filepath.Base(cfg.Command.Executable)
	}
	id = SanitizeLabel(id, 128)
	if id == "" {
		return "", "", errors.New("it has no name")
	}
	f, err := os.Open(cfg.Command.Executable)
	if err != nil {
		return "", "", errors.New("its executable could not be opened")
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxValidatorBytes+1))
	if err != nil {
		return "", "", errors.New("its executable could not be read")
	}
	if n > maxValidatorBytes {
		return "", "", fmt.Errorf("its executable is larger than %d bytes", maxValidatorBytes)
	}
	return id, hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(ctx context.Context, path string) (sum string, n int64, err error) {
	f, err := os.Open(path)
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
