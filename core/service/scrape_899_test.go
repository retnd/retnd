package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// EPIC O, O1.1 (#899): the scrape credential's store and the exposition cache,
// against the real service. Each test names the mutation that has to turn it red.

func scrapeService(t *testing.T) *BackupService {
	t.Helper()
	svc := newTestService(t)
	svc.configPath = filepath.Join(t.TempDir(), "config.yaml")

	return svc
}

// Falsify: store the plaintext, or leave the directory looser than 0700.
func TestAScrapeCredentialIsStoredAsADigestInAPrivateFile(t *testing.T) {
	svc := scrapeService(t)
	minted, err := svc.MintScrapeToken(context.Background())
	if err != nil {
		t.Fatalf("MintScrapeToken: %v", err)
	}
	if !strings.HasPrefix(minted.Token, ScrapeTokenPrefix) || len(minted.Token) != len(ScrapeTokenPrefix)+43 {
		t.Fatalf("token = %q, want rsc_ and 43 characters of base64 (256 bits)", minted.Token)
	}
	path := filepath.Join(filepath.Dir(svc.configPath), scrapeStoreDirName, scrapeStoreFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), minted.Token) || strings.Contains(string(raw), minted.Token[len(ScrapeTokenPrefix):]) {
		t.Errorf("the store holds the plaintext: %s", raw)
	}
	if !strings.Contains(string(raw), sha256Hex(minted.Token)) || !strings.Contains(string(raw), minted.Token[:scrapePrefixLen]) {
		t.Errorf("the store must hold the SHA-256 digest and the eight-character prefix: %s", raw)
	}
	file, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Errorf("store is %v in %v, want 0600 in 0700", file.Mode().Perm(), dir.Mode().Perm())
	}
}

// Falsify: compare the presented string with something other than the digest,
// or answer true when no credential exists.
func TestOnlyTheCurrentScrapeCredentialVerifies(t *testing.T) {
	svc := scrapeService(t)
	if svc.VerifyScrapeToken("") || svc.VerifyScrapeToken(ScrapeTokenPrefix+strings.Repeat("A", 43)) {
		t.Fatal("a deployment that never minted a credential admitted somebody")
	}
	first, _ := svc.MintScrapeToken(context.Background())
	if !svc.VerifyScrapeToken(first.Token) || svc.VerifyScrapeToken(first.Token+"x") || svc.VerifyScrapeToken(first.Token[:len(first.Token)-1]) || svc.VerifyScrapeToken("") {
		t.Fatal("the credential did not verify, or a near miss did")
	}
	second, _ := svc.MintScrapeToken(context.Background())
	if svc.VerifyScrapeToken(first.Token) || !svc.VerifyScrapeToken(second.Token) {
		t.Fatal("minting again must replace the credential at once, with no overlap")
	}
	if got := svc.ScrapeTokenState(); !got.Exists || got.Prefix != second.Token[:scrapePrefixLen] {
		t.Errorf("state = %+v, want the second credential's prefix", got)
	}
}

func TestAServiceWithNoConfigFileCannotKeepACredential(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.MintScrapeToken(context.Background()); err == nil {
		t.Fatal("a service with no file behind it minted a credential it has nowhere to keep")
	}
}

// Falsify: rebuild per call, or never rebuild. The clock is the package's own
// seam, so the interval is exact rather than a sleep.
func TestTheHealthExpositionIsRebuiltOnlyWhenTheIntervalHasPassed(t *testing.T) {
	svc := scrapeService(t)
	start := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	clock := pinAgentClock(t, start)
	interval := svc.state.Load().inner.Config.Observability.Metrics.MinRebuildInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}

	first, err := svc.HealthExposition(context.Background(), "test", "none")
	if err != nil || !strings.Contains(first, "retnd_report_generated_timestamp_seconds") {
		t.Fatalf("first exposition: %v\n%s", err, first)
	}
	builtAt := svc.scrape.cachedAt
	*clock = start.Add(interval - time.Second)
	if _, err := svc.HealthExposition(context.Background(), "test", "none"); err != nil || !svc.scrape.cachedAt.Equal(builtAt) {
		t.Fatalf("a scrape inside the interval rebuilt the report (built %v, now %v)", builtAt, svc.scrape.cachedAt)
	}
	*clock = start.Add(interval + time.Second)
	if _, err := svc.HealthExposition(context.Background(), "test", "none"); err != nil || !svc.scrape.cachedAt.Equal(*clock) {
		t.Fatalf("a scrape after the interval did not rebuild the report (built %v, clock %v)", svc.scrape.cachedAt, *clock)
	}
}

func TestEveryRefusalReasonIsCountedAndAnythingElseIsDropped(t *testing.T) {
	svc := newTestService(t)
	svc.ObserveScrapeRejected("unauthenticated")
	svc.ObserveScrapeRejected("/home/alice/.ssh/id_ed25519")
	out := svc.ScrapeRejectedMetrics()
	if !strings.Contains(out, `{reason="unauthenticated"} 1`) || strings.Contains(out, "alice") {
		t.Errorf("counter:\n%s", out)
	}
}
