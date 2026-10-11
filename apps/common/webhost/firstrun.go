// firstrun.go is the HTTP half of issue #176: what an instance with no
// configuration serves, and how it stops being one.
//
// # The shape of the decision
//
// An unconfigured instance is served by a DIFFERENT, deliberately tiny
// route table (newUnconfiguredRouter below), not by the full one with
// per-route guards bolted on. That is the fail-closed direction: a route
// added to the configured router in a year's time is unreachable on a
// fresh install because it was never registered there, rather than
// reachable because somebody forgot a decorator. Everything the small
// table does not name answers 503 NOT_CONFIGURED, so a client is told
// what is wrong rather than being handed a bare 404 to interpret.
//
// # Readiness, reused rather than reinvented
//
// Issue #157 added BackupService.Ready for §46.1's startup sequence, and
// §36 makes it the precondition in front of a destructive operation. An
// instance with no configuration has not run that sequence, so it is not
// ready, and /health/ready and GET /system/version's ready field say so
// through the same isReady (handlers_system.go) a configured instance
// uses. There is no second "is this usable" flag here.
//
// # Authentication comes first
//
// The whole setup surface sits INSIDE the authenticated /api/v1 group.
// An unconfigured instance reachable on a LAN must not be configurable by
// whoever reaches the port first, so the ordering is: enroll with the
// single-use bootstrap token printed to the container log
// (apps/common/auth/local, §49.1's "reaching the port SHALL NOT be
// sufficient to claim the account"), sign in, then set up. Enrollment
// needs no configuration of its own and already worked on a fresh
// install; what was missing was a process still running for it to happen
// against.
package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/retnd/retnd/apps/common/platform/capabilities"
	"github.com/retnd/retnd/core/service"
)

// FirstRunClient is the seam this package talks to core/service.FirstRun
// through, expressed as an interface for the same reason
// BackupServiceClient is: a handler test substitutes a double instead of
// standing up a real filesystem layout.
//
// *service.FirstRun satisfies this directly (see the compile-time
// assertion below).
type FirstRunClient interface {
	// Configured reports whether a configuration file already exists.
	// Once it is true the setup route refuses, so a live deployment's
	// configuration can never be replaced through it.
	Configured() bool

	// The three setup-only calls a client needs BEFORE any configuration
	// exists. Each is the same operation BackupServiceClient exposes, on
	// a surface that does not need a BackupService to reach it — which is
	// why SetupClient (below) can hold either one.
	ImportSSHKey(ctx context.Context, raw []byte, passphrase string) (service.SSHKeyRef, error)
	ProbeHostKey(ctx context.Context, host string, port int) (service.HostKeyProbe, error)
	TestConnection(ctx context.Context, req service.ConnectionTestRequest) (service.ConnectionTestResult, error)
	ListSSHKeys(ctx context.Context) ([]service.SSHKeyListing, error)
	DiscoverSSHKeyCandidates(ctx context.Context) (service.SSHKeyDiscovery, error)
	ImportSSHKeyCandidate(ctx context.Context, candidateID string) (service.SSHKeyRef, error)

	// CreateInitialConfig writes this deployment's first configuration.
	// See core/service.FirstRun.CreateInitialConfig for the ordering and
	// the exclusive-create contract this package relies on without
	// re-implementing any part of.
	CreateInitialConfig(ctx context.Context, req service.CreateBackupSetRequest) (service.BackupSet, error)
}

var _ FirstRunClient = (*service.FirstRun)(nil)

