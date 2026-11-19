package workflowrun

import (
	"path/filepath"
	"strings"
	"testing"
)

// A fixture directory made beside this package's sources is a directory
// every tree-walking test in the module can trip over while it comes and
// goes: core/tests/brandguard failed CI on one that vanished mid-walk
// (#1247). The fixtures live in the repository's ignored .testtmp/ instead,
// which is still a tree no other account can write, so the custody walk
// accepts it where it would refuse /tmp.
func TestFixturesAreMadeOutsideThePackageSources(t *testing.T) {
	dir := custodyTempDir(t)
	here, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(here, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rel, "..") {
		t.Fatalf("the fixture %s is inside the package's own sources (%s)", dir, here)
	}
	if !strings.Contains(filepath.ToSlash(dir), "/.testtmp/") {
		t.Fatalf("the fixture %s is not under the repository's ignored .testtmp/", dir)
	}
}
