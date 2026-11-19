package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/discovery"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issues #1191 and #1193: the default recovery destination is the backup's
// own source, an SFTP upload back to the SSH host the backup ran against,
// and the review's four decisions about how that meets a production host.
//
// Hermetic. The source host is a transport stub that holds what was uploaded
// in memory and answers the write probe the way the rclone adapter does, so
// nothing here reaches a real SSH host. tests/sftpintegration proves the
// adapter half against the throw-away SFTP container.

// sourceHost is the fixture's in-memory store plus a source that can be
// probed, asked for free space and uploaded to.
type sourceHost struct {
	*recoveryStore

	mu      sync.Mutex
	files   map[string][]byte // relPath -> bytes
	folders map[string]bool
	roots   []string // the Source.Root each session was opened on
	hosts   []string // the Source.Host each session was opened on

	probeErr  error
	probeGate chan struct{} // when set, a probe waits on it
	probes    atomic.Int32
	puts      atomic.Int32
	begins    atomic.Int32
	open      atomic.Int32 // sessions not yet closed
	maxOpen   atomic.Int32

	free    *int64
	freeErr error

	verify transport.UploadVerification
	// beforeUpload runs before every upload, a hook for a config change in
	// the middle of a run.
	beforeUpload func(rel string)
	// statLeft, when true, says a leftover probe file is still there.
	statLeft atomic.Bool
}

