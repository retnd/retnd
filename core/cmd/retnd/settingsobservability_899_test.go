package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// EPIC O, O1.1 (#899): the terminal side of the scrape credential. The mint
// prints the plaintext once, and nothing afterwards (the read, the config
// file, the state directory) holds it.

func TestSettingsObservability_RotateScrapeTokenPrintsThePlaintextOnceAndNothingAfterwardsHoldsIt(t *testing.T) {
	configPath, _ := workflowFixture(t)

	var code int
	out := captureStdout(t, func() { code = run([]string{"settings", "observability", "rotate-scrape-token", "--config", configPath}) })
	if code != 0 {
		t.Fatalf("rotate-scrape-token = %d, want 0; stdout:\n%s", code, out)
	}
	token := regexp.MustCompile(`rsc_[A-Za-z0-9_-]{43}`).FindString(out)
	if token == "" {
		t.Fatalf("the mint printed no rsc_ token of 256 bits:\n%s", out)
	}

	read := captureStdout(t, func() { code = run([]string{"settings", "observability", "--config", configPath}) })
	if code != 0 || !strings.Contains(read, "minted") || !strings.Contains(read, token[:8]) {
		t.Errorf("the read after a mint says %q (exit %d), want that a credential exists and its eight-character prefix", read, code)
	}
	if strings.Contains(read, token) {
		t.Errorf("the read printed the whole token")
	}

	err := filepath.Walk(filepath.Dir(configPath), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(raw), token) {
			t.Errorf("%s holds the plaintext scrape token", path)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	again := captureStdout(t, func() { code = run([]string{"settings", "observability", "rotate-scrape-token", "--config", configPath}) })
	if code != 0 || strings.Contains(again, token) || regexp.MustCompile(`rsc_[A-Za-z0-9_-]{43}`).FindString(again) == "" {
		t.Errorf("a second mint must print a new credential and never the old one, got:\n%s", again)
	}
}
