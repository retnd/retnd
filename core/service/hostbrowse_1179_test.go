package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
)

// Issue #1179: the folder browser behind "Folder under the staging root on the host".
//
// This is a server-side read of the host's file system, so the tests hold the
// four things that make it safe before they hold that it works: it only reads,
// it only reaches below the root the deployment allows, a name that climbs out
// is refused, and a symbolic link cannot be used to step outside.

// browseFixture is a service whose staging root holds:
//
//	root/alpha/inner/     root/beta/     root/notes.txt (a file)
//	root/escape -> <outside dir>         root/inside -> root/alpha
func browseFixture(t *testing.T) (b *BackupService, root, outside string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "staging")
	outside = filepath.Join(t.TempDir(), "outside")
	for _, d := range []string{filepath.Join(root, "alpha", "inner"), filepath.Join(root, "beta"), filepath.Join(outside, "secret-dir")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: func(c *config.Config) { c.Assurance.StagingRoot = root }})
	return f.b, root, outside
}

func names(d HostDirectory) []string {
	out := []string{}
	for _, e := range d.Entries {
		out = append(out, e.Name)
	}
	return out
}

func TestBrowseHostDirectory_1179_ListsOnlyFoldersAtTheRoot(t *testing.T) {
	b, root, _ := browseFixture(t)
	got, err := b.BrowseHostDirectory(context.Background(), "")
	if err != nil {
		t.Fatalf("listing the root: %v", err)
	}
	if got.Root != root || got.Path != "" || got.Parent != nil {
		t.Errorf("root=%q path=%q parent=%v, want %q, empty, nil", got.Root, got.Path, got.Parent, root)
	}
	// Folders only, sorted: no file, and no symbolic link (a link is how a
	// listing would otherwise invite a step out of the root).
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(names(got), want) {
		t.Errorf("entries = %v, want %v", names(got), want)
	}
	if got.Entries[0].Path != "alpha" {
		t.Errorf("entry path = %q, want the name relative to the root", got.Entries[0].Path)
	}
}

