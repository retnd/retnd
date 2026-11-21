// This file is issue #350's second half: what happens to a backup set's
// own processing while an operator is editing it.
//
// # The problem
//
// A set being edited while a cycle is running against it is two writers
// on one definition. The cycle is mid-transfer against the old remote
// path while the operator is changing it, and whichever finishes last
// wins silently. So entering edit mode takes a HOLD on that one backup
// set, which stops the pass currently running against it and stops the
// scheduler starting another until edit mode is left. Stopping the
// in-flight run while leaving the poll interval free would be the same
// race with extra steps.
//
// # A lease, not a flag
//
// A hold expires. That is the single most important property here,
// because the failure this design must not have is a set left
// permanently paused because somebody closed a tab: a backup silently
// not happening is this product's worst outcome, worse than the race the
// hold exists to prevent. So a hold is a lease with a short lifetime that
// the editing client renews while its form is open, and every route out
// of edit mode releases it explicitly on top of that. The explicit
// release is the fast path; the lease is what covers the routes a client
// cannot report (a closed laptop, a crashed browser, a lost network).
//
// It is deliberately NOT durable. A hold that survived a restart would
// mean a process coming up after a crash refuses to back up a set nobody
// is editing any more, and the only way out would be a second restart.
// The in-memory registry here is on exactly the same footing as
// liveProgress (progress.go) and the retention plan store (retention.go)
// and for the same reason.
//
// # What an authenticated caller can do with this, said out loud
//
// Any authenticated caller can hold any backup set, and can keep renewing
// it, which pauses that set's backups for as long as they keep going.
// That is worth stating rather than leaving to be discovered, and it is
// not an escalation: the same caller can already turn the set off
// outright through POST /backup-sets/{source}/{set}/enabled, which is
// both easier and more durable. The lease is what bounds the accidental
// version of it, which is the one that actually happens.
//
// # A holder token, and what it is not
//
// Taking and renewing used to be one unconditional call, so a renewal
// still in flight when edit mode was left could land after the release,
// put a full lease back and stop a run that had started in between
// (issue #1606). So every take now mints a token naming that holder, and
// the registry keeps one lease per holder. A renewal names its token and
// only ever EXTENDS that holder's live lease: a token that was released,
// has lapsed or never existed is refused with ErrEditHoldGone, nothing is
// held and nothing is broadcast. A client still editing that gets the
// refusal takes a fresh hold; one that has left edit mode drops it.
//
// Two tabs, or a tab and a script, are two holders. Neither renews the
// other's lease, and a release that names a token gives back that holder
// alone, so one tab leaving edit mode does not pull the hold out from
// under another that is still editing.
//
// The token is NOT a credential and does not change who may do what. A
// release with no token still gives back every hold on the set, because
// that is what a client from before tokens sends and what an operator
// clearing a hold somebody left behind needs, and making that path
// fallible would cost the thing that matters more than stopping operator
// B releasing operator A's hold. The concurrent-edit case that actually
// corrupts something is still caught by the staleness check the edit
// form runs (ui/shared's isSetEditStale), which compares against the
// value the form opened over rather than against who holds a lease.
//
// Tokens live in this registry and nowhere else, so they do not survive
// a restart either, for the reason a hold does not: after one, a renewal
// is refused and the client takes a fresh hold that says for itself what
// it stopped.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
)

// editHoldLease is how long one hold lasts without being renewed. Short,
// because the cost of a stale hold is backups not running; long enough
// that an ordinary client heartbeat (a third of this) covers a slow
// network without the hold lapsing under an operator mid-edit.
const editHoldLease = 90 * time.Second

// RunningWork names what a cycle is doing for one backup set at the
// moment it was asked. It is what makes the warning an operator sees
// before entering edit mode a real sentence rather than a bare "are you
// sure": discarding a partial transfer of a named artifact is a very
// different cost from cancelling a scheduler tick that has not started
// work, and only the first one is worth interrupting an operator over.
type RunningWork struct {
	// Artifact is the artifact being worked on, or "" during discovery,
	// when the cycle has reached this set but has not picked one yet.
	Artifact string
	// Stage is one of OperationStages (progress.go).
	Stage string
}

