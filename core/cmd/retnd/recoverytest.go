package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/recoverytest"
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
	follow := fs.Bool("follow", false, "print the test's stages on standard error as it passes through them; the exit status is the test's")
	showOutput := fs.Bool("show-output", false, "print what the newest recovery test of the set printed, as stored; runs no test")
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
	if *showOutput && (*follow || *ack || *artifact != "") {
		return usageError("recovery-test: --show-output reads the stored output and runs no test, so --follow, --acknowledge-cost and --artifact have nothing to apply to")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc, cleanup, err := openBackupServiceLoggingTo(ctx, *cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	logStartup(ctx, obs.New(os.Stderr, obs.LevelFromEnv()), app.BuildVersionInfo(version, commit))

	if *showOutput {
		return showRecoveryTestOutput(ctx, svc, set, *asJSON)
	}

	opts := service.RecoveryTestOptions{ArtifactID: *artifact, AcknowledgeCost: *ack}
	var out service.RecoveryTestOutcome
	if *follow {
		opts.OperationID = uuid.NewString()
		followWhile(ctx, svc, opByID(opts.OperationID), os.Stderr, os.Stderr, func() {
			out, err = svc.RunRecoveryTest(ctx, set, opts)
		})
	} else {
		out, err = svc.RunRecoveryTest(ctx, set, opts)
	}
	if err != nil {
		return fail(err)
	}
	printRecoveryTest(out, *asJSON)
	switch out.State {
	case state.RecoveryPassed, service.WireRecoveryReadable:
		return exitOK
	case state.RecoveryRefusedForCost:
		return recoverExitCodes[service.RecoveryCostNotAcknowledged]
	}
	if out.ReadOnly {
		return exitOK
	}
	return 1
}

// checkName is "builtin" for a verdict the product's structure check produced
// and "validator" for the operator's. A run that reached neither (a refusal
// before anything ran) names neither.
func checkName(o service.RecoveryTestOutcome) string {
	switch {
	case o.Check == recoverytest.CheckBuiltin:
		return "builtin"
	case o.ValidatorID != "":
		return "validator"
	}
	return ""
}

func printRecoveryTest(o service.RecoveryTestOutcome, asJSON bool) {
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"backup_set_id": o.BackupSet, "artifact_id": o.Artifact, "copy": o.Medium, "state": o.State,
			"digest_matched": o.DigestMatched, "validator": o.ValidatorID, "cleanup": o.CleanupState, "reason": o.Reason,
			"read_only": o.ReadOnly, "check": checkName(o),
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
	if o.Check == recoverytest.CheckBuiltin {
		fmt.Printf("check:               built-in structure check (no validator is declared for this backup set)\n")
		if o.CheckFormat != "" {
			fmt.Printf("format:              %s\n", o.CheckFormat)
		}
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

// showRecoveryTestOutput is `recovery-test <set> --show-output`: the newest
// test's output as the journal holds it, which is the same body
// GET /backup-sets/{source}/{set}/recovery-test/output serves. It reads the
// journal and runs nothing, so it works beside a serving engine and with
// nothing serving alike.
//
// The text is printed as stored. The records were made inert before they were
// written (no control byte survives into them), so nothing here needs to
// escape anything, and the runner's truncation record is printed as itself
// while a line that merely quotes it is printed as output.
func showRecoveryTestOutput(ctx context.Context, svc *service.BackupService, set model.BackupSetID, asJSON bool) int {
	out, err := svc.RecoveryTestOutput(ctx, set.String())
	if err != nil {
		return fail(err)
	}
	records := make([]apicontract.RecoveryTestOutputRecord, 0, len(out.Records))
	rendered := make([]recoverytest.OutputRecord, 0, len(out.Records))
	for _, r := range out.Records {
		records = append(records, apicontract.RecoveryTestOutputRecord{Kind: r.Kind, Text: r.Text})
		rendered = append(rendered, recoverytest.OutputRecord{Kind: recoverytest.OutputKind(r.Kind), Text: r.Text})
	}
	if asJSON {
		body := apicontract.RecoveryTestOutput{BackupSetID: out.BackupSetID, Recorded: out.Recorded, Truncated: out.Truncated, Records: records}
		if out.Recorded {
			body.OperationID, body.ArtifactID, body.Medium, body.State = out.OperationID, out.ArtifactID, out.Medium, out.State
			body.RecordedAt = out.RecordedAt.UTC().Format(time.RFC3339)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(body); err != nil {
			return fail(fmt.Errorf("recovery-test: writing the output: %w", err))
		}
		return exitOK
	}
	if !out.Recorded {
		fmt.Printf("no recovery test of %s has recorded output yet\n", out.BackupSetID)
		return exitOK
	}
	fmt.Printf("%s: %s on %s, %s (operation %s)\n", out.ArtifactID, out.State, out.Medium, out.RecordedAt.UTC().Format(time.RFC3339), out.OperationID)
	if len(rendered) == 0 {
		fmt.Println("the validator printed nothing")
		return exitOK
	}
	for _, line := range recoverytest.Render(rendered) {
		fmt.Println(line)
	}
	return exitOK
}
