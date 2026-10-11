package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/recovery/export"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// ErrRecoveryRangeNotSatisfiable is what the HTTP layer reports to Served when
// the client asked for a byte range that does not exist in the copy. It is
// the client's mistake and not a failure of the download: the row says it was
// refused, and the grant and the staged copy stay, because spending them on a
// typo would cost a remote copy a second read.
var ErrRecoveryRangeNotSatisfiable = errors.New("service: that byte range is not in the recovered copy")

// RecoveryStream is what the HTTP layer needs from a started download. It is
// an interface so that layer can be tested against a double that records
// exactly what it was told was served.
type RecoveryStream interface {
	// FileName is the name to offer the client.
	FileName() string

	// Length is the size of the verified copy.
	Length() int64

	// Reader is the verified bytes, seekable for Range requests.
	Reader() io.ReadSeeker

	// Close releases this request's handle. It does not spend the grant.
	Close()

	// Served records one request's outcome: which bytes went, how many were
	// written and whether it worked.
	Served(start, end, written int64, err error)
}

var _ RecoveryStream = (*RecoveryDownload)(nil)

// RecoveryDownload is a started browser download: a chunk-verified stream
// over the retained copy (recover_stream.go) that the HTTP layer serves ranges
// of, one request at a time. Nothing is staged.
//
// The grant is spent when every byte has been served (in any number of
// ranges, in any order), when any request
// fails, or when the session ends or the client stops coming back inside
// the grant's TTL. Until then further requests with the same token are
// continuations (the ranges a browser asks for) and each writes its own
// action-log row.
type RecoveryDownload struct {
	name    string
	size    int64
	content *export.VerifiedReader
	index   *export.ChunkIndex
	etag    string

	b      *BackupService
	token  string
	actor  string
	ev     recoveryEvent
	closed sync.Once
	trace  *recoveryTrace
}

// FileName implements RecoveryStream.
func (d *RecoveryDownload) FileName() string { return d.name }

// Length implements RecoveryStream.
func (d *RecoveryDownload) Length() int64 { return d.size }

// Reader implements RecoveryStream.
func (d *RecoveryDownload) Reader() io.ReadSeeker { return d.content }

// Close releases this request's read of the copy. It does not spend the
// grant; Served does that.
func (d *RecoveryDownload) Close() {
	d.closed.Do(func() {
		_ = d.content.Close()
		// The request is over: the grant's idle clock restarts from now.
		d.b.recoveryHost().grants.Done(d.token)
	})
}

// Served records one request's outcome: which bytes went, and whether it
// worked. A failed request spends the grant. So do the ranges that, taken
// together, are the whole artifact, and that moment is also when the
// download's final verification is written to the trail.
func (d *RecoveryDownload) Served(start, end, written int64, err error) {
	ev := d.ev
	ev.op = "range"
	ev.bytes = written
	ev.rng = strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(end, 10) + "/" + strconv.FormatInt(d.size, 10)
	if d.b == nil {
		return
	}
	if errors.Is(err, ErrRecoveryRangeNotSatisfiable) {
		// The client's mistake. Recorded as a refusal, and the grant and
		// the staged copy stay for the next, valid range.
		d.b.auditRecovery(context.Background(), &ev, err)
		return
	}
	d.b.auditRecovery(context.Background(), &ev, err)
	if err != nil {
		d.endTrace(err)
		d.b.finishRecoveryDownload(d.token)
		return
	}
	if d.b.recoveryHost().grants.Served(d.token, start, end, d.size) {
		d.recordVerified()
		d.endTrace(nil)
		d.b.finishRecoveryDownload(d.token)
	}
}

// endTrace writes the recovery's end line: a failed range is a failed
// recovery, and the last byte served is the Complete stage. (A download that
// is abandoned is ended by dropDownload.)
func (d *RecoveryDownload) endTrace(err error) {
	if d.trace == nil {
		return
	}
	if err != nil {
		d.trace.fail(context.Background(), err)
		return
	}
	d.trace.stage(context.Background(), "cleaning_temporary_files")
	d.trace.complete(context.Background(), d.size)
}

// recoveryDownloadState is what a started download keeps on its grant: the
// chunk index its continuations use without a second look at the disk, and
// the slot it holds against the download ceiling. The slot is given back
// by whichever of completion, failure, the reaper or a logout gets there
// first.
type recoveryDownloadState struct {
	index   *export.ChunkIndex
	release func()
	trace   *recoveryTrace
}

