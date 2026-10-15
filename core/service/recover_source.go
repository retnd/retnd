package service

import "context"

// The "Source host (SFTP)" recovery destination (issue #1191). Stubs: the
// behaviour lands with the implementation.

const (
	RecoverySourceUnavailable     = "RECOVERY_SOURCE_UNAVAILABLE"
	RecoverySourceReadOnly        = "RECOVERY_SOURCE_READ_ONLY"
	RecoverySourceNotAcknowledged = "RECOVERY_SOURCE_NOT_ACKNOWLEDGED"
)

// RecoverySourceDestination is what the source-host destination of one
// backup set is, and whether it can be written to.
type RecoverySourceDestination struct {
	SetID      string
	Available  bool
	Reason     string
	Host       string
	Port       int
	User       string
	RemotePath string
	Folder     string
	Writable   string
	Detail     string
}

// RecoverySourceFor describes the set's source-host destination without
// touching the network.
func (b *BackupService) RecoverySourceFor(setID string) RecoverySourceDestination {
	return RecoverySourceDestination{SetID: setID}
}

// PreflightRecoverySource is RecoverySourceFor plus the write probe.
func (b *BackupService) PreflightRecoverySource(ctx context.Context, setID string) (RecoverySourceDestination, error) {
	return b.RecoverySourceFor(setID), nil
}

// DefaultRecoveryTarget is the destination a recovery of this artifact or
// set starts on, and why there is none when there is none.
func (b *BackupService) DefaultRecoveryTarget(id string) (RecoveryTarget, string) {
	return RecoveryTarget{}, ""
}
