// Package brandguard holds the check that the project's former name is gone
// from the Go, SQL, API-spec, compose and install files (issue #1109).
//
// The name is never written out here: every spelling is assembled from
// pieces, because a literal in this file would be the first thing the scan
// found, and because scripts/rename/check-brand-drift.sh is right to refuse
// a new one.
package brandguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// stem is the former name in its lower-case form, in two halves.
var stem = strings.Join([]string{"backup", "d"}, "")

// spellings are the four shapes the name took: the environment-variable
// form, the product name, the CamelCase form, and the lower-case form. The
// last three are only a hit when no lower-case letter follows, so a word
// that merely begins with the stem is left alone.
var (
	upperForm = strings.ToUpper(stem)
	titleForm = strings.ToUpper(stem[:1]) + stem[1:]
	camelForm = "Backup" + "D"
	formerRE  = regexp.MustCompile(
		regexp.QuoteMeta(upperForm) + "|" +
			"(?:" + regexp.QuoteMeta(titleForm) + "|" + regexp.QuoteMeta(camelForm) + "|" + regexp.QuoteMeta(stem) + ")([^a-z]|$)")
)

// roots are the trees and files this guard owns.
var roots = []string{
	"core",
	"apps/common",
	"apps/generic/tests",
	"api/v1/openapi.json",
	"container",
	"scripts/install/install_docker_host.py",
}

// frozen are files that cannot be edited without breaking an installed
// database, and so are reported rather than fixed.
//
// core/migrations/0012_workflow_runs.sql carries one comment that quotes the
// name. Its sha256 is recorded in every journal that applied version 12
// (core/internal/state/migrate.go refuses a mismatch with ErrSchemaDrift) and
// is pinned by core/internal/state/migrationimmutability_test.go and
// migrationanchor_test.go, so even a comment edit would stop every existing
// deployment from opening.
var frozen = map[string]bool{
	"core/migrations/0012_workflow_runs.sql": true,
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)

	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// scan walks every root under root and returns the hits and how many files it
// read. It is a function of the root so a test can hand it a tree of its own.
func scan(root string) (hits []string, scanned int, err error) {
	for _, r := range roots {
		start := filepath.Join(root, r)
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return walkError(start, p, err)
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "node_modules", "dist", "build", "target", ".worktrees":
					return filepath.SkipDir
				}
				// internal/workflowrun's fixtures, from before they moved
				// to .testtmp/ or from a run of an older checkout: test
				// scratch, not files this guard owns (#1247).
				if strings.HasPrefix(d.Name(), ".wfrun-") {
					return filepath.SkipDir
				}

				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if frozen[rel] {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil || strings.IndexByte(string(data), 0) >= 0 {
				return nil
			}
			scanned++
			for i, line := range strings.Split(string(data), "\n") {
				if formerRE.MatchString(line) {
					hits = append(hits, rel+":"+strconv.Itoa(i+1))
				}
			}

			return nil
		})
		if err != nil {
			// The apps/ roots are optional: core is tested with apps/ deleted
			// (scripts/architecture/verify-core-without-apps.sh). Every other
			// root is required (#1125).
			if errors.Is(err, fs.ErrNotExist) && strings.HasPrefix(r, "apps/") {
				continue
			}

			return hits, scanned, fmt.Errorf("walking %s: %w", r, err)
		}
	}

	return hits, scanned, nil
}

// walkError decides what an error met on the way through a root means. An
// entry below the root that no longer exists was removed after the walk
// listed it (another package's test fixture, see
// TestAnEntryThatVanishesMidWalkIsSkippedButAMissingRootIsNot) and is
// skipped. Anything else, including a root that is not there, is returned.
func walkError(start, p string, err error) error {
	if p != start && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func TestTheFormerNameAppearsNowhereInTheFilesThisGuardOwns(t *testing.T) {
	hits, scanned, err := scan(repoRoot())
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d files; the roots moved and this guard proved nothing", scanned)
	}
	if len(hits) > 0 {
		t.Errorf("the former name is still spelled in %d places:\n%s", len(hits), strings.Join(hits, "\n"))
	}
}

