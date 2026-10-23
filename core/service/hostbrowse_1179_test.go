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
	"unicode/utf8"

	"github.com/retnd/retnd/core/internal/config"
)

// Issue #1179: the read behind GET /api/v1/system/staging-directories.
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

func names(d StagingDirectory) []string {
	out := []string{}
	for _, e := range d.Entries {
		out = append(out, e.Name)
	}
	return out
}

func TestListStagingDirectory_1179_ListsOnlyFoldersAtTheRoot(t *testing.T) {
	b, root, _ := browseFixture(t)
	got, err := b.ListStagingDirectory(context.Background(), "")
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

func TestListStagingDirectory_1179_ListsASubFolderAndNamesItsParent(t *testing.T) {
	b, _, _ := browseFixture(t)
	got, err := b.ListStagingDirectory(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "alpha" || got.Parent == nil || *got.Parent != "" {
		t.Errorf("path=%q parent=%v, want alpha and a parent of the root (empty)", got.Path, got.Parent)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "inner" || got.Entries[0].Path != "alpha/inner" {
		t.Errorf("entries = %+v, want the one folder alpha/inner", got.Entries)
	}
	deep, err := b.ListStagingDirectory(context.Background(), "alpha/inner")
	if err != nil {
		t.Fatal(err)
	}
	if deep.Parent == nil || *deep.Parent != "alpha" {
		t.Errorf("parent of alpha/inner = %v, want alpha", deep.Parent)
	}
}

func TestListStagingDirectory_1179_RefusesAnythingThatLeavesTheRoot(t *testing.T) {
	b, _, outside := browseFixture(t)
	for _, rel := range []string{
		"..", "../", "../outside", "alpha/..", "alpha/../..", "alpha/../../x", ".", "alpha/.",
		"/", "/etc", outside, "alpha//inner", "alpha/", "./alpha", "alpha\\..\\..", "alpha\x00",
	} {
		got, err := b.ListStagingDirectory(context.Background(), rel)
		if !errors.Is(err, ErrStagingBrowseInvalid) {
			t.Errorf("%q: err = %v, want ErrStagingBrowseInvalid", rel, err)
		}
		if len(got.Entries) != 0 || got.Root != "" {
			t.Errorf("%q: a refused name still returned a listing: %+v", rel, got)
		}
	}
}

func TestListStagingDirectory_1179_ASymbolicLinkCannotStepOutOrAside(t *testing.T) {
	b, _, outside := browseFixture(t)
	for _, rel := range []string{"escape", "escape/secret-dir", "inside", "inside/inner"} {
		got, err := b.ListStagingDirectory(context.Background(), rel)
		if err == nil {
			t.Errorf("%q: a path through a symbolic link was listed: %+v", rel, got)
			continue
		}
		if !errors.Is(err, ErrStagingBrowseInvalid) {
			t.Errorf("%q: err = %v, want ErrStagingBrowseInvalid", rel, err)
		}
		if strings.Contains(err.Error(), outside) {
			t.Errorf("%q: the refusal names the outside path: %v", rel, err)
		}
	}
}

func TestListStagingDirectory_1179_AMissingFolderOrAFileIsNotFound(t *testing.T) {
	b, _, _ := browseFixture(t)
	for _, rel := range []string{"nope", "alpha/nope", "notes.txt"} {
		if _, err := b.ListStagingDirectory(context.Background(), rel); !errors.Is(err, ErrStagingBrowseNotFound) {
			t.Errorf("%q: err = %v, want ErrStagingBrowseNotFound", rel, err)
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

func TestListStagingDirectory_1179_OnlyReads(t *testing.T) {
	b, root, outside := browseFixture(t)
	before := append(snapshotTree(t, root), snapshotTree(t, outside)...)
	for _, rel := range []string{"", "alpha", "alpha/inner", "escape", "../x", "nope"} {
		_, _ = b.ListStagingDirectory(context.Background(), rel)
	}
	after := append(snapshotTree(t, root), snapshotTree(t, outside)...)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("browsing changed the file system\nbefore: %v\n after: %v", before, after)
	}
}

func TestListStagingDirectory_1179_ARootThatDoesNotExistYetIsEmptyAndIsNotCreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-yet")
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: func(c *config.Config) { c.Assurance.StagingRoot = root }})
	got, err := f.b.ListStagingDirectory(context.Background(), "")
	if err != nil {
		t.Fatalf("a staging root nobody has used yet is an empty folder, not an error: %v", err)
	}
	if len(got.Entries) != 0 || got.Root != root {
		t.Errorf("listing = %+v, want empty at %q", got, root)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("browsing created the staging root (Lstat err = %v)", err)
	}
	if _, err := f.b.ListStagingDirectory(context.Background(), "sub"); !errors.Is(err, ErrStagingBrowseNotFound) {
		t.Errorf("a sub-folder of a root that does not exist: err = %v, want ErrStagingBrowseNotFound", err)
	}
}

func TestListStagingDirectory_1179_ALargeFolderIsCutAndSaysSo(t *testing.T) {
	b, root, _ := browseFixture(t)
	for i := 0; i < StagingDirectoryLimit+5; i++ {
		if err := os.Mkdir(filepath.Join(root, "beta", fmt.Sprintf("d%05d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.ListStagingDirectory(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != StagingDirectoryLimit || !got.Truncated {
		t.Errorf("len=%d truncated=%v, want %d and true: a listing that silently drops folders reads as complete", len(got.Entries), got.Truncated, StagingDirectoryLimit)
	}
	small, _ := b.ListStagingDirectory(context.Background(), "alpha")
	if small.Truncated {
		t.Error("a small folder reports truncated")
	}
}

func TestListStagingDirectory_1179_ARootThatFailsCustodyIsUnavailable(t *testing.T) {
	bad := t.TempDir()
	if err := os.Chmod(bad, 0o777); err != nil {
		t.Fatal(err)
	}
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: func(c *config.Config) { c.Assurance.StagingRoot = bad }})
	if _, err := f.b.ListStagingDirectory(context.Background(), ""); !errors.Is(err, ErrStagingBrowseUnavailable) {
		t.Errorf("err = %v, want ErrStagingBrowseUnavailable: recovery refuses a root another account can write to, and so does the browser", err)
	}
}

// The review of #1185 asked for the listing's limit to bound the work and not
// only the answer, for cancellation to be honoured while a folder is read, and
// for a few refusals to say what really happened. The tests below hold that.

// errAfter is a context whose Err() is nil for the first n calls and then says
// it was cancelled, so a test can cancel a listing partway through without a
// timing race.
type errAfter struct {
	context.Context
	n int
}

func (c *errAfter) Err() error {
	if c.n > 0 {
		c.n--
		return nil
	}
	return context.Canceled
}

func TestListStagingDirectory_1179_HonoursACancellationThatArrivesWhileItReads(t *testing.T) {
	b, _, _ := browseFixture(t)
	_, err := b.ListStagingDirectory(&errAfter{Context: context.Background(), n: 1}, "")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled: a listing must look at its context again once it has started", err)
	}
}

func TestListStagingDirectory_1179_AnOverLimitFolderKeepsTheFirstNamesInOrder(t *testing.T) {
	b, root, _ := browseFixture(t)
	// Created in reverse so the order the file system hands them back is not
	// the order they sort in.
	for i := StagingDirectoryLimit + 40; i >= 0; i-- {
		if err := os.Mkdir(filepath.Join(root, "beta", fmt.Sprintf("d%05d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			if err := os.WriteFile(filepath.Join(root, "beta", fmt.Sprintf("f%05d", i)), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := b.ListStagingDirectory(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Entries) != StagingDirectoryLimit {
		t.Fatalf("truncated=%v len=%d, want true and %d", got.Truncated, len(got.Entries), StagingDirectoryLimit)
	}
	for i, e := range got.Entries {
		if want := fmt.Sprintf("d%05d", i); e.Name != want {
			t.Fatalf("entry %d = %q, want %q: a cut listing is the first folders by name, not whichever the disk handed back first", i, e.Name, want)
		}
	}
}

func TestListStagingDirectory_1179_StopsAtTheScanBudgetAndSaysSo(t *testing.T) {
	b, root, _ := browseFixture(t)
	old := stagingScanBudget
	stagingScanBudget = 64
	t.Cleanup(func() { stagingScanBudget = old })
	for i := 0; i < 400; i++ {
		if err := os.WriteFile(filepath.Join(root, "beta", fmt.Sprintf("file%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.ListStagingDirectory(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Error("a folder with more entries than the scan budget was read to the end, or was not reported as cut")
	}
	small, err := b.ListStagingDirectory(context.Background(), "alpha")
	if err != nil || small.Truncated {
		t.Errorf("a small folder under a small budget: truncated=%v err=%v", small.Truncated, err)
	}
}

func TestListStagingDirectory_1179_AFolderSwappedForALinkAfterTheWalkIsNotListed(t *testing.T) {
	// Between the walk that saw a real folder and the open that lists it,
	// alpha becomes a link. os.Root follows a link that stays inside the root
	// (beside), so without a second look the listing would be beta's; it
	// refuses one that leads out (outward), which must read as a refused
	// name and not as the host failing.
	for name, target := range map[string]func(root, outside string) string{
		"beside":  func(string, string) string { return "beta" },
		"outward": func(_, outside string) string { return outside },
	} {
		t.Run(name, func(t *testing.T) {
			b, root, outside := browseFixture(t)
			if err := os.Mkdir(filepath.Join(root, "beta", "only-in-beta"), 0o700); err != nil {
				t.Fatal(err)
			}
			stagingBrowseBeforeOpen = func() {
				if err := os.Rename(filepath.Join(root, "alpha"), filepath.Join(root, "alpha-moved")); err != nil {
					t.Error(err)
				}
				if err := os.Symlink(target(root, outside), filepath.Join(root, "alpha")); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() { stagingBrowseBeforeOpen = nil })
			got, err := b.ListStagingDirectory(context.Background(), "alpha")
			if !errors.Is(err, ErrStagingBrowseInvalid) {
				t.Errorf("err = %v (listing %v), want ErrStagingBrowseInvalid: the folder listed must be the one the walk found", err, names(got))
			}
		})
	}
}

func TestListStagingDirectory_1179_AFolderTheHostCannotReadIsItsOwnRefusal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder whatever its mode")
	}
	b, root, _ := browseFixture(t)
	locked := filepath.Join(root, "beta")
	if err := os.Mkdir(filepath.Join(locked, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	for _, rel := range []string{"beta", "beta/inside"} {
		_, err := b.ListStagingDirectory(context.Background(), rel)
		if !errors.Is(err, ErrStagingBrowseNotReadable) {
			t.Errorf("%q: err = %v, want ErrStagingBrowseNotReadable: a folder the host may not read is not a bad name", rel, err)
		}
		if err != nil && strings.Contains(err.Error(), root) {
			t.Errorf("%q: the refusal names the host path: %v", rel, err)
		}
	}
}

func TestListStagingDirectory_1179_RechecksTheRootsCustodyOnEveryListing(t *testing.T) {
	b, root, _ := browseFixture(t)
	if _, err := b.ListStagingDirectory(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if _, err := b.ListStagingDirectory(context.Background(), ""); !errors.Is(err, ErrStagingBrowseUnavailable) {
		t.Errorf("err = %v, want ErrStagingBrowseUnavailable: a root another account can now write to is not one to offer", err)
	}
}

func TestListStagingDirectory_1179_AFolderNamedWithTheReplacementCharacterIsListable(t *testing.T) {
	b, root, _ := browseFixture(t)
	name := "caf�"
	if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ListStagingDirectory(context.Background(), name); err != nil {
		t.Errorf("a real folder whose name holds U+FFFD: %v", err)
	}
	if _, err := b.ListStagingDirectory(context.Background(), "caf\xff"); !errors.Is(err, ErrStagingBrowseInvalid) {
		t.Errorf("a name that is not UTF-8: err = %v, want ErrStagingBrowseInvalid", err)
	}
}

// FuzzStagingBrowseSegments holds the name check's whole promise for any
// input: whatever it accepts is relative, has no empty, "." or ".." part, no
// separator the host does not mean, nothing that is not text, and splits back
// into exactly the name it was given.
func FuzzStagingBrowseSegments(f *testing.F) {
	for _, s := range []string{"", "a", "a/b", "..", "../x", "a/../b", "/etc", "a//b", "a/", "./a", "a\\b", "a\x00", "caf�", "caf\xff", "\u202e..", "a/./b", strings.Repeat("a/", 3000)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		segs, err := stagingBrowseSegments(rel)
		if err != nil {
			if !errors.Is(err, ErrStagingBrowseInvalid) {
				t.Fatalf("%q: err = %v, want ErrStagingBrowseInvalid", rel, err)
			}
			return
		}
		if rel == "" {
			if segs != nil {
				t.Fatalf("the root split into %q", segs)
			}
			return
		}
		if len(rel) > StagingBrowseNameLimit || !utf8.ValidString(rel) || strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, '\\') {
			t.Fatalf("%q was accepted", rel)
		}
		for _, r := range rel {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("%q was accepted with a control character", rel)
			}
		}
		for _, s := range segs {
			if s == "" || s == "." || s == ".." || strings.Contains(s, "/") {
				t.Fatalf("%q was accepted with the part %q", rel, s)
			}
		}
		if strings.Join(segs, "/") != rel {
			t.Fatalf("%q split into %q", rel, segs)
		}
	})
}

// FuzzListStagingDirectoryStaysUnderTheRoot lists whatever name the fuzzer
// invents against a real tree with links that lead out and aside, and holds
// that every listing it answers is a real folder under the root.
func FuzzListStagingDirectoryStaysUnderTheRoot(f *testing.F) {
	base := f.TempDir()
	root := filepath.Join(base, "staging")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "alpha", "inner"), filepath.Join(root, "beta"), filepath.Join(outside, "secret")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			f.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		f.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(root, "beta", "aside")); err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{"", "alpha", "alpha/inner", "escape", "escape/secret", "beta/aside", "beta/aside/inner", "../outside", "alpha/../../outside"} {
		f.Add(s)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		got, err := listStagingDirectory(context.Background(), root, rel)
		if err != nil {
			if strings.Contains(err.Error(), base) {
				t.Fatalf("%q: the refusal names a host path: %v", rel, err)
			}
			return
		}
		at, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%q was listed but does not resolve: %v", rel, err)
		}
		if at != realRoot && !strings.HasPrefix(at, realRoot+string(filepath.Separator)) {
			t.Fatalf("%q was listed and resolves outside the root, to %q", rel, at)
		}
		if lexical := filepath.Join(realRoot, filepath.FromSlash(rel)); at != lexical {
			t.Fatalf("%q was listed through a link (it resolves to %q)", rel, at)
		}
		for _, e := range got.Entries {
			if e.Name == "escape" || e.Name == "aside" {
				t.Fatalf("%q listed the link %q", rel, e.Name)
			}
		}
	})
}
