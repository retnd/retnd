package service

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/backend"
	"github.com/retnd/retnd/core/internal/config"
)

// #1154: a local_volume's `path` is a field this manager can store now.
//
// config.StorageMedium and StorageMediumSpec both carry Path (#666), and the
// seed that creates the `local` destination writes it, but the manifest-shaped
// write refused the field by name as one with nowhere to go. That left a
// local_volume unconfigurable through PUT /storage-mediums/{id}/configuration
// and so from `retnd medium configure`, which is the whole of what the
// issue sets out to fix.

var localVolumeManifest = backend.Manifest{
	ID: "local_volume",
	Fields: []backend.Field{
		{ID: "path", Kind: backend.KindPath, Required: true},
		{ID: "prefix", Kind: backend.KindKeyPrefix},
		{ID: "upload_verification", Kind: backend.KindEnum},
	},
}

func TestIssue1154_ALocalVolumesPathIsFoldedOntoTheSpec(t *testing.T) {
	spec, err := storageMediumFromFields(
		config.StorageMedium{ID: "usb_disk", Type: "local_volume"},
		localVolumeManifest,
		StorageMediumConfiguration{Fields: map[string]string{"path": "/mnt/usb", "prefix": "retnd"}},
	)
	if err != nil {
		t.Fatalf("folding a local_volume configuration: %v", err)
	}
	if spec.Path != "/mnt/usb" {
		t.Errorf("spec.Path = %q, want /mnt/usb: the one value that decides where the backups go was dropped", spec.Path)
	}
	if spec.Prefix != "retnd" || spec.Type != "local_volume" {
		t.Errorf("spec = %+v, want prefix retnd on a local_volume", spec)
	}
}

func TestIssue1154_ALocalVolumesPathIsReadBack(t *testing.T) {
	got := mediumFieldValue(config.StorageMedium{ID: "usb_disk", Type: "local_volume", Path: "/mnt/usb"}, "path")
	if got != "/mnt/usb" {
		t.Errorf("mediumFieldValue(path) = %q, want /mnt/usb: a form could not offer an edit of a destination whose directory it cannot read", got)
	}
}

func TestIssue1154_AFieldWithNowhereToGoIsStillRefusedByName(t *testing.T) {
	// The control the refusal keeps: with `path` plumbed, the example of a
	// field this manager cannot store has to be one that really has no home.
	_, err := storageMediumFromFields(
		config.StorageMedium{ID: "volume_one", Type: "local_volume"},
		backend.Manifest{
			ID:     "local_volume",
			Fields: []backend.Field{{ID: "mount_options", Kind: backend.KindString}},
		},
		StorageMediumConfiguration{Fields: map[string]string{"mount_options": "noatime,ro"}},
	)
	if err == nil {
		t.Fatal("a field this manager cannot store was accepted; a save would have dropped it silently")
	}
	if !strings.Contains(err.Error(), "mount_options") {
		t.Errorf("the refusal does not name the field it refused: %v", err)
	}
	if strings.Contains(err.Error(), "noatime") {
		t.Errorf("the refusal echoes the value it was given: %v", err)
	}
}
