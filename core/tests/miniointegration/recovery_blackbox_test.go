package miniointegration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
	"github.com/retnd/retnd/core/tests/machines"
)

// This file is #986's black-box half against a REAL S3 endpoint (EPIC Q,
// #1073): the built `retnd` binary, a journal that says the only copy of an
// artifact is in a bucket, and MinIO holding the bytes.
//
// # Why it is in this package and why it builds the binary
//
// The rig the browser-and-terminal equivalence suites run on has no S3
// endpoint, so "recover from an S3 medium" and "an archive-class copy starts
// no provider job" cannot be proved there. Everything in cmd/retnd's own
// tests calls run() in-process against a local-directory medium. What none
// of that can show is the process an operator actually types at: its exit
// code, what it prints, and what it did to the network. So these cases run
// the binary as a subprocess, and watch the endpoint through a recording
// proxy that sits between the binary and MinIO.
//
// # The recording proxy
//
// The medium in the binary's config points at the proxy, not at MinIO.
// Every request the binary makes lands in a list the test reads afterwards,
// so "zero provider GET or restore requests" is a count taken off the wire
// rather than a claim about what the code intended. A request is classified
// by what it asks for, not by who sent it:
//
//   - an object read:  GET on /<bucket>/<key>, the call that bills egress;
//   - a restore:       POST on /<bucket>/<key>?restore, the call that starts
//     a provider job (and, on an archive class, a billed retrieval);
//   - a listing:       GET on /<bucket>?list-type=..., metadata only, and the
//     way the product asks a provider whether a restore has finished
//     (internal/transport/rclone.RestoreStatus). It reads no object.
//
// # How "archive class" is modelled
//
// The product decides a copy is archive-class from the storage_class of the
// medium in the configuration (service.recoveryCopies asks
// app.MediumFor for the class, archive.IsArchive decides). MinIO has no
// archive tier, so the object itself is an ordinary one; the configuration
// is what says it lives on DEEP_ARCHIVE, exactly as it would for a real
// bucket whose objects are in that class. MinIO also has no RestoreObject
// API, so the proxy answers the restore request itself with the 202 a real
// provider sends, after recording it. That is the only response the proxy
// ever forges, and the test fails if it is ever asked to for anything but a
// restore.

// recordedRequest is one request the binary made to the "provider".
type recordedRequest struct {
	Method string
	Path   string
	Query  string
}

func (r recordedRequest) String() string {
	if r.Query == "" {
		return r.Method + " " + r.Path
	}
	return r.Method + " " + r.Path + "?" + r.Query
}

// recordingProxy forwards to a real MinIO and remembers what it forwarded.
type recordingProxy struct {
	URL    string
	bucket string

	// reportArchive makes the "provider" say the objects are on DEEP_ARCHIVE
	// (see the file comment).
	reportArchive bool

	mu       sync.Mutex
	requests []recordedRequest
	forged   []recordedRequest
}

func startRecordingProxy(t *testing.T, upstream, bucket string) *recordingProxy {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parsing the MinIO endpoint %q: %v", upstream, err)
	}
	p := &recordingProxy{bucket: bucket}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ModifyResponse = func(resp *http.Response) error {
		if !p.reportArchive {
			return nil
		}
		return p.presentAsArchive(resp)
	}
	// The Host header is deliberately NOT rewritten: the binary signed the
	// request for the proxy's address, and MinIO checks the signature
	// against the Host it receives.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		p.mu.Lock()
		p.requests = append(p.requests, rec)
		p.mu.Unlock()
		if isRestore(rec) {
			// MinIO has no archive tier and so no RestoreObject. A real
			// provider accepts a restore of an archived object with 202.
			p.mu.Lock()
			p.forged = append(p.forged, rec)
			p.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			return
		}
		rp.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the recording proxy: %v", err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	p.URL = "http://" + ln.Addr().String()
	return p
}

