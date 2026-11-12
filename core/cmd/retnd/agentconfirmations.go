package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/service"
)

// `retnd agent confirmations list|approve|deny` (EPIC U, U2.1, #1349; FR-95).
//
// An agent whose token has the operate or configure scope never changes
// anything itself: each call it makes is a confirmation, and these verbs are
// how a person reads them and decides. They are the same acts as
// GET /api/v1/agent/confirmations and POST .../{id}/approve and .../{id}/deny.
//
// # Approve needs the engine, and the password
//
// Approving replays the recorded request through the engine's own handlers,
// as the token that asked, so the shut destructive gate and every other
// refusal apply to it as they would to the original. There is nothing to
// replay it through in this process, so with no engine serving the deployment
// approve says so and does nothing, while list and deny, which only read and
// end a record, act on this host's own journal. The step-up password is the
// one this command already signs in with (apiclient's stepUpOperations), never
// a flag.
//
// # The exit code is the replay's
//
// An approval that was made and whose replay the API refused exits with the
// code `retnd` gives that refusal anywhere else: 50 for the shut gate, as
// `retnd run` does. The approval itself stands, and the verb says so, because
// the person did what they were asked to do and the gate did what it does.

// agentConfirmations is the three acts both doors answer.
// *apiclient.Client satisfies it as it stands.
type agentConfirmations interface {
	ListAgentConfirmations(ctx context.Context) (apicontract.ListAgentConfirmationsResponse, error)
	GetAgentConfirmation(ctx context.Context, id string) (apicontract.AgentConfirmation, error)
	ApproveAgentConfirmation(ctx context.Context, id string) (apicontract.AgentConfirmation, error)
	DenyAgentConfirmation(ctx context.Context, id string) (apicontract.AgentConfirmation, error)
}

var _ agentConfirmations = (*apiclient.Client)(nil)

// errApproveNeedsEngine is what approve says when nothing is serving.
var errApproveNeedsEngine = errors.New("approving replays the recorded request through the engine's own handlers, and no engine is serving this deployment; start it and run this again, or deny the confirmation here")

// localAgentConfirmations answers list and deny from this host's own service.
// There is no session at the terminal, so the person deciding is recorded as
// cliActor, the name every other terminal write already uses for itself.
type localAgentConfirmations struct{ svc *service.BackupService }

func (l localAgentConfirmations) ListAgentConfirmations(ctx context.Context) (apicontract.ListAgentConfirmationsResponse, error) {
	got, err := l.svc.ListAgentConfirmations(ctx)
	if err != nil {
		return apicontract.ListAgentConfirmationsResponse{}, err
	}
	out := apicontract.ListAgentConfirmationsResponse{Confirmations: make([]apicontract.AgentConfirmation, 0, len(got))}
	for _, c := range got {
		out.Confirmations = append(out.Confirmations, c.Contract())
	}
	return out, nil
}

func (l localAgentConfirmations) GetAgentConfirmation(ctx context.Context, id string) (apicontract.AgentConfirmation, error) {
	c, err := l.svc.AgentConfirmationStatus(ctx, "", id)
	if err != nil {
		return apicontract.AgentConfirmation{}, err
	}
	return c.Contract(), nil
}

func (l localAgentConfirmations) ApproveAgentConfirmation(context.Context, string) (apicontract.AgentConfirmation, error) {
	return apicontract.AgentConfirmation{}, errApproveNeedsEngine
}

func (l localAgentConfirmations) DenyAgentConfirmation(ctx context.Context, id string) (apicontract.AgentConfirmation, error) {
	c, err := l.svc.DenyAgentConfirmation(ctx, id, cliActor)
	if err != nil {
		return apicontract.AgentConfirmation{}, err
	}
	return c.Contract(), nil
}

// openAgentConfirmations settles where a confirmation verb acts, the way
// openAgentTokens does for the token verbs.
func openAgentConfirmations(ctx context.Context, cfgPath string, asJSON bool) (agentConfirmations, func(), error) {
	route, cleanup, err := openEncryptionWrite(ctx, cfgPath, asJSON)
	if err != nil {
		return nil, func() {}, err
	}
	switch r := route.(type) {
	case *engineRoute:
		return r.client, cleanup, nil
	case *service.BackupService:
		return localAgentConfirmations{svc: r}, cleanup, nil
	default:
		cleanup()
		return nil, func() {}, fmt.Errorf("agent confirmations: no route to this deployment (%T)", route)
	}
}

