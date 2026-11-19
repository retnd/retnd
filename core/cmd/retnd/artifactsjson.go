package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/service"
)

// `artifacts --json` and `artifacts --state` (#1147, audit rows P0-2 and
// P1-7's artifacts line).
//
// --json prints the wire object the API returns for the same call: the
// contract's Artifact for `artifacts <id>` (GET /backups/{id}) and its
// ListArtifactsResponse for the list (GET /backups). It is built by the same
// BackupService read the web host serves, through service.ArtifactWire, so
// the per-copy scrub and recovery record, the retention policy and the
// quarantine reason are the API's own; apps/common/webhost holds that
// mapping to the handler's field for field.
//
// It reads the journal directly the way `settings assurance` does, with the
// log on stderr so stdout is the one JSON value. What it does not do is the
// text form's mode announcement and engine agreement check: a script asked
// for data, and the body is the API's.

// parseArtifactStateFlag reads --state. Empty means no narrowing; anything
// else must be one of the lifecycle's states (case-insensitively, since the
// states are spelled in capitals and nobody should be refused for a shift key).
func parseArtifactStateFlag(v string) (string, bool) {
	if v == "" {
		return "", true
	}
	want := strings.ToUpper(v)
	for _, s := range lifecycle.AllStates {
		if string(s) == want {
			return want, true
		}
	}
	return "", false
}

func lifecycleStateNames() string {
	names := make([]string, 0, len(lifecycle.AllStates))
	for _, s := range lifecycle.AllStates {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}

func withState(records []state.Record, want string) []state.Record {
	kept := make([]state.Record, 0, len(records))
	for _, r := range records {
		if r.State == want {
			kept = append(kept, r)
		}
	}
	return kept
}

// receivedIn keeps the records that arrived inside rng (#1251), by the
// discovered_at the API serves and the Backups page filters on.
func receivedIn(records []state.Record, rng receivedRange) []state.Record {
	kept := make([]state.Record, 0, len(records))
	for _, r := range records {
		if rng.holds(r.DiscoveredAt) {
			kept = append(kept, r)
		}
	}
	return kept
}

// bucketIDsFor opens the backup service the JSON form reads through and asks
// it which backups are in buckets, for the text form, which reads the journal
// through internal/app.
func bucketIDsFor(ctx context.Context, cfgPath, set string, buckets map[service.Bucket]bool) (map[string]bool, error) {
	svc, cleanup, err := openBackupServiceLoggingTo(ctx, cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return assuranceBucketIDs(ctx, svc, set, buckets)
}

func artifactsJSON(ctx context.Context, cfgPath string, operands []string, source, set, wantState string, rng receivedRange, buckets map[service.Bucket]bool) int {
	if len(operands) == 1 && (source != "" || set != "") {
		return usageError("artifacts: --source/--backup-set cannot be combined with an artifact argument")
	}
	if len(operands) == 0 && set != "" && !isBackupSetID(set) {
		return usageError("artifacts --json: --backup-set %q is not a backup set id; the wire filter is exactly source/name", set)
	}

	svc, cleanup, err := openBackupServiceLoggingTo(ctx, cfgPath, readsConfig, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	if len(operands) == 1 {
		id, err := app.ParseArtifactID(operands[0])
		if err != nil {
			return fail(err)
		}
		a, err := svc.GetArtifact(ctx, id.String())
		if err != nil {
			return fail(err)
		}
		return printWorkflowJSON(service.ArtifactWire(a))
	}

	all, err := svc.ListArtifacts(ctx, service.ArtifactFilter{BackupSetID: set})
	if err != nil {
		if errors.Is(err, service.ErrBackupSetNotFound) {
			return fail(fmt.Errorf("artifacts: no such backup set %q", set))
		}
		return fail(err)
	}
	var inBucket map[string]bool
	if buckets != nil {
		if inBucket, err = assuranceBucketIDs(ctx, svc, set, buckets); err != nil {
			return fail(err)
		}
	}
	kept := make([]service.Artifact, 0, len(all))
	for _, a := range all {
		// --source has no wire equivalent (GET /backups filters on a
		// source/set id and nothing else), so it narrows the body here.
		if source != "" && a.SourceName != source {
			continue
		}
		if wantState != "" && a.State != wantState {
			continue
		}
		// Nor does the received-date range (#1251): the page applies it in
		// the browser over the same list.
		if !rng.holds(a.DiscoveredAt) {
			continue
		}
		if inBucket != nil && !inBucket[a.ID] {
			continue
		}
		kept = append(kept, a)
	}
	return printWorkflowJSON(service.ArtifactsWire(kept))
}
