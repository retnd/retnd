package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/retnd/retnd/core/internal/state"
)

// ErrConfigLostOverJournal is the answer for a deployment whose configuration
// is missing while its journal holds backups, or holds something this process
// cannot read (retnd#1049, panel finding C7). That is a lost or unmounted
// configuration volume next to a state volume with history in it. It is not
// a fresh install, and nothing may treat it as one: no default configuration
// is written over it, and `status` and `check` fail instead of printing a
// first-run notice.
var ErrConfigLostOverJournal = errors.New("service: configuration missing but the journal holds backups")

// CheckJournalIsFresh returns nil when the journal at dbPath has recorded
// nothing (no file, an empty file, or a migrated journal with no artifacts),
// and otherwise an error wrapping ErrConfigLostOverJournal that names the
// journal and how many backups it holds.
//
// It reads the journal read-only: it creates nothing, runs no migration and
// takes no lock, so it is safe to call from `status` inside a container
// healthcheck while the engine is serving. A journal that exists but cannot
// be read is refused too, never assumed empty: the one thing this function
// must not do is call a deployment with history a fresh install because it
// failed to look.
func CheckJournalIsFresh(ctx context.Context, dbPath string) error {
	if dbPath == "" {
		return nil
	}
	n, exists, err := state.CountArtifactsReadOnly(ctx, dbPath)
	switch {
	case err != nil:
		return fmt.Errorf("%w: configuration missing and the journal at %s cannot be read (%v), so this is not treated as a fresh install", ErrConfigLostOverJournal, dbPath, err)
	case !exists || n == 0:
		return nil
	case n == 1:
		return fmt.Errorf("%w: configuration missing but the journal at %s holds 1 backup", ErrConfigLostOverJournal, dbPath)
	default:
		return fmt.Errorf("%w: configuration missing but the journal at %s holds %d backups", ErrConfigLostOverJournal, dbPath, n)
	}
}

// JournalArtifactCount is how many artifacts this deployment's journal holds.
// The web host reads it to tell a deployment that has never backed anything
// up from one whose configuration lost its sets (retnd#1049).
func (b *BackupService) JournalArtifactCount(ctx context.Context) (int64, error) {
	return b.journal.CountArtifacts(ctx)
}
