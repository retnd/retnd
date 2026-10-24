package sftpintegration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
	"github.com/retnd/retnd/core/tests/machines"
)

// Issue #1191's transport half, against the throw-away SFTP container this
// package already starts (never a real host). What it proves is the set of
// promises the "Source host (SFTP)" destination makes about a production
// machine: a file lands under a temporary name and is renamed into place, an
// existing file, folder or symlink is never replaced, a cancelled or dropped
// upload leaves nothing under the real name, no timestamp is ever set, a
// read-only account is refused as a permission, and one host takes one
// upload session at a time.
//
// Every subtest seeds its own directory under the fixture's upload folder,
// so any of them can run alone (-run) and their order does not matter.

const upFolder = transport.RecoveredFolderPrefix + "20261015-101500-ab12"

type uploadFixture struct {
	m   *machines.Machines
	src *machines.Source
	up  transport.SourceUploader
}

func startUploadFixture(t *testing.T) *uploadFixture {
	t.Helper()
	m := machines.Start(t)
	src := m.Source(t)
	up, ok := any(rclone.New()).(transport.SourceUploader)
	if !ok {
		t.Fatal("the rclone adapter does not implement transport.SourceUploader")
	}
	return &uploadFixture{m: m, src: src, up: up}
}

// seed makes a directory of this subtest's own under the upload folder,
// writable by the fixture's account, and returns its host-side path and the
// transport.Source rooted at it.
func (fx *uploadFixture) seed(t *testing.T, mode os.FileMode) (string, transport.Source) {
	t.Helper()
	name := "up-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	dir := filepath.Join(fx.src.UploadDir, name)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	return dir, fx.src.TransportSource("upload-"+name, name)
}

