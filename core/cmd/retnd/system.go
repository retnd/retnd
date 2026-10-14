package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/retnd/retnd/core/internal/config"
)

// `retnd system capabilities [--json]` (EPIC S, #1165).
//
// GET /api/v1/system/capabilities says what the serving engine's platform can
// do and, since #1165, whether its destructive gate is open. The gate is the
// engine's: the terminal has none, which is the whole reason the web
// interface sends people here for a run, a restore or a retention apply. So
// the only meaningful answer to "is the gate open" comes from the process
// that serves the deployment, and this verb is routed and refuses without a
// route, the way `operation list` does. Answering from a CLI process would
// report a platform it is not and a gate it does not have.
//
// --json prints the object the API served, unchanged.

func cmdSystem(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("system: expected `system capabilities`")
	}
	switch args[0] {
	case "capabilities":
		return cmdSystemCapabilities(args[1:])
	}
	return usageError("system: unknown subcommand %q, expected `capabilities`", args[0])
}

func cmdSystemCapabilities(args []string) int {
	fs, cfgPath := newFlagSet("system capabilities")
	asJSON := fs.Bool("json", false, "print the engine's answer as JSON, the same object the API serves")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("system capabilities: takes no operands")
	}
	ctx := context.Background()
	cfg, err := config.LoadAndValidate(*cfgPath)
	if err != nil {
		return fail(fmt.Errorf("config: %w", err))
	}
	mode, err := enterReadMode(ctx, *cfgPath, cfg, os.Stderr)
	if err != nil {
		return fail(err)
	}
	if !mode.attached() {
		return fail(fmt.Errorf("system capabilities asks the process serving this deployment, because what it reports (the platform, and whether its destructive gate is open) belongs to that process and this one has neither, and this command has no route to one. Set %s and the two credential variables beside it to reach that process", apiURLEnv))
	}
	got, err := mode.client.GetCapabilities(ctx)
	if err != nil {
		return fail(fmt.Errorf("reading capabilities: %w", err))
	}
	if *asJSON {
		if err := printJSON(got); err != nil {
			return fail(fmt.Errorf("system capabilities: writing the result: %w", err))
		}
		return exitOK
	}
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	fmt.Printf("platform: %s\n", got.Platform)
	fmt.Printf("native auth: %s\n", yesNo(got.NativeAuth))
	fmt.Printf("native notifications: %s\n", yesNo(got.NativeNotifications))
	fmt.Printf("storage picker: %s\n", yesNo(got.StoragePicker))
	fmt.Printf("embedded window: %s\n", yesNo(got.EmbeddedWindow))
	fmt.Printf("app store packaging: %s\n", yesNo(got.AppStorePackaging))
	gate := "destructive operations: " + got.DestructiveOperations.State
	if got.DestructiveOperations.Reason != "" {
		gate += " (" + got.DestructiveOperations.Reason + ")"
	}
	fmt.Println(gate)
	return exitOK
}
