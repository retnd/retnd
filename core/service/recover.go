package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/archive"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/recovery/export"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// Recovering an artifact's bytes (EPIC Q, FR-67 and FR-68).
//
// # Why this is not an operation
//
// POST /api/v1/operations is behind the destructive gate, and the only gate
// this repository ships never passes, so a recovery submitted there would
// be refused on every deployment it can build. Recovery reads. It deletes
// nothing, overwrites nothing and opens no way to either, so it is its own
// pair of routes: a mint that carries CSRF and a redeem that streams, in
// the tier handlers_artifacts.go already uses for reads that have a side
// effect on the process and none on the data.
//
// # Who may read, because there is no role
//
// capabilities.AuthContext carries an actor and a session lapse and no
// authority, so every authenticated caller can recover every artifact,
// exactly as every authenticated caller can hold any backup set
// (edithold.go says the same of its own route). That is stated here rather
// than hidden, and it is paid for with audit: a grant is short, single-use,
// bound to the minting session and dead when that session ends, and every
// mint and every served range writes one action-log row naming the actor,
// the artifact, the copy, the target and how it went.
//
// # Where the bytes go
//
// A server-side target (a name under the staging root, or another storage
// destination) goes through the staging area first, so nothing is placed
// until the whole copy has been read and has matched the SHA-256 the journal
// recorded. A browser or terminal download is different: it is streamed from
// the retained copy, one verified chunk at a time, after a pre-flight and a
// preparation read that prove the copy matches the journal (recover_stream.go
// says what that does and does not prove). It is not staged, so it cannot
// fill the staging disk and has nothing to clean up if the client leaves.

// Recovery staging defaults. They are constants here and not configuration
// because the settings surface for them is its own work (the staging root,
// the TTL and the ceilings are global-only knobs); these are the values it
// will default to.
const (
	recoveryPartialTTL = time.Hour
	recoveryOutputTTL  = 7 * 24 * time.Hour
	recoveryReserve    = 1 << 30 // keep 1 GiB free on the staging filesystem
	recoveryGrantTTL   = export.DefaultGrantTTL
	recoveryMaxGrants  = 32
)

// RecoveryTarget is where one recovery writes. The set is closed: Kind is
// "download", "staging" or "storage".
type RecoveryTarget struct {
	Kind string

	// Path is the name under the recovery staging root, for "staging".
	Path string

	// Medium and Key address another configured storage destination, for
	// "storage". Key is optional; the default is recovered/<artifact id>.
	Medium string
	Key    string
}

// RecoverySession is who is asking and how long that identity lasts. A zero
// Expires means the platform reports no lapse.
type RecoverySession struct {
	Actor   string
	Expires time.Time
}

func (s RecoverySession) toExport() export.Session {
	return export.Session{Actor: s.Actor, Expires: s.Expires}
}

// RecoveryRequest is one operator's ask.
type RecoveryRequest struct {
	ArtifactID string

	// Copy is a medium id, or "" / "best-available".
	Copy string

	Target RecoveryTarget

	// AcknowledgeCost is the explicit yes to a chargeable retrieval.
	AcknowledgeCost bool
}

// RecoveryThaw is the result of an acknowledged request whose only copy is
// archive-class: one provider restore was started and no grant exists yet.
type RecoveryThaw struct {
	OperationID string
	Wait        string
	Billing     string
}

// RecoveryGrant is what a mint returns. Token is shown once and is the only
// thing that redeems it.
type RecoveryGrant struct {
	Token      string
	ExpiresAt  time.Time
	ArtifactID string
	Copy       string
	Target     RecoveryTarget
	SizeBytes  *int64

	// Thaw is set instead of Token when the mint had to start a provider
	// restore first.
	Thaw *RecoveryThaw
}

// RecoveryResult is what a server-side target reports.
type RecoveryResult struct {
	ArtifactID string
	Copy       string
	Target     RecoveryTarget

	// Path is the full path of the output under the staging root, for the
	// "staging" target.
	Path string

	// Key is where the bytes landed, for the "storage" target.
	Key string

	Bytes  int64
	SHA256 string
}

