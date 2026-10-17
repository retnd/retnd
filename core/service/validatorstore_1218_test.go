package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/workflow"
)

// #1218: the script store on its own. None of these needs a BackupService, a
// journal or a configuration file.

func newTestStore(t *testing.T) (validatorStore, string) {
	t.Helper()
	_, _, rootPath := openWorkflowSaveService(t)
	root, err := workflow.NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	return newValidatorStore(root), rootPath
}

func TestValidatorStore_WriteReadAndDescribe(t *testing.T) {
	store, rootPath := newTestStore(t)
	if _, err := store.Write("x.local.sh", []byte(goodValidator)); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read("x.local.sh", 1<<20)
	if err != nil || string(got) != goodValidator {
		t.Fatalf("Read = %q, %v", got, err)
	}
	info, ok := store.Describe("x.local.sh", 1<<20)
	sum := sha256.Sum256([]byte(goodValidator))
	if !ok || info.SHA256 != hex.EncodeToString(sum[:]) || info.Size != int64(len(goodValidator)) || info.ModTime.IsZero() {
		t.Errorf("Describe = %+v, %v", info, ok)
	}
	if _, err := os.Stat(filepath.Join(rootPath, config.CustomValidatorDir, "x.local.sh")); err != nil {
		t.Errorf("the script is not where discovery looks: %v", err)
	}
	if _, ok := store.Describe("nope.local.sh", 1<<20); ok {
		t.Error("a script that is not there was described")
	}
}

func TestValidatorStore_WriteReturnsTheWayBack(t *testing.T) {
	store, _ := newTestStore(t)
	if _, err := store.Write("x.local.sh", []byte(goodValidator)); err != nil {
		t.Fatal(err)
	}
	restore, err := store.Write("x.local.sh", []byte(goodValidator+"echo new\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Read("x.local.sh", 1<<20); string(got) != goodValidator {
		t.Errorf("after restore = %q", got)
	}

	fresh, err := store.Write("y.local.sh", []byte(goodValidator))
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("y.local.sh", 1<<20); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("restoring a first write left the script behind: %v", err)
	}
}

func TestValidatorStore_MoveAsideKeepsEveryCopy(t *testing.T) {
	store, rootPath := newTestStore(t)
	for i := 0; i < 2; i++ {
		if _, err := store.Write("x.local.sh", []byte(goodValidator)); err != nil {
			t.Fatal(err)
		}
		if err := store.MoveAside("x.local.sh"); err != nil {
			t.Fatal(err)
		}
	}
	aside := filepath.Join(rootPath, config.CustomValidatorDir+".superseded")
	for _, name := range []string{"x.local.sh", "x.local.sh.1"} {
		if _, err := os.Stat(filepath.Join(aside, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := store.MoveAside("x.local.sh"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("moving a script that is gone = %v", err)
	}
}

func TestValidatorStore_ReconcileMovesWhatIsNotDeclaredAndCountsFiles(t *testing.T) {
	store, rootPath := newTestStore(t)
	for _, f := range []string{"keep.local.sh", "stray.local.sh"} {
		if _, err := store.Write(f, []byte(goodValidator)); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(rootPath, config.CustomValidatorDir)
	if err := os.WriteFile(filepath.Join(dir, "editor.swp"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if n := store.EntryCount(); n != 3 {
		t.Errorf("EntryCount = %d, want 3", n)
	}
	if err := store.Reconcile(map[string]bool{"keep.local.sh": true}); err != nil {
		t.Fatal(err)
	}
	if n := store.EntryCount(); n != 1 {
		t.Errorf("EntryCount after = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(rootPath, config.CustomValidatorDir+".superseded", "stray.local.sh")); err != nil {
		t.Errorf("the stray script was not kept aside: %v", err)
	}
}

func TestValidatorStore_RefusalNamesAStageDirectoryDiscoveryWouldRefuse(t *testing.T) {
	store, rootPath := newTestStore(t)
	if got := store.Refusal(); got != "" {
		t.Errorf("no directory yet = %q", got)
	}
	dir := filepath.Join(rootPath, config.CustomValidatorDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := store.Refusal(); got != "" {
		t.Errorf("a good directory = %q", got)
	}
	if err := os.Symlink(rootPath, filepath.Join(dir, "link.local.sh")); err != nil {
		t.Skip("cannot make a symlink here")
	}
	if got := store.Refusal(); got == "" {
		t.Error("a directory holding a link was not refused")
	}
}

func TestValidatorStore_AZeroRootStoresNothingAndSaysSo(t *testing.T) {
	store := newValidatorStore(workflow.Root{})
	if _, ok := store.Describe("x.local.sh", 1<<20); ok {
		t.Error("a zero root described a script")
	}
	if err := store.MoveAside("x.local.sh"); err != nil {
		t.Errorf("MoveAside on a zero root = %v", err)
	}
	if store.EntryCount() != 0 || store.Refusal() != "" {
		t.Error("a zero root reported entries or a refusal")
	}
}

// Nothing in core/service reaches into the stage directory except through the
// store: the directory's name is spelled in validatorstore.go and nowhere else
// in the package's own code.
func TestNothingOutsideTheStoreNamesTheStageDirectory(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "validatorstore.go" {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, "CustomValidatorDir") && (strings.Contains(line, "filepath.Join") || strings.Contains(line, "ReadDir") || strings.Contains(line, "ResolveStage")) {
				t.Errorf("%s:%d reaches into the stage directory without the store: %s", f, i+1, trimmed)
			}
		}
	}
}
