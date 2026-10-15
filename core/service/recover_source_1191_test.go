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

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1191: the default recovery destination is the backup's own source,
// an SFTP upload back to the SSH host the backup ran against.
//
// Hermetic. The source host is a transport stub that holds what was uploaded
// in memory and answers the write probe the way the rclone adapter does, so
// nothing here reaches a real SSH host. tests/sftpintegration proves the
// adapter half against the throw-away SFTP container.

// sourceHost is the fixture's in-memory store plus a source that can be
// probed and uploaded to.
type sourceHost struct {
	*recoveryStore

	mu       sync.Mutex
	files    map[string][]byte // relPath -> bytes
	roots    []string          // the Source.Root each upload was handed
	probeErr error
	probes   atomic.Int32
	puts     atomic.Int32
}

func (s *sourceHost) ProbeSourceWrite(context.Context, transport.Source) error {
	s.probes.Add(1)
	return s.probeErr
}

func (s *sourceHost) UploadToSource(_ context.Context, src transport.Source, localPath, relPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[relPath]; ok {
		return fmt.Errorf("upload %s: %w", relPath, transport.ErrSourceObjectExists)
	}
	b, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	s.files[relPath] = b
	s.roots = append(s.roots, src.Root)
	s.puts.Add(1)
	return nil
}

var (
	_ transport.SourceWriteProbe = (*sourceHost)(nil)
	_ transport.SourceUploader   = (*sourceHost)(nil)
)

const sourceRemotePath = "/var/backups/postgres"

// sftpSet points the fixture's one set at an SFTP source.
func sftpSet(readOnly bool) func(*config.Config) {
	return func(cfg *config.Config) {
		bs := &cfg.Sources[0].BackupSets[0]
		bs.Remote = config.Remote{Type: "sftp", Host: "db1.example.internal", Port: 2222, User: "retnd-pull"}
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
		host = &sourceHost{recoveryStore: s, files: map[string][]byte{}}
		return host
	}})
	return f, host
}

var recoveredFolder = regexp.MustCompile(`^` + regexp.QuoteMeta(transport.RecoveredFolderPrefix) + `\d{8}-\d{6}$`)

func sourceTarget(folder string) RecoveryTarget {
	return RecoveryTarget{Kind: "source", Path: folder, Acknowledged: true}
}

func TestRecoverySource1191_DescribesAnSFTPSetsSourceWithoutTouchingIt(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	d := f.b.RecoverySourceFor("production/postgres")
	if !d.Available {
		t.Fatalf("an SFTP set has a source-host destination; got %+v", d)
	}
	if d.Host != "db1.example.internal" || d.Port != 2222 || d.User != "retnd-pull" || d.RemotePath != sourceRemotePath {
		t.Errorf("destination does not carry the set's own source settings: %+v", d)
	}
	if !recoveredFolder.MatchString(d.Folder) {
		t.Errorf("default folder %q is not a new %s<timestamp> folder", d.Folder, transport.RecoveredFolderPrefix)
	}
	if n := host.probes.Load(); n != 0 {
		t.Errorf("describing the destination probed the source %d times; it must not touch the network", n)
	}
}

func TestRecoverySource1191_TheDefaultTargetIsTheSourceForAnSFTPSet(t *testing.T) {
	f, _ := newSourceFixture(t, sftpSet(false))
	for _, id := range []string{f.artifact, "production/postgres"} {
		got, why := f.b.DefaultRecoveryTarget(id)
		if got.Kind != "source" || why != "" {
			t.Errorf("DefaultRecoveryTarget(%q) = %+v, %q; want the source host", id, got, why)
		}
		if !recoveredFolder.MatchString(got.Path) {
			t.Errorf("DefaultRecoveryTarget(%q) folder %q is not a new recovered folder", id, got.Path)
		}
		if got.Acknowledged {
			t.Errorf("a default must never carry the acknowledgement: that is the operator's to give")
		}
	}
}