// recoveryHost is the per-process machinery: the grant table, the staging
// area and the engine. Built on first use so a service that never recovers
// anything creates no directory.
type recoveryHost struct {
	once    sync.Once
	grants  *export.Grants
	staging *export.Staging
	engine  *export.Engine

	// indexes is the cache of chunk indexes a streamed download is checked
	// against, and the single-flight in front of making one; verified is
	// what the server remembers of finished downloads.
	indexes  downloadIndexes
	verified verifiedLog

	// unavailable is set when there is nowhere to stage; every recovery
	// entry point then refuses with RecoveryUnavailable.
	unavailable bool

	// now is the clock the grants use; nil means time.Now. A field so a
	// test can expire a grant without sleeping.
	now func() time.Time

	sweepMu   sync.Mutex
	lastSweep time.Time

	// thaw starts a provider restore. A field so a test can count calls.
	thaw func(ctx context.Context, actor, artifactID, medium string) (RecoveryThaw, error)
}

func (b *BackupService) recoveryHost() *recoveryHost {
	h := &b.recovery
	h.once.Do(func() {
		cfg := b.state.Load().inner.Config
		if cfg.State.Database == "" {
			// No state directory means no place to stage. Refusing is
			// better than staging under the working directory.
			h.unavailable = true
			return
		}
		root := filepath.Join(filepath.Dir(cfg.State.Database), "recovery")
		h.staging = &export.Staging{
			Root:       root,
			PartialTTL: recoveryPartialTTL,
			OutputTTL:  recoveryOutputTTL,
			Reserve:    recoveryReserve,
		}
		h.engine = &export.Engine{Staging: h.staging}
		h.indexes.store = &export.IndexStore{Dir: filepath.Join(filepath.Dir(cfg.State.Database), "recovery-index")}
		h.grants = export.NewGrants(export.GrantOptions{TTL: recoveryGrantTTL, MaxOutstanding: recoveryMaxGrants, Now: h.now})
		if h.thaw == nil {
			h.thaw = b.startRecoveryThaw
		}
	})
	return h
}

// recoveryReady is recoveryHost for the entry points: it refuses, with a
// coded error, when there is nowhere to stage.
func (b *BackupService) recoveryReady() (*recoveryHost, error) {
	h := b.recoveryHost()
	if h.unavailable {
		return nil, recoveryErr(RecoveryUnavailable, "this deployment has no state directory to stage a recovery in")
	}
	return h, nil
}

// RecoveryError is a recovery refusal or failure with a stable code. The
// HTTP route and the CLI both switch on Code, so the two cannot describe
// one refusal differently.
type RecoveryError struct {
	Code    string
	Message string
	Cause   error
}

func (e *RecoveryError) Error() string { return e.Message }
func (e *RecoveryError) Unwrap() error { return e.Cause }

// The closed set of recovery codes.
const (
	RecoveryCostNotAcknowledged  = "RECOVERY_COST_NOT_ACKNOWLEDGED"
	RecoveryNoCopy               = "RECOVERY_NO_COPY"
	RecoveryCopyNotFound         = "RECOVERY_COPY_NOT_FOUND"
	RecoveryNotReadable          = "RECOVERY_NOT_READABLE"
	RecoveryLocalCopyCorrupt     = "RECOVERY_LOCAL_COPY_CORRUPT"
	RecoveryRemoteCopyCorrupt    = "RECOVERY_REMOTE_COPY_CORRUPT"
	RecoveryNoDigest             = "RECOVERY_NO_DIGEST"
	RecoveryNoSpace              = "RECOVERY_NO_SPACE"
	RecoveryBelowReserve         = "RECOVERY_BELOW_RESERVE"
	RecoveryGrantUnknown         = "RECOVERY_GRANT_UNKNOWN"
	RecoveryGrantUsed            = "RECOVERY_GRANT_USED"
	RecoveryGrantExpired         = "RECOVERY_GRANT_EXPIRED"
	RecoveryGrantSessionEnded    = "RECOVERY_GRANT_SESSION_ENDED"
	RecoveryTooManyGrants        = "RECOVERY_TOO_MANY_GRANTS"
	RecoveryTargetInvalid        = "RECOVERY_TARGET_INVALID"
	RecoveryTargetExists         = "RECOVERY_TARGET_EXISTS"
	RecoveryUseSnapshotRestore   = "RECOVERY_USE_SNAPSHOT_RESTORE"
	RecoveryUnavailable          = "RECOVERY_UNAVAILABLE"
	RecoveryBusy                 = "RECOVERY_BUSY"
	RecoveryInterrupted          = "RECOVERY_INTERRUPTED"
	RecoveryFailed               = "RECOVERY_FAILED"
	recoveryArtifactNotFoundCode = "ARTIFACT_NOT_FOUND"
)