// SetupClient is the part of the wizard's surface that works identically
// before and after a configuration exists: import a key, probe a host
// key, test a candidate connection. Both a configured backend and the
// first-run surface provide all three, so the three handlers in
// handlers_ssh.go call whichever one this deployment currently has
// instead of each carrying its own branch.
type SetupClient interface {
	ImportSSHKey(ctx context.Context, raw []byte, passphrase string) (service.SSHKeyRef, error)
	ProbeHostKey(ctx context.Context, host string, port int) (service.HostKeyProbe, error)
	TestConnection(ctx context.Context, req service.ConnectionTestRequest) (service.ConnectionTestResult, error)

	// Looking, as well as doing (#592). These are here rather than only
	// on the configured backend because the setup wizard is the surface
	// that needs them most: a brand new install has exactly one key on
	// the machine, the one our own installer generated and compose
	// mounted, and setup is the moment somebody is looking for it.
	ListSSHKeys(ctx context.Context) ([]service.SSHKeyListing, error)
	DiscoverSSHKeyCandidates(ctx context.Context) (service.SSHKeyDiscovery, error)
	ImportSSHKeyCandidate(ctx context.Context, candidateID string) (service.SSHKeyRef, error)
}

// setup returns the SetupClient this deployment currently has: the
// backend once configured, the first-run surface before that. Never both:
// a configured instance's key imports have to land through the same
// BackupService everything else goes through.
func (h *handlers) setup() SetupClient {
	if h.backend != nil {
		return h.backend
	}
	if h.firstRun != nil {
		return h.firstRun
	}
	return nil
}

// configured reports whether this deployment has a configuration at all.
//
// A live backend is the authority, and it is asked FIRST. A backend can
// only ever have been opened from a configuration that existed and
// validated, so a running process holding one is configured no matter
// what is on disk at this instant. The first-run surface, which answers
// by statting a file, is consulted only when there is no backend to ask,
// which is the one situation where the file genuinely is the only
// evidence there is.
//
// The order matters because both authorities survive activation: the
// configured router keeps FirstRun set so GET /system/first-run keeps
// answering. Asking the file first meant that deleting or renaming
// config.yaml under a fully running instance flipped this to false, sent
// every operator's browser to the setup wizard, and -- worse -- opened
// completeFirstRun's guard, which would then write a SECOND "first"
// configuration and report restart_required false while the process kept
// serving the configuration that was no longer on disk. Both halves of
// that answer were untrue.
func (h *handlers) configured() bool {
	if h.backend != nil {
		return true
	}
	if h.firstRun != nil {
		return h.firstRun.Configured()
	}
	return false
}

// activationTimeout bounds the in-process activation completeFirstRun
// triggers. It is generous on purpose: what it covers is a journal open
// plus any pending schema migration on a NAS's own disk, and cutting that
// short is the failure this bound exists to avoid, not the one it exists
// to cause. It is a backstop against an open that never returns, so the
// setup request cannot hang a connection for the life of the process.
const activationTimeout = 10 * time.Minute

// firstRunStatusResponse is GET /api/v1/system/first-run's body: the one
// question the shared UI asks before deciding whether to render the setup
// flow or the application, and (retnd#1049) the guide that leads a fresh
// install through its first steps.
//
// Steps maps each guide step to done, pending or skipped, and NextStep is
// the first step that is pending, or "none". Only a skip is stored; done is
// derived from the thing the step sets up, so the answer cannot drift from
// the truth.
type firstRunStatusResponse struct {
	Configured bool          `json:"configured"`
	Steps      firstRunSteps `json:"steps"`
	NextStep   string        `json:"next_step"`
}

// firstRunSteps is the contract's FirstRunSteps: each step's state.
type firstRunSteps struct {
	Administrator string `json:"administrator"`
	Email         string `json:"email"`
	BackupSet     string `json:"backup_set"`
}

// The guide's steps, in the order they are offered.
const (
	stepAdministrator = "administrator"
	stepEmail         = "email"
	stepBackupSet     = "backup_set"

	stepDone    = "done"
	stepPending = "pending"
	stepSkipped = "skipped"
)

var setupSteps = []string{stepAdministrator, stepEmail, stepBackupSet}

// SetupProgress is what the first-run guide asks the host about the parts
// of setup this package cannot see itself: whether an administrator
// exists, whether a mail server is set up for account recovery, and which
// steps an operator skipped. The generic host's local-auth service
// satisfies it. A nil SetupProgress is a platform with its own identity
// provider, where the caller is authenticated by construction and there
// is no mail step to set up.
type SetupProgress interface {
	AdministratorCreated() bool
	EmailConfigured() bool
	SetupSkips() []string
	SkipSetupStep(step string) error
}

