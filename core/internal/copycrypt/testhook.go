package copycrypt

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// This file is the registry's test hook (EPIC T, T2.1, #1316): a test can
// add a suite with any status to what Suites and Lookup answer, for its own
// lifetime, so that "deprecated" and "retired" are exercised end to end
// before any real suite is ever deprecated.
//
// The hook never touches the production table. An injected suite's id must
// start with TestSuitePrefix, and lifecycle_test.go asserts that no entry of
// the production table does, so a test id can never ship as a row. The
// suite an injected one wraps does the cryptography, which keeps the hook
// from being a construction of its own: it relabels a registered adapter,
// it does not encrypt anything itself.

// TestSuitePrefix starts every injected suite id.
const TestSuitePrefix = "test-"

// T is the part of testing.TB the hook needs, so this package does not
// import "testing" into the product binary (procseam's shape).
type T interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// TestSuite is an injected suite: the wrapped adapter under another id, with
// a status and a retirement version the test can change while it runs.
type TestSuite struct {
	Suite
	id     string
	mu     sync.Mutex
	status Status
	retire string
}

// ID is the injected id, never the wrapped adapter's.
func (s *TestSuite) ID() string { return s.id }

// Status is what the test last set.
func (s *TestSuite) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// RetiredIn is what the test last set.
func (s *TestSuite) RetiredIn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retire
}

// SetStatus moves the suite to status, announcing or recording retiredIn.
// A test writes a copy while the suite is supported and then retires it, to
// show that every copy it wrote still reads.
func (s *TestSuite) SetStatus(status Status, retiredIn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.retire = status, retiredIn
}

var injectedSuites atomic.Pointer[[]*TestSuite]

// injected is the suites tests added, in the order they were added; nil in
// the product binary, where nothing calls InjectForTest.
func injected() []*TestSuite {
	if p := injectedSuites.Load(); p != nil {
		return *p
	}
	return nil
}

// withInjected is the production table followed by the injected suites.
// Suites and Lookup answer from it, so an injected suite is visible to every
// package the test drives (config, placement, health, catalog rebuild).
func withInjected(table []Suite) []Suite {
	extra := injected()
	if len(extra) == 0 {
		return table
	}
	out := make([]Suite, 0, len(table)+len(extra))
	out = append(out, table...)
	for _, s := range extra {
		out = append(out, s)
	}
	return out
}

// InjectForTest adds base under id with status, for the life of t. id must
// start with TestSuitePrefix and must not be registered already. It is
// process-wide, so a test that calls it must not run in parallel with one
// that reads the registry.
func InjectForTest(t T, base Suite, id string, status Status, retiredIn string) *TestSuite {
	t.Helper()
	if !strings.HasPrefix(id, TestSuitePrefix) {
		t.Fatalf("copycrypt: an injected suite id must start with %q, got %q", TestSuitePrefix, id)
	}
	if base == nil {
		t.Fatalf("copycrypt: an injected suite needs a registered adapter to wrap")
	}
	if _, ok := Lookup(id); ok {
		t.Fatalf("copycrypt: the suite %q is registered already", id)
	}
	s := &TestSuite{Suite: base, id: id, status: status, retire: retiredIn}
	for {
		old := injectedSuites.Load()
		var next []*TestSuite
		if old != nil {
			next = append(next, *old...)
		}
		next = append(next, s)
		if injectedSuites.CompareAndSwap(old, &next) {
			break
		}
	}
	t.Cleanup(func() {
		for {
			old := injectedSuites.Load()
			if old == nil {
				return
			}
			var next []*TestSuite
			for _, x := range *old {
				if x != s {
					next = append(next, x)
				}
			}
			p := &next
			if len(next) == 0 {
				p = nil
			}
			if injectedSuites.CompareAndSwap(old, p) {
				return
			}
		}
	})
	return s
}

// String names the injected suite in a failure message.
func (s *TestSuite) String() string {
	return fmt.Sprintf("%s (test suite over %s, %s)", s.id, s.Suite.ID(), s.Status())
}
