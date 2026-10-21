package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// The review of #1254's first pass (PR #1258), each finding as the
// behaviour it asks for. firstrunroute_1254_test.go has the fixture.

// withFirstRunTimeouts shortens the two bounds the first-run route runs
// under for one test, and puts them back afterwards.
func withFirstRunTimeouts(t *testing.T, attach, complete time.Duration) {
	t.Helper()
	oldAttach, oldComplete := firstRunAttachTimeout, firstRunCompleteTimeout
	if attach > 0 {
		firstRunAttachTimeout = attach
	}
	if complete > 0 {
		firstRunCompleteTimeout = complete
	}
	t.Cleanup(func() { firstRunAttachTimeout, firstRunCompleteTimeout = oldAttach, oldComplete })
}

// TestFirstRun1254Review_TheClaimIsNotHeldWhileTheEngineIsAsked is M1.
//
// The journal's startup claim is what an engine's activation needs to start
// serving a first configuration, and it waits two seconds for it. A command
// that signs in and asks its questions while still holding that claim turns
// any activation happening meanwhile (the wizard submitting, a second
// terminal) into restart_required. So while the command is asking, a
// competing activation over the same journal has to get the lock.
func TestFirstRun1254Review_TheClaimIsNotHeldWhileTheEngineIsAsked(t *testing.T) {
	engine, cliConfig, dbPath := firstRunFixture(t)
	engine.attach(t)

	// A configuration naming the same journal, so a real service.Open over
	// it takes the same startup lock an engine's activation takes.
	competing := filepath.Join(t.TempDir(), "config.yaml")
	raw, err := service.DefaultConfigYAML(service.FirstRunDefaults{ConfigPath: competing, StateDatabase: dbPath})
	if err != nil {
		t.Fatalf("DefaultConfigYAML: %v", err)
	}
	if err := os.WriteFile(competing, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var once sync.Once
	var activationErr error
	answer := engine.systemVersion
	engine.handle("GET /system/version", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			_, closeFn, err := service.Open(r.Context(), competing)
			activationErr = err
			if err == nil {
				_ = closeFn()
			}
		})
		answer(w, r)
	})

	code, stdout, stderr := runCLI(t, createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath)...)
	if activationErr != nil {
		t.Errorf("an activation over this journal could not start while the command was asking the engine about itself, so a wizard submitting at that moment ends restart_required: %v", activationErr)
	}
	if code != exitOK {
		t.Errorf("the create exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}

// TestFirstRun1254Review_TheEngineIsAskedOnce is M1's other half: one
// GET /system/version answers both "is it configured" and "which deployment
// is it", so the two can never come from different answers.
func TestFirstRun1254Review_TheEngineIsAskedOnce(t *testing.T) {
	engine, cliConfig, dbPath := firstRunFixture(t)
	engine.attach(t)

	code, stdout, stderr := runCLI(t, createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath)...)
	if code != exitOK {
		t.Fatalf("the create exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	asked := 0
	for _, seen := range engine.requests() {
		if seen == "GET /system/version" {
			asked++
		}
	}
	if asked != 1 {
		t.Errorf("the command asked GET /system/version %d times, want once: %v", asked, engine.requests())
	}
}

// TestFirstRun1254Review_AnEngineThatDoesNotAnswerIsGivenUpOnQuickly is
// M1's deadline. The attach is a few reads; an engine that sits on them is
// reported as not answering within the attach bound, not after apiclient's
// thirty seconds per request.
func TestFirstRun1254Review_AnEngineThatDoesNotAnswerIsGivenUpOnQuickly(t *testing.T) {
	withFirstRunTimeouts(t, 300*time.Millisecond, 0)
	engine, cliConfig, dbPath := firstRunFixture(t)
	engine.attach(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	engine.handle("GET /system/version", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})

	start := time.Now()
	code, _, stderr := runCLI(t, createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath)...)
	elapsed := time.Since(start)
	if code == exitOK {
		t.Fatalf("a create beside an engine that never answered exited 0\nstderr: %s", stderr)
	}
	if elapsed > 3*time.Second {
		t.Errorf("the command waited %s on an engine that was not answering, past its attach bound", elapsed)
	}
	if _, err := os.Stat(cliConfig); !os.IsNotExist(err) {
		t.Errorf("a create that could not reach the engine wrote %s itself (stat err = %v)", cliConfig, err)
	}
}

// TestFirstRun1254Review_ASlowFirstRunIsReportedAsWhatHappened is M2. The
// engine's handler can take minutes (a connection test, then an activation
// under a ten minute bound), and a reply that does not arrive in time is
// not "no engine answered". The command asks again and says what is true.
func TestFirstRun1254Review_ASlowFirstRunIsReportedAsWhatHappened(t *testing.T) {
	t.Run("the engine wrote it", func(t *testing.T) {
		withFirstRunTimeouts(t, 0, 300*time.Millisecond)
		engine, cliConfig, dbPath := firstRunFixture(t)
		engine.attach(t)
		write := engine.completeFirstRun
		engine.handle("POST /system/first-run", func(w http.ResponseWriter, r *http.Request) {
			// Did the work, and answers after the command stopped waiting.
			rec := httptest.NewRecorder()
			write(rec, r)
			time.Sleep(time.Second)
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
		})

		code, stdout, stderr := runCLI(t, createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath)...)
		if code != exitOK {
			t.Errorf("a first run the engine carried out exited %d because its reply was slow\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		if strings.Contains(stdout+stderr, "no engine answered") {
			t.Errorf("a slow reply was reported as no engine answering:\nstdout: %s\nstderr: %s", stdout, stderr)
		}
		if !strings.Contains(stdout, "is configured now") {
			t.Errorf("the output does not say the engine is configured now:\n%s", stdout)
		}
	})
	t.Run("nothing was written", func(t *testing.T) {
		withFirstRunTimeouts(t, 0, 300*time.Millisecond)
		engine, cliConfig, dbPath := firstRunFixture(t)
		engine.attach(t)
		engine.handle("POST /system/first-run", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(time.Second)
			refuse(w, http.StatusInternalServerError, apicontract.ErrorCodeInternal, "too late")
		})

		code, stdout, stderr := runCLI(t, createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath)...)
		if code == exitOK {
			t.Fatalf("a first run nothing came of exited 0\nstdout: %s", stdout)
		}
		if strings.Contains(stderr, "no engine answered") {
			t.Errorf("a slow reply was reported as no engine answering:\n%s", stderr)
		}
		if !strings.Contains(stderr, "still has no configuration, so nothing from this command was written") {
			t.Errorf("the refusal does not say that nothing was written:\n%s", stderr)
		}
	})
}

