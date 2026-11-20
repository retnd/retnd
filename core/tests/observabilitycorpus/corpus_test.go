// Package observabilitycorpus_test is EPIC O's FR-104 corpus (#903, matrix row
// O-02, and the log half of O-03): one deployment loaded with five known
// secrets runs a cycle, is scraped and delivers an alert to a receiver that
// records the raw request, and none of the five appears on any of the three
// surfaces EPIC O adds.
//
// The three surfaces are three rows, each its own test, so a leak on one is
// exactly one red and not a composite boolean that cannot say which (the shape
// core/tests/encryptedretention keeps for key material):
//
//   - O-02/metrics: the exposition /metrics serves, built the way
//     apps/common/webhost's handler builds it (the cached health half, the
//     workflow half when an engine ran, the refusal counter). core cannot import
//     the web host, so the HTTP wrapper is not here; the bytes are.
//   - O-02/log: every line the engine's one logger wrote during the run, the
//     startup line, the cycle, the scrape-token mint and the alert's delivery
//     included, at debug.
//   - O-02/webhook: the request the receiver got, headers and body. The URL the
//     receiver was reached at is scanned for the other four secrets; it carries
//     the webhook credential by construction, because that is where a
//     Slack-style receiver keeps it. "The URL as logged" is the log row's job.
//
// Every secret is generated here at run time and appears in no fixture, commit
// message or log. What the five are, and the honest boundary on two of them:
//
//   - the repository passphrase, read from a 0600 file by a real kopia set that
//     the cycle snapshots;
//   - the scrape token, minted through the service, so its plaintext exists once
//     and then only as a digest, which is O-03's "persisted nowhere" read from
//     the log side;
//   - the webhook credential, in the path of the URL a url_ref file holds,
//     resolved at every delivery;
//   - an S3 secret key in a medium's credentials file, on a medium a retention
//     tier places onto. Whether a first cycle reaches a placement is retention's
//     decision, so this row proves no surface carries it, not that a cycle read
//     it;
//   - an SMTP password. No core process holds one: the SMTP password belongs to
//     the web host's account-recovery mailer (apps/common/auth/local), which
//     none of the three surfaces runs in. It is put where an engine could see it
//     at all, in its environment and in a file beside the config, and that is
//     the most this corpus can say about it.
//
// The guarantee is about secrets this product holds, not about text an operator
// typed into a hook script (docs/observability.md says so).
package observabilitycorpus_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/metrics"
	"github.com/retnd/retnd/core/service"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p)
}

func (s *syncBuffer) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return bytes.Clone(s.b.Bytes())
}

// renderings is every way a secret could plausibly reach a text surface.
func renderings(m string) []string {
	raw := []byte(m)

	return []string{
		m,
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		hex.EncodeToString(raw),
	}
}

// secret is one of the five, by name, so a red says which one leaked.
type secret struct{ name, value string }

func found(surface []byte, secrets []secret) []string {
	var out []string
	for _, s := range secrets {
		for _, r := range renderings(s.value) {
			if bytes.Contains(surface, []byte(r)) {
				out = append(out, s.name)

				break
			}
		}
	}

	return out
}

func random(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return prefix + hex.EncodeToString(b)
}

// request is what the receiver recorded of one delivery.
type request struct {
	url    string
	header []byte
	body   []byte
}

type receiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []request
	hits int
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		body, _ := io.ReadAll(q.Body)
		var header bytes.Buffer
		_ = q.Header.Write(&header)
		r.mu.Lock()
		r.got = append(r.got, request{url: q.URL.String(), header: header.Bytes(), body: body})
		r.hits++
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.srv.Close)

	return r
}

func (r *receiver) requests() []request {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]request(nil), r.got...)
}

// world is one deployment loaded with the five secrets, after the run.
type world struct {
	secrets     []secret
	webhookCred string
	exposition  []byte
	log         []byte
	deliveries  []request
}