func TestRecoverySource1191_ANonSFTPSetPreselectsNothingAndSaysWhy(t *testing.T) {
	f, host := newSourceFixture(t, localSet)
	d := f.b.RecoverySourceFor("production/postgres")
	if d.Available || !strings.Contains(d.Reason, "not SFTP") {
		t.Errorf("a local-mount set has no source host; got %+v", d)
	}
	got, why := f.b.DefaultRecoveryTarget(f.artifact)
	if got.Kind != "" || !strings.Contains(why, "not SFTP") {
		t.Errorf("DefaultRecoveryTarget = %+v, %q; want nothing preselected and the reason", got, why)
	}
	_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: sourceTarget("")})
	if codeOf(t, err) != RecoverySourceUnavailable {
		t.Errorf("mint to a non-SFTP set's source = %v", err)
	}
	if host.probes.Load() != 0 || host.puts.Load() != 0 {
		t.Errorf("a refused source target touched the source: %d probes, %d uploads", host.probes.Load(), host.puts.Load())
	}
}

func TestRecoverySource1191_PreflightSaysAReadOnlyAccountBeforeTheRun(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	host.probeErr = refusedWriteProbe()

	d, err := f.b.PreflightRecoverySource(context.Background(), "production/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if d.Writable != "no" || !strings.Contains(d.Detail, "read-only") {
		t.Errorf("preflight on a read-only account = %+v; it has to say so before the run", d)
	}
	if host.probes.Load() != 1 {
		t.Errorf("preflight probed %d times, want exactly one write probe", host.probes.Load())
	}

	_, err = f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: sourceTarget("")})
	if codeOf(t, err) != RecoverySourceReadOnly {
		t.Fatalf("mint onto a read-only account = %v; want %s before anything is read", err, RecoverySourceReadOnly)
	}
	if e := f.stagingEntries(t); len(e) != 0 || host.puts.Load() != 0 {
		t.Errorf("a refused source recovery staged %v and uploaded %d", e, host.puts.Load())
	}
}

func TestRecoverySource1191_AWritableAccountPassesThePreflight(t *testing.T) {
	f, _ := newSourceFixture(t, sftpSet(false))
	d, err := f.b.PreflightRecoverySource(context.Background(), "production/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Available || d.Writable != "yes" {
		t.Errorf("preflight on a writable account = %+v", d)
	}
}

func TestRecoverySource1191_ASetDeclaredReadOnlyIsRefusedWithoutAProbe(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(true))
	d, err := f.b.PreflightRecoverySource(context.Background(), "production/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if d.Writable != "no" || !strings.Contains(d.Detail, "read_only") {
		t.Errorf("preflight on a set declared read_only = %+v", d)
	}
	_, err = f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: sourceTarget("")})
	if codeOf(t, err) != RecoverySourceReadOnly {
		t.Errorf("mint onto a read_only set = %v", err)
	}
	if host.probes.Load() != 0 {
		t.Errorf("a set declared read_only was probed %d times: retnd must not write to it at all", host.probes.Load())
	}
}

func TestRecoverySource1191_WritingToTheSourceNeedsTheAcknowledgement(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	tg := sourceTarget("")
	tg.Acknowledged = false
	_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: tg})
	if codeOf(t, err) != RecoverySourceNotAcknowledged {
		t.Errorf("mint onto the source without the acknowledgement = %v", err)
	}
	if host.probes.Load() != 0 || host.puts.Load() != 0 {
		t.Errorf("an unacknowledged source recovery touched the source")
	}
}

