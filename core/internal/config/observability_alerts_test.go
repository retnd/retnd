package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EPIC O, O1.3 (FR-101, matrix row O-12): what Load refuses about a webhook
// target, and that a private target is not refused.

const webhookSecretSegment = "XXwebhooksecretXX"

func webhookConfig(t *testing.T, alerts string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "poll_interval: 15m\nstate:\n  database: /var/lib/retnd/state.db\n" + alerts +
		"sources:\n  - id: production\n    backup_sets:\n      - id: pg\n        remote:\n          type: local\n        remote_path: /srv/x\n        local_path: /backups/pg\n        completion:\n          strategy: stable\n          stable_for: 10m\n        stale_after: 30h\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func webhookBlock(ref, allowInsecure string) string {
	return "observability:\n  alerts:\n    delivery: webhook\n    webhook:\n      dialect: generic\n" + allowInsecure + ref
}

func TestValidate_WebhookTargetIsJudgedWithoutBeingEchoed(t *testing.T) {
	for _, tc := range []struct {
		name, url, allow string
		wantRefused      bool
	}{
		{"file scheme", "file:///etc/" + webhookSecretSegment, "", true},
		{"userinfo", "https://user:" + webhookSecretSegment + "@hooks.example.invalid/x", "", true},
		{"plain http without the acknowledgement", "http://hooks.example.invalid/" + webhookSecretSegment, "", true},
		{"plain http acknowledged", "http://hooks.example.invalid/" + webhookSecretSegment, "      allow_insecure_http: true\n", false},
		{"private target acknowledged", "http://alertmanager:9093/api/v2/alerts", "      allow_insecure_http: true\n", false},
		{"https", "https://hooks.example.invalid/" + webhookSecretSegment, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "s")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "url")
			if err := os.WriteFile(file, []byte(tc.url), 0o600); err != nil {
				t.Fatal(err)
			}
			err := webhookConfig(t, webhookBlock("      url_ref:\n        file: "+file+"\n", tc.allow)).Validate()
			if (err != nil) != tc.wantRefused {
				t.Fatalf("Validate() = %v, want refused=%v", err, tc.wantRefused)
			}
			if err != nil && (strings.Contains(err.Error(), webhookSecretSegment) || strings.Contains(err.Error(), "hooks.example")) {
				t.Fatalf("the refusal echoes the URL: %v", err)
			}
		})
	}
}

func TestValidate_WebhookDeliveryNeedsAReference(t *testing.T) {
	err := webhookConfig(t, "observability:\n  alerts:\n    delivery: webhook\n").Validate()
	if err == nil || !strings.Contains(err.Error(), "url_ref") {
		t.Fatalf("Validate() = %v, want a refusal naming url_ref", err)
	}
}

// A reference Validate cannot read must not make the configuration
// unloadable: the secret file moving is not a reason to refuse a reload.
func TestValidate_AnUnreadableWebhookReferenceIsNotARefusal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-provisioned-yet")
	if err := webhookConfig(t, webhookBlock("      url_ref:\n        file: "+missing+"\n", "")).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want it accepted", err)
	}
}

func TestValidate_WebhookRefreshMustBeBelowResolveTTL(t *testing.T) {
	cfg := webhookConfig(t, "observability:\n  alerts:\n    delivery: webhook\n    webhook:\n      dialect: alertmanager\n      refresh_interval: 10m\n      resolve_ttl: 10m\n      allow_insecure_http: true\n      url_ref:\n        file: /nonexistent/url\n")
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "refresh_interval") {
		t.Fatalf("Validate() = %v, want a refusal naming refresh_interval", err)
	}
}

// #1518 review: a refresh has to land, and its delivery attempt finish,
// before the receiver expires the alert. One delivery attempt is bounded at
// 30s (internal/alert's deliveryTimeout), so resolve_ttl must leave at least
// that much room above refresh_interval. 240s against 250s used to load, and
// a slow receiver then answered after Alertmanager had already resolved it.
func TestValidate_WebhookExpiryLeavesRoomForOneDeliveryAttempt(t *testing.T) {
	block := func(refresh, ttl string) string {
		return "observability:\n  alerts:\n    delivery: webhook\n    webhook:\n      dialect: alertmanager\n      refresh_interval: " + refresh + "\n      resolve_ttl: " + ttl + "\n      allow_insecure_http: true\n      url_ref:\n        file: /nonexistent/url\n"
	}
	for _, tc := range []struct{ refresh, ttl string }{{"240s", "250s"}, {"9m31s", "10m"}} {
		err := webhookConfig(t, block(tc.refresh, tc.ttl)).Validate()
		if err == nil || !strings.Contains(err.Error(), "refresh_interval") || !strings.Contains(err.Error(), "30s") {
			t.Errorf("refresh %s against resolve_ttl %s: Validate() = %v, want a refusal naming refresh_interval and the 30s delivery bound", tc.refresh, tc.ttl, err)
		}
	}
	for _, tc := range []struct{ refresh, ttl string }{{"9m30s", "10m"}, {"4m", "10m"}, {"400ms", "31s"}} {
		if err := webhookConfig(t, block(tc.refresh, tc.ttl)).Validate(); err != nil {
			t.Errorf("refresh %s against resolve_ttl %s was refused: %v", tc.refresh, tc.ttl, err)
		}
	}
}
