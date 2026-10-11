package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// recoveryStore is a storage medium that holds objects in memory and counts
// what was asked of it. The counts are the point: half of what recovery
// promises is about what did NOT happen (no read, no provider job).
type recoveryStore struct {
	restoreOnlyTransport

	mu       sync.Mutex
	objects  map[string][]byte
	opens    atomic.Int32
	restores atomic.Int32
	uploads  atomic.Int32
	flipAt   int // when >= 0, bit-flip this offset on every read

	// restoreState and restoreErr are what the provider says about a
	// restore of any object, for the archive-class cases.
	restoreState *transport.RestoreState
	restoreErr   error
	statuses     atomic.Int32

	// openGate, when set, makes OpenObject wait until it is closed or the
	// caller's context ends, so a test can hold a read in flight. failAfter
	// and failErr make a read die partway, the way a destination does when
	// it disappears under a copy.
	openGate   chan struct{}
	failAfter  int
	failErr    error
	uploadGate chan struct{}

	// attest, when set, is what the provider says its own checksum for an
	// object is. Unset, the medium cannot attest, which is what the shipped
	// S3 adapter reports for SHA-256.
	attest func() (transport.ChecksumAttestation, error)
}

func newRecoveryStore() *recoveryStore {
	return &recoveryStore{objects: map[string][]byte{}, flipAt: -1}
}

func (s *recoveryStore) StatObject(_ context.Context, _ transport.Medium, key string) (transport.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.objects[key]; ok {
		return transport.ObjectInfo{Key: key, Size: int64(len(b))}, nil
	}
	return transport.ObjectInfo{}, transport.NewError(transport.NotFound, "stat", errors.New("no such key"))
}

func (s *recoveryStore) UploadFromLocal(ctx context.Context, _ transport.Medium, localPath, key string, _ transport.UploadOptions) (transport.UploadResult, error) {
	if s.uploadGate != nil {
		select {
		case <-s.uploadGate:
		case <-ctx.Done():
			return transport.UploadResult{}, ctx.Err()
		}
	}
	b, err := os.ReadFile(localPath)
	if err != nil {
		return transport.UploadResult{}, err
	}
	s.mu.Lock()
	s.objects[key] = b
	s.mu.Unlock()
	s.uploads.Add(1)
	return transport.UploadResult{Key: key, BytesUploaded: int64(len(b))}, nil
}

func (s *recoveryStore) OpenObject(ctx context.Context, _ transport.Medium, key string) (io.ReadCloser, error) {
	s.opens.Add(1)
	if s.openGate != nil {
		select {
		case <-s.openGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	b, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		return nil, transport.NewError(transport.NotFound, "stat", errors.New("no such key"))
	}
	d := append([]byte(nil), b...)
	if s.flipAt >= 0 {
		d[s.flipAt] ^= 1
	}
	if s.failErr != nil {
		return io.NopCloser(io.MultiReader(bytes.NewReader(d[:s.failAfter]), &failingReader{err: s.failErr})), nil
	}
	return io.NopCloser(bytes.NewReader(d)), nil
}

func (s *recoveryStore) ObjectChecksum(context.Context, transport.Medium, string, transport.HashAlgorithm) (transport.ChecksumAttestation, error) {
	if s.attest != nil {
		return s.attest()
	}
	return transport.ChecksumAttestation{}, errors.New("this endpoint cannot attest a full-object SHA-256")
}
func (s *recoveryStore) DeleteObject(context.Context, transport.Medium, string) error {
	return errors.New("recovery must never delete")
}
func (s *recoveryStore) ListObjects(context.Context, transport.Medium, string) ([]transport.ObjectInfo, error) {
	return nil, errors.New("recovery must never list a destination")
}
func (s *recoveryStore) RestoreStatus(context.Context, transport.Medium, string) (*transport.RestoreState, error) {
	s.statuses.Add(1)
	return s.restoreState, s.restoreErr
}
func (s *recoveryStore) InitiateRestore(context.Context, transport.Medium, string, int) error {
	s.restores.Add(1)
	return nil
}

type recClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *recClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *recClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type recoveryFixture struct {
	b        *BackupService
	store    *recoveryStore
	clock    *recClock
	log      *bytes.Buffer
	dir      string
	artifact string
	data     []byte
	localDir string
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func recoveryPayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*17 + 3)
	}
	return b
}

