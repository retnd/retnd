package local

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1155: the one administrator can fix their own login from a shell.
//
// SetAdminPassword is the offline half of password rotation. It takes the
// same exclusive store lock a running Service holds and refuses rather than
// waits, so a server that is up cannot have its account rewritten under it,
// and so there is never a live session to leave behind: sessions live in the
// server's memory, and a server that holds the lock is the only thing that
// can have any.

const (
	oldPassword1155 = "correct-horse-battery-staple"
	newPassword1155 = "a-brand-new-passphrase-1155"
)

func enrolled1155(t *testing.T) (path string, before *AdminRecord) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "auth.json")
	if _, err := CreateAdmin(CreateAdminConfig{StorePath: path, Username: "bm-admin", Password: oldPassword1155}); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	admin, err := NewStore(path).Admin()
	if err != nil || admin == nil {
		t.Fatalf("Admin: %v %v", admin, err)
	}
	return path, admin
}

func TestSetAdminPassword1155_RotatesTheHashAndKeepsTheAccount(t *testing.T) {
	path, before := enrolled1155(t)

	if err := SetAdminPassword(SetPasswordConfig{StorePath: path, Username: "bm-admin", Password: newPassword1155}); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}

	after, err := NewStore(path).Admin()
	if err != nil || after == nil {
		t.Fatalf("Admin after rotation: %v %v", after, err)
	}
	if after.PasswordHash == before.PasswordHash {
		t.Fatal("the password hash did not change")
	}
	if err := verifyPassword(after.PasswordHash, newPassword1155); err != nil {
		t.Errorf("the new password does not verify against the stored hash: %v", err)
	}
	if err := verifyPassword(after.PasswordHash, oldPassword1155); err == nil {
		t.Error("the OLD password still verifies after the rotation")
	}
	if after.Username != before.Username || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("the account itself changed: %+v -> %+v", before, after)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{newPassword1155, oldPassword1155} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the store file carries a plaintext password %q", secret)
		}
	}
}

func TestSetAdminPassword1155_RefusalsLeaveTheStoreUntouched(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func(path string) SetPasswordConfig
		want string
	}{
		{"a username that is not the administrator", func(p string) SetPasswordConfig {
			return SetPasswordConfig{StorePath: p, Username: "someone-else", Password: newPassword1155}
		}, "someone-else"},
		{"a password under the minimum", func(p string) SetPasswordConfig {
			return SetPasswordConfig{StorePath: p, Username: "bm-admin", Password: "short"}
		}, "at least"},
		{"no username", func(p string) SetPasswordConfig {
			return SetPasswordConfig{StorePath: p, Password: newPassword1155}
		}, "username"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, before := enrolled1155(t)
			err := SetAdminPassword(tc.cfg(path))
			if err == nil {
				t.Fatal("SetAdminPassword = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal %q does not say %q", err, tc.want)
			}
			if strings.Contains(err.Error(), newPassword1155) {
				t.Errorf("the refusal echoes the password: %v", err)
			}
			after, aerr := NewStore(path).Admin()
			if aerr != nil || after == nil || after.PasswordHash != before.PasswordHash {
				t.Errorf("the stored hash moved on a refused call: %v %v", after, aerr)
			}
		})
	}
}

func TestSetAdminPassword1155_NeedsAnAdministratorToExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	err := SetAdminPassword(SetPasswordConfig{StorePath: path, Username: "bm-admin", Password: newPassword1155})
	if !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("SetAdminPassword on an empty store = %v, want ErrNotEnrolled (create-admin is the verb that makes the first one)", err)
	}
	if admin, aerr := NewStore(path).Admin(); aerr != nil || admin != nil {
		t.Errorf("a refused rotation created an account: %v %v", admin, aerr)
	}
}

// A running server holds the store lock for its whole life. Rewriting the
// account under it would race its own read-modify-write cycles, and would
// leave its in-memory sessions signed in under a password that no longer
// exists. Refusing is what makes "revokes every session" true: the only
// process that can hold a session is the one that holds the lock.
func TestSetAdminPassword1155_RefusesWhileARunningServiceHoldsTheStore(t *testing.T) {
	path, before := enrolled1155(t)
	svc, err := New(Config{StorePath: path, SendMail: (&mailRecorder{}).send, Log: io.Discard, Notice: io.Discard})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = svc

	err = SetAdminPassword(SetPasswordConfig{StorePath: path, Username: "bm-admin", Password: newPassword1155})
	if !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("SetAdminPassword beside a running Service = %v, want ErrStoreLocked", err)
	}
	after, aerr := NewStore(path).Admin()
	if aerr != nil || after == nil || after.PasswordHash != before.PasswordHash {
		t.Errorf("the hash moved under a running server: %v %v", after, aerr)
	}
}

// Issue #1155 asked for this to be run rather than read: create-admin is
// single-shot, so it is NOT a way back in once an administrator exists, and
// the refusal must leave the original account and hash alone.
func TestCreateAdmin1155_RefusesOnceAnAdministratorExistsAndChangesNothing(t *testing.T) {
	path, before := enrolled1155(t)
	_, err := CreateAdmin(CreateAdminConfig{StorePath: path, Username: "bm-admin", Password: newPassword1155})
	if !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("CreateAdmin over an existing administrator = %v, want ErrAlreadyEnrolled", err)
	}
	after, aerr := NewStore(path).Admin()
	if aerr != nil || after == nil || after.PasswordHash != before.PasswordHash {
		t.Errorf("a refused create-admin changed the stored hash: %v %v", after, aerr)
	}
}
