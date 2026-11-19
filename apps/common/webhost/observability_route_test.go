package webhost

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EPIC O (#898), the settings surface. The claim is the one the retention
// override flags once failed: a PATCH has to be re-resolved, so the next read
// reports what the file now says without a restart. It is driven through the
// real router over a real service opened from a real config file.

func writeObservabilityRouteConfig(t *testing.T, block string) string {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "poll_interval: 15m\n" +
		"state:\n  database: " + filepath.Join(dir, "state.db") + "\n" +
		block +
		"sources:\n  - id: production\n    backup_sets:\n" +
		"      - id: alpha\n        remote:\n          type: local\n" +
		"        remote_path: " + remote + "\n" +
		"        local_path: " + filepath.Join(dir, "local") + "\n" +
		"        include:\n          - \"*.dump\"\n        completion:\n          strategy: rename\n" +
		"        stale_after: 24h\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

type observabilityBody struct {
	Metrics struct {
		Enabled                   bool  `json:"enabled"`
		MinRebuildIntervalSeconds int64 `json:"min_rebuild_interval_seconds"`
		ScrapeToken               struct {
			Exists bool `json:"exists"`
		} `json:"scrape_token"`
	} `json:"metrics"`
	Logs struct {
		Level        string `json:"level"`
		LevelInForce string `json:"level_in_force"`
		LevelSource  string `json:"level_source"`
	} `json:"logs"`
	Alerts struct {
		Delivery string `json:"delivery"`
		Webhook  struct {
			Dialect                string `json:"dialect"`
			URLSet                 bool   `json:"url_set"`
			RefreshIntervalSeconds int64  `json:"refresh_interval_seconds"`
			ResolveTTLSeconds      int64  `json:"resolve_ttl_seconds"`
		} `json:"webhook"`
	} `json:"alerts"`
}

func readObservability(t *testing.T, router http.Handler) observabilityBody {
	t.Helper()
	code, body := call(t, router, http.MethodGet, "/api/v1/settings/observability", "")
	if code != http.StatusOK {
		t.Fatalf("GET /settings/observability = %d: %s", code, body)
	}
	var out observabilityBody
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding the block: %v\n%s", err, body)
	}

	return out
}

func TestObservabilitySettings_ADeploymentThatNeverWroteTheBlockReadsEverythingOffAndItsFileIsUntouched(t *testing.T) {
	path := writeObservabilityRouteConfig(t, "")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	router, closeRouter := gatedBoundaryRouter(t, path)
	defer closeRouter()

	got := readObservability(t, router)
	if got.Metrics.Enabled || got.Metrics.ScrapeToken.Exists || got.Metrics.MinRebuildIntervalSeconds != 15 {
		t.Errorf("metrics = %+v, want off, no credential and a 15s cache", got.Metrics)
	}
	if got.Logs.Level != "info" || got.Logs.LevelInForce != "info" || got.Logs.LevelSource != "config" {
		t.Errorf("logs = %+v, want the info floor from the config", got.Logs)
	}
	if got.Alerts.Delivery != "platform" || got.Alerts.Webhook.Dialect != "generic" || got.Alerts.Webhook.URLSet ||
		got.Alerts.Webhook.RefreshIntervalSeconds != 240 || got.Alerts.Webhook.ResolveTTLSeconds != 600 {
		t.Errorf("alerts = %+v, want platform delivery and the generic dialect with 4m and 10m", got.Alerts)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("a read wrote to a config.yaml with no observability key:\n%s", after)
	}
}

func TestObservabilitySettings_APatchIsReResolvedAndPersisted(t *testing.T) {
	path := writeObservabilityRouteConfig(t, "")
	router, closeRouter := gatedBoundaryRouter(t, path)
	defer closeRouter()

	code, body := call(t, router, http.MethodPatch, "/api/v1/settings/observability",
		`{"metrics_enabled":true,"metrics_min_rebuild_interval_seconds":30,"logs_level":"warn","alerts_delivery":"webhook","webhook_dialect":"alertmanager","webhook_refresh_interval_seconds":120,"webhook_resolve_ttl_seconds":300}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH /settings/observability = %d: %s", code, body)
	}

	got := readObservability(t, router)
	if !got.Metrics.Enabled || got.Metrics.MinRebuildIntervalSeconds != 30 || got.Logs.Level != "warn" ||
		got.Alerts.Delivery != "webhook" || got.Alerts.Webhook.Dialect != "alertmanager" ||
		got.Alerts.Webhook.RefreshIntervalSeconds != 120 || got.Alerts.Webhook.ResolveTTLSeconds != 300 {
		t.Errorf("the next read says %+v, want what the patch wrote, with no restart", got)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "observability:") || !strings.Contains(string(onDisk), "dialect: alertmanager") {
		t.Errorf("the patch was not persisted:\n%s", onDisk)
	}
}

func TestObservabilitySettings_ARefusalWritesNothing(t *testing.T) {
	path := writeObservabilityRouteConfig(t, "")
	before, _ := os.ReadFile(path)
	router, closeRouter := gatedBoundaryRouter(t, path)
	defer closeRouter()

	for name, patch := range map[string]string{
		"names nothing":             `{}`,
		"a log level nobody has":    `{"logs_level":"chatty"}`,
		"a cache below the floor":   `{"metrics_min_rebuild_interval_seconds":0}`,
		"a refresh past the expiry": `{"webhook_refresh_interval_seconds":900}`,
	} {
		code, body := call(t, router, http.MethodPatch, "/api/v1/settings/observability", patch)
		if code != http.StatusBadRequest || !strings.Contains(body, "INVALID_REQUEST") {
			t.Errorf("%s: PATCH = %d %s, want 400 INVALID_REQUEST", name, code, body)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("a refused patch changed config.yaml:\n%s", after)
	}
}
