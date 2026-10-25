package apiclient

import (
	"context"
	"errors"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1250: POST /operations requires an Idempotency-Key, and until `operation
// run` nothing in this client could send one. The key goes on exactly the
// submission it was handed for, and a submission with no key is refused here
// rather than sent for the engine to refuse.

func TestClient1250_SubmitOperationCarriesItsKeyAndNothingElseDoes(t *testing.T) {
	engine := newFakeEngine(t)
	client := engine.client(t, engine.start())
	ctx := context.Background()

	req := apicontract.SubmitOperationRequest{Action: apicontract.ActionRunCycle, ConfigRevision: "rev-1"}
	if _, err := client.SubmitOperation(ctx, "cli-run-cycle:k1", req); err != nil {
		t.Fatalf("SubmitOperation: %v", err)
	}
	if _, err := client.ListOperations(ctx); err != nil {
		t.Fatal(err)
	}
	submitted := 0
	for _, r := range engine.requests() {
		want := ""
		if r.Operation == "submitOperation" {
			submitted++
			want = "cli-run-cycle:k1"
		}
		if r.IdempotencyKey != want {
			t.Errorf("%s carried Idempotency-Key %q, want %q", r.Operation, r.IdempotencyKey, want)
		}
	}
	if submitted != 1 {
		t.Fatalf("the engine saw %d submissions, want 1", submitted)
	}
}

func TestClient1250_ASubmissionWithNoKeyIsNeverSent(t *testing.T) {
	engine := newFakeEngine(t)
	client := engine.client(t, engine.start())

	_, err := client.SubmitOperation(context.Background(), "", apicontract.SubmitOperationRequest{Action: apicontract.ActionRunCycle, ConfigRevision: "rev-1"})
	var violation *ContractViolation
	if !errors.As(err, &violation) {
		t.Fatalf("error is %T (%v), want a *ContractViolation", err, err)
	}
	for _, r := range engine.requests() {
		if r.Operation == "submitOperation" {
			t.Fatal("a submission with no key reached the engine, which answers it with a 400 an operator cannot act on")
		}
	}
}
