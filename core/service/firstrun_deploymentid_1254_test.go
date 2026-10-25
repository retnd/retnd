package service

import (
	"path/filepath"
	"testing"
)

// TestFirstRunDeploymentID_IsTheIdentityTheAnnouncementMinted_1254 is the
// service half of a first-run engine naming its deployment (#1254): the
// first-run surface reads the identity its announcement minted beside the
// journal, and a configured backend over that journal later reads the same
// one, so a terminal that confirmed the deployment before the first run is
// still talking to it after.
func TestFirstRunDeploymentID_IsTheIdentityTheAnnouncementMinted_1254(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	dbPath := filepath.Join(dir, "state", "state.db")

	firstRun, err := NewFirstRun(FirstRunDefaults{ConfigPath: configPath, StateDatabase: dbPath})
	if err != nil {
		t.Fatalf("NewFirstRun: %v", err)
	}
	if got := firstRun.DeploymentID(); got != "" {
		t.Fatalf("DeploymentID = %q before anything announced, want empty: a first-run surface must never mint one itself", got)
	}

	serving, err := AnnounceServingFirstRun(configPath, dbPath)
	if err != nil {
		t.Fatalf("AnnounceServingFirstRun: %v", err)
	}
	t.Cleanup(func() { _ = serving.Release() })

	want, err := DeploymentIdentity(dbPath)
	if err != nil || want == "" {
		t.Fatalf("the first-run announcement minted no identity beside %s (id %q, err %v)", dbPath, want, err)
	}
	if got := firstRun.DeploymentID(); got != want {
		t.Errorf("DeploymentID = %q, want the announced identity %q", got, want)
	}
}