// RecoveryCodes lists every code a recovery can refuse with, for the
// contract and for the tests that hold both surfaces to it.
func RecoveryCodes() []string {
	return []string{
		RecoveryCostNotAcknowledged, RecoveryNoCopy, RecoveryCopyNotFound, RecoveryNotReadable,
		RecoveryLocalCopyCorrupt, RecoveryRemoteCopyCorrupt, RecoveryNoDigest, RecoveryNoSpace,
		RecoveryBelowReserve, RecoveryGrantUnknown, RecoveryGrantUsed, RecoveryGrantExpired,
		RecoveryGrantSessionEnded, RecoveryTooManyGrants, RecoveryTargetInvalid, RecoveryTargetExists,
		RecoveryUseSnapshotRestore, RecoveryUnavailable, RecoveryBusy, RecoveryInterrupted, RecoveryFailed,
	}
}

// AsRecoveryError returns the recovery refusal inside err, if any.
func AsRecoveryError(err error) (*RecoveryError, bool) {
	var re *RecoveryError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}

func recoveryErr(code, format string, args ...any) *RecoveryError {
	return &RecoveryError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// mapRecoveryError turns the domain's errors into coded ones. The wording
// is chosen here, once, so the browser and the terminal read the same
// sentence.
func mapRecoveryError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := AsRecoveryError(err); ok {
		return err
	}
	var ie *export.IntegrityError
	var ce *export.CleanupError
	switch {
	case errors.As(err, &ie):
		code := RecoveryRemoteCopyCorrupt
		where := "the copy on " + ie.Medium
		if ie.Origin == export.OriginLocal {
			code = RecoveryLocalCopyCorrupt
			where = "the copy on this host's disk"
		}
		re := recoveryErr(code, "%s does not match the checksum recorded when this backup was ingested (%s mismatch), so none of it was delivered; the other copies, if any, are not affected by this", where, ie.Kind)
		re.Cause = err
		if errors.As(err, &ce) {
			re.Message += "; the staging cleanup also failed, which needs attention separately: " + ce.Error()
		}
		return re
	case errors.Is(err, export.ErrNoDigest):
		return &RecoveryError{Code: RecoveryNoDigest, Message: export.ErrNoDigest.Error(), Cause: err}
	case errors.Is(err, export.ErrNoSpace):
		return &RecoveryError{Code: RecoveryNoSpace, Message: "the staging disk filled while the copy was being staged; nothing was delivered and the partial file was removed", Cause: err}
	case errors.Is(err, export.ErrBelowReserve):
		return &RecoveryError{Code: RecoveryBelowReserve, Message: "there is not enough free space on the staging disk to stage this copy and keep its reserve; nothing was read", Cause: err}
	case errors.Is(err, export.ErrCostNotAcknowledged):
		return &RecoveryError{Code: RecoveryCostNotAcknowledged, Message: err.Error() + "; nothing was started", Cause: err}
	case errors.Is(err, export.ErrNoCopy):
		return &RecoveryError{Code: RecoveryNoCopy, Message: "this backup has no retained copy to recover", Cause: err}
	case errors.Is(err, export.ErrCopyNotFound):
		return &RecoveryError{Code: RecoveryCopyNotFound, Message: "this backup has no such copy; `" + recoverVerb + " artifacts <id>` lists its copies", Cause: err}
	case errors.Is(err, export.ErrNotReadable):
		return &RecoveryError{Code: RecoveryNotReadable, Message: "no copy of this backup can be read right now: each is mid-restore or on a destination this configuration cannot reach", Cause: err}
	case errors.Is(err, export.ErrGrantUnknown):
		return &RecoveryError{Code: RecoveryGrantUnknown, Message: "no such recovery grant", Cause: err}
	case errors.Is(err, export.ErrGrantUsed):
		return &RecoveryError{Code: RecoveryGrantUsed, Message: "this recovery grant has already been used; ask for a new one", Cause: err}
	case errors.Is(err, export.ErrGrantExpired):
		return &RecoveryError{Code: RecoveryGrantExpired, Message: "this recovery grant has expired; ask for a new one", Cause: err}
	case errors.Is(err, export.ErrGrantSessionEnded):
		return &RecoveryError{Code: RecoveryGrantSessionEnded, Message: "the session that asked for this recovery has ended, and its grant with it; sign in and ask again", Cause: err}
	case errors.Is(err, export.ErrTooManyGrants):
		return &RecoveryError{Code: RecoveryTooManyGrants, Message: "too many recoveries are waiting to be collected; use or let some expire first", Cause: err}
	case errors.Is(err, export.ErrTargetTooLong):
		return &RecoveryError{Code: RecoveryTargetInvalid, Message: "that target name is too long", Cause: err}
	case errors.Is(err, export.ErrTargetOutsideRoot):
		return &RecoveryError{Code: RecoveryTargetInvalid, Message: "that target name does not stay inside the recovery staging root", Cause: err}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &RecoveryError{Code: RecoveryInterrupted, Message: "the recovery was interrupted before it finished; nothing was delivered and its staging file was removed", Cause: err}
	}
	// The cause stays on the error for the log. It is never in the message:
	// it can carry a path, or the body of a provider's error, and neither
	// belongs on a response, in --json output or on the activity feed.
	return &RecoveryError{Code: RecoveryFailed, Message: "the recovery failed; the engine log has the cause", Cause: err}
}