// presentAsArchive makes MinIO's answers read like a bucket whose objects are
// in DEEP_ARCHIVE: listings carry the class on each entry and a HEAD carries
// the storage-class header. rclone's restore command skips objects its
// listing does not show as archived, so without this no provider restore
// would ever be asked for. Bodies are untouched otherwise.
func (p *recordingProxy) presentAsArchive(resp *http.Response) error {
	req := resp.Request
	switch {
	case req.Method == http.MethodHead && resp.StatusCode == http.StatusOK:
		resp.Header.Set("X-Amz-Storage-Class", "DEEP_ARCHIVE")
	case req.Method == http.MethodGet && p.isListing(recordedRequest{Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery}):
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		body = bytes.ReplaceAll(body, []byte("<StorageClass>STANDARD</StorageClass>"), []byte("<StorageClass>DEEP_ARCHIVE</StorageClass>"))
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	return nil
}

func isRestore(r recordedRequest) bool {
	if r.Method != http.MethodPost {
		return false
	}
	for _, part := range strings.Split(r.Query, "&") {
		if part == "restore" || strings.HasPrefix(part, "restore=") {
			return true
		}
	}
	return false
}

func (p *recordingProxy) snapshot() []recordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedRequest(nil), p.requests...)
}

func (p *recordingProxy) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = nil
	p.forged = nil
}

// isObjectRead is a GET of an object body (or a ranged part of one).
func (p *recordingProxy) isObjectRead(r recordedRequest) bool {
	if r.Method != http.MethodGet {
		return false
	}
	rest := strings.TrimPrefix(r.Path, "/"+p.bucket)
	return rest != r.Path && strings.Trim(rest, "/") != ""
}

func (p *recordingProxy) isListing(r recordedRequest) bool {
	return r.Method == http.MethodGet && strings.Trim(strings.TrimPrefix(r.Path, "/"+p.bucket), "/") == ""
}

// tally splits what was seen into the three kinds the assertions care about.
type tally struct {
	objectReads []recordedRequest
	restores    []recordedRequest
	listings    []recordedRequest
	other       []recordedRequest // HEADs and anything unclassified
	mutations   []recordedRequest // PUT, DELETE, and POSTs that are not a restore
}

func (p *recordingProxy) tally() tally {
	var t tally
	for _, r := range p.snapshot() {
		switch {
		case isRestore(r):
			t.restores = append(t.restores, r)
		case p.isObjectRead(r):
			t.objectReads = append(t.objectReads, r)
		case p.isListing(r):
			t.listings = append(t.listings, r)
		case r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPost:
			t.mutations = append(t.mutations, r)
		default:
			t.other = append(t.other, r)
		}
	}
	return t
}

func (t tally) String() string {
	return fmt.Sprintf("%d object read(s) %v, %d restore(s) %v, %d listing(s), %d mutation(s) %v, other %v",
		len(t.objectReads), t.objectReads, len(t.restores), t.restores, len(t.listings), len(t.mutations), t.mutations, t.other)
}

// --- the binary -------------------------------------------------------------

var (
	retndBuildOnce sync.Once
	retndBinary    string
	retndBuildErr  error
)

// builtRetnd builds core/cmd/retnd once for the whole package run.
//
// The binary lives in os.TempDir rather than a t.TempDir, because t.TempDir
// is removed when the first test that asked for it finishes and the second
// test still needs it.
func builtRetnd(t *testing.T) string {
	t.Helper()
	retndBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "retnd-blackbox-")
		if err != nil {
			retndBuildErr = err
			return
		}
		out := filepath.Join(dir, "retnd")
		cmd := exec.Command("go", "build", "-o", out, "github.com/retnd/retnd/core/cmd/retnd")
		if combined, err := cmd.CombinedOutput(); err != nil {
			retndBuildErr = fmt.Errorf("go build ./cmd/retnd: %v\n%s", err, combined)
			return
		}
		retndBinary = out
	})
	if retndBuildErr != nil {
		t.Fatalf("building the retnd binary: %v", retndBuildErr)
	}
	return retndBinary
}

type runResult struct {
	Exit   int
	Stdout string
	Stderr string
}

