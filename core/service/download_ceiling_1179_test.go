package service

import (
	"context"
	"testing"

	"github.com/retnd/retnd/core/internal/recovery/budget"
)

// #1185 review: "Download in this browser" for a selection offers at most as
// many downloads as the engine streams at once, because the one past the
// ceiling is refused when the browser follows it. The page can only hold to
// that if the settings say what the ceiling is, as the engine runs it.

func TestAssuranceSettings_1179_ReportTheDownloadCeiling(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	got, err := f.b.AssuranceSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxDownloadStreams != budget.DefaultMaxDownloadStreams {
		t.Errorf("MaxDownloadStreams = %d, want the engine's default %d", got.MaxDownloadStreams, budget.DefaultMaxDownloadStreams)
	}
}

func TestAssuranceSettings_1179_ReportThePlatformsDownloadCeiling(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	if err := f.b.SetRecoveryLimits(RecoveryLimits{MaxDownloadStreams: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := f.b.AssuranceSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxDownloadStreams != 2 {
		t.Errorf("MaxDownloadStreams = %d, want the platform's 2", got.MaxDownloadStreams)
	}
}