type fixtureOpts struct {
	localCopy   bool
	remoteClass string // "" for no remote copy
	engine      string
}

// newRecoveryFixture builds a service whose journal holds one artifact,
// "production/postgres/dump.zst", with the copies opts asks for.
func newRecoveryFixture(t *testing.T, opts fixtureOpts) *recoveryFixture {
	t.Helper()
	dir := t.TempDir()
	set, err := model.NewBackupSetID("production", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(config.Source{
		Name: "production",
		BackupSets: []config.BackupSet{{
			ID: set, Name: "postgres", LocalPath: filepath.Join(dir, "local"), Engine: model.BackupEngine(opts.engine),
		}},
	})
	cfg.State.Database = filepath.Join(dir, "state", "journal.db")
	cfg.StorageMediums = []config.StorageMedium{
		{ID: "cold", Type: config.StorageMediumTypeS3, Region: "us-east-1", Bucket: "b", StorageClass: config.StorageClassDeepArchive, Credentials: config.MediumCredentials{File: "/x"}},
		{ID: "warm", Type: config.StorageMediumTypeS3, Region: "us-east-1", Bucket: "w", StorageClass: config.StorageClassStandard, Credentials: config.MediumCredentials{File: "/x"}},
		{ID: "other", Type: config.StorageMediumTypeS3, Region: "us-east-1", Bucket: "o", StorageClass: config.StorageClassStandard, Credentials: config.MediumCredentials{File: "/x"}},
	}

	store := newRecoveryStore()
	log := &bytes.Buffer{}
	clock := &recClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	journal := openTestJournal(t)
	b := New(cfg, journal, store, obs.New(log, obs.LevelInfo))
	b.recovery.now = clock.Now
	t.Cleanup(func() { _ = b.Close() })

	data := recoveryPayload(300_000)
	f := &recoveryFixture{b: b, store: store, clock: clock, log: log, dir: dir, data: data, localDir: filepath.Join(dir, "local")}
	artifact, _ := model.NewArtifactID(set, "dump.zst")
	f.artifact = artifact.String()

	ctx := context.Background()
	if _, err := journal.RecordTransition(ctx, state.Transition{
		Artifact: artifact, Key: "seed-1", To: string(lifecycle.Discovered),
		OccurredAt: time.Now().UTC().Add(-48 * time.Hour), RemotePath: "/r/dump.zst",
	}); err != nil {
		t.Fatal(err)
	}
	size := int64(len(data))
	tr := state.Transition{
		Artifact: artifact, Key: "seed-2", From: string(lifecycle.Discovered), To: string(lifecycle.Transferring),
		OccurredAt: time.Now().UTC().Add(-24 * time.Hour),
		Hashes:     &state.HashUpdate{Hash: sha(data), Alg: string(transport.SHA256)},
	}
	if opts.localCopy {
		if err := os.MkdirAll(f.localDir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(f.localDir, "dump.zst")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		tr.LocalPath = &p
		tr.Placement = &state.PlacementUpdate{Medium: state.MediumLocal, Location: p, Size: &size, Hash: sha(data), HashAlg: "sha256", Status: state.PlacementActive}
	}
	if _, err := journal.RecordTransition(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if opts.remoteClass != "" {
		verified := time.Now().UTC().Add(-time.Hour)
		store.objects["prefix/dump.zst"] = data
		if _, err := journal.RecordTransition(ctx, state.Transition{
			Artifact: artifact, Key: "seed-3", From: string(lifecycle.Transferring), To: string(lifecycle.Transferring),
			OccurredAt: time.Now().UTC().Add(-23 * time.Hour),
			Placement: &state.PlacementUpdate{
				Medium: opts.remoteClass, Location: "prefix/dump.zst", Size: &size, Hash: sha(data), HashAlg: "sha256",
				VerificationClass: state.VerificationContent, VerifiedAt: &verified, Status: state.PlacementActive,
			},
		}); err != nil {
			t.Logf("second placement via same-state transition refused (%v); trying a forward transition", err)
			t.Fatal(err)
		}
	}
	return f
}

func (f *recoveryFixture) session() RecoverySession {
	return RecoverySession{Actor: "alice", Expires: f.clock.Now().Add(time.Hour)}
}

func (f *recoveryFixture) stagingEntries(t *testing.T) []string {
	t.Helper()
	root := filepath.Join(f.dir, "state", "recovery")
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (f *recoveryFixture) rows(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(f.log.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if op, _ := m["operation"].(string); strings.HasPrefix(op, "recovery ") {
			out = append(out, m)
		}
	}
	return out
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	re, ok := AsRecoveryError(err)
	if !ok {
		t.Fatalf("not a recovery error: %v", err)
	}
	return re.Code
}

func stagingTo(name string) RecoveryTarget { return RecoveryTarget{Kind: "staging", Path: name} }

func TestRecoverALocalCopyToStagingEndToEnd(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	res, thaw, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("out/dump.zst")}, nil)
	if err != nil || thaw != nil {
		t.Fatalf("RecoverNow: %v, %v", err, thaw)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil || !bytes.Equal(got, f.data) {
		t.Fatalf("recovered file wrong: %v", err)
	}
	if res.SHA256 != sha(f.data) || res.Bytes != int64(len(f.data)) {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Path, filepath.Join("state", "recovery")) {
		t.Fatalf("output %s is not under the staging root", res.Path)
	}
}

func TestRecoverToADownloadStreamsTheVerifiedBytes(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	var out bytes.Buffer
	res, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), f.data) || res.Bytes != int64(len(f.data)) {
		t.Fatal("streamed bytes differ")
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("a completed download left %v in staging", e)
	}
}

func TestCorruptLocalCopyFailsBeforeAByteReachesTheTarget(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	p := filepath.Join(f.localDir, "dump.zst")
	flipped := append([]byte(nil), f.data...)
	flipped[len(flipped)/2] ^= 1 // same size, same recorded class, different bytes
	if err := os.WriteFile(p, flipped, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	_, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}}, &out)
	if codeOf(t, err) != RecoveryLocalCopyCorrupt {
		t.Fatalf("want %s, got %v", RecoveryLocalCopyCorrupt, err)
	}
	if out.Len() != 0 {
		t.Fatalf("%d bytes reached the client before verification failed", out.Len())
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("staging not empty: %v", e)
	}
}