func (s *sourceHost) ProbeSourceWrite(ctx context.Context, _ transport.Source) error {
	s.probes.Add(1)
	if s.probeGate != nil {
		select {
		case <-s.probeGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.probeErr
}

func (s *sourceHost) Stat(ctx context.Context, src transport.Source, p string) (transport.RemoteArtifact, error) {
	if strings.HasPrefix(path.Base(p), transport.ProbeObjectPrefix) {
		if s.statLeft.Load() {
			return transport.RemoteArtifact{Path: p}, nil
		}
		return transport.RemoteArtifact{}, transport.NewError(transport.NotFound, "stat", errors.New("not found"))
	}
	return s.recoveryStore.Stat(ctx, src, p)
}

func (s *sourceHost) SourceFreeSpace(context.Context, transport.Source) (int64, bool, error) {
	if s.freeErr != nil {
		return 0, false, s.freeErr
	}
	if s.free == nil {
		return 0, false, nil
	}
	return *s.free, true, nil
}

func (s *sourceHost) BeginSourceUpload(_ context.Context, src transport.Source) (transport.SourceUploadSession, error) {
	s.begins.Add(1)
	if n := s.open.Add(1); n > s.maxOpen.Load() {
		s.maxOpen.Store(n)
	}
	s.mu.Lock()
	s.roots = append(s.roots, src.Root)
	s.hosts = append(s.hosts, src.Host)
	s.mu.Unlock()
	return &sourceSession{h: s}, nil
}

type sourceSession struct {
	h      *sourceHost
	closed bool
}

func (c *sourceSession) ClaimFolder(_ context.Context, folder string, reuse bool) error {
	c.h.mu.Lock()
	defer c.h.mu.Unlock()
	if c.h.folders[folder] && !reuse {
		return fmt.Errorf("claim %s: %w", folder, transport.ErrSourceObjectExists)
	}
	c.h.folders[folder] = true
	return nil
}

func (c *sourceSession) Upload(_ context.Context, localPath, rel, want string) (transport.UploadVerification, error) {
	if c.h.beforeUpload != nil {
		c.h.beforeUpload(rel)
	}
	c.h.mu.Lock()
	defer c.h.mu.Unlock()
	if _, ok := c.h.files[rel]; ok {
		return "", fmt.Errorf("upload %s: %w", rel, transport.ErrSourceObjectExists)
	}
	b, err := os.ReadFile(localPath)
	if err != nil {
		return "", err
	}
	if want != "" && c.h.verify == transport.VerifiedSHA256 && sha(b) != want {
		return "", transport.ErrSourceDigestMismatch
	}
	c.h.files[rel] = b
	c.h.puts.Add(1)
	if c.h.verify == "" {
		return transport.VerifiedSHA256, nil
	}
	return c.h.verify, nil
}

func (c *sourceSession) Close() error {
	if !c.closed {
		c.closed = true
		c.h.open.Add(-1)
	}
	return nil
}

var (
	_ transport.SourceWriteProbe    = (*sourceHost)(nil)
	_ transport.SourceUploader      = (*sourceHost)(nil)
	_ transport.SourceSpaceReporter = (*sourceHost)(nil)
)

const sourceRemotePath = "/var/backups/postgres"

// sftpSet points the fixture's one set at an SFTP source.
func sftpSet(readOnly bool) func(*config.Config) {
	return func(cfg *config.Config) {
		bs := &cfg.Sources[0].BackupSets[0]
		bs.Remote = config.Remote{Type: "sftp", Host: "db1.example.internal", Port: 2222, User: "retnd-pull", KnownHosts: "/etc/retnd/known_hosts"}
		bs.RemotePath = sourceRemotePath
		bs.ReadOnly = readOnly
	}
}

func localSet(cfg *config.Config) {
	bs := &cfg.Sources[0].BackupSets[0]
	bs.Remote = config.Remote{Type: "local"}
	bs.RemotePath = "/mnt/export/postgres"
}

func newSourceFixture(t *testing.T, cfg func(*config.Config)) (*recoveryFixture, *sourceHost) {
	t.Helper()
	var host *sourceHost
	f := newRecoveryFixture(t, fixtureOpts{localCopy: true, cfg: cfg, wrap: func(s *recoveryStore) transport.Transport {
		host = &sourceHost{recoveryStore: s, files: map[string][]byte{}, folders: map[string]bool{}}
		return host
	}})
	return f, host
}

// reconfigure swaps the service onto a copy of its configuration with the
// one set changed, the way a hot reload does.
func (f *recoveryFixture) reconfigure(t *testing.T, change func(*config.BackupSet)) {
	t.Helper()
	cur := *f.b.state.Load().inner.Config
	sources := append([]config.Source(nil), cur.Sources...)
	sources[0].BackupSets = append([]config.BackupSet(nil), sources[0].BackupSets...)
	change(&sources[0].BackupSets[0])
	cur.Sources = sources
	f.b.configMu.Lock()
	defer f.b.configMu.Unlock()
	f.b.adoptConfig(&cur)
}

var recoveredFolder = regexp.MustCompile(`^` + regexp.QuoteMeta(transport.RecoveredFolderPrefix) + `\d{8}-\d{6}-[0-9a-f]{4}$`)

// ackedSource is a source target acknowledging the destination as it is
// described now, for folder ("" takes the suggested one).
func ackedSource(t *testing.T, f *recoveryFixture, folder string) RecoveryTarget {
	t.Helper()
	d, err := f.b.DescribeSourceHost("production/postgres", folder)
	if err != nil {
		t.Fatalf("DescribeSourceHost: %v", err)
	}
	return RecoveryTarget{Kind: "source", Path: d.Folder, AcknowledgedDestination: d.DestinationID()}
}

func TestSourceHost1193_DescribingReadsConfigurationAndNeverTheNetwork(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	d, err := f.b.DescribeSourceHost("production/postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Available || d.Host != "db1.example.internal" || d.Port != 2222 || d.User != "retnd-pull" || d.RemotePath != sourceRemotePath {
		t.Errorf("destination does not carry the set's own source settings: %+v", d)
	}
	if !recoveredFolder.MatchString(d.Folder) {
		t.Errorf("suggested folder %q is not retnd-recovered-<date>-<time>-<4 hex>", d.Folder)
	}
	if d.WriteAccess != SourceWriteNotChecked {
		t.Errorf("write_access = %q before any check, want %q", d.WriteAccess, SourceWriteNotChecked)
	}
	if want := "sftp://retnd-pull@db1.example.internal:2222" + sourceRemotePath + "/" + d.Folder + "/"; d.URL() != want {
		t.Errorf("URL = %q, want %q", d.URL(), want)
	}
	if n := host.probes.Load(); n != 0 {
		t.Errorf("describing probed the host %d times; it must not touch the network", n)
	}
	if _, err := f.b.DescribeSourceHost("production/nope", ""); !errors.Is(err, ErrBackupSetNotFound) {
		t.Errorf("an unknown set = %v, want ErrBackupSetNotFound (a 404, not a 200 with a reason)", err)
	}
}

func TestSourceHost1193_TwoSuggestionsInOneSecondAreDifferentFolders(t *testing.T) {
	f, _ := newSourceFixture(t, sftpSet(false))
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		d, _ := f.b.DescribeSourceHost("production/postgres", "")
		seen[d.Folder] = true
	}
	if len(seen) < 2 {
		t.Errorf("20 suggestions in a row gave %d distinct folders; a name unique only to the second collides", len(seen))
	}
}

func TestSourceHost1193_TheDestinationIDBindsHostPortUserPathFolderAndHostKeys(t *testing.T) {
	f, _ := newSourceFixture(t, sftpSet(false))
	base, _ := f.b.DescribeSourceHost("production/postgres", transport.RecoveredFolderPrefix+"a")
	again, _ := f.b.DescribeSourceHost("production/postgres", transport.RecoveredFolderPrefix+"a")
	if base.DestinationID() == "" || base.DestinationID() != again.DestinationID() {
		t.Fatalf("one destination described twice has ids %q and %q", base.DestinationID(), again.DestinationID())
	}
	other, _ := f.b.DescribeSourceHost("production/postgres", transport.RecoveredFolderPrefix+"b")
	if other.DestinationID() == base.DestinationID() {
		t.Error("two folders share a destination id")
	}
	for name, change := range map[string]func(*config.BackupSet){
		"host":        func(bs *config.BackupSet) { bs.Remote.Host = "db2.example.internal" },
		"port":        func(bs *config.BackupSet) { bs.Remote.Port = 22 },
		"user":        func(bs *config.BackupSet) { bs.Remote.User = "root" },
		"remote path": func(bs *config.BackupSet) { bs.RemotePath = "/srv" },
		"known hosts": func(bs *config.BackupSet) { bs.Remote.KnownHosts = "/elsewhere" },
	} {
		g, _ := newSourceFixture(t, sftpSet(false))
		g.reconfigure(t, change)
		d, _ := g.b.DescribeSourceHost("production/postgres", transport.RecoveredFolderPrefix+"a")
		if d.DestinationID() == base.DestinationID() {
			t.Errorf("changing the %s left the destination id the same", name)
		}
	}
}

func TestSourceHost1193_AFolderNameIsOneSafeSegmentWithThePrefix(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	for _, folder := range []string{
		"../escape", "/etc", transport.RecoveredFolderPrefix + "x/../../y", transport.RecoveredFolderPrefix + "a/b",
		"plain-folder", ".", transport.RecoveredFolderPrefix + "ends-in-dot.", transport.RecoveredFolderPrefix + "spa ce",
		transport.RecoveredFolderPrefix + "$&", transport.RecoveredFolderPrefix + "é",
	} {
		if _, err := f.b.DescribeSourceHost("production/postgres", folder); codeOf(t, err) != RecoveryTargetInvalid {
			t.Errorf("describe folder %q: got %v, want %s", folder, err, RecoveryTargetInvalid)
		}
		_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact,
			Target: RecoveryTarget{Kind: "source", Path: folder, AcknowledgedDestination: "x"}})
		if codeOf(t, err) != RecoveryTargetInvalid {
			t.Errorf("mint folder %q: got %v, want %s", folder, err, RecoveryTargetInvalid)
		}
	}
	if host.probes.Load() != 0 {
		t.Errorf("an invalid folder was probed")
	}
}