func (w *world) except(name string) []secret {
	var out []secret
	for _, s := range w.secrets {
		if s.name != name {
			out = append(out, s)
		}
	}

	return out
}

func writePrivate(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	// Debug, from both ends: the environment wins over the configured floor,
	// so a stray LOG_LEVEL in the runner cannot quietly drop half the lines.
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("RETND_DEBUG", "")
	t.Setenv("RM_DEBUG", "")

	passphrase := random(t, "corpus-passphrase-")
	s3Key := random(t, "corpusS3Secret")
	smtp := random(t, "corpus-smtp-")
	webhookCred := random(t, "corpushook")

	dir := t.TempDir()
	remote, local, tree := filepath.Join(dir, "remote"), filepath.Join(dir, "local"), filepath.Join(dir, "tree")
	for _, d := range []string{remote, local, tree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(remote, "backup.dump"), []byte("observability corpus payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "file.txt"), []byte("observability corpus tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(dir, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	// Chmod as well: a TMPDIR whose directory carries setgid hands it to
	// every directory made under it, and the custody check refuses a
	// reference directory that is anything but 0700.
	if err := os.Chmod(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	passphraseFile := writePrivate(t, secrets, "repo.passphrase", passphrase)
	s3File := writePrivate(t, secrets, "s3-credentials", "[default]\naws_access_key_id = AKIACORPUS000000000\naws_secret_access_key = "+s3Key+"\n")
	writePrivate(t, secrets, "smtp-password", smtp)
	t.Setenv("SMTP_PASSWORD", smtp)

	r := newReceiver(t)
	urlFile := filepath.Join(secrets, "webhook-url")
	// The URL file does not exist at first, so the first pass is the
	// unreadable-reference path (an alert_destination error) and the second is
	// the heal and the delivery: both reach the log.

	cfg := filepath.Join(dir, "config.yaml")
	yaml := "poll_interval: 15m\n" +
		"state:\n  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n  - id: production\n    backup_sets:\n" +
		"      - id: postgres-primary\n        remote:\n          type: local\n        remote_path: " + remote + "\n" +
		"        local_path: " + local + "\n        include:\n          - \"*.dump\"\n        completion:\n          strategy: rename\n" +
		"        stale_after: 1ms\n" +
		"      - id: uploads-tree\n        uuid: 6f1d2b7a-1c4e-4f8b-9a2d-3e5c7b9d1f00\n        engine: kopia\n" +
		"        repository_domain: production\n        source_consistency: live_best_effort\n        verification_level: structural\n" +
		"        remote:\n          type: local\n        remote_path: " + tree + "\n        stale_after: 1ms\n" +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n  tiers:\n" +
		"    - name: daily\n      granularity: day\n      keep: 7\n" +
		"    - name: monthly\n      granularity: month\n      keep: 12\n      medium: offsite_s3\n" +
		"storage_mediums:\n  - id: offsite_s3\n    type: s3\n    region: us-east-1\n    endpoint: http://127.0.0.1:9\n    bucket: corpus\n" +
		"    credentials:\n      file: " + s3File + "\n" +
		"incremental_engine:\n  enabled: true\n" +
		"repository_domains:\n  - id: production\n    description: corpus\n    isolation: shared\n    passphrase:\n      file: " + passphraseFile + "\n" +
		"alerts:\n  enabled: true\n  repeated_failure_threshold: 3\n" +
		"observability:\n  metrics:\n    enabled: true\n    min_rebuild_interval: 1s\n  logs:\n    level: debug\n" +
		"  alerts:\n    delivery: webhook\n    webhook:\n      dialect: generic\n      allow_insecure_http: true\n" +
		"      url_ref:\n        file: " + urlFile + "\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	log := &syncBuffer{}
	svc, closeFn, err := service.OpenWith(ctx, cfg, service.OpenOptions{LogTo: log})
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	t.Cleanup(func() { _ = closeFn() })
	dp := svc.BackupDataPlane()
	dp.Logger.Startup(ctx, "corpus", "corpus", runtime.Version())

	minted, err := svc.MintScrapeToken(ctx)
	if err != nil {
		t.Fatalf("minting the scrape token: %v", err)
	}
	if !svc.VerifyScrapeToken(minted.Token) {
		t.Fatal("the minted scrape token does not verify, so the scrape below is not the real path")
	}
	svc.ObserveScrapeRejected(metrics.ScrapeRejectedUnauthenticated)

	if !svc.EnableAlerts(nil) {
		t.Fatal("webhook delivery did not enable alerting")
	}
	dp.RunCycle(ctx)
	time.Sleep(20 * time.Millisecond)
	dp.AlertTick(ctx)
	writePrivate(t, secrets, "webhook-url", r.srv.URL+"/hooks/"+webhookCred)
	dp.AlertTick(ctx)

	health, err := svc.HealthExposition(ctx, "corpus", "corpus")
	if err != nil {
		t.Fatalf("building the health exposition: %v", err)
	}
	expo := health
	if wf := svc.WorkflowMetrics(); wf != "" {
		expo += "\n" + wf
	}
	expo += "\n" + svc.ScrapeRejectedMetrics()

	return &world{
		secrets: []secret{
			{"the repository passphrase", passphrase},
			{"the S3 secret key", s3Key},
			{"the SMTP password", smtp},
			{"the scrape token", minted.Token},
			{"the webhook credential", webhookCred},
		},
		webhookCred: webhookCred,
		exposition:  []byte(expo),
		log:         log.bytes(),
		deliveries:  r.requests(),
	}
}

// row is one surface: the bytes the real producers made, a marker proving the
// collector reached something real, and which secrets its detector looks for.
type row struct {
	name    string
	marker  string
	collect func(w *world) []byte
	look    func(w *world) []secret
}

func all(w *world) []secret { return w.secrets }

var rows = []row{
	{
		name:    "O-02/metrics",
		marker:  "retnd_scrape_rejected_total{reason=\"unauthenticated\"} 1",
		collect: func(w *world) []byte { return w.exposition },
		look:    all,
	},
	{
		name:    "O-02/log",
		marker:  `"event":"startup"`,
		collect: func(w *world) []byte { return w.log },
		look:    all,
	},
	{
		name:   "O-02/webhook",
		marker: `"kind":"STALE_BACKUP"`,
		collect: func(w *world) []byte {
			var b []byte
			for _, d := range w.deliveries {
				b = append(b, d.header...)
				b = append(b, d.body...)
			}

			return b
		},
		look: all,
	},
	{
		name:   "O-02/webhook URL",
		marker: "/hooks/",
		collect: func(w *world) []byte {
			var b []byte
			for _, d := range w.deliveries {
				b = append(b, d.url...)
				b = append(b, '\n')
			}

			return b
		},
		// The receiver's own URL carries the webhook credential by
		// construction; the other four may not ride along with it.
		look: func(w *world) []secret { return w.except("the webhook credential") },
	},
}

var (
	once   sync.Once
	shared *world
)

// corpusWorld runs the deployment once for the package: every row and control
// reads the bytes that one run made, so a control is about the same surface its
// row scanned. The service and its directory go away with the first test; the
// world holds only what the surfaces carried.
func corpusWorld(t *testing.T) *world {
	t.Helper()
	once.Do(func() { shared = newWorld(t) })
	if shared == nil {
		t.Fatal("the corpus deployment did not come up (see the first test's failure)")
	}
	if len(shared.deliveries) == 0 {
		t.Fatalf("the receiver got no request, so the webhook surface is empty and would pass anything. The log:\n%s", shared.log)
	}

	return shared
}

// TestNoSecretOnAnyObservabilitySurface_903 is FR-104: each surface its own
// subtest, each naming the secret that reached it.
func TestNoSecretOnAnyObservabilitySurface_903(t *testing.T) {
	w := corpusWorld(t)
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := r.collect(w)
			if !bytes.Contains(got, []byte(r.marker)) {
				t.Fatalf("%s did not carry %q, so the collector is not reading the real surface:\n%.1500s", r.name, r.marker, got)
			}
			if leaked := found(got, r.look(w)); len(leaked) > 0 {
				t.Fatalf("%s carries %s", r.name, strings.Join(leaked, " and "))
			}
		})
	}
}

// The control, one row at a time, in every rendering the detector knows: each
// secret planted into that row's real bytes is found by that row. Red if a
// detector stops reading a rendering, or a collector stops returning the bytes
// it scans.
func TestNoSecretOnAnyObservabilitySurface_903_WouldCatchOne(t *testing.T) {
	w := corpusWorld(t)
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			real := r.collect(w)
			for _, s := range r.look(w) {
				for _, rendering := range renderings(s.value) {
					planted := append(bytes.Clone(real), []byte("\nplanted "+rendering+"\n")...)
					if got := found(planted, r.look(w)); len(got) == 0 {
						t.Fatalf("%s planted on %s as %.12q... was not found", s.name, r.name, rendering)
					}
				}
			}
		})
	}
}