// recoverVerb is the binary name used inside sentences.
const recoverVerb = cliecho.Binary

// recoveryPlan is everything a mint decides and a redeem re-derives from the
// journal: the artifact, the copy chosen and how to read it.
type recoveryPlan struct {
	rec      state.Record
	resolved export.Resolution
	medium   transport.Medium
	expect   export.Expect
	set      string
}

// planRecovery resolves a request against the journal and the
// configuration. It reads and decides; it starts nothing.
func (b *BackupService) planRecovery(ctx context.Context, artifactID, copySel string, ack bool) (recoveryPlan, error) {
	id, err := app.ParseArtifactID(artifactID)
	if err != nil {
		return recoveryPlan{}, fmt.Errorf("%w: %s", ErrArtifactNotFound, artifactID)
	}
	st := b.state.Load()

	if set, ok := configuredSet(st.inner.Config.Sources, id.Set); ok && set.Engine == model.EngineKopia {
		return recoveryPlan{}, recoveryErr(RecoveryUseSnapshotRestore,
			"%s is stored by the kopia engine, whose backups are restore points rather than files: use `%s snapshot restore %s` to extract one",
			id.Set, recoverVerb, id.Set)
	}

	rec, err := b.journal.Get(ctx, id)
	if err != nil {
		if errors.Is(err, state.ErrArtifactNotFound) {
			return recoveryPlan{}, fmt.Errorf("%w: %s", ErrArtifactNotFound, artifactID)
		}
		return recoveryPlan{}, fmt.Errorf("service: loading artifact %s: %w", artifactID, err)
	}

	copies := b.recoveryCopies(ctx, st, rec)
	res, err := export.Resolve(copies, export.ResolveRequest{Copy: copySel, AcknowledgeCost: ack})
	if err != nil {
		return recoveryPlan{}, mapRecoveryError(err)
	}

	plan := recoveryPlan{rec: rec, resolved: res}
	plan.expect = expectFor(rec, res.Copy)
	if !res.Copy.Local {
		m, _, err := app.MediumFor(st.inner.Config, res.Copy.Medium)
		if err != nil {
			return recoveryPlan{}, recoveryErr(RecoveryNotReadable, "the copy on %s is on a destination this configuration no longer declares", res.Copy.Medium)
		}
		plan.medium = m
	}
	return plan, nil
}

func configuredSet(sources []config.Source, id model.BackupSetID) (config.BackupSet, bool) {
	for _, src := range sources {
		for _, bs := range src.BackupSets {
			if bs.ID == id {
				return bs, true
			}
		}
	}
	return config.BackupSet{}, false
}

