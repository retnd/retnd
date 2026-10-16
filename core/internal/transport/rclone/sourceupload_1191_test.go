package rclone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"

	"github.com/retnd/retnd/core/internal/transport"
)

// Issue #1191's upload rules, checked against rclone's local backend so the
// failure paths can be forced. The rename and the refusals a real SFTP
// server gives are proved against the fixture in
// core/tests/sftpintegration/source_upload_1191_test.go; what is here is the
// logic around them: the path rule, the temporary name, and that a failure
// only ever removes what this call wrote.

const testFolder = transport.RecoveredFolderPrefix + "20261015-101500-ab12"

func localFs(t *testing.T, root string) fs.Fs {
	t.Helper()
	info, err := fs.Find("local")
	if err != nil {
		t.Fatal(err)
	}
	f, err := info.NewFs(context.Background(), "upload-test", root, configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func testSession(t *testing.T, f fs.Fs, root string) *uploadSession {
	t.Helper()
	s := newUploadSession(transport.Source{ID: "upload-test", Type: "sftp", Root: root}, f, func() {})
	s.hf = f // the local backend hashes on its own
	return s
}

func writeLocal(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dump.zst")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func sum(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	return names
}

func TestSourceUploadRelPath_TheRule(t *testing.T) {
	good := []string{
		testFolder + "/dump.zst",
		transport.RecoveredFolderPrefix + "x/a.b-c_d",
	}
	bad := []string{
		"", "dump.zst", "/" + testFolder + "/dump.zst", testFolder, testFolder + "/",
		testFolder + "/a/b", "../" + testFolder + "/x", testFolder + "/../x",
		"other/dump.zst", transport.RecoveredFolderPrefix + "/x",
		transport.RecoveredFolderPrefix + "x./dump", transport.RecoveredFolderPrefix + "a b/dump",
		transport.RecoveredFolderPrefix + "a\\b/dump", testFolder + "/.hidden",
		testFolder + "/x" + transport.UploadTempMarker + "1", testFolder + "/./x",
	}
	for _, rel := range good {
		if _, _, err := splitUploadRelPath(rel); err != nil {
			t.Errorf("%q was refused: %v", rel, err)
		}
	}
	for _, rel := range bad {
		if _, _, err := splitUploadRelPath(rel); err == nil {
			t.Errorf("%q was accepted", rel)
		}
	}
	for _, folder := range []string{testFolder, transport.RecoveredFolderPrefix + "a"} {
		if !validRecoveredFolderName(folder) {
			t.Errorf("folder %q was refused", folder)
		}
	}
	for _, folder := range []string{"", transport.RecoveredFolderPrefix, "x" + testFolder, testFolder + ".", testFolder + "/x", "..", "retnd-recovered-ä"} {
		if validRecoveredFolderName(folder) {
			t.Errorf("folder %q was accepted", folder)
		}
	}
}

func FuzzSourceUploadRelPath(f *testing.F) {
	for _, seed := range []string{
		testFolder + "/dump.zst", "../x", testFolder + "/../../etc/passwd", "/abs", testFolder + "/a/b",
		transport.RecoveredFolderPrefix + "../x", transport.RecoveredFolderPrefix + "./x", "a/b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		folder, name, err := splitUploadRelPath(rel)
		if err != nil {
			return
		}
		if !strings.HasPrefix(folder, transport.RecoveredFolderPrefix) {
			t.Fatalf("%q accepted with folder %q, which lacks the prefix", rel, folder)
		}
		if strings.Count(rel, "/") != 1 || folder+"/"+name != rel {
			t.Fatalf("%q accepted but is not exactly folder/name", rel)
		}
		joined := path.Join("/root", rel)
		if !strings.HasPrefix(joined, "/root/"+transport.RecoveredFolderPrefix) {
			t.Fatalf("%q escapes the root: %q", rel, joined)
		}
		if name == "." || name == ".." || folder == "." || folder == ".." || strings.HasPrefix(name, ".") {
			t.Fatalf("%q accepted with a dot segment", rel)
		}
	})
}

func TestBeginSourceUpload_RefusesWhatIsNotAnSFTPSourceWithARoot(t *testing.T) {
	a := New()
	for _, src := range []transport.Source{
		{ID: "l", Type: "local", Root: t.TempDir()},
		{ID: "s", Type: "sftp", Host: "h", User: "u"},
	} {
		s, err := a.BeginSourceUpload(context.Background(), src)
		if err == nil {
			s.Close()
			t.Fatalf("%+v: a session was opened", src)
		}
		if cat, _ := transport.CategoryOf(err); cat != transport.Configuration {
			t.Errorf("%+v: refused as %v, want Configuration", src, cat)
		}
	}
}

func TestUploadSession_PlacesTheFileThroughATemporaryName(t *testing.T) {
	root := t.TempDir()
	s := testSession(t, localFs(t, root), root)
	ctx := context.Background()
	if err := s.ClaimFolder(ctx, testFolder, false); err != nil {
		t.Fatalf("ClaimFolder: %v", err)
	}
	if names := entries(t, root); len(names) != 1 || names[0] != testFolder {
		t.Fatalf("after the claim the root holds %v, want only the folder (no claim dir left)", names)
	}
	v, err := s.Upload(ctx, writeLocal(t, "payload"), testFolder+"/dump.zst", sum("payload"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if v != transport.VerifiedSHA256 {
		t.Errorf("verified by %q, want sha256 on a backend that hashes", v)
	}
	got, err := os.ReadFile(filepath.Join(root, testFolder, "dump.zst"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("the placed file = %q, %v", got, err)
	}
	if names := entries(t, filepath.Join(root, testFolder)); len(names) != 1 {
		t.Fatalf("the folder holds %v, want only the file (no temporary name left)", names)
	}
}

func TestUploadSession_ClaimFolderIsNeverIdempotent(t *testing.T) {
	root := t.TempDir()
	s := testSession(t, localFs(t, root), root)
	ctx := context.Background()
	if err := os.Mkdir(filepath.Join(root, testFolder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimFolder(ctx, testFolder, false); !errors.Is(err, transport.ErrSourceObjectExists) {
		t.Fatalf("claiming an existing folder = %v, want ErrSourceObjectExists", err)
	}
	if err := s.ClaimFolder(ctx, testFolder, true); err != nil {
		t.Fatalf("reusing an existing folder when asked to = %v", err)
	}
	file := transport.RecoveredFolderPrefix + "file"
	if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimFolder(ctx, file, true); !errors.Is(err, transport.ErrSourceObjectExists) {
		t.Fatalf("claiming over a file, even with reuse = %v, want ErrSourceObjectExists", err)
	}
	if err := s.ClaimFolder(ctx, "not-recovered", false); err == nil {
		t.Fatal("a folder without the prefix was claimed")
	}
	for _, n := range entries(t, root) {
		if strings.Contains(n, ".claim-") {
			t.Fatalf("a refused claim left %s behind", n)
		}
	}
}

func TestUploadSession_AnExistingTargetIsRefusedAndLeftAlone(t *testing.T) {
	root := t.TempDir()
	s := testSession(t, localFs(t, root), root)
	dir := filepath.Join(root, testFolder)
	if err := os.MkdirAll(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dump.zst"), []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{testFolder + "/dump.zst", testFolder + "/adir"} {
		_, err := s.Upload(context.Background(), writeLocal(t, "ours"), rel, "")
		if !errors.Is(err, transport.ErrSourceObjectExists) {
			t.Errorf("%s: %v, want ErrSourceObjectExists", rel, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "dump.zst")); string(got) != "theirs" {
		t.Fatalf("the existing file was changed to %q", got)
	}
	if names := entries(t, dir); len(names) != 2 {
		t.Fatalf("a refused upload left %v", names)
	}
}

// failingPutFs writes part of the file under its name and then fails, the
// way a dropped connection leaves a short file behind. stuckRemove makes
// every Remove fail too.
type failingPutFs struct {
	fs.Fs
	stuckRemove bool
}

func (f *failingPutFs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	half := io.LimitReader(in, src.Size()/2)
	info := &sizedInfo{ObjectInfo: src, size: -1}
	if _, err := f.Fs.Put(ctx, half, info, options...); err != nil {
		return nil, err
	}
	return nil, errors.New("connection lost")
}

func (f *failingPutFs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, remote)
	if err != nil || !f.stuckRemove {
		return o, err
	}
	return stuckObject{o, false}, nil
}

type sizedInfo struct {
	fs.ObjectInfo
	size int64
}

func (s *sizedInfo) Size() int64 { return s.size }

// stuckObject refuses to be removed, or (gone) says it was and stays.
type stuckObject struct {
	fs.Object
	gone bool
}

func (o stuckObject) Remove(context.Context) error {
	if o.gone {
		return nil
	}
	return errors.New("permission denied")
}

func TestUploadSession_AFailedWriteRemovesOnlyItsTemporaryFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, testFolder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Somebody else's file in the same folder: a failure must not touch it.
	if err := os.WriteFile(filepath.Join(dir, "other.zst"), []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := testSession(t, &failingPutFs{Fs: localFs(t, root)}, root)
	_, err := s.Upload(context.Background(), writeLocal(t, strings.Repeat("x", 4096)), testFolder+"/dump.zst", "")
	if err == nil {
		t.Fatal("a failed write reported success")
	}
	if errors.Is(err, transport.ErrUploadNotRemoved) {
		t.Fatalf("the temporary file could be removed and was reported as not: %v", err)
	}
	if names := entries(t, dir); len(names) != 1 || names[0] != "other.zst" {
		t.Fatalf("after a failed write the folder holds %v, want only the other file", names)
	}
}

func TestUploadSession_ATemporaryFileThatCannotBeRemovedIsNamed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, testFolder), 0o755); err != nil {
		t.Fatal(err)
	}
	s := testSession(t, &failingPutFs{Fs: localFs(t, root), stuckRemove: true}, root)
	_, err := s.Upload(context.Background(), writeLocal(t, strings.Repeat("x", 4096)), testFolder+"/dump.zst", "")
	var left *transport.UploadNotRemovedError
	if !errors.As(err, &left) || !errors.Is(err, transport.ErrUploadNotRemoved) {
		t.Fatalf("err = %v, want an UploadNotRemovedError", err)
	}
	if !strings.HasPrefix(left.Path, root+"/"+testFolder+"/.dump.zst"+transport.UploadTempMarker) {
		t.Fatalf("the error names %q, want the temporary file under the root", left.Path)
	}
	if _, serr := os.Stat(filepath.Join(root, testFolder, "dump.zst")); !os.IsNotExist(serr) {
		t.Fatal("something was placed under the final name")
	}
}

func TestUploadSession_ADigestMismatchRemovesThePlacedFile(t *testing.T) {
	root := t.TempDir()
	s := testSession(t, localFs(t, root), root)
	if err := s.ClaimFolder(context.Background(), testFolder, false); err != nil {
		t.Fatal(err)
	}
	_, err := s.Upload(context.Background(), writeLocal(t, "payload"), testFolder+"/dump.zst", sum("something else"))
	if !errors.Is(err, transport.ErrSourceDigestMismatch) {
		t.Fatalf("err = %v, want ErrSourceDigestMismatch", err)
	}
	if names := entries(t, filepath.Join(root, testFolder)); len(names) != 0 {
		t.Fatalf("a file that failed its digest was left: %v", names)
	}
}

func TestUploadSession_CloseIsSafeTwiceAndGivesTheSlotBack(t *testing.T) {
	released := 0
	s := newUploadSession(transport.Source{ID: "x", Type: "sftp", Root: t.TempDir()}, nil, func() { released++ })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if released != 1 {
		t.Fatalf("released %d times, want 1", released)
	}
}

// probeFs wraps a local Fs so the probe's removal can be made to fail, or
// to report success and leave the file.
type probeFs struct {
	fs.Fs
	gone bool
}

func (f *probeFs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	o, err := f.Fs.Put(ctx, in, src, options...)
	if err != nil {
		return nil, err
	}
	return stuckObject{o, f.gone}, nil
}

func TestProbeOn_AProbeThatCannotBeRemovedIsNamed(t *testing.T) {
	for _, gone := range []bool{false, true} {
		root := t.TempDir()
		err := probeOn(context.Background(), &probeFs{Fs: localFs(t, root), gone: gone}, ".retnd-write-probe-abc")
		var left *transport.ProbeNotRemovedError
		if !errors.As(err, &left) || !errors.Is(err, transport.ErrProbeNotRemoved) {
			t.Fatalf("gone=%v: err = %v, want a ProbeNotRemovedError", gone, err)
		}
		if left.Name != ".retnd-write-probe-abc" {
			t.Fatalf("gone=%v: the error names %q", gone, left.Name)
		}
		if _, ok := transport.CategoryOf(err); !ok {
			t.Fatalf("gone=%v: the error lost its transport category", gone)
		}
	}
	root := t.TempDir()
	if err := probeOn(context.Background(), localFs(t, root), ".retnd-write-probe-ok"); err != nil {
		t.Fatalf("a removable probe = %v", err)
	}
	if names := entries(t, root); len(names) != 0 {
		t.Fatalf("a removed probe left %v", names)
	}
}
