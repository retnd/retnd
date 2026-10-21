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
	until      string
	preflight  bool
	ackSource  bool
	all        bool
}

// receivedRange is --since and --until, the received-date range `artifacts`
// and `recover --set` share with the Backups page's "Received on or after" and
// "Received on or before" (#1251). Both bounds are inclusive and either may be
// absent (the zero time). The time compared is a backup's updated_at, which is
// the page's receivedAt and what service.narrowSet reads.
type receivedRange struct {
	since, until time.Time
}

// parseReceivedRange reads the two flags. A DATE is YYYY-MM-DD or an RFC 3339
// time. A bare date is the whole of that day in UTC, the zone the journal
// stores times in and the API serves them in: --since is its first moment and
// --until its last, so --until 2026-03-15 keeps a backup received at 23:30
// that day, the way the page's date input does. An RFC 3339 time is that
// moment exactly. An --until before the --since is refused rather than
// answered with nothing, since it is a typo and not a question.
func parseReceivedRange(since, until string) (receivedRange, error) {
	var r receivedRange
	var err error
	if since != "" {
		if r.since, err = parseDateBound("--since", since, false); err != nil {
			return r, err
		}
	}
	if until != "" {
		if r.until, err = parseDateBound("--until", until, true); err != nil {
			return r, err
		}
	}
	if !r.since.IsZero() && !r.until.IsZero() && r.until.Before(r.since) {
		return r, fmt.Errorf("--until %s is before --since %s, so nothing can be in that range", until, since)
	}
	return r, nil
}

func parseDateBound(flag, s string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endOfDay {
			t = t.AddDate(0, 0, 1).Add(-time.Nanosecond)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%s %q is not a date (YYYY-MM-DD) or an RFC 3339 time", flag, s)
}

func (r receivedRange) set() bool { return !r.since.IsZero() || !r.until.IsZero() }

// holds reports whether t is inside the range, both ends included.
func (r receivedRange) holds(t time.Time) bool {
	return (r.since.IsZero() || !t.Before(r.since)) && (r.until.IsZero() || !t.After(r.until))
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
		if a.newestOnly || a.since != "" || a.until != "" {
			return usageError("recover: --newest-only, --since and --until need --set")
		}
		for _, id := range a.ids {
			if _, err := app.ParseArtifactID(id); err != nil {
				return usageError("recover: %v", err)
			}
		}
	}
	if a.all && a.set == "" {
		return usageError("recover: --all needs --set")
	}
	if a.all && (a.newestOnly || a.since != "" || a.until != "") {
		return usageError("recover: --all is every restore point, so it does not go with --newest-only, --since or --until")
	}
	rng, rerr := parseReceivedRange(a.since, a.until)
	if rerr != nil {
		return usageError("recover: %v", rerr)
	}
	var target service.RecoveryTarget
	if a.to != "" {
		var err error
		if target, err = parseRecoverTarget(a.to); err != nil {
			return usageError("recover: %v", err)
		}
		if err := checkSourceFlag(target, a.ackSource); err != nil {
			return usageError("recover: %v", err)
		}
	}
	if target.Kind == "download" {
		return usageError("recover: several backups cannot go to standard output; use a name under the recovery staging root or medium:ID[/PREFIX]")
	}
	req := service.RecoverManyRequest{
		Artifacts: a.ids, Set: a.set, NewestOnly: a.newestOnly, Since: rng.since, Until: rng.until,
		Copy: a.copySel, Target: target, AcknowledgeCost: a.ack,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	svc, cleanup, err := openBackupServiceLoggingTo(ctx, a.cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	logStartup(ctx, obs.New(os.Stderr, obs.LevelFromEnv()), app.BuildVersionInfo(version, commit))

	// Issue #1191: with no --to, the backups go back where they came from,
	// where that can work (#1193), decided from the configuration alone.
	of := a.set
	if of == "" {
		of = a.ids[0]
	}
	if a.to == "" {
		def, why := svc.DefaultRecoveryTarget(of)
		if def.Kind == "" {
			return usageError("recover: --to is required here, because %s: a name under the recovery staging root, or medium:ID[/PREFIX] for another storage destination", why)
		}
		req.Target = def
	}
	if service.RecoveryTargetWritesToSourceHost(req.Target) {
		// A whole set onto a production host is spelled out (#1193).
		if a.set != "" && !a.newestOnly && !rng.set() && !a.all && !a.preflight {
			return usageError("recover: a whole backup set onto its source host is said explicitly: add --newest-only (the newest restore point, which the dialog defaults to), --since DATE and/or --until DATE, or --all for every retained restore point")
		}
		if a.preflight {
			return sourcePreflight(ctx, svc, of, req.Target.Path, a.asJSON, func() int { return manyPreflight(ctx, svc, req, a.asJSON) })
		}
		acked, code := acknowledgeSourceDestination(svc, of, req.Target.Path, a.ackSource, a.asJSON)
		if code != exitOK {
			return code
		}
		req.Target = acked
	}

	if a.preflight {
		return manyPreflight(ctx, svc, req, a.asJSON)
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

// manyPreflight prints a plan, or the refusal with the plan it was about.
func manyPreflight(ctx context.Context, svc *service.BackupService, req service.RecoverManyRequest, asJSON bool) int {
	plan, perr := svc.PlanRecoverMany(ctx, req)
	if perr != nil {
		if !asJSON {
			printManyPlan(plan, true)
		}
		return reportRecoverError(perr, asJSON)
	}
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(manyPlanJSON(plan))
	} else {
		printManyPlan(plan, false)
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
	set("verified", it.Verified, it.Verified != "")
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
	if p.DestinationID != "" {
		m["destination_id"], m["url"], m["source_reserve_bytes"] = p.DestinationID, p.URL, p.SourceReserveBytes
	}
	if p.SourceFreeBytes != nil {
		m["source_free_bytes"] = *p.SourceFreeBytes
	}
	return m
}

func manyResultJSON(r service.RecoverManyResult) map[string]any {
	m := map[string]any{
		"operation_id": r.OperationID, "set": r.Set, "target": manyTargetJSON(r.Target), "folder": r.Folder,
		"items": manyItemsJSON(r.Items), "totals": manyTotalsJSON(r.Totals), "cancelled": r.Cancelled,
	}
	if r.DestinationID != "" {
		m["destination_id"], m["url"] = r.DestinationID, r.URL
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
	if p.URL != "" {
		fmt.Printf("  source host:         %s\n", p.URL)
		if p.SourceFreeBytes != nil {
			fmt.Printf("  free on the host:    %d bytes, %d kept free\n", *p.SourceFreeBytes, p.SourceReserveBytes)
		} else {
			fmt.Println("  free on the host:    unknown: the host did not say, so this run is not checked against it")
		}
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
