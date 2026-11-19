package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// Panel finding C10 (row Q1.6): the scheduled scrub and the due recovery tests
// run on a timer inside core/service's RunOnSchedule, and RunOnSchedule is
// only ever called by serve.RunEngine on the Scheduler `serve` hands it. A
// test of RunOnSchedule proves the loop works; it cannot notice that the
// `serve` command stopped starting it (a refactor that passes nil, as the
// UI-only `serve-ui` does on purpose). This reads the command's source and
// holds the one hop the loop's own tests cannot see.
//
// The rule: in the function that calls serve.RunEngine for `serve`, the
// scheduler argument is a variable, never the literal nil, and that variable
// is assigned something on the configured path and on the first-run path.

// schedulerWiring reports, for the function in src that declares a variable
// named scheduler and passes it to serve.RunEngine, how many times it is
// assigned and whether any of those assignments (or the call) uses nil.
type schedulerWiring struct {
	found       bool
	assignments int
	nilPassed   bool
}

func readSchedulerWiring(t *testing.T, filename string, src any) schedulerWiring {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var w schedulerWiring
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		var calls bool
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "RunEngine" || len(call.Args) < 3 {
				return true
			}
			if id, ok := call.Args[2].(*ast.Ident); ok {
				if id.Name == "nil" {
					return true // serve-ui's call: no backup service, on purpose
				}
				if id.Name == "scheduler" {
					calls = true
				}
			}
			return true
		})
		if !calls {
			continue
		}
		w.found = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == "scheduler" && i < len(as.Rhs) {
					w.assignments++
					if r, ok := as.Rhs[i].(*ast.Ident); ok && r.Name == "nil" {
						w.nilPassed = true
					}
				}
			}
			return true
		})
	}
	return w
}

func TestServeHandsTheEngineAScheduler(t *testing.T) {
	w := readSchedulerWiring(t, "main.go", nil)
	if !w.found {
		t.Fatal("found no function that passes a `scheduler` to serve.RunEngine: the serve command no longer starts the scheduled scrub and recovery tests, or this guard is looking at the wrong thing")
	}
	// One assignment for a configured start and one for the first-run engine.
	if w.assignments < 2 {
		t.Errorf("`scheduler` is assigned %d time(s) in the serve command, want its configured and its first-run assignment", w.assignments)
	}
	if w.nilPassed {
		t.Error("the serve command assigns a nil scheduler: the process would serve without ever running the scrub or the recovery tests")
	}
}

func TestTheServeSchedulerGuardCanFail(t *testing.T) {
	const good = `package main
func serveCmd() { var scheduler S; scheduler = a; scheduler = b; serve.RunEngine(ctx, h, scheduler, g, w) }`
	const nilScheduler = `package main
func serveCmd() { var scheduler S; scheduler = a; scheduler = nil; serve.RunEngine(ctx, h, scheduler, g, w) }`
	const dropped = `package main
func serveCmd() { serve.RunEngine(ctx, h, nil, g, w) }`
	if w := readSchedulerWiring(t, "x.go", good); !w.found || w.assignments != 2 || w.nilPassed {
		t.Errorf("good = %+v", w)
	}
	if w := readSchedulerWiring(t, "x.go", nilScheduler); !w.nilPassed {
		t.Errorf("a nil assignment was not seen: %+v", w)
	}
	if w := readSchedulerWiring(t, "x.go", dropped); w.found {
		t.Errorf("a call that passes nil was counted as wired: %+v", w)
	}
	if _, err := os.Stat("main.go"); err != nil {
		t.Fatal(err)
	}
}
