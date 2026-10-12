package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/recovery/export"
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
	if err == nil && written == 0 && d.size > 0 {
		// Nothing left the host (a conditional GET answered 304). That is not
		// a range served: it must neither cover the artifact nor claim the
		// download verified.
		return
	}
	if err != nil {
		// The end of the download, said once and plainly, ahead of the
		// per-range row below (which stays the last row, as it always was).
		d.recordEnd(start, written, err)
	}
	d.b.auditRecovery(context.Background(), &ev, err)
	if err != nil {
		d.b.finishRecoveryDownload(d.token)
		return
	}
	if d.b.recoveryHost().grants.Served(d.token, start, end, d.size) {
		d.recordVerified()
		d.b.finishRecoveryDownload(d.token)
	}
}

func (b *BackupService) finishRecoveryDownload(token string) {
	h := b.recoveryHost()
	if staged, ok := h.grants.Close(token).(*export.Staged); ok && staged != nil {
		if err := staged.Discard(); err != nil {
			b.logCleanupFailure(err)
		}
	}
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

	staged, serr := b.stageGrant(ctx, g)
	if serr != nil {
		h.grants.Close(token)
		b.auditRecovery(ctx, &ev, serr)
		return nil, nil, mapRecoveryError(serr)
	}

	switch g.Target.Kind {
	case export.TargetStaging:
		defer h.grants.Close(token)
		final, perr := h.staging.Publish(staged.Path, g.Target.Path)
		if perr != nil {
			b.discardStaged(staged)
			b.auditRecovery(ctx, &ev, perr)
			return nil, nil, mapRecoveryError(perr)
		}
		ev.bytes = staged.Size
		b.auditRecovery(ctx, &ev, nil)
		return nil, &RecoveryResult{
			ArtifactID: g.Artifact, Copy: g.Copy, Target: recoveryTargetOf(g.Target),
			Path: final, Bytes: staged.Size, SHA256: staged.Digest,
		}, nil

	case export.TargetStorage:
		defer h.grants.Close(token)
		defer b.discardStaged(staged)
		if uerr := b.uploadRecovered(ctx, staged, g.Target); uerr != nil {
			b.auditRecovery(ctx, &ev, uerr)
			return nil, nil, mapRecoveryError(uerr)
		}
		ev.bytes = staged.Size
		b.auditRecovery(ctx, &ev, nil)
		return nil, &RecoveryResult{
			ArtifactID: g.Artifact, Copy: g.Copy, Target: recoveryTargetOf(g.Target),
			Key: g.Target.Key, Bytes: staged.Size, SHA256: staged.Digest,
		}, nil
	}
	h.grants.Close(token)
	b.discardStaged(staged)
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
func (b *BackupService) stageGrant(ctx context.Context, g export.Grant) (*export.Staged, error) {
	// The grant already carries the cost acknowledgement: a chargeable
	// copy could not have been minted without it.
	plan, err := b.planRecovery(ctx, g.Artifact, g.Copy, true)
	if err != nil {
		return nil, err
	}
	if plan.resolved.NeedsThaw {
		return nil, recoveryErr(RecoveryNotReadable, "the copy on %s needs a provider restore before it can be read", g.Copy)
	}
	st := b.state.Load()
	src, err := b.sourceFor(st, plan)
	if err != nil {
		return nil, err
	}
	return b.recoveryHost().engine.Stage(ctx, src, plan.expect)
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
		if s, ok := v.(*export.Staged); ok {
			b.discardStaged(s)
		}
	}
}

// sweepRecoveryIfDue runs the TTL sweep at most once a minute, from the
// paths that already take a request, so no background goroutine is needed
// to keep staging bounded.
func (b *BackupService) sweepRecoveryIfDue(ctx context.Context) {
	h := b.recoveryHost()
	for _, v := range h.grants.Reap() {
		if s, ok := v.(*export.Staged); ok {
			b.discardStaged(s)
		}
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
