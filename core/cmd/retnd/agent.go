package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/service"
)

// `retnd agent token create|list|revoke` (EPIC U, U1.2, #1345; FR-91).
//
// An agent token is the credential an AI agent presents instead of a session.
// These verbs are how an operator mints, reads and ends one from a terminal,
// and they are the same three acts as POST and GET /api/v1/agent/tokens and
// POST /api/v1/agent/tokens/{id}/revoke: beside a serving engine they go to
// those routes, which are session-only, and with nothing serving they act on
// this host's own journal.
//
// # The token is shown once, on standard output, alone
//
// `create` prints the token on a line of its own and nothing else on that
// line, so `TOKEN=$(retnd agent token create triage)` captures it. Everything
// an operator needs to read, including that this is the only time the token
// is shown, goes to standard error. After that the deployment holds a SHA-256
// and an eight-character prefix, and no verb prints more of the token than
// the prefix.
//
// # There is no --token flag, and no flag that takes a secret
//
// The token is an output of this command and an input of nothing here. A
// secret on a command line lands in a shell history and a process listing,
// which is the rule `auth enroll`'s usage line already states.

// agentTokens is the three acts both doors answer. *apiclient.Client satisfies
// it as it stands, so beside an engine nothing sits between the verb and the
// route; localAgentTokens is the direct door.
type agentTokens interface {
	MintAgentToken(ctx context.Context, req apicontract.MintAgentTokenRequest) (apicontract.MintedAgentToken, error)
	ListAgentTokens(ctx context.Context) (apicontract.ListAgentTokensResponse, error)
	RevokeAgentToken(ctx context.Context, id string) (apicontract.AgentToken, error)
}

// localAgentTokens answers the three acts from this host's own service,
// converting its types to the contract's. It is what runs when nothing is
// serving this deployment, where there is no session and so no signed-in user:
// the owner is recorded as cliActor, the name every other terminal write
// already uses for itself.
type localAgentTokens struct{ svc *service.BackupService }

func (l localAgentTokens) MintAgentToken(ctx context.Context, req apicontract.MintAgentTokenRequest) (apicontract.MintedAgentToken, error) {
	minted, err := l.svc.MintAgentToken(ctx, service.MintAgentTokenRequest{
		Name: req.Name, Scope: req.Scope, ExpiresIn: req.ExpiresIn, Owner: cliActor,
	})
	if err != nil {
		return apicontract.MintedAgentToken{}, err
	}
	return apicontract.MintedAgentToken{Token: minted.Token, AgentToken: minted.AgentToken.Contract()}, nil
}

func (l localAgentTokens) ListAgentTokens(ctx context.Context) (apicontract.ListAgentTokensResponse, error) {
	tokens, err := l.svc.ListAgentTokens(ctx)
	if err != nil {
		return apicontract.ListAgentTokensResponse{}, err
	}
	out := apicontract.ListAgentTokensResponse{Tokens: make([]apicontract.AgentToken, 0, len(tokens))}
	for _, t := range tokens {
		out.Tokens = append(out.Tokens, t.Contract())
	}
	return out, nil
}

func (l localAgentTokens) RevokeAgentToken(ctx context.Context, id string) (apicontract.AgentToken, error) {
	revoked, err := l.svc.RevokeAgentToken(ctx, id)
	if err != nil {
		return apicontract.AgentToken{}, err
	}
	return revoked.Contract(), nil
}

// openAgentTokens settles where an agent token verb acts: the engine's routes
// when one is serving this deployment, this host's journal otherwise. It is
// the door every configuration write goes through, so the claim that keeps two
// writers apart applies here as well. With --json the log goes to standard
// error so the document is the only thing on standard output.
func openAgentTokens(ctx context.Context, cfgPath string, asJSON bool) (agentTokens, func(), error) {
	route, cleanup, err := openEncryptionWrite(ctx, cfgPath, asJSON)
	if err != nil {
		return nil, func() {}, err
	}
	switch r := route.(type) {
	case *engineRoute:
		return r.client, cleanup, nil
	case *service.BackupService:
		return localAgentTokens{svc: r}, cleanup, nil
	default:
		cleanup()
		return nil, func() {}, fmt.Errorf("agent token: no route to this deployment (%T)", route)
	}
}

