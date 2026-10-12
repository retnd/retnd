package procseam

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder is a T that records instead of failing, so the guard can be shown
// to fire without failing the run that shows it.
type recorder struct {
	errs     []string
	cleanups []func()
}

func (r *recorder) Helper()                   {}
func (r *recorder) Errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }
func (r *recorder) Cleanup(fn func())         { r.cleanups = append(r.cleanups, fn) }
func (r *recorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func TestWithoutAForbidASpawnGoesThrough(t *testing.T) {
	cmd := CommandContext(context.Background(), "true")
	if cmd.Err != nil {
		t.Fatalf("a command was refused with nothing forbidden: %v", cmd.Err)
	}
}

// The positive control: the guard must be able to fail. A seam that records
// nothing would pass every test that uses it.
func TestAForbiddenSpawnFailsTheTestAndCannotStart(t *testing.T) {
	r := &recorder{}
	Forbid(r)
	for name, mk := range map[string]func() error{
		"CommandContext": func() error { return CommandContext(context.Background(), "true").Run() },
		"Command":        func() error { return Command("true").Run() },
	} {
		before := len(r.errs)
		err := mk()
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: Run = %v, want ErrRefused (the process must not exist)", name, err)
		}
		if len(r.errs) != before+1 {
			t.Errorf("%s: the spawn was not reported to the test", name)
		}
	}
	r.finish()
	if cmd := Command("true"); cmd.Err != nil {
		t.Fatalf("the guard outlived its test: %v", cmd.Err)
	}
}

func TestTheGuardDoesNotEchoArguments(t *testing.T) {
	r := &recorder{}
	Forbid(r)
	defer r.finish()
	_ = Command("sh", "-c", "echo hunter2")
	if len(r.errs) != 1 || strings.Contains(r.errs[0], "hunter2") {
		t.Fatalf("report = %q: an argument can be a secret reference and must not reach a log", r.errs)
	}
}

// Every product spawn goes through the seam, or the guard proves nothing
// about the ones that do not. Test files, the test tiers and the gate's own
// tools are exempt: they are not the product.
func TestEveryProductSpawnGoesThroughTheSeam(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	exempt := []string{
		filepath.Join(root, "tests") + string(filepath.Separator),
		filepath.Join(root, "cmd", "gotestwatch") + string(filepath.Separator),
		filepath.Join(root, "internal", "testtier") + string(filepath.Separator),
		filepath.Join(root, "internal", "procseam") + string(filepath.Separator),
	}
	fset := token.NewFileSet()
	var offenders []string
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		for _, e := range exempt {
			if strings.HasPrefix(p, e) {
				return nil
			}
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		f, perr := parser.ParseFile(fset, p, src, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		name := ""
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "os/exec" {
				name = "exec"
				if imp.Name != nil {
					name = imp.Name.Name
				}
			}
		}
		if name == "" {
			return nil
		}
		for i, line := range strings.Split(string(src), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//") {
				continue
			}
			if strings.Contains(l, name+".Command(") || strings.Contains(l, name+".CommandContext(") {
				rel, _ := filepath.Rel(root, p)
				offenders = append(offenders, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if len(offenders) > 0 {
		t.Fatalf("these start a process without going through internal/procseam, so a test that forbids spawns cannot see them:\n  %s", strings.Join(offenders, "\n  "))
	}
}
