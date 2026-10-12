package recoverytest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

func roomy(string) (uint64, error) { return 1 << 50, nil }

func TestLocalRecovererStagesAPrivateByteIdenticalCopyAndRemovesIt(t *testing.T) {
	src := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r := LocalRecoverer{Root: root, Available: roomy}
	s, err := r.Recover(context.Background(), RecoverRequest{Copy: state.Placement{Medium: state.MediumLocal, Location: src}})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got, err := os.ReadFile(s.Path)
	if err != nil || string(got) != string(content) {
		t.Fatalf("staged bytes = %q, %v", got, err)
	}
	if !strings.HasPrefix(s.Path, root) || s.Path == src {
		t.Errorf("staged at %q, outside the root or over the source", s.Path)
	}
	fi, _ := os.Stat(s.Path)
	di, _ := os.Stat(filepath.Dir(s.Path))
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Errorf("modes = %v / %v, want 0600 file in a 0700 directory", fi.Mode().Perm(), di.Mode().Perm())
	}
	if err := r.Remove(context.Background(), s); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(s.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staging directory survived Remove: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("Remove touched the source: %v", err)
	}
}

func TestLocalRecovererRefusesAStorageMediumCopy(t *testing.T) {
	_, err := LocalRecoverer{Root: t.TempDir()}.Recover(context.Background(), RecoverRequest{
		Artifact: model.ArtifactID{}, Copy: state.Placement{Medium: "offsite", Location: "k"}})
	if !errors.Is(err, ErrUnsupportedCopy) {
		t.Errorf("err = %v, want ErrUnsupportedCopy", err)
	}
}

func TestLocalRecovererWillNotRemoveAnythingItDidNotCreate(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "precious")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "artifact"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := LocalRecoverer{Root: root}
	for _, p := range []string{
		filepath.Join(victim, "artifact"),                                    // right shape, wrong directory name
		filepath.Join(root, stagingPrefix+"x", "..", "precious", "artifact"), // traversal
		filepath.Join(root, "artifact"),                                      // the root itself
		filepath.Join(os.TempDir(), "artifact"),
	} {
		if err := r.Remove(context.Background(), Staged{Path: p}); err == nil {
			t.Errorf("Remove(%q) succeeded", p)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the victim directory was removed: %v", err)
	}
}

func TestLocalRecovererErrorsCarryNoSourcePath(t *testing.T) {
	_, err := LocalRecoverer{Root: t.TempDir()}.Recover(context.Background(), RecoverRequest{
		Copy: state.Placement{Medium: state.MediumLocal, Location: "/very/secret/place/dump.sql"}})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v: a stored reason must not carry the artifact's real path", err)
	}
}

// --- found by the review of this diff ---

// A recovery test copies a whole artifact, and a disk that fills because of
// it is a worse outcome than a test that did not run. The copy is refused
// before a byte is written unless the artifact fits with the reserve to spare.
func TestLocalRecovererRefusesACopyThatDoesNotFitWithTheReserve(t *testing.T) {
	src := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	size := int64(len(content))
	root := t.TempDir()
	for name, tc := range map[string]struct {
		available uint64
		wantErr   bool
	}{
		"plenty":                   {available: uint64(size) + DefaultStagingReserve + 1, wantErr: false},
		"exactly the reserve over": {available: uint64(size) + DefaultStagingReserve, wantErr: false},
		"one byte inside reserve":  {available: uint64(size) + DefaultStagingReserve - 1, wantErr: true},
		"less than the artifact":   {available: uint64(size) - 1, wantErr: true},
		"nothing":                  {available: 0, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := LocalRecoverer{Root: root, Available: func(string) (uint64, error) { return tc.available, nil }}
			s, err := r.Recover(context.Background(), RecoverRequest{Copy: state.Placement{Medium: state.MediumLocal, Location: src, Size: &size}})
			if tc.wantErr {
				if err == nil {
					t.Fatal("a copy that does not fit was staged")
				}
				entries, _ := os.ReadDir(root)
				if len(entries) != 0 {
					t.Errorf("%d entries left behind by a refused copy", len(entries))
				}
				return
			}
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			_ = r.Remove(context.Background(), s)
		})
	}
}