func (fx *uploadFixture) begin(t *testing.T, ts transport.Source) transport.SourceUploadSession {
	t.Helper()
	s, err := fx.up.BeginSourceUpload(fx.src.Context(), ts)
	if err != nil {
		t.Fatalf("BeginSourceUpload: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func localFile(t *testing.T, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dump.zst")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range des {
		out = append(out, d.Name())
	}
	return out
}

func noTempLeft(t *testing.T, dir string) {
	t.Helper()
	for _, n := range dirNames(t, dir) {
		if strings.Contains(n, transport.UploadTempMarker) || strings.Contains(n, ".claim-") {
			t.Errorf("%s was left in %s", n, dir)
		}
	}
}

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestSourceUpload1191_AgainstARealServer(t *testing.T) {
	fx := startUploadFixture(t)
	payload := bytes.Repeat([]byte("recovered "), 4096)
	rel := upFolder + "/dump.zst"

	t.Run("it lands through a temporary name in a new folder", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		s := fx.begin(t, ts)
		if err := s.ClaimFolder(fx.src.Context(), upFolder, false); err != nil {
			t.Fatalf("ClaimFolder: %v", err)
		}
		v, err := s.Upload(fx.src.Context(), localFile(t, payload), rel, digestOf(payload))
		if err != nil {
			t.Fatalf("Upload: %v", err)
		}
		if v != transport.VerifiedSHA256 && v != transport.VerifiedSize {
			t.Errorf("verified by %q", v)
		}
		got, err := os.ReadFile(filepath.Join(dir, upFolder, "dump.zst"))
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("the file on the source is not the recovered one: %v", err)
		}
		noTempLeft(t, dir)
		noTempLeft(t, filepath.Join(dir, upFolder))
	})

	t.Run("an existing file is never replaced", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		mustMkdir(t, filepath.Join(dir, upFolder))
		theirs := []byte("somebody else's file")
		mustWrite(t, filepath.Join(dir, upFolder, "dump.zst"), theirs)
		s := fx.begin(t, ts)
		_, err := s.Upload(fx.src.Context(), localFile(t, payload), rel, "")
		if !errors.Is(err, transport.ErrSourceObjectExists) {
			t.Fatalf("upload over an existing file = %v, want ErrSourceObjectExists", err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, upFolder, "dump.zst")); !bytes.Equal(got, theirs) {
			t.Fatal("the existing file was changed")
		}
		noTempLeft(t, filepath.Join(dir, upFolder))
	})

	t.Run("a folder at the target is refused", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		mustMkdir(t, filepath.Join(dir, upFolder, "dump.zst"))
		s := fx.begin(t, ts)
		if _, err := s.Upload(fx.src.Context(), localFile(t, payload), rel, ""); !errors.Is(err, transport.ErrSourceObjectExists) {
			t.Fatalf("upload over a folder = %v, want ErrSourceObjectExists", err)
		}
		if fi, err := os.Stat(filepath.Join(dir, upFolder, "dump.zst")); err != nil || !fi.IsDir() {
			t.Fatal("the folder at the target was changed")
		}
		noTempLeft(t, filepath.Join(dir, upFolder))
	})

	t.Run("a dangling symlink at the target is refused and left dangling", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		mustMkdir(t, filepath.Join(dir, upFolder))
		link := filepath.Join(dir, upFolder, "dump.zst")
		if err := os.Symlink("/nowhere-1191/target", link); err != nil {
			t.Fatal(err)
		}
		s := fx.begin(t, ts)
		if _, err := s.Upload(fx.src.Context(), localFile(t, payload), rel, ""); !errors.Is(err, transport.ErrSourceObjectExists) {
			t.Fatalf("upload over a dangling symlink = %v, want ErrSourceObjectExists", err)
		}
		if target, err := os.Readlink(link); err != nil || target != "/nowhere-1191/target" {
			t.Fatalf("the symlink was changed: %q, %v", target, err)
		}
		noTempLeft(t, filepath.Join(dir, upFolder))
	})

	t.Run("claiming a folder is never idempotent", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		s := fx.begin(t, ts)
		existing := transport.RecoveredFolderPrefix + "existing"
		mustMkdir(t, filepath.Join(dir, existing))
		file := transport.RecoveredFolderPrefix + "file"
		mustWrite(t, filepath.Join(dir, file), []byte("x"))
		dangling := transport.RecoveredFolderPrefix + "dangling"
		if err := os.Symlink("/nowhere-1191", filepath.Join(dir, dangling)); err != nil {
			t.Fatal(err)
		}
		toDir := transport.RecoveredFolderPrefix + "to-dir"
		if err := os.Symlink(".", filepath.Join(dir, toDir)); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{existing, file, dangling, toDir} {
			if err := s.ClaimFolder(fx.src.Context(), name, false); !errors.Is(err, transport.ErrSourceObjectExists) {
				t.Errorf("claiming %s = %v, want ErrSourceObjectExists", name, err)
			}
		}
		for _, name := range []string{file, dangling} {
			if err := s.ClaimFolder(fx.src.Context(), name, true); !errors.Is(err, transport.ErrSourceObjectExists) {
				t.Errorf("reusing %s = %v, want ErrSourceObjectExists", name, err)
			}
		}
		if err := s.ClaimFolder(fx.src.Context(), existing, true); err != nil {
			t.Errorf("reusing a real folder when asked to = %v", err)
		}
		if err := s.ClaimFolder(fx.src.Context(), upFolder, false); err != nil {
			t.Fatalf("claiming a new folder = %v", err)
		}
		if fi, err := os.Lstat(filepath.Join(dir, upFolder)); err != nil || !fi.IsDir() {
			t.Fatalf("the new folder is not a folder: %v", err)
		}
		if err := s.ClaimFolder(fx.src.Context(), upFolder, false); !errors.Is(err, transport.ErrSourceObjectExists) {
			t.Fatalf("a second claim of the same name = %v, want ErrSourceObjectExists", err)
		}
		noTempLeft(t, dir)
	})

	t.Run("a cancelled upload leaves nothing under the real name", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		mustMkdir(t, filepath.Join(dir, upFolder))
		s := fx.begin(t, ts)
		ctx, cancel := context.WithCancel(fx.src.Context())
		defer cancel()
		res := fx.slowUpload(t, ctx, s, rel, filepath.Join(dir, upFolder), func() { cancel() })
		if res == nil {
			t.Fatal("a cancelled upload reported success")
		}
		assertNothingPlaced(t, filepath.Join(dir, upFolder), res)
	})

	t.Run("a dropped connection leaves nothing under the real name", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		mustMkdir(t, filepath.Join(dir, upFolder))
		s := fx.begin(t, ts)
		res := fx.slowUpload(t, fx.src.Context(), s, rel, filepath.Join(dir, upFolder), func() { fx.dropSessions(t) })
		if res == nil {
			t.Fatal("an upload whose connection was killed reported success")
		}
		assertNothingPlaced(t, filepath.Join(dir, upFolder), res)
	})

	t.Run("no timestamp is ever set on the source", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		mustMkdir(t, filepath.Join(dir, upFolder))
		local := localFile(t, payload)
		old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
		if err := os.Chtimes(local, old, old); err != nil {
			t.Fatal(err)
		}
		s := fx.begin(t, ts)
		if _, err := s.Upload(fx.src.Context(), local, rel, ""); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		fi, err := os.Stat(filepath.Join(dir, upFolder, "dump.zst"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.ModTime().Year() == 2001 {
			t.Fatal("the file on the source carries the local file's time, so a setstat was sent")
		}
	})

	t.Run("a read-only account is refused as a permission", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o555)
		s := fx.begin(t, ts)
		err := s.ClaimFolder(fx.src.Context(), upFolder, false)
		if err == nil {
			t.Fatal("a folder was claimed in a directory the account cannot write")
		}
		if cat, ok := transport.CategoryOf(err); !ok || cat != transport.PermissionDenied {
			t.Errorf("refusal classified as (%v, %v), want PermissionDenied", cat, ok)
		}
		if names := dirNames(t, dir); len(names) != 0 {
			t.Errorf("a refused claim left %v behind", names)
		}
	})

	t.Run("two uploads to one name: one lands, the other is refused", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		mustMkdir(t, filepath.Join(dir, upFolder))
		s := fx.begin(t, ts)
		bodies := [][]byte{bytes.Repeat([]byte("A"), 1<<20), bytes.Repeat([]byte("B"), 1<<20)}
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range bodies {
			local := localFile(t, bodies[i])
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = s.Upload(fx.src.Context(), local, rel, "")
			}(i)
		}
		wg.Wait()
		won := -1
		for i, err := range errs {
			switch {
			case err == nil:
				if won >= 0 {
					t.Fatal("both uploads to one name reported success")
				}
				won = i
			case !errors.Is(err, transport.ErrSourceObjectExists):
				t.Errorf("upload %d: %v, want ErrSourceObjectExists", i, err)
			}
		}
		if won < 0 {
			t.Fatalf("neither upload landed: %v", errs)
		}
		got, _ := os.ReadFile(filepath.Join(dir, upFolder, "dump.zst"))
		if !bytes.Equal(got, bodies[won]) {
			t.Fatal("the file on the source is not the one whose upload succeeded")
		}
		noTempLeft(t, filepath.Join(dir, upFolder))
	})

	t.Run("a digest the host disagrees with is refused where the host can hash", func(t *testing.T) {
		dir, ts := fx.seed(t, 0o777)
		mustMkdir(t, filepath.Join(dir, upFolder))
		s := fx.begin(t, ts)
		v, err := s.Upload(fx.src.Context(), localFile(t, payload), upFolder+"/first.zst", digestOf(payload))
		if err != nil {
			t.Fatalf("Upload: %v", err)
		}
		if v == transport.VerifiedSize {
			t.Logf("this fixture's account cannot run a hash command, so it verified by size; the mismatch half is in the rclone package's unit tests")
			return
		}
		_, err = s.Upload(fx.src.Context(), localFile(t, payload), rel, digestOf([]byte("other")))
		if !errors.Is(err, transport.ErrSourceDigestMismatch) {
			t.Fatalf("a wrong digest = %v, want ErrSourceDigestMismatch", err)
		}
		if _, err := os.Stat(filepath.Join(dir, upFolder, "dump.zst")); !os.IsNotExist(err) {
			t.Fatal("a file that failed its digest was left on the source")
		}
	})

	t.Run("the host says how much space is free", func(t *testing.T) {
		_, ts := fx.seed(t, 0o777)
		rep, ok := any(rclone.New()).(transport.SourceSpaceReporter)
		if !ok {
			t.Fatal("the rclone adapter does not implement transport.SourceSpaceReporter")
		}
		free, known, err := rep.SourceFreeSpace(fx.src.Context(), ts)
		if err != nil || !known || free <= 0 {
			t.Fatalf("SourceFreeSpace = (%d, %v, %v), want a known positive figure from an OpenSSH server", free, known, err)
		}
	})

	t.Run("one host takes one upload session at a time", func(t *testing.T) {
		_, ts := fx.seed(t, 0o777)
		first := fx.begin(t, ts)
		ctx, cancel := context.WithTimeout(fx.src.Context(), 300*time.Millisecond)
		defer cancel()
		if second, err := fx.up.BeginSourceUpload(ctx, ts); err == nil {
			_ = second.Close()
			t.Fatal("a second session opened on a host whose slot was held")
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		second := fx.begin(t, ts)
		_ = second.Close()
	})
}

