package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/retnd/retnd/core/service"
)

// `retnd assurance list` (#1174): the terminal's twin of the Dashboard's
// Assurance tile. It lists the backups behind the Due, Overdue and Degraded
// counts, one per line, with why each is listed and how old its last check
// is. The classification is service.ListAssurance, a port of the web client's
// own, so the tile and this list name the same backups.
//
// It reads this host's journal the way `settings assurance due` does and has
// no route of its own, so there is no API field to match: --json prints
// {generated_at, items:[...]} with snake_case keys, the buckets as an array of
// names, and each claim as {state, at, check}.

type assuranceClaimJSON struct {
	State string `json:"state"`
	At    string `json:"at,omitempty"`
	Check string `json:"check,omitempty"`
}

type assuranceListItemJSON struct {
	ArtifactID   string             `json:"artifact_id"`
	ArtifactName string             `json:"artifact_name"`
	BackupSetID  string             `json:"backup_set_id"`
	Buckets      []string           `json:"buckets"`
	Content      assuranceClaimJSON `json:"content"`
	Recovery     assuranceClaimJSON `json:"recovery"`
	Reason       string             `json:"reason"`
}

type assuranceListJSON struct {
	GeneratedAt string                  `json:"generated_at"`
	Items       []assuranceListItemJSON `json:"items"`
}

func claimJSON(c service.AssuranceClaim) assuranceClaimJSON {
	out := assuranceClaimJSON{State: c.State, Check: c.Check}
	if !c.At.IsZero() {
		out.At = c.At.UTC().Format(time.RFC3339)
	}
	return out
}

func cmdAssuranceList(cfgPath, set, bucket string, asJSON bool) int {
	if bucket != "" {
		ok := false
		for _, b := range service.AssuranceBuckets {
			ok = ok || b == bucket
		}
		if !ok {
			return usageError("assurance list: --bucket %q is not one of %s", bucket, strings.Join(service.AssuranceBuckets, ", "))
		}
	}
	if set != "" && !isBackupSetID(set) {
		return usageError("assurance list: --backup-set %q is not a backup set id (source/name)", set)
	}
	ctx := context.Background()
	svc, cleanup, err := openBackupServiceLoggingTo(ctx, cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	artifacts, err := svc.ListArtifacts(ctx, service.ArtifactFilter{BackupSetID: set})
	if err != nil {
		return fail(err)
	}
	due, err := svc.AssuranceDue(ctx, service.AssuranceDueFilter{BackupSetID: set})
	if err != nil {
		return fail(err)
	}
	off := map[string]bool{}
	for _, a := range artifacts {
		if _, seen := off[a.BackupSetID]; seen {
			continue
		}
		p, perr := svc.BackupSetAssurance(ctx, a.BackupSetID)
		off[a.BackupSetID] = perr == nil && p.Policy.RecoveryTest.Mode == "off"
	}
	now := time.Now()
	rows := service.ListAssurance(artifacts, due, off, now)

	keep := func(l service.AssuranceListing) bool {
		for _, b := range l.Buckets {
			if bucket != "" {
				if b == bucket {
					return true
				}
			} else if b != service.BucketUntested {
				return true
			}
		}
		return false
	}
	out := assuranceListJSON{GeneratedAt: now.UTC().Format(time.RFC3339), Items: []assuranceListItemJSON{}}
	var picked []service.AssuranceListing
	for _, r := range rows {
		if !keep(r) {
			continue
		}
		picked = append(picked, r)
		out.Items = append(out.Items, assuranceListItemJSON{
			ArtifactID: r.ArtifactID, ArtifactName: r.ArtifactName, BackupSetID: r.BackupSetID,
			Buckets: append([]string{}, r.Buckets...), Content: claimJSON(r.Content), Recovery: claimJSON(r.Recovery), Reason: r.Reason,
		})
	}
	if asJSON {
		return printWorkflowJSON(out)
	}
	if len(picked) == 0 {
		fmt.Println("nothing is due, overdue or degraded")
		return exitOK
	}
	for _, r := range picked {
		fmt.Printf("%-9s %s\n          %s\n          run it: retnd recovery-test %s --artifact %s\n", strings.Join(r.Buckets, ","), r.ArtifactID, r.Reason, r.BackupSetID, r.ArtifactID)
	}
	return exitOK
}
