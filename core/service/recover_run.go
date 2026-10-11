package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/recovery/export"
	"github.com/retnd/retnd/core/internal/transport"
)

// RecoveryDownload is a started browser download: a verified, staged copy
// the HTTP layer serves ranges of, one request at a time.
//
// The grant is spent when the last byte has been served, when any request
// fails, or when the session ends or the client stops coming back inside
// the grant's TTL. Until then further requests with the same token are
// continuations (the ranges a browser asks for) and each writes its own
// action-log row.
type RecoveryDownload struct {
	// Name is the file name to offer, the artifact's own.
	Name string

	// Size is the length of the verified copy.
	Size int64

	// Content is the verified bytes, seekable so the HTTP layer can answer
	// Range requests. Close releases this request's handle.
	Content *os.File

	b      *BackupService
	token  string
	actor  string
	ev     recoveryEvent
	closed sync.Once
}

// Close releases the file handle of this request. It does not spend the
// grant; Served does that.
func (d *RecoveryDownload) Close() { _ = d.Content.Close() }

// Served records one request's outcome: which bytes went, and whether it
// worked. A failed request, or one that reached the last byte, spends the
// grant and removes the staged copy.
func (d *RecoveryDownload) Served(start, end, written int64, err error) {
	ev := d.ev
	ev.op = "range"
	ev.bytes = written
	ev.rng = strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(end, 10) + "/" + strconv.FormatInt(d.Size, 10)
	d.b.auditRecovery(context.Background(), &ev, err)
	if err != nil || (d.Size == 0 || end >= d.Size-1) {
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
func (b *BackupService) RedeemRecovery(ctx context.Context, sess RecoverySession, token string) (dl *RecoveryDownload, res *RecoveryResult, err error) {
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

	if !redeem.First {
		staged, ok := redeem.Attached.(*export.Staged)
		if !ok || staged == nil || g.Target.Kind != export.TargetDownload {
			berr := recoveryErr(RecoveryBusy, "this recovery is still being prepared; try the same link again in a moment")
			b.auditRecovery(ctx, &ev, berr)
			return nil, nil, berr
		}
		d, oerr := b.newRecoveryDownload(staged, token, sess.Actor, ev)
		if oerr != nil {
			b.finishRecoveryDownload(token)
			b.auditRecovery(ctx, &ev, oerr)
			return nil, nil, mapRecoveryError(oerr)
		}
		return d, nil, nil
	}

	staged, serr := b.stageGrant(ctx, g)
	if serr != nil {
		h.grants.Close(token)
		b.auditRecovery(ctx, &ev, serr)
		return nil, nil, mapRecoveryError(serr)
	}

	switch g.Target.Kind {
	case export.TargetDownload:
		h.grants.Attach(token, staged)
		d, oerr := b.newRecoveryDownload(staged, token, sess.Actor, ev)
		if oerr != nil {
			b.finishRecoveryDownload(token)
			b.auditRecovery(ctx, &ev, oerr)
			return nil, nil, mapRecoveryError(oerr)
		}
		return d, nil, nil

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

func (b *BackupService) newRecoveryDownload(staged *export.Staged, token, actor string, ev recoveryEvent) (*RecoveryDownload, error) {
	f, err := staged.Open()
	if err != nil {
		return nil, err
	}
	name := filepath.Base(ev.artifact)
	return &RecoveryDownload{Name: name, Size: staged.Size, Content: f, b: b, token: token, actor: actor, ev: ev}, nil
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
	n, cerr := io.Copy(out, dl.Content)
	if n < 0 {
		n = 0
	}
	end := n - 1
	dl.Served(0, end, n, cerr)
	if cerr != nil {
		return RecoveryResult{}, nil, mapRecoveryError(cerr)
	}
	return RecoveryResult{ArtifactID: req.ArtifactID, Copy: grant.Copy, Target: req.Target, Bytes: n}, nil, nil
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