func TestLocalRecovererRefusesWhenTheStagingFilesystemCannotBeMeasured(t *testing.T) {
	src := filepath.Join(t.TempDir(), "dump.sql")
	_ = os.WriteFile(src, content, 0o644)
	size := int64(len(content))
	r := LocalRecoverer{Root: t.TempDir(), Available: func(string) (uint64, error) { return 0, errors.New("statfs failed") }}
	if _, err := r.Recover(context.Background(), RecoverRequest{Copy: state.Placement{Medium: state.MediumLocal, Location: src, Size: &size}}); err == nil {
		t.Error("a copy was staged on a filesystem nobody could measure")
	}
}

func TestLocalRecovererWithoutARecordedSizeUsesTheFileItself(t *testing.T) {
	src := filepath.Join(t.TempDir(), "dump.sql")
	_ = os.WriteFile(src, content, 0o644)
	r := LocalRecoverer{Root: t.TempDir(), Available: func(string) (uint64, error) { return 10, nil }}
	if _, err := r.Recover(context.Background(), RecoverRequest{Copy: state.Placement{Medium: state.MediumLocal, Location: src}}); err == nil {
		t.Error("a copy with no recorded size was staged on a 10 byte filesystem: the size of the file is what has to fit")
	}
}

// A crash between staging a copy and removing it leaves a directory nothing
// would ever look at again. The sweep removes what this recoverer created and
// has outlived the age it is given, and nothing else.
func TestSweepRemovesOnlyOldStagingDirectoriesThisRecovererCreated(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	mk := func(name string, mtime time.Time) string {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "artifact"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(d, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return d
	}
	stale := mk(stagingPrefix+"stale", old)
	fresh := mk(stagingPrefix+"fresh", time.Now())
	foreign := mk("somebody-elses-directory", old)
	file := filepath.Join(root, stagingPrefix+"a-file-not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(file, old, old)

	n, err := LocalRecoverer{Root: root}.Sweep(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("Sweep removed %d, want 1", n)
	}
	for path, wantGone := range map[string]bool{stale: true, fresh: false, foreign: false, file: false} {
		_, err := os.Stat(path)
		gone := errors.Is(err, os.ErrNotExist)
		if gone != wantGone {
			t.Errorf("%s: gone=%v, want %v", filepath.Base(path), gone, wantGone)
		}
	}
}

func TestSweepOfAMissingRootIsNothingToDo(t *testing.T) {
	n, err := LocalRecoverer{Root: filepath.Join(t.TempDir(), "never-created")}.Sweep(context.Background(), time.Hour)
	if err != nil || n != 0 {
		t.Errorf("Sweep = %d, %v", n, err)
	}
}

// C11: opening a copy path that has been made into a fifo must not block the
// tick for ever, and a symlink is not followed to something else.
func TestLocalRecovererRefusesAFifoWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "copy.dump")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no fifos here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := LocalRecoverer{Root: t.TempDir(), Available: func(string) (uint64, error) { return 1 << 40, nil }}.Recover(
			context.Background(), RecoverRequest{Copy: state.Placement{Medium: state.MediumLocal, Location: fifo}})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a fifo was staged as a copy")
		}
	case <-time.After(3 * time.Second):
		// Let the goroutine go: a writer arriving releases the open.
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		t.Fatal("Recover blocked on a fifo: one such path wedges every later tick")
	}
}

func TestLocalRecovererDoesNotFollowASymlinkAtTheCopyPath(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("not the artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "copy.dump")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	_, err := LocalRecoverer{Root: t.TempDir(), Available: func(string) (uint64, error) { return 1 << 40, nil }}.Recover(
		context.Background(), RecoverRequest{Copy: state.Placement{Medium: state.MediumLocal, Location: link}})
	if err == nil {
		t.Fatal("a symlink at the copy path was followed and staged")
	}
}
