package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/model"
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
	svc, _, cleanup, err := openService(ctx, *cfgPath, false)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	logStartup(ctx, svc.Logger, app.BuildVersionInfo(version, commit))

	sum, err := svc.Scrub(ctx, req)
	if err != nil {
		return fail(err)
	}
	return printScrub(sum)
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

	if sum.Failed+sum.CouldNotComplete+sum.SkippedHeld+sum.SkippedLeased+sum.Refused > 0 {
		return 1
	}
	return 0
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