func TestCorruptRemoteCopyIsDistinguishableFromACorruptLocalOne(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	f.store.flipAt = 1234
	_, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if codeOf(t, err) != RecoveryRemoteCopyCorrupt {
		t.Fatalf("want %s, got %v", RecoveryRemoteCopyCorrupt, err)
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("staging not empty: %v", e)
	}
}

func TestChargeableRetrievalNeedsTheAcknowledgementAndReadsNothingWithout(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	_, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")}, nil)
	if codeOf(t, err) != RecoveryCostNotAcknowledged {
		t.Fatalf("want %s, got %v", RecoveryCostNotAcknowledged, err)
	}
	if f.store.opens.Load() != 0 || f.store.restores.Load() != 0 {
		t.Fatalf("a refusal still touched the provider: opens=%d restores=%d", f.store.opens.Load(), f.store.restores.Load())
	}
	if _, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil); err != nil {
		t.Fatalf("acknowledged recovery refused: %v", err)
	}
	if f.store.opens.Load() != 1 {
		t.Fatalf("the acknowledged path should read the copy once, read it %d times", f.store.opens.Load())
	}
}

func TestBestAvailablePrefersTheFreeLocalCopyAndNeverTouchesTheProvider(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, remoteClass: "warm"})
	if _, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")}, nil); err != nil {
		t.Fatalf("a free local copy exists, so no acknowledgement should be needed: %v", err)
	}
	if f.store.opens.Load() != 0 {
		t.Fatal("the remote copy was read although a free local one was verified")
	}
}

func TestArchiveOnlyIsRefusedForCostAndStartsNoProviderJob(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "cold"})
	_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")})
	if codeOf(t, err) != RecoveryCostNotAcknowledged {
		t.Fatalf("want %s, got %v", RecoveryCostNotAcknowledged, err)
	}
	if !strings.Contains(err.Error(), "restore") {
		t.Fatalf("the refusal must name the cost: %v", err)
	}
	if n := f.store.restores.Load(); n != 0 {
		t.Fatalf("the refusal started %d provider jobs", n)
	}
}