// slowUpload feeds the upload through a named pipe so it is still in flight
// when interrupt runs: the pipe is written a megabyte at a time until the
// temporary file shows up on the host with bytes in it, then interrupt is
// called, and the writer is closed once the upload has returned or
// shortly after. It returns the upload's error.
func (fx *uploadFixture) slowUpload(t *testing.T, ctx context.Context, s transport.SourceUploadSession, rel, folderDir string, interrupt func()) error {
	t.Helper()
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Upload(ctx, fifo, rel, "")
		done <- err
	}()
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	chunk := bytes.Repeat([]byte("in flight "), 100_000)
	deadline := time.Now().Add(30 * time.Second)
	var stopAt time.Time
	for time.Now().Before(deadline) && (stopAt.IsZero() || time.Now().Before(stopAt)) {
		select {
		case err := <-done:
			return err
		default:
		}
		if stopAt.IsZero() && tempHasBytes(folderDir) {
			interrupt()
			// Keep feeding for a moment, so the interruption lands in the
			// middle of a transfer rather than at a quiet pipe.
			stopAt = time.Now().Add(2 * time.Second)
		}
		_ = w.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := w.Write(chunk); err != nil && !stopAt.IsZero() && !errors.Is(err, os.ErrDeadlineExceeded) {
			break
		}
	}
	if stopAt.IsZero() {
		t.Fatal("the upload's temporary file never appeared on the host")
	}
	_ = w.Close()
	select {
	case err := <-done:
		return err
	case <-time.After(60 * time.Second):
		t.Fatal("the interrupted upload never returned")
		return nil
	}
}