func runRetnd(t *testing.T, bin string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Nothing ambient may steer the binary: no AWS profile or key, no API
	// URL that would route the verb to some other engine.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s %v: %v", bin, args, err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		res := runResult{Stdout: stdout.String(), Stderr: stderr.String()}
		if err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("running %v: %v", args, err)
			}
			res.Exit = ee.ExitCode()
		}
		return res
	case <-time.After(3 * time.Minute):
		_ = cmd.Process.Kill()
		t.Fatalf("retnd %v did not finish in 3 minutes\nstdout: %s\nstderr: %s", args, stdout.String(), stderr.String())
	}
	return runResult{}
}

// --- the deployment ---------------------------------------------------------

const (
	bbSource  = "production"
	bbSet     = "postgres-primary"
	bbMedium  = "offsite"
	bbArtName = "2026-10-11T03-00-00Z.dump"
)

type blackboxWorld struct {
	configPath string
	artifact   model.ArtifactID
	artifactID string
	content    []byte
	digest     string
	key        string
	proxy      *recordingProxy
	dir        string
}

// newBlackboxWorld writes a deployment whose journal says the artifact's only
// copy is on the medium, uploads the bytes to a real bucket, and points the
// binary's configuration at the recording proxy in front of that bucket.
//
// storageClass is the class the CONFIGURATION declares for the medium; ""
// leaves it at the default (standard).
func newBlackboxWorld(t *testing.T, fixture *machines.Medium, storageClass string) *blackboxWorld {
	t.Helper()
	ctx := context.Background()

	w := &blackboxWorld{dir: t.TempDir()}
	w.content = bytes.Repeat([]byte("the only copy of this backup is in the bucket. "), 2048)
	w.digest = sha256HexOf(w.content)
	w.artifact = model.ArtifactID{Set: model.BackupSetID{Source: bbSource, Set: bbSet}, Name: bbArtName}
	w.artifactID = w.artifact.String()

	bucket := fixture.NewBucket(t)
	key, err := transport.MediumKey(bucket.Prefix, w.artifact)
	if err != nil {
		t.Fatalf("computing the object key: %v", err)
	}
	w.key = key

	// Upload straight to MinIO (not through the proxy), so the proxy's list
	// starts empty and everything it records is something the binary did.
	src := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(src, w.content, 0o600); err != nil {
		t.Fatalf("writing the upload source: %v", err)
	}
	if _, err := rclone.New().UploadFromLocal(ctx, bucket, src, key, transport.UploadOptions{}); err != nil {
		t.Fatalf("putting the artifact in the bucket: %v", err)
	}

	w.proxy = startRecordingProxy(t, fixture.Endpoint, bucket.Bucket)
	w.proxy.reportArchive = storageClass != ""

	dbPath := filepath.Join(w.dir, "state.db")
	journal, err := state.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening the journal: %v", err)
	}
	seedArtifactOnMedium(t, journal, w.artifact, key, w.content, bbMedium, time.Now().UTC())
	if err := journal.Close(); err != nil {
		t.Fatalf("closing the seeded journal: %v", err)
	}

	remoteDir := filepath.Join(w.dir, "remote")
	localDir := filepath.Join(w.dir, "local")
	for _, d := range []string{remoteDir, localDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", d, err)
		}
	}
	class := ""
	if storageClass != "" {
		class = "    storage_class: " + storageClass + "\n"
	}
	cfg := "poll_interval: 15m\n" +
		"state:\n  database: " + dbPath + "\n" +
		"sources:\n" +
		"  - id: " + bbSource + "\n" +
		"    backup_sets:\n" +
		"      - id: " + bbSet + "\n" +
		"        remote:\n          type: local\n" +
		"        remote_path: " + remoteDir + "\n" +
		"        local_path: " + localDir + "\n" +
		"        completion:\n          strategy: stable\n          stable_for: 10m\n" +
		"        stale_after: 30h\n" +
		"storage_mediums:\n" +
		"  - id: " + bbMedium + "\n" +
		"    type: s3\n" +
		"    region: " + fixture.Region + "\n" +
		"    endpoint: " + w.proxy.URL + "\n" +
		"    bucket: " + bucket.Bucket + "\n" +
		class +
		"    credentials:\n      file: " + fixture.CredentialsFile + "\n" +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n"
	w.configPath = filepath.Join(w.dir, "config.yaml")
	if err := os.WriteFile(w.configPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	return w
}