// stepStates computes the guide for the current moment.
func (h *handlers) stepStates(ctx context.Context) (map[string]string, string) {
	steps := map[string]string{}
	skipped := map[string]bool{}
	if h.progress != nil {
		for _, s := range h.progress.SetupSkips() {
			skipped[s] = true
		}
	}

	steps[stepAdministrator] = stepDone
	steps[stepEmail] = stepSkipped
	if h.progress != nil {
		if !h.progress.AdministratorCreated() {
			steps[stepAdministrator] = stepPending
		}
		switch {
		case h.progress.EmailConfigured():
			steps[stepEmail] = stepDone
		case skipped[stepEmail]:
			steps[stepEmail] = stepSkipped
		default:
			steps[stepEmail] = stepPending
		}
	}

	steps[stepBackupSet] = stepPending
	if h.backend != nil {
		if sets, err := h.backend.ListBackupSets(ctx); err == nil && len(sets) > 0 {
			steps[stepBackupSet] = stepDone
		}
	}
	if steps[stepBackupSet] == stepPending && skipped[stepBackupSet] {
		steps[stepBackupSet] = stepSkipped
	}

	// The mail step is optional and never blocks the guide: it stays
	// listed (pending, skipped or done) and reachable, but the step the
	// service names as next is the first required one that is still open,
	// so a fresh install is led to its first backup set whether or not
	// anybody has a mail server to hand.
	next := "none"
	for _, name := range setupSteps {
		if name == stepEmail {
			continue
		}
		if steps[name] == stepPending {
			next = name
			break
		}
	}
	return steps, next
}

// firstRunStatus is GET /api/v1/system/first-run: whether this instance
// has been configured yet, and which guided setup step comes next.
//
// It stays registered on the configured router too, rather than
// disappearing once setup is done, so a client asking always gets an
// answer. A route that vanished would answer 404, which a client cannot
// tell apart from a wrong URL or an older server.
func (h *handlers) firstRunStatus(w http.ResponseWriter, r *http.Request) {
	steps, next := h.stepStates(r.Context())
	writeJSON(w, http.StatusOK, firstRunStatusResponse{
		Configured: h.configured(),
		Steps: firstRunSteps{
			Administrator: steps[stepAdministrator],
			Email:         steps[stepEmail],
			BackupSet:     steps[stepBackupSet],
		},
		NextStep: next,
	})
}

// skipFirstRunRequest is POST /api/v1/system/first-run/skip's body.
type skipFirstRunRequest struct {
	Step string `json:"step"`
}

// skipFirstRunStep is POST /api/v1/system/first-run/skip: an operator
// choosing to leave a guide step for later. The skip is stored durably, and
// the step stays reachable from the same guide at any time. Only the email
// and backup-set steps can be skipped: the administrator is how the
// operator got here at all.
func (h *handlers) skipFirstRunStep(w http.ResponseWriter, r *http.Request) {
	var body skipFirstRunRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeDecodeError(w, err, 4096)
		return
	}
	if body.Step != stepEmail && body.Step != stepBackupSet {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "step must be \"email\" or \"backup_set\"")
		return
	}
	if h.progress == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "this deployment keeps no setup steps to skip")
		return
	}
	if err := h.progress.SkipSetupStep(body.Step); err != nil {
		h.internalError(w, r, "INTERNAL", "could not record the skipped step", err)
		return
	}
	h.firstRunStatus(w, r)
}

// completeFirstRunResponse is POST /api/v1/system/first-run's body.
//
// RestartRequired is the honest half of this call. The configuration is
// durably on disk the moment CreateInitialConfig returns, so a failure to
// bring a service up against it in the SAME process is not a failed
// setup, and reporting one as an error would leave an operator retrying a
// creation that has already happened. It says what is actually true: the
// instance is configured, and this process needs restarting to serve it.
type completeFirstRunResponse struct {
	BackupSet       backupSetResponse `json:"backup_set"`
	RestartRequired bool              `json:"restart_required"`
}