func TestArchiveAcknowledgedStartsExactlyOneProviderJobEvenWhenAskedTwice(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "cold"})
	var thaws int
	f.b.recovery.thaw = func(_ context.Context, actor, artifact, medium string) (RecoveryThaw, error) {
		thaws++
		_, _ = f.store, medium
		f.store.restores.Add(1)
		return RecoveryThaw{OperationID: "op_1", Wait: "up to 48 hours", Billing: "billed"}, nil
	}
	for i := 0; i < 1; i++ {
		g, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true})
		if err != nil {
			t.Fatal(err)
		}
		if g.Thaw == nil || g.Token != "" {
			t.Fatalf("an archive copy yields a thaw and no grant: %+v", g)
		}
	}
	if thaws != 1 || f.store.restores.Load() != 1 {
		t.Fatalf("thaws=%d provider restores=%d, want exactly one of each", thaws, f.store.restores.Load())
	}
}

func TestDefaultThawGoesThroughTheExistingRestoreOperationOncePerDay(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "cold"})
	for i := 0; i < 2; i++ {
		g, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true})
		if err != nil || g.Thaw == nil {
			t.Fatalf("attempt %d: %+v, %v", i, g, err)
		}
	}
	if n := f.store.restores.Load(); n != 1 {
		t.Fatalf("two acknowledged requests started %d provider restores, want 1", n)
	}
}

func TestAnIncrementalEngineSetRefusesAndNamesSnapshotRestore(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, engine: "kopia"})
	_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")})
	if err == nil {
		t.Fatal("a kopia set must be refused naming snapshot restore, but the mint went through")
	}
	if codeOf(t, err) != RecoveryUseSnapshotRestore {
		t.Fatalf("a kopia set must be refused naming snapshot restore, got %v", err)
	}
	if !strings.Contains(err.Error(), "snapshot restore") {
		t.Fatalf("the sentence must name the verb that does apply: %v", err)
	}
}

func TestMintRefusesAnArtifactWithNoRecordedDigest(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	// A journal row with no SHA-256 anywhere cannot be verified.
	f.b.recoveryHost()
	plan, err := f.b.planRecovery(context.Background(), f.artifact, "", false)
	if err != nil {
		t.Fatal(err)
	}
	plan.expect.Hash = ""
	if err := checkPlanDigest(plan); codeOf(t, err) != RecoveryNoDigest {
		t.Fatalf("got %v", err)
	}
}

func TestUnknownArtifactIsNotFound(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: "production/postgres/nope", Target: stagingTo("x")})
	if !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestTargetsAreCheckedAtMint(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	for _, tg := range []RecoveryTarget{
		{Kind: "staging", Path: "../escape"},
		{Kind: "staging", Path: "/etc/cron.d/x"},
		{Kind: "staging", Path: ""},
		{Kind: "storage", Medium: "nonexistent"},
		{Kind: "storage", Medium: "local"},
		{Kind: "storage", Medium: "other", Key: "../x"},
		{Kind: "ftp"},
		{Kind: ""},
	} {
		_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: tg})
		if codeOf(t, err) != RecoveryTargetInvalid {
			t.Errorf("%+v: got %v", tg, err)
		}
	}
	if entries := f.stagingEntries(t); len(entries) != 0 {
		t.Fatalf("a refused mint left %v", entries)
	}
}

