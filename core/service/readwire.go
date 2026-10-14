package service

import (
	"context"
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

// RepositoryHealthWire is the contract's RepositoryHealth for one domain.
func RepositoryHealthWire(r RepositoryHealth) apicontract.RepositoryHealth {
	out := apicontract.RepositoryHealth{
		BackupSets:             r.BackupSets,
		ClockSane:              r.ClockSane,
		CredentialsValid:       r.CredentialsValid,
		Detail:                 r.Detail,
		Domain:                 r.Domain,
		LastMaintenanceAt:      readWireTime(r.LastMaintenanceAt),
		LastMaintenanceResult:  r.LastMaintenanceResult,
		LastSnapshotAt:         readWireTime(r.LastSnapshotAt),
		LastSnapshotStatus:     r.LastSnapshotStatus,
		LastVerificationAt:     readWireTime(r.LastVerificationAt),
		LastVerificationStatus: r.LastVerificationStatus,
		MaintenanceOverdue:     r.MaintenanceOverdue,
		MayShare:               r.MayShare,
		Reachable:              r.Reachable,
		Readable:               r.Readable,
		State:                  r.State,
		Writable:               r.Writable,
	}
	if r.ClockSkew != nil {
		// Whole seconds, signed, as the handler sends it.
		seconds := int64(r.ClockSkew.Seconds())
		out.ClockSkewSeconds = &seconds
	}
	return out
}

// RepositoriesWire is GET /repositories.
func RepositoriesWire(report RepositoryHealthReport) apicontract.ListRepositoriesResponse {
	out := apicontract.ListRepositoriesResponse{
		GeneratedAt:  readWireTime(report.GeneratedAt),
		Repositories: make([]apicontract.RepositoryHealth, 0, len(report.Repositories)),
	}
	for _, r := range report.Repositories {
		out.Repositories = append(out.Repositories, RepositoryHealthWire(r))
	}
	return out
}

// RepositoryMaintenanceWire is GET /repositories/{domain}/maintenance.
func RepositoryMaintenanceWire(m RepositoryMaintenance) apicontract.RepositoryMaintenance {
	return apicontract.RepositoryMaintenance{
		Domain:         m.Domain,
		Due:            m.Due,
		DueMode:        m.DueMode,
		DueReason:      m.DueReason,
		Failing:        m.Failing,
		Failures:       m.Failures,
		LastFullAt:     readWireTime(m.LastFullAt),
		LastQuickAt:    readWireTime(m.LastQuickAt),
		NextEligibleAt: readWireTime(m.NextEligibleAt),
		Overdue:        m.Overdue,
		Owner:          m.Owner,
		ReclaimedBytes: m.ReclaimedBytes,
		Runs:           m.Runs,
	}
}

func retentionTiersWire(tiers []RetentionTier) []apicontract.RetentionTier {
	out := make([]apicontract.RetentionTier, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, apicontract.RetentionTier{
			Granularity: t.Granularity,
			Keep:        t.Keep,
			Medium:      t.Medium,
			Name:        t.Name,
			PeriodDays:  t.PeriodDays,
			WindowUnit:  t.WindowUnit,
		})
	}
	return out
}

// RetentionSettingsWire is the contract's RetentionSettings: a resolved
// policy, with the chain always an array.
func RetentionSettingsWire(r RetentionSettings) apicontract.RetentionSettings {
	return apicontract.RetentionSettings{
		ProtectLastKnownGood: r.ProtectLastKnownGood,
		Tiers:                retentionTiersWire(r.Tiers),
		Timezone:             r.Timezone,
		WeekStartsOn:         r.WeekStartsOn,
	}
}