// recoveryCopies turns a record's placements into the resolver's view.
// The work list is the journal's placements, never a listing of any
// destination.
func (b *BackupService) recoveryCopies(ctx context.Context, st *configState, rec state.Record) []export.Copy {
	var out []export.Copy
	nowT := now()
	for _, p := range rec.Placements {
		c := export.Copy{
			Medium:            p.Medium,
			Local:             p.IsLocal(),
			Active:            p.Status == state.PlacementActive,
			Size:              p.Size,
			Hash:              p.Hash,
			HashAlg:           p.HashAlg,
			VerificationClass: p.VerificationClass,
			VerifiedAt:        p.VerifiedAt,
		}
		if c.Local {
			c.Access = archive.Immediate
		} else {
			m, class, err := app.MediumFor(st.inner.Config, p.Medium)
			switch {
			case err != nil:
				c.Access = archive.Unreachable
			default:
				access, aerr := archive.Access(p.Medium, class, b.observeRestore(ctx, st, m, class, p), nowT)
				if aerr != nil {
					access = archive.Unreachable
				}
				c.Access = access
				c.StorageClass = class
				c.Chargeable = m.Type != transport.MediumTypeLocalDir
			}
		}
		out = append(out, c)
	}
	if len(rec.Placements) == 0 {
		if path, ok := rec.ReadableLocalPath(); ok && path != "" {
			c := export.Copy{Medium: state.MediumLocal, Local: true, Active: true, Access: archive.Immediate}
			out = append(out, c)
		}
	}
	return out
}

func expectFor(rec state.Record, c export.Copy) export.Expect {
	e := export.Expect{
		HashAlg: rec.LocalHashAlg,
		Hash:    rec.LocalHash,
		Medium:  c.Medium,
		Origin:  export.OriginRemote,
	}
	if c.Local {
		e.Origin = export.OriginLocal
	}
	if e.Hash == "" {
		e.HashAlg, e.Hash = c.HashAlg, c.Hash
	}
	switch {
	case c.Size != nil:
		e.Size = c.Size
	case rec.Transfer != nil && rec.Transfer.BytesTransferred > 0:
		n := rec.Transfer.BytesTransferred
		e.Size = &n
	}
	return e
}

// recoverySource opens the chosen copy.
type recoverySource struct {
	local  string
	store  transport.MediumStore
	medium transport.Medium
	key    string
}

func (s recoverySource) Open(ctx context.Context) (io.ReadCloser, error) {
	if s.store == nil {
		// Lstat before Open, and a symlink is refused: opening a fifo
		// blocks until a writer appears, and a link is how a path under a
		// backup root becomes a read of something else. (The SHA-256 check
		// would refuse bytes that are not the artifact's, but not before
		// the read had begun, and not a read that never ends.)
		fi, err := os.Lstat(s.local)
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, recoveryErr(RecoveryNotReadable, "the local copy is not a regular file, so it is not read")
		}
		return os.Open(s.local)
	}
	return s.store.OpenObject(ctx, s.medium, s.key)
}

func (b *BackupService) sourceFor(st *configState, plan recoveryPlan) (export.Source, error) {
	c := plan.resolved.Copy
	if c.Local {
		path, ok := plan.rec.ReadableLocalPath()
		if !ok {
			return nil, recoveryErr(RecoveryNotReadable, "the local copy has no readable path in the journal")
		}
		return recoverySource{local: path}, nil
	}
	store, _ := st.inner.Transport.(transport.MediumStore)
	if store == nil {
		return nil, recoveryErr(RecoveryUnavailable, "this deployment has no way to reach a storage medium")
	}
	var key string
	for _, p := range plan.rec.Placements {
		if p.Medium == c.Medium {
			key = p.Location
		}
	}
	return recoverySource{store: store, medium: plan.medium, key: key}, nil
}