// completeFirstRun is POST /api/v1/system/first-run: the one write a
// fresh install exposes, and the end of the operator's journey from
// "installed from an app store" to "configured".
//
// It is authenticated (the whole /api/v1 group is) and CSRF-protected
// (router.go), and it is deliberately NOT behind the destructive gate:
// there is no backup data on a fresh install for it to touch, and gating
// it would make a brand-new instance permanently unconfigurable until
// #92 lands, which is the opposite of this issue.
//
// It takes the backup-set SPEC that POST /api/v1/backup-sets also takes,
// because the operator is answering the same questions in the same
// wizard. Two things differ. Underneath, a create folds a set into an
// existing configuration while this one writes the first configuration
// there has ever been. On the wire, the create's body carries one field
// this one's does not, run_immediately, because there is nothing running
// to run it: see the decode below.
func (h *handlers) completeFirstRun(w http.ResponseWriter, r *http.Request) {
	if h.firstRun == nil || h.configured() {
		writeError(w, http.StatusConflict, "ALREADY_CONFIGURED",
			"this instance is already configured; change its configuration through the ordinary settings and backup-set routes")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCreateBackupSetBodyBytes)

	var body backupSetSpec
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDecodeError(w, err, maxCreateBackupSetBodyBytes)
		return
	}

	// The body is backupSetSpec, not backupSetRequest, and that is the
	// contract's own shape rather than a convenience here: this operation
	// takes api/v1/openapi.json's BackupSetSpec, which carries no
	// run_immediately at all. There is no BackupService to submit an
	// operation to until activation below has happened, so honouring the
	// field is not something this handler can promise -- and a request
	// field a contract declares and an endpoint ignores is worse than one
	// it never declared, because a client has no way to find out. See
	// core/service.FirstRun.CreateInitialConfig's own doc.
	//
	// Every OTHER field of that spec has to arrive on the request below,
	// and this used to drop read_only. An operator who ticked "read only"
	// in the wizard on a fresh install got a set that was not read only,
	// with nothing saying so, which is the wrong direction for a mistake
	// to fall in: read-only is what stops this manager ever deleting the
	// remote copies (#282, #316) and the wizard offers it as a safety
	// choice. A request assembled field by field, beside a decoder that
	// fills in the whole spec, is a shape that loses a field every time
	// one is added, so firstRunSpecCarriage (firstrun_test.go) walks
	// backupSetSpec itself and fails on any field with no row rather than
	// on the field somebody happened to notice.
	set, err := h.firstRun.CreateInitialConfig(r.Context(), service.CreateBackupSetRequest{
		SourceName:          body.SourceName,
		Name:                body.Name,
		Host:                body.Host,
		Port:                body.Port,
		User:                body.User,
		SSHKeyID:            body.SSHKeyID,
		KnownHostsLine:      body.KnownHostsLine,
		RemotePath:          body.RemotePath,
		LocalPath:           body.LocalPath,
		Include:             body.Include,
		CompletionStrategy:  body.CompletionStrategy,
		ValidatorID:         service.ValidatorID(body.ValidatorID),
		StableFor:           secondsToDuration(body.StableForSeconds),
		StaleAfter:          secondsToDuration(body.StaleAfterSeconds),
		Disabled:            body.Disabled,
		ReadOnly:            body.ReadOnly,
		SkipConnectionCheck: body.SkipConnectionCheck,

		// EPIC K's engine seam (#788), carried here for the reason
		// TestCompleteFirstRun_CarriesEveryFieldOfTheSpecItWasGiven
		// exists: this request is assembled field by field, and a field
		// added to the spec and not added here is a wizard answer that
		// silently does not happen. An operator who chose the
		// incremental engine on a fresh install and got an artifact set
		// would find out at the first restore.
		Engine:                        body.Engine,
		UUID:                          body.UUID,
		RepositoryDomain:              body.RepositoryDomain,
		SourceConsistency:             body.SourceConsistency,
		VerificationLevel:             body.VerificationLevel,
		VerificationSamplePercent:     body.VerificationSamplePercent,
		VerificationFullEvery:         time.Duration(body.VerificationFullEverySeconds) * time.Second,
		VerificationRestoreDrillEvery: time.Duration(body.VerificationRestoreDrillEverySeconds) * time.Second,
		SourceMountPrefix:             body.SourceMountPrefix,
		Actor:                         actorFromContext(r.Context()),
	})
	if err != nil {
		if errors.Is(err, service.ErrAlreadyConfigured) {
			writeError(w, http.StatusConflict, "ALREADY_CONFIGURED",
				"this instance was configured while your setup was in progress; reload and sign in")
			return
		}
		// The two refusals that are about this deployment's state volume
		// rather than about the operator's answers, said in words instead
		// of collapsed into writeBackupSetError's "failed to write backup
		// set" (#571, and PR #581's review of it).
		//
		// They are the whole diagnosis a fresh install gets. An operator
		// who installed this from an app store has a browser and nothing
		// else: no shell, no CLI, no config.yaml to look at, and until
		// this the container was not even staying up long enough to be
		// asked. A read-only bind mount, a state volume mounted after the
		// service started, or a uid that cannot write /data/state all
		// arrive here, and all three are things that person can go and
		// fix and then press the button again.
		//
		// 500 rather than 400: nothing is wrong with the request, this
		// deployment cannot carry it out. INTERNAL is the code the
		// contract already declares for that on this operation, so this
		// says more without widening the wire.
		//
		// Safe to echo, on the same terms as ErrInvalidRequest in
		// writeBackupSetError: both messages are built by core/service
		// from this deployment's OWN configured paths and an OS reason,
		// never from rclone or state internals, and the path is what makes
		// the sentence actionable.
		if errors.Is(err, service.ErrStateDirInvalid) || errors.Is(err, service.ErrNotAnnounced) {
			// The one 500 in this package that writes its own body rather
			// than going through internalError, because it deliberately
			// echoes the service's own sentence (see the paragraph above
			// for why that is safe here and nowhere else). It still logs,
			// under the id it just minted: the argument for recording a
			// refusal does not weaken because the client got a better
			// message this time (#598).
			id := writeError(w, http.StatusInternalServerError, "INTERNAL",
				"this deployment cannot be set up yet and nothing was written: "+err.Error()+". Fix that and submit this form again; there is no need to reinstall or restart anything.")
			h.logRefusal(r, http.StatusInternalServerError, "INTERNAL", id, err)
			return
		}
		h.writeBackupSetError(w, r, err)
		return
	}

	// The configuration exists now. Ask the host to bring a real service
	// up against it, in this process, so the operator never has to
	// restart a container they just installed. A failure here is
	// reported, not hidden, but it is not an error: see
	// completeFirstRunResponse.RestartRequired.
	restartRequired := false
	if h.onConfigured != nil {
		// Activation runs on a context detached from the request, and
		// that is a correctness requirement rather than a nicety. What it
		// builds is a process-lifetime resource -- a journal handle and
		// the shared journal lock -- and the request context is cancelled
		// the instant this handler returns, early if the operator closes
		// the browser tab, and earlier still if serve-ui's reverse proxy
		// hits its own ResponseHeaderTimeout on a slow first migration.
		//
		// A cancellation reaching state.Open surfaces as an error that is
		// neither ErrUnknownSchemaVersion nor ErrSchemaDrift, so the
		// migration failure path falls through to the snapshot restore,
		// which rename-overwrites the journal's files. Whether a browser
		// tab stayed open must not decide whether that runs.
		//
		// The timeout replaces the cancellation that was just removed:
		// detaching means a client that gives up can no longer stop a
		// hung open, so activation gets a bound of its own instead of
		// none at all. r.Context() stays in use for writing the response.
		activationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), activationTimeout)
		defer cancel()
		if err := h.onConfigured(activationCtx); err != nil {
			restartRequired = true
		}
	}

	writeJSON(w, http.StatusCreated, completeFirstRunResponse{
		BackupSet:       toBackupSetResponse(set),
		RestartRequired: restartRequired,
	})
}