func TestSourceHost1193_TheDefaultResolvesFromConfigurationOnlyWhereItCanWork(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	for _, id := range []string{f.artifact, "production/postgres"} {
		got, why := f.b.DefaultRecoveryTarget(id)
		if got.Kind != "source" || why != "" || !recoveredFolder.MatchString(got.Path) {
			t.Errorf("DefaultRecoveryTarget(%q) = %+v, %q; want the source host in a new folder", id, got, why)
		}
		if got.AcknowledgedDestination != "" {
			t.Error("a default must never carry the acknowledgement: that is the operator's to give")
		}
	}
	ro, _ := newSourceFixture(t, sftpSet(true))
	if got, why := ro.b.DefaultRecoveryTarget(ro.artifact); got.Kind != "" || !strings.Contains(why, "read_only") {
		t.Errorf("a set declared read_only has no default: got %+v, %q", got, why)
	}
	lo, _ := newSourceFixture(t, localSet)
	if got, why := lo.b.DefaultRecoveryTarget(lo.artifact); got.Kind != "" || !strings.Contains(why, "not SFTP") {
		t.Errorf("a local set has no default: got %+v, %q", got, why)
	}
	if host.probes.Load() != 0 {
		t.Error("resolving the default touched the network")
	}
}

func TestSourceHost1193_TheWriteCheckProbesOnceAndIsCachedForFiveMinutes(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	free := int64(5 << 30)
	host.free = &free
	clock := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	old := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = old })
	ctx := context.Background()

	d, err := f.b.CheckSourceHostWrite(ctx, "alice", "production/postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.WriteAccess != SourceWriteWritable || d.CheckedAt.IsZero() || d.FreeBytes == nil || *d.FreeBytes != free {
		t.Errorf("write check = %+v", d)
	}
	if _, err := f.b.CheckSourceHostWrite(ctx, "alice", "production/postgres", ""); err != nil {
		t.Fatal(err)
	}
	desc, _ := f.b.DescribeSourceHost("production/postgres", "")
	if host.probes.Load() != 1 || desc.WriteAccess != SourceWriteWritable {
		t.Errorf("probes = %d, describe says %q; the verdict is cached and shared", host.probes.Load(), desc.WriteAccess)
	}
	clock = clock.Add(5*time.Minute + time.Second)
	if _, err := f.b.CheckSourceHostWrite(ctx, "alice", "production/postgres", ""); err != nil {
		t.Fatal(err)
	}
	if host.probes.Load() != 2 {
		t.Errorf("probes = %d after the cache expired, want 2", host.probes.Load())
	}
	if !strings.Contains(f.log.String(), "write-check") {
		t.Errorf("a probe wrote no audit row:\n%s", f.log.String())
	}
}

