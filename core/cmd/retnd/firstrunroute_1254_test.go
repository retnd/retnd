package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/apiclient"
	"github.com/retnd/retnd/core/service"
)

// Issue #1254: a headless install has to be able to finish its first run
// from a terminal while the first-run engine is serving.
//
// The container a headless install runs is the engine serving the setup
// flow, so stopping that process to let `backup-set create` write the first
// config.yaml stops the container the command was typed in. Nobody is in
// front of the wizard either. What is left is handing the first
// configuration to the engine the same way the wizard does, through POST
// /system/first-run, and that is what these tests hold the CLI to.
//
// The configured side is held exactly where it was: a process that is past
// its first run never takes that request, so a first configuration typed
// beside one is still refused with nothing sent and nothing written.

// fakeFirstRunEngine is an engine still on its first-run setup flow: no
// configuration, a real core/service.FirstRun behind POST /system/first-run,
// and the announcement a real one makes about the journal its packaging
// names (core/service's AnnounceServingFirstRun).
//
// It reuses fakeEngine for the session and the double-submit checks and
// scripts every route a first-run instance serves, because the routes a
// configured fakeEngine answers are exactly the ones an unconfigured one
// does not (apps/common/webhost's newUnconfiguredRouter).
type fakeFirstRunEngine struct {
	*fakeEngine

	firstRun *service.FirstRun
	serving  *service.FirstRunServing
	dbPath   string

	mu sync.Mutex
	// activated is the BackupService this engine brought up over the
	// configuration POST /system/first-run wrote, the way retnd-web's
	// Activate does. Nil until then, and still nil when activation failed,
	// which is the restart_required answer.
	activated       *service.BackupService
	closeActivated  func() error
	restartRequired bool
}

// startFakeFirstRunEngine stands up an engine on its first-run flow over a
// configuration path of its own, announcing about dbPath.
//
// Its own configuration path, and a journal it shares with the command
// under test, for the reason startFakeEngineFor gives: the journal is the
// deployment, and a separate config.yaml is what lets a test tell a first
// configuration the engine wrote from one the command wrote itself.
func startFakeFirstRunEngine(t *testing.T, dbPath string) *fakeFirstRunEngine {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	serving, err := service.AnnounceServingFirstRun(configPath, dbPath)
	if err != nil {
		t.Fatalf("announcing a first-run engine over %s: %v", dbPath, err)
	}
	t.Cleanup(func() {
		if err := serving.Release(); err != nil {
			t.Errorf("releasing the first-run announcement: %v", err)
		}
	})
	firstRun, err := service.NewFirstRun(service.FirstRunDefaults{ConfigPath: configPath, StateDatabase: dbPath})
	if err != nil {
		t.Fatalf("NewFirstRun: %v", err)
	}

	base := &fakeEngine{
		t:          t,
		configPath: configPath,
		username:   "operator",
		// Never a real credential, as fakeEngine's own.
		password: "not-a-real-password",
		sessions: map[string]bool{},
		csrf:     map[string]bool{},
	}
	e := &fakeFirstRunEngine{fakeEngine: base, firstRun: firstRun, serving: serving, dbPath: dbPath}
	e.handle("GET /system/version", e.systemVersion)
	e.handle("GET /system/first-run", e.firstRunStatus)
	e.handle("POST /ssh-keys", e.importKey)
	e.handle("POST /system/first-run", e.completeFirstRun)
	base.server = httptest.NewServer(base)
	t.Cleanup(base.server.Close)
	t.Cleanup(func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.closeActivated != nil {
			_ = e.closeActivated()
		}
	})
	return e
}

// configured is what this engine says about itself, from the same state
// GET /system/version reports.
func (e *fakeFirstRunEngine) configured() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.firstRun.Configured()
}