// writeNotConfigured is the one refusal every route outside the setup
// surface gets on an unconfigured instance. 503 rather than 404 or 409:
// the route exists and will work, this instance simply cannot serve it
// yet, which is exactly what "Service Unavailable" means and what a
// client can act on by sending the operator to the setup flow.
func writeNotConfigured(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED",
		"this instance has not been configured yet; complete the setup flow at /api/v1/system/first-run first")
}

// getDefaultSettings is GET /api/v1/settings on an instance with no
// configuration on disk (retnd#1049): the engine's own defaults, marked
// first_run and not persisted, so a page can say "these are the defaults,
// nobody has changed them" instead of refusing to show a setting.
func (h *handlers) getDefaultSettings(w http.ResponseWriter, r *http.Request) {
	resp := toSettingsResponse(service.DefaultSettings())
	resp.FirstRun = true
	resp.Persisted = false
	writeJSON(w, http.StatusOK, resp)
}

// getEmptyHealth is GET /api/v1/system/health on an instance with no
// configuration: nothing is being backed up, which is an empty report and
// not an unavailable one.
func (h *handlers) getEmptyHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		GeneratedAt: formatTime(time.Now()),
		BackupSets:  []backupSetHealthResponse{},
	})
}

// newUnconfiguredRouter builds the entire HTTP surface of an instance
// that has no configuration: the two health probes, and an authenticated
// /api/v1 group holding nothing but what setup genuinely needs.
//
// The list below IS the answer to issue #176's "what is reachable before
// configuration exists". Every entry is either read-only, or a setup
// write that cannot touch backup data because there is none. Nothing
// that runs a backup, evaluates retention, quarantines an artifact,
// deletes anything or edits settings appears here, and nothing can be
// added by accident: an unlisted route falls to NotFound/MethodNotAllowed
// below, which answer 503 NOT_CONFIGURED.
func newUnconfiguredRouter(h *handlers, platform capabilities.PlatformAdapter) http.Handler {
	r := chi.NewRouter()

	// The same edge as the configured table's (see NewRouter): a
	// first-run instance is the one an operator is most likely to be
	// diagnosing, so it is the last place a response should arrive
	// without an id to quote.
	r.Use(RequestScope)

	r.Get("/health/live", healthLive)
	r.Get("/health/ready", h.healthReady)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMiddleware(platform))

		// Read-only, and how a client learns which mode it is looking at.
		r.Get("/system/version", h.systemVersion)
		r.Get("/system/capabilities", h.systemCapabilities)
		r.Get("/system/first-run", h.firstRunStatus)
		r.With(requireCSRF).Post("/system/first-run/skip", h.skipFirstRunStep)

		// retnd#1049: the two reads every page makes first. An instance
		// that is still waiting for its configuration answers them with
		// what is true, the engine's own defaults and an empty report,
		// instead of refusing: "nothing configured yet" is a state, not
		// an error. Only writes stay refused below.
		r.Get("/settings", h.getDefaultSettings)
		r.Get("/system/health", h.getEmptyHealth)

		// The setup flow itself.
		r.With(requireCSRF).Post("/system/first-run", h.completeFirstRun)
		r.With(requireCSRF).Post("/ssh-keys", h.importSSHKey)
		r.With(requireCSRF).Post("/ssh-keys/from-candidate", h.importSSHKeyFromCandidate)
		r.With(requireCSRF).Post("/ssh/host-key-probe", h.probeHostKey)
		r.With(requireCSRF).Post("/backup-sets/test-connection", h.testConnection)

		// The wizard's step 5 picklist. Read-only, and code-defined
		// rather than deployment-defined, so it says nothing about a
		// configuration that does not exist.
		r.Get("/validators", h.listValidators)

		r.NotFound(writeNotConfigured)
		r.MethodNotAllowed(writeNotConfigured)
	})

	return r
}
