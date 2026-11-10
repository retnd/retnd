package keycustody_test

import (
	"context"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/keycustody"
)

// #1294: a managed policy with no active key is its own sentence, not "no key
// provider is declared", which says the operator forgot to configure
// something when the truth is that nobody has created a key yet.
//
// Written by reading; not run.

func TestAManagedPolicyWithNoActiveKeySaysSo_1294(t *testing.T) {
	s := openStore(t, t.TempDir())
	conds, unevaluated := s.AlertConditions(context.Background(), []keycustody.SetPolicy{
		{Set: "nas-a/postgres", Required: true, Managed: true},
	})
	if len(unevaluated) != 0 {
		t.Fatalf("unevaluated = %v, want none: nothing here could not be looked at", unevaluated)
	}
	if len(conds) != 1 || conds[0].Kind != alert.EncryptionKeyUnresolvable || conds[0].Scope != "nas-a/postgres" {
		t.Fatalf("conditions = %+v, want one ENCRYPTION_KEY_UNRESOLVABLE scoped to the set", conds)
	}
	d := conds[0].Detail
	if !strings.Contains(d, "no managed key is active") {
		t.Errorf("Detail = %q, want it to say no managed key is active", d)
	}
	if strings.Contains(d, "no key provider is declared") || strings.Contains(d, "no key provider)") {
		t.Errorf("Detail = %q still talks about an undeclared provider", d)
	}
	if !strings.Contains(d, "refused") {
		t.Errorf("Detail = %q does not say copies are being refused", d)
	}
}

func TestAPolicyWithNoProviderAtAllKeepsTheUndeclaredSentence_1294(t *testing.T) {
	s := openStore(t, t.TempDir())
	conds, _ := s.AlertConditions(context.Background(), []keycustody.SetPolicy{{Set: "nas-a/postgres", Required: true}})
	if len(conds) != 1 || !strings.Contains(conds[0].Detail, "no key provider is declared") {
		t.Fatalf("conditions = %+v, want the undeclared-provider sentence for a policy that names neither provider", conds)
	}
}

func TestUnresolvableConditionIsTheOneSentenceBothBuildersUse_1294(t *testing.T) {
	c := keycustody.UnresolvableCondition("nas-a/postgres", "an external key from env X", "something")
	if c.Kind != alert.EncryptionKeyUnresolvable || c.Scope != "nas-a/postgres" {
		t.Fatalf("condition = %+v", c)
	}
	for _, want := range []string{"nas-a/postgres", "an external key from env X", "something", "refused", "nothing is deleted"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("Detail = %q lacks %q", c.Detail, want)
		}
	}
}
