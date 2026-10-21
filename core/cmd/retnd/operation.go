package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/model"
)

// `retnd operation list` and `retnd operation show <id> [--follow]` (EPIC S,
// #1149), and `retnd operation run [--backup-set S] [--follow]` (#1250).
//
// The API lists and shows operations (GET /operations, GET /operations/{id})
// and `assurance cancel <op>` takes an operation id, but nothing in the
// terminal could say which ids exist. A scrub or recovery test started in the
// engine is async there, so a terminal also could not poll or watch it.
//
// Both verbs are routed and refuse without a route, for the reason `assurance
// cancel` does: an operation is a row in the memory of the process serving the
// deployment, so with nothing serving there is nothing to list, and answering
// "none" from a world where the question has no meaning would be a lie.
//
// The API takes no filter, so --backup-set and --action narrow the list here.
// --json prints the object the API served, unchanged, for the narrowed list.
//
// `operation run` is the twin of the Dashboard's "Run all enabled sets" and
// the set page's "Run this backup set", which POST /operations with run_cycle
// or run_backup_set. It is not `run` or `fetch --backup-set`, and those stay
// what they are: they run the cycle in this process against the local journal,
// and a script that relies on that must not start doing something else because
// RETND_API_URL happens to be set. This one asks the serving engine, so the run
// takes the engine's single-flight lock, shows in its feeds and is a row in
// `operation list`. It is routed for the same reason list and show are.

// operationPollEvery is how often --follow asks again. A variable so a test
// need not wait whole seconds.
var operationPollEvery = time.Second

// The waits the submit and the follow sit out (#1250's review). Declared
// ahead of the behaviour that uses them so the tests for it can shorten them.
var (
	operationSubmitRetryWait = time.Second
	operationSubmitDeadline  = 2 * time.Minute
	operationFollowPatience  = 3 * time.Minute
	operationFollowRetryMax  = 30 * time.Second
)

// isLoopbackHost says whether a route's host is this machine. A variable so
// a test can stand an engine on 127.0.0.1 and have it treated as remote.
var isLoopbackHost = func(string) bool { return true }

// exitEngineGateShut: `operation run` was refused by the engine's destructive
// gate (DESTRUCTIVE_OPERATIONS_DISABLED), so nothing was started. Its own code
// because it is not a failure that retrying fixes and not one the command line
// caused: the run has to be started on the host with `run` or `fetch`, which
// is what the sentence beside it says. 50 rather than the next free number
// under it, so recover's blocks (10 to 43) have room to grow (#1250).
const exitEngineGateShut = 50

func cmdOperation(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("operation: expected `operation list`, `operation show <operation-id>` or `operation run`")
	}
	switch args[0] {
	case "list":
		return cmdOperationList(args[1:])
	case "show":
		return cmdOperationShow(args[1:])
	case "run":
		return cmdOperationRun(args[1:])
	}
	return usageError("operation: unknown subcommand %q, expected `list`, `show` or `run`", args[0])
}

func operationRoute(ctx context.Context, cfgPath string) (readDecision, int) {
	cfg, err := config.LoadAndValidate(cfgPath)
	if err != nil {
		return readDecision{}, fail(fmt.Errorf("config: %w", err))
	}
	mode, err := enterReadMode(ctx, cfgPath, cfg, os.Stderr)
	if err != nil {
		return readDecision{}, fail(err)
	}
	if !mode.attached() {
		return readDecision{}, fail(fmt.Errorf("operations are held in the memory of the process serving this deployment, and this command has no route to one, so there is nothing to read or start. Set %s and the two credential variables beside it to reach that process", apiURLEnv))
	}
	return mode, -1
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cmdOperationList(args []string) int {
	fs, cfgPath := newFlagSet("operation list")
	asJSON := fs.Bool("json", false, "print the engine's list as JSON, the same object the API serves")
	set := fs.String("backup-set", "", "only operations of this backup set (source/name)")
	action := fs.String("action", "", "only operations of this action (for example run_cycle or scrub)")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("operation list: takes no operands, narrow it with --backup-set and --action")
	}
	if *set != "" {
		if _, err := model.ParseBackupSetID(*set); err != nil {
			return usageError("operation list: --backup-set %q is not a backup set id (source/name)", *set)
		}
	}
	ctx := context.Background()
	mode, code := operationRoute(ctx, *cfgPath)
	if code >= 0 {
		return code
	}
	got, err := mode.client.ListOperations(ctx)
	if err != nil {
		return fail(fmt.Errorf("listing operations: %w", err))
	}
	kept := make([]apicontract.Operation, 0, len(got.Operations))
	for _, op := range got.Operations {
		if *set != "" && op.BackupSetID != *set {
			continue
		}
		if *action != "" && op.Action != *action {
			continue
		}
		kept = append(kept, op)
	}
	got.Operations = kept
	if *asJSON {
		if err := printJSON(got); err != nil {
			return fail(fmt.Errorf("operation list: writing the result: %w", err))
		}
		return exitOK
	}
	if len(kept) == 0 {
		fmt.Println("no operations")
		return exitOK
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "OPERATION\tACTION\tSTATUS\tBACKUP SET\tCREATED")
	for _, op := range kept {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", op.OperationID, dash(op.Action), op.Status, dash(op.BackupSetID), dash(op.CreatedAt))
	}
	if err := tw.Flush(); err != nil {
		return fail(err)
	}
	return exitOK
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdOperationShow(args []string) int {
	fs, cfgPath := newFlagSet("operation show")
	asJSON := fs.Bool("json", false, "print the operation as JSON, the same object the API serves")
	follow := fs.Bool("follow", false, "poll until the operation finishes, printing each change of status on standard error; the exit status is 0 for completed and 1 otherwise")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("operation show: expected exactly one operation id, as `operation list` prints it")
	}
	id := operands[0]
	ctx := context.Background()
	mode, code := operationRoute(ctx, *cfgPath)
	if code >= 0 {
		return code
	}
	op, err := mode.client.GetOperation(ctx, id)
	if err != nil {
		return fail(fmt.Errorf("reading %s: %w", id, err))
	}
	return finishOperation(ctx, mode, op, *follow, *asJSON, "operation show")
}

