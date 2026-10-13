// Package deploymentdiff holds the canonical deployment to what it was when
// EPIC Q began (#983): recovery, scrubbing and their budgets add no
// container, no port, no mount and no capability.
//
// The files named here are the whole of what an operator deploys: the
// reference compose file, the seven per-platform compose files, the two
// Unraid templates, the TrueNAS catalog and the four packaging manifests.
// This product runs distroless, non-root, read-only, with `cap_drop: ALL`
// and `no-new-privileges`, no Docker socket and no host-root mount, and the
// way any of that gets weakened is one reasonable-looking line in one of
// these files. So their digests are checked in, and a change to any of them
// fails here, naming the file.
//
// This is a tripwire and not a ban. A change that should alter the
// deployment says so by updating testdata/digests.txt in the same commit,
// where a reviewer sees it as its own line; the point is that it cannot
// happen as a side effect of something else.
package deploymentdiff

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Built from path elements: these classify recorded paths and import nothing,
// and a literal "apps/<provider>/" reads as core importing that provider to the
// provider-removal-preserves-core conformance check (#1082).
var (
	unraidTemplateDir = strings.Join([]string{"apps", "unraid", "template"}, "/") + "/"
	truenasCatalogDir = strings.Join([]string{"apps", "truenas", "catalog"}, "/") + "/"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

var (
	perPlatformCompose = regexp.MustCompile(`^apps/[^/]+/compose/retnd\.y(a)?ml$`)
	packagingManifest  = regexp.MustCompile(`^distribution/packaging/[^/]+\.json$`)
)

// deploymentFiles lists, relative to the repository root, every file this
// test holds fixed.
func deploymentFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "build", "target", ".worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == "container/compose.yaml",
			perPlatformCompose.MatchString(rel),
			rel == unraidTemplateDir+"retnd.xml",
			rel == unraidTemplateDir+"retnd-ui.xml",
			strings.HasPrefix(rel, truenasCatalogDir),
			packagingManifest.MatchString(rel):
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func digestOf(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func readRecorded(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "digests.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, path, ok := strings.Cut(sc.Text(), "  ")
		if !ok {
			t.Fatalf("malformed line in testdata/digests.txt: %q", sc.Text())
		}
		out[path] = sum
	}
	return out
}

// compare returns one complaint per file that was added, removed or
// changed. It is separate from the test so the test of the test can hand it
// a deployment that has drifted.
func compare(recorded, actual map[string]string) []string {
	var complaints []string
	for path, want := range recorded {
		got, ok := actual[path]
		switch {
		case !ok:
			complaints = append(complaints, path+" is gone")
		case got != want:
			complaints = append(complaints, path+" has changed")
		}
	}
	for path := range actual {
		if _, ok := recorded[path]; !ok {
			complaints = append(complaints, path+" is new")
		}
	}
	sort.Strings(complaints)
	return complaints
}

func TestTheCanonicalDeploymentIsExactlyWhatItWasBeforeEpicQ(t *testing.T) {
	skip, failures := verdict(t, repoRoot(t), readRecorded(t))
	if skip != "" {
		t.Skip(skip)
	}
	if len(failures) > 0 {
		t.Fatal(strings.Join(failures, "\n"))
	}
}

// verdict is the whole decision, separate from the test so the controls
// below can hand it a tree. skip is non-empty only when the apps tree is
// genuinely absent; failures are what the test reports otherwise.
func verdict(t *testing.T, root string, recorded map[string]string) (skip string, failures []string) {
	t.Helper()
	actual := map[string]string{}
	for _, rel := range deploymentFiles(t, root) {
		sum, err := digestOf(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		actual[rel] = sum
	}
	if len(actual) < 18 {
		return "", []string{fmt.Sprintf("only %d deployment files found; the seven compose files, two templates, the catalog and four manifests are at least 18, so the walk is not finding them", len(actual))}
	}
	if complaints := compare(recorded, actual); len(complaints) > 0 {
		return "", []string{"the deployment moved:\n  " + strings.Join(complaints, "\n  ") + "\n\nEPIC Q adds no container, port, mount or capability. If this change is meant to alter what an operator deploys, update core/tests/deploymentdiff/testdata/digests.txt in the same commit so the line shows up in review; if it is not, take the edit out of this change."}
	}
	return "", nil
}

// fakeRepo builds a tree holding the recorded deployment files with the
// given content; withApps controls whether the apps directory exists.
func fakeRepo(t *testing.T, withApps bool, drift string) string {
	t.Helper()
	root := t.TempDir()
	for rel := range readRecorded(t) {
		if !withApps && strings.HasPrefix(rel, "apps/") {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if withApps {
		if err := os.MkdirAll(filepath.Join(root, "apps"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if drift != "" {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(drift)), []byte("drifted"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestAbsentAppsTreeSkipsWithAReason is the control for the skip: it fires
// for a tree with no apps directory (the verify-core-without-apps check
// deletes it on purpose) and says why.
func TestAbsentAppsTreeSkipsWithAReason(t *testing.T) {
	skip, failures := verdict(t, fakeRepo(t, false, ""), readRecorded(t))
	if skip == "" || len(failures) != 0 {
		t.Fatalf("a tree with no apps directory must skip with a reason, got skip=%q failures=%v", skip, failures)
	}
	if !strings.Contains(skip, "apps") {
		t.Errorf("the skip reason does not say what is missing: %q", skip)
	}
}

// TestPresentAppsTreeNeverSkips is the control that the skip cannot hide a
// real failure: with apps/ present, a drifted file and a missing file both
// fail, and nothing skips.
func TestPresentAppsTreeNeverSkips(t *testing.T) {
	recorded := readRecorded(t)
	drifted := fakeRepo(t, true, "container/compose.yaml")
	if skip, failures := verdict(t, drifted, recorded); skip != "" || len(failures) == 0 {
		t.Errorf("a drifted file with apps/ present must fail, got skip=%q failures=%v", skip, failures)
	}
	partial := fakeRepo(t, true, "")
	for rel := range recorded {
		if strings.HasPrefix(rel, "apps/") {
			if err := os.Remove(filepath.Join(partial, filepath.FromSlash(rel))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if skip, failures := verdict(t, partial, recorded); skip != "" || len(failures) == 0 {
		t.Errorf("apps/ present but its deployment files gone must fail, got skip=%q failures=%v", skip, failures)
	}
}

func TestTheComparisonCanActuallyFail(t *testing.T) {
	recorded := map[string]string{"a": "1", "b": "2", "c": "3"}
	for name, actual := range map[string]map[string]string{
		"changed": {"a": "1", "b": "X", "c": "3"},
		"removed": {"a": "1", "b": "2"},
		"added":   {"a": "1", "b": "2", "c": "3", "d": "4"},
	} {
		if len(compare(recorded, actual)) == 0 {
			t.Errorf("a deployment where a file was %s compared clean", name)
		}
	}
	if got := compare(recorded, map[string]string{"a": "1", "b": "2", "c": "3"}); len(got) != 0 {
		t.Errorf("an identical deployment complained: %v", got)
	}
}

func TestTheRecordedDigestsCoverTheSevenComposeFilesTheTwoTemplatesTheCatalogAndTheManifests(t *testing.T) {
	recorded := readRecorded(t)
	counts := map[string]int{}
	for path := range recorded {
		switch {
		case path == "container/compose.yaml":
			counts["container"]++
		case perPlatformCompose.MatchString(path):
			counts["platform compose"]++
		case strings.HasPrefix(path, unraidTemplateDir):
			counts["unraid"]++
		case strings.HasPrefix(path, truenasCatalogDir):
			counts["truenas catalog"]++
		case packagingManifest.MatchString(path):
			counts["manifests"]++
		}
	}
	want := map[string]int{"container": 1, "platform compose": 7, "unraid": 2, "manifests": 4}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("%s: %d recorded, want %d", k, counts[k], n)
		}
	}
	if counts["truenas catalog"] == 0 {
		t.Error("no TrueNAS catalog file is recorded")
	}
	_ = fmt.Sprint
}
