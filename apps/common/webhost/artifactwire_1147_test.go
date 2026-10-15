package webhost

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// #1147: `retnd artifacts --json` and `retnd quarantine list --json` print
// what core/service.ArtifactWire builds, and this file is where that is held
// to what the API serves. core may not import this package, so the diff lives
// here: the handler's own mapping and the CLI's mapping are marshalled and
// compared field for field, over an artifact in which every optional field is
// set and over one in which every optional field is absent.

func fullArtifact() service.Artifact {
	at := time.Date(2026, 10, 1, 12, 30, 0, 123456789, time.UTC)
	size := int64(4096)
	return service.Artifact{
		ID: "prod/pg/dump.zst", BackupSetID: "prod/pg", SourceName: "prod", SetName: "pg", Name: "dump.zst",
		RemotePath: "/remote/dump.zst", LocalPath: "/local/dump.zst", State: "QUARANTINED",
		DiscoveredAt: at, UpdatedAt: at.Add(time.Hour), SizeBytes: 4096,
		Checksum: "abc", ChecksumAlgorithm: "sha256", Validation: "failed", ValidationDetail: "bad schema",
		RemoteSourceRemovedAt: at.Add(2 * time.Hour),
		Quarantined:           true, QuarantineIrrecoverable: true, QuarantineReason: "hash mismatch",
		RetentionTier: "daily", RetentionPolicy: service.RetentionPolicyConfigured,
		Placements: []service.Placement{{
			Medium: "cold", MediumType: "s3", Location: "k/dump.zst", SizeBytes: &size, StorageClass: "GLACIER",
			VerificationClass: "content", VerifiedAt: at, Access: "requires_restore", Status: "ACTIVE",
			ScrubState: "failed", ScrubLastCheckedAt: at, ScrubLastFailureAt: at, ScrubLastFailureReason: "short read",
			ScrubConsecutiveFailures: 3,
			RecoveryState:            "readable", RecoveryTestedAt: at, RecoveryFailureReason: "r", RecoveryCheck: "builtin",
		}},
	}
}

func sparseArtifact() service.Artifact {
	return service.Artifact{
		ID: "prod/pg/a", BackupSetID: "prod/pg", SourceName: "prod", SetName: "pg", Name: "a",
		State: "COMMITTED", Validation: "pending", RetentionPolicy: service.RetentionPolicyNone,
		Placements: []service.Placement{{Medium: "local", MediumType: "local", Location: "/l/a", Access: "immediate", Status: "ACTIVE", ScrubState: "unknown", RecoveryState: "unknown"}},
	}
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestIssue1147_TheCLIsArtifactWireIsTheHandlersArtifactFieldForField(t *testing.T) {
	for name, a := range map[string]service.Artifact{"every field set": fullArtifact(), "every optional field absent": sparseArtifact()} {
		t.Run(name, func(t *testing.T) {
			api, cli := asMap(t, toArtifactResponse(a)), asMap(t, service.ArtifactWire(a))
			if !reflect.DeepEqual(api, cli) {
				t.Errorf("the CLI's --json differs from the API body\napi: %v\ncli: %v", api, cli)
			}
			if len(api) < 12 {
				t.Errorf("the API body has only %d keys; the comparison would pass on two empty objects", len(api))
			}
		})
	}
}

func TestIssue1147_TheCLIsListWireIsTheHandlersListFieldForField(t *testing.T) {
	as := []service.Artifact{fullArtifact(), sparseArtifact()}
	api := listArtifactsResponse{Artifacts: []artifactResponse{toArtifactResponse(as[0]), toArtifactResponse(as[1])}}
	if got, want := asMap(t, service.ArtifactsWire(as)), asMap(t, api); !reflect.DeepEqual(got, want) {
		t.Errorf("list body differs\napi: %v\ncli: %v", want, got)
	}
	// An empty listing is an array, never null: on both sides.
	if got := asMap(t, service.ArtifactsWire(nil)); !reflect.DeepEqual(got, map[string]any{"artifacts": []any{}}) {
		t.Errorf("an empty listing is %v, want {\"artifacts\":[]}", got)
	}
}
