package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/retnd/retnd/core/internal/config"
)

// `retnd assurance cancel <operation-id>` (EPIC Q, #980, #1071): stop a
// running scrub or recovery test.
//
// # Why this is routed, and refuses without a route
//
// A running scrub or recovery test is the work of the process serving the
// deployment, and its cancel function lives in that process's memory (see
// core/service's assuranceoperations.go). With nothing serving there is
// nothing running, so there is nothing to cancel and nothing on disk that
// could be consulted: this refuses rather than answering "not found" from a
// world where the question has no meaning, for the reason `backup-set
// edit-hold` refuses.
//
// It is the same call as POST /api/v1/assurance/operations/{op}/cancel, which
// is deliberately not under /operations: stopping a read deletes nothing, so
// it does not need the destructive gate.
//
// Only `cancel` exists under `assurance` today. Starting a scrub or a recovery
// test is `scrub` and `recovery-test`, and reading the policy is `settings
// assurance`; none of those is a subcommand of this verb.
func cmdAssurance(args []string) int {
	fs, cfgPath := newFlagSet("assurance cancel")
	asJSON := fs.Bool("json", false, "print the engine's answer as JSON, the same object the API serves")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) == 0 || operands[0] != "cancel" {
		return usageError("assurance: expected `assurance cancel <operation-id>`")
	}
	if len(operands) != 2 {
		return usageError("assurance cancel: expected exactly one operation id, the one the scrub or recovery test returned when it started")
	}
	opID := operands[1]

	cfg, err := config.LoadAndValidate(*cfgPath)
	if err != nil {
		return fail(fmt.Errorf("config: %w", err))
	}
	ctx := context.Background()
	mode, err := enterReadMode(ctx, *cfgPath, cfg, os.Stderr)
	if err != nil {
		return fail(err)
	}
	if !mode.attached() {
		return fail(fmt.Errorf("a running scrub or recovery test belongs to the process serving this deployment, and this command has no route to one, so there is nothing to cancel. Set %s and the two credential variables beside it to reach that process", apiURLEnv))
	}

	got, err := mode.client.CancelAssuranceOperation(ctx, opID)
	if err != nil {
		return fail(fmt.Errorf("cancelling %s: %w", opID, err))
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(got); err != nil {
			return fail(fmt.Errorf("assurance cancel: writing the result: %w", err))
		}
		return exitOK
	}
	fmt.Printf("%s of %s (%s): %s\n", got.Kind, got.BackupSetID, got.OperationID, got.Status)
	return exitOK
}
