package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// #1246: the text listing says where scripts are kept and whether that root is
// the mounted default, as the Settings card and `settings workflow` do.
func TestValidatorListSaysTheRootIsTheDefault(t *testing.T) {
	var buf bytes.Buffer
	printCustomValidatorList(&buf, service.CustomValidators{
		Available: true, RunnerConfigured: true, WorkflowRoot: "/workflows", WorkflowRootDefault: true,
		Directory: "recovery-test-validators", ScriptsDir: "/workflows/recovery-test-validators",
	})
	out := buf.String()
	if !strings.Contains(out, "/workflows/recovery-test-validators") || !strings.Contains(out, "the default") {
		t.Errorf("the listing does not say the scripts are under the default root:\n%s", out)
	}
}

func TestValidatorListDoesNotCallAConfiguredRootTheDefault(t *testing.T) {
	var buf bytes.Buffer
	printCustomValidatorList(&buf, service.CustomValidators{
		Available: true, RunnerConfigured: true, WorkflowRoot: "/srv/hooks", Directory: "recovery-test-validators",
		ScriptsDir: "/srv/hooks/recovery-test-validators",
	})
	out := buf.String()
	if !strings.Contains(out, "/srv/hooks/recovery-test-validators") {
		t.Errorf("the listing does not say where scripts are kept:\n%s", out)
	}
	if strings.Contains(out, "default") {
		t.Errorf("a configured root is called the default:\n%s", out)
	}
}