func TestSourceHost1193_ConcurrentWriteChecksShareOneProbe(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	host.probeGate = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.b.CheckSourceHostWrite(context.Background(), "alice", "production/postgres", ""); err != nil {
				t.Error(err)
			}
		}()
	}
	for host.probes.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(host.probeGate)
	wg.Wait()
	if n := host.probes.Load(); n != 1 {
		t.Errorf("five concurrent checks ran %d probes, want 1", n)
	}
}

func TestSourceHost1193_TheWriteCheckIsRateLimited(t *testing.T) {
	ctx := context.Background()
	var limited error
	shared := newSourceCheckLimiter()
	for i := 0; i < sourceWriteChecksPerMinute+1 && limited == nil; i++ {
		// A new fixture per check, so the per-set cache never answers, all
		// counted against one limiter.
		f, _ := newSourceFixture(t, sftpSet(false))
		f.b.sourceHosts.limiter = shared
		_, limited = f.b.CheckSourceHostWrite(ctx, "alice", "production/postgres", "")
	}
	var rl *SourceWriteCheckRateLimited
	if !errors.As(limited, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("check %d past the limit = %v, want a rate limit with Retry-After", sourceWriteChecksPerMinute+1, limited)
	}
}

func TestSourceHost1193_ASetDeclaredReadOnlyIsNeverProbed(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(true))
	d, err := f.b.CheckSourceHostWrite(context.Background(), "alice", "production/postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.WriteAccess != SourceWriteReadOnly || !d.ReadOnly || d.Code != RecoverySourceHostReadOnly {
		t.Errorf("check of a read_only set = %+v", d)
	}
	_, err = f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact,
		Target: RecoveryTarget{Kind: "source", Path: d.Folder, AcknowledgedDestination: d.DestinationID()}})
	if codeOf(t, err) != RecoverySourceHostReadOnly {
		t.Errorf("mint onto a read_only set = %v", err)
	}
	if host.probes.Load() != 0 {
		t.Errorf("a set declared read_only was probed %d times", host.probes.Load())
	}
}

func TestSourceHost1193_AReadOnlyAccountIsSaidBeforeTheRun(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	host.probeErr = refusedWriteProbe()
	d, err := f.b.CheckSourceHostWrite(context.Background(), "alice", "production/postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.WriteAccess != SourceWriteReadOnly || d.Code != RecoverySourceHostReadOnly || !strings.Contains(d.Detail, "read-only") {
		t.Errorf("check on a read-only account = %+v", d)
	}
	_, err = f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: ackedSource(t, f, "")})
	if codeOf(t, err) != RecoverySourceHostReadOnly {
		t.Fatalf("mint onto a read-only account = %v", err)
	}
	if host.probes.Load() != 1 {
		t.Errorf("the mint probed again (%d probes); it shares the cached verdict", host.probes.Load())
	}
	if e := f.stagingEntries(t); len(e) != 0 || host.puts.Load() != 0 {
		t.Errorf("a refused source recovery staged %v and uploaded %d", e, host.puts.Load())
	}
}