// systemVersion answers the way apps/common/webhost's does: configured once
// the first configuration exists, and naming its deployment throughout.
// Before activation that name is the identity the first-run announcement
// minted beside the journal; afterwards it is the backend's, which is read
// from the same file.
func (e *fakeFirstRunEngine) systemVersion(w http.ResponseWriter, _ *http.Request) {
	e.mu.Lock()
	activated := e.activated
	e.mu.Unlock()
	v := service.BuildVersion("dev", "none")
	resp := apicontract.VersionResponse{
		APIVersion:    apicontract.Version,
		CoreVersion:   v.CoreVersion,
		Commit:        v.Commit,
		GoVersion:     v.GoVersion,
		EngineVersion: v.EngineVersion,
		Configured:    e.firstRun.Configured(),
	}
	if activated != nil {
		resp.DeploymentID = activated.DeploymentID()
		resp.ConfigRevision = activated.ConfigRevision()
		resp.Ready = activated.Ready()
	} else {
		id, err := service.DeploymentIdentity(e.dbPath)
		if err != nil {
			refuse(w, http.StatusInternalServerError, apicontract.ErrorCodeInternal, err.Error())
			return
		}
		resp.DeploymentID = id
	}
	writeJSON(w, http.StatusOK, resp)
}

func (e *fakeFirstRunEngine) firstRunStatus(w http.ResponseWriter, _ *http.Request) {
	state := "pending"
	if e.firstRun.Configured() {
		state = "done"
	}
	writeJSON(w, http.StatusOK, apicontract.FirstRunStatusResponse{
		Configured: e.firstRun.Configured(),
		Steps:      apicontract.FirstRunSteps{Administrator: "done", Email: "skipped", BackupSet: state},
	})
}

func (e *fakeFirstRunEngine) importKey(w http.ResponseWriter, r *http.Request) {
	var body apicontract.ImportSSHKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}
	ref, err := e.firstRun.ImportSSHKey(r.Context(), []byte(body.PrivateKeyPEM), body.Passphrase)
	if err != nil {
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, apicontract.ImportSSHKeyResponse{ID: ref.ID, Algorithm: ref.Algorithm, Fingerprint: ref.Fingerprint})
}

// completeFirstRun is POST /system/first-run as apps/common/webhost serves
// it: the spec in, CreateInitialConfig behind the announcement, then an
// activation in this process whose failure is restart_required rather than
// an error.
//
// The activation is a real service.Open, and that is the point of it. It
// takes the journal's startup lock, so a command that sent this request
// while still holding its own configuration-write claim on that journal
// would make the engine wait the claim out and answer restart_required: a
// first run that "worked" and left the engine serving nothing.
func (e *fakeFirstRunEngine) completeFirstRun(w http.ResponseWriter, r *http.Request) {
	if e.firstRun.Configured() {
		refuse(w, http.StatusConflict, apicontract.ErrorCodeAlreadyConfigured,
			"this instance is already configured; change its configuration through the ordinary settings and backup-set routes")
		return
	}
	var body apicontract.BackupSetSpec
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}
	if err := e.serving.Blocked(); err != nil {
		refuse(w, http.StatusInternalServerError, apicontract.ErrorCodeInternal,
			"this deployment cannot be set up yet and nothing was written: "+err.Error())
		return
	}
	set, err := e.firstRun.CreateInitialConfig(r.Context(), service.CreateBackupSetRequest{
		SourceName:                    body.SourceName,
		Name:                          body.Name,
		Host:                          body.Host,
		Port:                          body.Port,
		User:                          body.User,
		SSHKeyID:                      body.SSHKeyID,
		KnownHostsLine:                body.KnownHostsLine,
		RemotePath:                    body.RemotePath,
		LocalPath:                     body.LocalPath,
		Include:                       body.Include,
		CompletionStrategy:            body.CompletionStrategy,
		ValidatorID:                   service.ValidatorID(body.ValidatorID),
		StableFor:                     time.Duration(body.StableForSeconds) * time.Second,
		StaleAfter:                    time.Duration(body.StaleAfterSeconds) * time.Second,
		Disabled:                      body.Disabled,
		ReadOnly:                      body.ReadOnly,
		SkipConnectionCheck:           body.SkipConnectionCheck,
		Engine:                        body.Engine,
		UUID:                          body.Uuid,
		RepositoryDomain:              body.RepositoryDomain,
		SourceConsistency:             body.SourceConsistency,
		VerificationLevel:             body.VerificationLevel,
		VerificationSamplePercent:     body.VerificationSamplePercent,
		VerificationFullEvery:         time.Duration(body.VerificationFullEverySeconds) * time.Second,
		VerificationRestoreDrillEvery: time.Duration(body.VerificationRestoreDrillEverySeconds) * time.Second,
		SourceMountPrefix:             body.SourceMountPrefix,
		Actor:                         e.username,
	})
	if err != nil {
		if errors.Is(err, service.ErrAlreadyConfigured) {
			refuse(w, http.StatusConflict, apicontract.ErrorCodeAlreadyConfigured,
				"this instance was configured while your setup was in progress; reload and sign in")
			return
		}
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	opened, closeFn, openErr := service.Open(ctx, e.configPath)
	e.mu.Lock()
	if openErr != nil {
		e.restartRequired = true
	} else {
		e.activated = opened
		e.closeActivated = closeFn
	}
	restart := e.restartRequired
	e.mu.Unlock()

	writeJSON(w, http.StatusCreated, apicontract.CompleteFirstRunResponse{
		BackupSet:       toContractBackupSet(set),
		RestartRequired: restart,
	})
}