// dropDownload gives back the stream slot.
func (b *BackupService) dropDownload(v any) {
	st, ok := v.(*recoveryDownloadState)
	if !ok || st == nil {
		return
	}
	if st.release != nil {
		st.release()
	}
	// Whoever gets here first ends the story. A download that was fully
	// served has already said so (Served); this is the client that never
	// came back, or a logout, and it is not a success.
	st.trace.fail(context.Background(), recoveryErr(RecoveryInterrupted, "the download was abandoned before it was collected"))
}

func (b *BackupService) finishRecoveryDownload(token string) {
	h := b.recoveryHost()
	b.dropDownload(h.grants.Close(token))
}

func (b *BackupService) logCleanupFailure(err error) {
	// Separately visible from a recovery failure: this one means the
	// staging disk is filling, not that an operator lacks their bytes.
	b.logger.Error(context.Background(), "recovery_cleanup", err)
}

// RedeemRecovery presents a grant. A download target comes back as a
// *RecoveryDownload to serve; a staging or storage target is carried out
// here and comes back as a *RecoveryResult.
func (b *BackupService) RedeemRecovery(ctx context.Context, sess RecoverySession, token string) (dl RecoveryStream, res *RecoveryResult, err error) {
	h, herr := b.recoveryReady()
	if herr != nil {
		return nil, nil, herr
	}
	b.sweepRecoveryIfDue(ctx)

	ev := recoveryEvent{op: "run", actor: sess.Actor}
	redeem, rerr := h.grants.Redeem(token, sess.toExport())
	if rerr != nil {
		b.auditRecovery(ctx, &ev, rerr)
		return nil, nil, mapRecoveryError(rerr)
	}
	g := redeem.Grant
	ev.artifact, ev.copy, ev.target = g.Artifact, g.Copy, recoveryTargetOf(g.Target).label()

	if g.Target.Kind == export.TargetDownload {
		// Streamed, never staged. A first request and a range after it run
		// the same pre-flight, and two requests at once share one
		// preparation read, so there is no "still being prepared" to wait
		// out.
		d, derr := b.redeemDownload(ctx, h, sess, token, redeem, ev)
		if derr != nil {
			return nil, nil, derr
		}
		return d, nil, nil
	}
	if !redeem.First {
		// A server-side target is carried out in the request that redeems
		// it. A second request for the same token while the first is still
		// at it has nothing to continue.
		h.grants.Done(token)
		berr := recoveryErr(RecoveryBusy, "this recovery is still being prepared; try the same link again in a moment")
		b.auditRecovery(ctx, &ev, berr)
		return nil, nil, berr
	}

	trace := b.newRecoveryTrace(ctx, g.Artifact)
	staged, serr := b.stageGrant(ctx, g, trace)
	if serr == nil && h.afterStage != nil {
		h.afterStage()
	}
	if serr == nil && ctx.Err() != nil {
		// Cancelled between verifying and delivering: nothing has been
		// placed, so nothing should be left either.
		b.discardStaged(staged)
		serr = ctx.Err()
	}
	if serr != nil {
		h.grants.Close(token)
		trace.fail(ctx, serr)
		b.auditRecovery(ctx, &ev, serr)
		return nil, nil, mapRecoveryError(serr)
	}

	switch g.Target.Kind {
	case export.TargetStaging:
		defer h.grants.Close(token)
		trace.stage(ctx, "writing_target")
		final, perr := staged.PublishTo(g.Target.Path)
		if perr != nil {
			b.discardStaged(staged)
			trace.fail(ctx, perr)
			b.auditRecovery(ctx, &ev, perr)
			return nil, nil, mapRecoveryError(perr)
		}
		ev.bytes = staged.Size
		trace.stage(ctx, "cleaning_temporary_files")
		trace.complete(ctx, staged.Size)
		b.auditRecovery(ctx, &ev, nil)
		return nil, &RecoveryResult{
			ArtifactID: g.Artifact, Copy: g.Copy, Target: recoveryTargetOf(g.Target),
			Path: final, Bytes: staged.Size, SHA256: staged.Digest,
		}, nil

	case export.TargetStorage:
		defer h.grants.Close(token)
		discarded := false
		defer func() {
			if !discarded {
				b.discardStaged(staged)
			}
		}()
		trace.stage(ctx, "writing_target")
		if uerr := b.uploadRecovered(ctx, staged, g.Target); uerr != nil {
			trace.fail(ctx, uerr)
			b.auditRecovery(ctx, &ev, uerr)
			return nil, nil, mapRecoveryError(uerr)
		}
		ev.bytes = staged.Size
		trace.stage(ctx, "cleaning_temporary_files")
		b.discardStaged(staged)
		discarded = true
		trace.complete(ctx, staged.Size)
		b.auditRecovery(ctx, &ev, nil)
		return nil, &RecoveryResult{
			ArtifactID: g.Artifact, Copy: g.Copy, Target: recoveryTargetOf(g.Target),
			Key: g.Target.Key, Bytes: staged.Size, SHA256: staged.Digest,
		}, nil
	}
	h.grants.Close(token)
	b.discardStaged(staged)
	trace.fail(ctx, recoveryErr(RecoveryTargetInvalid, "unknown target kind"))
	return nil, nil, recoveryErr(RecoveryTargetInvalid, "unknown target kind")
}