// TestFirstRun1254Review_RunIsRefusedInTheWordsOfItsPath is M4. Beside a
// first-run engine there IS a running service, so "there is no running
// service" and "then `retnd run`" (a cycle in this process, not in the
// engine) are both false there. The direct path keeps its sentence.
func TestFirstRun1254Review_RunIsRefusedInTheWordsOfItsPath(t *testing.T) {
	t.Run("routed", func(t *testing.T) {
		engine, cliConfig, dbPath := firstRunFixture(t)
		engine.attach(t)
		code, _, stderr := runCLI(t, createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath, "--run")...)
		if code != exitUsage {
			t.Errorf("--run on a routed first run exited %d, want %d\nstderr: %s", code, exitUsage, stderr)
		}
		if strings.Contains(stderr, "no running service") || strings.Contains(stderr, "`retnd run`") {
			t.Errorf("the --run refusal beside a first-run engine says there is no running service, or sends the operator to a cycle in their own process:\n%s", stderr)
		}
		if !strings.Contains(stderr, "POST /system/first-run carries no run") {
			t.Errorf("the --run refusal does not say why the routed first run cannot carry it:\n%s", stderr)
		}
		if engine.askedFor("POST /system/first-run") {
			t.Errorf("a refused --run still handed the first configuration over: %v", engine.requests())
		}
	})
	t.Run("direct", func(t *testing.T) {
		dir := t.TempDir()
		code, _, stderr := runCLI(t, createArgs(filepath.Join(dir, "config.yaml"), writeTestPrivateKey(t), "api/postgres",
			"--state-database", filepath.Join(dir, "state.db"), "--run")...)
		if code != exitUsage {
			t.Errorf("--run on a direct first run exited %d, want %d\nstderr: %s", code, exitUsage, stderr)
		}
		if !strings.Contains(stderr, "`retnd run`") {
			t.Errorf("the direct --run refusal no longer names `retnd run`:\n%s", stderr)
		}
	})
}

