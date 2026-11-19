package main

import (
	"os"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// TestMain makes this suite say the same thing on a developer's machine
// and in CI.
//
// The three route settings (route.go) are read from the environment, and
// most of the refusal cases in this package are about what happens when
// none of them is set. Inheriting one from the shell that started `go
// test` would turn those into routed writes aimed at whatever engine that
// developer happens to be pointed at, which is both a suite that proves
// something different and a command reaching a real deployment from a
// test run.
//
// Cleared before anything runs rather than per test, so a case added later
// cannot forget. The daemon child this package re-executes
// (daemon_signal_test.go) inherits this process's environment, so it is
// covered by the same call.
//
// The default workflow root is switched off for the same reason (#1246): a
// suite run inside the retnd image, or on a NAS, has a real /workflows, and
// every command here would quietly pick it up.
func TestMain(m *testing.M) {
	clearInheritedRouteSettings()
	workflowRootDefault = func() service.WorkflowRootDefault { return service.WorkflowRootDefault{} }
	os.Exit(m.Run())
}
