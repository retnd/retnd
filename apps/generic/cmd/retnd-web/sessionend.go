package main

import (
	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/core/service"
)

// recoverySessionEnder is the one method of the backup service a logout needs.
type recoverySessionEnder interface {
	EndRecoverySession(sess service.RecoverySession)
}

// endRecoverySessionsOnLogout is a stub until the fix lands.
func endRecoverySessionsOnLogout(auth *local.Service, backend recoverySessionEnder) {}
