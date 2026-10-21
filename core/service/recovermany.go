package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/recovery/export"
)

// Recovering several backups to one destination (#1106, #1107).
//
// RecoverMany is the one general call. A whole backup set is the case that
// lists every restore point of the set (Set), and a selection from the
// Backups list is the case that names them (Artifacts). Both go through the
// same plan, the same per-file recovery code as a single recovery
// (planRecovery, stageGrant, the verifying engine) and the same manifest.

// RecoverManyRequest is one operator's ask. Exactly one of Artifacts and Set
// is given.
type RecoverManyRequest struct {
	// Artifacts are backup ids, "source/set/name" each.
	Artifacts []string

	// Set is a "source/set" id: every retained, known-good backup of the
	// set. NewestOnly narrows it to the newest restore point, Since to those
	// updated at or after that moment and Until to those updated at or
	// before it (#1251). All three only apply with Set.
	Set        string
	NewestOnly bool
	Since      time.Time
	Until      time.Time

	// Copy is a medium id, or "" / "best-available", applied to every file.
	Copy string

	// Target is where everything goes: "staging" (a folder name under the
	// recovery staging root) or "storage" (a key prefix on another
	// configured destination). "download" is refused: many files do not fit
	// one browser download, and the refusal says where to send them.
	Target RecoveryTarget

	// AcknowledgeCost is the explicit yes to the cost shown by the plan,
	// once, for the whole request.
	AcknowledgeCost bool
}

// The statuses of one file in a plan or a result.
const (
	RecoverManyReady         = "ready"
	RecoverManyRecovered     = "recovered"
	RecoverManyFailed        = "failed"
	RecoverManySkipped       = "skipped"
	RecoverManyNeedsThaw     = "needs_thaw"
	RecoverManyQuarantined   = "quarantined"
	RecoverManyUnrecoverable = "unrecoverable"
)

// RecoverManyItem is one file of a plan or a result.
type RecoverManyItem struct {
	ArtifactID string
	Name       string
	Status     string
	Copy       string

	// SizeBytes is what the journal recorded, when it recorded one.
	SizeBytes *int64
	// Chargeable is true when reading this file bills the operator.
	Chargeable bool

	// Bytes and SHA256 are what was delivered, for a recovered file.
	Bytes  int64
	SHA256 string
	// Path is the full path under the staging root, Key the object key on a
	// storage destination, Rel the name the manifest lists the file under.
	Path string
	Key  string
	Rel  string

	// Code and Message say why a file was not recovered.
	Code    string
	Message string
	Thaw    *RecoveryThaw

	// Verified is, for a file placed on a source host (#1193), "sha256"
	// (the host's own digest matched) or "size" (it cannot compute one).
	Verified string
}

// RecoverManyTotals counts a plan or a result.
type RecoverManyTotals struct {
	Count         int
	Ready         int
	Recovered     int
	Failed        int
	Skipped       int
	NeedsThaw     int
	Quarantined   int
	Unrecoverable int
	Bytes         int64
}

// RecoverManyPlan is the pre-flight: what would be recovered, what would not
// and why, and what the destination can hold.
type RecoverManyPlan struct {
	Set    string
	Target RecoveryTarget
	// Folder is where the files go, relative to the destination root.
	Folder string
	Items  []RecoverManyItem
	Totals RecoverManyTotals

	ChargeableCount int
	ChargeableBytes int64
	// UnknownSizes counts ready files the journal recorded no size for.
	UnknownSizes int

	// FreeBytes and ReserveBytes describe the staging disk, for a staging
	// target. FreeBytes is nil where the destination cannot say.
	FreeBytes    *int64
	ReserveBytes int64

	// For a source target (#1193): the destination the run would write
	// into, and the host's free space (nil when it cannot say) and the
	// reserve a run leaves there.
	DestinationID      string
	URL                string
	SourceFreeBytes    *int64
	SourceReserveBytes int64
}

