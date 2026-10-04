package testenv

import (
	"os"
	"testing"
)

// CoreWithoutAppsEnv marks the architecture proof that deliberately removes
// apps/ before building and testing core/. Repository-wide source scanners
// cannot make a complete-tree claim in that worktree; core-local behavioral
// tests still run normally.
const CoreWithoutAppsEnv = "RETND_CORE_WITHOUT_APPS"

// SkipIfCoreWithoutApps skips a test whose subject is the complete repository,
// rather than core's behavior. The ordinary core test job does not set this
// marker and remains the complete-tree enforcement point.
func SkipIfCoreWithoutApps(t *testing.T) {
	t.Helper()
	if os.Getenv(CoreWithoutAppsEnv) == "1" {
		t.Skipf("%s=1: this repository-wide source scan requires the intentionally removed apps/ tree", CoreWithoutAppsEnv)
	}
}
