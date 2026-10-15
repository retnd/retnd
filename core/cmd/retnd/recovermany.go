package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/service"
)

// `retnd recover --set` and `retnd recover <id> <id> ...` (#1106, #1107): the
// same recovery as one backup, for a whole backup set or a list of backups,
// into one destination. It is not a second verb and not a second restore
// path: `recover` already means "get retained bytes back, checked", the
// operand decides how many, and the work is service.RecoverMany, the call
// POST /api/v1/recoveries makes.

type manyArgs struct {
	cfgPath    string
	set        string
	ids        []string
	to         string
	copySel    string
	ack        bool
	asJSON     bool
	follow     bool
	newestOnly bool
	since      string
	preflight  bool
	ackSource  bool
}

// parseSince reads --since as a date or an RFC 3339 time.
func parseSince(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q is not a date (YYYY-MM-DD) or an RFC 3339 time", s)
}

func cmdRecoverMany(a manyArgs) int {
	if a.set != "" && len(a.ids) > 0 {
		return usageError("recover: give either --set or backup ids, not both")
	}
	if a.follow {
		return usageError("recover: --follow is for one backup; a set recover prints its own progress")
	}
	if a.set != "" {
		if src, name, ok := strings.Cut(a.set, "/"); !ok || src == "" || name == "" || strings.Contains(name, "/") {
			return usageError("recover: --set takes source/backup-set, got %q", a.set)
		}
	} else {
		if a.newestOnly || a.since != "" {
			return usageError("recover: --newest-only and --since need --set")
		}
		for _, id := range a.ids {
			if _, err := app.ParseArtifactID(id); err != nil {
				return usageError("recover: %v", err)
			}
		}
	}
	var target service.RecoveryTarget
	if a.to != "" {
		var err error
		if target, err = parseRecoverTarget(a.to); err != nil {
			return usageError("recover: %v", err)
		}
	}
	if target.Kind == "download" {
		return usageError("recover: several backups cannot go to standard output; use a name under the recovery staging root or medium:ID[/PREFIX]")
	}
	req := service.RecoverManyRequest{
		Artifacts: a.ids, Set: a.set, NewestOnly: a.newestOnly, Copy: a.copySel, Target: target, AcknowledgeCost: a.ack,
	}
	if a.since != "" {
		t, perr := parseSince(a.since)
		if perr != nil {
			return usageError("recover: %v", perr)
		}
		req.Since = t
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	svc, cleanup, err := openBackupServiceLoggingTo(ctx, a.cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	logStartup(ctx, obs.New(os.Stderr, obs.LevelFromEnv()), app.BuildVersionInfo(version, commit))

	// Issue #1191: with no --to, the backups go back where they came from.
	if a.to == "" {
		of := a.set
		if of == "" {
			of = a.ids[0]
		}
		def, why := svc.DefaultRecoveryTarget(of)
		if def.Kind == "" {
			return usageError("recover: --to is required here, because %s: a name under the recovery staging root, or medium:ID[/PREFIX] for another storage destination", why)
		}
		req.Target = def
	}
	if req.Target.Kind == "source" {
		req.Target.Acknowledged = a.ackSource
	}

	if a.preflight {
		plan, perr := svc.PlanRecoverMany(ctx, req)
		if perr != nil {
			if !a.asJSON {
				printManyPlan(plan, true)
			}
			return reportRecoverError(perr, a.asJSON)
		}
		if a.asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(manyPlanJSON(plan))
		} else {
			printManyPlan(plan, false)
		}
		return exitOK
	}

	progress := func(p service.RecoverManyProgress) {
		if a.asJSON {
			return
		}
		switch p.Kind {
		case "plan":
			printManyPlan(*p.Plan, false)
		case "item":
			printManyItem(p)
		}
	}
	res, rerr := svc.RecoverMany(ctx, service.RecoverySession{Actor: "cli"}, req, progress)
	if rerr != nil && len(res.Items) == 0 {
		return reportRecoverError(rerr, a.asJSON)
	}
	if a.asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(manyResultJSON(res))
	} else {
		printManyResult(res)
	}
	if rerr != nil {
		return reportRecoverError(rerr, false)
	}
	t := res.Totals
	if t.Failed+t.NeedsThaw+t.Quarantined+t.Unrecoverable > 0 {
		return exitRecoverPartial
	}
	return exitOK
}

func manyItemJSON(it service.RecoverManyItem) map[string]any {
	m := map[string]any{"artifact_id": it.ArtifactID, "name": it.Name, "status": it.Status}
	set := func(k string, v any, ok bool) {
		if ok {
			m[k] = v
		}
	}
	set("copy", it.Copy, it.Copy != "")
	set("size_bytes", it.SizeBytes, it.SizeBytes != nil)
	set("chargeable", true, it.Chargeable)
	set("bytes", it.Bytes, it.Bytes != 0)
	set("sha256", it.SHA256, it.SHA256 != "")
	set("path", it.Path, it.Path != "")
	set("key", it.Key, it.Key != "")
	set("file", it.Rel, it.Rel != "")
	set("code", it.Code, it.Code != "")
	set("message", it.Message, it.Message != "")
	if it.Thaw != nil {
		m["restore"] = map[string]string{"operation_id": it.Thaw.OperationID, "wait": it.Thaw.Wait, "billing": it.Thaw.Billing}
	}
	return m
}

func manyTotalsJSON(t service.RecoverManyTotals) map[string]any {
	return map[string]any{
		"count": t.Count, "ready": t.Ready, "recovered": t.Recovered, "failed": t.Failed, "skipped": t.Skipped,
		"needs_thaw": t.NeedsThaw, "quarantined": t.Quarantined, "unrecoverable": t.Unrecoverable, "bytes": t.Bytes,
	}
}

