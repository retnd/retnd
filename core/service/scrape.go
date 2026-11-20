package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/metrics"
)

// The Prometheus scrape endpoint's half of core/service (EPIC O, O1.1, #899;
// FR-99): the credential, the cached health exposition and the refusal
// counter, handed to apps/common/webhost as plain values because that package
// cannot import core/internal (AgentAccess is the precedent for the seam).
//
// # The credential is its own
//
// It has the agent token's SHAPE (a distinct prefix, 256 bits from crypto/rand,
// only a SHA-256 and an eight-character prefix kept, shown once) and none of
// its authority: it is not a scope on rat_, it is not in the agent tables and
// it is not audited per call. The hash is SHA-256 and not argon2id because the
// token is 256 random bits, so a slow KDF adds nothing against guessing and, on
// a route that answers an unauthenticated caller, is an amplifier the caller
// picks the rate of. ADR 0034 made the same call for agent tokens.
//
// At rest it is one mode-0600 file in a mode-0700 directory beside config.yaml,
// the place mediumcredentials.go keeps its own secrets and for the same
// reason: not under any backup root. A file and not a table, so
// core/migrations/ stays where it is.

const (
	// ScrapeTokenPrefix is what every scrape credential starts with, distinct
	// from AgentTokenPrefix so neither can be mistaken for the other.
	ScrapeTokenPrefix = "rsc_"

	scrapeStoreDirName  = "scrape_credential"
	scrapeStoreFileName = "scrape-token.json"
	// scrapePrefixLen is the length of the stored, display-only prefix.
	scrapePrefixLen = 8
)

