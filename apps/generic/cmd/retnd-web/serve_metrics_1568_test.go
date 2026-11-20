package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// #1568 at the shipped binary: `retnd-web serve` with the metrics block on
// answers GET /metrics, 401 without the scrape credential and the exposition
// with it. #899 tested the route on the router alone, and the engine's mux
// never mounted it, so this binary answered 404 to every scrape.
//
// Driven through serve_signal_test.go's child-process entry point, so the
// listener, the composition and the service are the ones the engine
// container runs.

func TestServe_1568_TheEngineAnswersMetrics(t *testing.T) {
	configPath := writeServeTestConfig(t)
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("observability:\n  metrics:\n    enabled: true\n    min_rebuild_interval: 1s\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Minted before the engine starts, the way `retnd settings observability
	// rotate-scrape-token` does with nothing serving: the credential is
	// written beside config.yaml and read when the engine opens.
	svc, closeSvc, err := service.Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	minted, err := svc.MintScrapeToken(context.Background())
	if cerr := closeSvc(); cerr != nil {
		t.Fatalf("closing the service: %v", cerr)
	}
	if err != nil {
		t.Fatalf("MintScrapeToken: %v", err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	cmd, out := startServeChild(t, configPath, t.TempDir(), serveChildListen+"="+addr)
	defer stopServeChild(t, cmd)
	base := "http://" + addr

	deadline := time.Now().Add(startupWithinTimeout)
	for {
		resp, err := http.Get(base + "/health/live")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine did not answer /health/live within %s\n%s", startupWithinTimeout, out.dump())
		}
		time.Sleep(200 * time.Millisecond)
	}

	get := func(authorization string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, base+"/metrics", nil)
		if err != nil {
			t.Fatal(err)
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /metrics: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, body := get(""); code != http.StatusUnauthorized {
		t.Errorf("GET /metrics on retnd-web serve with no credential = %d, want 401; body=%q", code, body)
	}
	code, body := get("Bearer " + minted.Token)
	if code != http.StatusOK {
		t.Fatalf("GET /metrics on retnd-web serve with the minted credential = %d, want 200; body=%q", code, body)
	}
	if !strings.Contains(body, "retnd_process_info{") {
		t.Errorf("the 200 carries no retnd_process_info sample, so it is not this product's exposition:\n%s", body)
	}
}
