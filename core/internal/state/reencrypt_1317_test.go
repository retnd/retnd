package state

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #1317: a key rotation or a suite deprecation must never turn into a rewrite
// of copies that already exist. The one thing that makes a move a rewrite is
// the opt-in row (encrypt_existing_consents, migration 0019, reused as it is
// for re-encryption), so the guard is on who can write it.
//
// The engine's half is TestTheRetentionCycleNeverReencryptsOnItsOwn_1317 in
// placement: a cycle planned against the same copy re-homes it under the old
// key and leaves no row. This half reads the production source of the whole
// core module, so a later change that adds a writer anywhere (a scheduler, a
// policy hook, the retention pass) is red here before it ships.
//
// Written by reading the source tree; not run.

func TestTheRetentionCycleWritesNoConsentRow_1317(t *testing.T) {
	root := filepath.Join("..", "..")
	type call struct{ file, fn string }
	var calls []call
	var inserts []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if strings.Contains(string(src), "INSERT") && strings.Contains(string(src), "encrypt_existing_consents") {
			inserts = append(inserts, rel)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RecordEncryptExisting" {
					calls = append(calls, call{file: rel, fn: fn.Name.Name})
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the core module: %v", err)
	}

	// The operator's entry point is the only caller in the product. The crash
	// harness's journal decorator forwards to the real write so it can kill
	// after it; it is a test binary and writes nothing of its own.
	allowed := map[call]bool{
		{file: "internal/placement/retroencrypt.go", fn: "EncryptExisting"}:    true,
		{file: "tests/movecrash/harness/main.go", fn: "RecordEncryptExisting"}: true,
	}
	if len(calls) == 0 {
		t.Fatal("control: no caller of RecordEncryptExisting found at all, so this guard is reading the wrong tree")
	}
	for _, c := range calls {
		if !allowed[c] {
			t.Errorf("%s calls RecordEncryptExisting from %s; only the operator's EncryptExisting may opt a move in, so a cycle, a rotation or a deprecation can never rewrite a copy", c.file, c.fn)
		}
	}
	for _, f := range inserts {
		if f != "internal/state/encryptexisting.go" {
			t.Errorf("%s writes encrypt_existing_consents directly; the one writer is Journal.RecordEncryptExisting", f)
		}
	}
}