// MintRecovery authorises one recovery and returns the grant that redeems
// it. A refusal here (cost, no readable copy, a kopia set) leaves nothing
// behind: no grant, no provider job, no byte read.
func (b *BackupService) MintRecovery(ctx context.Context, sess RecoverySession, req RecoveryRequest) (grant RecoveryGrant, err error) {
	ev := recoveryEvent{op: "mint", actor: sess.Actor, artifact: req.ArtifactID, copy: req.Copy, target: req.Target.label()}
	defer func() { b.auditRecovery(ctx, &ev, err) }()

	h, err := b.recoveryReady()
	if err != nil {
		return RecoveryGrant{}, err
	}
	target, err := b.validateRecoveryTarget(h, req.ArtifactID, req.Target)
	if err != nil {
		return RecoveryGrant{}, err
	}
	plan, err := b.planRecovery(ctx, req.ArtifactID, req.Copy, req.AcknowledgeCost)
	if err != nil {
		return RecoveryGrant{}, err
	}
	ev.copy = plan.resolved.Copy.Medium

	if plan.resolved.NeedsThaw {
		thaw, terr := h.thaw(ctx, sess.Actor, req.ArtifactID, plan.resolved.Copy.Medium)
		if terr != nil {
			if errors.Is(terr, ErrRestoreRefused) || errors.Is(terr, ErrRestoreUnavailable) {
				return RecoveryGrant{}, &RecoveryError{Code: RecoveryNotReadable, Message: "the provider's restore of that copy could not be started right now, so nothing was recovered", Cause: terr}
			}
			return RecoveryGrant{}, mapRecoveryError(terr)
		}
		ev.note = "provider restore started: " + thaw.OperationID
		return RecoveryGrant{ArtifactID: req.ArtifactID, Copy: plan.resolved.Copy.Medium, Target: req.Target, Thaw: &thaw}, nil
	}
	if derr := checkPlanDigest(plan); derr != nil {
		return RecoveryGrant{}, derr
	}

	token, g, err := h.grants.Mint(export.GrantSpec{
		Session:  sess.toExport(),
		Artifact: req.ArtifactID,
		Copy:     plan.resolved.Copy.Medium,
		Target:   target,
	})
	if err != nil {
		return RecoveryGrant{}, mapRecoveryError(err)
	}
	return RecoveryGrant{
		Token:      token,
		ExpiresAt:  g.ExpiresAt,
		ArtifactID: req.ArtifactID,
		Copy:       plan.resolved.Copy.Medium,
		Target:     req.Target,
		SizeBytes:  plan.expect.Size,
	}, nil
}

// checkPlanDigest refuses, at mint, an artifact the journal holds no
// SHA-256 for: there is nothing to verify its bytes against, and recovery
// does not hand over bytes nobody can vouch for.
func checkPlanDigest(plan recoveryPlan) error {
	if err := export.CheckExpect(plan.expect); err != nil {
		return mapRecoveryError(err)
	}
	return nil
}

func (t RecoveryTarget) label() string {
	switch t.Kind {
	case "staging":
		return "staging:" + t.Path
	case "storage":
		return "storage:" + t.Medium
	}
	return t.Kind
}

// validateRecoveryTarget checks the target at mint time, so a bad one is
// refused before anything is authorised.
func (b *BackupService) validateRecoveryTarget(h *recoveryHost, artifactID string, t RecoveryTarget) (export.Target, error) {
	switch t.Kind {
	case "download":
		return export.Target{Kind: export.TargetDownload}, nil
	case "staging":
		if err := h.staging.ValidateName(t.Path); err != nil {
			return export.Target{}, mapRecoveryError(err)
		}
		return export.Target{Kind: export.TargetStaging, Path: t.Path}, nil
	case "storage":
		cfg := b.state.Load().inner.Config
		if t.Medium == "" || t.Medium == state.MediumLocal {
			return export.Target{}, recoveryErr(RecoveryTargetInvalid, "a storage target needs the id of a configured storage destination")
		}
		if _, _, err := app.MediumFor(cfg, t.Medium); err != nil {
			return export.Target{}, recoveryErr(RecoveryTargetInvalid, "%q is not a configured storage destination", t.Medium)
		}
		key := t.Key
		if key == "" {
			key = "recovered/" + artifactID
		}
		clean := path.Clean(key)
		if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(key, 0) {
			return export.Target{}, recoveryErr(RecoveryTargetInvalid, "that key does not stay inside the destination's namespace")
		}
		return export.Target{Kind: export.TargetStorage, Medium: t.Medium, Key: clean}, nil
	}
	return export.Target{}, recoveryErr(RecoveryTargetInvalid, "target kind must be download, staging or storage")
}

// startRecoveryThaw is the default thaw: the existing restore_placement
// operation, keyed so that asking twice on one day starts one provider job.
func (b *BackupService) startRecoveryThaw(ctx context.Context, actor, artifactID, medium string) (RecoveryThaw, error) {
	sub, err := b.SubmitRestorePlacement(ctx, RestorePlacementRequest{
		IdempotencyKey: "recovery-thaw:" + artifactID + ":" + medium + ":" + now().Format("2006-01-02"),
		Actor:          actor,
		ConfigRevision: b.ConfigRevision(),
		ArtifactID:     artifactID,
		Medium:         medium,
		WindowDays:     defaultRecoveryThawDays,
		Acknowledged:   true,
	})
	if err != nil {
		return RecoveryThaw{}, err
	}
	return RecoveryThaw{OperationID: sub.Operation.ID, Wait: sub.Wait, Billing: sub.Billing}, nil
}