// askedFor reports whether this engine received request ("METHOD /path").
func (e *fakeFirstRunEngine) askedFor(request string) bool {
	for _, seen := range e.requests() {
		if seen == request {
			return true
		}
	}
	return false
}

// reportsConfiguredOverTheWire asks the engine what it is, the way any
// client would, so "the engine reports configured" is the engine's answer
// and not this test reading a field.
func (e *fakeFirstRunEngine) reportsConfiguredOverTheWire(t *testing.T) apicontract.VersionResponse {
	t.Helper()
	client, err := apiclient.New(apiclient.Config{BaseURL: e.baseURL(), Username: e.username, Password: e.password})
	if err != nil {
		t.Fatalf("a client for the fake first-run engine: %v", err)
	}
	got, err := client.SystemVersion(context.Background())
	if err != nil {
		t.Fatalf("GET /system/version on the fake first-run engine: %v", err)
	}
	return got
}

// firstRunFixture is the arrangement every case below starts from: an
// engine on its first-run flow announcing about dbPath, and a --config the
// command under test names that has no config.yaml in it.
func firstRunFixture(t *testing.T) (engine *fakeFirstRunEngine, cliConfig, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "state", "state.db")
	cliConfig = filepath.Join(dir, "config", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cliConfig), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	engine = startFakeFirstRunEngine(t, dbPath)
	return engine, cliConfig, dbPath
}