func TestSourceHost1193_AProbeThatCannotRemoveItsFileIsNotWritable(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	host.probeErr = transport.NewError(transport.Permanent, "probe_source_write",
		&transport.ProbeNotRemovedError{Name: transport.ProbeObjectPrefix + "abc", Err: errors.New("remove refused")})
	host.statLeft.Store(true)
	ctx := context.Background()
	d, err := f.b.CheckSourceHostWrite(ctx, "alice", "production/postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.WriteAccess != SourceWriteProbeLeftFile || d.Code != RecoverySourceHostProbeLeftFile {
		t.Fatalf("a probe that left its file = %+v; it must not count as writable", d)
	}
	if want := path.Join(sourceRemotePath, transport.ProbeObjectPrefix+"abc"); d.LeftFile != want || !strings.Contains(d.Detail, want) {
		t.Errorf("left_file = %q, detail %q; want the file named (%s)", d.LeftFile, d.Detail, want)
	}
	_, err = f.b.MintRecovery(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: ackedSource(t, f, "")})
	if codeOf(t, err) != RecoverySourceHostProbeLeftFile {
		t.Errorf("mint after a probe left its file = %v", err)
	}
	// Still there, and the cache long gone: retnd does not probe again.
	old := now
	now = func() time.Time { return old().Add(time.Hour) }
	t.Cleanup(func() { now = old })
	if _, err := f.b.CheckSourceHostWrite(ctx, "alice", "production/postgres", ""); err != nil {
		t.Fatal(err)
	}
	if host.probes.Load() != 1 {
		t.Errorf("probes = %d while the left file is still there, want 1", host.probes.Load())
	}
	// Gone: the next check probes.
	host.statLeft.Store(false)
	host.probeErr = nil
	if d, _ := f.b.CheckSourceHostWrite(ctx, "alice", "production/postgres", ""); d.WriteAccess != SourceWriteWritable || host.probes.Load() != 2 {
		t.Errorf("after the left file went: %+v, probes %d", d, host.probes.Load())
	}
}

func TestSourceHost1193_AnUnreachableHostIsRetryableAndNotReadOnly(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	host.probeErr = transport.NewError(transport.Transient, "probe_source_write", errors.New("connection reset"))
	d, err := f.b.CheckSourceHostWrite(context.Background(), "alice", "production/postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.WriteAccess != SourceWriteUnreachable || d.Code != RecoverySourceHostUnreachable {
		t.Errorf("check on an unreachable host = %+v", d)
	}
	_, err = f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: ackedSource(t, f, "")})
	if codeOf(t, err) != RecoverySourceHostUnreachable {
		t.Errorf("mint onto an unreachable host = %v", err)
	}
	if isRefusal(RecoverySourceHostUnreachable) {
		t.Error("an unreachable host is a failure worth retrying, not a refusal of the request")
	}
}

func TestSourceHost1193_ConsentIsBoundToTheDestinationItWasGivenFor(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	ctx := context.Background()
	tg := ackedSource(t, f, "")

	missing := tg
	missing.AcknowledgedDestination = ""
	if _, err := f.b.MintRecovery(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: missing}); codeOf(t, err) != RecoverySourceHostNotAcknowledged {
		t.Errorf("mint with no acknowledgement = %v", err)
	}
	// The operator read db1, then the configuration moved to db2.
	f.reconfigure(t, func(bs *config.BackupSet) { bs.Remote.Host = "db2.example.internal" })
	_, err := f.b.MintRecovery(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: tg})
	if codeOf(t, err) != RecoverySourceHostNotAcknowledged || !strings.Contains(err.Error(), "db2.example.internal") {
		t.Errorf("mint after the host changed = %v; it must refuse and name what it would write to now", err)
	}
	if host.probes.Load() != 0 || host.puts.Load() != 0 {
		t.Error("a refused acknowledgement touched the host")
	}
}

