package main

import (
	"flag"
	"strings"
	"testing"
)

// Issue #1150: usage() had drifted from the code it describes in three
// places the parity audit found. These tests read the usage text the same
// way an operator does, and compare it with the flag sets the verbs really
// register, so the next flag added without a synopsis entry fails here.

// usageBlock returns the part of usage() that starts at the line beginning
// with from and stops before the next line that starts with one of the
// stop prefixes. A block is the synopsis plus the prose under it.
func usageBlock(t *testing.T, text, from string, stops ...string) string {
	t.Helper()
	start := strings.Index(text, "\n"+from)
	if start < 0 {
		t.Fatalf("usage() has no entry beginning %q:\n%s", from, text)
	}
	rest := text[start+1:]
	end := len(rest)
	for _, stop := range stops {
		if i := strings.Index(rest[len(from):], "\n"+stop); i >= 0 && i+len(from) < end {
			end = i + len(from)
		}
	}
	return rest[:end]
}

func TestUsage1150_AssuranceWritesAreNotDescribedAsRefused(t *testing.T) {
	text := captureStderr(t, usage)
	blocks := map[string]string{
		"settings assurance patch": usageBlock(t, text, "  settings assurance patch", "  settings assurance due"),
		"backup-set assurance":     usageBlock(t, text, "  backup-set assurance <source/backup-set> [--scrub-cadence", "  backup-set workflow "),
	}
	for name, block := range blocks {
		for _, stale := range []string{"refused beside", "no route", "has no route"} {
			if strings.Contains(strings.ToLower(block), stale) {
				t.Errorf("usage() still says %q under %s, but that write is routed to a serving engine (assurance.go):\n%s", stale, name, block)
			}
		}
	}
}

func TestUsage1150_EveryAssuranceFlagIsInItsSynopsis(t *testing.T) {
	text := captureStderr(t, usage)
	setBlock := usageBlock(t, text, "  backup-set assurance <source/backup-set> [--scrub-cadence", "  backup-set workflow ")
	patchBlock := usageBlock(t, text, "  settings assurance patch", "  settings assurance due")

	for _, name := range setScopeAssuranceFlags {
		if !strings.Contains(setBlock, "--"+name) {
			t.Errorf("`backup-set assurance` accepts --%s and its usage() synopsis does not name it:\n%s", name, setBlock)
		}
	}
	for _, name := range append(append([]string(nil), setScopeAssuranceFlags...), globalOnlyAssuranceFlags...) {
		if !strings.Contains(patchBlock, "--"+name) {
			t.Errorf("`settings assurance patch` accepts --%s and its usage() synopsis does not name it:\n%s", name, patchBlock)
		}
	}
}

// Every flag the create verb registers for create must be in the create
// synopsis. Reading the registered FlagSet, rather than a list in this
// file, is what keeps the check from going stale the way the synopsis did.
func TestUsage1150_EveryCreateFlagIsInTheCreateSynopsis(t *testing.T) {
	text := captureStderr(t, usage)
	block := usageBlock(t, text, "  backup-set create ", "  backup-set patch ")
	synopsis := block
	if i := strings.Index(block, "create a backup set."); i >= 0 {
		synopsis = block[:i]
	}

	f := declareBackupSetFlags()
	var checked int
	f.fs.VisitAll(func(fl *flag.Flag) {
		// Only flags whose own help says they apply to create.
		if !strings.HasPrefix(fl.Usage, "create") {
			return
		}
		checked++
		if !strings.Contains(synopsis, "--"+fl.Name) {
			t.Errorf("`backup-set create` registers --%s (%q) and the usage() synopsis does not name it:\n%s", fl.Name, fl.Usage, synopsis)
		}
	})
	if checked < 10 {
		t.Fatalf("only %d create flags were found; this test would pass vacuously", checked)
	}
}
