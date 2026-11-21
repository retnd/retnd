package main

import (
	"context"
	"fmt"

	"github.com/retnd/retnd/core/internal/app"
)

// cmdRetire is issue #1528's second way out of FAILED: retire one failed
// backup whose source is gone, so the set stops counting it as a failure
// while its record stays.
//
// Like retry it opens the deployment itself and calls internal/app, which
// holds the whole rule; the API route reaches the same method, so a refusal
// here is the same refusal, with the same sentence, the browser gets. Every
// refusal exits 1, as retry's do, and names the acknowledgement that would
// override it when there is one.
func cmdRetire(args []string) int {
	fs, cfgPath := newFlagSet("retire")
	supersededBy := fs.String("superseded-by", "", "the newer good backup in the same set that replaces this one, instead of the newest one the engine would pick")
	noSuccessor := fs.Bool("no-successor", false, "retire it although no newer good backup replaces it")
	acceptPresent := fs.Bool("accept-source-present", false, "retire it although the source object is still there or could not be checked")
	acceptUnverified := fs.Bool("accept-unverified-copy", false, "retire it although its own copy at the final name could not be verified, once its source is gone; the file is kept")
	note := fs.String("note", "", "operator note recorded with the retirement")
	dryRun := fs.Bool("dry-run", false, "print what retiring would do, and change nothing")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return 2
	}
	if len(operands) != 1 {
		return usageError("retire: expected <source/backup-set/artifact>")
	}
	id, err := app.ParseArtifactID(operands[0])
	if err != nil {
		return fail(err)
	}

	ctx := context.Background()
	svc, _, cleanup, err := openService(ctx, *cfgPath, true)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	logStartup(ctx, svc.Logger, app.BuildVersionInfo(version, commit))

	req := app.RetireRequest{SupersededBy: *supersededBy, NoSuccessor: *noSuccessor, AcceptSourcePresent: *acceptPresent,
		AcceptUnverifiedCopy: *acceptUnverified, Note: *note, ActorKind: "operator", Actor: cliActor}
	if *dryRun {
		p, err := svc.PreviewRetirement(ctx, id, req)
		if err != nil {
			return fail(err)
		}
		fmt.Printf("%s: %s\n", id, p.State)
		if p.FailureReason != "" {
			fmt.Printf("  failed on: %s\n", p.FailureReason)
		}
		if p.SourceCheck.Sentence != "" {
			fmt.Printf("  source: %s\n", p.SourceCheck.Sentence)
		}
		if p.ProposedSuccessor != nil {
			fmt.Printf("  replaced by: %s (%s)\n", p.ProposedSuccessor.Name, p.ProposedSuccessor.State)
		}
		if p.Refusal != nil {
			fmt.Printf("  would be refused (%s): %s\n", p.Refusal.Code, p.Refusal.Sentence)
			if p.Refusal.OverridableWith != "" {
				fmt.Printf("  --%s would let it go ahead\n", flagFor(p.Refusal.OverridableWith))
			}
			return 0
		}
		for _, e := range p.Effects {
			fmt.Printf("  - %s\n", e)
		}
		return 0
	}

	res, err := svc.RetireArtifact(ctx, id, req)
	if err != nil {
		if r, ok := app.AsRetireRefusal(err); ok && r.OverridableWith != "" {
			fmt.Printf("%s: refused (%s): %s\n  --%s would let it go ahead\n", id, r.Code, r.Sentence, flagFor(r.OverridableWith))
			return 1
		}
		return fail(err)
	}
	fmt.Printf("%s: retired (FAILED -> RETIRED), source %s", id, res.Retirement.SourceCheck)
	if res.Retirement.SupersededBy != "" {
		fmt.Printf(", superseded by %s", res.Retirement.SupersededBy)
	}
	fmt.Println()
	if res.PartialRemoved {
		fmt.Println("  removed the leftover partial download")
	}
	return 0
}

// flagFor turns an acknowledgement's wire name into this command's flag.
func flagFor(ack string) string {
	switch ack {
	case app.AckAcceptSourcePresent:
		return "accept-source-present"
	case app.AckNoSuccessor:
		return "no-successor"
	case app.AckAcceptUnverifiedCopy:
		return "accept-unverified-copy"
	}
	return ack
}
