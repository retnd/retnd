package main

import (
	"strings"
	"testing"
)

// #1558: `retnd check`, `status` and `run` read the configuration and the
// journal themselves, so on a deployment still mounted at its pre-0.5.0 path
// they used to read the new, empty path and answer as if it were a fresh
// install ("no backup set configured yet", the first-run notice), with no
// word about the catalogue sitting at the old path. They refuse now, with the
// same message `retnd daemon` and `retnd-web serve` give (#1546), before they
// read anything.
//
// Each case drives the real command in this process (runCLI), against
// #1546's deployment fixture: a root with the current and the pre-rename
// spelling of the configuration directory under it.
//
// Falsify: drop the refusedAtPreRenamePath call from check, or the
// checkPreRenamePath call from openService (status) or openBackupDataPlane
// (run); that command's subtests go red. prerename_everycommand_1609_test.go
// has the rest of the verbs.

var commands1558 = []string{"check", "status", "run"}

func assertCLIRefused1558(t *testing.T, cmd string, code int, stdout, stderr string, wants ...string) {
	t.Helper()
	if code == 0 {
		t.Errorf("retnd %s exited 0; a refusal has to be a non-zero exit\nstdout:\n%s\nstderr:\n%s", cmd, stdout, stderr)
	}
	for _, w := range wants {
		if !strings.Contains(stderr, w) {
			t.Errorf("retnd %s's standard error does not mention %q\nstdout:\n%s\nstderr:\n%s", cmd, w, stdout, stderr)
		}
	}
	for _, firstRun := range []string{"no backup set configured", "first run", "config OK"} {
		if strings.Contains(stdout, firstRun) {
			t.Errorf("retnd %s answered %q about an empty path while the catalogue is at the old one\nstdout:\n%s", cmd, firstRun, stdout)
		}
	}
}

func TestCLI_1558_OnlyTheFormerPathHoldsTheCatalogue_Refuses(t *testing.T) {
	for _, cmd := range commands1558 {
		t.Run(cmd, func(t *testing.T) {
			d := deployment1546{root: t.TempDir()}
			d.populate(t, d.formerConfig(), "old")

			code, stdout, stderr := runCLI(t, cmd, "--config", d.currentConfig())
			assertCLIRefused1558(t, cmd, code, stdout, stderr,
				"refusing to start",
				d.formerConfig(),
				d.currentConfig(),
				"--config "+d.formerConfig(),
			)
		})
	}
}

func TestCLI_1558_BothPathsPopulated_RefusesNamingTheConflict(t *testing.T) {
	for _, cmd := range commands1558 {
		t.Run(cmd, func(t *testing.T) {
			d := deployment1546{root: t.TempDir()}
			d.populate(t, d.currentConfig(), "new")
			d.populate(t, d.formerConfig(), "old")

			code, stdout, stderr := runCLI(t, cmd, "--config", d.currentConfig())
			assertCLIRefused1558(t, cmd, code, stdout, stderr,
				"refusing to start",
				"two different",
				d.currentConfig(),
				d.formerConfig(),
			)
		})
	}
}

// The controls: a deployment at the current path, and a fresh install with
// nothing anywhere, are never refused. Each command keeps its ordinary answer
// (whatever exit the health of the fixture gives it), and the refusal must
// not replace it.
func TestCLI_1558_OnlyTheCurrentPathPopulated_IsNotRefused(t *testing.T) {
	for _, cmd := range commands1558 {
		t.Run(cmd, func(t *testing.T) {
			d := deployment1546{root: t.TempDir()}
			d.populate(t, d.currentConfig(), "new")

			code, stdout, stderr := runCLI(t, cmd, "--config", d.currentConfig())
			if strings.Contains(stderr, "refusing to start") {
				t.Errorf("retnd %s refused a deployment at the current path (exit %d)\n%s", cmd, code, stderr)
			}
			if cmd == "check" && (code != 0 || !strings.Contains(stdout, "config OK")) {
				t.Errorf("retnd check against a working deployment = exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
		})
	}
}

func TestCLI_1558_NeitherPathPopulated_IsAnOrdinaryFreshInstall(t *testing.T) {
	for _, cmd := range []string{"check", "status"} {
		t.Run(cmd, func(t *testing.T) {
			d := deployment1546{root: t.TempDir()}

			code, stdout, stderr := runCLI(t, cmd, "--config", d.currentConfig())
			if strings.Contains(stderr, "refusing to start") {
				t.Errorf("retnd %s refused a fresh install (exit %d)\n%s", cmd, code, stderr)
			}
			if code != 0 {
				t.Errorf("retnd %s on a fresh install = exit %d, want its first-run notice and 0\nstdout:\n%s\nstderr:\n%s", cmd, code, stdout, stderr)
			}
		})
	}
	t.Run("run", func(t *testing.T) {
		d := deployment1546{root: t.TempDir()}
		_, _, stderr := runCLI(t, "run", "--config", d.currentConfig())
		if strings.Contains(stderr, "refusing to start") {
			t.Errorf("retnd run refused a fresh install\n%s", stderr)
		}
	})
}