func tempHasBytes(dir string) bool {
	des, _ := os.ReadDir(dir)
	for _, d := range des {
		if strings.Contains(d.Name(), transport.UploadTempMarker) {
			if fi, err := d.Info(); err == nil && fi.Size() > 0 {
				return true
			}
		}
	}
	return false
}

// dropSessions kills the fixture's per-connection sshd processes (never the
// listener), which is a dropped connection as far as the client can tell.
func (fx *uploadFixture) dropSessions(t *testing.T) {
	t.Helper()
	out := fx.src.Inside(t, "ps", "-o", "pid,args")
	var pids []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// OpenSSH 9.8 and later run each connection as sshd-session; older
		// ones retitle the forked sshd "sshd: <user>". PID 1 is the listener
		// and is never touched.
		if fields[0] != "1" && (strings.Contains(line, "sshd-session") || strings.Contains(line, "sshd: "+fx.src.User)) {
			pids = append(pids, fields[0])
		}
	}
	if len(pids) == 0 {
		t.Fatalf("no session process for %s in the fixture:\n%s", fx.src.User, out)
	}
	if out := fx.src.Inside(t, append([]string{"kill", "-9"}, pids...)...); strings.TrimSpace(out) != "" {
		t.Logf("kill said: %s", out)
	}
}

func assertNothingPlaced(t *testing.T, folderDir string, uploadErr error) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(folderDir, "dump.zst")); !os.IsNotExist(err) {
		t.Fatalf("something is under the real name after an interrupted upload (%v)", uploadErr)
	}
	var left *transport.UploadNotRemovedError
	if errors.As(uploadErr, &left) {
		t.Logf("the temporary file could not be removed and was named: %s", left.Path)
		return
	}
	noTempLeft(t, folderDir)
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o666); err != nil {
		t.Fatal(err)
	}
}
