package service

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
)

// #1246 review: in the packaged layout the stage directory and its .superseded
// sibling are two mounts, so every move aside is the copy path. These hold
// that copy to what the stage directory can contain, since anyone who can
// write the host's workflows directory can put anything there.

// crossMountStore is a store whose renames fail the way a rename between two
// mounts does, with both directories present.
func crossMountStore(t *testing.T) (validatorStore, string, string) {
	t.Helper()
	store, rootPath := newTestStore(t)
	store.rename = func(src, dst string) error {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: syscall.EXDEV}
	}
	stage := filepath.Join(rootPath, config.CustomValidatorDir)
	aside := filepath.Join(rootPath, config.CustomValidatorDir+supersededSuffix)
	for _, d := range []string{stage, aside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return store, stage, aside
}

// reconcileWithin runs Reconcile with a deadline, so an entry that blocks it
// fails the test rather than hanging the suite.
func reconcileWithin(t *testing.T, store validatorStore, declared map[string]bool) ([]string, error) {
	t.Helper()
	type result struct {
		skipped []string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		skipped, err := store.Reconcile(declared)
		done <- result{skipped, err}
	}()
	select {
	case r := <-done:
		return r.skipped, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("Reconcile did not return within 5s; something in the stage directory blocked it while configMu would be held")
		return nil, nil
	}
}

func TestValidatorStore_AMoveAcrossMountsStillMoves(t *testing.T) {
	store, stage, aside := crossMountStore(t)
	if _, err := store.Write("v.local.sh", []byte(goodValidator)); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveAside("v.local.sh"); err != nil {
		t.Fatalf("MoveAside across mounts: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(stage, "v.local.sh")); !os.IsNotExist(err) {
		t.Errorf("the script is still in the stage directory: %v", err)
	}
	moved, err := os.ReadFile(filepath.Join(aside, "v.local.sh"))
	if err != nil || string(moved) != goodValidator {
		t.Errorf("the superseded copy = %q, %v", moved, err)
	}
}

func TestValidatorStore_ACrossMountMoveSkipsWhatIsNotARegularFile(t *testing.T) {
	store, stage, aside := crossMountStore(t)
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("runner-token-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(stage, "link.local.sh")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(stage, "pipe.local.sh"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(stage, "dir.local.sh"), 0o750); err != nil {
		t.Fatal(err)
	}

	skipped, err := reconcileWithin(t, store, map[string]bool{})
	if err != nil {
		t.Fatalf("Reconcile refused over entries it should leave alone, so every later upload would be refused too: %v", err)
	}
	for _, name := range []string{"link.local.sh", "pipe.local.sh", "dir.local.sh"} {
		if !strings.Contains(strings.Join(skipped, " "), name) {
			t.Errorf("%s is not reported as skipped (skipped = %v)", name, skipped)
		}
		if _, err := os.Lstat(filepath.Join(aside, name)); !os.IsNotExist(err) {
			t.Errorf("%s reached the superseded directory: %v", name, err)
		}
	}
	entries, _ := os.ReadDir(aside)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		body, _ := os.ReadFile(filepath.Join(aside, e.Name()))
		if strings.Contains(string(body), "runner-token-material") {
			t.Errorf("a symlink's target was copied into %s", e.Name())
		}
	}
}

func TestValidatorStore_ACrossMountMoveRefusesAFileOverTheBound(t *testing.T) {
	store, stage, aside := crossMountStore(t)
	big := make([]byte, MaxCustomValidatorScriptBytes+1)
	if err := os.WriteFile(filepath.Join(stage, "big.local.sh"), big, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveAside("big.local.sh"); err == nil {
		t.Error("a file over the script bound was copied aside")
	}
	if _, err := os.Lstat(filepath.Join(aside, "big.local.sh")); !os.IsNotExist(err) {
		t.Errorf("a partial copy was left in the superseded directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(stage, "big.local.sh")); err != nil {
		t.Errorf("the original is gone: %v", err)
	}
	skipped, err := reconcileWithin(t, store, map[string]bool{})
	if err != nil || len(skipped) != 1 {
		t.Errorf("Reconcile = %v, %v; an oversized stray is skipped and named, not a reason to refuse every upload", skipped, err)
	}
}

func TestValidatorStore_AStaleTemporaryFileIsRemovedNotKept(t *testing.T) {
	store, stage, aside := crossMountStore(t)
	stale := filepath.Join(stage, ".retnd-validator-123.tmp")
	if err := os.WriteFile(stale, []byte("half a script"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileWithin(t, store, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("a crash-left temporary file is still in the stage directory: %v", err)
	}
	if entries, _ := os.ReadDir(aside); len(entries) != 0 {
		t.Errorf("a crash-left temporary file was kept as a superseded script: %v", entries)
	}
}

func TestValidatorStore_ACopyWhoseSourceCannotBeRemovedLeavesOneCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes through a 0500 directory")
	}
	store, stage, aside := crossMountStore(t)
	if _, err := store.Write("v.local.sh", []byte(goodValidator)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stage, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stage, 0o750) })

	if err := store.MoveAside("v.local.sh"); err == nil {
		t.Error("a move whose source could not be removed reported success")
	}
	if _, err := os.Lstat(filepath.Join(stage, "v.local.sh")); err != nil {
		t.Errorf("the original is gone: %v", err)
	}
	if entries, _ := os.ReadDir(aside); len(entries) != 0 {
		t.Errorf("the copy was kept beside the original, so the script now exists twice: %v", entries)
	}
}

func TestValidatorStore_UnwritableNamesASupersededDirectoryItCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a 0500 directory")
	}
	store, rootPath := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(rootPath, config.CustomValidatorDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rootPath, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rootPath, 0o700) })

	why := store.Unwritable()
	if !strings.Contains(why, config.CustomValidatorDir+supersededSuffix) {
		t.Errorf("Unwritable() = %q; with no writable %s a removal fails after the card offered it", why, config.CustomValidatorDir+supersededSuffix)
	}
}
