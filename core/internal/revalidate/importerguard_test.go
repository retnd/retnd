// A guard that fails when this package has no production importer, and its
// own mutation self-test.
//
// For a long time internal/revalidate was fully written, fully tested and
// run by nothing: the only Go file that imported it was a test. Every
// deployment carried a validated `revalidation:` block per backup set that
// no code read. A package like that cannot be told apart from a working one
// by its own tests, which is the reason this lives here and reads the rest of
// the tree.
//
// A guard that has never failed has proven nothing, so the scan is a
// function of a directory and TestTheImporterGuardCanFail runs it against a
// tree built to violate it.

package revalidate

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const revalidateImportPath = "github.com/retnd/retnd/core/internal/revalidate"

// productionImporters returns the non-test Go files under root that import
// the revalidate package, other than the package's own files.
func productionImporters(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "revalidate" && strings.HasSuffix(filepath.Dir(path), filepath.Join("internal", "revalidate")) {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return fmt.Errorf("parsing %s: %w", path, perr)
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == revalidateImportPath {
				rel, _ := filepath.Rel(root, path)
				out = append(out, rel)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func TestRevalidateHasAProductionImporter(t *testing.T) {
	root := filepath.Join("..", "..") // core/
	got, err := productionImporters(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatalf("no non-test Go file under core/ imports %s: the scrub is written, bounded and tested, and nothing runs it", revalidateImportPath)
	}
}

// The self-test: build a tree where the package's only importer is a _test.go
// file and require the scan to find nothing; then add a production importer
// and require it to be found. A scan that cannot tell these apart is the
// guard having never failed.
func TestTheImporterGuardCanFail(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := "package x\n\nimport _ %q\n"
	write("tests/it/revalidate_test.go", fmt.Sprintf(src, revalidateImportPath))
	write("internal/revalidate/own.go", fmt.Sprintf(src, revalidateImportPath))
	write("testdata/fixture.go", fmt.Sprintf(src, revalidateImportPath))

	got, err := productionImporters(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("scan found %v in a tree whose only importers are a test, the package itself and testdata: the guard cannot go red", got)
	}

	write("internal/app/scrub.go", fmt.Sprintf(src, revalidateImportPath))
	got, err = productionImporters(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != filepath.Join("internal", "app", "scrub.go") {
		t.Fatalf("scan found %v, want exactly internal/app/scrub.go", got)
	}
}
