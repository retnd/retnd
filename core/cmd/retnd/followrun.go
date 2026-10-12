package main

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/service"
)

// `--follow` on `recover` and `recovery-test` (EPIC Q, #1059).
//
// Both verbs run the operation inside this process, so there is no serving
// engine to put a question to: the feed they follow is this process's own,
// the one the stage lines are written into as the recovery passes through
// them. The loop is the one `activity --follow` uses (followFeed), with two
// differences. The reading comes straight from the service, and only the
// operation's own lines are kept, matched on the op_id every start, stage
// and end line carries.
//
// The lines go to standard error. Standard output belongs to the verb's
// answer (and, for `recover --to -`, to the recovered bytes), and a stream
// of progress mixed into that would break whoever pipes it. The exit status
// is the operation's, exactly as without the flag.

// inProcessFeed reads the live feed of svc and renders it in the contract's
// shape, which is what the polling loop (and the browser) read.
func inProcessFeed(svc *service.BackupService) liveReader {
	return func(ctx context.Context, backupSetID, scope string, since int64, limit int) (apicontract.LiveActivityResponse, error) {
		la, err := svc.LiveActivity(ctx, service.LiveActivityRequest{
			BackupSetID:    backupSetID,
			DeploymentOnly: scope == service.LiveActivityScopeDeployment,
			Since:          since,
			Limit:          limit,
		})
		if err != nil {
			return apicontract.LiveActivityResponse{}, err
		}
		resp := apicontract.LiveActivityResponse{Epoch: la.Epoch, PollAfterMs: int(la.PollAfter / time.Millisecond)}
		for _, s := range la.Sets {
			resp.Sets = append(resp.Sets, apicontract.LiveActivitySet{
				BackupSetID: s.BackupSetID, Events: contractEvents(s.Events),
				LatestSequence: s.LatestSequence, Truncated: s.Truncated,
			})
		}
		if la.Deployment != nil {
			resp.Deployment = &apicontract.LiveActivityDeployment{
				Events: contractEvents(la.Deployment.Events), LatestSequence: la.Deployment.LatestSequence,
				Truncated: la.Deployment.Truncated,
			}
		}
		return resp, nil
	}
}

func contractEvents(in []service.LiveActivityEvent) []apicontract.LiveActivityEvent {
	out := make([]apicontract.LiveActivityEvent, 0, len(in))
	for _, e := range in {
		ev := apicontract.LiveActivityEvent{
			Sequence: e.Sequence, At: e.At.Format(time.RFC3339Nano), Level: e.Level, Result: e.Result,
			Action: e.Action, ActionID: e.ActionID, Event: e.Event, Scope: e.Scope, Message: e.Message,
			Fields: make([]apicontract.LiveActivityField, 0, len(e.Fields)),
		}
		for _, f := range e.Fields {
			ev.Fields = append(ev.Fields, apicontract.LiveActivityField{Key: f.Key, Value: f.Value})
		}
		out = append(out, ev)
	}
	return out
}

// opByID keeps the lines of one operation.
func opByID(id string) func(apicontract.LiveActivityEvent) bool {
	return func(e apicontract.LiveActivityEvent) bool { return e.ActionID == id }
}

// opByFirstStart keeps the lines of the first recovery this process starts
// for the artifact. `recover` mints its operation id inside the redeem, after
// the verb has handed over its request, so the id is learned from the start
// line it writes rather than chosen up front.
func opByFirstStart(artifact string) func(apicontract.LiveActivityEvent) bool {
	var mu sync.Mutex
	var id string
	return func(e apicontract.LiveActivityEvent) bool {
		mu.Lock()
		defer mu.Unlock()
		if id == "" && e.Event == obs.EventRecoveryStart && fieldOf(e, "artifact") == artifact {
			id = e.ActionID
		}
		return id != "" && e.ActionID == id
	}
}

func fieldOf(e apicontract.LiveActivityEvent, key string) string {
	for _, f := range e.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// followWhile runs op and, until it returns, prints the lines onlyOp keeps
// to out. It returns what op returned. A follow that fails to read the feed
// is reported on errOut and never changes the operation's own result.
func followWhile(ctx context.Context, svc *service.BackupService, onlyOp func(apicontract.LiveActivityEvent) bool, out, errOut io.Writer, op func()) {
	done := make(chan struct{})
	followed := make(chan error, 1)
	go func() {
		followed <- followFeed(ctx, readDecision{}, inProcessFeed(svc),
			followOptions{onlyOp: onlyOp, until: done, pollEvery: 50 * time.Millisecond}, out, errOut)
	}()
	op()
	close(done)
	if err := <-followed; err != nil {
		_, _ = io.WriteString(errOut, "could not follow the operation: "+err.Error()+"\n")
	}
}