// EditHold is what BeginBackupSetEdit and RenewBackupSetEdit return.
type EditHold struct {
	BackupSetID string
	// ExpiresAt is when this hold lapses if nothing renews it. A client
	// that means to keep editing renews well before it.
	ExpiresAt time.Time
	// Token names this holder (issue #1606). Renewing and releasing with
	// it touch this holder's lease and nobody else's.
	Token string
	// Stopped describes the work this hold interrupted, or nil when
	// nothing was running for this set. Reported so a client can say what
	// it actually stopped, rather than claiming it stopped something
	// whenever an operator presses Edit.
	Stopped *RunningWork
}

// BackupSetEditState is the read behind the warning: what is happening to
// this set right now, and whether a hold is already in place.
type BackupSetEditState struct {
	BackupSetID string
	// Held reports whether a hold is currently in force.
	Held bool
	// ExpiresAt is meaningful only when Held.
	ExpiresAt time.Time
	// Running is what a cycle is currently doing for this set, or nil.
	// Nil is what makes "entering edit mode is silent when nothing is
	// running" implementable: no prompt for a risk that does not exist.
	Running *RunningWork
}

// editHolds is the in-memory registry of every backup set currently held
// for editing in this process. It implements internal/app's
// BackupSetHolds, which is how a cycle in flight consults it.
//
// Expiry is lazy, checked on every read rather than swept by a
// goroutine: there is nothing to clean up when a hold lapses (no file, no
// journal row), the only observable effect is that Held stops answering
// true, and a sweeper would be a goroutine whose only job is to make that
// happen a few milliseconds earlier.
type editHolds struct {
	mu sync.Mutex
	// held maps a backup set id to its holders, each a token mapped to
	// when that holder's lease expires (issue #1606). A set is held while
	// any of its holders' leases is live.
	held map[string]map[string]time.Time
	// removed names every backup set whose configuration this process has
	// REMOVED (issue #391), and it is the one hold here that does not
	// expire.
	//
	// The lease above exists because an edit hold that outlived its
	// client would leave a set silently not backing up, and nothing worse
	// than that can happen here. A removal is the other way round: the
	// set is gone from the configuration, so a hold on its id stops
	// nothing an operator still wants, and letting it lapse is what would
	// hurt. A cycle can run for hours holding the config snapshot it
	// started with, so a removal whose hold expired after ninety seconds
	// would be a cycle that reaches the removed set forty minutes later,
	// sees Held answer false again, and processes it from the old
	// snapshot: discovering, transferring and, for a set that is not
	// read-only, deleting from the operator's source machine, all after
	// they removed it and watched the dialog close.
	//
	// It is cleared by exactly one thing: a reload of a configuration
	// that names the id again (adoptConfig, which every write path ends
	// in), so a set brought back gets a clean start whether it came back
	// through CreateBackupSet or through a hand edit of config.yaml that
	// the next write picked up. A process restart clears it too, and
	// correctly: by then the configuration on disk no longer names the
	// set, so nothing will reach it anyway.
	removed map[string]bool
	// changed is closed and replaced whenever a hold is PLACED, which is
	// how a cycle already inside that set learns to stop. Releases
	// deliberately do not broadcast; see BackupSetHolds.Changed's own doc.
	changed chan struct{}
	// now is the clock, a seam so a test can prove a lease actually
	// lapses without sleeping for a minute and a half.
	now func() time.Time
}

func newEditHolds() *editHolds {
	return &editHolds{
		held:    map[string]map[string]time.Time{},
		removed: map[string]bool{},
		changed: make(chan struct{}),
		now:     func() time.Time { return now() },
	}
}

// Held is internal/app.BackupSetHolds.Held.
func (h *editHolds) Held(setID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.heldLocked(setID)
}

// heldLocked is what a cycle asks: is this set held for ANY reason. A
// removal outranks a lease and is checked first, because it is the answer
// that must not depend on a clock.
func (h *editHolds) heldLocked(setID string) bool {
	if h.removed[setID] {
		return true
	}
	return h.leaseHeldLocked(setID)
}

// leaseHeldLocked is the edit hold on its own, expiry and all. It is
// separate from heldLocked so BackupSetEditState can report on edit mode
// without a removed set's permanent hold showing up there as an edit
// somebody is in the middle of.
func (h *editHolds) leaseHeldLocked(setID string) bool {
	_, ok := h.latestLocked(setID)
	return ok
}

