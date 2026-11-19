package alert_test

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/alert"
)

// EPIC Q, Q1.4 (#982): the four conditions about a copy nobody has looked at
// lately and a copy that fails when somebody does.

func TestAssuranceConditionsOneConditionPerKindPerSet(t *testing.T) {
	got := alert.AssuranceConditions([]alert.AssuranceSignal{
		{BackupSet: "prod/db", ScrubOverdue: 3, ScrubFailing: 1, RecoveryTestOverdue: true},
		{BackupSet: "prod/media", RecoveryTestFailing: true},
		{BackupSet: "prod/quiet"},
	})
	var kinds []alert.Kind
	for _, c := range got {
		kinds = append(kinds, c.Kind)
		if c.Scope == "prod/quiet" {
			t.Errorf("a set with nothing wrong produced %v", c)
		}
		if strings.TrimSpace(c.Detail) == "" {
			t.Errorf("%s/%s has no detail", c.Scope, c.Kind)
		}
	}
	mustEqualKinds(t, "conditions", kinds, []alert.Kind{
		alert.ScrubOverdue, alert.ScrubFailing, alert.RecoveryTestOverdue,
		alert.RecoveryTestFailing,
	})
}

func TestAssuranceConditionsNeverCarryValidatorOutputOrPaths(t *testing.T) {
	hostile := "\x1b]0;pwned\x07/etc/shadow\nPASSWORD=hunter2"
	got := alert.AssuranceConditions([]alert.AssuranceSignal{{BackupSet: hostile, RecoveryTestFailing: true, ValidatorID: hostile}})
	if len(got) != 1 || got[0].Kind != alert.RecoveryTestFailing {
		t.Fatalf("got %+v", got)
	}
	for _, bad := range []string{"\x1b", "hunter2", "/etc/shadow", "\n"} {
		if strings.Contains(got[0].Detail, bad) || strings.Contains(got[0].Scope, bad) && bad != "/etc/shadow" {
			t.Errorf("detail %q leaks %q", got[0].Detail, bad)
		}
	}
}

func TestAssuranceKindsHaveTheirOwnHeadline(t *testing.T) {
	seen := map[string]alert.Kind{}
	for _, k := range []alert.Kind{alert.ScrubOverdue, alert.ScrubFailing, alert.RecoveryTestOverdue, alert.RecoveryTestFailing} {
		title := k.Title()
		if title == "retnd alert" || title == "" {
			t.Errorf("%s fell through to the generic headline", k)
		}
		if other, dup := seen[title]; dup {
			t.Errorf("%s and %s share the headline %q", k, other, title)
		}
		seen[title] = k
	}
}

// #1111: the product's own check is named as what it is, not as a validator
// an operator wrote.
func TestIssue1111_TheReservedIdentityIsRenderedAsTheBuiltInStructureCheck(t *testing.T) {
	got := alert.AssuranceConditions([]alert.AssuranceSignal{{BackupSet: "prod/db", RecoveryTestFailing: true, ValidatorID: "retnd-builtin-structure"}})
	if len(got) != 1 {
		t.Fatalf("conditions = %+v", got)
	}
	if !strings.Contains(got[0].Detail, "(the built-in structure check)") || strings.Contains(got[0].Detail, "validator retnd-builtin-structure") {
		t.Errorf("detail = %q", got[0].Detail)
	}
	other := alert.AssuranceConditions([]alert.AssuranceSignal{{BackupSet: "prod/db", RecoveryTestFailing: true, ValidatorID: "pg-check"}})
	if !strings.Contains(other[0].Detail, "(validator pg-check)") {
		t.Errorf("an operator's validator lost its clause: %q", other[0].Detail)
	}
}