// RecoverManyResult is what a run reports, and what its manifest says.
type RecoverManyResult struct {
	OperationID string
	Set         string
	Target      RecoveryTarget
	Folder      string
	Items       []RecoverManyItem
	Totals      RecoverManyTotals
	Cancelled   bool

	// DestinationID and URL say where a source-host run wrote (#1193).
	DestinationID string
	URL           string

	// ManifestPath (staging) or ManifestKey (storage) is the manifest file.
	ManifestPath   string
	ManifestKey    string
	ManifestSHA256 string
}

// RecoverManyProgress is one event of a run. Kind is "plan" (once, after the
// pre-flight passed and before any byte is read), "start" (a file is about to
// be read) or "item" (a file finished, with its outcome). DoneBytes and
// TotalBytes are the running total.
type RecoverManyProgress struct {
	Kind       string
	Index      int
	Total      int
	Item       RecoverManyItem
	Plan       *RecoverManyPlan
	DoneBytes  int64
	TotalBytes int64
}

// ErrRecoverManyInvalid is returned for a request that is malformed rather
// than refused: both a set and a list, neither, or a narrowing that has no
// set to narrow.
var ErrRecoverManyInvalid = errors.New("service: a recovery of several backups names either one backup set or a list of backups")

const (
	manifestVersion  = 1
	manifestFileName = "recovery-manifest.json"
	manyDefaultKey   = "recovered"
)

// restorePointStates are FR-19's valid restore points, the same four
// core/internal/health calls known-good. Every other state is not a restore
// point, and a set recover lists such a row instead of reading it.
var restorePointStates = map[string]bool{
	string(lifecycle.Committed):           true,
	string(lifecycle.RemoteDeletePending): true,
	string(lifecycle.Complete):            true,
	string(lifecycle.RemoteRetained):      true,
}

// recoverManyScope is a request after validation: the destination, the rows
// it covers, and where in the destination each lands.
type recoverManyScope struct {
	target   RecoveryTarget
	exTarget export.Target
	arts     []Artifact
	set      string
	folder   string
	// source is the resolved source host, for a source target.
	source resolvedSourceHost
}

// manyFolderFor is the per-set folder a backup lands in under the target.
func manyFolderFor(base, source, set string) string {
	if base == "" {
		return path.Join(source, set)
	}
	return path.Join(base, source, set)
}