// A secret planted on one surface turns that surface's row red and no other:
// every other row, read over its own real bytes in the same world, stays quiet.
// Red if two rows share a collector, which is how a composite "no secret
// anywhere" boolean hides a per-surface regression.
func TestAPlantingTurnsExactlyItsOwnRowRed_903_WouldCatchOne(t *testing.T) {
	w := corpusWorld(t)
	plant := w.secrets[0].value
	for i, planted := range rows {
		var red []string
		for j, r := range rows {
			b := r.collect(w)
			if i == j {
				b = append(bytes.Clone(b), []byte("\n"+plant+"\n")...)
			}
			if len(found(b, r.look(w))) > 0 {
				red = append(red, r.name)
			}
		}
		sort.Strings(red)
		if len(red) != 1 || red[0] != planted.name {
			t.Errorf("a secret planted on %s turned %v red, want exactly [%s]", planted.name, red, planted.name)
		}
	}
}

// The corpus has to be about a deployment that really did the three things,
// or every row is clean because nothing happened: the cycle ran, the alert was
// delivered once the URL could be read, and the log says the reference was
// unreadable before that.
func TestTheCorpusRunIsARealRun_903(t *testing.T) {
	w := corpusWorld(t)
	for _, want := range []string{`"event":"startup"`, `"event":"alert_destination"`, `"event":"alert"`} {
		if !bytes.Contains(w.log, []byte(want)) {
			t.Errorf("the log has no %s line, so the run did not exercise what the log row claims:\n%.2000s", want, w.log)
		}
	}
	if !bytes.Contains(w.exposition, []byte("retnd_backup_set_state{")) {
		t.Errorf("the exposition carries no backup set state, so no health report was rendered:\n%.1500s", w.exposition)
	}
	if len(w.deliveries) == 0 {
		t.Error("no delivery reached the receiver")
	}
	// What the run did, for the record a matrix row cites: the distinct
	// events the log carried and how many deliveries the receiver saw.
	events := map[string]int{}
	for _, line := range bytes.Split(w.log, []byte("\n")) {
		if m := eventField.FindSubmatch(line); m != nil {
			events[string(m[1])]++
		}
	}
	names := make([]string, 0, len(events))
	for e, n := range events {
		names = append(names, e+"="+strconv.Itoa(n))
	}
	sort.Strings(names)
	t.Logf("%d log lines, events %s; %d webhook deliveries; exposition %d bytes", bytes.Count(w.log, []byte("\n")), strings.Join(names, " "), len(w.deliveries), len(w.exposition))
}

var eventField = regexp.MustCompile(`"event":"([a-z_]+)"`)