func recoveryTargetOf(t export.Target) RecoveryTarget {
	return RecoveryTarget{Kind: string(t.Kind), Path: t.Path, Medium: t.Medium, Key: t.Key}
}

func (b *BackupService) discardStaged(s *export.Staged) {
	if err := s.Discard(); err != nil {
		b.logCleanupFailure(err)
	}
}

// stageGrant re-derives the plan from the journal as it is NOW, so a copy
// that was deleted or repointed between mint and redeem is refused, and
// stages the chosen copy through the verifying engine.
func (b *BackupService) stageGrant(ctx context.Context, g export.Grant, trace *recoveryTrace) (*export.Staged, error) {
	h := b.recoveryHost()
	// One execution per (artifact, copy): two operators asking for the same
	// recovery at once is one read of a copy that may be billed to read, and
	// each of them is handed their own handle on the result.
	return h.flight.Do(ctx, g.Artifact+"\x00"+g.Copy, func(ctx context.Context) (*export.Staged, error) {
		// The grant already carries the cost acknowledgement: a chargeable
		// copy could not have been minted without it.
		trace.stage(ctx, "resolving_copy")
		plan, err := b.planRecovery(ctx, g.Artifact, g.Copy, true)
		if err != nil {
			return nil, err
		}
		if plan.resolved.NeedsThaw {
			return nil, recoveryErr(RecoveryNotReadable, "the copy on %s needs a provider restore before it can be read", g.Copy)
		}
		st := b.state.Load()
		if err := b.checkProviderChecksum(ctx, st, plan); err != nil {
			return nil, err
		}
		src, err := b.sourceFor(st, plan)
		if err != nil {
			return nil, err
		}
		trace.stage(ctx, "reading")
		staged, err := h.engine.Stage(ctx, src, plan.expect)
		if err != nil {
			return nil, err
		}
		// The engine reads and hashes in one pass and refuses on a mismatch
		// before it returns, so this stage is entered with the verdict in
		// hand.
		trace.stage(ctx, "verifying")
		return staged, nil
	})
}

// checkProviderChecksum asks the destination for its own checksum of the
// object before a byte of it is downloaded. It is one metadata call and no
// egress, and where the destination can attest a full-object SHA-256 and
// disagrees with the one recorded at ingestion, that is a positive finding
// that the copy is not the recorded bytes: the recovery is refused, and the
// download that would have been billed is not made.
//
// It never refuses on silence. A destination that cannot attest (the S3
// adapter this product ships cannot attest a SHA-256 at all), attests another
// algorithm, or does not answer has said nothing, and the bytes are verified
// on the way out regardless, which is the stronger check.
func (b *BackupService) checkProviderChecksum(ctx context.Context, st *configState, plan recoveryPlan) error {
	c := plan.resolved.Copy
	if c.Local {
		return nil
	}
	store, _ := st.inner.Transport.(placement.Store)
	if store == nil {
		return nil
	}
	var p state.Placement
	for _, candidate := range plan.rec.Placements {
		if candidate.Medium == c.Medium {
			p = candidate
		}
	}
	res, err := placement.Verify(ctx, store, plan.medium, p, placement.Attested, now())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	if !res.Passed {
		return &RecoveryError{
			Code:    RecoveryRemoteCopyCorrupt,
			Message: "the destination's own checksum for the copy on " + c.Medium + " does not match the one recorded when this backup was ingested, so it was not downloaded",
		}
	}
	return nil
}