// latestLocked drops setID's lapsed holders and reports the latest expiry
// among the ones left, or false when none is. Lapsed holders are dropped
// here rather than left to accumulate, so the map holds exactly the
// holders editing right now, and a lapsed holder's token is refused from
// then on exactly like a released one.
func (h *editHolds) latestLocked(setID string) (time.Time, bool) {
	holders := h.held[setID]
	now := h.now()
	var latest time.Time
	for token, expiry := range holders {
		if !now.Before(expiry) {
			delete(holders, token)
			continue
		}
		if expiry.After(latest) {
			latest = expiry
		}
	}
	if len(holders) == 0 {
		delete(h.held, setID)
		return time.Time{}, false
	}
	return latest, true
}

// Changed is internal/app.BackupSetHolds.Changed.
func (h *editHolds) Changed() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changed
}

// place takes a new hold on setID for a new holder, and returns that
// holder's token and expiry. It broadcasts, which is what stops a cycle
// already inside the set.
func (h *editHolds) place(setID string) (string, time.Time, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("service: minting an edit hold token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	h.mu.Lock()
	defer h.mu.Unlock()
	expiry := h.now().Add(editHoldLease)
	if h.held[setID] == nil {
		h.held[setID] = map[string]time.Time{}
	}
	h.held[setID][token] = expiry
	close(h.changed)
	h.changed = make(chan struct{})
	return token, expiry, nil
}

// renew extends the live lease token names on setID and returns its new
// expiry, or false when that holder is not holding the set: released,
// lapsed, or never given out (issue #1606). It never creates a hold and,
// when it refuses, never broadcasts, so a renewal that lands after the
// release can neither put the hold back nor stop a cycle that started in
// between. A successful renewal does not broadcast either: the set was
// already held by this holder, so no cycle a broadcast could stop is
// inside it.
func (h *editHolds) renew(setID, token string) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latestLocked(setID)
	if _, ok := h.held[setID][token]; !ok {
		return time.Time{}, false
	}
	expiry := h.now().Add(editHoldLease)
	h.held[setID][token] = expiry
	return expiry, true
}

// release gives back the hold token names on setID, or, when token is "",
// every hold on it. Releasing one that is not held is not an error: every
// route out of edit mode calls this, several of them can fire for the same
// edit (a Save-and-exit followed by an unmount), and a second release must
// not be something a client has to avoid.
func (h *editHolds) release(setID, token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if token == "" {
		delete(h.held, setID)
		return
	}
	delete(h.held[setID], token)
	if len(h.held[setID]) == 0 {
		delete(h.held, setID)
	}
}

// holdRemoved holds setID permanently, because its configuration is being
// removed. It broadcasts exactly as place does, which is what stops a
// cycle already inside that set: the watcher wakes, re-reads Held, and
// cancels that set's own context.
//
// Called BEFORE the configuration is rewritten and the service swapped,
// never after, and under configMu. The write swaps this service's
// *app.Service, and a cycle already running kept the pointer and the
// config snapshot it started with, so nothing about the write reaches
// it. The hold is the only thing that does. Under the lock, because two
// removals of the same set can overlap, and a hold taken outside it was
// one the losing call could not tell from its own and gave back; see
// RemoveBackupSet's own doc.
func (h *editHolds) holdRemoved(setID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removed[setID] = true
	close(h.changed)
	h.changed = make(chan struct{})
}

// forgetRemoved drops setID's removal hold. Called by RemoveBackupSet
// when it fails after having taken the hold, under the same lock it took
// it under, so the hold it drops is its own. Forgetting one that was
// never taken is not an error, for the same reason release beside it
// says so.
func (h *editHolds) forgetRemoved(setID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.removed, setID)
}

// forgetRemovedNamedIn drops the removal hold of every backup set cfg
// names. adoptConfig calls it on every hot reload, which is what keeps
// "a set the configuration names is never removal-held" true by whatever
// route the set came back: CreateBackupSet, or a hand-restored config.yaml
// that an unrelated write re-read. A set cfg does not name keeps its
// hold, which is the removal's own case.
func (h *editHolds) forgetRemovedNamedIn(cfg *config.Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, src := range cfg.Sources {
		for _, bs := range src.BackupSets {
			delete(h.removed, src.Name+"/"+bs.Name)
		}
	}
}

