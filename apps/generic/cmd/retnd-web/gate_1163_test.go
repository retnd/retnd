package main

import (
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/apps/common/auth/local"
	"github.com/retnd/retnd/apps/common/platform/capabilities"
	"github.com/retnd/retnd/apps/common/platform/profile"
)

// Issue #1163: retnd-web wires the destructive gate for the profile it was
// started with. The gate is read off the EngineConfig it builds, not off a
// running server, so a deleted or hard-coded line shows up here.

func engineConfigFor(t *testing.T, id profile.ID) (state string, hasStepUp, hasSetup bool) {
	t.Helper()
	p := id.Profile()
	var authSvc *local.Service
	if p.Gateway == nil {
		var err error
		authSvc, err = local.New(local.Config{StorePath: filepath.Join(t.TempDir(), "auth.json")})
		if err != nil {
			t.Fatalf("local.New: %v", err)
		}
	}
	if p.Gateway != nil {
		p.Gateway.TrustedPeers = []string{"127.0.0.0/8"}
	}
	var localAuth capabilities.Authenticator
	if authSvc != nil {
		localAuth = authSvc.Authenticator()
	}
	adapter, err := p.Adapter(profile.AdapterConfig{LocalAuth: localAuth})
	if err != nil {
		t.Fatalf("Adapter: %v", err)
	}
	cfg := newEngineConfig(p, adapter, authSvc, nil, false, nil, "test", "test")
	switch {
	case cfg.Gate == nil:
		state = "nil"
	case cfg.Gate.Passed():
		state = "open"
	default:
		state = "shut"
	}
	return state, cfg.StepUp != nil, cfg.Setup != nil
}

func TestEngineConfig1163_EveryShippedProfileIsWiredToItsGate(t *testing.T) {
	want := map[profile.ID]string{
		profile.Generic: "open", profile.TrueNAS: "open", profile.Unraid: "open",
		profile.OpenMediaVault: "open", profile.Proxmox: "open", profile.Synology: "open",
		profile.UGOS: "shut",
	}
	for _, id := range profile.IDs() {
		w, known := want[id]
		if !known {
			t.Errorf("profile %q is not classified in this test: a new profile has to be decided here before it ships", id)
			continue
		}
		state, stepUp, _ := engineConfigFor(t, id)
		if state != w {
			t.Errorf("profile %q: EngineConfig.Gate is %s, want %s (nil means the wiring is missing; an unwired gate is shut, so this is the line that makes a local-account profile useful)", id, state, w)
		}
		if wantStepUp := id != profile.UGOS; stepUp != wantStepUp {
			t.Errorf("profile %q: StepUp wired = %v, want %v (a restore asks for the password again, and a gateway profile has none to ask for)", id, stepUp, wantStepUp)
		}
	}
}
