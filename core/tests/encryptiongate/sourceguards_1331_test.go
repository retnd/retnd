package encryptiongate_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Two of EPIC N's consensus findings are claims about source, so they are held
// by reading source (#1331, the 0.8.0 release PR). They live here, outside the
// packages they read, so a planted import or field that stops those packages
// building still reaches this test as text.

// productFiles parses every non-test Go file directly in dir.
func productFiles(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		out[name] = f
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no Go source, so this guard read nothing", dir)
	}
	return out
}

// NC-01: the transform (core/internal/copycrypt) is built on the library ADR
// 0027 names, filippo.io/age, and takes nothing from the rclone transport,
// whose keyencryption.go is the whole-buffer AES-GCM envelope for key files
// the ADR rules out for artifact material. Nor does it reach for a raw AEAD
// of its own: ADR 0033 D5 says nothing behind the Suite interface is
// designed here.
func TestTheTransformImportsAgeAndNothingOfTheKeyFileEnvelope_1331(t *testing.T) {
	root := repoRoot(t)
	files := productFiles(t, filepath.Join(root, "core/internal/copycrypt"))
	forbidden := []string{
		"github.com/retnd/retnd/core/internal/transport",
		"crypto/aes",
		"crypto/cipher",
		"golang.org/x/crypto/chacha20poly1305",
	}
	sawAge := false
	var bad []string
	for name, f := range files {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == "filippo.io/age" || strings.HasPrefix(path, "filippo.io/age/") {
				sawAge = true
			}
			for _, p := range forbidden {
				if path == p || strings.HasPrefix(path, p+"/") {
					bad = append(bad, name+" imports "+path)
				}
			}
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s; the transform uses the library ADR 0027 names and never the key-file envelope or a raw AEAD", b)
	}
	if !sawAge {
		t.Error("no file in core/internal/copycrypt imports filippo.io/age, the library ADR 0027 names")
	}
}

// NC-16: key safety comes from never wrapping key material in anything that
// renders. No type in the key-custody seam holds an obs.Secret (the
// material crosses it as a []byte handed to a callback and zeroed), so there
// is no type that could forget one of the six rendering interfaces
// core/internal/obs:TestSecretInAnUnexportedFieldStillLeaks says a holder
// must reassert. A struct field of type obs.Secret in these packages is what
// would start needing them, and this names it.
func TestNoKeyCustodyTypeHoldsAnObsSecret_1331(t *testing.T) {
	root := repoRoot(t)
	var bad []string
	for _, dir := range []string{"core/internal/keycustody", "core/internal/copycrypt"} {
		for name, f := range productFiles(t, filepath.Join(root, dir)) {
			obsName := ""
			for _, imp := range f.Imports {
				if p, _ := strconv.Unquote(imp.Path.Value); p == "github.com/retnd/retnd/core/internal/obs" {
					obsName = "obs"
					if imp.Name != nil {
						obsName = imp.Name.Name
					}
				}
			}
			if obsName == "" {
				continue
			}
			ast.Inspect(f, func(n ast.Node) bool {
				st, ok := n.(*ast.StructType)
				if !ok {
					return true
				}
				for _, fld := range st.Fields.List {
					typ := fld.Type
					if star, ok := typ.(*ast.StarExpr); ok {
						typ = star.X
					}
					if sel, ok := typ.(*ast.SelectorExpr); ok && sel.Sel.Name == "Secret" {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == obsName {
							bad = append(bad, dir+"/"+name)
						}
					}
				}
				return true
			})
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s declares a struct field of type obs.Secret; key material is never wrapped in a renderable type here, and a holder would owe all six rendering interfaces", b)
	}
}