// state reports whether setID is held for EDITING and until when: the
// latest expiry among its holders, which is when the set is free again if
// nobody renews.
func (h *editHolds) state(setID string) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latestLocked(setID)
}

var _ app.BackupSetHolds = (*editHolds)(nil)

// cycleWatch is the ONE live reading of whatever run cycle is executing
// in this process, whichever way it started.
//
// liveProgress (progress.go) beside it is keyed by operation id, which is
// the right shape for polling a submitted operation and the wrong shape
// for this question: a SCHEDULED tick has no operation row and therefore
// no id, so a set being transferred by the scheduler was invisible to
// every reader. That is exactly the case an operator meets, since the
// scheduler is what runs unattended, and a warning that could not name
// what it was about to stop would be the bare "are you sure" the issue
// rules out.
type cycleWatch struct {
	mu      sync.RWMutex
	running bool
	cur     app.Progress
}

func newCycleWatch() *cycleWatch { return &cycleWatch{} }

// begin marks a cycle as executing. end is deferred by both callers, so a
// cycle that panicked stops answering exactly like one that returned.
func (c *cycleWatch) begin() {
	c.mu.Lock()
	c.running = true
	c.cur = app.Progress{}
	c.mu.Unlock()
}

func (c *cycleWatch) end() {
	c.mu.Lock()
	c.running = false
	c.cur = app.Progress{}
	c.mu.Unlock()
}

// ObserveProgress is internal/app.ProgressObserver.
func (c *cycleWatch) ObserveProgress(p app.Progress) {
	c.mu.Lock()
	c.cur = p
	c.mu.Unlock()
}

// workFor reports what the running cycle is doing for setID, or nil when
// no cycle is running or the running one is somewhere else. Nil is the
// answer that means "no prompt", so it has to be nil for the ordinary
// case rather than a zero-valued struct a caller might render.
func (c *cycleWatch) workFor(setID string) *RunningWork {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.running || c.cur.BackupSetID != setID {
		return nil
	}
	return &RunningWork{Artifact: c.cur.Artifact, Stage: c.cur.Stage}
}

// progressFanout hands one cycle's readings to several observers, so an
// API-submitted run can feed both its own operation-scoped reading
// (liveProgress, which a client polls) and the process-wide cycleWatch
// above, which answers "what would entering edit mode stop".
type progressFanout []app.ProgressObserver

func (f progressFanout) ObserveProgress(p app.Progress) {
	for _, o := range f {
		o.ObserveProgress(p)
	}
}

// BackupSetEditState reports what entering edit mode for this set would
// interrupt, and whether a hold is already in force. Read-only: it takes
// no hold, stops nothing, and is safe to poll.
//
// This is the call behind "the warning appears only when something is
// actually running": Running is nil whenever no cycle is currently inside
// this set, and a client that gets nil opens edit mode with no prompt.
func (b *BackupService) BackupSetEditState(_ context.Context, id string) (BackupSetEditState, error) {
	if _, _, ok := splitBackupSetID(id); !ok {
		return BackupSetEditState{}, wrapNotFound(id)
	}
	if err := b.requireBackupSet(id); err != nil {
		return BackupSetEditState{}, err
	}
	expiry, held := b.holds.state(id)
	return BackupSetEditState{
		BackupSetID: id,
		Held:        held,
		ExpiresAt:   expiry,
		Running:     b.cycleWatch.workFor(id),
	}, nil
}