// uploadRecovered writes a verified staged file to another configured
// destination, refusing to replace an object that is already there.
func (b *BackupService) uploadRecovered(ctx context.Context, staged *export.Staged, t export.Target) error {
	st := b.state.Load()
	store, _ := st.inner.Transport.(transport.MediumStore)
	if store == nil {
		return recoveryErr(RecoveryUnavailable, "this deployment has no way to reach a storage medium")
	}
	medium, err := b.mediumForRecovery(st, t.Medium)
	if err != nil {
		return err
	}
	if _, serr := store.StatObject(ctx, medium, t.Key); serr == nil {
		return recoveryErr(RecoveryTargetExists, "%s already holds an object at %s; recovery never replaces one", t.Medium, t.Key)
	} else if cat, ok := transport.CategoryOf(serr); !ok || cat != transport.NotFound {
		return fmt.Errorf("checking the destination before writing: %w", serr)
	}
	_, err = store.UploadFromLocal(ctx, medium, staged.Path, t.Key, transport.UploadOptions{})
	return err
}

// RecoverNow is what the terminal does: mint and redeem in one step, in
// this process, through exactly the code the two routes use. For a
// download target the verified bytes are copied to out.
func (b *BackupService) RecoverNow(ctx context.Context, sess RecoverySession, req RecoveryRequest, out io.Writer) (RecoveryResult, *RecoveryThaw, error) {
	grant, err := b.MintRecovery(ctx, sess, req)
	if err != nil {
		return RecoveryResult{}, nil, err
	}
	if grant.Thaw != nil {
		return RecoveryResult{ArtifactID: req.ArtifactID, Copy: grant.Copy}, grant.Thaw, nil
	}
	dl, res, err := b.RedeemRecovery(ctx, sess, grant.Token)
	if err != nil {
		return RecoveryResult{}, nil, err
	}
	if res != nil {
		return *res, nil, nil
	}
	defer dl.Close()
	if out == nil {
		out = io.Discard
	}
	n, cerr := io.Copy(out, dl.Reader())
	if n < 0 {
		n = 0
	}
	end := n - 1
	dl.Served(0, end, n, cerr)
	if cerr != nil {
		return RecoveryResult{}, nil, mapRecoveryError(cerr)
	}
	out2 := RecoveryResult{ArtifactID: req.ArtifactID, Copy: grant.Copy, Target: req.Target, Bytes: n}
	if v, ok := dl.(RecoveryValidator); ok {
		// Every chunk written matched the digest the journal recorded, so
		// this is the digest of what was written, not a promise about it.
		out2.SHA256 = v.SHA256()
	}
	return out2, nil, nil
}

// EndRecoverySession revokes every grant the session minted and removes any
// download it had staged. A logout calls it.
func (b *BackupService) EndRecoverySession(sess RecoverySession) {
	h, err := b.recoveryReady()
	if err != nil {
		return
	}
	for _, v := range h.grants.EndSession(sess.toExport()) {
		b.dropDownload(v)
	}
}

// sweepRecoveryIfDue runs the TTL sweep at most once a minute, from the
// paths that already take a request, so no background goroutine is needed
// to keep staging bounded.
func (b *BackupService) sweepRecoveryIfDue(ctx context.Context) {
	h := b.recoveryHost()
	for _, v := range h.grants.Reap() {
		b.dropDownload(v)
	}
	h.sweepMu.Lock()
	due := now().Sub(h.lastSweep) >= time.Minute
	if due {
		h.lastSweep = now()
	}
	h.sweepMu.Unlock()
	if !due {
		return
	}
	if _, err := h.indexes.store.Sweep(); err != nil {
		b.logger.Error(ctx, "recovery_index_sweep", err)
	}
	if rep, err := h.staging.Sweep(); err != nil {
		b.logger.Error(ctx, "recovery_cleanup", err)
	} else {
		b.reportRecoveryCleanup(ctx, "sweep", rep)
	}
}

// ReconcileRecoveryStaging is the startup pass for the process that serves:
// any partial left by a process that died is an orphan, and is removed. It
// reports what it removed, and a removal that failed is reported apart.
//
// It is called by the serving process only. A terminal command opening the
// same deployment beside a running engine must not reconcile, because to
// it the engine's in-flight partials look exactly like orphans.
func (b *BackupService) ReconcileRecoveryStaging(ctx context.Context) (removed, failed int, err error) {
	h, err := b.recoveryReady()
	if err != nil {
		return 0, 0, err
	}
	rep, err := h.staging.Reconcile()
	if err != nil {
		return 0, 0, err
	}
	b.reportRecoveryCleanup(ctx, "reconcile", rep)
	return len(rep.Removed), len(rep.Failed), nil
}

