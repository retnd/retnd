package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/service"
)

// `retnd recovery-test <source/backup-set>` (EPIC Q, #980): prove now that the
// newest known good copy of a set can be got back and used.
//
// It is the terminal's door to the test the scheduler runs, and the same code
// as POST /api/v1/backup-sets/{source}/{set}/recovery-test: the copy is staged
// by the recovery engine, hashed and matched against the digest recorded for
// the artifact before the operator's validator is allowed to mean anything,
// and the result is written on the copy's recovery claim. The route starts the
// test in the background and answers at once; here the terminal waits, because
// the exit status is the answer.
//
// A read that costs money (a download from a billed object store, or an
// archived object) is refused unless --acknowledge-cost says it is agreed,
// and a test never starts a provider restore. Exit status:
//
//	0  the copy passed
//	1  the copy failed the digest or the validator, or the test could not be
//	   completed (nothing is known to be wrong with the copy in that case,
//	   but nothing was proved either)
//	10 the read was refused because it costs money (the same code recover uses)
func cmdRecoveryTest(args []string) int {
	fs, cfgPath := newFlagSet("recovery-test")
	artifact := fs.String("artifact", "", "test this artifact (source/backup-set/name) instead of the newest known good one")
	ack := fs.Bool("acknowledge-cost", false, "accept a read that costs money (a billed download, or an archived object that is already readable)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("recovery-test: expected <source/backup-set>")
	}
	set, err := model.ParseBackupSetID(operands[0])
	if err != nil || strings.Count(operands[0], "/") != 1 {
		return usageError("recovery-test: %q is not <source/backup-set>", operands[0])
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc, cleanup, err := openBackupServiceLoggingTo(ctx, *cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	logStartup(ctx, obs.New(os.Stderr, obs.LevelFromEnv()), app.BuildVersionInfo(version, commit))

	out, err := svc.RunRecoveryTest(ctx, set, service.RecoveryTestOptions{ArtifactID: *artifact, AcknowledgeCost: *ack})
	if err != nil {
		return fail(err)
	}
	printRecoveryTest(out, *asJSON)
	switch out.State {
	case state.RecoveryPassed:
		return exitOK
	case state.RecoveryRefusedForCost:
		return recoverExitCodes[service.RecoveryCostNotAcknowledged]
	}
	if out.ReadOnly {
		return exitOK
	}
	return 1
}

func printRecoveryTest(o service.RecoveryTestOutcome, asJSON bool) {
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"backup_set_id": o.BackupSet, "artifact_id": o.Artifact, "copy": o.Medium, "state": o.State,
			"digest_matched": o.DigestMatched, "validator": o.ValidatorID, "cleanup": o.CleanupState, "reason": o.Reason,
			"read_only": o.ReadOnly,
		})
		return
	}
	if o.ReadOnly {
		fmt.Printf("%s: an incremental backup set; its restore drill is read from its snapshot history, and nothing was run (%s)\n", o.BackupSet, o.State)
		return
	}
	fmt.Printf("recovery test:       %s\n", o.State)
	fmt.Printf("backup:              %s\n", o.Artifact)
	fmt.Printf("copy:                %s\n", o.Medium)
	switch {
	case o.DigestMatched == nil:
		fmt.Printf("digest:              not compared\n")
	case *o.DigestMatched:
		fmt.Printf("digest:              matched what was recorded when the backup was ingested\n")
	default:
		fmt.Printf("digest:              DID NOT MATCH what was recorded; the validator was not run\n")
	}
	if o.ValidatorID != "" {
		fmt.Printf("validator:           %s\n", o.ValidatorID)
	}
	if o.Reason != "" {
		fmt.Printf("reason:              %s\n", o.Reason)
	}
	if o.CleanupState != "" {
		fmt.Printf("staged copy:         %s\n", o.CleanupState)
	}
}