// --- the cases --------------------------------------------------------------

// TestBlackBox_RecoverFromAnS3MediumDeliversTheRecordedDigest is #986's S3
// recovery case. The journal's only active copy is in a bucket; `retnd
// recover` must read it back, check ALL of it against the SHA-256 recorded
// at ingestion, and only then place it.
func TestBlackBox_RecoverFromAnS3MediumDeliversTheRecordedDigest(t *testing.T) {
	fixture := machines.Start(t).Medium(t)
	bin := builtRetnd(t)
	w := newBlackboxWorld(t, fixture, "")

	res := runRetnd(t, bin, "recover", "--config", w.configPath, w.artifactID, "--to", "restored/backup.dump", "--acknowledge-cost", "--json")
	if res.Exit != 0 {
		t.Fatalf("recover exited %d, want 0\nstdout: %s\nstderr: %s", res.Exit, res.Stdout, res.Stderr)
	}

	var out struct {
		Copy   string `json:"copy"`
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("recover --json printed something that is not JSON: %q (%v)", res.Stdout, err)
	}
	if out.Copy != bbMedium {
		t.Errorf("recovered from %q, want the medium %q (it holds the only copy)", out.Copy, bbMedium)
	}
	if out.SHA256 != w.digest {
		t.Errorf("the verb reports sha256 %s, the journal recorded %s", out.SHA256, w.digest)
	}
	got, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("the recovered file is not at the path the verb printed (%q): %v", out.Path, err)
	}
	// Digest the bytes on disk ourselves: the verb's own claim is not
	// evidence about the file.
	if sha256HexOf(got) != w.digest {
		t.Errorf("the recovered bytes hash to %s, the recorded digest is %s", sha256HexOf(got), w.digest)
	}
	if out.Bytes != int64(len(w.content)) || int64(len(got)) != int64(len(w.content)) {
		t.Errorf("recovered %d bytes (file holds %d), want %d", out.Bytes, len(got), len(w.content))
	}

	// The bytes really crossed the wire from the bucket: at least one object
	// read, and nothing was written to or deleted from the provider.
	tl := w.proxy.tally()
	if len(tl.objectReads) == 0 {
		t.Errorf("the endpoint saw no object read, so the bytes did not come from it: %s", tl)
	}
	if len(tl.restores) != 0 || len(tl.mutations) != 0 {
		t.Errorf("a standard-class recovery changed something at the provider: %s", tl)
	}

	assertRecoveryStageEvents(t, res.Stderr, w.artifactID)
}

// assertRecoveryStageEvents reads the recovery's own activity-feed lines off
// the verb's structured log: recovery_start, then the stages in order, then
// a recovery_end that reports success. The three share one operation id,
// which is what lets `--follow` and a browser follow the same recovery.
//
// The durable `retnd activity` table is deliberately not used here: it
// records journal transitions, and a one-shot CLI process does not persist
// its live feed to it (only an engine does), so asking it would assert
// nothing about this recovery.
func assertRecoveryStageEvents(t *testing.T, stderr, artifact string) {
	t.Helper()
	var (
		opID    string
		stages  []string
		started bool
		ended   bool
	)
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "{") {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		id, _ := ev["op_id"].(string)
		switch ev["event"] {
		case "recovery_start":
			started, opID = true, id
			if ev["artifact"] != artifact {
				t.Errorf("recovery_start names artifact %v, want %s", ev["artifact"], artifact)
			}
		case "recovery_stage":
			if id != opID {
				t.Errorf("a stage line carries op_id %q, want the start line's %q", id, opID)
			}
			stages = append(stages, fmt.Sprint(ev["stage"]))
		case "recovery_end":
			ended = true
			if id != opID {
				t.Errorf("the end line carries op_id %q, want %q", id, opID)
			}
			if ev["result"] != "success" {
				t.Errorf("recovery_end result = %v, want success: %v", ev["result"], ev)
			}
		}
	}
	if !started || !ended {
		t.Fatalf("the feed lacks the recovery's start (%v) or end (%v):\n%s", started, ended, stderr)
	}
	want := []string{"resolving_copy", "reading", "verifying", "complete"}
	next := 0
	for _, s := range stages {
		if next < len(want) && s == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Errorf("stages seen %v, want %v in that order", stages, want)
	}
	if len(stages) > 0 && stages[len(stages)-1] != "complete" {
		t.Errorf("the last stage is %q, want complete", stages[len(stages)-1])
	}
}

