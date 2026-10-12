package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/revalidate"
	"github.com/retnd/retnd/core/internal/state"
)

// cmdScrub is `retnd scrub [<source/backup-set | source/backup-set/artifact>]`:
// check the durable copies of what is named now, or every copy that is due
// when nothing is named (EPIC Q, #978).
//
// It is the operator's door to the scrub the daemon runs on a schedule, and
// it is the same code: app.Service.Scrub, called with a manual trigger. Naming
// a set, an artifact or (with --medium) one copy is an operator saying "now",
// so it checks whether or not the copy is due. Naming nothing checks only what
// is due, bounded the way the schedule is.
//
// # What it will not do
//
// It reads a copy on a storage medium at the strongest class an unattended
// pass may run (an existence check) unless --content says otherwise, and
// --content is refused unless --allow-egress also says the download may be
// paid for. An archive-class copy is never read and no restore is started;
// it is recorded as needing one. A HEAD is reported as a HEAD: this command
// prints the class that ran beside every result and never calls an existence
// check verification.
//
// # Beside a serving engine
//
// An edit hold lives in the memory of the process serving the deployment, so
// when RETND_API_URL (with its two credential variables) reaches that process
// this command asks it, and a set it is holding is skipped exactly as the
// engine's own scrub skips it: the same line, counted as a skip, and exit
// status 1. A hold this command cannot read is treated as a hold. With no
// route to an engine there is no hold to see, and the announcement the
// reads make on stderr says which world this ran in.
//
// # --json
//
// --json prints the one result object the API contract defines
// (apicontract.ScrubResult) and nothing else on standard output; the log
// lines this process writes go to standard error for that run. The exit
// status means what it means without the flag.
//
// Exit status is 0 when nothing failed and nothing was left undone, and 1
// when any copy failed, could not be checked, was skipped (held for editing,
// or being scrubbed by something else) or was refused for cost. A skip is
// work the operator asked for that did not happen, and a script that branches
// on this command's status has to be able to tell.
func cmdScrub(args []string) int {
	fs, cfgPath := newFlagSet("scrub")
	medium := fs.String("medium", "", "check only the copy on this medium (\"local\" for the local copy); needs an artifact")
	content := fs.Bool("content", false, "read the bytes of a copy on a storage medium and re-hash them (costs egress; needs --allow-egress)")
	allowEgress := fs.Bool("allow-egress", false, "acknowledge that --content downloads the copy and may be billed for it")
	asJSON := fs.Bool("json", false, "emit the scrub result as JSON, the same object the API reports, instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return 2
	}
	if len(operands) > 1 {
		return usageError("scrub: expected at most one of <source/backup-set> or <source/backup-set/artifact>")
	}

	req := app.ScrubRequest{Trigger: state.ScrubByManual, Medium: *medium, Content: *content, AllowEgress: *allowEgress}
	if len(operands) == 1 {
		switch strings.Count(operands[0], "/") {
		case 1:
			set, err := model.ParseBackupSetID(operands[0])
			if err != nil {
				return fail(err)
			}
			req.Set = &set
		case 2:
			id, err := app.ParseArtifactID(operands[0])
			if err != nil {
				return fail(err)
			}
			req.Artifact = &id
		default:
			return usageError("scrub: %q is neither <source/backup-set> nor <source/backup-set/artifact>", operands[0])
		}
	}
	if req.Medium != "" && req.Artifact == nil {
		return usageError("scrub: --medium names one copy of an artifact, so it needs <source/backup-set/artifact>")
	}
	if req.Content && !req.AllowEgress {
		return usageError("scrub: --content downloads the copy, which is billed by most providers; add --allow-egress to say that is acknowledged")
	}

	ctx := context.Background()
	svc, cfg, cleanup, err := openService(ctx, *cfgPath, false)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	if *asJSON {
		// Standard output is the result and only the result.
		svc.Logger = obs.New(os.Stderr, obs.LevelFromEnv())
	}
	logStartup(ctx, svc.Logger, app.BuildVersionInfo(version, commit))

	// The same door every read goes through: it says which world this ran in
	// and, when it can reach the engine serving this deployment, hands back
	// the route the hold is asked through.
	mode, err := enterReadMode(ctx, *cfgPath, cfg, os.Stderr)
	if err != nil {
		return fail(err)
	}
	if mode.attached() {
		ctx = app.WithBackupSetHolds(ctx, newEngineHolds(ctx, mode.client))
	}

	sum, err := svc.Scrub(ctx, req)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return printScrubJSON(sum)
	}
	return printScrub(sum)
}

