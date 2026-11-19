package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/apiclient"
)

// engineHolds is the edit hold of the process serving this deployment, as a
// CLI scrub sees it (D7, EPIC Q).
//
// An edit hold lives in the memory of the serving process and nowhere else,
// so a scrub run from a second process has no way to see one except by asking
// that process. This asks it, through the same route `backup-set edit-hold`
// reads, and answers app.BackupSetHolds with it, which makes the scrub skip a
// held set through the same code, with the same words and the same exit
// status, as a scrub the engine runs itself.
//
// It fails closed. A hold this command cannot read is a hold it cannot rule
// out, so the set is reported as held (and the reason is said once on
// stderr) rather than scrubbed on a guess: the skip is visible and the
// operator can run it again, where a scrub that quarantined an artifact in a
// set somebody is editing is not undone by running it again.
type engineHolds struct {
	client *apiclient.Client
	ctx    context.Context
	now    func() time.Time

	mu     sync.Mutex
	cached map[string]heldAnswer
	warned bool
}

type heldAnswer struct {
	held bool
	at   time.Time
}

// holdFreshness is how long one answer stands. A pass asks per artifact, and
// a request per artifact would make a thousand-copy scrub a thousand-request
// one; a second is short enough that a hold taken mid-pass stops the next
// artifact, not the one after the pass.
const holdFreshness = time.Second

func newEngineHolds(ctx context.Context, c *apiclient.Client) *engineHolds {
	return &engineHolds{client: c, ctx: ctx, now: time.Now, cached: map[string]heldAnswer{}}
}

// Held reports whether the engine is holding setID ("source/name").
func (h *engineHolds) Held(setID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, ok := h.cached[setID]; ok && h.now().Sub(a.at) < holdFreshness {
		return a.held
	}
	held := h.ask(setID)
	h.cached[setID] = heldAnswer{held: held, at: h.now()}
	return held
}

func (h *engineHolds) ask(setID string) bool {
	source, set, ok := splitBackupSetID(setID)
	if !ok {
		return h.cannotTell(setID, fmt.Errorf("not a backup set id"))
	}
	st, err := h.client.GetBackupSetEditHold(h.ctx, source, set)
	if err != nil {
		return h.cannotTell(setID, err)
	}
	return st.Held
}

func (h *engineHolds) cannotTell(setID string, err error) bool {
	if !h.warned {
		h.warned = true
		fmt.Fprintf(os.Stderr, "retnd: could not ask the serving engine whether %s is held for editing (%v); treating it as held, so nothing in it is scrubbed\n", setID, err)
	}
	return true
}

// Changed never fires. The engine pushes nothing a CLI could wait on, so a
// hold taken during a pass is noticed at the next artifact's Held, within
// holdFreshness, and not by cancelling the read already in flight.
func (h *engineHolds) Changed() <-chan struct{} { return nil }