// scrapeStoreRecord is the file's whole content: a digest, a display prefix and
// a time. There is no field a plaintext could be written into.
type scrapeStoreRecord struct {
	SHA256    string    `json:"sha256"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
}

// scrapeRuntime is the endpoint's in-process state: the refusal counter, the
// loaded credential and the cached health exposition. Its zero value is ready.
type scrapeRuntime struct {
	counters metrics.Scrape

	mu sync.Mutex
	// credential is the store as last read, and credentialStat is what the
	// file looked like then, so a verification costs one stat and not a read
	// and an unauthenticated caller cannot make this a disk amplifier.
	credential     scrapeStoreRecord
	credentialStat os.FileInfo
	loaded         bool

	// cacheMu guards the cached exposition, and only that. It is not mu: a
	// slow journal read behind a rebuild must not hold up verifying a token or
	// reading the credential's state for GET /settings/observability (#899
	// review). Concurrent scrapes still wait on cacheMu, so they share one
	// rebuild instead of each starting their own.
	cacheMu sync.Mutex
	// cached is the last rendered health exposition and when it was built.
	cached   string
	cachedAt time.Time
	cacheErr error
}

// MintedScrapeToken is the one answer that carries the credential itself.
type MintedScrapeToken struct {
	Token string
	State ScrapeTokenState
}

func (b *BackupService) scrapeStorePath() (string, error) {
	if b.configPath == "" {
		return "", ErrConfigNotFileBacked
	}

	return filepath.Join(filepath.Dir(b.configPath), scrapeStoreDirName, scrapeStoreFileName), nil
}

// MintScrapeToken mints the scrape credential, replacing any earlier one at
// once: there is no overlap window, so the operator updates Prometheus's
// credentials_file and reloads it. The plaintext is in the return value and
// nowhere else; the store holds a SHA-256 and an eight-character prefix.
func (b *BackupService) MintScrapeToken(_ context.Context) (MintedScrapeToken, error) {
	path, err := b.scrapeStorePath()
	if err != nil {
		return MintedScrapeToken{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return MintedScrapeToken{}, fmt.Errorf("service: reading randomness for a scrape credential: %w", err)
	}
	token := ScrapeTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	record := scrapeStoreRecord{SHA256: hex.EncodeToString(digest[:]), Prefix: token[:scrapePrefixLen], CreatedAt: now().Truncate(time.Second)}
	encoded, err := json.Marshal(record)
	if err != nil {
		return MintedScrapeToken{}, fmt.Errorf("service: encoding the scrape credential store: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return MintedScrapeToken{}, fmt.Errorf("service: creating the scrape credential directory: %w", err)
	}
	// MkdirAll leaves an existing directory as it found it, and a custody
	// directory that is looser than 0700 is not custody.
	if err := os.Chmod(dir, 0o700); err != nil {
		return MintedScrapeToken{}, fmt.Errorf("service: securing the scrape credential directory: %w", err)
	}

	b.scrape.mu.Lock()
	defer b.scrape.mu.Unlock()
	if err := writeConfigBytesAtomically(path, encoded); err != nil {
		return MintedScrapeToken{}, fmt.Errorf("service: persisting the scrape credential: %w", err)
	}
	b.scrape.loaded = false

	return MintedScrapeToken{Token: token, State: ScrapeTokenState{Exists: true, Prefix: record.Prefix, CreatedAt: record.CreatedAt}}, nil
}

// loadScrapeCredentialLocked reads the store when it has changed since the
// last read. The caller holds scrape.mu. A missing file is "no credential",
// and so is one that does not parse: a store nobody can read admits nobody.
func (b *BackupService) loadScrapeCredentialLocked() (scrapeStoreRecord, bool) {
	path, err := b.scrapeStorePath()
	if err != nil {
		return scrapeStoreRecord{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		b.scrape.loaded, b.scrape.credential, b.scrape.credentialStat = false, scrapeStoreRecord{}, nil

		return scrapeStoreRecord{}, false
	}
	if b.scrape.loaded && b.scrape.credentialStat != nil && os.SameFile(info, b.scrape.credentialStat) &&
		info.ModTime().Equal(b.scrape.credentialStat.ModTime()) && info.Size() == b.scrape.credentialStat.Size() {
		return b.scrape.credential, true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return scrapeStoreRecord{}, false
	}
	var record scrapeStoreRecord
	if err := json.Unmarshal(raw, &record); err != nil || len(record.SHA256) != hex.EncodedLen(sha256.Size) {
		return scrapeStoreRecord{}, false
	}
	b.scrape.credential, b.scrape.credentialStat, b.scrape.loaded = record, info, true

	return record, true
}

// ScrapeTokenState says whether a credential exists and when it was minted:
// the most a read of the settings may report.
func (b *BackupService) ScrapeTokenState() ScrapeTokenState {
	b.scrape.mu.Lock()
	defer b.scrape.mu.Unlock()
	record, ok := b.loadScrapeCredentialLocked()
	if !ok {
		return ScrapeTokenState{}
	}

	return ScrapeTokenState{Exists: true, Prefix: record.Prefix, CreatedAt: record.CreatedAt}
}

// VerifyScrapeToken reports whether presented is the current scrape
// credential. The comparison is constant time over SHA-256 digests, and it
// runs against a zero digest when no credential has been minted, so the
// absence of one costs the same as a wrong one.
func (b *BackupService) VerifyScrapeToken(presented string) bool {
	b.scrape.mu.Lock()
	record, ok := b.loadScrapeCredentialLocked()
	b.scrape.mu.Unlock()

	want := make([]byte, sha256.Size)
	if ok {
		if decoded, err := hex.DecodeString(record.SHA256); err == nil && len(decoded) == sha256.Size {
			want = decoded
		} else {
			ok = false
		}
	}
	got := sha256.Sum256([]byte(presented))

	return subtle.ConstantTimeCompare(got[:], want) == 1 && ok
}

// ScrapeEnabled reports whether observability.metrics.enabled is on. It reads
// the current configuration on every call, so a PATCH takes effect on the next
// scrape with no restart.
func (b *BackupService) ScrapeEnabled() bool {
	return b.state.Load().inner.Config.Observability.Metrics.Enabled
}

// ObserveScrapeRejected counts one refused scrape. reason is one of the three
// words of the closed vocabulary; anything else is dropped.
func (b *BackupService) ObserveScrapeRejected(reason string) {
	b.scrape.counters.ObserveRejected(reason)
}

// ScrapeRejectedMetrics renders retnd_scrape_rejected_total.
func (b *BackupService) ScrapeRejectedMetrics() string {
	return b.scrape.counters.RenderRejected()
}

// ErrScrapeUnavailable is returned when a health exposition could not be built.
var ErrScrapeUnavailable = errors.New("service: the health exposition is unavailable")

// HealthExposition is metrics.Render over a health report, rebuilt at most once
// per observability.metrics.min_rebuild_interval, so N scrapers cost one
// journal read per interval. Its age is the existing
// retnd_report_generated_timestamp_seconds sample; no new timestamp is
// invented for it.
//
// A failed build is cached for the interval too, so a journal that cannot be
// read is not hammered once per scraper. The interval is read on every call, so
// shortening it takes effect at the next scrape.
func (b *BackupService) HealthExposition(ctx context.Context, binaryVersion, commit string) (string, error) {
	interval := b.state.Load().inner.Config.Observability.Metrics.MinRebuildInterval
	if interval <= 0 {
		// A Config built by hand and never validated. Every loaded one has
		// been resolved, and a zero here would mean a rebuild per scrape.
		interval = config.DefaultMetricsMinRebuildInterval
	}

	b.scrape.cacheMu.Lock()
	defer b.scrape.cacheMu.Unlock()
	if !b.scrape.cachedAt.IsZero() && now().Sub(b.scrape.cachedAt) < interval {
		return b.scrape.cached, b.scrape.cacheErr
	}

	report, err := b.state.Load().inner.BuildHealthReport(ctx, app.BuildVersionInfo(binaryVersion, commit))
	if err != nil {
		failure := fmt.Errorf("%w: %v", ErrScrapeUnavailable, err)
		if ctx.Err() == nil {
			// A scraper that hung up is not a journal that failed, so only a
			// real failure is remembered for the interval.
			b.scrape.cached, b.scrape.cacheErr, b.scrape.cachedAt = "", failure, now()
		}

		return "", failure
	}
	b.scrape.cachedAt = now()
	b.scrape.cached, b.scrape.cacheErr = metrics.Render(report), nil

	return b.scrape.cached, nil
}

// ScrapeContentType is the Content-Type of the exposition and nothing else.
const ScrapeContentType = metrics.ContentType