func TestSourceHost1193_AConfigChangeBetweenMintAndRedeemIsRefused(t *testing.T) {
	for name, change := range map[string]struct {
		change func(*config.BackupSet)
		code   string
	}{
		"host moved":        {func(bs *config.BackupSet) { bs.Remote.Host = "db2.example.internal" }, RecoverySourceHostNotAcknowledged},
		"declared readonly": {func(bs *config.BackupSet) { bs.ReadOnly = true }, RecoverySourceHostReadOnly},
		"no longer sftp":    {func(bs *config.BackupSet) { bs.Remote = config.Remote{Type: "local"} }, RecoverySourceHostUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f, host := newSourceFixture(t, sftpSet(false))
			ctx := context.Background()
			g, err := f.b.MintRecovery(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: ackedSource(t, f, "")})
			if err != nil {
				t.Fatal(err)
			}
			f.reconfigure(t, change.change)
			_, _, err = f.b.RedeemRecovery(ctx, f.session(), g.Token)
			if codeOf(t, err) != change.code {
				t.Errorf("redeem after %s = %v, want %s", name, err, change.code)
			}
			if host.puts.Load() != 0 {
				t.Errorf("redeem after %s uploaded %d files", name, host.puts.Load())
			}
		})
	}
}

func TestSourceHost1193_ASourceGrantLivesSixtySeconds(t *testing.T) {
	f, _ := newSourceFixture(t, sftpSet(false))
	g, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: ackedSource(t, f, "")})
	if err != nil {
		t.Fatal(err)
	}
	if life := g.ExpiresAt.Sub(f.clock.Now()); life > 60*time.Second || life <= 0 {
		t.Errorf("a source grant lives %v, want at most 60s", life)
	}
	if !f.b.RecoveryGrantWritesToSourceHost(g.Token) {
		t.Error("RecoveryGrantWritesToSourceHost does not recognise a source grant")
	}
	sg, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x.zst")})
	if err != nil {
		t.Fatal(err)
	}
	if f.b.RecoveryGrantWritesToSourceHost(sg.Token) {
		t.Error("a staging grant reported as a source grant")
	}
}

func TestSourceHost1193_UploadsIntoANewFolderVerifiedAndNeverOverwrites(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	ctx := context.Background()
	tg := ackedSource(t, f, "")
	res, thaw, err := f.b.RecoverNow(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: tg}, nil)
	if err != nil || thaw != nil {
		t.Fatalf("RecoverNow to the source: %v, %v", err, thaw)
	}
	rel := tg.Path + "/dump.zst"
	if !bytes.Equal(host.files[rel], f.data) || len(host.files) != 1 {
		t.Fatalf("uploads = %v, want %s", uploadedPaths(host.files), rel)
	}
	if host.roots[0] != sourceRemotePath {
		t.Errorf("uploaded under root %q, want the set's remote path", host.roots[0])
	}
	if res.Path != path.Join(sourceRemotePath, rel) || res.SHA256 != sha(f.data) || res.Verified != "sha256" ||
		res.DestinationID != tg.AcknowledgedDestination || !strings.HasPrefix(res.URL, "sftp://") {
		t.Errorf("result = %+v", res)
	}
	if res.Target.AcknowledgedDestination != "" {
		t.Error("a result echoes the consent; it must say destination_id and url instead")
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Errorf("staging not empty after a source upload: %v", e)
	}
	// The same folder again: claimed already, so nothing is written.
	_, _, err = f.b.RecoverNow(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: ackedSource(t, f, tg.Path)}, nil)
	if codeOf(t, err) != RecoveryTargetExists {
		t.Errorf("a second recovery into the same folder = %v; it must never overwrite", err)
	}
	if host.puts.Load() != 1 {
		t.Errorf("uploads = %d, want 1", host.puts.Load())
	}
}

func TestSourceHost1193_AHostThatCannotHashSaysSizeOnly(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	host.verify = transport.VerifiedSize
	res, _, err := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: ackedSource(t, f, "")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified != "size" {
		t.Errorf("verified = %q, want size", res.Verified)
	}
}

func TestSourceHost1193_NoSpaceOnTheHostIsRefusedBeforeAnythingIsRead(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	free := int64(1000)
	host.free = &free
	_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: ackedSource(t, f, "")})
	if codeOf(t, err) != RecoverySourceHostNoSpace {
		t.Fatalf("mint of a %d byte file onto a host with %d free = %v", len(f.data), free, err)
	}
	if !strings.Contains(err.Error(), "1000") {
		t.Errorf("the refusal does not give the numbers: %v", err)
	}
	if e := f.stagingEntries(t); len(e) != 0 || host.puts.Load() != 0 {
		t.Errorf("a refused source recovery staged %v and uploaded %d", e, host.puts.Load())
	}
}