var _ agentTokens = (*apiclient.Client)(nil)

func cmdAgent(args []string) int {
	if len(args) == 0 {
		return usageError("agent: expected `agent token create|list|revoke`, `agent confirmations list|approve|deny`, `agent audit` or `agent serve --stdio --token-file PATH`")
	}
	switch args[0] {
	case "token":
		return cmdAgentToken(args[1:])
	case "confirmations":
		return cmdAgentConfirmations(args[1:])
	case "audit":
		return agentAudit(args[1:])
	case "serve":
		return agentServe(args[1:])
	default:
		return usageError("agent: unknown subcommand %q (token, confirmations, audit and serve exist)", args[0])
	}
}

func cmdAgentToken(args []string) int {
	if len(args) == 0 {
		return usageError("agent token: expected `agent token create NAME`, `agent token list` or `agent token revoke ID`")
	}
	switch args[0] {
	case "create":
		return agentTokenCreate(args[1:])
	case "list":
		return agentTokenList(args[1:])
	case "revoke":
		return agentTokenRevoke(args[1:])
	default:
		return usageError("agent token: unknown subcommand %q (create, list and revoke exist)", args[0])
	}
}

func agentTokenCreate(args []string) int {
	fs, cfgPath := newFlagSet("agent token create")
	scope := fs.String("scope", "", "read (the default), operate or configure")
	expiresIn := fs.String("expires-in", "", "how long the token lives: days such as 90d, or a duration such as 720h; the default is 90d and the most is 365d")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("agent token create: expected exactly one NAME, the agent the token is for")
	}

	ctx := context.Background()
	// The log goes to standard error whatever the format: standard output is
	// the token, alone, and one stray record in it is a token nobody can use.
	route, cleanup, err := openAgentTokens(ctx, *cfgPath, true)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	minted, err := route.MintAgentToken(ctx, apicontract.MintAgentTokenRequest{Name: operands[0], Scope: *scope, ExpiresIn: *expiresIn})
	if err != nil {
		return fail(err)
	}
	t := minted.AgentToken
	fmt.Fprintf(os.Stderr, "%s: minted agent token %s (id %s, scope %s, expires %s, principal agent:%s).\n", cliecho.Binary, t.Name, t.ID, t.Scope, t.ExpiresAt, t.Name)
	fmt.Fprintln(os.Stderr, "The token on standard output is shown once. This deployment keeps only its hash, so if it is lost, revoke it and mint another.")
	fmt.Println(minted.Token)
	return exitOK
}

func agentTokenList(args []string) int {
	fs, cfgPath := newFlagSet("agent token list")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("agent token list: takes no operands")
	}

	ctx := context.Background()
	route, cleanup, err := openAgentTokens(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	got, err := route.ListAgentTokens(ctx)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return emitWireJSON(got)
	}
	if len(got.Tokens) == 0 {
		fmt.Println("no agent tokens")
		return exitOK
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	rows := []string{"NAME\tPREFIX\tSCOPE\tSTATUS\tOWNER\tEXPIRES\tLAST USED\tID"}
	for _, t := range got.Tokens {
		lastUsed := "never"
		if t.LastUsedAt != nil {
			lastUsed = *t.LastUsedAt
		}
		rows = append(rows, strings.Join([]string{t.Name, t.Prefix, t.Scope, t.Status, t.Owner, t.ExpiresAt, lastUsed, t.ID}, "\t"))
	}
	if _, err := fmt.Fprintln(w, strings.Join(rows, "\n")); err != nil {
		return fail(fmt.Errorf("writing the list: %w", err))
	}
	if err := w.Flush(); err != nil {
		return fail(fmt.Errorf("writing the list: %w", err))
	}
	return exitOK
}

func agentTokenRevoke(args []string) int {
	fs, cfgPath := newFlagSet("agent token revoke")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("agent token revoke: expected exactly one ID, as `agent token list` prints it")
	}

	ctx := context.Background()
	route, cleanup, err := openAgentTokens(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	got, err := route.RevokeAgentToken(ctx, strings.TrimSpace(operands[0]))
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return emitWireJSON(got)
	}
	fmt.Printf("revoked agent token %s (%s): the next request that presents it is refused\n", got.Name, got.ID)
	return exitOK
}
