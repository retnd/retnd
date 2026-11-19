package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #1163: a restore on a local-account profile asks for the
// administrator's password again. The check is the same one password rotation
// uses (the stored hash), it is rate limited on its own, and it says only yes
// or no.

const stepUpPassword1163 = "correct-horse-battery-staple"

func stepUpService(t *testing.T, limit int) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if _, err := CreateAdmin(CreateAdminConfig{StorePath: path, Username: "bm-admin", Password: stepUpPassword1163}); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	svc, err := New(Config{StorePath: path, StepUpRateLimit: limit, Log: os.Stderr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc, path
}

func TestVerifyStepUp1163_AcceptsTheAdministratorsPasswordOnly(t *testing.T) {
	svc, _ := stepUpService(t, 0)
	ctx := context.Background()
	if err := svc.VerifyStepUp(ctx, "bm-admin", stepUpPassword1163); err != nil {
		t.Fatalf("the right password was refused: %v", err)
	}
	for _, tc := range []struct{ name, user, pass string }{
		{"wrong password", "bm-admin", "not the password"},
		{"empty password", "bm-admin", ""},
		{"the password with a space on it", "bm-admin", stepUpPassword1163 + " "},
		{"a different user, right password", "someone-else", stepUpPassword1163},
		{"no user", "", stepUpPassword1163},
	} {
		err := svc.VerifyStepUp(ctx, tc.user, tc.pass)
		if !errors.Is(err, ErrStepUpRefused) {
			t.Errorf("%s: err = %v, want ErrStepUpRefused", tc.name, err)
		}
	}
}

func TestVerifyStepUp1163_IsRateLimitedAndTheLimitHoldsForTheRightPasswordToo(t *testing.T) {
	svc, _ := stepUpService(t, 3)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = svc.VerifyStepUp(ctx, "bm-admin", "wrong")
	}
	err := svc.VerifyStepUp(ctx, "bm-admin", stepUpPassword1163)
	if !errors.Is(err, ErrStepUpRateLimited) {
		t.Errorf("after the limit, the right password gave %v, want ErrStepUpRateLimited: a limiter that lets the correct guess through is no limiter", err)
	}
}

func TestVerifyStepUp1163_DoesNotEndTheSessionOrChangeTheStore(t *testing.T) {
	svc, path := stepUpService(t, 0)
	before, _ := os.ReadFile(path)
	token, _, err := svc.sessions.create("bm-admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.VerifyStepUp(context.Background(), "bm-admin", stepUpPassword1163); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.sessions.lookup(token); !ok {
		t.Error("a successful step-up ended the session")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a step-up rewrote the store")
	}
	if strings.Contains(string(after), stepUpPassword1163) {
		t.Error("the store carries the plaintext password")
	}
}

func TestVerifyStepUp1163_ErrorsNeverEchoThePassword(t *testing.T) {
	svc, _ := stepUpService(t, 1)
	for i := 0; i < 2; i++ {
		err := svc.VerifyStepUp(context.Background(), "bm-admin", "a-guess-1163-xyz")
		if err != nil && strings.Contains(err.Error(), "a-guess-1163-xyz") {
			t.Errorf("the error echoes the password: %v", err)
		}
	}
}

// A tripped limiter says how long until the next check is allowed, so the web
// interface can answer 429 with Retry-After instead of "wrong password".
func TestVerifyStepUp1163_ATrippedLimiterSaysHowLongToWait(t *testing.T) {
	svc, _ := stepUpService(t, 2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_ = svc.VerifyStepUp(ctx, "bm-admin", "wrong")
	}
	err := svc.VerifyStepUp(ctx, "bm-admin", stepUpPassword1163)
	if !errors.Is(err, ErrStepUpRateLimited) {
		t.Fatalf("err = %v, want ErrStepUpRateLimited", err)
	}
	var wait interface{ RetryAfter() time.Duration }
	if !errors.As(err, &wait) {
		t.Fatalf("the rate-limit error %T does not say how long to wait", err)
	}
	if d := wait.RetryAfter(); d <= 0 || d > time.Minute {
		t.Errorf("RetryAfter = %v, want within the one-minute window", d)
	}
}
