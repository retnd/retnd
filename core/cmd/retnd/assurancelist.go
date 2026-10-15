package main

import (
	"context"
	"fmt"
	"os"
	"slices"
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
	out := assuranceClaimJSON{State: string(c.State), Check: c.Check}
	if !c.At.IsZero() {
		out.At = c.At.UTC().Format(time.RFC3339)
	}
	return out
}

// parseBuckets reads --bucket: one bucket or a comma list of them. Empty is
// the tile's default, every backup in due, overdue or degraded.
func parseBuckets(v string) (map[service.Bucket]bool, error) {
	want := map[service.Bucket]bool{}
	if strings.TrimSpace(v) == "" {
		for _, b := range service.AssuranceBuckets() {
			if b != service.BucketUntested {
				want[b] = true
			}
		}
		return want, nil
	}
	known := service.AssuranceBuckets()
	names := make([]string, len(known))
	for i, b := range known {
		names[i] = string(b)
	}
	for _, part := range strings.Split(v, ",") {
		b := service.Bucket(strings.TrimSpace(part))
		if !slices.Contains(known, b) {
			return nil, fmt.Errorf("--bucket %q is not one of %s (untested is the tile's \"No recovery test\")", part, strings.Join(names, ", "))
		}
		want[b] = true
	}
	return want, nil
}

func cmdAssuranceList(args []string) int {
	fs, cfgPath := newFlagSet("assurance list")
	asJSON := fs.Bool("json", false, "print {generated_at, items} instead of text")
	set := fs.String("backup-set", "", "narrow to one source/backup-set")
	bucketFlag := fs.String("bucket", "", "due, overdue, degraded or untested, or a comma list of them; default is every backup in due, overdue or degraded")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("assurance list takes no operands; narrow it with --backup-set or --bucket")
	}
	want, err := parseBuckets(*bucketFlag)
	if err != nil {
		return usageError("assurance list: %v", err)
	}
	if *set != "" && !isBackupSetID(*set) {
		return usageError("assurance list: --backup-set %q is not a backup set id (source/name)", *set)
	}
	ctx := context.Background()
	svc, cleanup, err := openBackupServiceLoggingTo(ctx, *cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	artifacts, err := svc.ListArtifacts(ctx, service.ArtifactFilter{BackupSetID: *set})
	if err != nil {
		return fail(err)
	}
	due, err := svc.AssuranceDue(ctx, service.AssuranceDueFilter{BackupSetID: *set})
	if err != nil {
		return fail(err)
	}
	// Each set's recovery-test policy, read once. A read that fails is said
	// on stderr and makes that set's recovery claims unknown rather than
	// "tests on", which would count its backups due on no evidence.
	policies := map[string]service.SetRecoveryPolicy{}
	for _, a := range artifacts {
		if _, seen := policies[a.BackupSetID]; seen {
			continue
		}
		p, perr := svc.BackupSetAssurance(ctx, a.BackupSetID)
		switch {
		case perr != nil:
			fmt.Fprintf(os.Stderr, "assurance list: could not read %s's recovery-test policy, so its backups' recovery claims are unknown: %v\n", a.BackupSetID, perr)
			policies[a.BackupSetID] = service.SetRecoveryPolicy{Basis: service.PolicyUnavailable}
		case p.Override:
			policies[a.BackupSetID] = service.SetRecoveryPolicy{Off: p.Policy.RecoveryTest.Mode == "off", Basis: service.PolicyOwn}
		default:
			policies[a.BackupSetID] = service.SetRecoveryPolicy{Off: p.Policy.RecoveryTest.Mode == "off", Basis: service.PolicyDefault}
		}
	}
	now := time.Now()
	rows := service.ListAssurance(artifacts, due, policies, now)

	out := assuranceListJSON{GeneratedAt: now.UTC().Format(time.RFC3339), Items: []assuranceListItemJSON{}}
	var picked []service.AssuranceListing
	for _, r := range rows {
		if !slices.ContainsFunc(r.Buckets, func(b service.Bucket) bool { return want[b] }) {
			continue
		}
		picked = append(picked, r)
		buckets := make([]string, len(r.Buckets))
		for i, b := range r.Buckets {
			buckets[i] = string(b)
		}
		out.Items = append(out.Items, assuranceListItemJSON{
			ArtifactID: r.ArtifactID, ArtifactName: r.ArtifactName, BackupSetID: r.BackupSetID,
			Buckets: buckets, Content: claimJSON(r.Content), Recovery: claimJSON(r.Recovery), Reason: r.Reason,
		})
	}
	if *asJSON {
		return printWorkflowJSON(out)
	}
	if len(picked) == 0 {
		fmt.Println("nothing is due, overdue or degraded")
		return exitOK
	}
	for i, r := range picked {
		fmt.Printf("%-9s %s\n          %s\n          run it: retnd recovery-test %s --artifact %s\n", strings.Join(out.Items[i].Buckets, ","), r.ArtifactID, r.Reason, r.BackupSetID, r.ArtifactID)
	}
	return exitOK
}