func manyTargetJSON(t service.RecoveryTarget) map[string]string {
	return map[string]string{"kind": t.Kind, "path": t.Path, "medium": t.Medium, "key": t.Key}
}

func manyItemsJSON(items []service.RecoverManyItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, manyItemJSON(it))
	}
	return out
}

func manyPlanJSON(p service.RecoverManyPlan) map[string]any {
	m := map[string]any{
		"set": p.Set, "target": manyTargetJSON(p.Target), "folder": p.Folder, "items": manyItemsJSON(p.Items),
		"totals": manyTotalsJSON(p.Totals), "chargeable_count": p.ChargeableCount, "chargeable_bytes": p.ChargeableBytes,
		"unknown_sizes": p.UnknownSizes, "reserve_bytes": p.ReserveBytes,
	}
	if p.FreeBytes != nil {
		m["free_bytes"] = *p.FreeBytes
	}
	return m
}

func manyResultJSON(r service.RecoverManyResult) map[string]any {
	m := map[string]any{
		"operation_id": r.OperationID, "set": r.Set, "target": manyTargetJSON(r.Target), "folder": r.Folder,
		"items": manyItemsJSON(r.Items), "totals": manyTotalsJSON(r.Totals), "cancelled": r.Cancelled,
	}
	if r.ManifestPath != "" {
		m["manifest_path"] = r.ManifestPath
	}
	if r.ManifestKey != "" {
		m["manifest_key"] = r.ManifestKey
	}
	if r.ManifestSHA256 != "" {
		m["manifest_sha256"] = r.ManifestSHA256
	}
	return m
}

func printManyPlan(p service.RecoverManyPlan, refused bool) {
	what := p.Set
	if what == "" {
		what = fmt.Sprintf("%d backups", p.Totals.Count)
	}
	note := ""
	if refused {
		note = " (refused)"
	}
	t := p.Totals
	fmt.Printf("pre-flight%s: %s -> %s\n", note, what, p.Folder)
	fmt.Printf("  files:               %d (ready %d, needs a thaw %d, quarantined %d, lost %d, not a restore point %d)\n",
		t.Count, t.Ready, t.NeedsThaw, t.Quarantined, t.Unrecoverable, t.Skipped)
	fmt.Printf("  size:                %d bytes", t.Bytes)
	if p.UnknownSizes > 0 {
		fmt.Printf(" (%d files have no recorded size)", p.UnknownSizes)
	}
	fmt.Println()
	if p.ChargeableCount > 0 {
		fmt.Printf("  cost:                %d files (%d bytes) are billed to read; archived copies need a provider restore first. Pass --acknowledge-cost to accept it for the whole set\n",
			p.ChargeableCount, p.ChargeableBytes)
	} else {
		fmt.Println("  cost:                nothing is billed")
	}
	if p.FreeBytes != nil {
		fmt.Printf("  staging disk:        %d bytes free, %d reserved\n", *p.FreeBytes, p.ReserveBytes)
	}
	for _, it := range p.Items {
		line := fmt.Sprintf("  %-14s %s", it.Status, it.ArtifactID)
		if it.Copy != "" {
			line += " (copy " + it.Copy + ")"
		}
		if it.SizeBytes != nil {
			line += fmt.Sprintf(" %d bytes", *it.SizeBytes)
		}
		if it.Message != "" {
			line += ": " + it.Message
		}
		fmt.Println(line)
	}
}

func printManyItem(p service.RecoverManyProgress) {
	it := p.Item
	line := fmt.Sprintf("[%d/%d] %-10s %s", p.Index, p.Total, it.Status, it.ArtifactID)
	switch it.Status {
	case service.RecoverManyRecovered:
		line += fmt.Sprintf("  %d bytes  sha256 %s", it.Bytes, it.SHA256)
	default:
		if it.Message != "" {
			line += ": " + it.Message
		}
	}
	fmt.Printf("%s  (%d of %d bytes)\n", line, p.DoneBytes, p.TotalBytes)
}

func printManyResult(r service.RecoverManyResult) {
	t := r.Totals
	fmt.Printf("recovered: %d\n", t.Recovered)
	fmt.Printf("failed: %d\n", t.Failed)
	fmt.Printf("skipped: %d\n", t.Skipped)
	fmt.Printf("needs a thaw: %d\n", t.NeedsThaw)
	fmt.Printf("quarantined: %d\n", t.Quarantined)
	fmt.Printf("lost: %d\n", t.Unrecoverable)
	fmt.Printf("bytes: %d\n", t.Bytes)
	for _, it := range r.Items {
		switch it.Status {
		case service.RecoverManyFailed, service.RecoverManyNeedsThaw, service.RecoverManyQuarantined, service.RecoverManyUnrecoverable:
			fmt.Printf("  %s: %s (%s)\n", it.Status, it.ArtifactID, firstNonBlank(it.Message, it.Code))
		}
	}
	switch {
	case r.ManifestPath != "":
		fmt.Printf("manifest: %s\n", r.ManifestPath)
	case r.ManifestKey != "":
		fmt.Printf("manifest: %s on %s\n", r.ManifestKey, r.Target.Medium)
	}
	if r.Cancelled {
		fmt.Println("the run was cancelled: the files above are complete and nothing half-written was kept")
	}
	if t.NeedsThaw > 0 {
		fmt.Printf("run this command again once the archived copies are readable (`%s status`)\n", cliecho.Binary)
	}
}

func firstNonBlank(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
