package main

import (
	"errors"

	"github.com/retnd/retnd/core/service"
)

var recoverExitCodes = map[string]int{}

func parseRecoverTarget(string) (service.RecoveryTarget, error) {
	return service.RecoveryTarget{}, errors.New("not implemented")
}

// cmdRecover is a stub until the tests in recover_test.go go green.
func cmdRecover(args []string) int { return exitFailure }
