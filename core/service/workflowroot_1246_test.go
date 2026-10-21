package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
)

// #1246: a Docker host install mounts its workflows directory at /workflows
// and has no workflows block in config.yaml, and custom validators refused
// there with "no workflow root is configured". These run the same
// deployment against a directory of the test's own (stubDefaultWorkflowRoot),
// so none of them depends on a real /workflows.

// stubDefaultWorkflowRoot points the default root at dir and says whether it
// is there, for the rest of the test.
func stubDefaultWorkflowRoot(t *testing.T, dir string, there bool) {
	t.Helper()
	prevRoot, prevPresent := defaultWorkflowRoot, workflowRootPresent
	defaultWorkflowRoot = dir
	workflowRootPresent = func(d string) bool { return there && d == dir }
	t.Cleanup(func() { defaultWorkflowRoot, workflowRootPresent = prevRoot, prevPresent })
}

// openNoWorkflowsService is openWorkflowSaveService's deployment written the
// way an install that never ran `settings workflow patch --root` has it: no
// workflows block at all. The default root is stubbed (present or not)
// BEFORE the service opens, because opening is when it is resolved.
func openNoWorkflowsService(t *testing.T, mounted bool) (*BackupService, string, string) {
	t.Helper()

	dir := symlinkFreeTempDir(t)
	root := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stubDefaultWorkflowRoot(t, root, mounted)

	remote := filepath.Join(dir, "remote")
	local := filepath.Join(dir, "local")
	for _, d := range []string{remote, local} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	content := "poll_interval: 15m\n" +
		"state:\n" +
		"  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n" +
		"  - id: production\n" +
		"    backup_sets:\n" +
		"      - id: alpha\n" +
		"        remote:\n" +
		"          type: local\n" +
		"        remote_path: " + remote + "\n" +
		"        local_path: " + local + "\n" +
		"        include:\n" +
		"          - \"*.dump\"\n" +
		"        completion:\n" +
		"          strategy: rename\n" +
		"        stale_after: 24h\n" +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, cleanup, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.Close()
		_ = cleanup()
	})
	return svc, configPath, root
}

func TestWorkflowRootDefault_AMountedWorkflowsWithNoSectionCanKeepAValidator(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openNoWorkflowsService(t, true)

	list, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !list.Available || list.UnavailableReason != "" {
		t.Errorf("list says unavailable (%q) on a deployment with %s mounted", list.UnavailableReason, root)
	}
	if list.WorkflowRoot != root || !list.WorkflowRootDefault {
		t.Errorf("list root = %q (default %v), want %q reported as the default", list.WorkflowRoot, list.WorkflowRootDefault, root)
	}

	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "manifest", Pattern: "^MANIFEST", Script: goodValidator}); err != nil {
		t.Fatalf("Create on a default root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, config.CustomValidatorDir, "manifest.local.sh")); err != nil {
		t.Errorf("the script is not under the default root: %v", err)
	}
}

func TestWorkflowRootDefault_SettingsReportTheDefaultRatherThanNotConfigured(t *testing.T) {
	svc, _, root := openNoWorkflowsService(t, true)

	s, err := svc.WorkflowSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Configured || s.Root != root || !s.RootDefault {
		t.Errorf("settings = configured %v, root %q, default %v; want the mounted %q as the default", s.Configured, s.Root, s.RootDefault, root)
	}
}

func TestWorkflowRootDefault_AnExplicitlyClearedRootStillRefusesWithAReason(t *testing.T) {
	ctx := context.Background()
	svc, configPath, root := openWorkflowSaveService(t)
	stubDefaultWorkflowRoot(t, root, true)

	clear := ""
	if _, err := svc.UpdateWorkflowSettings(ctx, UpdateWorkflowSettingsRequest{Root: &clear}); err != nil {
		t.Fatalf("clearing the root: %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "disabled: true") {
		t.Errorf("a cleared root left nothing in config.yaml to say so, so the next start would pick the default up again:\n%s", raw)
	}

	check := func(svc *BackupService, when string) {
		t.Helper()
		_, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "x", Pattern: "x", Script: goodValidator})
		if !errors.Is(err, ErrValidatorsUnavailable) || !strings.Contains(err.Error(), "workflows.disabled") {
			t.Errorf("%s: Create = %v; want a refusal that says workflows were turned off", when, err)
		}
		list, err := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
		if err != nil || list.Available || !strings.Contains(list.UnavailableReason, "workflows.disabled") {
			t.Errorf("%s: list = available %v, reason %q, %v", when, list.Available, list.UnavailableReason, err)
		}
		if s, _ := svc.WorkflowSettings(ctx); s.Configured || s.RootDefault {
			t.Errorf("%s: settings = %+v; a cleared root is not a default one", when, s)
		}
	}
	check(svc, "after the patch")

	again, cleanup, err := Open(ctx, configPath)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		_ = again.Close()
		_ = cleanup()
	})
	check(again, "after a restart")
}

