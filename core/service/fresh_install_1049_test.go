package service

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// captureOpenStdout runs fn with os.Stdout redirected into a pipe and
// returns everything written there. Open builds its logger from
// os.Stdout at call time (service.go), so swapping the descriptor before
// the call is enough to read what a container's `docker logs` would show.
func captureOpenStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	fn()

	_ = w.Close()
	os.Stdout = orig
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}

// TestOpen_AFreshInstallIsNotLoggedAsAnError pins the engine side of the
// first-run invariant (retnd#1049): a deployment that has not been
// configured yet is a normal state, and a clean first start must not
// write an ERROR line about it.
//
// Today Open logs every OpenConfigAndJournal failure at ERROR before
// returning it (service.go, `logger.Error(ctx, "startup", err)`), and
// ErrConfigAbsent goes through that same line. The provider app then
// reads the error correctly and serves the setup flow, so the process is
// healthy, and the log says
//
//	{"level":"ERROR","event":"error","op":"startup","error":"service: no configuration file at ..."}
//
// on every start of every fresh install. An operator reading
// `docker logs` after installing sees the product report its own
// expected state as a failure.
func TestOpen_AFreshInstallIsNotLoggedAsAnError(t *testing.T) {
	configPath, _ := emptyInstall(t)

	out := captureOpenStdout(t, func() {
		svc, cleanup, err := Open(context.Background(), configPath)
		if err == nil {
			// Once the engine writes its own defaults on first start,
			// Open may well succeed here. That is fine for this test, which
			// is about the log line and not about the error; the service
			// is closed so the journal lock is released.
			_ = svc
			_ = cleanup()
		}
	})

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `"level":"ERROR"`) {
			t.Errorf("a clean first start logged at ERROR about the absent configuration; an unconfigured install is a normal state, not a failure:\n%s", line)
		}
	}
}