func (b *BackupService) reportRecoveryCleanup(ctx context.Context, what string, rep export.SweepReport) {
	if len(rep.Removed) == 0 && len(rep.Failed) == 0 {
		return
	}
	for _, f := range rep.Failed {
		b.logger.Error(ctx, "recovery_cleanup", fmt.Errorf("recovery staging %s could not remove %s: %w", what, f.Path, f.Err))
	}
	if len(rep.Removed) > 0 {
		b.logger.Completed(ctx, obs.ResultInfo, "recovery_cleanup",
			fmt.Sprintf("recovery staging %s removed %d leftover file(s)", what, len(rep.Removed)))
	}
}

func (b *BackupService) mediumForRecovery(st *configState, id string) (transport.Medium, error) {
	m, _, err := app.MediumFor(st.inner.Config, id)
	if err != nil {
		return transport.Medium{}, recoveryErr(RecoveryTargetInvalid, "%q is not a configured storage destination", id)
	}
	return m, nil
}

// recoveryChunkSize is the verification and memory unit of a streamed
// download. A variable so a test can use a small one.
var recoveryChunkSize = export.DefaultChunkSize

// recoveryReapEvery is how often the serving process reaps lapsed grants and
// sweeps the staging area without a request to do it. A variable so a test
// need not wait a minute.
var recoveryReapEvery = time.Minute

// runRecoveryReaper reaps lapsed grants and sweeps staging on a timer until
// ctx ends. Without it a download the client walked away from keeps its
// staged copy (and a download-stream slot) until the next recovery request,
// which on a quiet deployment may be days.
func (b *BackupService) runRecoveryReaper(ctx context.Context) {
	for {
		timer := time.NewTimer(recoveryReapEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		b.reapRecoveryOnce(ctx)
	}
}

// reapRecoveryOnce is one reaper pass, with the panic recovery the other
// background passes in this process have: it shares a process with the API.
func (b *BackupService) reapRecoveryOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error(context.Background(), "recovery-reap-panic", fmt.Errorf("recovered panic: %v", r))
		}
	}()
	if _, err := b.recoveryReady(); err != nil {
		return
	}
	b.sweepRecoveryIfDue(ctx)
}

// recoveryTrace is one recovery's start, stage and end lines on the activity
// feed (obs.RecoveryStart, RecoveryStage, RecoveryEnd). They share an
// operation id, so the CLI's --follow and the browser follow one recovery,
// and the end line is written exactly once however the recovery finishes:
// served, failed, or abandoned by a client that never came back.
type recoveryTrace struct {
	b        *BackupService
	opID     string
	set      string
	artifact string
	started  time.Time
	once     sync.Once
}

func (b *BackupService) newRecoveryTrace(ctx context.Context, artifact string) *recoveryTrace {
	t := &recoveryTrace{b: b, opID: uuid.NewString(), artifact: artifact, started: now()}
	if id, err := app.ParseArtifactID(artifact); err == nil {
		t.set = id.Set.String()
	}
	b.logger.RecoveryStart(ctx, t.opID, t.set, t.artifact)
	return t
}

func (t *recoveryTrace) stage(ctx context.Context, id string) {
	if t == nil {
		return
	}
	t.b.logger.RecoveryStage(ctx, t.opID, t.set, id)
}

// complete writes the Complete stage and a success end line.
func (t *recoveryTrace) complete(ctx context.Context, bytes int64) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		t.b.logger.RecoveryStage(ctx, t.opID, t.set, "complete")
		t.b.logger.RecoveryEnd(ctx, t.opID, t.set, t.artifact, bytes, now().Sub(t.started), obs.ResultSuccess, "")
	})
}

// fail writes an end line that is not a success. It never writes Complete.
func (t *recoveryTrace) fail(ctx context.Context, err error) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		result, reason := obs.ResultError, "the recovery failed"
		if re, ok := AsRecoveryError(mapRecoveryError(err)); ok {
			reason = re.Code
			if isRefusal(re.Code) {
				result = obs.ResultWarn
			}
		}
		t.b.logger.RecoveryEnd(ctx, t.opID, t.set, t.artifact, 0, now().Sub(t.started), result, reason)
	})
}