// TestFirstRun1254Review_SomebodyElseFinishingFirstIsSaidForATerminal is
// M5: the engine's 409 is written for a browser ("reload and sign in"); a
// terminal needs to hear that its own set was not written and where to look.
func TestFirstRun1254Review_SomebodyElseFinishingFirstIsSaidForATerminal(t *testing.T) {
	engine, cliConfig, dbPath := firstRunFixture(t)
	engine.attach(t)
	engine.handle("POST /system/first-run", func(w http.ResponseWriter, _ *http.Request) {
		refuse(w, http.StatusConflict, apicontract.ErrorCodeAlreadyConfigured,
			"this instance was configured while your setup was in progress; reload and sign in")
	})

	code, _, stderr := runCLI(t, createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath)...)
	if code == exitOK {
		t.Fatalf("a first run somebody else finished first exited 0\nstderr: %s", stderr)
	}
	if strings.Contains(stderr, "reload and sign in") {
		t.Errorf("the terminal was handed the browser's instruction:\n%s", stderr)
	}
	for _, want := range []string{"was configured by someone else while this ran", "nothing from this command was written", "`retnd sources`"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, stderr)
		}
	}
}

// TestFirstRun1254Review_UsageNoLongerSaysTheFirstConfigurationHasNoRoute
// is M3. The mode paragraph is what a headless operator reads to decide
// whether to set RETND_API_URL, and it told them not to bother.
func TestFirstRun1254Review_UsageNoLongerSaysTheFirstConfigurationHasNoRoute(t *testing.T) {
	out := strings.Join(strings.Fields(captureStderr(t, usage)), " ")
	if strings.Contains(out, "One write has no route at all") {
		t.Error("usage() still says the first configuration has no route at all")
	}
	if !strings.Contains(out, "routed only to an engine still on its first run") {
		t.Error("usage() does not say where the first configuration is routed")
	}
}

// TestFirstRun1254Review_SSHKeyVerbsReachAFirstRunEngine is O10. A
// headless first run needs the key step as much as the create, and the
// key store that matters is the engine's.
func TestFirstRun1254Review_SSHKeyVerbsReachAFirstRunEngine(t *testing.T) {
	engine, cliConfig, dbPath := firstRunFixture(t)
	engine.attach(t)
	t.Setenv(service.StateDatabaseEnv, dbPath)

	code, stdout, stderr := runCLI(t, []string{"ssh-key", "import", "--config", cliConfig, "--file", writeTestPrivateKey(t)}...)
	if code != exitOK {
		t.Fatalf("ssh-key import beside a first-run engine exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !engine.askedFor("POST /ssh-keys") {
		t.Errorf("the key never reached the first-run engine's store: %v", engine.requests())
	}

	code, stdout, stderr = runCLI(t, []string{"ssh-key", "list", "--config", cliConfig}...)
	if code != exitOK {
		t.Fatalf("ssh-key list beside a first-run engine exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !engine.askedFor("GET /ssh-keys") {
		t.Errorf("ssh-key list read this host's own store instead of the engine's: %v", engine.requests())
	}
	if !strings.Contains(stdout, "ssh-ed25519") {
		t.Errorf("the key imported a moment ago is not in the listing:\n%s", stdout)
	}

	code, stdout, stderr = runCLI(t, []string{"ssh-key", "discover", "--config", cliConfig}...)
	if code != exitOK {
		t.Fatalf("ssh-key discover beside a first-run engine exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !engine.askedFor("GET /ssh/key-candidates") {
		t.Errorf("ssh-key discover scanned this host instead of asking the engine: %v", engine.requests())
	}
	if _, err := os.Stat(cliConfig); !os.IsNotExist(err) {
		t.Errorf("an ssh-key verb wrote %s (stat err = %v)", cliConfig, err)
	}
}

// TestFirstRun1254Review_SSHKeyImportWithNoRouteSaysHowToGetOne is O10's
// refusal: beside a first-run engine with no address, an import still has
// nowhere local to go, and the sentence now names the address it needs.
func TestFirstRun1254Review_SSHKeyImportWithNoRouteSaysHowToGetOne(t *testing.T) {
	_, cliConfig, dbPath := firstRunFixture(t)
	t.Setenv(service.StateDatabaseEnv, dbPath)

	code, _, stderr := runCLI(t, []string{"ssh-key", "import", "--config", cliConfig, "--file", writeTestPrivateKey(t)}...)
	if code == exitOK {
		t.Fatalf("an import with no route and no configuration exited 0\nstderr: %s", stderr)
	}
	if !strings.Contains(stderr, "RETND_API_URL") {
		t.Errorf("the refusal beside a first-run engine does not say how to reach it:\n%s", stderr)
	}
}
