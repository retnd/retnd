package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/auth/local"
)

// Issue #1155: `retnd-web auth set-password`, the terminal way back into an
// account when the browser cannot be used. The password comes from stdin and
// from nowhere else, and it is written to no output stream.

const (
	oldPW1155 = "correct-horse-battery-staple"
	newPW1155 = "a-brand-new-passphrase-1155"
)

// captureBoth drains stdout and stderr while fn runs, so output larger than
// a pipe's buffer (usage() is) cannot block the command it belongs to.
func captureBoth1155(t *testing.T, fn func()) (string, string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	ro, wo, _ := os.Pipe()
	re, we, _ := os.Pipe()
	os.Stdout, os.Stderr = wo, we
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr })
	outC, errC := make(chan string, 1), make(chan string, 1)
	go func() { b, _ := io.ReadAll(ro); outC <- string(b) }()
	go func() { b, _ := io.ReadAll(re); errC <- string(b) }()
	fn()
	_ = wo.Close()
	_ = we.Close()
	os.Stdout, os.Stderr = origOut, origErr
	return <-outC, <-errC
}

func enrolledStore1155(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if _, err := local.CreateAdmin(local.CreateAdminConfig{StorePath: path, Username: "bm-admin", Password: oldPW1155}); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	admin, err := local.NewStore(path).Admin()
	if err != nil || admin == nil {
		t.Fatalf("Admin: %v %v", admin, err)
	}
	return path, admin.PasswordHash
}

func hashOf1155(t *testing.T, path string) string {
	t.Helper()
	admin, err := local.NewStore(path).Admin()
	if err != nil || admin == nil {
		t.Fatalf("Admin: %v %v", admin, err)
	}
	return admin.PasswordHash
}

func TestAuthSetPassword1155_ReadsTheNewPasswordFromStdinAndRotatesIt(t *testing.T) {
	path, before := enrolledStore1155(t)
	withStdin(t, newPW1155+"\n")

	var code int
	stdout, stderr := captureBoth1155(t, func() {
		code = run([]string{"auth", "set-password", "--username", "bm-admin", "--password-stdin", "--auth-store", path})
	})
	if code != exitOK {
		t.Fatalf("auth set-password exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if hashOf1155(t, path) == before {
		t.Error("the stored hash did not change")
	}
	for _, stream := range []string{stdout, stderr} {
		if strings.Contains(stream, newPW1155) || strings.Contains(stream, oldPW1155) {
			t.Errorf("a password reached an output stream: %q", stream)
		}
	}
	if !strings.Contains(stdout, "bm-admin") {
		t.Errorf("the confirmation does not name the account: %q", stdout)
	}
}

func TestAuthSetPassword1155_NeverTakesThePasswordAsAFlag(t *testing.T) {
	path, before := enrolledStore1155(t)
	for _, argv := range [][]string{
		{"auth", "set-password", "--username", "bm-admin", "--auth-store", path},
		{"auth", "set-password", "--username", "bm-admin", "--password", newPW1155, "--auth-store", path},
		{"auth", "set-password", "--password-stdin", "--auth-store", path},
	} {
		var code int
		_, stderr := captureBoth1155(t, func() { code = run(argv) })
		if code != exitUsage {
			t.Errorf("%v exited %d, want the usage code %d", argv[:2], code, exitUsage)
		}
		if strings.Contains(stderr, newPW1155) {
			t.Errorf("a usage error echoed the password: %q", stderr)
		}
	}
	if hashOf1155(t, path) != before {
		t.Error("a usage error changed the stored hash")
	}
}

func TestAuthSetPassword1155_RefusesTheWrongAccountAndAnEmptyPipe(t *testing.T) {
	path, before := enrolledStore1155(t)

	withStdin(t, newPW1155+"\n")
	var code int
	_, stderr := captureBoth1155(t, func() {
		code = run([]string{"auth", "set-password", "--username", "someone-else", "--password-stdin", "--auth-store", path})
	})
	if code == exitOK || code == exitUsage {
		t.Errorf("a wrong username exited %d, want a failure that is not a usage error", code)
	}
	if strings.Contains(stderr, newPW1155) {
		t.Errorf("the refusal echoed the password: %q", stderr)
	}

	withStdin(t, "")
	code = -1
	captureBoth1155(t, func() {
		code = run([]string{"auth", "set-password", "--username", "bm-admin", "--password-stdin", "--auth-store", path})
	})
	if code == exitOK {
		t.Error("an empty stdin was accepted as a password")
	}
	if hashOf1155(t, path) != before {
		t.Error("a refused set-password changed the stored hash")
	}
}

func TestAuthSetPassword1155_RefusesWhileAServerHoldsTheStore(t *testing.T) {
	path, before := enrolledStore1155(t)
	svc, err := local.New(local.Config{StorePath: path, Log: io.Discard, Notice: io.Discard})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	_ = svc

	withStdin(t, newPW1155+"\n")
	var code int
	_, stderr := captureBoth1155(t, func() {
		code = run([]string{"auth", "set-password", "--username", "bm-admin", "--password-stdin", "--auth-store", path})
	})
	if code == exitOK {
		t.Fatal("set-password succeeded against a store a running server holds")
	}
	if !strings.Contains(stderr, "stop") {
		t.Errorf("the refusal does not tell the operator to stop the server: %q", stderr)
	}
	if hashOf1155(t, path) != before {
		t.Error("the hash moved under a running server")
	}
}

// create-admin is not a way back in. This is the run the issue asked for.
func TestAuthCreateAdmin1155_RefusesOnceAnAdministratorExists(t *testing.T) {
	path, before := enrolledStore1155(t)
	withStdin(t, newPW1155+"\n")
	var code int
	_, stderr := captureBoth1155(t, func() {
		code = run([]string{"auth", "create-admin", "--username", "bm-admin", "--password-stdin", "--auth-store", path})
	})
	if code == exitOK {
		t.Fatal("create-admin overwrote an existing administrator")
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("the refusal does not say an account already exists: %q", stderr)
	}
	if !strings.Contains(stderr, "set-password") {
		t.Errorf("the refusal does not point at set-password, the way back in: %q", stderr)
	}
	if hashOf1155(t, path) != before {
		t.Error("a refused create-admin changed the stored hash")
	}
}

func TestUsage1155_NamesSetPassword(t *testing.T) {
	_, stderr := captureBoth1155(t, func() { _ = run(nil) })
	if !strings.Contains(stderr, "auth set-password --username U --password-stdin") {
		t.Errorf("usage() does not list auth set-password:\n%s", stderr)
	}
}
