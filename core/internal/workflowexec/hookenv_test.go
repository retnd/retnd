package workflowexec

import (
	"context"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/workflow"
)

// The hook environment, proved the only way it can be: by running an
// operator's script under a real bash and reading what the script saw.
//
// A hook gets the RETND_* built-ins and nothing under the product's former
// name. The former name is never written out in this file: it is assembled
// from pieces, so this test cannot be the thing that keeps it alive.
//
// Everything below goes through the real assembly path --
// workflow.Environment.Resolve, then ProcessEnv and StdinPayload, then
// `bash --noprofile --norc -s` -- because the mistake worth catching is
// not "the map has the right keys": it is a name that survives the map and
// is lost, or invented, by the envelope, the quoting or the baseline
// sanitiser.

// formerPrefix is the prefix hooks used to receive, built from pieces.
func formerPrefix() string { return strings.Join([]string{"BACK", "UPD"}, "") + "_" }

// hookFacts is one step's built-ins, as internal/workflowrun writes them:
// every documented name, with a value distinct enough that a mix-up
// between two of them is visible.
func hookFacts() map[string]string {
	return map[string]string{
		"RETND":                   "1",
		"RETND_RUN_ID":            "run-7",
		"RETND_BACKUP_SET_ID":     "prod/postgres-primary",
		"RETND_BACKUP_SET_NAME":   "postgres-primary",
		"RETND_PHASE":             "after",
		"RETND_STEP_ID":           "step-3",
		"RETND_STEP_NAME":         "20-unmount.local.sh",
		"RETND_STEP_TARGET":       "local",
		workflow.EnvSourceHost:    "db01",
		workflow.EnvSourcePath:    "/srv/pg",
		workflow.EnvDestination:   "/mnt/backups/prod",
		"RETND_WORK_DIR":          "/var/lib/retnd/workflow/run-7",
		"RETND_BACKUP_STATUS":     "failed",
		"RETND_WORKFLOW_STATUS":   "failed",
		"RETND_CLEANUP_STATUS":    "unknown",
		"RETND_BACKUP_ERROR_CODE": "TRANSPORT_LOST",
		"RETND_STARTED_AT":        "2026-01-02T03:04:05Z",
		"RETND_RECOVERY":          "1",
		"RETND_CLEANUP_REASON":    "interrupted_run",
	}
}

// runHook assembles a real hook environment and runs script under a real
// bash, returning what the script printed.
func runHook(t *testing.T, script string) string {
	t.Helper()

	env, err := workflow.NewEnvironment(workflow.SanitizedBaseline())
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	resolved, err := env.Resolve(context.Background(), hookFacts())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	environ, err := ProcessEnv(nil, resolved.Environ())
	if err != nil {
		t.Fatalf("ProcessEnv: %v", err)
	}
	payload, err := StdinPayload(environ, []byte(script))
	if err != nil {
		t.Fatalf("StdinPayload: %v", err)
	}

	stdout, stderr, exit := runUnderRealBash(t, hostileParent(), payload)
	if exit != 0 {
		t.Fatalf("hook exited %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}

	return stdout
}

// A hook reads the current name and sees what this product observed.
func TestAHookReadingTheCurrentNameSeesTheRunsFact(t *testing.T) {
	t.Parallel()

	got := runHook(t, `printf 'status=%s\n' "$RETND_BACKUP_STATUS"`)
	if strings.TrimSpace(got) != "status=failed" {
		t.Fatalf("hook printed %q, want status=failed", got)
	}
}

// A script still reading the former name gets the empty string, and the
// product does not export it behind the operator's back.
func TestAHookReadingTheFormerNameSeesNothing(t *testing.T) {
	t.Parallel()

	former := formerPrefix() + "BACKUP_STATUS"
	got := runHook(t, `printf 'status=[%s]\n' "${`+former+`-}"`)
	if strings.TrimSpace(got) != "status=[]" {
		t.Fatalf("a hook reading $%s printed %q; the former name must not be exported", former, got)
	}
}

// The same property for the whole environment block: every documented
// built-in arrives under its RETND name with its value, and no variable at
// all carries the former prefix or the former bare name.
func TestAHooksEnvironmentHoldsOnlyTheCurrentNames(t *testing.T) {
	t.Parallel()

	bare := strings.TrimSuffix(formerPrefix(), "_")
	printed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(runHook(t, `for n in $(compgen -e); do printf '%s=%s\n' "$n" "${!n}"; done`)), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		printed[name] = value
		if name == bare || strings.HasPrefix(name, formerPrefix()) {
			t.Errorf("the hook's environment carries %s, a name under the former brand", name)
		}
	}

	facts := hookFacts()
	for _, name := range workflow.BuiltinEnvNames() {
		if printed[name] != facts[name] {
			t.Errorf("$%s = %q, want %q", name, printed[name], facts[name])
		}
	}
}
