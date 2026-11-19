package config

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
)

// EPIC T, T2.1 (#1316): a retired suite's Load refusal names the version
// that retired it, read from the registry's table entry and never typed in
// the message. The suite comes through the registry's own test hook, so
// this is the path a real retirement takes (copycrypt.Lookup), not
// lookupSuite's seam.
//
// Written by reading; not run (the suites run on the release PR).
func TestARetiredSuiteRefusalNamesTheVersionThatRetiredIt(t *testing.T) {
	ts := copycrypt.InjectForTest(t, copycrypt.Default(), copycrypt.TestSuitePrefix+"retired/v1", copycrypt.StatusRetired, "0.9.0")
	_, err := loadValidated(t, suiteYAML(ts.ID()))
	if err == nil {
		t.Fatal("a retired suite loaded")
	}
	for _, want := range []string{ts.ID(), "retired in 0.9.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}