func (b *BackupService) scopeRecoverMany(ctx context.Context, h *recoveryHost, req RecoverManyRequest) (recoverManyScope, error) {
	var sc recoverManyScope
	switch {
	case req.Set != "" && len(req.Artifacts) > 0, req.Set == "" && len(req.Artifacts) == 0:
		return sc, ErrRecoverManyInvalid
	case req.Set == "" && (req.NewestOnly || !req.Since.IsZero() || !req.Until.IsZero()):
		return sc, fmt.Errorf("%w: newest-only, since and until narrow a backup set, so they need one", ErrRecoverManyInvalid)
	}

	t := req.Target
	if t.Kind == "download" || t.Kind == "" {
		return sc, recoveryErr(RecoveryTargetInvalid, "several backups cannot go to one browser download or standard output: recover them into a folder under the staging root (a name, or --to NAME) or onto another storage destination (--to medium:ID/PREFIX)")
	}
	if t.Kind == "storage" && t.Key == "" {
		t.Key = manyDefaultKey
	}
	if !RecoveryTargetWritesToSourceHost(t) {
		// A source target is checked once the rows are known, because it
		// is the rows' own set that decides where it writes.
		ex, err := b.validateRecoveryTarget(h, "", t)
		if err != nil {
			return sc, err
		}
		sc.target, sc.exTarget = t, ex
		if t.Kind == "storage" {
			sc.target.Key = ex.Key
		}
	}

	st := b.state.Load()
	if req.Set != "" {
		source, set, ok := splitBackupSetID(req.Set)
		if !ok {
			return sc, fmt.Errorf("%w: %s", ErrBackupSetNotFound, req.Set)
		}
		sc.set = req.Set
		if bs, found := configuredSet(st.inner.Config.Sources, model.BackupSetID{Source: source, Set: set}); found && bs.Engine == model.EngineKopia {
			return sc, recoveryErr(RecoveryUseSnapshotRestore,
				"%s is stored by the kopia engine, whose backups are restore points rather than files, so the whole set is a snapshot restore and not a copy of files: use `%s snapshot restore %s` to extract one",
				req.Set, recoverVerb, req.Set)
		}
		all, lerr := b.ListArtifacts(ctx, ArtifactFilter{BackupSetID: req.Set})
		if lerr != nil {
			return sc, lerr
		}
		sc.arts = narrowSet(all, req)
	} else {
		seen := map[string]bool{}
		for _, id := range req.Artifacts {
			if seen[id] {
				continue
			}
			seen[id] = true
			a, gerr := b.GetArtifact(ctx, id)
			if gerr != nil {
				return sc, gerr
			}
			sc.arts = append(sc.arts, a)
		}
	}
	sort.SliceStable(sc.arts, func(i, j int) bool {
		if !sc.arts[i].UpdatedAt.Equal(sc.arts[j].UpdatedAt) {
			return sc.arts[i].UpdatedAt.Before(sc.arts[j].UpdatedAt)
		}
		return sc.arts[i].ID < sc.arts[j].ID
	})
	if len(sc.arts) == 0 {
		return sc, recoveryErr(RecoveryNoCopy, "there is no backup in %s to recover", firstNonEmpty(req.Set, "this request"))
	}
	if RecoveryTargetWritesToSourceHost(t) {
		sets := distinctSets(sc.arts)
		if len(sets) != 1 {
			return sc, recoveryErr(RecoveryTargetInvalid,
				"a source-host recovery sends backups back to the one host they came from, so it takes the backups of one set at a time, and these come from %d sets", len(sets))
		}
		ex, r, err := b.validateSourceTarget(sc.arts[0].BackupSetID, t, "", false)
		if err != nil {
			return sc, err
		}
		t.Path = r.d.Folder
		sc.target, sc.exTarget, sc.folder, sc.source = publicTarget(t), ex, r.d.Folder, r
		return sc, nil
	}
	sc.folder = sc.target.Path
	if sc.target.Kind == "storage" {
		sc.folder = sc.target.Key
	}
	if sets := distinctSets(sc.arts); len(sets) == 1 {
		sc.folder = manyFolderFor(sc.folder, sc.arts[0].SourceName, sc.arts[0].SetName)
	}
	return sc, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func distinctSets(arts []Artifact) map[string]bool {
	out := map[string]bool{}
	for _, a := range arts {
		out[a.BackupSetID] = true
	}
	return out
}

// narrowSet applies since, until and newest-only to a whole set's rows. Rows
// that are not restore points are kept (they are listed in the summary, never
// silently dropped) unless since or until rules them out by age, or
// newest-only has already chosen a restore point newer than they are.
func narrowSet(all []Artifact, req RecoverManyRequest) []Artifact {
	var rows []Artifact
	for _, a := range all {
		if !req.Since.IsZero() && a.UpdatedAt.Before(req.Since) {
			continue
		}
		if !req.Until.IsZero() && a.UpdatedAt.After(req.Until) {
			continue
		}
		rows = append(rows, a)
	}
	if !req.NewestOnly {
		return rows
	}
	var newest *Artifact
	for i := range rows {
		a := &rows[i]
		if !restorePointStates[a.State] {
			continue
		}
		if newest == nil || a.UpdatedAt.After(newest.UpdatedAt) || (a.UpdatedAt.Equal(newest.UpdatedAt) && a.ID > newest.ID) {
			newest = a
		}
	}
	var out []Artifact
	for _, a := range rows {
		switch {
		case restorePointStates[a.State]:
			if newest != nil && a.ID == newest.ID {
				out = append(out, a)
			}
		case newest == nil || a.UpdatedAt.After(newest.UpdatedAt):
			out = append(out, a)
		}
	}
	return out
}

// planManyItem classifies one backup: a restore point whose copy can be read
// is ready, one that needs a thaw says so, and everything else is listed with
// the reason it is not included.
func (b *BackupService) planManyItem(ctx context.Context, a Artifact, copySel string) (RecoverManyItem, error) {
	it := RecoverManyItem{ArtifactID: a.ID, Name: a.Name}
	switch {
	case a.QuarantineIrrecoverable:
		it.Status, it.Code = RecoverManyUnrecoverable, "ARTIFACT_IRRECOVERABLE"
		it.Message = "this backup was lost: its only copy is gone and nothing remains to re-ingest it from"
		return it, nil
	case a.Quarantined:
		it.Status, it.Code = RecoverManyQuarantined, "ARTIFACT_QUARANTINED"
		it.Message = firstNonEmpty(a.QuarantineReason, "this backup is quarantined, so it is not a restore point")
		return it, nil
	case !restorePointStates[a.State]:
		it.Status, it.Code = RecoverManySkipped, "NOT_A_RESTORE_POINT"
		it.Message = "this backup is " + a.State + ", which is not a restore point"
		return it, nil
	}

	plan, err := b.planRecovery(ctx, a.ID, copySel, false)
	if re, ok := AsRecoveryError(err); ok && re.Code == RecoveryCostNotAcknowledged {
		it.Chargeable = true
		plan, err = b.planRecovery(ctx, a.ID, copySel, true)
	}
	if err == nil {
		err = checkPlanDigest(plan)
	}
	if err != nil {
		if errors.Is(err, ErrArtifactNotFound) {
			return it, err
		}
		re, ok := AsRecoveryError(mapRecoveryError(err))
		if !ok {
			return it, err
		}
		it.Status, it.Code, it.Message = RecoverManyUnrecoverable, re.Code, re.Message
		return it, nil
	}
	it.Copy = plan.resolved.Copy.Medium
	it.SizeBytes = plan.expect.Size
	if plan.resolved.NeedsThaw {
		it.Status, it.Chargeable = RecoverManyNeedsThaw, true
		return it, nil
	}
	it.Status = RecoverManyReady
	return it, nil
}

func totalsOf(items []RecoverManyItem) RecoverManyTotals {
	t := RecoverManyTotals{Count: len(items)}
	for _, it := range items {
		switch it.Status {
		case RecoverManyReady:
			t.Ready++
			if it.SizeBytes != nil {
				t.Bytes += *it.SizeBytes
			}
		case RecoverManyRecovered:
			t.Recovered++
			t.Bytes += it.Bytes
		case RecoverManyFailed:
			t.Failed++
		case RecoverManySkipped:
			t.Skipped++
		case RecoverManyNeedsThaw:
			t.NeedsThaw++
		case RecoverManyQuarantined:
			t.Quarantined++
		case RecoverManyUnrecoverable:
			t.Unrecoverable++
		}
	}
	return t
}

// PlanRecoverMany is the pre-flight alone: it reads the journal and the
// destination, starts nothing and writes nothing. It returns the plan even
// when it refuses (the cost was not acknowledged, the files do not fit), so a
// caller can show the numbers the refusal is about.
func (b *BackupService) PlanRecoverMany(ctx context.Context, req RecoverManyRequest) (RecoverManyPlan, error) {
	h, err := b.recoveryReady()
	if err != nil {
		return RecoverManyPlan{}, err
	}
	sc, err := b.scopeRecoverMany(ctx, h, req)
	if err != nil {
		return RecoverManyPlan{}, err
	}
	plan := RecoverManyPlan{Set: sc.set, Target: sc.target, Folder: sc.folder}
	for _, a := range sc.arts {
		it, ierr := b.planManyItem(ctx, a, req.Copy)
		if ierr != nil {
			return RecoverManyPlan{}, ierr
		}
		plan.Items = append(plan.Items, it)
	}
	plan.Totals = totalsOf(plan.Items)
	var largest int64
	for _, it := range plan.Items {
		if it.Chargeable && (it.Status == RecoverManyReady || it.Status == RecoverManyNeedsThaw) {
			plan.ChargeableCount++
			if it.SizeBytes != nil {
				plan.ChargeableBytes += *it.SizeBytes
			}
		}
		if it.Status == RecoverManyReady {
			if it.SizeBytes == nil {
				plan.UnknownSizes++
			} else if *it.SizeBytes > largest {
				largest = *it.SizeBytes
			}
		}
	}
	plan.ReserveBytes = h.staging.Reserve
	free, ferr := h.staging.Available()
	if ferr == nil {
		plan.FreeBytes = &free
	}
	var sourceSpace error
	if sc.exTarget.Kind == export.TargetSource {
		plan.DestinationID, plan.URL = sc.exTarget.Destination, sc.source.d.URL()
		plan.SourceReserveBytes = h.staging.Reserve
		// Every file stays on the host, so the whole run has to fit there
		// (#1193), not only the largest file as on the staging disk.
		if plan.Totals.Ready > 0 {
			plan.SourceFreeBytes, sourceSpace = b.checkSourceSpace(ctx, sc.source, plan.Totals.Bytes, plan.SourceReserveBytes)
		}
	}

	if plan.ChargeableCount > 0 && !req.AcknowledgeCost {
		return plan, recoveryErr(RecoveryCostNotAcknowledged,
			"reading %d of these %d files is billed (%d bytes in all, and any archived copy needs a provider restore first); pass the acknowledgement once for the whole set, nothing was started",
			plan.ChargeableCount, plan.Totals.Count, plan.ChargeableBytes)
	}

	// What must fit on the staging disk: for a staging target every finished
	// file stays there, so the total; for a storage target each file is
	// staged, uploaded and removed in turn, so the largest one.
	need := plan.Totals.Bytes
	what := "the files need"
	if sc.target.Kind == "storage" || sc.target.Kind == "source" {
		need, what = largest, "the largest file needs"
	}
	if plan.FreeBytes != nil && plan.Totals.Ready > 0 {
		switch {
		case need > *plan.FreeBytes:
			return plan, recoveryErr(RecoveryNoSpace,
				"%s %d bytes (%d files) and the staging disk has %d bytes free, so nothing was read", what, need, plan.Totals.Ready, *plan.FreeBytes)
		case *plan.FreeBytes-need < plan.ReserveBytes:
			return plan, recoveryErr(RecoveryBelowReserve,
				"%s %d bytes (%d files) and the staging disk has %d bytes free, which would leave less than its %d byte reserve, so nothing was read", what, need, plan.Totals.Ready, *plan.FreeBytes, plan.ReserveBytes)
		}
	}
	if sourceSpace != nil {
		return plan, sourceSpace
	}
	return plan, nil
}

// manifest is the file written beside the recovered files. It lists every
// row of the request, recovered or not, so it can be checked against the
// disk later and agrees with the summary by construction (both are built from
// the same items).
type manyManifest struct {
	Version     int                `json:"version"`
	OperationID string             `json:"operation_id"`
	CreatedAt   string             `json:"created_at"`
	Set         string             `json:"set,omitempty"`
	Outcome     string             `json:"outcome"`
	Target      manyManifestTarget `json:"target"`
	Totals      map[string]any     `json:"totals"`
	Files       []manyManifestFile `json:"files"`
}

type manyManifestTarget struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Medium string `json:"medium,omitempty"`
	Key    string `json:"key,omitempty"`
}