func TestSourceHost1193_ASetGoesBackInOneSessionIntoOneNewFolder(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 40_000), age: 48 * time.Hour},
		manyFile{name: "b.zst", data: manyData(2, 30_000), age: 24 * time.Hour},
	)
	tg := ackedSource(t, f, "")
	unacked := RecoverManyRequest{Set: "production/postgres", All: true, Target: RecoveryTarget{Kind: "source", Path: tg.Path}}
	if _, err := f.b.RecoverMany(context.Background(), f.session(), unacked, nil); codeOf(t, err) != RecoverySourceHostNotAcknowledged {
		t.Fatalf("a set run onto the source without the acknowledgement = %v", err)
	}
	if host.probes.Load() != 0 || host.puts.Load() != 0 {
		t.Fatalf("an unacknowledged set run touched the source")
	}

	res, err := f.b.RecoverMany(context.Background(), f.session(), RecoverManyRequest{Set: "production/postgres", All: true, Target: tg}, nil)
	if err != nil {
		t.Fatalf("RecoverMany to the source: %v", err)
	}
	if res.Totals.Recovered != 2 || res.Folder != tg.Path || res.DestinationID != tg.AcknowledgedDestination {
		t.Fatalf("result = %+v", res)
	}
	manifest := "manifest-" + res.OperationID + ".json"
	for _, name := range []string{"a.zst", "b.zst", manifest} {
		if _, ok := host.files[res.Folder+"/"+name]; !ok {
			t.Errorf("%s is not in the new folder; uploads = %v", name, uploadedPaths(host.files))
		}
	}
	if host.begins.Load() != 1 {
		t.Errorf("a run of 2 files and a manifest opened %d upload sessions, want 1", host.begins.Load())
	}
	if host.probes.Load() != 1 {
		t.Errorf("a run probed %d times, want 1", host.probes.Load())
	}
}

func TestSourceHost1193_AConfigChangeMidRunStopsTheRestOfTheRun(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 40_000), age: 48 * time.Hour},
		manyFile{name: "b.zst", data: manyData(2, 30_000), age: 24 * time.Hour},
	)
	var once sync.Once
	host.beforeUpload = func(string) {
		// A change freeze declared while the run is going.
		once.Do(func() { f.reconfigure(t, func(bs *config.BackupSet) { bs.ReadOnly = true }) })
	}
	res, _ := f.b.RecoverMany(context.Background(), f.session(), RecoverManyRequest{Set: "production/postgres", All: true, Target: ackedSource(t, f, "")}, nil)
	if host.puts.Load() > 1 {
		t.Errorf("%d files went onto a set declared read_only mid-run", host.puts.Load())
	}
	failed := 0
	for _, it := range res.Items {
		if it.Status == RecoverManyFailed && it.Code == RecoverySourceHostReadOnly {
			failed++
		}
	}
	if failed == 0 {
		t.Errorf("no file was refused with %s after the set was declared read_only: %+v", RecoverySourceHostReadOnly, res.Items)
	}
}

func TestSourceHost1193_ASetRunThatDoesNotFitOnTheHostIsRefused(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 40_000), age: 48 * time.Hour},
		manyFile{name: "b.zst", data: manyData(2, 30_000), age: 24 * time.Hour},
	)
	free := int64(50_000)
	host.free = &free
	plan, err := f.b.PlanRecoverMany(context.Background(), RecoverManyRequest{Set: "production/postgres", Target: ackedSource(t, f, "")})
	if codeOf(t, err) != RecoverySourceHostNoSpace {
		t.Fatalf("plan of a set larger than the host's free space = %v", err)
	}
	if plan.SourceFreeBytes == nil || *plan.SourceFreeBytes != free {
		t.Errorf("plan.SourceFreeBytes = %v, want %d", plan.SourceFreeBytes, free)
	}
	host.free = nil
	plan, err = f.b.PlanRecoverMany(context.Background(), RecoverManyRequest{Set: "production/postgres", Target: ackedSource(t, f, "")})
	if err != nil || plan.SourceFreeBytes != nil {
		t.Errorf("a host that cannot say: plan %+v, %v; unknown is not a refusal", plan.SourceFreeBytes, err)
	}
	if host.puts.Load() != 0 {
		t.Error("a plan wrote something")
	}
}

