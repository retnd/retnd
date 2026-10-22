package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
// All three are routed and refuse without a route, for the reason `assurance
// cancel` does: an operation is a row in the memory of the process serving the
// deployment, so with nothing serving there is nothing to list, and answering
// "none" from a world where the question has no meaning would be a lie. From
// a terminal with no deployment of its own they go remote (remote.go).
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

// interruptContext is ctx cancelled by Ctrl-C or SIGTERM, for a follow. A
// variable because a test cannot send this process a real SIGINT: once
// anything has run a transfer, the embedded rclone's own handler ends the
// whole test binary on one (internal/transport/rclone/signalexit.go).
var interruptContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}

// isLoopbackHost says whether a route's host is this machine (remote.go). A
// variable so a test can stand an engine on 127.0.0.1 and have it treated
// as somewhere else.
var isLoopbackHost = loopbackHost

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

// operationRoute is the client the three verbs ask, remote or beside this
// host's own engine, or the exit code of the refusal it already printed.
// starts is true for operation run, which wants a remote deployment pinned.
func operationRoute(ctx context.Context, cfgPath string, starts bool) (*apiclient.Client, int) {
	remote, err := engineOnlyRemote(ctx, cfgPath, starts)
	if err != nil {
		return nil, fail(err)
	}
	if remote != nil {
		return remote.client, -1
	}
	cfg, err := config.LoadAndValidate(cfgPath)
	if err != nil {
		return nil, fail(fmt.Errorf("config: %w", err))
	}
	mode, err := enterReadMode(ctx, cfgPath, cfg, os.Stderr)
	if err != nil {
		return nil, fail(err)
	}
	if !mode.attached() {
		return nil, fail(noEngineRefusal("operations are held in the memory of the process serving this deployment"))
	}
	return mode.client, -1
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
	client, code := operationRoute(ctx, *cfgPath, false)
	if code >= 0 {
		return code
	}
	got, err := client.ListOperations(ctx)
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
	follow := fs.Bool("follow", false, followFlagHelp)
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("operation show: expected exactly one operation id, as `operation list` prints it")
	}
	id := operands[0]
	ctx := context.Background()
	client, code := operationRoute(ctx, *cfgPath, false)
	if code >= 0 {
		return code
	}
	op, err := client.GetOperation(ctx, id)
	if err != nil {
		return fail(fmt.Errorf("reading %s: %w", id, err))
	}
	return finishOperation(ctx, client, op, *follow, *asJSON, "operation show")
}

// followFlagHelp is --follow's help on show and run, which follow alike.
const followFlagHelp = "poll until the operation finishes, printing each change of status on standard error; the exit status is 0 for completed, 1 for any other ending, and 51 when it stopped watching first (the engine stopped answering, forgot the operation, or Ctrl-C), in which case the operation may still be running"