func TestBrowseHostDirectory_1179_ListsASubFolderAndNamesItsParent(t *testing.T) {
	b, _, _ := browseFixture(t)
	got, err := b.BrowseHostDirectory(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "alpha" || got.Parent == nil || *got.Parent != "" {
		t.Errorf("path=%q parent=%v, want alpha and a parent of the root (empty)", got.Path, got.Parent)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "inner" || got.Entries[0].Path != "alpha/inner" {
		t.Errorf("entries = %+v, want the one folder alpha/inner", got.Entries)
	}
	deep, err := b.BrowseHostDirectory(context.Background(), "alpha/inner")
	if err != nil {
		t.Fatal(err)
	}
	if deep.Parent == nil || *deep.Parent != "alpha" {
		t.Errorf("parent of alpha/inner = %v, want alpha", deep.Parent)
	}
}

func TestBrowseHostDirectory_1179_RefusesAnythingThatLeavesTheRoot(t *testing.T) {
	b, _, outside := browseFixture(t)
	for _, rel := range []string{
		"..", "../", "../outside", "alpha/..", "alpha/../..", "alpha/../../x", ".", "alpha/.",
		"/", "/etc", outside, "alpha//inner", "alpha/", "./alpha", "alpha\\..\\..", "alpha\x00",
	} {
		got, err := b.BrowseHostDirectory(context.Background(), rel)
		if !errors.Is(err, ErrHostBrowseInvalid) {
			t.Errorf("%q: err = %v, want ErrHostBrowseInvalid", rel, err)
		}
		if len(got.Entries) != 0 || got.Root != "" {
			t.Errorf("%q: a refused name still returned a listing: %+v", rel, got)
		}
	}
}

func TestBrowseHostDirectory_1179_ASymbolicLinkCannotStepOutOrAside(t *testing.T) {
	b, _, outside := browseFixture(t)
	for _, rel := range []string{"escape", "escape/secret-dir", "inside", "inside/inner"} {
		got, err := b.BrowseHostDirectory(context.Background(), rel)
		if err == nil {
			t.Errorf("%q: a path through a symbolic link was listed: %+v", rel, got)
			continue
		}
		if !errors.Is(err, ErrHostBrowseInvalid) {
			t.Errorf("%q: err = %v, want ErrHostBrowseInvalid", rel, err)
		}
		if strings.Contains(err.Error(), outside) {
			t.Errorf("%q: the refusal names the outside path: %v", rel, err)
		}
	}
}

func TestBrowseHostDirectory_1179_AMissingFolderOrAFileIsNotFound(t *testing.T) {
	b, _, _ := browseFixture(t)
	for _, rel := range []string{"nope", "alpha/nope", "notes.txt"} {
		if _, err := b.BrowseHostDirectory(context.Background(), rel); !errors.Is(err, ErrHostBrowseNotFound) {
			t.Errorf("%q: err = %v, want ErrHostBrowseNotFound", rel, err)
		}
	}
}

func snapshotTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		out = append(out, fmt.Sprintf("%s %v %d %d", p, info.Mode(), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	sort.Strings(out)
	return out
}

func TestBrowseHostDirectory_1179_OnlyReads(t *testing.T) {
	b, root, outside := browseFixture(t)
	before := append(snapshotTree(t, root), snapshotTree(t, outside)...)
	for _, rel := range []string{"", "alpha", "alpha/inner", "escape", "../x", "nope"} {
		_, _ = b.BrowseHostDirectory(context.Background(), rel)
	}
	after := append(snapshotTree(t, root), snapshotTree(t, outside)...)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("browsing changed the file system\nbefore: %v\n after: %v", before, after)
	}
}

func TestBrowseHostDirectory_1179_ARootThatDoesNotExistYetIsEmptyAndIsNotCreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-yet")
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: func(c *config.Config) { c.Assurance.StagingRoot = root }})
	got, err := f.b.BrowseHostDirectory(context.Background(), "")
	if err != nil {
		t.Fatalf("a staging root nobody has used yet is an empty folder, not an error: %v", err)
	}
	if len(got.Entries) != 0 || got.Root != root {
		t.Errorf("listing = %+v, want empty at %q", got, root)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("browsing created the staging root (Lstat err = %v)", err)
	}
	if _, err := f.b.BrowseHostDirectory(context.Background(), "sub"); !errors.Is(err, ErrHostBrowseNotFound) {
		t.Errorf("a sub-folder of a root that does not exist: err = %v, want ErrHostBrowseNotFound", err)
	}
}

func TestBrowseHostDirectory_1179_ALargeFolderIsCutAndSaysSo(t *testing.T) {
	b, root, _ := browseFixture(t)
	for i := 0; i < HostDirectoryLimit+5; i++ {
		if err := os.Mkdir(filepath.Join(root, "beta", fmt.Sprintf("d%05d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.BrowseHostDirectory(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != HostDirectoryLimit || !got.Truncated {
		t.Errorf("len=%d truncated=%v, want %d and true: a listing that silently drops folders reads as complete", len(got.Entries), got.Truncated, HostDirectoryLimit)
	}
	small, _ := b.BrowseHostDirectory(context.Background(), "alpha")
	if small.Truncated {
		t.Error("a small folder reports truncated")
	}
}

func TestBrowseHostDirectory_1179_ARootThatFailsCustodyIsUnavailable(t *testing.T) {
	bad := t.TempDir()
	if err := os.Chmod(bad, 0o777); err != nil {
		t.Fatal(err)
	}
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: func(c *config.Config) { c.Assurance.StagingRoot = bad }})
	if _, err := f.b.BrowseHostDirectory(context.Background(), ""); !errors.Is(err, ErrHostBrowseUnavailable) {
		t.Errorf("err = %v, want ErrHostBrowseUnavailable: recovery refuses a root another account can write to, and so does the browser", err)
	}
}
