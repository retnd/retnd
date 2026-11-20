package webhost

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EPIC O, O1.3 (FR-101, matrix row O-13): the webhook URL is a credential,
// and the settings surface says whether one is set and nothing else.

const routeSecretSegment = "XXroutewebhooksecretXX"

func webhookRouteConfig(t *testing.T) (path, urlFile string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	urlFile = filepath.Join(dir, "webhook-url")
	if err := os.WriteFile(urlFile, []byte("https://hooks.example.invalid/services/T0/B0/"+routeSecretSegment), 0o600); err != nil {
		t.Fatal(err)
	}
	block := "observability:\n  alerts:\n    delivery: webhook\n    webhook:\n      dialect: generic\n      url_ref:\n        file: " + urlFile + "\n"

	return writeObservabilityRouteConfig(t, block), urlFile
}

func TestObservabilitySettings_TheWebhookURLNeverLeavesAndItsPresenceStillShows(t *testing.T) {
	path, urlFile := webhookRouteConfig(t)
	router, closeRouter := gatedBoundaryRouter(t, path)
	defer closeRouter()

	code, body := call(t, router, http.MethodGet, "/api/v1/settings/observability", "")
	if code != http.StatusOK {
		t.Fatalf("GET = %d: %s", code, body)
	}
	if !readObservability(t, router).Alerts.Webhook.URLSet {
		t.Fatal("a configured webhook reads url_set=false, so an operator cannot tell it from an unconfigured one")
	}
	for _, leak := range []string{routeSecretSegment, "hooks.example.invalid", urlFile} {
		if strings.Contains(body, leak) {
			t.Fatalf("GET /settings/observability carries %q: %s", leak, body)
		}
	}

	// A patch answers with the same body, so it must be as clean.
	code, body = call(t, router, http.MethodPatch, "/api/v1/settings/observability", `{"webhook_dialect":"alertmanager"}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", code, body)
	}
	for _, leak := range []string{routeSecretSegment, "hooks.example.invalid", urlFile} {
		if strings.Contains(body, leak) {
			t.Fatalf("PATCH /settings/observability carries %q: %s", leak, body)
		}
	}

	// Control: a deployment with no webhook reads url_set=false.
	bare, closeBare := gatedBoundaryRouter(t, writeObservabilityRouteConfig(t, ""))
	defer closeBare()
	if readObservability(t, bare).Alerts.Webhook.URLSet {
		t.Fatal("an unconfigured webhook reads url_set=true")
	}
}

func TestObservabilitySettings_AWebhookDeliveryWithoutAReferenceIsRefused(t *testing.T) {
	path := writeObservabilityRouteConfig(t, "")
	router, closeRouter := gatedBoundaryRouter(t, path)
	defer closeRouter()

	code, body := call(t, router, http.MethodPatch, "/api/v1/settings/observability", `{"alerts_delivery":"webhook"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "url_ref") {
		t.Fatalf("PATCH = %d %s, want 400 naming url_ref: delivery through nothing is the silent failure this feature exists to end", code, body)
	}
}