func TestSourceHost1193_TheServiceWritesToASourceHostOnlyWhenTheClassifierSaysSo(t *testing.T) {
	// The gate asks RecoveryTargetWritesToSourceHost; this holds every kind
	// the service accepts to the same answer, so a new kind that writes onto
	// the source cannot get past the gate by not being called "source".
	for _, tg := range []RecoveryTarget{
		{Kind: "download"}, {Kind: "staging", Path: "x"}, {Kind: "storage", Medium: "other"},
		{Kind: "source"}, {Kind: "source", Path: transport.RecoveredFolderPrefix + "a"},
		{Kind: "SOURCE"}, {Kind: ""},
	} {
		f, host := newSourceFixture(t, sftpSet(false))
		tg2 := tg
		if tg.Kind == "source" {
			tg2 = ackedSource(t, f, tg.Path)
		}
		_, _, _ = f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: tg2}, nil)
		wrote := host.puts.Load() > 0 || host.probes.Load() > 0
		if wrote && !RecoveryTargetWritesToSourceHost(tg) {
			t.Errorf("target %+v wrote to the source host and the classifier says it does not", tg)
		}
	}
}

func TestSourceHost1193_TheNewCodesAreInTheClosedSetAndAuditedAsRefusals(t *testing.T) {
	have := map[string]bool{}
	for _, c := range RecoveryCodes() {
		have[c] = true
	}
	for _, c := range []string{RecoverySourceHostUnavailable, RecoverySourceHostReadOnly, RecoverySourceHostNotAcknowledged,
		RecoverySourceHostUnreachable, RecoverySourceHostNoSpace, RecoverySourceHostProbeLeftFile} {
		if !have[c] {
			t.Errorf("%s is not in RecoveryCodes()", c)
		}
		if c != RecoverySourceHostUnreachable && !isRefusal(c) {
			t.Errorf("%s must be audited as a refusal", c)
		}
	}
}

func uploadedPaths(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSourceHost1193_TheConnectionTestCountsWhatDiscoveryWouldLookAt(t *testing.T) {
	p := transport.RecoveredFolderPrefix
	got := listedCandidates([]transport.RemoteArtifact{
		{Path: "a.dump"}, {Path: p + "x/a.dump"}, {Path: p + "x/manifest-1.json"}, {Path: "deep/" + p + "x/b.dump"},
	})
	if got != 2 {
		t.Errorf("listedCandidates = %d, want 2: the two inside the recovered folder are what discovery skips", got)
	}
}

// FuzzValidRecoveredFolder holds the folder rule to what the transport and
// discovery need of it, for any input: one segment, the prefix, a safe
// alphabet, no leading or trailing dot after the prefix.
func FuzzValidRecoveredFolder(f *testing.F) {
	for _, seed := range []string{transport.RecoveredFolderPrefix + "a", "../x", transport.RecoveredFolderPrefix + ".claim-1", transport.RecoveredFolderPrefix + "a.", transport.RecoveredFolderPrefix + "a/b", "x"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, folder string) {
		if !validRecoveredFolder(folder) {
			return
		}
		rest := strings.TrimPrefix(folder, transport.RecoveredFolderPrefix)
		switch {
		case !strings.HasPrefix(folder, transport.RecoveredFolderPrefix):
			t.Fatalf("%q accepted without the prefix", folder)
		case strings.ContainsAny(folder, "/\\\x00 "):
			t.Fatalf("%q accepted with a separator or a space", folder)
		case rest == "" || strings.HasPrefix(rest, ".") || strings.HasSuffix(folder, "."):
			t.Fatalf("%q accepted with an empty, dotted or trailing-dot name", folder)
		case len(folder) > maxRecoveredFolderLen:
			t.Fatalf("%q accepted over the length bound", folder)
		case path.Clean(folder) != folder:
			t.Fatalf("%q accepted though it does not clean to itself", folder)
		}
		if !discovery.InRecoveredFolder(folder + "/x") {
			t.Fatalf("a file in accepted folder %q is not one discovery skips", folder)
		}
	})
}