type manyManifestFile struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Copy       string `json:"copy,omitempty"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256,omitempty"`
	File       string `json:"file,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

func buildManifest(res RecoverManyResult, outcome string, at time.Time) manyManifest {
	m := manyManifest{
		Version: manifestVersion, OperationID: res.OperationID, CreatedAt: at.UTC().Format(time.RFC3339),
		Set: res.Set, Outcome: outcome,
		Target: manyManifestTarget{Kind: res.Target.Kind, Path: res.Target.Path, Medium: res.Target.Medium, Key: res.Target.Key},
		Totals: map[string]any{
			"count": res.Totals.Count, "recovered": res.Totals.Recovered, "failed": res.Totals.Failed, "skipped": res.Totals.Skipped,
			"needs_thaw": res.Totals.NeedsThaw, "quarantined": res.Totals.Quarantined, "unrecoverable": res.Totals.Unrecoverable,
			"bytes": res.Totals.Bytes,
		},
	}
	for _, it := range res.Items {
		file := ""
		if it.Status == RecoverManyRecovered {
			file = it.Rel
		}
		m.Files = append(m.Files, manyManifestFile{
			ArtifactID: it.ArtifactID, Name: it.Name, Status: it.Status, Copy: it.Copy, Bytes: it.Bytes, SHA256: it.SHA256,
			File: file, Code: it.Code, Message: it.Message,
		})
	}
	return m
}

// RecoverMany plans, refuses early when it must, and then recovers every
// ready file to the one destination, calling progress (which may be nil) as
// it goes. One file failing never stops the others; cancelling ctx stops the
// run after discarding the file in flight, and the manifest still says what
// happened. A cancelled run returns its result together with a
// RECOVERY_INTERRUPTED error.
func (b *BackupService) RecoverMany(ctx context.Context, sess RecoverySession, req RecoverManyRequest, progress func(RecoverManyProgress)) (res RecoverManyResult, err error) {
	ev := recoveryEvent{op: "many", actor: sess.Actor, artifact: firstNonEmpty(req.Set, fmt.Sprintf("%d backups", len(req.Artifacts))), target: req.Target.label()}
	defer func() { b.auditRecovery(ctx, &ev, err) }()
	emit := func(p RecoverManyProgress) {
		if progress != nil {
			progress(p)
		}
	}

	h, err := b.recoveryReady()
	if err != nil {
		return RecoverManyResult{}, err
	}
	b.sweepRecoveryIfDue(ctx)
	plan, err := b.PlanRecoverMany(ctx, req)
	if err != nil {
		return RecoverManyResult{}, err
	}
	var up *sourceUpload
	if RecoveryTargetWritesToSourceHost(plan.Target) {
		// The plan wrote nothing; the run writes onto a production host, so
		// it needs the acknowledgement of exactly this destination, then
		// the write check (one probe, or a fresh cached verdict), then one
		// session for the whole run that claims the new folder, all before
		// any file is read.
		setID, _ := splitArtifactID(plan.Items[0].ArtifactID)
		tg := req.Target
		tg.Path = plan.Folder
		_, r, verr := b.validateSourceTarget(setID.String(), tg, "", true)
		if verr != nil {
			return RecoverManyResult{}, verr
		}
		ev.target = "source:" + r.d.URL() + " (" + plan.DestinationID + ")"
		if werr := b.ensureSourceWritable(ctx, sess.Actor, r); werr != nil {
			return RecoverManyResult{}, werr
		}
		up, err = b.beginSourceUpload(ctx, setID.String(), plan.Folder, plan.DestinationID)
		if err != nil {
			return RecoverManyResult{}, err
		}
		defer up.close()
	}
	ex := b.manyExportTarget(h, plan)

	res = RecoverManyResult{OperationID: uuid.NewString(), Set: plan.Set, Target: plan.Target, Folder: plan.Folder, Items: append([]RecoverManyItem(nil), plan.Items...),
		DestinationID: plan.DestinationID, URL: plan.URL}
	emit(RecoverManyProgress{Kind: "plan", Plan: &plan, Total: len(res.Items), TotalBytes: plan.Totals.Bytes})

	var done, index int
	actionable := plan.Totals.Ready + plan.Totals.NeedsThaw
	interrupted := false
	for i := range res.Items {
		it := &res.Items[i]
		if it.Status != RecoverManyReady && it.Status != RecoverManyNeedsThaw {
			continue
		}
		index++
		if interrupted || ctx.Err() != nil {
			interrupted = true
			it.Status, it.Code, it.Message = RecoverManySkipped, RecoveryInterrupted, "the run was cancelled before this file was started"
			continue
		}
		emit(RecoverManyProgress{Kind: "start", Index: index, Total: actionable, Item: *it, DoneBytes: int64(done), TotalBytes: plan.Totals.Bytes})
		if it.Status == RecoverManyNeedsThaw {
			b.thawManyItem(ctx, h, sess, it)
		} else {
			b.recoverManyItem(ctx, h, ex, up, it)
			if it.Status == RecoverManyRecovered {
				done += int(it.Bytes)
			}
			if ctx.Err() != nil && it.Status != RecoverManyRecovered {
				interrupted = true
				it.Status, it.Code, it.Message = RecoverManySkipped, RecoveryInterrupted, "the run was cancelled while this file was being read; nothing of it was kept"
			}
		}
		emit(RecoverManyProgress{Kind: "item", Index: index, Total: actionable, Item: *it, DoneBytes: int64(done), TotalBytes: plan.Totals.Bytes})
	}
	res.Totals = totalsOf(res.Items)
	res.Cancelled = interrupted

	outcome := "completed"
	switch {
	case interrupted:
		outcome = "cancelled"
	case res.Totals.Failed > 0:
		outcome = "partial"
	}
	if merr := b.writeManyManifest(h, ex, up, &res, outcome); merr != nil {
		b.logger.Error(ctx, "recovery_manifest", merr)
		ev.bytes = int64(done)
		err = recoveryErr(RecoveryFailed, "the files were recovered but their manifest could not be written; the engine log has the cause")
		return res, err
	}
	ev.bytes = int64(done)
	ev.note = fmt.Sprintf("recovered %d, failed %d, skipped %d, needs thaw %d, quarantined %d, unrecoverable %d of %d",
		res.Totals.Recovered, res.Totals.Failed, res.Totals.Skipped, res.Totals.NeedsThaw, res.Totals.Quarantined, res.Totals.Unrecoverable, res.Totals.Count)
	if interrupted {
		err = recoveryErr(RecoveryInterrupted, "the recovery was cancelled after %d of %d files; the files already recovered are complete and listed in the manifest, and nothing half-written was kept", res.Totals.Recovered, actionable)
	}
	return res, err
}

// manyExportTarget is the destination every file of a run shares.
func (b *BackupService) manyExportTarget(h *recoveryHost, plan RecoverManyPlan) export.Target {
	t := plan.Target
	switch t.Kind {
	case "storage":
		return export.Target{Kind: export.TargetStorage, Medium: t.Medium, Key: t.Key}
	case "source":
		return export.Target{Kind: export.TargetSource, Path: t.Path, Destination: plan.DestinationID}
	}
	return export.Target{Kind: export.TargetStaging, Path: t.Path}
}

func (b *BackupService) thawManyItem(ctx context.Context, h *recoveryHost, sess RecoverySession, it *RecoverManyItem) {
	thaw, err := h.thaw(ctx, sess.Actor, it.ArtifactID, it.Copy)
	if err != nil {
		code, msg := RecoveryNotReadable, "the provider's restore of that copy could not be started right now"
		if !errors.Is(err, ErrRestoreRefused) && !errors.Is(err, ErrRestoreUnavailable) {
			if re, ok := AsRecoveryError(mapRecoveryError(err)); ok {
				code, msg = re.Code, re.Message
			}
		}
		it.Status, it.Code, it.Message = RecoverManyFailed, code, msg
		return
	}
	it.Thaw = &thaw
	it.Message = "a provider restore was started; run this again once the copy is readable"
}

// recoverManyItem recovers one file through exactly the code a single
// recovery uses: the plan is re-derived from the journal as it is now, the
// copy is read through the bounded buffer and compared with the digest
// recorded at ingestion, and only a file that matched is handed over.
func (b *BackupService) recoverManyItem(ctx context.Context, h *recoveryHost, ex export.Target, up *sourceUpload, it *RecoverManyItem) {
	trace := b.newRecoveryTrace(ctx, it.ArtifactID)
	fail := func(err error) {
		trace.fail(ctx, err)
		re, ok := AsRecoveryError(mapRecoveryError(err))
		code, msg := RecoveryFailed, "the recovery failed; the engine log has the cause"
		if ok {
			code, msg = re.Code, re.Message
			if code == RecoveryFailed {
				b.logger.Error(ctx, "recovery_failure", err)
			}
		}
		if errors.Is(err, ErrArtifactNotFound) {
			code, msg = recoveryArtifactNotFoundCode, "this backup is no longer in the journal"
		}
		it.Status, it.Code, it.Message = RecoverManyFailed, code, msg
	}
	staged, err := b.stageGrant(ctx, export.Grant{Artifact: it.ArtifactID, Copy: it.Copy}, trace)
	if err == nil && h.afterStage != nil {
		h.afterStage()
	}
	if err == nil && ctx.Err() != nil {
		b.discardStaged(staged)
		err = ctx.Err()
	}
	if err != nil {
		fail(err)
		return
	}
	id, _ := splitArtifactID(it.ArtifactID)
	switch ex.Kind {
	case export.TargetStaging:
		trace.stage(ctx, "writing_target")
		final, perr := staged.PublishTo(path.Join(manyFolderFor(ex.Path, id.Source, id.Set), it.Name))
		if perr != nil {
			b.discardStaged(staged)
			fail(perr)
			return
		}
		it.Path = final
		if rel, rerr := filepath.Rel(h.staging.Root, final); rerr == nil {
			it.Rel = filepath.ToSlash(rel)
		}
	case export.TargetStorage:
		key := path.Join(ex.Key, id.Source, id.Set, it.Name)
		trace.stage(ctx, "writing_target")
		uerr := b.uploadRecovered(ctx, staged, export.Target{Kind: export.TargetStorage, Medium: ex.Medium, Key: key})
		b.discardStaged(staged)
		if uerr != nil {
			fail(uerr)
			return
		}
		it.Key, it.Rel = key, key
	case export.TargetSource:
		// Through the run's one session, after the destination has been
		// worked out again from the configuration as it is now (#1193).
		trace.stage(ctx, "writing_target")
		full, verified, uerr := up.put(ctx, staged, it.Name)
		b.discardStaged(staged)
		if uerr != nil {
			fail(uerr)
			return
		}
		it.Path, it.Rel, it.Verified = full, path.Join(ex.Path, it.Name), verified
	}
	it.Status, it.Bytes, it.SHA256 = RecoverManyRecovered, staged.Size, staged.Digest
	trace.stage(ctx, "cleaning_temporary_files")
	trace.complete(ctx, staged.Size)
}

// splitArtifactID splits "source/set/name" into its set and name.
func splitArtifactID(id string) (model.BackupSetID, string) {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) != 3 {
		return model.BackupSetID{}, id
	}
	return model.BackupSetID{Source: parts[0], Set: parts[1]}, parts[2]
}

// writeManyManifest writes the manifest beside the files, even for a run that
// was cancelled (so what happened is on record), and records where it went.
func (b *BackupService) writeManyManifest(h *recoveryHost, ex export.Target, up *sourceUpload, res *RecoverManyResult, outcome string) error {
	raw, err := json.MarshalIndent(buildManifest(*res, outcome, now()), "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	staged, err := h.staging.WriteSmall(raw)
	if err != nil {
		return err
	}
	res.ManifestSHA256 = staged.Digest
	wctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch ex.Kind {
	case export.TargetStaging:
		final, perr := staged.PublishTo(path.Join(res.Folder, manifestFileName))
		if perr != nil {
			b.discardStaged(staged)
			return perr
		}
		res.ManifestPath = final
	case export.TargetStorage:
		key := path.Join(res.Folder, manifestFileName)
		uerr := b.uploadRecovered(wctx, staged, export.Target{Kind: export.TargetStorage, Medium: ex.Medium, Key: key})
		b.discardStaged(staged)
		if uerr != nil {
			return uerr
		}
		res.ManifestKey = key
	case export.TargetSource:
		// Named for the run, so it can never collide with another run's in a
		// folder somebody reused, and written through the run's session.
		full, _, uerr := up.put(wctx, staged, "manifest-"+res.OperationID+".json")
		b.discardStaged(staged)
		if uerr != nil {
			return uerr
		}
		res.ManifestPath = full
	}
	return nil
}