const defaultRecoveryThawDays = 7

// recoveryEvent is one action-log row.
type recoveryEvent struct {
	op       string // mint, range, run, abort
	actor    string
	artifact string
	copy     string
	target   string
	bytes    int64
	rng      string
	note     string
}

// auditRecovery writes one row to the action log (obs.EventAPIAction, the
// same event every other API action lands on), naming actor, artifact,
// copy, target and the outcome. A failure writes a row that says so.
func (b *BackupService) auditRecovery(ctx context.Context, ev *recoveryEvent, err error) {
	if b == nil {
		return
	}
	result, outcome, code := obs.ResultSuccess, "ok", ""
	if err != nil {
		result, outcome = obs.ResultError, "failed"
		code = RecoveryFailed
		if re, ok := AsRecoveryError(mapRecoveryError(err)); ok {
			code = re.Code
		}
		if errors.Is(err, ErrArtifactNotFound) {
			code = recoveryArtifactNotFoundCode
		}
		if errors.Is(err, ErrRecoveryRangeNotSatisfiable) {
			code = "RECOVERY_RANGE_NOT_SATISFIABLE"
		}
		if isRefusal(code) {
			result, outcome = obs.ResultWarn, "refused"
		}
	}
	if code == RecoveryFailed {
		b.logger.Error(ctx, "recovery_failure", err)
	}
	route := "GET /api/v1/recoveries/{grant}"
	if ev.op == "mint" {
		route = "POST /api/v1/backups/{id}/recovery"
	}
	attrs := []slog.Attr{
		slog.String("actor", ev.actor),
		slog.String("route", route),
		slog.String("operation", "recovery "+ev.op),
		slog.String("artifact", ev.artifact),
		slog.String("copy", ev.copy),
		slog.String("target", ev.target),
		slog.String("outcome", outcome),
	}
	if code != "" {
		attrs = append(attrs, slog.String("error_code", code))
	}
	if ev.rng != "" {
		attrs = append(attrs, slog.String("range", ev.rng))
	}
	if ev.bytes > 0 {
		attrs = append(attrs, slog.Int64("bytes", ev.bytes))
	}
	if ev.note != "" {
		attrs = append(attrs, slog.String("detail", ev.note))
	}
	b.logger.Completed(ctx, result, obs.EventAPIAction, "recovery "+ev.op+" "+outcome, attrs...)
}

func isRefusal(code string) bool {
	switch code {
	case RecoveryCostNotAcknowledged, RecoveryNoCopy, RecoveryCopyNotFound, RecoveryNotReadable,
		RecoveryGrantUnknown, RecoveryGrantUsed, RecoveryGrantExpired, RecoveryGrantSessionEnded,
		RecoveryTooManyGrants, RecoveryTargetInvalid, RecoveryTargetExists, RecoveryUseSnapshotRestore,
		RecoveryBelowReserve, RecoveryBusy, recoveryArtifactNotFoundCode, "RECOVERY_RANGE_NOT_SATISFIABLE":
		return true
	}
	return false
}

// observeRestore asks the provider whether a restore of an archive-class
// copy has finished, which is the one fact the journal cannot hold and the
// one without which a recovery could never complete: a copy on an archive
// class reads as needing a restore for ever unless something looks.
//
// It is a metadata call. It starts nothing, reads no object and costs no
// egress (FR-34's rule is that a read never initiates a restore), and it is
// made only for an archive class, so a standard-class copy is never asked.
// A medium that does not answer is recorded as not answering, which makes
// the copy unreachable and therefore refused: the direction that never
// reads, and never pays, on a guess.
func (b *BackupService) observeRestore(ctx context.Context, st *configState, m transport.Medium, class string, p state.Placement) archive.Observation {
	if !archive.IsArchive(class) {
		return archive.Observation{}
	}
	store, _ := st.inner.Transport.(archive.Store)
	if store == nil {
		return archive.Observation{}
	}
	rs, err := store.RestoreStatus(ctx, m, p.Location)
	if err != nil {
		return archive.Observation{Probe: archive.DidNotAnswer}
	}
	return archive.Observation{Probe: archive.Answered, Restore: rs}
}

// RecoveryErrorFor is the coded form of any error a recovery can hit, for
// the HTTP layer to answer a failure it met outside the service (a first
// chunk that failed verification) in the words the terminal would use.
func RecoveryErrorFor(err error) error { return mapRecoveryError(err) }