func TestWorkflowRootDefault_AnUpgradeWithoutTheSectionEndsUpWithAUsableRoot(t *testing.T) {
	ctx := context.Background()
	svc, configPath, root := openNoWorkflowsService(t, true)

	// A write that needs a root in the file (a deployment-wide hook
	// variable) writes the default one there, so the file says what the
	// deployment does rather than being refused over a root it uses.
	if _, err := svc.SetWorkflowEnv(ctx, "", WorkflowEnvVar{Name: "DEPLOYMENT", Value: "nas", HasValue: true}); err != nil {
		t.Fatalf("SetWorkflowEnv on a default root: %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "root: "+root) {
		t.Errorf("config.yaml does not name the root the write depended on:\n%s", raw)
	}
	if s, _ := svc.WorkflowSettings(ctx); s.Root != root || s.RootDefault {
		t.Errorf("settings = root %q default %v; once the file names it, it is configured", s.Root, s.RootDefault)
	}
}

func TestWorkflowRootDefault_NowhereToWriteSaysWhatItTried(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openNoWorkflowsService(t, false)

	_, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "x", Pattern: "x", Script: goodValidator})
	if !errors.Is(err, ErrValidatorsUnavailable) {
		t.Fatalf("Create = %v", err)
	}
	for _, want := range []string{root, "workflows.root"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	list, _ := svc.ListCustomValidators(ctx, CustomValidatorsFilter{})
	if list.Available || !strings.Contains(list.UnavailableReason, root) {
		t.Errorf("list = available %v, reason %q; it should name the directory it looked for", list.Available, list.UnavailableReason)
	}
}

// readOnlyHookTree makes root unwritable the way the :ro mount is, with the
// validator directories beside it writable the way their own mounts are.
func readOnlyHookTree(t *testing.T, root string, writable ...string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes through a 0500 directory, so this cannot stand in for a read-only mount")
	}
	for _, d := range writable {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
}

func TestWorkflowRootDefault_AReadOnlyHookTreeDoesNotStopAValidator(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	readOnlyHookTree(t, root, config.CustomValidatorDir, config.CustomValidatorDir+supersededSuffix)

	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "manifest", Pattern: "^MANIFEST", Script: goodValidator}); err != nil {
		t.Fatalf("Create with a read-only hook tree and a writable validator directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, config.CustomValidatorDir, "manifest.local.sh")); err != nil {
		t.Errorf("the script is not there: %v", err)
	}
	if _, err := svc.RemoveCustomValidator(ctx, "manifest"); err != nil {
		t.Fatalf("Remove with a read-only hook tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, config.CustomValidatorDir+supersededSuffix, "manifest.local.sh")); err != nil {
		t.Errorf("the removed script was not moved aside: %v", err)
	}
}

func TestWorkflowRootDefault_AReadOnlyTreeWithNoValidatorDirectorySaysSo(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	readOnlyHookTree(t, root)

	_, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "x", Pattern: "x", Script: goodValidator})
	if !errors.Is(err, ErrValidatorsUnavailable) {
		t.Fatalf("Create = %v", err)
	}
	for _, want := range []string{filepath.Join(root, config.CustomValidatorDir), "writable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// The validator directory and its .superseded sibling are two mounts in a
// container, and rename(2) between two mounts is EXDEV even on one disk.
func TestWorkflowRootDefault_AMoveAsideAcrossMountsStillMoves(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	prev := renameValidatorFile
	renameValidatorFile = func(src, dst string) error {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { renameValidatorFile = prev })

	if _, err := svc.RemoveCustomValidator(ctx, "v"); err != nil {
		t.Fatalf("Remove across mounts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, config.CustomValidatorDir, "v.local.sh")); !os.IsNotExist(err) {
		t.Errorf("the script is still in the stage directory: %v", err)
	}
	moved, err := os.ReadFile(filepath.Join(root, config.CustomValidatorDir+supersededSuffix, "v.local.sh"))
	if err != nil || string(moved) != goodValidator {
		t.Errorf("the superseded copy = %q, %v", moved, err)
	}
}
