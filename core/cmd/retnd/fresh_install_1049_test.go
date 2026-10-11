package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The CLI side of the first-run invariant (retnd#1049): on a deployment
// nobody has configured yet, `status` and `check` are the two commands an
// operator reaches for to ask "is this thing all right", and the answer
// to that question on a fresh install is yes, with a next step. Today
// both fail with
//
//	retnd: service: no configuration file at /etc/retnd/config/config.yaml
//
// and exit 1, which is the same shape as a broken deployment.
//
// --config names the packaged configuration DIRECTORY, as the container
// image spells it (#196), and the directory is empty: that is exactly
// what a just-installed deployment looks like from inside the container.

var (
	// What a fresh install has to be called, in whichever words the
	// product picks: the state, not a failure.
	saysFirstRun = regexp.MustCompile(`(?i)not configured yet|first run|first-run|fresh install`)
	// And what to do about it. The CLI's own verb for the first backup
	// set, or the web UI; either names the step.
	namesTheNextStep = regexp.MustCompile(`(?i)backup-set create|add .*backup set|next step|web ui|web interface`)
)

func runOnAFreshInstall(t *testing.T, verb string) (code int, stdout, stderr string) {
	t.Helper()
	configDir := t.TempDir()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code = run([]string{verb, "--config", configDir})
		})
	})
	return code, stdout, stderr
}

func TestRun_StatusOnAFreshInstallIsAFirstRunNoticeNotAFailure(t *testing.T) {
	code, stdout, stderr := runOnAFreshInstall(t, "status")

	if code != 0 {
		t.Errorf("status on a fresh install exited %d, want 0: there is no configured backup set to be unhealthy, so there is nothing to fail about\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "no configuration file") {
		t.Errorf("status reported the absent configuration as a failure on stderr:\n%s", stderr)
	}
	if !saysFirstRun.MatchString(stdout) {
		t.Errorf("status said nothing about this being a fresh install\nstdout:\n%s", stdout)
	}
	if !namesTheNextStep.MatchString(stdout) {
		t.Errorf("status did not name the next step (the first backup set)\nstdout:\n%s", stdout)
	}
}

func TestRun_CheckOnAFreshInstallIsAFirstRunNoticeNotAFailure(t *testing.T) {
	code, stdout, stderr := runOnAFreshInstall(t, "check")

	if code != 0 {
		t.Errorf("check on a fresh install exited %d, want 0: a deployment with a writable, empty configuration directory can start, which is the question check answers\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "no configuration file") {
		t.Errorf("check reported the absent configuration as a failure on stderr:\n%s", stderr)
	}
	if !saysFirstRun.MatchString(stdout) {
		t.Errorf("check said nothing about this being a fresh install\nstdout:\n%s", stdout)
	}
	if !namesTheNextStep.MatchString(stdout) {
		t.Errorf("check did not name the next step (the first backup set)\nstdout:\n%s", stdout)
	}
}

// check on a fresh install whose configuration directory cannot be
// written is the one real thing to fix, so it is named and fails.
func TestRun_CheckOnAFreshInstallWithAnUnwritableConfigDirectoryNamesIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere, so the directory cannot be made unwritable")
	}
	parent := t.TempDir()
	configDir := filepath.Join(parent, "config")
	if err := os.Mkdir(configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })

	var code int
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { code = run([]string{"check", "--config", configDir}) })
	})
	if code != exitFailure {
		t.Errorf("check = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "not writable") || !strings.Contains(stderr, configDir) {
		t.Errorf("check did not name the unwritable directory:\n%s", stderr)
	}
}
