package transport_test

import (
	"os"
	"path/filepath"
	"testing"
)

// The two deletion scans walk the whole core/ tree while every other
// package's tests run beside them, and internal/workflow's tests make and
// remove .wf-custody-* scratch directories inside their own package
// directory. A directory listed by the walk and gone by the time it is read
// failed the scan with ENOENT (CI run 39700665736), which says nothing about
// who calls DeleteObject. walkSourceFiles skips what vanished and still
// fails on anything else.
func TestTheScanSurvivesAScratchDirectoryThatVanishes(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"a/x.go", "b/sub/y.go", "c/z.go"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package p\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	err := walkSourceFiles(root, func(path string, d os.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		seen = append(seen, rel)
		if rel == filepath.Join("a", "x.go") {
			// Gone after the root was listed and before it is read.
			return os.RemoveAll(filepath.Join(root, "b"))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a directory that vanished mid-walk failed the walk: %v", err)
	}
	if len(seen) != 2 || seen[1] != filepath.Join("c", "z.go") {
		t.Errorf("the walk visited %v, want a/x.go and c/z.go", seen)
	}
}

// The control: a failure that is not a vanished path still fails the walk.
func TestTheScanStillFailsOnOtherErrors(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "x.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 directory anyway")
	}
	if err := walkSourceFiles(root, func(string, os.DirEntry) error { return nil }); err == nil {
		t.Fatal("an unreadable directory did not fail the walk")
	}
}