// TestFirstRun1254_CreateIsHandedToAServingFirstRunEngine is the issue's
// expected behaviour: beside a first-run engine, with an address to reach
// it, `backup-set create` hands the first configuration to that engine
// through POST /system/first-run and the engine comes out of it configured.
func TestFirstRun1254_CreateIsHandedToAServingFirstRunEngine(t *testing.T) {
	engine, cliConfig, dbPath := firstRunFixture(t)
	engine.attach(t)
	keyPath := writeTestPrivateKey(t)

	args := createArgs(cliConfig, keyPath, "api/postgres", "--state-database", dbPath, "--read-only")
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run(args) })
	})

	if code != exitOK {
		t.Fatalf("backup-set create beside a first-run engine exited %d, want %d; a headless install has no other way to finish its first run\nstdout: %s\nstderr: %s",
			code, exitOK, stdout, stderr)
	}
	if !engine.askedFor("POST /system/first-run") {
		t.Errorf("the first configuration never reached POST /system/first-run; the engine saw %v", engine.requests())
	}
	if engine.askedFor("POST /backup-sets") {
		t.Errorf("the first configuration was sent as an ordinary create, which a first-run engine does not serve: %v", engine.requests())
	}
	if _, err := os.Stat(cliConfig); !os.IsNotExist(err) {
		t.Errorf("the command wrote %s itself rather than handing the write over (stat err = %v)", cliConfig, err)
	}
	if !engine.configured() {
		t.Error("the engine is still unconfigured after a create that exited 0")
	}

	got := engine.reportsConfiguredOverTheWire(t)
	if !got.Configured {
		t.Errorf("GET /system/version answers configured=false after the first run: %+v", got)
	}
	if !got.Ready {
		t.Errorf("GET /system/version answers ready=false after the first run, so the engine could not bring its service up; a command that held the journal's claim across the request does exactly this\nstdout: %s", stdout)
	}

	// What the engine wrote, said as the engine's: the address it went to,
	// and the set it came back with.
	if !strings.Contains(stdout, engine.baseURL()) {
		t.Errorf("the output does not name the engine the first configuration was handed to:\n%s", stdout)
	}
	if !strings.Contains(stdout, "api/postgres") || !strings.Contains(stdout, "read_only: true") {
		t.Errorf("the output does not print the set the engine wrote:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, "restart") {
		t.Errorf("the output talks about a restart after a first run the engine activated in place:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

// TestFirstRun1254_AConfiguredEngineStillRefusesAFirstConfiguration keeps
// the refusal for a deployment that is past its first run exactly as it
// was. The engine is the deployment the command names and the address is
// right, and it still gets nothing: POST /system/first-run is the wrong
// request for an engine with a configuration, and the file it would have
// written is one nothing reads.
func TestFirstRun1254_AConfiguredEngineStillRefusesAFirstConfiguration(t *testing.T) {
	liveConfig := writeTestConfig(t)
	engine := startFakeEngineFor(t, liveConfig)
	engine.attach(t)
	dbPath := filepath.Join(filepath.Dir(liveConfig), "state.db")
	keyPath := writeTestPrivateKey(t)

	mistyped := filepath.Join(filepath.Dir(liveConfig), "confg.yaml")
	args := createArgs(mistyped, keyPath, "api/postgres", "--state-database", dbPath)
	var code int
	stderr := captureStderr(t, func() {
		code = captureStdoutCode(t, func() int { return run(args) })
	})

	if code != exitEngineHoldsDeployment {
		t.Errorf("a first configuration beside a CONFIGURED engine exited %d, want %d\nstderr: %s", code, exitEngineHoldsDeployment, stderr)
	}
	if _, err := os.Stat(mistyped); !os.IsNotExist(err) {
		t.Errorf("a first configuration was written at %s beside a configured engine (stat err = %v)", mistyped, err)
	}
	for _, seen := range engine.requests() {
		if !strings.HasPrefix(seen, "GET ") && seen != "POST /auth/login" {
			t.Errorf("a refused first configuration sent %q to a configured engine", seen)
		}
	}
	if !strings.Contains(stderr, "nothing was written") || !strings.Contains(stderr, "cannot carry this one") {
		t.Errorf("the refusal is not the one a configured deployment always got:\n%s", stderr)
	}
}

// TestFirstRun1254_TheEchoOfTheWizardsSubmissionFinishesTheFirstRun is the
// cliecho half. The Web UI's terminal prints `backup-set create` as the
// twin of POST /system/first-run, so pasting that line on the host the
// wizard is served from has to do what the wizard did, not exit 3.
func TestFirstRun1254_TheEchoOfTheWizardsSubmissionFinishesTheFirstRun(t *testing.T) {
	engine, cliConfig, dbPath := firstRunFixture(t)
	engine.attach(t)

	// The wizard imports its key first, through its own request, and the
	// submission names the id that came back. The echo does the same.
	raw, err := os.ReadFile(writeTestPrivateKey(t))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	ref, err := engine.firstRun.ImportSSHKey(context.Background(), raw, "")
	if err != nil {
		t.Fatalf("importing the wizard's key: %v", err)
	}

	spec := apicontract.BackupSetSpec{
		SourceName:         "api",
		Name:               "postgres",
		Host:               "source.example.internal",
		Port:               2222,
		User:               "backupuser",
		SSHKeyID:           ref.ID,
		KnownHostsLine:     aKnownHostsLine,
		RemotePath:         "/srv/backups",
		LocalPath:          "/data/backups/api",
		CompletionStrategy: "rename",
		StaleAfterSeconds:  172800,
		ReadOnly:           true,
	}
	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	line := cliecho.Echo(cliecho.Action{Method: http.MethodPost, Route: "/system/first-run", Body: body})
	if len(line.Command) < 2 || line.Command[1] != "backup-set" {
		t.Fatalf("POST /system/first-run no longer echoes `backup-set create`: %+v", line)
	}

	// --config and --state-database say which host the line is pasted on,
	// which the browser cannot know. --no-verify because
	// source.example.internal is not a machine, exactly as createArgs says.
	argv := append(append([]string(nil), line.Command[1:]...),
		"--config", cliConfig, "--state-database", dbPath, "--no-verify")
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run(argv) })
	})
	if code != exitOK {
		t.Fatalf("the echoed `%s` exited %d beside the first-run engine it was echoed from, want %d\nstdout: %s\nstderr: %s",
			line.Shell(), code, exitOK, stdout, stderr)
	}
	if !engine.askedFor("POST /system/first-run") || !engine.configured() {
		t.Errorf("the echoed command did not finish the first run: configured=%v, requests %v", engine.configured(), engine.requests())
	}
}