// Other packages' tests run beside this one and make and remove fixture
// directories inside the tree (internal/workflowrun puts its .wfrun-* ones
// next to its own sources). One that goes away between being listed and
// being opened is not a file this guard owns, and failing on it made the
// guard fail whenever the two happened to overlap.
func TestAnEntryThatVanishesMidWalkIsSkippedButAMissingRootIsNot(t *testing.T) {
	start := filepath.Join("r", "core")
	gone := &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist}
	if err := walkError(start, filepath.Join(start, "internal", ".wfrun-1"), gone); err != nil {
		t.Errorf("an entry that vanished during the walk failed it: %v", err)
	}
	if err := walkError(start, start, gone); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing root was let through as %v; it has to stay an error (#1125)", err)
	}
	denied := &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}
	if err := walkError(start, filepath.Join(start, "internal"), denied); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("an unreadable directory was let through as %v; only a vanished one is skipped", err)
	}
}

// A fixture directory another package's tests make inside the tree
// (internal/workflowrun's .wfrun-*) is that test's scratch, not a file this
// guard owns, and walking into one is how the guard came to race its
// removal. The control beside it proves the same file outside such a
// directory is still found.
func TestAFixtureDirectoryIsNotScanned(t *testing.T) {
	tree := t.TempDir()
	for _, r := range roots {
		if strings.HasPrefix(r, "apps/") {
			continue
		}
		dir := filepath.Join(tree, r)
		if filepath.Ext(r) != "" {
			dir = filepath.Dir(dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(r) != "" {
			if err := os.WriteFile(filepath.Join(tree, r), []byte("clean\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture := filepath.Join(tree, "core", "internal", "workflowrun", ".wfrun-123")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "state.txt"), []byte(upperForm+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hits, _, err := scan(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("the scan walked into a test fixture directory: %v", hits)
	}

	if err := os.WriteFile(filepath.Join(tree, "core", "internal", "workflowrun", "state.txt"), []byte(upperForm+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hits, _, _ := scan(tree); len(hits) != 1 {
		t.Errorf("the same file outside the fixture directory gave %v; the control needs exactly one hit", hits)
	}
}

// scripts/architecture/verify-core-without-apps.sh deletes apps/ and runs
// core's tests, so a root under apps/ that is not there is not an error. A
// root that is not under apps/ still is, or a moved core/ would pass as an
// empty scan (#1125).
func TestARootUnderAppsMayBeAbsentButNoOtherRootMay(t *testing.T) {
	tree := t.TempDir()
	for _, r := range roots {
		if strings.HasPrefix(r, "apps/") {
			continue
		}
		p := filepath.Join(tree, r)
		if filepath.Ext(r) != "" {
			p = filepath.Join(tree, filepath.Dir(r))
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(r) != "" {
			if err := os.WriteFile(filepath.Join(tree, r), []byte("clean\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, _, err := scan(tree); err != nil {
		t.Errorf("a tree with no apps/ must scan clean, got: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(tree, "core")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scan(tree); err == nil {
		t.Errorf("a tree with no core/ must be an error, or a moved root passes as an empty scan")
	}
}

// The scan has to be able to fail, or a green result says nothing.
func TestTheScanRecognisesEachSpellingAndSparesLongerWords(t *testing.T) {
	for _, s := range []string{upperForm, upperForm + "_DEBUG", "the " + titleForm + " page", camelForm, stem, stem + ".service", "x " + titleForm + "'s"} {
		if !formerRE.MatchString(s) {
			t.Errorf("the scan missed %q", s)
		}
	}
	for _, s := range []string{stem + "x", titleForm + "s", "RETND_DEBUG"} {
		if formerRE.MatchString(s) {
			t.Errorf("the scan flagged %q, which is not the former name", s)
		}
	}
}
