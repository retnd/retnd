package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
)

// #1246: a Docker host install mounts its workflows directory at /workflows
// and has no workflows block in config.yaml, and custom validators refused
// there with "no workflow root is configured". These run the same
// deployment against a directory of the test's own (stubDefaultWorkflowRoot),
// so none of them depends on a real /workflows.

// rootDefault is a default workflow root at dir, present or not, for a test
// to hand to OpenWith rather than looking at a real /workflows.
func rootDefault(dir string, there bool) WorkflowRootDefault {
	return WorkflowRootDefault{Dir: dir, Present: func(d string) bool { return there && d == dir }}
}

// openWithDefault opens configPath the way the engine's own entry points do,
// with def as its default root.
func openWithDefault(t *testing.T, configPath string, def WorkflowRootDefault) *BackupService {
	t.Helper()
	svc, cleanup, err := OpenWith(context.Background(), configPath, OpenOptions{WorkflowRoot: def})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.Close()
		_ = cleanup()
	})
	return svc
}

// openNoWorkflowsService is openWorkflowSaveService's deployment written the
// way an install that never ran `settings workflow patch --root` has it: no
// workflows block at all, opened with a default root (present or not) at a
// directory of the test's own.
func openNoWorkflowsService(t *testing.T, mounted bool) (*BackupService, string, string) {
	t.Helper()

	dir := symlinkFreeTempDir(t)
	root := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

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

	return openWithDefault(t, configPath, rootDefault(root, mounted)), configPath, root
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
	// One shape for every client: the absolute directory an upload lands in.
	if want := filepath.Join(root, config.CustomValidatorDir); list.ScriptsDir != want {
		t.Errorf("list scripts dir = %q, want %q", list.ScriptsDir, want)
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
	_, configPath, root := openWorkflowSaveService(t)
	svc := openWithDefault(t, configPath, rootDefault(root, true))

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

	check(openWithDefault(t, configPath, rootDefault(root, true)), "after a restart")
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

// The default is the running process's, never the configuration's: a copy of
// the running config that grew a per-set hook block with no root in the file
// has to be refused exactly as the file would be on the next start.
func TestWorkflowRootDefault_TheRunningConfigNeverCarriesTheDefault(t *testing.T) {
	svc, _, _ := openNoWorkflowsService(t, true)
	running := *svc.state.Load().inner.Config
	if running.Workflows.Root != "" || running.WorkflowsConfigured() {
		t.Fatalf("the running config carries a root (%q, configured %v); the default belongs to the service", running.Workflows.Root, running.WorkflowsConfigured())
	}
	running.Sources = append([]config.Source(nil), running.Sources...)
	running.Sources[0].BackupSets = append([]config.BackupSet(nil), running.Sources[0].BackupSets...)
	running.Sources[0].BackupSets[0].Workflow = &config.SetWorkflow{BeforeDir: "alpha-before"}
	if err := running.Validate(); err == nil || !strings.Contains(err.Error(), "workflows.root") {
		t.Errorf("Validate() = %v; a per-set hook block with no root in the file has to be refused", err)
	}
}

func TestWorkflowRootDefault_OpenWithNoDefaultLooksForNone(t *testing.T) {
	asked := false
	got := ResolveWorkflowRoot(&config.Config{}, WorkflowRootDefault{})
	if got.Path != "" || got.Default {
		t.Errorf("a zero default resolved %+v", got)
	}
	got = ResolveWorkflowRoot(&config.Config{Workflows: config.Workflows{Root: "/srv/hooks"}},
		WorkflowRootDefault{Dir: "/workflows", Present: func(string) bool { asked = true; return true }})
	if got.Path != "/srv/hooks" || got.Default || asked {
		t.Errorf("an explicit root resolved %+v (probed %v)", got, asked)
	}
	got = ResolveWorkflowRoot(&config.Config{Workflows: config.Workflows{Disabled: true}},
		WorkflowRootDefault{Dir: "/workflows", Present: func(string) bool { return true }})
	if got.Path != "" || !got.Disabled {
		t.Errorf("a cleared root resolved %+v", got)
	}
	got = ResolveWorkflowRoot(&config.Config{}, WorkflowRootDefault{Dir: "/workflows", Present: func(string) bool { return true }})
	if got.Path != "/workflows" || !got.Default {
		t.Errorf("a mounted default resolved %+v", got)
	}
}

func TestWorkflowRootDefault_AClearedRootCanGoBackToTheDefault(t *testing.T) {
	ctx := context.Background()
	_, configPath, root := openWorkflowSaveService(t)
	svc := openWithDefault(t, configPath, rootDefault(root, true))

	off := true
	if _, err := svc.UpdateWorkflowSettings(ctx, UpdateWorkflowSettingsRequest{Disabled: &off}); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	if s, _ := svc.WorkflowSettings(ctx); !s.Disabled || s.Configured {
		t.Errorf("after disable: %+v", s)
	}
	on := false
	s, err := svc.UpdateWorkflowSettings(ctx, UpdateWorkflowSettingsRequest{Disabled: &on})
	if err != nil {
		t.Fatalf("enabling: %v", err)
	}
	if s.Disabled || !s.RootDefault || s.Root != root {
		t.Errorf("after enable: %+v; turning workflows back on with no root has to land on the default", s)
	}
	raw, _ := os.ReadFile(configPath)
	if strings.Contains(string(raw), "disabled") || strings.Contains(string(raw), "root:") {
		t.Errorf("config.yaml still names a root or disabled after enable:\n%s", raw)
	}
}

func TestWorkflowRootDefault_DisablingWithARootIsRefused(t *testing.T) {
	svc, _, _ := openWorkflowSaveService(t)
	off, root := true, "/srv/hooks"
	_, err := svc.UpdateWorkflowSettings(context.Background(), UpdateWorkflowSettingsRequest{Disabled: &off, Root: &root})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("disabled with a root = %v; that is two statements that cannot both be meant", err)
	}
}