// BeginBackupSetEdit takes a new hold on one backup set, for a new
// holder: the cycle currently processing it, if any, is stopped at its
// next safe boundary, and no new pass will be started for it until every
// hold on it is released or lapses. The returned Token names this holder;
// RenewBackupSetEdit and EndBackupSetEdit take it back.
//
// It returns what it interrupted, so a client can report the truth
// afterwards rather than claiming to have stopped something every time.
// The reading is taken BEFORE the hold is placed, which is the only order
// that can answer the question: once the hold lands the cycle starts
// unwinding, and a reading taken after it would race the very
// cancellation this call caused.
func (b *BackupService) BeginBackupSetEdit(_ context.Context, id string) (EditHold, error) {
	if _, _, ok := splitBackupSetID(id); !ok {
		return EditHold{}, wrapNotFound(id)
	}
	if err := b.requireBackupSet(id); err != nil {
		return EditHold{}, err
	}
	stopped := b.cycleWatch.workFor(id)
	token, expiry, err := b.holds.place(id)
	if err != nil {
		return EditHold{}, err
	}
	return EditHold{BackupSetID: id, ExpiresAt: expiry, Token: token, Stopped: stopped}, nil
}

// RenewBackupSetEdit extends the lease of the holder token names. A client
// holding an edit form open calls this on a heartbeat; without it the
// hold lapses on its own, which is what stops a closed tab pausing a
// backup set forever.
//
// It refuses with ErrEditHoldGone when that holder is no longer holding
// the set (released, lapsed, or never given out), and it never creates a
// hold (issue #1606). Until #1606 this was the same call as taking one, on
// the reasoning that a heartbeat a second late should still end with the
// set held; that same unconditional take is what let a renewal in flight
// at release time put the hold back. The late heartbeat is still covered,
// one step further out: the client that gets the refusal while its form
// is open takes a fresh hold, which also reports what it stopped.
//
// A renewal of a live lease reports Stopped as nil. The set was already
// held by this holder, so whatever a cycle was doing when the hold was
// taken was that take's to report, and a cycle still unwinding from it is
// not something this call interrupted.
func (b *BackupService) RenewBackupSetEdit(_ context.Context, id, token string) (EditHold, error) {
	if _, _, ok := splitBackupSetID(id); !ok {
		return EditHold{}, wrapNotFound(id)
	}
	if err := b.requireBackupSet(id); err != nil {
		return EditHold{}, err
	}
	expiry, ok := b.holds.renew(id, token)
	if !ok {
		return EditHold{}, fmt.Errorf("%w: %s", ErrEditHoldGone, id)
	}
	return EditHold{BackupSetID: id, ExpiresAt: expiry, Token: token}, nil
}

// ErrEditHoldGone is a renewal naming a holder that is no longer holding
// the set: its hold was released, its lease lapsed, the engine restarted,
// or the token was never given out. Nothing is held when this is returned.
// The HTTP surface answers it as 409 EDIT_HOLD_GONE.
var ErrEditHoldGone = errors.New("service: this edit hold was released or has lapsed")

// EndBackupSetEdit releases the hold token names, and leaves any other
// holder of the same set in force. An empty token releases every hold on
// the set, which is what a client from before tokens sends and what an
// operator clearing a hold somebody left behind means.
//
// Every route out of edit mode calls it, and calling it for a set that is
// not held, or with a token that is already gone, is a success: several
// routes can fire for one edit (a save-and-exit followed by the form
// unmounting), and a duplicate release must not be something a client has
// to take care to avoid.
func (b *BackupService) EndBackupSetEdit(_ context.Context, id, token string) error {
	if _, _, ok := splitBackupSetID(id); !ok {
		return wrapNotFound(id)
	}
	if err := b.requireBackupSet(id); err != nil {
		return err
	}
	b.holds.release(id, token)
	return nil
}

// requireBackupSet refuses an id this deployment does not configure, so
// holding, renewing or releasing a set that does not exist is a 404
// rather than a hold on a name nothing will ever consult.
func (b *BackupService) requireBackupSet(id string) error {
	st := b.state.Load()
	for _, src := range st.inner.Config.Sources {
		for _, bs := range src.BackupSets {
			if src.Name+"/"+bs.Name == id {
				return nil
			}
		}
	}
	return wrapNotFound(id)
}

// wrapNotFound is the same %w-wrap every other method in this package
// uses for this condition (GetBackupSet, SetBackupSetEnabled,
// UpdateBackupSet), rather than a bespoke error type: a caller gets
// errors.Is(err, ErrBackupSetNotFound) and a message naming what was
// asked for, and one spelling means one thing to read.
func wrapNotFound(id string) error {
	return fmt.Errorf("%w: %s", ErrBackupSetNotFound, id)
}
