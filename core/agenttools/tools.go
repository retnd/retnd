package agenttools

import (
	"slices"
	"sort"
	"strings"
)

// Scope is how far a token reaches. The three words are the closed
// vocabulary FR-92 names; core/service holds the same three for the token
// store and a test holds the two sets equal.
type Scope string

const (
	ScopeRead      Scope = "read"
	ScopeOperate   Scope = "operate"
	ScopeConfigure Scope = "configure"
)

// Row is one answer to "may an agent do this?". A tool row has a Tool name,
// its Scope and a static Description; any other row has a Why, the reason no
// agent reaches the route by a tool.
//
// Route is the router's own pattern with the /api/v1 prefix off, the key
// core/cliecho's table uses, so the same string finds the terminal twin of a
// tool in cliecho and the row of a registered route here.
//
// Action is set for the one route several acts arrive on (POST /operations,
// where run_cycle and a restore are different acts) and is the body's own
// action word. A route with an Action has a row for every action it carries,
// and the pair (Method, Route, Action) is what is unique.
//
// Untrusted names the result fields whose values originate outside the
// deployment, as paths into the JSON result ("artifacts[].name"). A transport
// puts them under an untrusted object in the structured result and never in
// the human-readable text.
type Row struct {
	Tool        string
	Method      string
	Route       string
	Action      string
	Scope       Scope
	Description string
	Untrusted   []string
	Why         string
}

// Key is "METHOD /route", the string cliecho keys its table by.
func (r Row) Key() string { return r.Method + " " + r.Route }

// Rows reports every row, tools first, in the order the table declares them.
// The slices are copies: nothing a caller does to them changes the table.
func Rows() []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		r.Untrusted = slices.Clone(r.Untrusted)
		out[i] = r
	}
	return out
}

// Tools reports the rows that are tools.
func Tools() []Row {
	var out []Row
	for _, r := range Rows() {
		if r.Tool != "" {
			out = append(out, r)
		}
	}
	return out
}

// Lookup finds a tool by name.
func Lookup(name string) (Row, bool) {
	for _, r := range Rows() {
		if r.Tool != "" && r.Tool == name {
			return r, true
		}
	}
	return Row{}, false
}

