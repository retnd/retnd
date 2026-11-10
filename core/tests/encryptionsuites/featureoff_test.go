package encryptionsuites_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/service"
)

// The feature-off row of #1319: a deployment that writes nothing about
// encryption (no retained_copy_encryption block, so no suite either) is left
// exactly as EPIC N left it by everything this epic adds that only reads.
// Listing the suites, reading the policy, reading health and listing keys
// do not create a key directory, do not touch the
// configuration file, do not mint a key, and answer with zero counts.
//
// What this does not do: diff a full cycle's outputs against the EPIC N
// canonical capture. That capture is T2.5's (#1320), and the matrix row stays
// PARTIAL here with that named as missing.
//
// Written by reading; not run.

func TestAFeatureOffDeploymentIsUntouchedByEverySuiteRead_1319(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote, local := filepath.Join(dir, "remote"), filepath.Join(dir, "local")
	for _, d := range []string{remote, local} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, "config.yaml")
	yaml := "poll_interval: 15m\nstate:\n  database: " + filepath.Join(dir, "state.db") + "\nsources:\n  - id: production\n    backup_sets:\n" +
		"      - id: postgres-primary\n        remote:\n          type: local\n        remote_path: " + remote + "\n" +
		"        local_path: " + local + "\n        include:\n          - \"*.dump\"\n        completion:\n" +
		"          strategy: rename\n        stale_after: 24h\nretention:\n  timezone: UTC\n  week_starts_on: monday\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, closeFn, err := service.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	t.Cleanup(func() { _ = closeFn() })

	suites, err := svc.ListEncryptionSuites(ctx)
	if err != nil {
		t.Fatalf("ListEncryptionSuites: %v", err)
	}
	if len(suites.Suites) != len(shipped) {
		t.Fatalf("the listing has %d suites, want %d: %+v", len(suites.Suites), len(shipped), suites)
	}
	for _, s := range suites.Suites {
		if s.ActiveCopies != 0 || s.Keys != 0 {
			t.Errorf("a deployment that encrypts nothing lists %s with %d copies and %d keys", s.ID, s.ActiveCopies, s.Keys)
		}
	}
	pol, err := svc.EncryptionPolicy(ctx)
	if err != nil {
		t.Fatalf("EncryptionPolicy: %v", err)
	}
	if pol.Mode != "off" || pol.SuiteConfigured != "default" {
		t.Errorf("the policy of a deployment that wrote nothing is %+v, want off with the suite at default", pol)
	}
	if _, err := svc.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if keys, err := svc.ListEncryptionKeys(ctx); err != nil || len(keys.Keys) != 0 {
		t.Fatalf("keys after only reads: %+v (err=%v)", keys, err)
	}

	if _, err := os.Stat(keycustody.DirBeside(cfg)); !os.IsNotExist(err) {
		t.Errorf("a read created the key directory (stat err=%v)", err)
	}
	after, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, []byte(yaml)) {
		t.Errorf("a read rewrote the configuration:\n%s", after)
	}
	if strings.Contains(string(after), "suite") {
		t.Error("the configuration names a suite though nobody wrote one")
	}

	// The control: the same comparison does see a write. Patching the policy
	// changes the file, so "unchanged" above is a finding.
	if _, err := svc.PatchEncryptionPolicy(ctx, apicontract.EncryptionPolicyPatch{Mode: "off"}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if again, _ := os.ReadFile(cfg); bytes.Equal(again, []byte(yaml)) {
		t.Fatal("control: a policy write left the file byte-identical, so the comparison above cannot see a write")
	}
}