// printScrubJSON writes the contract's scrub result and returns the status
// the text form would have returned for the same pass.
func printScrubJSON(sum app.ScrubSummary) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sum.Wire()); err != nil {
		return fail(fmt.Errorf("scrub: writing the result: %w", err))
	}
	return scrubStatus(sum)
}

// scrubStatus is the exit status of a pass: 0 only when nothing failed and
// nothing was left undone.
func scrubStatus(sum app.ScrubSummary) int {
	if sum.Failed+sum.CouldNotComplete+sum.SkippedHeld+sum.SkippedLeased+sum.Refused > 0 {
		return 1
	}
	return 0
}

func printScrub(sum app.ScrubSummary) int {
	for _, sr := range sum.Sets {
		if sr.Err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", sr.Set, sr.Err)
		}
		for _, f := range sr.Report.Findings {
			for _, c := range f.Copies {
				fmt.Printf("%s on %s: %s\n", f.Artifact, c.Medium, describeCopyResult(c))
			}
		}
		for _, e := range sr.Report.Errors {
			// The copies the pass did get an answer for are said as
			// themselves, each once, before the artifact's own problem.
			for _, c := range e.Copies {
				fmt.Printf("%s on %s: %s\n", e.Artifact, c.Medium, describeCopyResult(c))
			}
			fmt.Fprintf(os.Stderr, "%s: could not be checked: %v\n", e.Artifact, e.Err)
		}
		for _, sk := range sr.Report.Skipped {
			target := sr.Set.String()
			if sk.Artifact.Name != "" {
				target = sk.Artifact.String()
				if sk.Medium != "" {
					target += " on " + sk.Medium
				}
			}
			switch sk.Reason {
			case revalidate.SkipHeld:
				fmt.Printf("%s: skipped, the backup set is held for editing\n", target)
			case revalidate.SkipLeased:
				fmt.Printf("%s: skipped, another scrub is checking it right now\n", target)
			}
		}
	}
	fmt.Printf("scrub (%s): %d selected, %d passed, %d failed, %d could not be checked, %d need a restore, %d skipped, %d refused; %d bytes read\n",
		sum.Scope, sum.Selected, sum.Passed, sum.Failed, sum.CouldNotComplete, sum.RequiresRestore,
		sum.SkippedHeld+sum.SkippedLeased, sum.Refused, sum.BytesRead)

	return scrubStatus(sum)
}

// describeCopyResult says what ran and what it proves, in that order. The
// class is spelled out because "passed" at existence is a much smaller claim
// than "passed" at content, and the line must not let the first read as the
// second.
func describeCopyResult(c revalidate.CopyResult) string {
	switch {
	case c.Refused:
		return "refused, " + c.Reason
	case c.Verdict == state.ScrubPassed:
		return fmt.Sprintf("passed (%s: %s)", c.Class, proves(c))
	case c.Verdict == state.ScrubRequiresRestore:
		return "needs a restore before its bytes can be read; no restore was started"
	case c.Verdict == state.ScrubFailed:
		return fmt.Sprintf("FAILED (%s): %s", c.Class, c.Reason)
	case c.Verdict == state.ScrubCouldNotComplete:
		return "could not be checked: " + c.Reason
	default:
		return "not checked: " + c.Reason
	}
}

func proves(c revalidate.CopyResult) string {
	if c.Class != "" {
		return c.Class.Proves()
	}
	return "nothing"
}
