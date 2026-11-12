package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/service"
)

// `retnd agent audit` (EPIC U, U1.5, #1348; FR-97): the trail of every call
// an agent made, reads included, newest first. It is GET /api/v1/agent/audit:
// beside a serving engine the verb asks that route, and with nothing serving
// it reads this host's own journal, converting through the same
// service.AgentAuditEntry.Contract the route does, so --json is the same page
// for the same cursor from either door.
//
// --token is a token's id, as `agent token list` prints it, and never the
// token. A value that looks like a token (the rat_ prefix) is refused before
// anything is opened, so a secret pasted here does not reach a shell history
// by way of a flag that was only meant to filter.

// agentAuditReader is the one act both doors answer. *apiclient.Client
// satisfies it as it stands; localAgentAudit is the direct door.
type agentAuditReader interface {
	ListAgentAudit(ctx context.Context, tokenID, since, before string, limit int) (apicontract.ListAgentAuditResponse, error)
}

type localAgentAudit struct{ svc *service.BackupService }

func (l localAgentAudit) ListAgentAudit(ctx context.Context, tokenID, since, before string, limit int) (apicontract.ListAgentAuditResponse, error) {
	entries, next, err := l.svc.ListAgentAudit(ctx, service.AgentAuditQuery{TokenID: tokenID, Since: since, Before: before, Limit: limit})
	if err != nil {
		return apicontract.ListAgentAuditResponse{}, err
	}
	out := apicontract.ListAgentAuditResponse{Entries: make([]apicontract.AgentAuditEntry, 0, len(entries)), NextCursor: next}
	for _, e := range entries {
		out.Entries = append(out.Entries, e.Contract())
	}
	return out, nil
}

var _ agentAuditReader = (*apiclient.Client)(nil)

func openAgentAudit(ctx context.Context, cfgPath string, asJSON bool) (agentAuditReader, func(), error) {
	route, cleanup, err := openEncryptionWrite(ctx, cfgPath, asJSON)
	if err != nil {
		return nil, func() {}, err
	}
	switch r := route.(type) {
	case *engineRoute:
		return r.client, cleanup, nil
	case *service.BackupService:
		return localAgentAudit{svc: r}, cleanup, nil
	default:
		cleanup()
		return nil, func() {}, fmt.Errorf("agent audit: no route to this deployment (%T)", route)
	}
}

func agentAudit(args []string) int {
	fs, cfgPath := newFlagSet("agent audit")
	tokenID := fs.String("token", "", "only this token's rows: its id, as `agent token list` prints it")
	since := fs.String("since", "", "only rows from then on: days such as 7d, a duration such as 36h, or an RFC 3339 time")
	limit := fs.Int("limit", 0, "how many rows (the default is 100, the most 1000)")
	before := fs.String("before", "", "the next page: the cursor the previous page printed")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("agent audit: takes no operands")
	}
	if strings.HasPrefix(*tokenID, "rat_") {
		return usageError("agent audit: --token takes a token's id (agt_...), as `agent token list` prints it, never the token itself")
	}
	if *limit < 0 {
		return usageError("agent audit: --limit cannot be negative")
	}

	ctx := context.Background()
	route, cleanup, err := openAgentAudit(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	got, err := route.ListAgentAudit(ctx, *tokenID, *since, *before, *limit)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return emitWireJSON(got)
	}
	if len(got.Entries) == 0 {
		fmt.Println("no agent calls recorded")
		return exitOK
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	rows := []string{"AT\tTOKEN\tTOOL\tVIA\tSTATUS\tCODE\tCORRELATION\tARGUMENTS"}
	for _, e := range got.Entries {
		token := e.TokenID
		if token == "" {
			token = "-"
		}
		tool := e.Tool
		if tool == "" {
			tool = "-"
		}
		code := e.Code
		if code == "" {
			code = "-"
		}
		rows = append(rows, strings.Join([]string{e.At, token, tool, e.Transport, strconv.Itoa(e.Status), code, e.CorrelationID, e.Arguments}, "\t"))
	}
	if _, err := fmt.Fprintln(w, strings.Join(rows, "\n")); err != nil {
		return fail(fmt.Errorf("writing the trail: %w", err))
	}
	if err := w.Flush(); err != nil {
		return fail(fmt.Errorf("writing the trail: %w", err))
	}
	if got.NextCursor != "" {
		fmt.Fprintf(os.Stderr, "%s: more behind this page: add --before %s\n", cliecho.Binary, got.NextCursor)
	}
	return exitOK
}
