package recoverytest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

func TestLocalRecovererStagesAPrivateByteIdenticalCopyAndRemovesIt(t *testing.T) {
	src := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r := LocalRecoverer{Root: root}
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