// Routes reports the distinct "METHOD /route" keys the table answers, tool or
// refusal, sorted.
func Routes() []string {
	seen := make(map[string]bool, len(rows))
	var out []string
	for _, r := range rows {
		if k := r.Key(); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Offered reports whether a mutating request to a concrete path below
// /api/v1 reaches a route that has a tool, which is the only kind of change an
// agent can ask for (and, from U2.1, a person confirm). action is the body's
// own action word for the route several acts share (POST /operations) and ""
// anywhere else: a tool row that names an action is offered only for that
// action, so a restore sent to the operations route is not a run_cycle. A safe
// method is not a question here: the deny list (Denied) is what holds back the
// reads.
func Offered(method, path, action string) bool {
	for _, r := range rows {
		if r.Tool != "" && r.Method == method && matches(r.Route, path) && (r.Action == "" || r.Action == action) {
			return true
		}
	}
	return false
}

// The reasons. Many routes share one, and a reason that is written twice is a
// reason that says two things.
const (
	whyAuth = "signing in, enrolling, resetting a password and the recovery settings belong to a person at a keyboard, and a token never authenticates anything but /api/v1"

	whyFirstRun = "the first-run guide configures a deployment that has no operator yet, so there is nobody for an agent to act for"

	whyGrant = "a recovery grant lets whoever holds it read a backup, so it is a credential and an agent is never given one"

	whyTokens = "a token never mints, lists or revokes a token; a person does that"

	whyKeys = "key custody: this route lists, makes, exports, confirms or retires key material, or names the recipient that opens a copy, and no scope reaches it"

	whyProvider = "this route carries a key provider's credential or a passphrase, and no scope reaches it"

	whyCredential = "this route carries or names a credential for a storage medium or a remote host, and no scope reaches it"

	whySSH = "an SSH key, its candidates and a host key probe are credentials and the reach of one, and no scope reaches them"

	whySecret = "a workflow environment value can be a secret, and the route returns or writes it, so no scope reaches it"

	whyLog = "a workflow log is whatever a script printed, which can be anything including a secret, so no scope reaches it"

	whyScript = "a validator script is operator-written text that can carry a secret, so no scope reaches it"

	whyLifecycle = "creating or deleting a backup set or a medium changes what is protected and where, so it is a person's decision and no scope reaches it"

	whyDestructive = "this route deletes data or rebuilds what the engine believes about it, so it is a person's decision and no scope reaches it"

	whyConfigure = "changing this is a person's decision in this release; the four configure tools are the only settings an agent may propose, and a person confirms each"

	whyMutation = "this starts or changes something that has no tool: an agent proposes only the operate and configure tools, and a person does the rest"

	whyRead = "no tool reads this: the status, backup set, backup and activity tools carry what an agent needs, and the rest is configuration an operator reads in the browser"

	whyCatalogue = "the catalogue is read by an operator from a terminal or the browser, and an agent already has it as the list of its own tools"

	whyOperationAction = "this action on the operations route is a restore, a hold or a snapshot check, each a person's decision about data, and no tool starts it"
)

// no is the refusal rows for the routes named, each "METHOD /route".
func no(why string, keys ...string) []Row {
	out := make([]Row, 0, len(keys))
	for _, k := range keys {
		method, route, _ := strings.Cut(k, " ")
		out = append(out, Row{Method: method, Route: route, Why: why})
	}
	return out
}

// join concatenates row groups into the table.
func join(groups ...[]Row) []Row {
	var out []Row
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

var rows = join(toolRows, refusalRows)

var toolRows = []Row{
	// ------------------------------------------------------------ read ---
	{
		Tool: "status", Method: "GET", Route: "/system/health", Scope: ScopeRead,
		Description: "Read the health of every backup set: its state and the reason for it, the newest good backup, quarantined and stale counts, free space and copies away from home.",
		Untrusted:   []string{"backup_sets[].reason", "backup_sets[].halt_reason", "backup_sets[].failed_move_reason"},
	},
	{
		Tool: "backup_sets_list", Method: "GET", Route: "/backup-sets", Scope: ScopeRead,
		Description: "List the configured backup sets with their source, schedule, completion strategy, retention flags and whether each is disabled or read only.",
		Untrusted:   []string{"backup_sets[].host", "backup_sets[].remote_path", "backup_sets[].include[]"},
	},
	{
		Tool: "backup_set_show", Method: "GET", Route: "/backup-sets/*", Scope: ScopeRead,
		Description: "Read one backup set by its source and name: where it comes from, how it is checked and how long it is kept.",
		Untrusted:   []string{"host", "remote_path", "include[]"},
	},
	{
		Tool: "backups_list", Method: "GET", Route: "/backups", Scope: ScopeRead,
		Description: "List the backups this deployment holds, each with its state, size, validation result, quarantine flag and the placements that hold a copy.",
		Untrusted: []string{
			"artifacts[].name", "artifacts[].remote_path", "artifacts[].local_path", "artifacts[].validation_detail",
			"artifacts[].quarantine_reason", "artifacts[].placements[].location",
			"artifacts[].placements[].last_failure_reason", "artifacts[].placements[].recovery_failure_reason",
		},
	},
	{
		Tool: "backup_show", Method: "GET", Route: "/backups/{source}/{set}/{name}", Scope: ScopeRead,
		Description: "Read one backup by source, set and name: its state, validation, checksum, retention tier and every placement that holds a copy.",
		Untrusted: []string{
			"name", "remote_path", "local_path", "validation_detail", "quarantine_reason",
			"placements[].location", "placements[].last_failure_reason", "placements[].recovery_failure_reason",
		},
	},
	{
		Tool: "copies_show", Method: "GET", Route: "/encryption/copies", Scope: ScopeRead,
		Description: "Read how each stored copy of one backup is encrypted: the format, whether the key resolves and the stored size. Never a key.",
		Untrusted:   []string{},
	},
	{
		Tool: "encryption_state", Method: "GET", Route: "/encryption/policy", Scope: ScopeRead,
		Description: "Read the encryption policy: the mode, the suite, each set's effective mode and whether a rotation is due. Never a key or a recipient.",
		Untrusted:   []string{},
	},
	{
		Tool: "assurance_due", Method: "GET", Route: "/assurance/due", Scope: ScopeRead,
		Description: "List the backups whose scrub or recovery test is due or overdue, with when each was last assured and when it is next due.",
		Untrusted:   []string{"items[].artifact_name"},
	},
	{
		Tool: "assurance_show", Method: "GET", Route: "/backup-sets/{source}/{set}/assurance", Scope: ScopeRead,
		Description: "Read one backup set's assurance settings: the scrub cadence and strategy and how its recovery test is run.",
		Untrusted:   []string{},
	},
	{
		Tool: "activity", Method: "GET", Route: "/activity", Scope: ScopeRead,
		Description: "Page through the activity journal, newest first: what changed, to which backup, when and who did it.",
		Untrusted:   []string{"events[].artifact_name", "events[].detail"},
	},
	{
		Tool: "activity_live", Method: "GET", Route: "/activity/live", Scope: ScopeRead,
		Description: "Read one snapshot of the live activity feed: what is happening on this deployment right now.",
		Untrusted: []string{
			"sets[].artifact", "sets[].events[].message", "sets[].events[].fields[].value",
			"deployment.events[].message", "deployment.events[].fields[].value",
		},
	},
	{
		Tool: "quarantine_list", Method: "GET", Route: "/quarantine", Scope: ScopeRead,
		Description: "List the backups held in quarantine and the reason each was held back.",
		Untrusted: []string{
			"artifacts[].name", "artifacts[].remote_path", "artifacts[].local_path", "artifacts[].validation_detail",
			"artifacts[].quarantine_reason", "artifacts[].placements[].location",
			"artifacts[].placements[].last_failure_reason", "artifacts[].placements[].recovery_failure_reason",
		},
	},
	{
		Tool: "operations_list", Method: "GET", Route: "/operations", Scope: ScopeRead,
		Description: "List recent operations: what was started, by whom, how far it got and how it ended.",
		Untrusted: []string{
			"operations[].error", "operations[].result", "operations[].progress.artifact", "operations[].restore.detail",
			"operations[].snapshots[].reason", "operations[].snapshots[].holds[].reason",
		},
	},
	{
		Tool: "operation_show", Method: "GET", Route: "/operations/{id}", Scope: ScopeRead,
		Description: "Read one operation by its id: its status, progress and result.",
		Untrusted: []string{
			"error", "result", "progress.artifact", "restore.detail",
			"snapshots[].reason", "snapshots[].holds[].reason",
		},
	},
	{
		Tool: "storage_mediums_list", Method: "GET", Route: "/storage-mediums", Scope: ScopeRead,
		Description: "List the storage mediums copies are kept on: type, region, storage class and which is the default.",
		Untrusted:   []string{"mediums[].endpoint", "mediums[].path", "mediums[].prefix"},
	},
	{
		Tool: "storage_usage", Method: "GET", Route: "/storage-mediums/{id}/usage", Scope: ScopeRead,
		Description: "Read what one storage medium holds: how many placements, and per backup set how many are the only copy there.",
		Untrusted:   []string{},
	},
	{
		Tool: "retention_preview", Method: "GET", Route: "/backup-sets/{source}/{set}/retention/preview", Scope: ScopeRead,
		Description: "Preview what retention would keep and delete for one backup set, with the reason for each verdict. It deletes nothing.",
		Untrusted:   []string{"verdicts[].artifact", "verdicts[].reason", "moves[].artifact", "unconfirmed_placements[]"},
	},
	{
		Tool: "workflow_runs_list", Method: "GET", Route: "/workflow-runs", Scope: ScopeRead,
		Description: "List workflow runs with their state, the step that failed and how long each took. Never a step's log.",
		Untrusted:   []string{"runs[].failed_step", "runs[].failed_script", "runs[].steps[].script_name", "runs[].steps[].target"},
	},
	{
		Tool: "workflow_run_show", Method: "GET", Route: "/workflow-runs/{run}", Scope: ScopeRead,
		Description: "Read one workflow run and each of its steps: state, exit code and timing. Never a step's log.",
		Untrusted:   []string{"failed_step", "failed_script", "steps[].script_name", "steps[].target"},
	},

	// --------------------------------------------------------- operate ---
	{
		Tool: "run_cycle", Method: "POST", Route: "/operations", Action: "run_cycle", Scope: ScopeOperate,
		Description: "Propose running one backup cycle across every enabled backup set. A person confirms it before anything starts.",
		Untrusted:   []string{},
	},
	{
		Tool: "run_backup_set", Method: "POST", Route: "/operations", Action: "run_backup_set", Scope: ScopeOperate,
		Description: "Propose running one backup cycle over a single backup set. A person confirms it before anything starts.",
		Untrusted:   []string{},
	},
	{
		Tool: "recovery_test_start", Method: "POST", Route: "/backup-sets/{source}/{set}/recovery-test", Scope: ScopeOperate,
		Description: "Propose a recovery test of a backup set's newest known good copy. A person confirms it before anything starts.",
		Untrusted:   []string{},
	},
	{
		Tool: "verify_copy", Method: "POST", Route: "/encryption/verify", Scope: ScopeOperate,
		Description: "Propose decrypt-verifying one backup's encrypted copy against its recorded digest. A person confirms it before anything starts.",
		Untrusted:   []string{},
	},
	{
		Tool: "scrub_start", Method: "POST", Route: "/backup-sets/{source}/{set}/scrub", Scope: ScopeOperate,
		Description: "Propose scrubbing a backup set's copies for corruption. A person confirms it before anything starts.",
		Untrusted:   []string{},
	},
	{
		Tool: "retry_backup", Method: "POST", Route: "/backups/{source}/{set}/{name}/retry", Scope: ScopeOperate,
		Description: "Propose retrying one backup that failed. A person confirms it before anything starts.",
		Untrusted:   []string{},
	},
	{
		Tool: "quarantine_revalidate", Method: "POST", Route: "/quarantine/{source}/{set}/{name}/revalidate", Scope: ScopeOperate,
		Description: "Propose validating a quarantined backup again. A person confirms it before anything starts.",
		Untrusted:   []string{},
	},

	// ------------------------------------------------------- configure ---
	{
		Tool: "backup_set_enabled", Method: "POST", Route: "/backup-sets/{source}/{set}/enabled", Scope: ScopeConfigure,
		Description: "Propose enabling or disabling one backup set. A person confirms it before it changes.",
		Untrusted:   []string{},
	},
	{
		Tool: "backup_set_retention_set", Method: "PUT", Route: "/backup-sets/{source}/{set}/retention", Scope: ScopeConfigure,
		Description: "Propose a retention override for one backup set. A person confirms it before it changes.",
		Untrusted:   []string{},
	},
	{
		Tool: "backup_set_retention_clear", Method: "DELETE", Route: "/backup-sets/{source}/{set}/retention", Scope: ScopeConfigure,
		Description: "Propose removing one backup set's retention override so it follows the deployment default. A person confirms it before it changes.",
		Untrusted:   []string{},
	},
	{
		Tool: "backup_set_assurance_set", Method: "PATCH", Route: "/backup-sets/{source}/{set}/assurance", Scope: ScopeConfigure,
		Description: "Propose changing one backup set's scrub and recovery test settings. A person confirms it before it changes.",
		Untrusted:   []string{},
	},
}

// refusalRows is every other route, grouped by the reason.
var refusalRows = join(
	// The reasons an operation action has no tool: the same route as run_cycle.
	[]Row{
		{Method: "POST", Route: "/operations", Action: "restore_placement", Why: whyOperationAction},
		{Method: "POST", Route: "/operations", Action: "restore_snapshot", Why: whyOperationAction},
		{Method: "POST", Route: "/operations", Action: "verify_snapshot", Why: whyOperationAction},
		{Method: "POST", Route: "/operations", Action: "hold_snapshot", Why: whyOperationAction},
		{Method: "POST", Route: "/operations", Action: "release_snapshot_hold", Why: whyOperationAction},
	},
	no(whyAuth,
		"POST /auth/login", "POST /auth/enroll", "POST /auth/password", "POST /auth/logout", "GET /auth/session",
		"POST /auth/forgot-password", "POST /auth/reset-password", "POST /auth/verify-email",
		"POST /auth/verify-email/resend", "GET /auth/recovery", "PATCH /auth/recovery", "POST /auth/recovery/test"),
	no(whyFirstRun, "GET /system/first-run", "POST /system/first-run", "POST /system/first-run/skip"),
	no(whyGrant, "POST /recoveries", "GET /recoveries/{grant}", "POST /backups/{source}/{set}/{name}/recovery"),
	no(whyTokens, "GET /agent/tokens", "POST /agent/tokens", "POST /agent/tokens/{id}/revoke"),
	no(whyKeys,
		"GET /encryption/keys", "POST /encryption/keys", "POST /encryption/keys/rotate",
		"POST /encryption/keys/{id}/retire", "POST /encryption/keys/{id}/export", "POST /encryption/keys/{id}/confirm"),
	no(whyProvider, "POST /encryption/providers/test"),
	no(whyCredential,
		"POST /storage-credentials", "POST /storage-mediums/preflight", "POST /storage-mediums/{id}/preflight",
		"GET /storage-mediums/{id}/configuration", "POST /storage-mediums/{id}/configuration/preflight",
		"PUT /storage-mediums/{id}/configuration"),
	no(whySSH,
		"POST /ssh-keys", "POST /ssh-keys/from-candidate", "POST /ssh/host-key-probe", "GET /ssh-keys",
		"GET /ssh/key-candidates"),
	no(whySecret,
		"GET /settings/workflow/environment", "PUT /settings/workflow/environment/{name}",
		"DELETE /settings/workflow/environment/{name}",
		"GET /backup-sets/{source}/{set}/workflow/environment",
		"PUT /backup-sets/{source}/{set}/workflow/environment/{name}",
		"DELETE /backup-sets/{source}/{set}/workflow/environment/{name}"),
	no(whyLog, "GET /workflow-runs/{run}/steps/{step}/logs"),
	no(whyScript, "GET /assurance/validators/{name}/script"),
	no(whyLifecycle,
		"POST /backup-sets", "DELETE /backup-sets/{source}/{set}", "POST /storage-mediums", "DELETE /storage-mediums/{id}"),
	no(whyDestructive,
		"POST /catalog/rebuild", "POST /backup-sets/{source}/{set}/retention/apply",
		"POST /quarantine/{source}/{set}/{name}/reinstate"),
	no(whyConfigure,
		"PATCH /backup-sets/{source}/{set}", "POST /backup-sets/{source}/{set}/read-only",
		"POST /backup-sets/{source}/{set}/edit-hold", "POST /backup-sets/{source}/{set}/edit-hold/release",
		"PUT /storage-mediums/{id}", "PUT /storage-mediums/{id}/default",
		"PATCH /settings", "PATCH /settings/workflow", "PATCH /backup-sets/{source}/{set}/workflow",
		"PATCH /settings/assurance", "DELETE /backup-sets/{source}/{set}/assurance",
		"PATCH /encryption/policy", "POST /encryption/policy/reset",
		"PUT /backup-sets/{source}/{set}/encryption", "DELETE /backup-sets/{source}/{set}/encryption",
		"POST /assurance/validators", "PUT /assurance/validators/{name}", "DELETE /assurance/validators/{name}"),
	no(whyMutation,
		"POST /backup-sets/test-connection", "POST /backup-sets/{source}/{set}/source-host/write-check",
		"POST /quarantine/{source}/{set}/{name}/retry", "POST /catalog/scan",
		"POST /assurance/operations/{op}/cancel", "POST /assurance/validator-preview",
		"POST /encryption/encrypt-existing", "POST /encryption/reencrypt",
		"POST /repositories", "POST /workflow-recovery/{run}/resume-cleanup", "POST /workflow-recovery/{run}/acknowledge"),
	no(whyCatalogue, "GET /agent/tools"),
	no(whyRead,
		"GET /system/version", "GET /system/capabilities", "GET /system/staging-directories", "GET /system/storage",
		"GET /backup-sets/{source}/{set}/snapshots", "GET /backup-sets/{source}/{set}/snapshots/{run}",
		"GET /backup-sets/{source}/{set}/holds", "GET /backup-sets/{source}/{set}/snapshot-retention",
		"GET /repositories", "GET /repositories/{domain}/maintenance",
		"GET /backup-sets/{source}/{set}/retention", "GET /backup-sets/{source}/{set}/edit-hold",
		"GET /backup-sets/{source}/{set}/source-host", "GET /storage-mediums/{id}",
		"GET /validators", "GET /backends", "GET /settings", "GET /settings/workflow",
		"GET /backup-sets/{source}/{set}/workflow", "GET /backup-sets/{source}/{set}/workflow/validation",
		"GET /settings/assurance", "GET /backup-sets/{source}/{set}/recovery-test/output",
		"GET /encryption/suites", "GET /encryption/encrypt-existing", "GET /encryption/reencrypt",
		"GET /assurance/validators", "GET /assurance/validator-patterns", "GET /assurance/default-validator-script",
		"GET /assurance/operations/{op}", "GET /workflow-runs/{run}/steps", "GET /workflow-recovery"),
)