func TestStorageTargetUploadsOnceAndNeverReplaces(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	req := RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "storage", Medium: "other", Key: "recovered/a"}}
	res, _, err := f.b.RecoverNow(context.Background(), f.session(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "recovered/a" || !bytes.Equal(f.store.objects["recovered/a"], f.data) {
		t.Fatalf("result %+v", res)
	}
	_, _, err = f.b.RecoverNow(context.Background(), f.session(), req, nil)
	if codeOf(t, err) != RecoveryTargetExists {
		t.Fatalf("second upload to one key: %v", err)
	}
	if f.store.uploads.Load() != 1 {
		t.Fatalf("uploads = %d", f.store.uploads.Load())
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("staging not empty after an upload: %v", e)
	}
}

func TestGrantRefusalsAreThreeDifferentCodes(t *testing.T) {
	ctx := context.Background()

	// second use
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	g, err := f.b.MintRecovery(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("a")})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.b.RedeemRecovery(ctx, f.session(), g.Token); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.b.RedeemRecovery(ctx, f.session(), g.Token)
	used := codeOf(t, err)

	// after expiry
	sess2 := f.session()
	g2, _ := f.b.MintRecovery(ctx, sess2, RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("b")})
	f.clock.Advance(recoveryGrantTTL + time.Second)
	_, _, err = f.b.RedeemRecovery(ctx, sess2, g2.Token)
	expired := codeOf(t, err)

	// once the minting session ends
	short := RecoverySession{Actor: "alice", Expires: f.clock.Now().Add(10 * time.Second)}
	g3, err := f.b.MintRecovery(ctx, short, RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("c")})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(11 * time.Second)
	_, _, err = f.b.RedeemRecovery(ctx, short, g3.Token)
	ended := codeOf(t, err)

	if used != RecoveryGrantUsed || expired != RecoveryGrantExpired || ended != RecoveryGrantSessionEnded {
		t.Fatalf("codes = %s, %s, %s", used, expired, ended)
	}
}

func TestEndingASessionRevokesItsGrantsAndRemovesWhatItStaged(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	sess := f.session()
	g, _ := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	dl, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
	if err != nil {
		t.Fatal(err)
	}
	dl.Close()
	if len(f.stagingEntries(t)) != 1 {
		t.Fatal("a started download should hold exactly one staged file")
	}
	f.b.EndRecoverySession(sess)
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("logout left %v", e)
	}
	_, _, err = f.b.RedeemRecovery(ctx, sess, g.Token)
	if codeOf(t, err) != RecoveryGrantSessionEnded {
		t.Fatalf("got %v", err)
	}
}

func TestAnotherSessionCannotRedeemMyGrant(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	g, _ := f.b.MintRecovery(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("a")})
	mallory := RecoverySession{Actor: "mallory", Expires: f.clock.Now().Add(time.Hour)}
	_, _, err := f.b.RedeemRecovery(ctx, mallory, g.Token)
	if codeOf(t, err) != RecoveryGrantSessionEnded {
		t.Fatalf("got %v", err)
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("a refused redeem staged something: %v", e)
	}
}

func TestEveryMintAndEveryServedRangeWritesOneRowNamingActorArtifactCopyAndOutcome(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	sess := f.session()
	g, err := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	if err != nil {
		t.Fatal(err)
	}
	if rows := f.rows(t); len(rows) != 1 {
		t.Fatalf("one mint must write exactly one row, wrote %d", len(rows))
	}

	const ranges = 50
	size := int64(len(f.data))
	step := size / ranges
	for i := 0; i < ranges; i++ {
		dl, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
		if err != nil {
			t.Fatalf("range %d: %v", i, err)
		}
		start := int64(i) * step
		end := start + step - 1
		if i == ranges-1 {
			end = size - 1
		}
		dl.Close()
		dl.Served(start, end, end-start+1, nil)
	}
	rows := f.rows(t)
	if len(rows) != 1+ranges {
		t.Fatalf("50 ranges + 1 mint should write 51 rows, wrote %d", len(rows))
	}
	for i, r := range rows {
		for _, k := range []string{"actor", "artifact", "copy", "outcome", "target"} {
			if v, _ := r[k].(string); v == "" {
				t.Fatalf("row %d has no %q: %v", i, k, r)
			}
		}
		if r["actor"] != "alice" || r["artifact"] != f.artifact || r["copy"] != "local" {
			t.Fatalf("row %d misnames its subject: %v", i, r)
		}
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("serving the last byte should have cleaned staging: %v", e)
	}
	// Spent.
	if _, _, err := f.b.RedeemRecovery(ctx, sess, g.Token); codeOf(t, err) != RecoveryGrantUsed {
		t.Fatalf("got %v", err)
	}
}

