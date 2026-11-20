package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1606 from a terminal. A hold has a holder now: --take prints the
// token it was given, --take --token renews that holder and nothing else,
// and --release --token gives back that holder alone. A renewal whose
// holder was released or lapsed is refused, says so in words a script
// author can act on, and leaves nothing held. The fake engine serves all of
// it off the real BackupService.

const editHold1606Set = "production/postgres-primary"

var tokenLine1606 = regexp.MustCompile(`(?m)^\s*token: (\S+)$`)

// takeFromCLI runs --take and returns the token it printed.
func takeFromCLI(t *testing.T, cliConfig string) string {
	t.Helper()
	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"backup-set", "edit-hold", editHold1606Set, "--take", "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("--take = %d, want 0; it printed %q", code, stdout)
	}
	m := tokenLine1606.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("--take printed no `token:` line, so a script has nothing to renew or release its own hold with:\n%s", stdout)
	}
	return m[1]
}

func TestBackupSetEditHold1606_TakePrintsTheHolderToken(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)
	token := takeFromCLI(t, cliConfig)

	if _, err := e.svc.RenewBackupSetEdit(t.Context(), editHold1606Set, token); err != nil {
		t.Errorf("the engine does not know the token --take printed (%q): %v", token, err)
	}
}

func TestBackupSetEditHold1606_TakeWithATokenRenewsThatHolder(t *testing.T) {
	cliConfig, _ := oneServedDeployment(t)
	token := takeFromCLI(t, cliConfig)

	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"backup-set", "edit-hold", editHold1606Set, "--take", "--token", token, "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("--take --token with a live token = %d, want 0; it printed %q", code, stdout)
	}
	m := tokenLine1606.FindStringSubmatch(stdout)
	if m == nil || m[1] != token {
		t.Errorf("a renewal must print the token it renewed (%q):\n%s", token, stdout)
	}
	if !strings.Contains(stdout, "held: true") {
		t.Errorf("the set is not reported held after a renewal:\n%s", stdout)
	}
}

// The issue's race from a script: the hold was released (by the script's
// own --release, an operator, or the lease) before the renewal arrived.
func TestBackupSetEditHold1606_ARenewalAfterTheReleaseIsRefusedAndHoldsNothing(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)
	token := takeFromCLI(t, cliConfig)
	if err := e.svc.EndBackupSetEdit(t.Context(), editHold1606Set, token); err != nil {
		t.Fatalf("releasing on the engine: %v", err)
	}

	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code = run([]string{"backup-set", "edit-hold", editHold1606Set, "--take", "--token", token, "--config", cliConfig})
		})
	})
	if code == 0 {
		t.Fatalf("a renewal of a released hold exited 0, so a script would carry on editing under no hold; it printed %q", stdout)
	}
	if code == exitUsage {
		t.Fatalf("a renewal of a released hold exited %d (usage), want the refusal: %s", code, stderr)
	}
	if !strings.Contains(stderr, "released or has lapsed") || !strings.Contains(stderr, "--take") {
		t.Errorf("the refusal must say the hold is gone and how to take a new one:\n%s", stderr)
	}
	state, err := e.svc.BackupSetEditState(t.Context(), editHold1606Set)
	if err != nil {
		t.Fatalf("reading the hold back: %v", err)
	}
	if state.Held {
		t.Error("the refused renewal left the set held on the engine")
	}
}

// Two holders: a script and a browser tab, say. The script giving back
// its own hold must not give back the tab's.
func TestBackupSetEditHold1606_ReleaseWithATokenLeavesAnotherHolder(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)
	other, err := e.svc.BeginBackupSetEdit(t.Context(), editHold1606Set)
	if err != nil {
		t.Fatalf("the other holder's take: %v", err)
	}
	mine := takeFromCLI(t, cliConfig)

	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"backup-set", "edit-hold", editHold1606Set, "--release", "--token", mine, "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("--release --token = %d, want 0; it printed %q", code, stdout)
	}
	if !strings.Contains(stdout, "held: true") {
		t.Errorf("after giving back one of two holds the set must still read held:\n%s", stdout)
	}
	if _, err := e.svc.RenewBackupSetEdit(t.Context(), editHold1606Set, other.Token); err != nil {
		t.Errorf("the other holder's renewal after the script released its own: %v", err)
	}
	if _, err := e.svc.RenewBackupSetEdit(t.Context(), editHold1606Set, mine); !errors.Is(err, service.ErrEditHoldGone) {
		t.Errorf("the script's own token still renews after --release --token: err = %v", err)
	}
}

// --release with no token is the operator's "clear whatever is holding
// this set", as it was before tokens.
func TestBackupSetEditHold1606_ReleaseWithNoTokenGivesBackEveryHold(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)
	for i := 0; i < 2; i++ {
		if _, err := e.svc.BeginBackupSetEdit(t.Context(), editHold1606Set); err != nil {
			t.Fatalf("take %d: %v", i+1, err)
		}
	}
	var code int
	stdout := captureStdout(t, func() {
		code = run([]string{"backup-set", "edit-hold", editHold1606Set, "--release", "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("--release = %d, want 0; it printed %q", code, stdout)
	}
	if !strings.Contains(stdout, "held: false") {
		t.Errorf("a release with no token must give back every hold:\n%s", stdout)
	}
}

func TestBackupSetEditHold1606_TokenOnItsOwnIsAUsageError(t *testing.T) {
	cliConfig, _ := oneServedDeployment(t)
	if got := run([]string{"backup-set", "edit-hold", editHold1606Set, "--token", "abc", "--config", cliConfig}); got != exitUsage {
		t.Errorf("--token with neither --take nor --release = %d, want %d: a report does not act as any holder", got, exitUsage)
	}
}

// The synopsis line itself stays as it was, because retnd-tests pins it
// whole (suites/cli/cases/global/g3-usage-lists-every-command.yaml); the
// token is in the description under it.
func TestBackupSetEditHold1606_UsageNamesTheToken(t *testing.T) {
	text := captureStderr(t, usage)
	if !strings.Contains(text, "--take --token T renews that hold") {
		t.Errorf("usage() does not say what --token does on `backup-set edit-hold`")
	}
}
