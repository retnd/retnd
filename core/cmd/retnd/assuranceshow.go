package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/service"
)

// `retnd assurance show <operation-id> [--json]` (#1200): the terminal's
// counterpart of the Operation page's lines and outcome (the page also shows
// the validator's output, which lives per set and is `recovery-test
// --show-output`; this prints that command). For the engine's own record of
// an operation, a different thing, see `retnd operation show`. It prints one scrub's or recovery test's lines, in
// the line format `activity --follow` uses, under a heading that says what
// the run was and how it ended, for a run still going or one that finished
// an hour ago. --json prints the body of GET /assurance/operations/{op}.
//
// # Where it reads
//
// The lines are in the durable activity record, which is in this host's
// journal, so the verb reads there with nothing serving, the way
// `recovery-test --show-output` does. With an engine attached it asks the
// engine instead, because only the engine knows whether it is still running
// the operation, and its ring has every stage line where the journal keeps
// the last one. Read alone, a run with no end line is "unfinished": still
// going in the serving process, or that process stopped before it could
// write one.
//
// An id with nothing on record is an error (exit 1) in words that cover
// both ways it happens: it never was an operation here, or its lines were
// pruned past the record's retention.

func cmdAssuranceShow(args []string) int {
	fs, cfgPath := newFlagSet("assurance show")
	asJSON := fs.Bool("json", false, "print the operation as JSON, the same object GET /api/v1/assurance/operations/{op} serves")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("assurance show: expected exactly one operation id, the one the scrub or recovery test returned when it started")
	}
	opID := operands[0]
	ctx := context.Background()

	cfg, journal, releaseJournal, err := service.OpenConfigAndJournal(ctx, *cfgPath)
	if err != nil {
		return fail(err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "retnd: closing state database: %v\n", err)
		}
		if err := releaseJournal(); err != nil {
			fmt.Fprintf(os.Stderr, "retnd: releasing the state database lock: %v\n", err)
		}
	}()
	mode, err := enterReadMode(ctx, *cfgPath, cfg, os.Stderr)
	if err != nil {
		return fail(err)
	}

	var rec apicontract.AssuranceOperationRecord
	if mode.attached() {
		rec, err = mode.client.GetAssuranceOperation(ctx, opID)
		var refused *apiclient.Error
		if errors.As(err, &refused) && refused.Code == apicontract.ErrorCodeOperationNotFound {
			return fail(notOnRecord(opID))
		}
		if err != nil {
			return fail(mode.unanswered("what operation "+opID+" did", err))
		}
	} else {
		// The same read `activity` makes with nothing serving: a service
		// over this host's journal, which the deferred close above owns.
		got, err := service.New(cfg, journal, nil, nil).AssuranceOperation(ctx, opID)
		if errors.Is(err, service.ErrOperationNotFound) {
			return fail(notOnRecord(opID))
		}
		if err != nil {
			return fail(err)
		}
		rec = assuranceOperationContract(got)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rec); err != nil {
			return fail(fmt.Errorf("assurance show: writing the result: %w", err))
		}
		return exitOK
	}
	writeAssuranceOperation(os.Stdout, rec)
	return exitOK
}

func notOnRecord(opID string) error {
	return fmt.Errorf("operation %s: %s", opID, service.NotOnRecordWords())
}

// assuranceOperationContract renders the service's record in the contract's
// shape, the same fields and the same time format the route writes.
func assuranceOperationContract(r service.AssuranceOperationRecord) apicontract.AssuranceOperationRecord {
	return apicontract.AssuranceOperationRecord{
		OperationID: r.OperationID, Kind: r.Kind, BackupSetID: r.BackupSetID, State: r.State,
		Stage: r.Stage, Reason: r.Reason, Events: contractEvents(r.Events),
		StartedAt: formatActivityTime(r.StartedAt), EndedAt: formatActivityTime(r.EndedAt),
	}
}

// assuranceKindWords and assuranceStateWords are the Operation page's own
// words for the heading (OperationPanel.tsx), so the two say the same thing.
var assuranceKindWords = map[string]string{"scrub": "Scrub", "recovery_test": "Recovery test", "recovery": "Recovery"}

var assuranceStateWords = map[string]string{
	service.AssuranceOpRunning:        "running",
	service.AssuranceOpPassed:         "finished",
	service.AssuranceOpFailed:         "failed",
	service.AssuranceOpDidNotComplete: "did not complete",
	service.AssuranceOpCancelled:      "cancelled",
	service.AssuranceOpUnfinished:     "has no end on record (still running in the serving process, or that process stopped before it ended)",
}

func writeAssuranceOperation(w io.Writer, r apicontract.AssuranceOperationRecord) {
	kind := assuranceKindWords[r.Kind]
	if kind == "" {
		kind = "Operation"
	}
	state := assuranceStateWords[r.State]
	if state == "" {
		state = r.State
	}
	head := kind + " " + state
	if r.BackupSetID != "" {
		head += ": " + r.BackupSetID
	}
	_, _ = fmt.Fprintf(w, "%s (%s)\n", head, r.OperationID)
	if r.Stage != "" && r.Stage != "complete" {
		_, _ = fmt.Fprintf(w, "stage:  %s\n", stageWithLabel(r.Stage))
	}
	if r.Reason != "" {
		fmt.Fprintf(w, "reason: %s\n", r.Reason)
	}
	for _, e := range r.Events {
		_, _ = fmt.Fprintln(w, formatFollowEvent(e))
	}
	// The Operation page shows the validator's output under a recovery
	// test; the record doesn't carry it (it is the newest test's, kept per
	// set), so say where it is rather than leave it looking absent. A test
	// that never ended has none.
	if r.Kind == "recovery_test" && r.BackupSetID != "" && r.State != service.AssuranceOpRunning && r.State != service.AssuranceOpUnfinished {
		fmt.Fprintf(w, "validator output: retnd recovery-test %s --show-output\n", r.BackupSetID)
	}
}

// stageWithLabel is "Verifying (verifying)": the label the page shows and the
// id the stage field carries.
func stageWithLabel(id string) string {
	if label := obs.StageLabel(id); label != "" {
		return label + " (" + id + ")"
	}
	return id
}