func TestRecoverySource1191_UploadsIntoANewSubFolderAndNeverOverwrites(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	ctx := context.Background()

	res, thaw, err := f.b.RecoverNow(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: sourceTarget("")}, nil)
	if err != nil || thaw != nil {
		t.Fatalf("RecoverNow to the source: %v, %v", err, thaw)
	}
	if len(host.files) != 1 {
		t.Fatalf("uploads = %v, want one", host.files)
	}
	var rel string
	for k := range host.files {
		rel = k
	}
	folder, name := path.Split(rel)
	if !recoveredFolder.MatchString(strings.TrimSuffix(folder, "/")) || name != "dump.zst" {
		t.Errorf("uploaded to %q; want <new recovered folder>/dump.zst", rel)
	}
	if !bytes.Equal(host.files[rel], f.data) {
		t.Errorf("the bytes on the source are not the recovered ones")
	}
	if host.roots[0] != sourceRemotePath {
		t.Errorf("uploaded under root %q, want the set's remote path", host.roots[0])
	}
	if res.Target.Kind != "source" || res.Path != path.Join(sourceRemotePath, rel) || res.SHA256 != sha(f.data) {
		t.Errorf("result = %+v", res)
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Errorf("staging not empty after a source upload: %v", e)
	}

	// The same folder again: the file is there, so nothing is replaced.
	_, _, err = f.b.RecoverNow(ctx, f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: sourceTarget(strings.TrimSuffix(folder, "/"))}, nil)
	if codeOf(t, err) != RecoveryTargetExists {
		t.Errorf("a second recovery into the same folder = %v; it must never overwrite", err)
	}
	if host.puts.Load() != 1 {
		t.Errorf("uploads = %d, want 1", host.puts.Load())
	}
}

func TestRecoverySource1191_TheFolderIsAlwaysANewRecoveredFolder(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	for _, folder := range []string{
		"../escape",
		"/etc",
		transport.RecoveredFolderPrefix + "x/../../y",
		transport.RecoveredFolderPrefix + "a/b",
		"plain-folder", // would be listed by the next run as backups
		".",
	} {
		_, err := f.b.MintRecovery(context.Background(), f.session(), RecoveryRequest{ArtifactID: f.artifact, Target: sourceTarget(folder)})
		if codeOf(t, err) != RecoveryTargetInvalid {
			t.Errorf("folder %q: got %v, want %s", folder, err, RecoveryTargetInvalid)
		}
	}
	if host.probes.Load() != 0 {
		t.Errorf("an invalid folder was probed")
	}
}

func TestRecoverySource1191_ASetGoesBackToItsSourceInOneNewFolder(t *testing.T) {
	f, host := newSourceFixture(t, sftpSet(false))
	res, err := f.b.RecoverMany(context.Background(), f.session(), RecoverManyRequest{
		Set: "production/postgres", Target: sourceTarget(""),
	}, nil)
	if err != nil {
		t.Fatalf("RecoverMany to the source: %v", err)
	}
	if res.Totals.Recovered != 1 {
		t.Fatalf("totals = %+v", res.Totals)
	}
	found := false
	for rel := range host.files {
		if strings.HasSuffix(rel, "/dump.zst") && recoveredFolder.MatchString(strings.Split(rel, "/")[0]) {
			found = true
		}
	}
	if !found {
		t.Errorf("uploads = %v; want dump.zst inside one new recovered folder", uploadedPaths(host.files))
	}
}

func TestRecoverySource1191_TheNewCodesAreInTheClosedSet(t *testing.T) {
	have := map[string]bool{}
	for _, c := range RecoveryCodes() {
		have[c] = true
	}
	for _, c := range []string{RecoverySourceUnavailable, RecoverySourceReadOnly, RecoverySourceNotAcknowledged} {
		if !have[c] {
			t.Errorf("%s is not in RecoveryCodes()", c)
		}
	}
	if !isRefusal(RecoverySourceReadOnly) || !isRefusal(RecoverySourceNotAcknowledged) || !isRefusal(RecoverySourceUnavailable) {
		t.Errorf("the source refusals must be audited as refusals, not failures")
	}
	if !errors.Is(fmt.Errorf("x: %w", transport.ErrSourceObjectExists), transport.ErrSourceObjectExists) {
		t.Fatal("sentinel")
	}
}

func uploadedPaths(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