// finishOperation is what show and run do once they hold an operation: with
// follow, poll until it is no longer queued or running, printing each change
// of status on standard error; then print it, and with follow take the exit
// status from how it ended (0 for completed, 1 for anything else).
func finishOperation(ctx context.Context, mode readDecision, op apicontract.Operation, follow, asJSON bool, verb string) int {
	if follow {
		var err error
		last := ""
		for op.Status == "queued" || op.Status == "running" {
			if op.Status != last {
				fmt.Fprintf(os.Stderr, "%s: %s\n", op.OperationID, op.Status)
				last = op.Status
			}
			select {
			case <-ctx.Done():
				return fail(ctx.Err())
			case <-time.After(operationPollEvery):
			}
			if op, err = mode.client.GetOperation(ctx, op.OperationID); err != nil {
				return fail(fmt.Errorf("reading %s: %w", op.OperationID, err))
			}
		}
	}
	if asJSON {
		if err := printJSON(op); err != nil {
			return fail(fmt.Errorf("%s: writing the result: %w", verb, err))
		}
	} else {
		writeOperation(os.Stdout, op)
	}
	if follow && op.Status != "completed" {
		return exitFailure
	}
	return exitOK
}

func cmdOperationRun(args []string) int {
	fs, cfgPath := newFlagSet("operation run")
	asJSON := fs.Bool("json", false, "print the operation the engine started as JSON, the same object the API answers with")
	follow := fs.Bool("follow", false, "poll until the run finishes, printing each change of status on standard error; the exit status is 0 for completed and 1 otherwise")
	set := fs.String("backup-set", "", "run only this backup set (source/name), as run_backup_set; without it every enabled set, as run_cycle")
	skipScripts := fs.Bool("skip-workflow-scripts", false, "with --backup-set: run the set without its workflow scripts, as fetch --skip-workflow-scripts does")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("operation run: takes no operands, name one set with --backup-set")
	}
	if *set != "" {
		if _, err := model.ParseBackupSetID(*set); err != nil {
			return usageError("operation run: --backup-set %q is not a backup set id (source/name)", *set)
		}
	}
	if *skipScripts && *set == "" {
		// The engine refuses skip_workflow_scripts on a run_cycle as a
		// parameter that action does not own, so it is wrong here already.
		return usageError("operation run: --skip-workflow-scripts needs --backup-set, a run of every set has no such option")
	}
	ctx := context.Background()
	mode, code := operationRoute(ctx, *cfgPath)
	if code >= 0 {
		return code
	}

	// The revision the engine holds now, which is what the run buttons send
	// too: a run is checked against the configuration it was asked for.
	served, err := mode.client.SystemVersion(ctx)
	if err != nil {
		return fail(fmt.Errorf("reading the engine's configuration revision: %w", err))
	}
	req := apicontract.SubmitOperationRequest{Action: apicontract.ActionRunCycle, ConfigRevision: served.ConfigRevision}
	local := cliecho.Binary + " run"
	if *set != "" {
		req.Action = apicontract.ActionRunBackupSet
		req.BackupSetID = *set
		req.SkipWorkflowScripts = *skipScripts
		local = cliecho.Binary + " fetch --backup-set " + *set
	}

	// Fresh per invocation, for snapshot.go's reason: two runs typed at a
	// keyboard are two requests, and a reused key would hand the second one
	// the first run's operation.
	key := "cli-" + strings.ReplaceAll(req.Action, "_", "-") + ":" + uuid.NewString()
	op, err := mode.client.SubmitOperation(ctx, key, req)
	if err != nil {
		var refused *apiclient.Error
		if errors.As(err, &refused) && refused.Code == apicontract.ErrorCodeDestructiveOperationsDisabled {
			fmt.Fprintf(os.Stderr, "error: the engine's destructive gate refused the run (%s), so nothing was started. `%s` on the host runs it in that process instead: %s\n",
				refused.Code, local, refused.Message)
			return exitEngineGateShut
		}
		return fail(fmt.Errorf("starting the run: %w", err))
	}
	return finishOperation(ctx, mode, op, *follow, *asJSON, "operation run")
}

func writeOperation(w io.Writer, op apicontract.Operation) {
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "%-12s %s\n", k+":", v)
		}
	}
	row("operation", op.OperationID)
	row("action", op.Action)
	row("status", op.Status)
	row("backup set", op.BackupSetID)
	row("actor", op.Actor)
	row("created", op.CreatedAt)
	row("started", op.StartedAt)
	row("finished", op.FinishedAt)
	row("result", op.Result)
	row("error", op.Error)
	if p := op.Progress; p != nil {
		row("progress", fmt.Sprintf("%s, %d artifacts done, %d of %d backup sets", p.Artifact, p.ArtifactsDone, p.BackupSetsDone, p.BackupSetsTotal))
	}
	if c := op.Cycle; c != nil {
		row("cycle", fmt.Sprintf("%d backup sets, %d artifacts walked, %d through", c.BackupSetsProcessed, c.ArtifactsWalked, c.ArtifactsThrough))
	}
	if r := op.Restore; r != nil {
		row("restore", strings.TrimSpace(r.ArtifactID+" "+r.Medium+" "+r.Detail))
	}
	if n := len(op.Snapshots); n > 0 {
		row("snapshots", fmt.Sprint(n))
	}
}
