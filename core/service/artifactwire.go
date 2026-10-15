package service

import (
	"time"

	"github.com/retnd/retnd/core/apicontract"
)

// ArtifactWire is the contract's Artifact for one backup: the body GET
// /backups/{source}/{set}/{name} serves, built from the same read model.
//
// `retnd artifacts --json` and `retnd quarantine list --json` print this
// (#1147). It is a second mapping of core/service.Artifact next to the web
// host's own, and the two are held together by a test in apps/common/webhost
// that marshals both for the same artifact and compares them field for field,
// including which optional fields are absent: core may not import the web
// host, so the diff lives on its side of the boundary.
//
// Timestamps are RFC3339Nano and OMITTED when the event has not happened;
// the unknown scrub and recovery states are omitted too, never spelled.
func ArtifactWire(a Artifact) apicontract.Artifact {
	policy := a.RetentionPolicy
	if policy == "" {
		// Read an unset policy as the alarming answer, as the handler does:
		// silence must not read as "governed".
		policy = RetentionPolicyNone
	}
	placements := make([]apicontract.Placement, 0, len(a.Placements))
	for _, p := range a.Placements {
		placements = append(placements, placementWire(p))
	}
	return apicontract.Artifact{
		BackupSetID:             a.BackupSetID,
		Checksum:                a.Checksum,
		ChecksumAlgorithm:       a.ChecksumAlgorithm,
		DiscoveredAt:            wireTime(a.DiscoveredAt),
		ID:                      a.ID,
		LocalPath:               a.LocalPath,
		Name:                    a.Name,
		Placements:              placements,
		QuarantineIrrecoverable: a.QuarantineIrrecoverable,
		QuarantineReason:        a.QuarantineReason,
		Quarantined:             a.Quarantined,
		RemotePath:              a.RemotePath,
		RemoteSourceRemovedAt:   wireTime(a.RemoteSourceRemovedAt),
		RetentionPolicy:         policy,
		RetentionTier:           a.RetentionTier,
		SetName:                 a.SetName,
		SizeBytes:               a.SizeBytes,
		SourceName:              a.SourceName,
		State:                   a.State,
		UpdatedAt:               wireTime(a.UpdatedAt),
		Validation:              a.Validation,
		ValidationDetail:        a.ValidationDetail,
	}
}

// ArtifactsWire is the contract's ListArtifactsResponse. An empty listing is
// an empty array, never null: one iterates and the other has to be guarded.
func ArtifactsWire(as []Artifact) apicontract.ListArtifactsResponse {
	out := apicontract.ListArtifactsResponse{Artifacts: make([]apicontract.Artifact, 0, len(as))}
	for _, a := range as {
		out.Artifacts = append(out.Artifacts, ArtifactWire(a))
	}
	return out
}

func placementWire(p Placement) apicontract.Placement {
	return apicontract.Placement{
		Access:                p.Access,
		AssuranceState:        wireUnlessUnknown(p.ScrubState),
		ConsecutiveFailures:   p.ScrubConsecutiveFailures,
		LastCheckedAt:         wireTime(p.ScrubLastCheckedAt),
		LastFailureAt:         wireTime(p.ScrubLastFailureAt),
		LastFailureReason:     p.ScrubLastFailureReason,
		Location:              p.Location,
		Medium:                p.Medium,
		MediumType:            p.MediumType,
		RecoveryCheck:         p.RecoveryCheck,
		RecoveryFailureReason: p.RecoveryFailureReason,
		RecoveryState:         wireUnlessUnknown(p.RecoveryState),
		RecoveryTestedAt:      wireTime(p.RecoveryTestedAt),
		SizeBytes:             p.SizeBytes,
		Status:                p.Status,
		StorageClass:          p.StorageClass,
		VerificationClass:     p.VerificationClass,
		VerifiedAt:            wireTime(p.VerifiedAt),
	}
}

func wireTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func wireUnlessUnknown(s string) string {
	if s == "unknown" {
		return ""
	}
	return s
}
