package service

import (
	"context"
	"errors"
)

// ErrConfigLostOverJournal is the answer for a deployment whose configuration
// is missing while its journal holds backups (retnd#1049). It is not a fresh
// install and must not be treated as one.
var ErrConfigLostOverJournal = errors.New("service: configuration missing but the journal holds backups")

// CheckJournalIsFresh returns nil when the journal at dbPath has recorded
// nothing, and an error wrapping ErrConfigLostOverJournal when it holds
// backups. The red-test commit ships it as a stub that always says nil.
func CheckJournalIsFresh(_ context.Context, _ string) error { return nil }
