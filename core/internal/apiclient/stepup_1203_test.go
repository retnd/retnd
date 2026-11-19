package apiclient

import (
	"context"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1203: the custom validator upload and edit ask for the administrator's
// password again. A terminal signs in with that password, so it answers the
// step-up itself, on exactly those two operations and nowhere else.

func TestClient_TheValidatorWritesCarryTheStepUpAndNothingElseDoes(t *testing.T) {
	engine := newFakeEngine(t)
	client := engine.client(t, engine.start())
	ctx := context.Background()

	if _, err := client.CreateCustomValidator(ctx, apicontract.CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: "#!/usr/bin/env bash\n"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := client.UpdateCustomValidator(ctx, "v", apicontract.UpdateCustomValidatorRequest{Pattern: "^b"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := client.RemoveCustomValidator(ctx, "v"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := client.ListBackupSets(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range engine.requests() {
		want := ""
		if r.Operation == "createCustomValidator" || r.Operation == "updateCustomValidator" {
			want = engine.password
		}
		if r.StepUp != want {
			t.Errorf("%s carried step-up %q, want %q", r.Operation, r.StepUp, want)
		}
		if r.Operation == "updateCustomValidator" && r.Path != "/api/v1/assurance/validators/v" {
			t.Errorf("update went to %s", r.Path)
		}
	}
}
