package sftpintegration_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
	"github.com/retnd/retnd/core/tests/machines"
)

// Issue #1191's transport half, against the throw-away SFTP container this
// package already starts (never a real host): a recovered file goes back
// onto the source into a new folder, an existing file is never replaced, and
// a read-only account is refused as a permission, not as a broken host.
func TestSourceUpload1191_AgainstARealServer(t *testing.T) {
	m := machines.Start(t)
	src := m.Source(t)
	writableDir, readOnlyDir := probeDirs(t, src)

	up, ok := any(rclone.New()).(transport.SourceUploader)
	if !ok {
		t.Fatal("the rclone adapter does not implement transport.SourceUploader")
	}
	payload := bytes.Repeat([]byte("recovered "), 4096)
	local := filepath.Join(t.TempDir(), "dump.zst")
	if err := os.WriteFile(local, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	rel := transport.RecoveredFolderPrefix + "20261015-101500/dump.zst"

	t.Run("it lands in a new folder under the set's remote path", func(t *testing.T) {
		if err := up.UploadToSource(src.Context(), src.TransportSource("upload-set", writableDir), local, rel); err != nil {
			t.Fatalf("UploadToSource: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(src.UploadDir, writableDir, filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("the file on the source is not the recovered one: %v", err)
		}
	})

	t.Run("an existing file is never replaced", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other")
		if err := os.WriteFile(other, []byte("not the same bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := up.UploadToSource(src.Context(), src.TransportSource("upload-set", writableDir), other, rel)
		if !errors.Is(err, transport.ErrSourceObjectExists) {
			t.Fatalf("a second upload to one path = %v, want ErrSourceObjectExists", err)
		}
		got, _ := os.ReadFile(filepath.Join(src.UploadDir, writableDir, filepath.FromSlash(rel)))
		if !bytes.Equal(got, payload) {
			t.Fatal("the file on the source was replaced")
		}
	})

	t.Run("a read-only account is refused as a permission", func(t *testing.T) {
		err := up.UploadToSource(src.Context(), src.TransportSource("upload-readonly", readOnlyDir), local, rel)
		if err == nil {
			t.Fatal("an upload into a directory the account cannot write succeeded")
		}
		if cat, ok := transport.CategoryOf(err); !ok || cat != transport.PermissionDenied {
			t.Errorf("refusal classified as (%v, %v), want PermissionDenied", cat, ok)
		}
		if names := probeDirEntries(t, filepath.Join(src.UploadDir, readOnlyDir)); len(names) != 0 {
			t.Errorf("a refused upload left %v behind", names)
		}
	})
}