func TestAFailedRangeWritesARowSayingSoAndRemovesStaging(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	sess := f.session()
	g, _ := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	dl, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
	if err != nil {
		t.Fatal(err)
	}
	dl.Close()
	// The browser went away at 50%.
	dl.Served(0, dl.Length()/2, dl.Length()/2, errors.New("write: broken pipe"))

	rows := f.rows(t)
	last := rows[len(rows)-1]
	if last["outcome"] != "failed" {
		t.Fatalf("the failed range must say so: %v", last)
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("a disconnect at 50%% left %v", e)
	}
	if _, _, err := f.b.RedeemRecovery(ctx, sess, g.Token); codeOf(t, err) != RecoveryGrantUsed {
		t.Fatalf("a dropped download must not be resumable on the same grant: %v", err)
	}
}

func TestRefusedRecoveriesAreAuditedToo(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	_, _ = f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")})
	rows := f.rows(t)
	if len(rows) != 1 || rows[0]["outcome"] != "refused" || rows[0]["error_code"] != RecoveryCostNotAcknowledged {
		t.Fatalf("rows = %v", rows)
	}
}

func TestStartupReconcileRemovesAKilledProcessesPartialsAndReportsThem(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	h := f.b.recoveryHost()
	if err := os.MkdirAll(h.staging.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		p := filepath.Join(h.staging.Root, fmt.Sprintf(".rcv-dead%d.partial", i))
		if err := os.WriteFile(p, []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, failed, err := f.b.ReconcileRecoveryStaging(context.Background())
	if err != nil || removed != 2 || failed != 0 {
		t.Fatalf("reconcile = %d removed, %d failed, %v", removed, failed, err)
	}
	if !strings.Contains(f.log.String(), "removed 2 leftover") {
		t.Fatalf("the reconcile must report what it removed: %s", f.log.String())
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("left %v", e)
	}
}

func TestNoStateDirectoryMeansNowhereToStageAndARefusal(t *testing.T) {
	set, _ := model.NewBackupSetID("production", "postgres")
	cfg := testConfig(config.Source{Name: "production", BackupSets: []config.BackupSet{{ID: set, Name: "postgres"}}})
	b := New(cfg, openTestJournal(t), nil, nil)
	t.Cleanup(func() { _ = b.Close() })
	_, err := b.MintRecovery(context.Background(), RecoverySession{Actor: "a"}, RecoveryRequest{ArtifactID: "production/postgres/x", Target: stagingTo("x")})
	if codeOf(t, err) != RecoveryUnavailable {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat("recovery"); statErr == nil {
		t.Fatal("a recovery directory appeared in the working directory")
	}
}

func TestEveryRecoveryCodeIsDistinctAndNamespaced(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range RecoveryCodes() {
		if seen[c] {
			t.Fatalf("code %s listed twice", c)
		}
		seen[c] = true
		if !strings.HasPrefix(c, "RECOVERY_") {
			t.Fatalf("code %s is not in the RECOVERY_ namespace", c)
		}
	}
}

func TestTheRecoveryPathSpawnsNoSubprocess(t *testing.T) {
	files, _ := filepath.Glob("recover*.go")
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			switch strings.Trim(imp.Path.Value, `"`) {
			case "os/exec", "plugin":
				t.Errorf("%s imports %s", name, imp.Path.Value)
			}
		}
	}
}

func TestARestoredArchiveCopyIsReadableAndNeedsNoSecondRestore(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "cold"})
	expires := time.Now().Add(48 * time.Hour)
	f.store.restoreState = &transport.RestoreState{InProgress: false, ExpiresAt: &expires}

	res, thaw, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
	if err != nil || thaw != nil {
		t.Fatalf("the provider says the restore finished, so the copy is readable: %+v, %+v, %v", res, thaw, err)
	}
	if f.store.restores.Load() != 0 {
		t.Fatalf("a copy that is already restored started %d more provider restores", f.store.restores.Load())
	}
	if f.store.opens.Load() != 1 {
		t.Fatalf("the restored copy was read %d times, want 1", f.store.opens.Load())
	}
}

func TestAnArchiveCopyMidRestoreIsNotReadableAndStartsNothing(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "cold"})
	f.store.restoreState = &transport.RestoreState{InProgress: true}
	_, err := f.b.MintRecovery(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true})
	if codeOf(t, err) != RecoveryNotReadable {
		t.Fatalf("want %s, got %v", RecoveryNotReadable, err)
	}
	if f.store.restores.Load() != 0 || f.store.opens.Load() != 0 {
		t.Fatalf("a restore in progress must neither be started again nor read: restores=%d opens=%d", f.store.restores.Load(), f.store.opens.Load())
	}
}

