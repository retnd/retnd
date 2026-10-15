package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/retnd/retnd/core/service"
)

// `quarantine list` (#1147, audit row P1-5): every backup being held for a
// human, and why. It is GET /api/v1/quarantine from a terminal, through the
// same BackupService read, so the two cannot disagree about what is held.
//
// Like the API it stops at the configuration: a quarantined backup whose set
// has since been removed is not listed, because none of the three actions can
// act on it (it stays on `artifacts`, marked quarantined). --json prints the
// contract's ListArtifactsResponse, the API's own body.
//
// It reads the journal directly the way `settings assurance` does, and logs
// to stderr so stdout is the listing and nothing else.

type quarantineListFlags struct {
	set    *string
	asJSON *bool
}

func declareQuarantineListFlags(fs *flag.FlagSet) quarantineListFlags {
	return quarantineListFlags{
		set:    fs.String("backup-set", "", "list only this backup set's held artifacts, named <source/backup-set> (quarantine list only)"),
		asJSON: fs.Bool("json", false, "emit JSON, the same body the API serves, instead of text (quarantine list only)"),
	}
}

// quarantineListOnlyFlags names the list-only flags the command line carried.
func quarantineListOnlyFlags(fs *flag.FlagSet) []string {
	var named []string
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "backup-set" || fl.Name == "json" {
			named = append(named, fl.Name)
		}
	})
	return named
}

func cmdQuarantineList(cfgPath string, fs *flag.FlagSet, f quarantineListFlags, rest []string) int {
	if len(rest) != 0 {
		return usageError("quarantine list takes no argument; %q is one (use --backup-set to narrow it)", rest[0])
	}
	if visited := visitedFlags(fs, "config", "backup-set", "json"); len(visited) > 0 {
		return usageError("quarantine list: --%s belongs to a different form of this command", visited[0])
	}
	if *f.set != "" && !isBackupSetID(*f.set) {
		return usageError("quarantine list: --backup-set %q is not a backup set id; a backup set id is exactly source/name", *f.set)
	}

	ctx := context.Background()
	svc, cleanup, err := openBackupServiceLoggingTo(ctx, cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	held, err := svc.ListArtifacts(ctx, service.ArtifactFilter{BackupSetID: *f.set, QuarantinedOnly: true})
	if err != nil {
		if errors.Is(err, service.ErrBackupSetNotFound) {
			return fail(fmt.Errorf("quarantine list: no such backup set %q", *f.set))
		}
		return fail(err)
	}
	if *f.asJSON {
		return printWorkflowJSON(service.ArtifactsWire(held))
	}
	if len(held) == 0 {
		fmt.Println("nothing is held in quarantine")
		return 0
	}
	for _, a := range held {
		reason := a.QuarantineReason
		if reason == "" {
			reason = "(the quarantining transition recorded no reason)"
		}
		fmt.Println(a.ID)
		fmt.Printf("  reason:         %s\n", reason)
		fmt.Printf("  irrecoverable:  %v\n", a.QuarantineIrrecoverable)
		fmt.Printf("  updated_at:     %s\n", a.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
	}
	fmt.Printf("%d quarantined artifact(s)\n", len(held))
	return 0
}
