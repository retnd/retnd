package app

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/mediumcheck"
)

// #1154: a local_volume candidate is proven the way a declared local_volume
// is, as a directory.
//
// PreflightMediumCandidate sent every candidate through the object-store
// check, so a directory was probed as a "bucket" with a storage class, the
// space and distinct-volume steps a directory can fail on never ran, and the
// by-id check an operator runs afterwards could disagree with the proof that
// let the write through.

func localVolumeCandidate(dir string) config.StorageMedium {
	return config.StorageMedium{ID: "usb_disk", Type: config.StorageMediumTypeLocalVolume, Path: dir}
}

func stepsOf(r mediumcheck.Report) map[mediumcheck.Step]bool {
	got := map[mediumcheck.Step]bool{}
	for _, c := range r.Checks {
		got[c.Step] = true
	}
	return got
}

func TestIssue1154_ACandidateLocalVolumeRunsTheLocalSteps(t *testing.T) {
	svc := New(preflightConfig(), nil, nil, nil)
	// Deliberately no MediumStore: a directory is reached by the local
	// check and not through the object boundary, so a deployment that
	// cannot reach a bucket can still prove a directory.

	report, err := svc.PreflightMediumCandidate(context.Background(), localVolumeCandidate(t.TempDir()))
	if err != nil {
		t.Fatalf("PreflightMediumCandidate: %v", err)
	}
	if !report.OK {
		t.Fatalf("a writable empty directory did not pass: %+v", report.Failures())
	}
	steps := stepsOf(report)
	for _, want := range []mediumcheck.Step{mediumcheck.StepSpace, mediumcheck.StepDistinctVolume} {
		if !steps[want] {
			t.Errorf("the report has no %q step, so the two ways a directory fails that a bucket cannot were never asked: %+v", want, report.Checks)
		}
	}
	for _, c := range report.Checks {
		if strings.Contains(c.Detail, "bucket") {
			t.Errorf("step %s speaks of a bucket for a directory: %q", c.Step, c.Detail)
		}
	}
}

// filesUnder lists every regular file beneath dir. The empty reserved
// directory a local check keeps (mediumcheck's localProbeDir, documented as
// staying on purpose) is not a file, and the probe FILE being removed is the
// claim StepDelete actually makes.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return files
}

func TestIssue1154_ACandidateLocalVolumeLeavesNoProbeFileBehind(t *testing.T) {
	dir := t.TempDir()
	svc := New(preflightConfig(), nil, nil, nil)
	report, err := svc.PreflightMediumCandidate(context.Background(), localVolumeCandidate(dir))
	if err != nil {
		t.Fatalf("PreflightMediumCandidate: %v", err)
	}
	if !report.OK {
		t.Fatalf("the proof did not pass, so a leftover could mean it stopped half way: %+v", report.Failures())
	}
	if files := filesUnder(t, dir); len(files) != 0 {
		t.Errorf("the proof left %v in the directory it tested", files)
	}
}

func TestIssue1154_ACandidateLocalVolumeFailsWhenItIsTheSameDiskAsAnotherDestination(t *testing.T) {
	dir := t.TempDir()
	declared := config.StorageMedium{ID: "first_disk", Type: config.StorageMediumTypeLocalVolume, Path: dir}
	svc := New(preflightConfig(declared), nil, nil, nil)

	// The same directory under a second id: one disk wearing two names.
	candidate := localVolumeCandidate(dir)
	report, err := svc.PreflightMediumCandidate(context.Background(), candidate)
	if err != nil {
		t.Fatalf("PreflightMediumCandidate: %v", err)
	}
	if report.OK {
		t.Error("a candidate on the same filesystem as a declared local destination passed; a retention tier would count it as a second copy")
	}
}

func TestIssue1154_ReconfiguringADeclaredLocalVolumeIsNotItsOwnDuplicate(t *testing.T) {
	dir := t.TempDir()
	declared := config.StorageMedium{ID: "usb_disk", Type: config.StorageMediumTypeLocalVolume, Path: dir}
	svc := New(preflightConfig(declared), nil, nil, nil)

	report, err := svc.PreflightMediumCandidate(context.Background(), localVolumeCandidate(dir))
	if err != nil {
		t.Fatalf("PreflightMediumCandidate: %v", err)
	}
	if !report.OK {
		t.Errorf("a destination reconfigured onto its own directory was refused as a duplicate of itself: %+v", report.Failures())
	}
}