// TestFirstRun1254_ARefusalFromTheFirstRunRouteSaysWhy is the other half of
// handing the write over: when the engine says no, the operator reads the
// engine's reason, and nothing is written on this side to make up for it.
func TestFirstRun1254_ARefusalFromTheFirstRunRouteSaysWhy(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		code    apicontract.ErrorCode
		message string
	}{
		{
			// apps/common/webhost's answer for a state volume it cannot
			// announce on, the one refusal a fresh install most often
			// meets (#571).
			name:    "the state volume",
			status:  http.StatusInternalServerError,
			code:    apicontract.ErrorCodeInternal,
			message: "this deployment cannot be set up yet and nothing was written: /data/state is mounted read-only. Fix that and submit this form again; there is no need to reinstall or restart anything.",
		},
		{
			// The wizard finished first.
			name:    "a race with the wizard",
			status:  http.StatusConflict,
			code:    apicontract.ErrorCodeAlreadyConfigured,
			message: "this instance was configured while your setup was in progress; reload and sign in",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, cliConfig, dbPath := firstRunFixture(t)
			engine.attach(t)
			engine.handle("POST /system/first-run", func(w http.ResponseWriter, _ *http.Request) {
				refuse(w, tc.status, tc.code, tc.message)
			})

			args := createArgs(cliConfig, writeTestPrivateKey(t), "api/postgres", "--state-database", dbPath)
			var code int
			stderr := captureStderr(t, func() {
				code = captureStdoutCode(t, func() int { return run(args) })
			})
			if code == exitOK {
				t.Fatalf("a create the engine refused exited 0\nstderr: %s", stderr)
			}
			if !engine.askedFor("POST /system/first-run") {
				t.Fatalf("the request never reached POST /system/first-run, so this is not the refusal under test: %v\nstderr: %s", engine.requests(), stderr)
			}
			if !strings.Contains(stderr, tc.message) {
				t.Errorf("the engine's reason did not reach the operator\nwant it to contain: %s\nstderr: %s", tc.message, stderr)
			}
			if _, err := os.Stat(cliConfig); !os.IsNotExist(err) {
				t.Errorf("a refused hand-over wrote %s locally instead (stat err = %v)", cliConfig, err)
			}
		})
	}
}