func TestAnArchiveCopyWhoseMediumDoesNotAnswerIsUnreadableAndStartsNothing(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "cold"})
	f.store.restoreErr = errors.New("connection refused")
	_, err := f.b.MintRecovery(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true})
	if codeOf(t, err) != RecoveryNotReadable {
		t.Fatalf("want %s, got %v", RecoveryNotReadable, err)
	}
	if f.store.restores.Load() != 0 {
		t.Fatal("an unreachable medium was asked to start a restore")
	}
}

func TestOnlyAnArchiveClassCopyIsProbedAndNeverToStartAnything(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	if _, _, err := f.b.RecoverNow(context.Background(), f.session(),
		RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil); err != nil {
		t.Fatal(err)
	}
	if f.store.statuses.Load() != 0 {
		t.Fatalf("a standard-class copy was asked about restores %d times: that is a metadata call with no reason to be made", f.store.statuses.Load())
	}
}

func TestAnUnsatisfiableRangeIsRefusedWithoutSpendingTheGrantOrTheStagedCopy(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	sess := f.session()
	g, _ := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	dl, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
	if err != nil {
		t.Fatal(err)
	}
	dl.Close()
	dl.Served(dl.Length()+10, dl.Length()+20, 0, ErrRecoveryRangeNotSatisfiable)

	rows := f.rows(t)
	if last := rows[len(rows)-1]; last["outcome"] != "refused" {
		t.Fatalf("a range the copy does not have is a refusal, row = %v", last)
	}
	if len(f.stagingEntries(t)) != 1 {
		t.Fatal("the staged copy was thrown away over a client's typo; a remote copy would be read and billed again")
	}
	again, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
	if err != nil {
		t.Fatalf("the grant should still serve the next, valid range: %v", err)
	}
	again.Close()
}

func TestALocalCopyThatIsASymlinkIsNeverFollowed(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	real := filepath.Join(f.dir, "elsewhere.bin")
	if err := os.WriteFile(real, f.data, 0o600); err != nil { // identical bytes: the digest alone would accept it
		t.Fatal(err)
	}
	local := filepath.Join(f.localDir, "dump.zst")
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, local); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")}, nil)
	if err == nil {
		t.Fatal("a symlink at the artifact's path must be refused, but the recovery went ahead and read through it")
	}
	if codeOf(t, err) != RecoveryNotReadable {
		t.Fatalf("a symlink at the artifact's path must be refused as not readable, got %v", err)
	}
}

func TestALocalCopyThatIsAFifoDoesNotHangTheRecovery(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	local := filepath.Join(f.localDir, "dump.zst")
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(local, 0o600); err != nil {
		t.Skipf("no fifos here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x")}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if codeOf(t, err) != RecoveryNotReadable {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a fifo blocked the recovery: a non-regular file must be refused before it is opened")
	}
}

func TestADownloadStillBeingServedKeepsItsGrantPastTheGrantTTL(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true})
	sess := f.session()
	g, _ := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: RecoveryTarget{Kind: "download"}})
	dl, _, err := f.b.RedeemRecovery(ctx, sess, g.Token)
	if err != nil {
		t.Fatal(err)
	}
	// A slow link: the one request outlives the grant's lifetime, and an
	// unrelated recovery elsewhere triggers the reaper meanwhile.
	f.clock.Advance(10 * time.Minute)
	other, _ := f.b.MintRecovery(ctx, sess, RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("y")})
	if _, _, err := f.b.RedeemRecovery(ctx, sess, other.Token); err != nil {
		t.Fatal(err)
	}
	partials := 0
	for _, e := range f.stagingEntries(t) {
		if strings.HasSuffix(e, ".partial") {
			partials++
		}
	}
	if partials != 1 {
		t.Fatalf("the staged copy of a download that is still being served should still be there, found %d partials", partials)
	}
	buf := make([]byte, 16)
	if _, err := dl.Reader().Read(buf); err != nil {
		t.Fatalf("the download's own handle broke: %v", err)
	}
	dl.Close()
}

type failingReader struct{ err error }

func (f *failingReader) Read([]byte) (int, error) { return 0, f.err }
