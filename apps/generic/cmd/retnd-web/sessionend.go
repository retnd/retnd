package main

import (
	"time"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/core/service"
)

// recoverySessionEnder is the one method of the backup service a logout needs.
type recoverySessionEnder interface {
	EndRecoverySession(sess service.RecoverySession)
}

// endRecoverySessionsOnLogout makes ending a local session (logout, a
// password rotation, a reset) revoke the recovery grants minted in it and
// remove what they staged. The pair handed over is the one the
// authenticator reports for that session, which is what a grant was bound
// to at the mint. A nil auth is a gateway profile, which has no sessions
// of its own to end.
func endRecoverySessionsOnLogout(auth *local.Service, backend recoverySessionEnder) {
	if auth == nil || backend == nil {
		return
	}
	auth.OnSessionEnd(func(username string, expiresAt time.Time) {
		backend.EndRecoverySession(service.RecoverySession{Actor: username, Expires: expiresAt})
	})
}
