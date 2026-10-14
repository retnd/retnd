package service

import (
	"time"

	"github.com/retnd/retnd/core/apicontract"
)

// The wire objects `retnd ... --json` prints for the reads that used to print
// text only (#1152).
//
// Each function is a second mapping of a core/service read model onto the
// contract's own type, next to the web host's. core may not import the web
// host, so the two are held together by a test in apps/common/webhost that
// marshals both for the same input and compares them field for field,
// including which optional keys are absent. A mapping that drifts therefore
// fails there rather than in front of a script.
//
// Conventions shared with the handlers, restated here because they are what
// the diff test checks:
//
//   - a timestamp is RFC3339Nano and OMITTED (empty string, omitempty) when
//     the event has not happened;
//   - a list is an empty array, never null: one iterates and the other has to
//     be guarded;
//   - a counter nobody measured is null, never 0.

// readWireTime is the web host's formatTime.
func readWireTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// readWireTimePtr is the web host's formatTimePtr.
func readWireTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// SnapshotWire is the contract's Snapshot for one run.
func SnapshotWire(s Snapshot) apicontract.Snapshot {
	out := apicontract.Snapshot{
		BackupSetID:               s.BackupSetID,
		CompletedAt:               readWireTimePtr(s.CompletedAt),
		ConsistencyMode:           s.SourceConsistency,
		ContentReusedBytes:        s.ContentReusedBytes,
		DeleteRequestedAt:         readWireTimePtr(s.DeleteRequestedAt),
		Directories:               s.Directories,
		Engine:                    s.Engine,
		EntriesScanned:            s.EntriesScanned,
		Files:                     s.Files,
		LastKnownGood:             s.LastKnownGood,
		LogicalBytes:              s.LogicalBytes,
		OperationID:               s.OperationID,
		Phase:                     s.Phase,
		Reason:                    s.Reason,
		RepositoryBytesWritten:    s.RepositoryBytesWritten,
		RepositoryDomain:          s.RepositoryDomain,
		RunID:                     s.RunID,
		SnapshotID:                s.SnapshotID,
		SourceBytesRead:           s.SourceBytesRead,
		SourceComplete:            s.SourceComplete,
		StartedAt:                 readWireTime(s.StartedAt),
		VerificationLevel:         s.VerificationLevel,
		VerificationLevelAchieved: s.VerificationAchieved,
		VerificationStatus:        s.VerificationStatus,
	}
	if s.Duration != nil {
		// Whole seconds, truncated, as the handler does: a run is measured
		// in minutes and hours, and a fractional second is precision two
		// clients would round differently.
		seconds := int64(s.Duration.Seconds())
		out.DurationSeconds = &seconds
	}
	for _, h := range s.Holds {
		out.Holds = append(out.Holds, SnapshotHoldWire(h))
	}
	return out
}

// SnapshotHoldWire is the contract's SnapshotHold.
func SnapshotHoldWire(h SnapshotHold) apicontract.SnapshotHold {
	return apicontract.SnapshotHold{
		Active:      h.Active,
		BackupSetID: h.BackupSetID,
		HoldID:      h.HoldID,
		PlacedAt:    readWireTime(h.PlacedAt),
		PlacedBy:    h.PlacedBy,
		Reason:      h.Reason,
		ReleasedAt:  readWireTimePtr(h.ReleasedAt),
		ReleasedBy:  h.ReleasedBy,
		RunID:       h.RunID,
	}
}

// SnapshotsWire is GET .../snapshots.
func SnapshotsWire(snapshots []Snapshot) apicontract.ListSnapshotsResponse {
	out := apicontract.ListSnapshotsResponse{Snapshots: make([]apicontract.Snapshot, 0, len(snapshots))}
	for _, s := range snapshots {
		out.Snapshots = append(out.Snapshots, SnapshotWire(s))
	}
	return out
}

// SnapshotDetailWire is GET .../snapshots/{run}.
func SnapshotDetailWire(d SnapshotDetail) apicontract.SnapshotResponse {
	out := apicontract.SnapshotResponse{
		Snapshot:    SnapshotWire(d.Snapshot),
		Transitions: make([]apicontract.SnapshotTransition, 0, len(d.Transitions)),
	}
	for _, t := range d.Transitions {
		out.Transitions = append(out.Transitions, apicontract.SnapshotTransition{
			From:   t.From,
			To:     t.To,
			At:     readWireTime(t.At),
			Detail: t.Detail,
		})
	}
	return out
}

// SnapshotHoldsWire is GET .../holds.
func SnapshotHoldsWire(holds []SnapshotHold) apicontract.ListSnapshotHoldsResponse {
	out := apicontract.ListSnapshotHoldsResponse{Holds: make([]apicontract.SnapshotHold, 0, len(holds))}
	for _, h := range holds {
		out.Holds = append(out.Holds, SnapshotHoldWire(h))
	}
	return out
}

// SnapshotRetentionWire is GET .../snapshot-retention.
func SnapshotRetentionWire(p SnapshotRetentionPreview) apicontract.SnapshotRetentionResponse {
	out := apicontract.SnapshotRetentionResponse{
		GeneratedAt: readWireTime(p.GeneratedAt),
		Verdicts:    make([]apicontract.SnapshotRetentionVerdict, 0, len(p.Verdicts)),
	}
	for _, v := range p.Verdicts {
		verdict := apicontract.SnapshotRetentionVerdict{
			Action:     v.Action,
			HoldReason: v.HoldReason,
			Reason:     v.Reason,
			RunID:      v.RunID,
			SnapshotID: v.SnapshotID,
			StartedAt:  readWireTime(v.StartedAt),
		}
		for _, t := range v.Tiers {
			verdict.Tiers = append(verdict.Tiers, apicontract.SnapshotRetentionTier{SelectedBy: t.SelectedBy, Tier: t.Tier})
		}
		for _, h := range v.Holds {
			verdict.Holds = append(verdict.Holds, SnapshotHoldWire(h))
		}
		out.Verdicts = append(out.Verdicts, verdict)
	}
	return out
}