// finishOperation is what show and run do once they hold an operation: with
// follow, poll until it is no longer queued or running, printing each change
// of status on standard error; then print it, and with follow take the exit
// status from how it ended (0 for completed, 1 for anything else).
//
// A follow can last as long as a cycle does, which is hours, so one failed
// poll is not the end of it (#1250's review). A failure that may clear (no
// answer, a 5xx) is sat out with a growing wait for up to
// operationFollowPatience; past that, or when the engine says it no longer
// has the operation (it restarted), or on Ctrl-C, it ends on exitLostTrack
// and names the operation, because the run may well be going and "it
// failed" would be the wrong thing for a script to alert on.
func finishOperation(ctx context.Context, client *apiclient.Client, op apicontract.Operation, follow, asJSON bool, verb string) int {
	if follow {
		// Taken once, before the loop: a failed read hands back a zero
		// Operation, and the id is the one thing the operator needs to
		// pick the run back up.
		id := op.OperationID
		if id == "" {
			return fail(fmt.Errorf("%s: the engine answered with an operation that has no id, so there is nothing to follow", verb))
		}
		ctx, stop := interruptContext(ctx)
		defer stop()
		last := ""
		var failingSince time.Time
		wait := operationPollEvery
		for op.Status == "queued" || op.Status == "running" {
			if op.Status != last {
				fmt.Fprintf(os.Stderr, "%s: %s\n", id, op.Status)
				last = op.Status
			}
			select {
			case <-ctx.Done():
				return lostTrack(id, "stopped following at your request")
			case <-time.After(wait):
			}
			next, err := client.GetOperation(ctx, id)
			if err == nil {
				op, failingSince, wait = next, time.Time{}, operationPollEvery
				continue
			}
			switch {
			case ctx.Err() != nil:
				return lostTrack(id, "stopped following at your request")
			case operationVanished(err):
				return lostTrack(id, "the engine no longer has it, which is what a restart of the engine looks like")
			case !transientPollFailure(err):
				return fail(fmt.Errorf("reading %s: %w", id, err))
			}
			if failingSince.IsZero() {
				failingSince = time.Now()
			}
			if time.Since(failingSince) > operationFollowPatience {
				return lostTrack(id, fmt.Sprintf("the engine has not answered for more than %s (%v)", operationFollowPatience, err))
			}
			fmt.Fprintf(os.Stderr, "%s: could not read it (%v), asking again\n", id, err)
			wait = min(max(wait*2, operationPollEvery), operationFollowRetryMax)
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

// lostTrack ends a follow that stopped watching before the operation did.
func lostTrack(id, why string) int {
	fmt.Fprintf(os.Stderr, "%s: %s: %s. The operation carries on in the engine regardless; `%s operation show %s --follow` picks it back up.\n",
		cliecho.Binary, id, why, cliecho.Binary, id)
	return exitLostTrack
}

// transientPollFailure is a failure that may clear by itself: nothing
// answered, or something answered 5xx (the engine, or a proxy in front of
// it while the engine restarts).
func transientPollFailure(err error) bool {
	if errors.Is(err, apiclient.ErrUnreachable) {
		return true
	}
	var refused *apiclient.Error
	if errors.As(err, &refused) {
		return refused.Status >= 500
	}
	var violation *apiclient.ContractViolation
	if errors.As(err, &violation) {
		return violation.Status >= 500
	}
	return false
}

// operationVanished is the engine saying it has no such operation. An
// operation lives in the engine's memory, so after the follow saw it once
// this means the engine restarted, not that the id was wrong.
func operationVanished(err error) bool {
	var refused *apiclient.Error
	return errors.As(err, &refused) && refused.Code == apicontract.ErrorCodeOperationNotFound
}

func cmdOperationRun(args []string) int {
	fs, cfgPath := newFlagSet("operation run")
	asJSON := fs.Bool("json", false, "print the operation the engine started as JSON, the same object the API answers with")
	follow := fs.Bool("follow", false, followFlagHelp)
	set := fs.String("backup-set", "", "run only this backup set (source/name), as run_backup_set; without it every enabled set, as run_cycle")
	skipScripts := fs.Bool("skip-workflow-scripts", false, "with --backup-set: ask the engine to run the set without its workflow hooks; the engine records the bypass on the run. fetch has no equivalent and refuses the flag")
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
	client, code := operationRoute(ctx, *cfgPath, true)
	if code >= 0 {
		return code
	}

	// The revision the engine holds now, which is what the run buttons send
	// too: a run is checked against the configuration it was asked for.
	served, err := client.SystemVersion(ctx)
	if err != nil {
		return fail(fmt.Errorf("reading the engine's configuration revision: %w", err))
	}
	req := apicontract.SubmitOperationRequest{Action: apicontract.ActionRunCycle, ConfigRevision: served.ConfigRevision}
	local := "`" + cliecho.Binary + " run` on the host runs every enabled set in that process instead"
	if *set != "" {
		req.Action = apicontract.ActionRunBackupSet
		req.BackupSetID = *set
		req.SkipWorkflowScripts = *skipScripts
		local = "`" + cliecho.Binary + " fetch --backup-set " + *set + "` on the host runs it in that process instead"
		if *skipScripts {
			// fetch refuses --skip-workflow-scripts on purpose, so the one
			// local verb there is would run exactly the hooks this asked
			// to skip. Saying so beats naming it.
			local = "there is no local equivalent: fetch refuses --skip-workflow-scripts, and without it the set's workflow hooks would run"
		}
	}

	// Fresh per invocation, for snapshot.go's reason: two runs typed at a
	// keyboard are two requests, and a reused key would hand the second one
	// the first run's operation.
	key := "cli-" + strings.ReplaceAll(req.Action, "_", "-") + ":" + uuid.NewString()
	op, err := submitWithSameKey(ctx, client, key, req)
	if errors.Is(err, errSubmitUnanswered) {
		fmt.Fprintf(os.Stderr, "%s: %v. The run may have started all the same: `%s operation list --action %s` shows it if it did. Check before asking again, because a new command is a new request.\n",
			cliecho.Binary, err, cliecho.Binary, req.Action)
		return exitLostTrack
	}
	if err != nil {
		var refused *apiclient.Error
		if errors.As(err, &refused) && refused.Code == apicontract.ErrorCodeDestructiveOperationsDisabled {
			// The verb's own remedy, then the shared mapping in fail().
			return fail(fmt.Errorf("the engine's destructive gate refused the run, so nothing was started; %s: %w", local, err))
		}
		return fail(fmt.Errorf("starting the run: %w", err))
	}
	return finishOperation(ctx, client, op, *follow, *asJSON, "operation run")
}

// errSubmitUnanswered is a submission whose reply never arrived, after every
// resend this command makes.
var errSubmitUnanswered = errors.New("the engine never answered the submission")

// submitWithSameKey sends the submission, and when the reply is lost resends
// it with the SAME key, up to three times inside operationSubmitDeadline
// (#1250's review). The engine writes the operation row before it answers,
// and on a busy NAS that write is what pushes the reply past the client's
// timeout, so a lost reply is often a run that started. The engine answers a
// replay of the same key with the operation it already holds, so resending
// is safe where a new command (with a new key) would be refused as already
// running, with no id to follow.
func submitWithSameKey(ctx context.Context, client *apiclient.Client, key string, req apicontract.SubmitOperationRequest) (apicontract.Operation, error) {
	deadline := time.Now().Add(operationSubmitDeadline)
	wait := operationSubmitRetryWait
	for attempt := 0; ; attempt++ {
		op, err := client.SubmitOperation(ctx, key, req)
		if err == nil || !errors.Is(err, apiclient.ErrUnreachable) {
			return op, err
		}
		if attempt >= 3 || time.Now().Add(wait).After(deadline) {
			return apicontract.Operation{}, fmt.Errorf("%w (%v)", errSubmitUnanswered, err)
		}
		fmt.Fprintf(os.Stderr, "%s: no reply to the submission (%v); sending it again with the same key, which the engine answers with the run it already started if it did\n", cliecho.Binary, err)
		select {
		case <-ctx.Done():
			return apicontract.Operation{}, fmt.Errorf("%w (%v)", errSubmitUnanswered, ctx.Err())
		case <-time.After(wait):
		}
		wait *= 2
	}
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