const (
	exitRecoverCostNotAcknowledged = 10
	exitRecoverThawStarted         = 31
)

// TestBlackBox_ArchiveClassIsRefusedForCostAndStartsNoProviderJob is #986 AC5:
// "the archive refusal starts no provider job", measured on the wire.
//
// The only copy is on a medium the configuration declares DEEP_ARCHIVE (the
// proxy also reports that class in listings and HEADs, as the provider
// would). Without the acknowledgement the verb must exit 10 having made no
// object read, no restore request and no write: only the metadata calls
// that ask the provider whether a restore has already finished (a bucket
// listing and HEADs). With --acknowledge-cost it must start exactly one
// provider restore (one POST <key>?restore), read no object body (nothing
// is readable until the restore finishes), exit 31 and name the operation.
func TestBlackBox_ArchiveClassIsRefusedForCostAndStartsNoProviderJob(t *testing.T) {
	fixture := machines.Start(t).Medium(t)
	bin := builtRetnd(t)
	w := newBlackboxWorld(t, fixture, "DEEP_ARCHIVE")
	args := []string{"recover", "--config", w.configPath, w.artifactID, "--to", "restored/backup.dump", "--json"}

	// --- refused ---
	refused := runRetnd(t, bin, args...)
	if refused.Exit != exitRecoverCostNotAcknowledged {
		t.Fatalf("recover without the acknowledgement exited %d, want %d\nstdout: %s\nstderr: %s",
			refused.Exit, exitRecoverCostNotAcknowledged, refused.Stdout, refused.Stderr)
	}
	if !strings.Contains(refused.Stdout, "RECOVERY_COST_NOT_ACKNOWLEDGED") || !strings.Contains(refused.Stdout, "nothing was started") {
		t.Errorf("the refusal does not say the cost was not acknowledged and nothing started: %s", refused.Stdout)
	}
	before := w.proxy.tally()
	if len(before.objectReads) != 0 || len(before.restores) != 0 || len(before.mutations) != 0 {
		t.Fatalf("the refusal reached the provider: %s", before)
	}
	if len(before.listings) == 0 {
		t.Errorf("the refusal made no status probe at all, so it decided the copy is archive-class without asking the provider: %s", before)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "recovery")); err == nil {
		t.Errorf("a refused recovery created the staging root")
	}

	// --- acknowledged ---
	w.proxy.reset()
	ackArgs := append(append([]string(nil), args...), "--acknowledge-cost")
	acked := runRetnd(t, bin, ackArgs...)
	if acked.Exit != exitRecoverThawStarted {
		t.Fatalf("recover with the acknowledgement exited %d, want %d (a restore started, nothing recovered yet)\nstdout: %s\nstderr: %s",
			acked.Exit, exitRecoverThawStarted, acked.Stdout, acked.Stderr)
	}
	var out struct {
		Restore struct {
			OperationID string `json:"operation_id"`
		} `json:"restore"`
	}
	if err := json.Unmarshal([]byte(acked.Stdout), &out); err != nil || out.Restore.OperationID == "" {
		t.Errorf("the acknowledged run names no restore operation: %q (%v)", acked.Stdout, err)
	}
	after := w.proxy.tally()
	wantRestore := "POST /" + w.proxy.bucket + "/" + w.key + "?restore="
	if len(after.restores) != 1 || after.restores[0].String() != wantRestore {
		t.Errorf("want exactly one %q, saw %s", wantRestore, after)
	}
	if len(after.objectReads) != 0 || len(after.mutations) != 0 {
		t.Errorf("the acknowledged run read an object body or wrote something: %s", after)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "restored")); err == nil {
		t.Errorf("bytes were placed although only a restore was started")
	}
}