// SettingsReadWire is GET /settings. firstRun is the handler's own question
// (no backup set declared, default settings and an empty journal) and is
// answered by SettingsAreFirstRun; persisted is true because a CLI that has
// read these settings read them off a configuration file.
func SettingsReadWire(s Settings, firstRun bool) apicontract.SettingsResponse {
	retention := RetentionSchema()
	storage := StorageSchema()
	classes := make([]apicontract.VerificationClassInfo, 0, len(storage.VerificationClasses))
	for _, c := range storage.VerificationClasses {
		classes = append(classes, apicontract.VerificationClassInfo{
			Class:           c.Class,
			DownloadsObject: c.DownloadsObject,
			Proves:          c.Proves,
			Requires:        c.Requires,
		})
	}
	mediums := make([]apicontract.StorageMediumSummary, 0, len(s.Mediums))
	for _, m := range s.Mediums {
		mediums = append(mediums, apicontract.StorageMediumSummary{
			Bucket:               m.Bucket,
			ConnectionUnverified: m.ConnectionUnverified,
			Endpoint:             m.Endpoint,
			ID:                   m.ID,
			IsDefault:            m.IsDefault,
			IsLocal:              m.IsLocal,
			Path:                 m.Path,
			Prefix:               m.Prefix,
			ReadsRequireRestore:  m.ReadsRequireRestore,
			Region:               m.Region,
			StorageClass:         m.StorageClass,
			Type:                 m.Type,
			UploadVerification:   m.UploadVerification,
		})
	}
	return apicontract.SettingsResponse{
		Capacity: apicontract.CapacitySettings{
			BackupRoot:           s.Capacity.BackupRoot,
			BackupRootConfigured: s.Capacity.BackupRootConfigured,
			CapBytes:             s.Capacity.CapBytes,
			CriticalFreeBytes:    s.Capacity.CriticalFreeBytes,
			SafetyMarginBytes:    s.Capacity.SafetyMarginBytes,
			WarningFreeBytes:     s.Capacity.WarningFreeBytes,
		},
		FirstRun:  firstRun,
		Mediums:   mediums,
		Persisted: true,
		Retention: RetentionSettingsWire(s.Retention),
		Schema: apicontract.SettingsSchema{
			Retention: apicontract.RetentionSchema{
				DefaultTiers:     retentionTiersWire(retention.DefaultTiers),
				Granularities:    retention.Granularities,
				KeepMax:          retention.KeepMax,
				PeriodDaysMax:    retention.PeriodDaysMax,
				ReservedTierName: retention.ReservedTierName,
				TierNamePattern:  retention.TierNamePattern,
				WindowUnits:      retention.WindowUnits,
			},
			Service: apicontract.ServiceSchema{
				MinPollIntervalSeconds: int(ServiceSchema().MinPollInterval / time.Second),
			},
			Storage: apicontract.StorageSchema{
				MediumDisclosure:    storage.MediumDisclosure,
				RetrievalDisclosure: storage.RetrievalDisclosure,
				VerificationClasses: classes,
			},
		},
		Service: apicontract.ServiceSettings{PollIntervalSeconds: int(s.Service.PollInterval / time.Second)},
	}
}

// SettingsAreFirstRun is the web host's `first_run` for GET /settings: these
// are the engine's defaults and nobody has changed anything yet. It needs no
// backup set declared, the settings still at their defaults, and a journal
// that holds nothing, because a configuration with no sets over a journal full
// of backups is a deployment that lost its sets, not one that never had any.
// A read that fails errs toward the settled reading, false.
func (b *BackupService) SettingsAreFirstRun(ctx context.Context, s Settings) bool {
	if !IsDefaultSettings(s) {
		return false
	}
	sets, err := b.ListBackupSets(ctx)
	if err != nil || len(sets) != 0 {
		return false
	}
	n, err := b.JournalArtifactCount(ctx)
	return err == nil && n == 0
}

// BackupSetRetentionWire is GET .../retention, and the body PUT and DELETE
// answer with. override is present and null for a set that inherits.
func BackupSetRetentionWire(r BackupSetRetention) apicontract.BackupSetRetention {
	out := apicontract.BackupSetRetention{
		BackupSetID: r.BackupSetID,
		Deployment:  RetentionSettingsWire(r.Deployment),
		Effective:   RetentionSettingsWire(r.Effective),
		IsOverride:  r.IsOverride,
	}
	if r.Override != nil {
		o := apicontract.RetentionOverride{
			DailyDays:     r.Override.DailyDays,
			MonthlyMonths: r.Override.MonthlyMonths,
			Timezone:      r.Override.Timezone,
			WeekStartsOn:  r.Override.WeekStartsOn,
			WeeklyMonths:  r.Override.WeeklyMonths,
		}
		if r.Override.Tiers != nil {
			o.Tiers = retentionTiersWire(r.Override.Tiers)
		}
		if r.Override.ProtectLastKnownGood != nil {
			protect := *r.Override.ProtectLastKnownGood
			o.ProtectLastKnownGood = &protect
		}
		out.Override = &o
	}
	return out
}