func cmdAgentConfirmations(args []string) int {
	if len(args) == 0 {
		return usageError("agent confirmations: expected `agent confirmations list`, `show ID`, `approve ID` or `deny ID`")
	}
	switch args[0] {
	case "list":
		return agentConfirmationsList(args[1:])
	case "show":
		return agentConfirmationsShow(args[1:])
	case "approve":
		return agentConfirmationsDecide("approve", args[1:])
	case "deny":
		return agentConfirmationsDecide("deny", args[1:])
	default:
		return usageError("agent confirmations: unknown subcommand %q (list, show, approve and deny exist)", args[0])
	}
}

func agentConfirmationsList(args []string) int {
	fs, cfgPath := newFlagSet("agent confirmations list")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("agent confirmations list: takes no operands")
	}

	ctx := context.Background()
	route, cleanup, err := openAgentConfirmations(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	got, err := route.ListAgentConfirmations(ctx)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return emitWireJSON(got)
	}
	if len(got.Confirmations) == 0 {
		fmt.Println("no agent confirmations")
		return exitOK
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	rows := []string{"ID\tSTATE\tTOKEN\tTOOL\tEXPIRES\tCOMMAND"}
	for _, c := range got.Confirmations {
		rows = append(rows, strings.Join([]string{c.ID, c.State, c.TokenName, c.Tool, c.ExpiresAt, c.Command}, "\t"))
	}
	if _, err := fmt.Fprintln(w, strings.Join(rows, "\n")); err != nil {
		return fail(fmt.Errorf("writing the list: %w", err))
	}
	if err := w.Flush(); err != nil {
		return fail(fmt.Errorf("writing the list: %w", err))
	}
	return exitOK
}

func agentConfirmationsShow(args []string) int {
	fs, cfgPath := newFlagSet("agent confirmations show")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("agent confirmations show: expected exactly one ID, as `agent confirmations list` prints it")
	}

	ctx := context.Background()
	route, cleanup, err := openAgentConfirmations(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	got, err := route.GetAgentConfirmation(ctx, strings.TrimSpace(operands[0]))
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return emitWireJSON(got)
	}
	fmt.Printf("%s: %s, asked by %s for %s, expires %s\n  %s\n", got.ID, got.State, got.TokenName, got.Tool, got.ExpiresAt, got.Command)
	if e := got.Execution; e != nil {
		fmt.Printf("  ran: %d %s %s (correlation id %s, exit code %d)\n", e.Status, e.Code, e.Message, e.CorrelationID, e.ExitCode)
	}
	return exitOK
}

// agentConfirmationsDecide is approve and deny, which differ in one call and
// in what they print.
func agentConfirmationsDecide(verb string, args []string) int {
	fs, cfgPath := newFlagSet("agent confirmations " + verb)
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("agent confirmations %s: expected exactly one ID, as `agent confirmations list` prints it", verb)
	}

	ctx := context.Background()
	route, cleanup, err := openAgentConfirmations(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	id := strings.TrimSpace(operands[0])
	var got apicontract.AgentConfirmation
	if verb == "approve" {
		got, err = route.ApproveAgentConfirmation(ctx, id)
	} else {
		got, err = route.DenyAgentConfirmation(ctx, id)
	}
	if err != nil {
		return fail(err)
	}
	exit := exitOK
	if e := got.Execution; e != nil {
		exit = e.ExitCode
	}
	if *asJSON {
		if code := emitWireJSON(got); code != exitOK {
			return code
		}
		return exit
	}
	if verb == "deny" {
		fmt.Printf("denied %s (%s): nothing was run\n", got.ID, got.Tool)
		return exitOK
	}
	e := got.Execution
	switch {
	case e == nil:
		fmt.Printf("approved %s (%s); how its replay ended was not recorded\n", got.ID, got.Tool)
	case e.Code == "":
		fmt.Printf("approved %s (%s) and ran it: %d %s (correlation id %s)\n", got.ID, got.Tool, e.Status, e.Message, e.CorrelationID)
	default:
		fmt.Fprintf(os.Stderr, "%s: approved %s (%s), and the engine refused to run it: %s: %s (correlation id %s)\n",
			cliecho.Binary, got.ID, got.Tool, e.Code, e.Message, e.CorrelationID)
		fmt.Fprintln(os.Stderr, "The approval stands and nothing was changed. Whatever refused this refuses it for a person too.")
	}
	return exit
}
