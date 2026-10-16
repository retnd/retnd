package cliecho

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/retnd/retnd/core/apicontract"
)

// The table: every route the /api/v1 router registers, and what an
// operator would have typed instead.
//
// A route is in exactly one of two states, and the parity test in
// apps/common/webhost is what keeps it that way. Either it has a builder,
// or it has a `why` saying there is no equivalent. A route added to the
// router with neither fails that test, which is the whole point of this
// file: a UI action with no command to name becomes a visible line in a
// panel an operator is reading, rather than a gap somebody finds at an
// audit nobody runs.
//
// The `why` lines are written for an operator, not for us. "No equivalent"
// on its own is not actionable; "there is no verb that does X" says what
// would have to be built.
//
// A `why` is also a claim about this binary, and claims go stale: five of
// these said a verb did not exist while the same tree shipped it, and one
// of the five quoted a usage() line that had been replaced by the flag it
// was denying. core/cmd/retnd's TestNoGapClaimsAVerbThisBinaryShips
// reads every sentence here against the verb tables now. A sentence that
// names a shipped verb on purpose, as a counterexample ("`retnd
// run` is not this"), says so with namesShippedVerbs, and a sentence that
// names one by accident fails.
//
// The request bodies are core/apicontract's own types rather than a map
// read by string keys, deliberately. That is what makes a field renamed in
// the contract a compile error here instead of a builder that quietly
// stops emitting a flag.
// The sentences a builder prints in place of a command, as constants
// because each of them is written in two places: the builder that refuses
// with it, and the entry that declares it so Gaps can report it. A
// sentence that could differ between those two would be a sentence the
// guard in core/cmd/retnd checks a copy of.
const (
	gapRunCycle = "`" + Binary + " run` starts a cycle in your own shell, not in this engine, so it is a different act against a different process"

	gapRunBackupSet = "`" + Binary + " fetch --backup-set <source/backup-set>` runs that set's cycle in your own shell, not in this engine, so it is a different act against a different process"

	// Issue #624. The candidate half of the connection check has no verb
	// and deliberately gets none, which is a decision rather than the next
	// gap to close. A candidate is a form somebody is still typing: its
	// values exist nowhere, so the command would have to carry all of
	// them, and the moment they are worth carrying is the create, which
	// proves the connection before it writes anything.
	gapCandidateConnection = "there is no verb that checks a source that is not saved yet: `" + Binary + " backup-set create` proves the connection before it writes, and `" + Binary + " backup-set test-connection <source/backup-set>` re-checks one that exists"
)

// positiveQuery reads a query parameter that is a count, and reports 0
// for anything the route itself would treat as absent.
//
// The routes that take one (a limit on the journal read and on the live
// feed) treat an absent, unparseable or non-positive value alike, as the
// backend's own default. A line printing --limit 0 or --limit banana
// would name a value the request did not make and, in the second case,
// one the binary would refuse.
func positiveQuery(a Action, name string) int {
	raw := a.Query.Get(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

var routes = map[string]entry{
	// ---------------------------------------------------------- system ---
	key("GET", "/system/version"): {
		// --engine, because this route is the ENGINE's version, and bare
		// `version` is this binary's own (#1152).
		build:    func(Action) *cmd { return newCmd("version").bare("engine") },
		examples: []Action{{}},
	},
	key("GET", "/system/health"): {
		build:    func(Action) *cmd { return newCmd("status") },
		examples: []Action{{}},
	},
	key("GET", "/system/capabilities"): {
		// #1165. It used to be a gap ("nothing on a terminal asks it"); the
		// terminal asks it now, and it is the way to read whether the
		// engine's destructive gate is open.
		build:    func(Action) *cmd { return newCmd("system", "capabilities") },
		examples: []Action{{}},
	},
	key("GET", "/system/staging-directories"): {
		// #1179. The staging folder browser's route: the terminal lists the same folders with `system staging`, and a path
		// given to the page is the PATH given here.
		build: func(a Action) *cmd {
			c := newCmd("system", "staging")
			if p := a.Query.Get("path"); p != "" {
				c.argv = append(c.argv, p)
			}
			return c
		},
		examples: []Action{{}, {Query: url.Values{"path": {"daily/2026-10"}}}},
	},
	key("GET", "/system/storage"): {
		build:    func(Action) *cmd { return newCmd("storage") },
		examples: []Action{{}},
	},
	key("GET", "/system/first-run"): {
		build:    func(Action) *cmd { return newCmd("system", "first-run") },
		examples: []Action{{}},
	},
	key("POST", "/system/first-run/skip"): {
		why: "skipping a step of the first-run guide is a choice about what the web interface shows next; a terminal has no guide to leave a step of",
	},
	key("POST", "/system/first-run"): {
		// The first config.yaml, which usage() puts on `backup-set
		// create`: "On an instance with no config.yaml yet this writes
		// the first one instead (#176)". Same act, same verb.
		build: func(a Action) *cmd {
			var spec apicontract.BackupSetSpec
			if !decode(a.Body, &spec) {
				return nil
			}
			return backupSetCreateCommand(spec, false, false)
		},
		why:      "there is no verb that writes a first configuration from a request body",
		examples: []Action{{Body: []byte(`{"source_name":"api-server","name":"var-backups","host":"10.0.0.14","port":22,"user":"backups","remote_path":"/var/backups","local_path":"/data/backups","ssh_key_id":"key_1","known_hosts_line":"10.0.0.14 ssh-ed25519 AAAAC3Nz","completion_strategy":"rename","include":["*.gz"],"stale_after_seconds":172800,"validator_id":"gzip","read_only":true,"disabled":true}`)}},
	},

	// ------------------------------------------------------ operations ---
	key("POST", "/operations"): {
		// Three actions arrive here and they are three different acts, so
		// this switch is on apicontract's own constants rather than on
		// string literals. It used to be on "restore", which is not one
		// of the three and never was: the client sends restore_placement
		// and run_backup_set, so NO real request matched that arm. Every
		// restore and every per-set run fell through to a default whose
		// sentence is about `retnd run`, a different verb for a
		// different act, and the example body said "restore" too, so the
		// dispatcher-driven parse test certified a branch production
		// never reaches. A constant spelled in one place cannot be wrong
		// in one of them.
		build: func(a Action) *cmd {
			var req apicontract.SubmitOperationRequest
			if !decode(a.Body, &req) {
				return nil
			}
			switch req.Action {
			case apicontract.ActionRestorePlacement:
				if req.Restore == nil {
					return nil
				}
				c := newCmd("restore", req.Restore.ArtifactID).flag("medium", req.Restore.Medium)
				if req.Restore.WindowDays > 0 {
					c.flag("days", itoa(req.Restore.WindowDays))
				}
				if req.Restore.Acknowledged {
					c.bare("acknowledge")
				}
				return c
			case apicontract.ActionRunBackupSet:
				// Its own sentence, because it is its own act. `fetch`
				// runs one backup set's cycle and is the verb an operator
				// reaches for, and it is not this for the same reason
				// `run` is not a run_cycle: usage() puts both among the
				// commands that are "ordinary beside a running engine",
				// so it opens the service in the operator's own process
				// and runs the set THERE, while this asks the serving
				// engine to run it. Answering with run_cycle's sentence,
				// which is what happened until #599's review, sends
				// somebody asking about one set to a verb about all of
				// them.
				return newCmd().refuse(gapRunBackupSet)
			case apicontract.ActionRestoreSnapshot:
				// The gap this arm used to print is closed (#788):
				// `snapshot restore` is the verb, and it is deliberately
				// NOT `restore`, which is the archived-copy retrieval on
				// the arm above -- printing that one here would answer
				// "get this file back out of last night's restore point"
				// with a billed provider retrieval of a different object.
				if req.SnapshotRestore == nil {
					return nil
				}
				c := newCmd("snapshot", "restore", req.SnapshotRestore.BackupSetID).
					flag("to", req.SnapshotRestore.TargetPath)
				c.flagIfSet("snapshot", req.SnapshotRestore.SnapshotID)
				c.flagIfSet("path", req.SnapshotRestore.SourcePath)
				c.flagIfSet("conflict", req.SnapshotRestore.Conflict)
				return c
			case apicontract.ActionVerifySnapshot:
				if req.SnapshotVerify == nil {
					return nil
				}
				c := newCmd("snapshot", "verify", req.SnapshotVerify.BackupSetID)
				c.flagIfSet("run", req.SnapshotVerify.RunID)
				c.flagIfSet("level", req.SnapshotVerify.Level)
				if req.SnapshotVerify.SamplePercent > 0 {
					c.flag("sample-percent", itoa(req.SnapshotVerify.SamplePercent))
				}
				return c
			case apicontract.ActionHoldSnapshot:
				if req.SnapshotHold == nil {
					return nil
				}
				c := newCmd("snapshot", "hold", req.SnapshotHold.BackupSetID).
					flag("reason", req.SnapshotHold.Reason)
				c.flagIfSet("run", req.SnapshotHold.RunID)
				return c
			case apicontract.ActionReleaseSnapshotHold:
				if req.SnapshotHoldRelease == nil {
					return nil
				}
				return newCmd("snapshot", "unhold", req.SnapshotHoldRelease.BackupSetID, req.SnapshotHoldRelease.HoldID)
			default:
				// The gap the issue names, and the one a lazier
				// implementation gets wrong. `retnd run`
				// exists and is NOT this: usage() puts it among the
				// commands that are "ordinary beside a running engine",
				// so it opens the service in the operator's own process
				// and runs a cycle there. This asks the SERVING engine
				// to run one. Printing `retnd run` would print
				// a command that does something different to a
				// different process.
				return newCmd().refuse(gapRunCycle)
			}
		},
		// A body that will not decode names no action at all, so the
		// route-level sentence is the one every other builder here gives
		// for that case rather than one of the two below, which are
		// answers about a specific act.
		why:               "there is no verb that submits an operation from a request body",
		refusals:          []string{gapRunCycle, gapRunBackupSet},
		namesShippedVerbs: []string{"run", "fetch"},
		examples: []Action{
			{Body: []byte(`{"action":"` + apicontract.ActionRestorePlacement + `","config_revision":"r1","restore":{"artifact_id":"api-server/var-backups/dump.tar","medium":"offsite_s3","window_days":7,"acknowledged":true}}`)},
			{Body: []byte(`{"action":"` + apicontract.ActionRunBackupSet + `","config_revision":"r1","backup_set_id":"api-server/var-backups"}`)},
			{Body: []byte(`{"action":"` + apicontract.ActionRunCycle + `","config_revision":"r1"}`)},
			{Body: []byte(`{"action":"` + apicontract.ActionRestoreSnapshot + `","config_revision":"r1","snapshot_restore":{"backup_set_id":"api-server/var-backups","target_path":"/srv/restored","snapshot_id":"k1234","source_path":"var/lib/pg","conflict":"skip"}}`)},
			{Body: []byte(`{"action":"` + apicontract.ActionVerifySnapshot + `","config_revision":"r1","snapshot_verify":{"backup_set_id":"api-server/var-backups","run_id":"run_1","level":"content_sample","sample_percent":10}}`)},
			{Body: []byte(`{"action":"` + apicontract.ActionHoldSnapshot + `","config_revision":"r1","snapshot_hold":{"backup_set_id":"api-server/var-backups","run_id":"run_1","reason":"kept for the incident review"}}`)},
			{Body: []byte(`{"action":"` + apicontract.ActionReleaseSnapshotHold + `","config_revision":"r1","snapshot_hold_release":{"backup_set_id":"api-server/var-backups","hold_id":"hold_1"}}`)},
		},
	},
	key("GET", "/operations"): {
		build:    func(Action) *cmd { return newCmd("operation", "list") },
		examples: []Action{{}},
	},
	key("GET", "/operations/{id}"): {
		build: func(a Action) *cmd {
			return newCmd("operation", "show", a.Params["id"])
		},
		examples: []Action{{Params: map[string]string{"id": "7b0c9a1e-0000-4000-8000-000000000002"}}},
	},

	// ----------------------------------------------------- backup sets ---
	key("GET", "/backup-sets"): {
		build:    func(Action) *cmd { return newCmd("sources") },
		examples: []Action{{}},
	},
	key("GET", "/backup-sets/*"): {
		// Issue #1151 gave this a verb. It was a gap that said "`sources`
		// lists every configured backup set; there is no verb that prints
		// one on its own", and `backup-set show` is that verb.
		build:    func(a Action) *cmd { return newCmd("backup-set", "show", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("POST", "/backup-sets"): {
		build: func(a Action) *cmd {
			var req apicontract.CreateBackupSetRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return backupSetCreateCommand(req.BackupSetSpec, req.RunImmediately, req.AcknowledgeRepoint)
		},
		why: "there is no verb that creates a backup set from a request body",
		examples: []Action{
			{Body: []byte(`{"source_name":"api-server","name":"var-backups","host":"10.0.0.14","user":"backups","remote_path":"/var/backups","local_path":"/data/backups","ssh_key_id":"key_1","known_hosts_line":"10.0.0.14 ssh-ed25519 AAAAC3Nz","completion_strategy":"stable","stable_for_seconds":300,"run_immediately":true,"acknowledge_repoint":true}`)},
			// The incremental shape (#788), which is the one the line
			// used to get WRONG rather than incomplete: every flag below
			// was dropped, so a kopia set into a declared repository
			// domain printed a command that makes a whole-file artifact
			// set under the same id.
			{Body: []byte(`{"source_name":"api-server","name":"var-backups","host":"10.0.0.14","user":"backups","remote_path":"/var/backups","local_path":"/data/backups","ssh_key_id":"key_1","known_hosts_line":"10.0.0.14 ssh-ed25519 AAAAC3Nz","completion_strategy":"rename","engine":"kopia","repository_domain":"production-vault","source_consistency":"quiesced","verification_level":"sample","verification_sample_percent":10,"verification_full_every_seconds":604800,"verification_restore_drill_every_seconds":2592000}`)},
			// Re-declaring a set under its old lineage key (#1151).
			{Body: []byte(`{"source_name":"api-server","name":"var-backups","host":"10.0.0.14","user":"backups","remote_path":"/srv/var-backups","local_path":"/data/backups","ssh_key_id":"key_1","known_hosts_line":"10.0.0.14 ssh-ed25519 AAAAC3Nz","completion_strategy":"rename","engine":"kopia","repository_domain":"production-vault","uuid":"7d1c5a6e-3b8f-4c2a-9e41-0f6b2d8a5c13","source_mount_prefix":"/srv"}`)},
		},
	},
	key("POST", "/backup-sets/test-connection"): {
		// This entry was a `why` line for two issues. #596 built the
		// check, so the route answered with six named steps rather than a
		// boolean, and nothing could reach it from a terminal; the gap
		// sentence named the verb that had to exist rather than saying
		// "no equivalent", which is what makes a gap actionable. #624
		// shipped that verb, so this is a builder now.
		//
		// Only the PERSISTED mode has one. A candidate check is the step
		// the wizard runs while an operator is still filling a form in,
		// against values that exist nowhere yet: the equivalent command
		// would have to carry the whole unsaved form, and what a terminal
		// can do with it is run the create, which verifies before it
		// writes. So the candidate mode refuses with the reason rather
		// than printing a command that checks something else.
		build: func(a Action) *cmd {
			var req apicontract.TestConnectionRequest
			if !decode(a.Body, &req) {
				return nil
			}
			if req.BackupSetID == "" {
				return newCmd().refuse(gapCandidateConnection)
			}
			return newCmd("backup-set", "test-connection", req.BackupSetID)
		},
		why:      "there is no verb that checks a source that has not been saved yet",
		refusals: []string{gapCandidateConnection},
		// `backup-set` is named as the counterexample rather than as the
		// gap: the refusal's whole point is that the thing to run instead
		// is `backup-set create`, which proves the connection before it
		// writes, or `backup-set test-connection` once the set exists.
		namesShippedVerbs: []string{"backup-set"},
		examples: []Action{
			{Body: []byte(`{"backup_set_id":"api-server/var-backups"}`)},
			{Body: []byte(`{"host":"10.0.0.14","user":"backups","ssh_key_id":"key_1","known_hosts_line":"10.0.0.14 ssh-ed25519 AAAAC3Nz","remote_path":"/var/backups"}`)},
		},
	},
	key("PATCH", "/backup-sets/{source}/{set}"): {
		build: func(a Action) *cmd {
			var req apicontract.UpdateBackupSetRequest
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("backup-set", "patch", setID(a))
			if req.Host != nil {
				c.flag("host", *req.Host)
			}
			if req.Port != nil {
				c.flag("port", itoa(*req.Port))
			}
			if req.User != nil {
				c.flag("user", *req.User)
			}
			if req.RemotePath != nil {
				c.flag("remote-path", *req.RemotePath)
			}
			if req.LocalPath != nil {
				c.flag("local-path", *req.LocalPath)
			}
			if req.Include != nil {
				c.flag("include", strings.Join(*req.Include, ","))
			}
			if req.CompletionStrategy != nil {
				c.flag("completion-strategy", *req.CompletionStrategy)
			}
			if req.StableForSeconds != nil {
				c.flag("stable-for", seconds(int64(*req.StableForSeconds)))
			}
			if req.StaleAfterSeconds != nil {
				c.flag("stale-after", seconds(int64(*req.StaleAfterSeconds)))
			}
			if req.ValidatorID != nil {
				c.flag("validator-id", *req.ValidatorID)
			}
			// Issue #1151. Zero is the request to inherit the
			// deployment's cadence and is printed, for the reason the
			// verification cadences below are.
			if req.PollIntervalSeconds != nil {
				c.flag("poll-interval", seconds(int64(*req.PollIntervalSeconds)))
			}
			// EPIC K's editable verification budget (#788), with the
			// nil/non-nil rule every pointer on this body keeps. An
			// explicit zero cadence is "stop doing this", which is a
			// request an operator makes when a nightly drill turns out
			// to cost more than it is worth, so it is printed rather
			// than skipped as if nothing had been said.
			if req.SourceConsistency != nil {
				c.flag("source-consistency", *req.SourceConsistency)
			}
			if req.VerificationLevel != nil {
				c.flag("verification-level", *req.VerificationLevel)
			}
			if req.VerificationSamplePercent != nil {
				c.flag("verification-sample-percent", itoa(*req.VerificationSamplePercent))
			}
			if req.VerificationFullEverySeconds != nil {
				c.flag("verification-full-every", seconds(*req.VerificationFullEverySeconds))
			}
			if req.VerificationRestoreDrillEverySeconds != nil {
				c.flag("verification-restore-drill-every", seconds(*req.VerificationRestoreDrillEverySeconds))
			}
			if req.SSHKeyID != nil {
				c.flag("ssh-key-id", *req.SSHKeyID)
			}
			if req.KnownHostsLine != nil {
				c.flag("known-hosts-line", *req.KnownHostsLine)
			}
			if req.AcknowledgeRepoint {
				c.bare("acknowledge-repoint")
			}
			if req.AcknowledgeHostKeyChange {
				c.bare("acknowledge-host-key-change")
			}
			return c
		},
		why: "there is no verb that edits a backup set from a request body",
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"stale_after_seconds":172800}`)},
			// The duration shapes, and they are examples rather than a
			// unit test's table because this is the corpus the dispatcher
			// is driven with in core/cmd/retnd: a shape no
			// example carries is a shape nothing parses end to end. These
			// three are the ones the old renderer got wrong (a bare
			// seconds value, a value whose last unit ends in a zero, and
			// an hour whose minutes component is empty), and they were
			// missing for the reason such gaps usually are: the two
			// values above are the two it happened to render correctly.
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"stale_after_seconds":30,"stable_for_seconds":600}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"stale_after_seconds":3610,"stable_for_seconds":90}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"host":"10.0.0.15","port":2222,"user":"backups","remote_path":"/var/backups","local_path":"/data/backups","include":["*.gz","*.sql"],"completion_strategy":"stable","stable_for_seconds":300,"validator_id":"gzip","ssh_key_id":"key_2","known_hosts_line":"10.0.0.15 ssh-ed25519 AAAAC3Nz","acknowledge_repoint":true,"acknowledge_host_key_change":true}`)},
			// EPIC K's verification budget (#788), in the corpus rather
			// than only in a unit test for the reason the duration
			// shapes above are: this is what core/cmd/retnd's
			// dispatcher parses end to end, so a flag no example carries
			// is a flag nothing proves the binary takes back.
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"source_consistency":"quiesced","verification_level":"sample","verification_sample_percent":10,"verification_full_every_seconds":604800,"verification_restore_drill_every_seconds":2592000}`)},
			// The per-set poll interval (#1151), once overriding and once
			// returning to the deployment's.
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"poll_interval_seconds":900}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"poll_interval_seconds":0}`)},
		},
	},
	key("DELETE", "/backup-sets/{source}/{set}"): {
		build:    func(a Action) *cmd { return newCmd("backup-set", "remove", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	// The two post-creation toggles (#788). Both were gaps until this
	// issue, and the gap sentences said so honestly: --disabled and
	// --read-only are in backupSetCreateOnlyFlags, so `backup-set patch`
	// refuses them, and a set created disabled from a terminal could
	// never be enabled from one again. Each now has a verb of its own,
	// taking one word rather than a flag, because "on" and "off" read as
	// instructions where a bare --read-only has no off and a
	// --read-only=false is a spelling people get wrong under pressure.
	key("POST", "/backup-sets/{source}/{set}/enabled"): {
		build: func(a Action) *cmd {
			var req apicontract.SetEnabledRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return newCmd("backup-set", "enabled", setID(a), onOff(req.Enabled))
		},
		why: "there is no verb that enables or disables a set from a request body",
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}, Body: []byte(`{"enabled":true}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}, Body: []byte(`{"enabled":false}`)},
		},
	},
	key("POST", "/backup-sets/{source}/{set}/read-only"): {
		build: func(a Action) *cmd {
			var req apicontract.SetReadOnlyRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return newCmd("backup-set", "read-only", setID(a), onOff(req.ReadOnly))
		},
		why: "there is no verb that changes a set's read-only posture from a request body",
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}, Body: []byte(`{"read_only":true}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}, Body: []byte(`{"read_only":false}`)},
		},
	},
	// EPIC K's snapshot and repository reads (#788). Every one has a
	// verb, which is the whole point of shipping the CLI and the API
	// together: a screen an operator is reading can print the command
	// that would have shown them the same thing.
	key("GET", "/backup-sets/{source}/{set}/snapshots"): {
		build:    func(a Action) *cmd { return newCmd("snapshot", "list", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("GET", "/backup-sets/{source}/{set}/snapshots/{run}"): {
		build: func(a Action) *cmd {
			return newCmd("snapshot", "show", setID(a), a.Params["run"])
		},
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups", "run": "run_1"}}},
	},
	key("GET", "/backup-sets/{source}/{set}/holds"): {
		build:    func(a Action) *cmd { return newCmd("snapshot", "holds", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("GET", "/backup-sets/{source}/{set}/snapshot-retention"): {
		build:    func(a Action) *cmd { return newCmd("snapshot", "retention", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("GET", "/repositories"): {
		build:    func(Action) *cmd { return newCmd("repository", "health") },
		examples: []Action{{}},
	},
	key("POST", "/repositories"): {
		// `repository create` writes the same declaration through the
		// same door, so the line is the whole request: the id as the
		// operand, the co-tenancy posture, and where the passphrase is
		// read from.
		//
		// The passphrase's three spellings are echoed exactly as the
		// storage destination's four credential spellings are, and for
		// the same reasons: --passphrase-file is a PATH, --passphrase-env
		// is a variable NAME, and --passphrase-command is named and never
		// printed, because its words are the caller's and `printf %s
		// hunter2` is a valid passphrase command and a secret on a
		// command line.
		build: func(a Action) *cmd {
			var req apicontract.CreateRepositoryDomainRequest
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("repository", "create", req.ID)
			c.flagIfSet("isolation", req.Isolation)
			c.flagIfSet("description", req.Description)
			c.flagIfSet("location", req.Location)
			c.flagIfSet("owner", req.MaintenanceOwner)
			c.flagIfSet("passphrase-file", req.Passphrase.File)
			c.flagIfSet("passphrase-env", req.Passphrase.Env)
			if len(req.Passphrase.Command) > 0 {
				c.placeholderFlag("passphrase-command", "the command that prints this repository's passphrase")
			}
			return c
		},
		why: "there is no verb that declares a repository domain from a request body",
		examples: []Action{
			{Body: []byte(`{"id":"offsite-b2","isolation":"isolated","description":"Second copy, off site","passphrase":{"file":"/etc/retnd/offsite-b2.passphrase"}}`)},
			{Body: []byte(`{"id":"production","isolation":"shared","maintenance_owner":"another-instance","passphrase":{"env":"RETND_PRODUCTION_PASSPHRASE"}}`)},
		},
	},
	key("GET", "/repositories/{domain}/maintenance"): {
		build: func(a Action) *cmd {
			return newCmd("repository", "maintenance", a.Params["domain"])
		},
		examples: []Action{{Params: map[string]string{"domain": "production-vault"}}},
	},
	key("GET", "/backup-sets/{source}/{set}/retention"): {
		build:    func(a Action) *cmd { return newCmd("backup-set", "retention", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("PUT", "/backup-sets/{source}/{set}/retention"): {
		build: func(a Action) *cmd {
			var req apicontract.RetentionOverride
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("backup-set", "retention", setID(a))
			wrote := false
			if len(req.Tiers) > 0 {
				// A whole tier chain does not fit on a command line and
				// the CLI does not pretend it does: --policy-file takes
				// the contents of a config.yaml "retention:" block. So
				// the flag is named and its value is a placeholder,
				// which is what makes the line say it is not runnable as
				// printed rather than quietly wrong.
				//
				// And it is named ALONE. --policy-file carries the whole
				// retention section, so `backup-set retention` refuses it
				// beside --timezone and the rest (backupsetretention.go's
				// second mutual-exclusion rule); a body carrying tiers
				// and a timezone would otherwise have printed a line the
				// binary exits 2 on. Those values are in the block, which
				// is what the placeholder says.
				c.placeholderFlag("policy-file", "a file holding this whole retention: block")
				wrote = true
			} else {
				if req.Timezone != "" {
					c.flag("timezone", req.Timezone)
					wrote = true
				}
				if req.WeekStartsOn != "" {
					c.flag("week-starts-on", req.WeekStartsOn)
					wrote = true
				}
				if req.DailyDays > 0 {
					c.flag("daily-days", itoa(req.DailyDays))
					wrote = true
				}
				if req.WeeklyMonths > 0 {
					c.flag("weekly-months", itoa(req.WeeklyMonths))
					wrote = true
				}
				if req.MonthlyMonths > 0 {
					c.flag("monthly-months", itoa(req.MonthlyMonths))
					wrote = true
				}
				if req.ProtectLastKnownGood != nil {
					c.assigned("protect-last-known-good", *req.ProtectLastKnownGood)
					wrote = true
				}
			}
			// The acknowledgement consents to a policy write, and the CLI
			// refuses it on a command line that writes no policy, so it
			// only goes on beside one.
			if req.AcknowledgeMediumDisclosure && wrote {
				c.bare("acknowledge-medium-disclosure")
			}
			return c
		},
		why: "there is no verb that sets a backup set's retention policy from a request body",
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"daily_days":7,"weekly_months":3,"monthly_months":12,"timezone":"Europe/Berlin","week_starts_on":"monday","protect_last_known_good":false,"acknowledge_medium_disclosure":true}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"tiers":[{"name":"daily","granularity":"day","keep":7}]}`)},
			// A chain AND the scalars that go in the block with it, which
			// is the shape that used to print --policy-file beside
			// --timezone and get exit 2.
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"tiers":[{"name":"daily","granularity":"day","keep":7}],"timezone":"Europe/Berlin","week_starts_on":"monday","acknowledge_medium_disclosure":true}`)},
		},
	},
	key("DELETE", "/backup-sets/{source}/{set}/retention"): {
		build: func(a Action) *cmd {
			return newCmd("backup-set", "retention", setID(a)).bare("inherit")
		},
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("GET", "/backup-sets/{source}/{set}/retention/preview"): {
		build:    func(a Action) *cmd { return newCmd("retention", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("POST", "/backup-sets/{source}/{set}/retention/apply"): {
		// This said `retention` on a terminal previews and deletes
		// nothing, quoting a usage() line that #602 has since replaced:
		// `retention apply <source/backup-set> --acknowledge` is in this
		// tree, in retentionapply.go, and it deletes.
		//
		// The plan_id is not on the line and cannot be: over HTTP a plan
		// is issued, rendered for an administrator and applied against a
		// fingerprint of exactly what they were shown, while on a
		// terminal the preview and the apply are one invocation and the
		// person is the one who typed it. Both go through the same
		// PreviewRetention/ApplyRetentionPlan pair and both are refused
		// with RETENTION_PLAN_STALE if anything moved in between, so the
		// act is the same act. --acknowledge is the terminal's half of
		// the confirmation and is required, which is why it is on the
		// line rather than conditional on anything in the body.
		build: func(a Action) *cmd {
			return newCmd("retention", "apply", setID(a)).bare("acknowledge")
		},
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"},
			Body: []byte(`{"plan_id":"plan_01HX"}`)}},
	},
	// Two of these three are commands now. #600 built `backup-set
	// edit-hold <source/backup-set> [--release]`, which reports a hold
	// and gives one back, so the entries saying no verb read or released
	// one were describing a tree that had one open in the next file.
	key("GET", "/backup-sets/{source}/{set}/edit-hold"): {
		build:    func(a Action) *cmd { return newCmd("backup-set", "edit-hold", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("POST", "/backup-sets/{source}/{set}/edit-hold"): {
		// The one that is still a gap, and usage() says the same thing in
		// as many words: "There is no verb that TAKES a hold". A hold
		// protects an editing session, a browser has one and a terminal
		// does not, and a CLI edit is one `backup-set patch` that either
		// runs or does not.
		why:               "`backup-set edit-hold` reports a hold and releases one, and no verb TAKES one and leaves it held: a hold outliving the command that took it is exactly what a browser needs and a terminal does not",
		namesShippedVerbs: []string{"backup-set"},
	},
	key("POST", "/backup-sets/{source}/{set}/edit-hold/release"): {
		build: func(a Action) *cmd {
			return newCmd("backup-set", "edit-hold", setID(a)).bare("release")
		},
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},

	// -------------------------------------------------------- backups ---
	key("GET", "/backups"): {
		build: func(a Action) *cmd {
			c := newCmd("artifacts")
			// The route's one filter is setId (handlers_artifacts.go). It
			// reads nothing else, so nothing else is echoed: a printed
			// --source would show more than the panel being read.
			if set := a.Query.Get("setId"); set != "" {
				c.flag("backup-set", set)
			}
			return c
		},
		examples: []Action{
			{},
			{Query: mustQuery("setId=api-server/var-backups")},
		},
	},
	key("GET", "/backups/{source}/{set}/{name}"): {
		build:    func(a Action) *cmd { return newCmd("artifacts", artifactID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"}}},
	},
	key("POST", "/backups/{source}/{set}/{name}/recovery"): {
		build: func(a Action) *cmd {
			var req apicontract.RecoveryMintRequest
			c := newCmd("recover", artifactID(a))
			if !decode(a.Body, &req) {
				return c
			}
			if req.Copy != "" && req.Copy != "best-available" {
				c.flag("copy", req.Copy)
			}
			switch req.Target.Kind {
			case "download":
				c.flag("to", "-")
			case "staging":
				c.flag("to", stagingName(req.Target.Path))
			case "storage":
				to := "medium:" + req.Target.Medium
				if req.Target.Key != "" {
					to += "/" + req.Target.Key
				}
				c.flag("to", to)
			case "source":
				sourceTarget(c, req.Target.Path, req.Target.AcknowledgedDestination)
			}
			if req.Acknowledged {
				c.bare("acknowledge-cost")
			}
			return c
		},
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"},
				Body: []byte(`{"target":{"kind":"download"}}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"},
				Body: []byte(`{"copy":"offsite_s3","acknowledged":true,"target":{"kind":"staging","path":"restore/dump.tar"}}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"},
				Body: []byte(`{"target":{"kind":"storage","medium":"offsite_s3","key":"recovered/dump.tar"}}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"},
				Body: []byte(`{"target":{"kind":"source","path":"retnd-recovered-20261015-101500-a1b2","acknowledged_destination":"sh1-0123456789abcdef0123456789abcdef"}}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"},
				Body: []byte(`{"target":{"kind":"staging","path":"source:old"}}`)},
		},
	},
	key("POST", "/recoveries"): {
		build: func(a Action) *cmd {
			var req apicontract.RecoveryManyRequest
			c := newCmd("recover")
			if !decode(a.Body, &req) {
				return c
			}
			if req.Set != "" {
				c.flag("set", req.Set)
			}
			for _, id := range req.Artifacts {
				c.argv = append(c.argv, id)
			}
			if req.NewestOnly {
				c.bare("newest-only")
			}
			c.flagIfSet("since", req.Since)
			if req.Copy != "" && req.Copy != "best-available" {
				c.flag("copy", req.Copy)
			}
			switch req.Target.Kind {
			case "staging":
				c.flag("to", stagingName(req.Target.Path))
			case "storage":
				to := "medium:" + req.Target.Medium
				if req.Target.Key != "" {
					to += "/" + req.Target.Key
				}
				c.flag("to", to)
			case "source":
				sourceTarget(c, req.Target.Path, req.Target.AcknowledgedDestination)
				// A whole set onto the source host is spelled out (#1193):
				// the terminal refuses it without a narrowing or --all.
				if req.Set != "" && !req.NewestOnly && req.Since == "" && !req.Preflight {
					c.bare("all")
				}
			}
			if req.Preflight {
				c.bare("preflight")
			}
			if req.Acknowledged {
				c.bare("acknowledge-cost")
			}
			return c
		},
		examples: []Action{
			{Body: []byte(`{"set":"api-server/var-backups","target":{"kind":"staging","path":"restore/var-backups"}}`)},
			{Body: []byte(`{"set":"api-server/var-backups","newest_only":true,"acknowledged":true,"target":{"kind":"storage","medium":"offsite_s3","key":"recovered"}}`)},
			{Body: []byte(`{"set":"api-server/var-backups","since":"2026-10-01T00:00:00Z","preflight":true,"target":{"kind":"staging","path":"restore/var-backups"}}`)},
			{Body: []byte(`{"artifacts":["api-server/var-backups/a.tar","api-server/var-backups/b.tar"],"target":{"kind":"staging","path":"restore/picked"}}`)},
			{Body: []byte(`{"set":"api-server/var-backups","target":{"kind":"source","path":"retnd-recovered-20261015-101500-a1b2","acknowledged_destination":"sh1-0123456789abcdef0123456789abcdef"}}`)},
			{Body: []byte(`{"set":"api-server/var-backups","newest_only":true,"target":{"kind":"source","path":"retnd-recovered-20261015-101500-a1b2","acknowledged_destination":"sh1-0123456789abcdef0123456789abcdef"}}`)},
			{Body: []byte(`{"set":"api-server/var-backups","preflight":true,"target":{"kind":"source","path":"retnd-recovered-20261015-101500-a1b2"}}`)},
			{Body: []byte(`{"artifacts":["api-server/var-backups/a.tar"],"target":{"kind":"staging","path":"-odd"}}`)},
			{Body: []byte(`{"artifacts":["api-server/var-backups/a.tar"],"target":{"kind":"staging","path":"medium:x"}}`)},
		},
	},
	key("GET", "/backup-sets/{source}/{set}/source-host"): {
		why:               "describing where a source-host recovery would write has no verb of its own, because the terminal never describes without checking: `" + Binary + " recover --set <source/backup-set> --to source: --preflight` prints the same description and then runs the write check, which writes and removes one probe file on that host",
		namesShippedVerbs: []string{"recover"},
	},
	key("POST", "/backup-sets/{source}/{set}/source-host/write-check"): {
		build: func(a Action) *cmd {
			c := newCmd("recover")
			c.flag("set", a.Params["source"]+"/"+a.Params["set"])
			c.flag("to", "source:"+a.Query.Get("folder"))
			c.bare("preflight")
			return c
		},
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}, Query: mustQuery("folder=retnd-recovered-20261015-101500-a1b2")},
		},
	},
	key("GET", "/recoveries/{grant}"): {
		why:               "a grant is minted and redeemed in one step by `" + Binary + " recover`, in your own shell, so there is no verb that redeems a grant somebody else minted: the token belongs to the browser session that asked for it",
		namesShippedVerbs: []string{"recover"},
	},
	key("POST", "/backups/{source}/{set}/{name}/retry"): {
		build: func(a Action) *cmd {
			var req apicontract.RetryFailedRequest
			c := newCmd("retry", artifactID(a))
			if decode(a.Body, &req) && req.Note != "" {
				c.flag("note", req.Note)
			}
			return c
		},
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"}},
			{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"},
				Body: []byte(`{"note":"the NAS came back"}`)},
		},
	},

	// ------------------------------------------------------ quarantine ---
	key("GET", "/quarantine"): {
		build:    func(a Action) *cmd { return newCmd("quarantine", "list") },
		examples: []Action{{}},
	},
	key("POST", "/quarantine/{source}/{set}/{name}/revalidate"): {
		build:    func(a Action) *cmd { return newCmd("quarantine", "revalidate", artifactID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"}}},
	},
	key("POST", "/quarantine/{source}/{set}/{name}/retry"): {
		build:    func(a Action) *cmd { return newCmd("quarantine", "retry", artifactID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"}}},
	},
	key("POST", "/quarantine/{source}/{set}/{name}/reinstate"): {
		build:    func(a Action) *cmd { return newCmd("quarantine", "reinstate", artifactID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"}}},
	},

	// -------------------------------------------------------- activity ---
	// Both of these said "there is no verb yet" and both verbs are in
	// this same tree: #598 built `activity` and `activity --follow`, and
	// main.go has dispatched them since. A gap that outlives the thing it
	// describes is worse than no gap at all, because it tells an operator
	// a feature does not exist while the binary beside them ships it, and
	// TestNoGapClaimsAVerbThisBinaryShips is what stops it happening
	// again.
	key("GET", "/activity"): {
		build: func(a Action) *cmd {
			c := newCmd("activity")
			// The one query parameter this route reads that a command can
			// say. The handler treats an absent, unparseable or
			// non-positive value as the backend's own default, so a line
			// that printed --limit 0 would be naming a value the request
			// did not make.
			if n := positiveQuery(a, "limit"); n > 0 {
				c.flag("limit", itoa(n))
			}
			// `before` is the page behind a cursor, and since #1152 the
			// binary has a flag for it. Only a cursor the feed could have
			// issued is echoed (a positive row id), because the route reads
			// anything else as no cursor and the binary refuses it: a line
			// printing --before banana would be a command that exits 2.
			if n := positiveQuery(a, "before"); n > 0 {
				c.flag("before", itoa(n))
			}
			return c
		},
		examples: []Action{{}, {Query: mustQuery("limit=50")}, {Query: mustQuery("limit=50&before=120")}},
	},
	key("GET", "/activity/live"): {
		build: func(a Action) *cmd {
			c := newCmd("activity").bare("follow")
			if set := a.Query.Get("backup_set"); set != "" {
				// A named set wins over scope=deployment, the way the
				// route answers it (a named set is the narrower
				// question), and the CLI refuses the pair outright, so
				// the pair is never echoed.
				c.flag("backup-set", set)
			} else if a.Query.Get("scope") == "deployment" {
				c.flag("scope", "deployment")
			}
			if n := positiveQuery(a, "limit"); n > 0 {
				c.flag("limit", itoa(n))
			}
			// `since` is deliberately not echoed. It is the browser's
			// resume cursor, a sequence number from a feed it was already
			// reading, and a follow started at a terminal starts from
			// now; there is no flag for it and there should not be one.
			return c
		},
		examples: []Action{
			{},
			{Query: mustQuery("backup_set=api-server%2Fvar-backups&limit=25")},
			{Query: mustQuery("since=42")},
			{Query: mustQuery("scope=deployment")},
			{Query: mustQuery("scope=deployment&backup_set=api-server%2Fvar-backups")},
		},
	},

	// --------------------------------------------------------- storage ---
	//
	// The whole storage surface, and every route on it is a real command
	// rather than a gap. `medium preflight` was the only verb until G2.2
	// (#594) added list, show, add, edit, remove and import-credentials,
	// which is what made a destination something an operator can configure
	// from a terminal instead of only by hand-editing config.yaml, and it
	// is why nothing down here has to say "there is no verb that".
	//
	// # What these lines can and cannot carry
	//
	// `medium` declares no flag that takes credential MATERIAL: the
	// material reaches the binary only on standard input, and the four
	// credential flags name an id this deployment minted, a path, a
	// variable NAME, and a command. Three of those four are references and
	// go on the line exactly as the request sent them.
	//
	// The fourth is not a reference, and neither is the endpoint. A
	// credentials command is a program and its arguments, written by
	// whoever made the request, and an endpoint URL takes userinfo in a
	// spelling rclone accepts and this contract does not validate. Both
	// were printed in full until #599's review, on a panel that is
	// copy-to-clipboard and exportable. So a command is named with a
	// placeholder and never printed, an endpoint has its userinfo removed,
	// and mediumSpecFlags says which of its flags are which. Everything
	// else down here is the command that actually works, byte for byte.
	key("GET", "/storage-mediums"): {
		build:    func(Action) *cmd { return newCmd("medium", "list") },
		examples: []Action{{}},
	},
	key("GET", "/storage-mediums/{id}"): {
		build:    func(a Action) *cmd { return newCmd("medium", "show", a.Params["id"]) },
		examples: []Action{{Params: map[string]string{"id": "offsite_s3"}}},
	},
	key("GET", "/storage-mediums/{id}/usage"): {
		// `show` again, and not a usage verb, because there is no usage
		// verb and that is deliberate rather than missing: `medium show`
		// prints the destination and then FR-30's report of what is on
		// it, on the reasoning its own doc gives, that whether anything
		// is there and whether it is the only copy is the fact an
		// operator is actually asking about when they look one up. Two
		// routes answering to one command is the honest line here; a gap
		// would be inventing a missing verb for a question the CLI does
		// answer.
		build:    func(a Action) *cmd { return newCmd("medium", "show", a.Params["id"]) },
		examples: []Action{{Params: map[string]string{"id": "offsite_s3"}}},
	},
	key("POST", "/storage-mediums"): {
		// `medium add` proves the destination before it writes it, and so
		// does this route since #636: the check is the engine's now,
		// whoever the caller is. The wizard still preflights first, on the
		// route below, and then saves here, so the two lines an operator
		// reads back are a candidate preflight followed by an add and the
		// add re-proves what the preflight just proved. That is a repeated
		// check rather than a wrong command.
		//
		// --no-verify is echoed when, and only when, the request asked to
		// skip. The browser never does: the wizard cannot save until its
		// own check has come back green, so there is nothing there to
		// skip. A request that DID skip and a line that did not would be a
		// printed command that behaves differently from the action it
		// claims to be the equivalent of, which is the one thing this
		// whole surface exists to avoid.
		build: func(a Action) *cmd {
			var req apicontract.StorageMediumRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return mediumSkipFlag(mediumSpecFlags(newCmd("medium", "add", req.ID), req), req)
		},
		why: "there is no verb that declares a storage destination from a request body",
		examples: []Action{
			{Body: []byte(`{"id":"offsite_s3","type":"s3","region":"eu-central-1","bucket":"acme-backups","prefix":"prod","storage_class":"STANDARD_IA","upload_verification":"readback","credentials":{"credentials_id":"cred_01HX"}}`)},
			{Body: []byte(`{"id":"offsite_s3","type":"s3","region":"eu-central-1","bucket":"acme-backups","credentials":{"command":["aws-vault","exec","backups"]}}`)},
		},
	},
	key("PUT", "/storage-mediums/{id}"): {
		build: func(a Action) *cmd {
			var req apicontract.StorageMediumRequest
			if !decode(a.Body, &req) {
				return nil
			}
			// The path id, never the body's. That is this route's own
			// rule rather than a choice made here: a body naming a
			// different destination is refused outright, so by the time
			// anything is written the two agree, and echoing the path id
			// is echoing the one that was edited.
			return mediumSkipFlag(mediumSpecFlags(newCmd("medium", "edit", a.Params["id"]), req), req)
		},
		why: "there is no verb that edits a storage destination from a request body",
		examples: []Action{
			{Params: map[string]string{"id": "offsite_s3"},
				Body: []byte(`{"region":"eu-west-1","storage_class":"GLACIER_IR"}`)},
			{Params: map[string]string{"id": "offsite_s3"},
				Body: []byte(`{"type":"s3","region":"eu-west-1","endpoint":"https://s3.eu-west-1.example.net","bucket":"acme-backups","prefix":"prod","storage_class":"STANDARD","upload_verification":"attested","credentials":{"file":"/etc/retnd/aws-credentials"}}`)},
		},
	},
	key("DELETE", "/storage-mediums/{id}"): {
		build:    func(a Action) *cmd { return newCmd("medium", "remove", a.Params["id"]) },
		examples: []Action{{Params: map[string]string{"id": "offsite_s3"}}},
	},
	key("POST", "/storage-mediums/preflight"): {
		// The candidate form of the same verb, which is the whole reason
		// `preflight` takes --candidate: this route proves a destination
		// that has not been declared, and the flag is what says the id on
		// the line names a description rather than something already in
		// the configuration.
		build: func(a Action) *cmd {
			var req apicontract.StorageMediumRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return mediumSpecFlags(newCmd("medium", "preflight", req.ID).bare("candidate"), req)
		},
		why: "there is no verb that proves an undeclared storage destination from a request body",
		examples: []Action{
			{Body: []byte(`{"id":"offsite_s3","type":"s3","region":"eu-central-1","endpoint":"https://s3.eu-central-1.example.net","bucket":"acme-backups","prefix":"prod","storage_class":"STANDARD","upload_verification":"readback","credentials":{"env":"RETND_S3_CREDENTIALS"}}`)},
		},
	},
	key("POST", "/storage-mediums/{id}/preflight"): {
		// `test-connection`, not `preflight`, since H2.2 (#622). The
		// check is the same one it always was and `preflight` still runs
		// it, kept as an alias so anything scripted against it goes on
		// working. What changed is the NAME an operator reads: the same
		// idea was called "Verify" in the browser, `preflight` on the
		// command line and "Test connection" on the source side of the
		// same product, and three names for one thing is three things to
		// an operator. This line echoes the name the button now carries,
		// because a command line printed under a button that says
		// something else teaches the wrong word.
		build:    func(a Action) *cmd { return newCmd("medium", "test-connection", a.Params["id"]) },
		examples: []Action{{Params: map[string]string{"id": "offsite_s3"}}, {Params: map[string]string{"id": "local"}}},
	},
	key("GET", "/storage-mediums/{id}/configuration"): {
		// `medium show` again, for /usage's reason: there is no
		// configuration-read verb and there does not need to be, because
		// what `medium show` prints IS the destination's fields. Two
		// routes answering to one command is the honest line.
		build:    func(a Action) *cmd { return newCmd("medium", "show", a.Params["id"]) },
		examples: []Action{{Params: map[string]string{"id": "offsite_s3"}}},
	},
	key("POST", "/storage-mediums/{id}/configuration/preflight"): {
		// The write-nothing form of the next route's verb: the same line
		// with --preflight on the end, which is what makes the two
		// readable as one command asked two ways.
		build: func(a Action) *cmd {
			var req apicontract.MediumConfigurationRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return mediumConfigurationFlags(newCmd("medium", "configure", a.Params["id"]), req).bare("preflight")
		},
		why: "there is no verb that checks a destination's configuration from a request body",
		examples: []Action{
			{Params: map[string]string{"id": "usb_disk"},
				Body: []byte(`{"backend":"local_volume","fields":[{"field":"path","value":"/mnt/usb"}],"credentials":{}}`)},
		},
	},
	key("PUT", "/storage-mediums/{id}/configuration"): {
		// `medium configure`, since #1154: a destination in its backend's
		// own field ids, so a local_volume's `path` is a thing a line can
		// say. The path id, for the reason PUT /storage-mediums/{id}
		// gives: the body names a backend and fields, never which
		// destination.
		build: func(a Action) *cmd {
			var req apicontract.MediumConfigurationRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return mediumConfigurationFlags(newCmd("medium", "configure", a.Params["id"]), req)
		},
		why: "there is no verb that configures a storage destination from a request body",
		examples: []Action{
			{Params: map[string]string{"id": "usb_disk"},
				Body: []byte(`{"backend":"local_volume","fields":[{"field":"path","value":"/mnt/usb"},{"field":"prefix","value":"retnd"}],"credentials":{}}`)},
			{Params: map[string]string{"id": "cold"},
				Body: []byte(`{"backend":"s3","fields":[{"field":"bucket","value":"acme-backups"},{"field":"region","value":"eu-central-1"}],"credentials":{"credentials_id":"cred_01HX"}}`)},
		},
	},
	key("PUT", "/storage-mediums/{id}/default"): {
		// Moving the destination a newly created retention tier starts on
		// (#622). One verb, one operand, and no flags: the whole content
		// of the request is which destination, and the id in the path is
		// the only place it appears.
		build:    func(a Action) *cmd { return newCmd("medium", "default", a.Params["id"]) },
		examples: []Action{{Params: map[string]string{"id": "offsite_s3"}}, {Params: map[string]string{"id": "local"}}},
	},
	key("POST", "/storage-credentials"): {
		// The one request in this whole contract that carries S3
		// credential MATERIAL, and so the one line in this file that
		// could have leaked one.
		//
		// It cannot, and not because this builder is careful with the
		// body: it never reads the body at all, because the command it
		// names takes nothing from it. `medium import-credentials
		// --stdin` reads the shared-credentials text from standard
		// input, which is not in the process table and not in shell
		// history, and --stdin is required rather than assumed precisely
		// so that this is the only shape the command has.
		//
		// So this is a working invocation printed in full: no
		// placeholder, nothing starred out, and no "not runnable as
		// printed" note, because it IS runnable as printed. An operator
		// pastes it, pipes their credentials in, and gets back the same
		// id the wizard got.
		build:    func(Action) *cmd { return newCmd("medium", "import-credentials").bare("stdin") },
		examples: []Action{{}},
	},

	// --------------------------------------------------------- catalog ---
	key("POST", "/catalog/scan"): {
		build:    func(Action) *cmd { return newCmd("catalog", "rebuild").bare("dry-run") },
		examples: []Action{{}},
	},
	key("POST", "/catalog/rebuild"): {
		build:    func(Action) *cmd { return newCmd("catalog", "rebuild") },
		examples: []Action{{}},
	},

	// ------------------------------------------------------ validators ---
	key("GET", "/validators"): {
		build:    func(Action) *cmd { return newCmd("validators") },
		examples: []Action{{}},
	},

	// ------------------------------------------------------- backends ---
	// EPIC I (#664). A gap entry rather than a command, and the gap is
	// real: there is no verb that lists the registered backends at all.
	// `medium add --backend <id>` takes one and refuses an id no manifest
	// declares, so a terminal operator learns the set from a refusal
	// rather than from a listing. Naming the missing verb here is the
	// point of this table: a parity gap that is written down is a
	// decision, and inventing a `medium backends` verb to satisfy this
	// test would invert the rule, which exists so a missing verb is
	// VISIBLE rather than absent.
	key("GET", "/backends"): {
		build:    func(Action) *cmd { return newCmd("medium", "backends") },
		examples: []Action{{}},
	},

	// ------------------------------------------------------------- ssh ---
	//
	// The key store and the host-key probe had no verb until #1153, and
	// each of these five entries was a written gap saying so. They name
	// their verbs now. Key material and the passphrase are in the body
	// and stay there: the key is a FILE on the command line and the
	// passphrase is the first line of stdin, so the line is exportable.
	key("POST", "/ssh-keys"): {
		build: func(a Action) *cmd {
			var req struct {
				Passphrase string `json:"passphrase"`
			}
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("ssh-key", "import").placeholderFlag("file", "the private key file you chose")
			if req.Passphrase != "" {
				c.bare("passphrase-stdin")
			}
			return c
		},
		why: "the request body would not decode, so there is no command that says what it imported",
		examples: []Action{
			{Body: []byte(`{"private_key_pem":"x"}`)},
			{Body: []byte(`{"private_key_pem":"x","passphrase":"y"}`)},
		},
	},
	// Selecting a candidate (#592). An opaque handle that sits on a command
	// line perfectly well, and `ssh-key import --candidate` is the verb
	// that takes it.
	key("POST", "/ssh-keys/from-candidate"): {
		build: func(a Action) *cmd {
			var req struct {
				CandidateID string `json:"candidate_id"`
			}
			if !decode(a.Body, &req) {
				return nil
			}
			return newCmd("ssh-key", "import").flagIfSet("candidate", req.CandidateID)
		},
		why: "the request body would not decode, so there is no command that says which candidate it selected",
		examples: []Action{
			{Body: []byte(`{"candidate_id":"cand_1"}`)},
		},
	},
	key("POST", "/ssh/host-key-probe"): {
		build: func(a Action) *cmd {
			var req struct {
				Host string `json:"host"`
				Port int    `json:"port"`
			}
			if !decode(a.Body, &req) || req.Host == "" {
				return nil
			}
			c := newCmd("ssh", "host-key-probe", req.Host)
			if req.Port > 0 {
				c.flag("port", itoa(req.Port))
			}
			return c
		},
		why: "the request names no host, so there is nothing to probe and no command that says what was probed",
		examples: []Action{
			{Body: []byte(`{"host":"source.example.internal","port":2222}`)},
			{Body: []byte(`{"host":"source.example.internal","port":22}`)},
		},
	},
	key("GET", "/ssh-keys"): {
		build:    func(Action) *cmd { return newCmd("ssh-key", "list") },
		examples: []Action{{}},
	},
	key("GET", "/ssh/key-candidates"): {
		build:    func(Action) *cmd { return newCmd("ssh-key", "discover") },
		examples: []Action{{}},
	},

	// -------------------------------------------------------- settings ---
	key("GET", "/settings"): {
		build:    func(Action) *cmd { return newCmd("settings") },
		examples: []Action{{}},
	},
	key("PATCH", "/settings"): {
		// This refused a tier-chain replacement outright, and quoted
		// usage() as its authority: "a full retention tier-chain
		// replacement is still a config-file edit". usage() now says the
		// opposite, because #595 put --policy-file on `settings patch`,
		// so the sentence was quoting a line that no longer existed while
		// the flag it denied was three files away.
		build: func(a Action) *cmd {
			var req apicontract.UpdateSettingsRequest
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("settings", "patch")
			policy := false
			if r := req.Retention; r != nil {
				if len(r.Tiers) > 0 {
					// The chain, as a file. --policy-file carries the
					// whole retention section and `settings patch`
					// refuses it beside --timezone and the rest, so a
					// request that also carried those scalars has them
					// inside the block rather than beside it.
					c.placeholderFlag("policy-file", "a file holding this retention: block")
					policy = true
				} else {
					if r.Timezone != nil {
						c.flag("timezone", *r.Timezone)
						policy = true
					}
					if r.WeekStartsOn != nil {
						c.flag("week-starts-on", *r.WeekStartsOn)
						policy = true
					}
					if r.ProtectLastKnownGood != nil {
						c.assigned("protect-last-known-good", *r.ProtectLastKnownGood)
						policy = true
					}
				}
			}
			if cap := req.Capacity; cap != nil {
				if cap.CapBytes != nil {
					c.flag("cap-bytes", itoa64(*cap.CapBytes))
				}
				if cap.WarningFreeBytes != nil {
					c.flag("warning-free-bytes", itoa64(*cap.WarningFreeBytes))
				}
				if cap.CriticalFreeBytes != nil {
					c.flag("critical-free-bytes", itoa64(*cap.CriticalFreeBytes))
				}
				if cap.SafetyMarginBytes != nil {
					c.flag("safety-margin-bytes", itoa64(*cap.SafetyMarginBytes))
				}
			}
			// Issue #1151: the deployment-wide poll interval, the one
			// field of the service section the CLI can set.
			if svc := req.Service; svc != nil && svc.PollIntervalSeconds != nil {
				c.flag("poll-interval", seconds(int64(*svc.PollIntervalSeconds)))
			}
			// The acknowledgement rides beside a policy write and the CLI
			// refuses it on a line that writes none.
			if req.AcknowledgeMediumDisclosure && policy {
				c.bare("acknowledge-medium-disclosure")
			}
			return c
		},
		why: "there is no verb that patches the deployment's settings from a request body",
		examples: []Action{
			{Body: []byte(`{"retention":{"timezone":"Europe/Berlin","week_starts_on":"monday","protect_last_known_good":false}}`)},
			{Body: []byte(`{"capacity":{"cap_bytes":1099511627776,"warning_free_bytes":107374182400,"critical_free_bytes":53687091200,"safety_margin_bytes":1073741824}}`)},
			{Body: []byte(`{"retention":{"tiers":[{"name":"daily","granularity":"day","keep":7}],"timezone":"Europe/Berlin"},"acknowledge_medium_disclosure":true}`)},
			{Body: []byte(`{"retention":{"tiers":[{"name":"daily","granularity":"day","keep":7}]},"capacity":{"cap_bytes":1099511627776}}`)},
			// The deployment's poll interval (#1151), in the corpus so the
			// dispatcher parses the flag back end to end.
			{Body: []byte(`{"service":{"poll_interval_seconds":3600}}`)},
		},
	},

	// ------------------------------------------------------- workflows ---
	//
	// EPIC L (#813). Every one of these is a real command rather than a
	// gap, which is what shipping the two surfaces together is for: the
	// browser screen an operator is reading can print the line that
	// would have done the same thing from a terminal.
	//
	// # What these lines can and cannot carry
	//
	// The environment write is the one that needed a decision, and it is
	// the same decision `medium add --credentials-command` and
	// `repository create --passphrase-command` already made, applied one
	// field wider. Three of the four value spellings name a LOCATION --
	// a path on the operator's own host, the NAME of an environment
	// variable, an argv -- and the fourth is the value itself.
	//
	// A location is printed. A VALUE never is, including the plain
	// `--value` literal, and that is deliberate rather than cautious
	// bookkeeping: this panel is copy-to-clipboard and exportable, the
	// field is a free string whose content the operator chose, and
	// nothing here can tell `--value production` from `--value hunter2`.
	// The whole reason a secret variable is configured as a REFERENCE is
	// that credentials do not belong in that field, and a line that
	// printed it anyway would be a surface that leaks one the first time
	// somebody ignores that advice. So both of the value-carrying flags
	// are named with a placeholder and the line says it is not runnable
	// as printed, exactly as a passphrase command's is.
	key("GET", "/settings/workflow"): {
		build:    func(Action) *cmd { return newCmd("settings", "workflow") },
		examples: []Action{{}},
	},
	key("PATCH", "/settings/workflow"): {
		build: func(a Action) *cmd {
			var req apicontract.UpdateWorkflowSettingsRequest
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("settings", "workflow", "patch")
			if req.Root != nil {
				c.flag("root", *req.Root)
			}
			// An empty string is a REQUEST here and not an absence: it
			// clears the stage directory, which disables that stage. So
			// these are printed on nil-ness rather than through
			// flagIfSet, which would silently drop the one edit an
			// operator most needs to see echoed back.
			if req.BeforeDir != nil {
				c.flag("before-dir", *req.BeforeDir)
			}
			if req.AfterDir != nil {
				c.flag("after-dir", *req.AfterDir)
			}
			if req.ScriptTimeoutSeconds != nil {
				c.flag("script-timeout", seconds(*req.ScriptTimeoutSeconds))
			}
			if req.MaxScriptSizeBytes != nil {
				c.flag("max-script-size-bytes", itoa64(*req.MaxScriptSizeBytes))
			}
			return c
		},
		why: "there is no verb that patches the deployment's workflow configuration from a request body",
		examples: []Action{
			{Body: []byte(`{"root":"/workflows"}`)},
			{Body: []byte(`{"before_dir":"before.d","after_dir":"after.d","script_timeout_seconds":300,"max_script_size_bytes":65536}`)},
			// The clearing shapes, which are requests rather than
			// omissions: an empty directory disables that stage and a
			// zero timeout gives the bound back to the deployment
			// default. They are examples rather than a unit test's table
			// because this corpus is what core/cmd/retnd parses end to
			// end, and a shape no example carries is a shape nothing
			// proves the binary takes.
			{Body: []byte(`{"before_dir":"","after_dir":"","script_timeout_seconds":0}`)},
		},
	},
	key("GET", "/settings/workflow/environment"): {
		build:    func(Action) *cmd { return newCmd("settings", "workflow", "env", "list") },
		examples: []Action{{}},
	},
	key("PUT", "/settings/workflow/environment/{name}"): {
		build: func(a Action) *cmd {
			var req apicontract.WorkflowEnvironmentVariableRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return workflowEnvSetFlags(newCmd("settings", "workflow", "env", "set", a.Params["name"]), req)
		},
		why:      "there is no verb that configures a workflow environment variable from a request body",
		examples: workflowEnvSetExamples(map[string]string{"name": "PGPASSWORD"}),
	},
	key("DELETE", "/settings/workflow/environment/{name}"): {
		build: func(a Action) *cmd {
			return newCmd("settings", "workflow", "env", "unset", a.Params["name"])
		},
		examples: []Action{{Params: map[string]string{"name": "PGPASSWORD"}}},
	},
	key("GET", "/backup-sets/{source}/{set}/workflow"): {
		build:    func(a Action) *cmd { return newCmd("backup-set", "workflow", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("PATCH", "/backup-sets/{source}/{set}/workflow"): {
		build: func(a Action) *cmd {
			var req apicontract.UpdateBackupSetWorkflowRequest
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("backup-set", "workflow", "patch", setID(a))
			if req.BeforeDir != nil {
				c.flag("before-dir", *req.BeforeDir)
			}
			if req.AfterDir != nil {
				c.flag("after-dir", *req.AfterDir)
			}
			if req.ScriptTimeoutSeconds != nil {
				c.flag("script-timeout", seconds(*req.ScriptTimeoutSeconds))
			}
			// The wire spells this remote_exec_connection_ref, which is
			// config.yaml's own key; the verb spells it
			// --exec-connection. Two names for one thing is two things to
			// an operator, and the one that goes on a command line has to
			// be the one the binary declares.
			if req.RemoteExecConnectionRef != nil {
				c.flag("exec-connection", *req.RemoteExecConnectionRef)
			}
			return c
		},
		why: "there is no verb that patches a backup set's workflow configuration from a request body",
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"before_dir":"pg.before.d","script_timeout_seconds":90}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"before_dir":"","after_dir":"","script_timeout_seconds":0,"remote_exec_connection_ref":"pg-primary"}`)},
		},
	},
	key("GET", "/backup-sets/{source}/{set}/workflow/environment"): {
		build: func(a Action) *cmd {
			return newCmd("backup-set", "workflow", "env", setID(a), "list")
		},
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("PUT", "/backup-sets/{source}/{set}/workflow/environment/{name}"): {
		build: func(a Action) *cmd {
			var req apicontract.WorkflowEnvironmentVariableRequest
			if !decode(a.Body, &req) {
				return nil
			}
			return workflowEnvSetFlags(
				newCmd("backup-set", "workflow", "env", setID(a), "set", a.Params["name"]), req)
		},
		why: "there is no verb that configures a backup set's workflow environment variable from a request body",
		examples: workflowEnvSetExamples(map[string]string{
			"source": "api-server", "set": "var-backups", "name": "PGPASSWORD",
		}),
	},
	key("DELETE", "/backup-sets/{source}/{set}/workflow/environment/{name}"): {
		build: func(a Action) *cmd {
			return newCmd("backup-set", "workflow", "env", setID(a), "unset", a.Params["name"])
		},
		examples: []Action{{Params: map[string]string{
			"source": "api-server", "set": "var-backups", "name": "PGPASSWORD",
		}}},
	},
	key("GET", "/backup-sets/{source}/{set}/workflow/validation"): {
		build:    func(a Action) *cmd { return newCmd("validate", "workflow", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	// ------------------------------------------------------- assurance ---
	//
	// EPIC Q, Q1.4 (#982). Every route is a real command. The patch
	// builders print on nil-ness rather than non-zero for the reason the
	// workflow ones do: an explicit zero or empty value CLEARS a setting
	// back to its default, and a builder that dropped it would echo a
	// command that does not make the edit the operator asked for.
	key("GET", "/settings/assurance"): {
		build:    func(Action) *cmd { return newCmd("settings", "assurance") },
		examples: []Action{{}},
	},
	key("PATCH", "/settings/assurance"): {
		build: func(a Action) *cmd {
			var req apicontract.UpdateAssuranceSettingsRequest
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("settings", "assurance", "patch")
			if req.ScrubCadenceSeconds != nil {
				c.flag("scrub-cadence", seconds(*req.ScrubCadenceSeconds))
			}
			if req.ScrubStrategy != nil {
				c.flag("scrub-strategy", *req.ScrubStrategy)
			}
			if req.RecoveryTestMode != nil {
				c.flag("recovery-test-mode", *req.RecoveryTestMode)
			}
			if req.RecoveryTestCadenceSeconds != nil {
				c.flag("recovery-test-cadence", seconds(*req.RecoveryTestCadenceSeconds))
			}
			if req.RecoveryTestValidatorID != nil {
				c.flag("recovery-test-validator", *req.RecoveryTestValidatorID)
			}
			if req.RecoveryTestBuiltinCheck != nil {
				c.flag("recovery-test-builtin-check", *req.RecoveryTestBuiltinCheck)
			}
			if req.StagingRoot != nil {
				c.flag("staging-root", *req.StagingRoot)
			}
			if req.StagingTtlSeconds != nil {
				c.flag("staging-ttl", seconds(*req.StagingTtlSeconds))
			}
			if req.MaxConcurrentFullReads != nil {
				c.flag("max-concurrent-full-reads", itoa64(*req.MaxConcurrentFullReads))
			}
			if req.MaxPerDestination != nil {
				c.flag("max-per-destination", itoa64(*req.MaxPerDestination))
			}
			return c
		},
		why: "there is no verb that patches the deployment's assurance policy from a request body",
		examples: []Action{
			{Body: []byte(`{"scrub_cadence_seconds":43200}`)},
			{Body: []byte(`{"recovery_test_mode":"required","recovery_test_validator_id":"pg-restore-check","recovery_test_cadence_seconds":2592000}`)},
			{Body: []byte(`{"staging_ttl_seconds":3600,"max_concurrent_full_reads":4,"max_per_destination":2}`)},
			{Body: []byte(`{"scrub_cadence_seconds":0,"scrub_strategy":"","staging_root":"/var/lib/retnd/staging"}`)},
			{Body: []byte(`{"recovery_test_mode":"builtin","recovery_test_cadence_seconds":2592000}`)},
			{Body: []byte(`{"recovery_test_builtin_check":"off"}`)},
		},
	},
	key("GET", "/backup-sets/{source}/{set}/assurance"): {
		build:    func(a Action) *cmd { return newCmd("backup-set", "assurance", setID(a)) },
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("PATCH", "/backup-sets/{source}/{set}/assurance"): {
		build: func(a Action) *cmd {
			var req apicontract.UpdateBackupSetAssuranceRequest
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("backup-set", "assurance", setID(a))
			if req.ScrubCadenceSeconds != nil {
				c.flag("scrub-cadence", seconds(*req.ScrubCadenceSeconds))
			}
			if req.ScrubStrategy != nil {
				c.flag("scrub-strategy", *req.ScrubStrategy)
			}
			if req.RecoveryTestMode != nil {
				c.flag("recovery-test-mode", *req.RecoveryTestMode)
			}
			if req.RecoveryTestCadenceSeconds != nil {
				c.flag("recovery-test-cadence", seconds(*req.RecoveryTestCadenceSeconds))
			}
			if req.RecoveryTestValidatorID != nil {
				c.flag("recovery-test-validator", *req.RecoveryTestValidatorID)
			}
			if req.RecoveryTestBuiltinCheck != nil {
				c.flag("recovery-test-builtin-check", *req.RecoveryTestBuiltinCheck)
			}
			return c
		},
		why: "there is no verb that patches a backup set's assurance policy from a request body",
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}, Body: []byte(`{"scrub_cadence_seconds":7200}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"recovery_test_mode":"custom","recovery_test_cadence_seconds":172800,"recovery_test_validator_id":"pg-restore-check"}`)},
		},
	},
	key("DELETE", "/backup-sets/{source}/{set}/assurance"): {
		build: func(a Action) *cmd {
			return newCmd("backup-set", "assurance", setID(a)).bare("reset")
		},
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("POST", "/backup-sets/{source}/{set}/recovery-test"): {
		build: func(a Action) *cmd {
			c := newCmd("recovery-test", setID(a))
			var req apicontract.StartRecoveryTestRequest
			if !decode(a.Body, &req) {
				return c
			}
			if req.ArtifactID != "" {
				c.flag("artifact", req.ArtifactID)
			}
			if req.Acknowledged {
				c.bare("acknowledge-cost")
			}
			return c
		},
		examples: []Action{
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"}, Body: []byte(`{}`)},
			{Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body: []byte(`{"artifact_id":"api-server/var-backups/dump.tar","acknowledged":true}`)},
		},
	},
	key("GET", "/backup-sets/{source}/{set}/recovery-test/output"): {
		build: func(a Action) *cmd {
			return newCmd("recovery-test", setID(a)).bare("show-output")
		},
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("POST", "/backup-sets/{source}/{set}/scrub"): {
		build: func(a Action) *cmd {
			return newCmd("scrub", setID(a))
		},
		examples: []Action{{Params: map[string]string{"source": "api-server", "set": "var-backups"}}},
	},
	key("POST", "/assurance/operations/{op}/cancel"): {
		build: func(a Action) *cmd {
			return newCmd("assurance", "cancel", a.Params["op"])
		},
		examples: []Action{{Params: map[string]string{"op": "7b0c9a1e-0000-4000-8000-000000000001"}}},
	},
	key("GET", "/assurance/operations/{op}"): {
		// #1200. The Operation page's read: one scrub or recovery test's
		// lines and how it ended, from the durable activity record.
		build: func(a Action) *cmd {
			return newCmd("assurance", "show", a.Params["op"])
		},
		examples: []Action{{Params: map[string]string{"op": "7b0c9a1e-0000-4000-8000-000000000001"}}},
	},
	key("GET", "/assurance/due"): {
		build: func(a Action) *cmd {
			c := newCmd("settings", "assurance", "due")
			if set := a.Query.Get("backup_set"); set != "" {
				c.flag("backup-set", set)
			}
			switch a.Query.Get("status") {
			case "due":
				c.bare("due")
			case "overdue":
				c.bare("overdue")
			}
			return c
		},
		examples: []Action{
			{},
			{Query: mustQuery("backup_set=api-server%2Fvar-backups&status=overdue")},
			{Query: mustQuery("status=due")},
		},
	},
	key("GET", "/workflow-runs"): {
		build: func(a Action) *cmd {
			c := newCmd("workflow", "run", "list")
			if set := a.Query.Get("backup_set"); set != "" {
				c.flag("backup-set", set)
			}
			if n := positiveQuery(a, "limit"); n > 0 {
				c.flag("limit", itoa(n))
			}
			return c
		},
		examples: []Action{
			{},
			{Query: mustQuery("backup_set=api-server%2Fvar-backups&limit=25")},
		},
	},
	key("GET", "/workflow-runs/{run}"): {
		build:    func(a Action) *cmd { return newCmd("workflow", "run", "show", a.Params["run"]) },
		examples: []Action{{Params: map[string]string{"run": "wfr_01HX"}}},
	},
	key("GET", "/workflow-runs/{run}/steps"): {
		build:    func(a Action) *cmd { return newCmd("workflow", "run", "steps", a.Params["run"]) },
		examples: []Action{{Params: map[string]string{"run": "wfr_01HX"}}},
	},
	key("GET", "/workflow-runs/{run}/steps/{step}/logs"): {
		build: func(a Action) *cmd {
			c := newCmd("workflow", "run", "log", a.Params["run"]).flag("step", a.Params["step"])
			if n := positiveQuery(a, "limit"); n > 0 {
				c.flag("limit", itoa(n))
			}
			// A request that asked to WAIT is a follower, and --follow is
			// what a terminal does instead. The number of seconds is not
			// echoed: the route's wait is one page's bound and the verb's
			// follow is a loop, so printing a value would name a
			// parameter the command does not have.
			if positiveQuery(a, "wait") > 0 {
				c.bare("follow")
			}
			// `after` is deliberately not echoed, for the reason
			// `activity --follow` does not echo `since`: it is a
			// browser's resume cursor into output it is already partway
			// through, and a log read started at a terminal starts at the
			// beginning or follows from now. The verb does take a
			// --cursor, and printing the browser's would hand an operator
			// a line that silently skips everything before it.
			return c
		},
		examples: []Action{
			{Params: map[string]string{"run": "wfr_01HX", "step": "s3"}},
			{Params: map[string]string{"run": "wfr_01HX", "step": "s3"},
				Query: mustQuery("after=42&limit=500&wait=5")},
		},
	},
	key("GET", "/workflow-recovery"): {
		build:    func(Action) *cmd { return newCmd("workflow", "recovery", "show") },
		examples: []Action{{}},
	},
	key("POST", "/workflow-recovery/{run}/resume-cleanup"): {
		build: func(a Action) *cmd {
			return newCmd("workflow", "recovery", "resume-cleanup", a.Params["run"])
		},
		examples: []Action{{Params: map[string]string{"run": "wfr_01HX"}}},
	},
	key("POST", "/workflow-recovery/{run}/acknowledge"): {
		build: func(a Action) *cmd {
			var req apicontract.WorkflowAcknowledgementRequest
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("workflow", "recovery", "acknowledge", a.Params["run"])
			// flagIfSet, for backupSetCreateCommand's reason: an
			// acknowledgement with no reason is REFUSED by the route, and
			// the line has to be the command that was asked for rather
			// than --reason followed by an empty string, which is a
			// different request the verb refuses for a different reason.
			// The actor is not on the line at all: the route takes it
			// from the session and the verb takes it from the account
			// running the binary, so there is nothing here to pass.
			c.flagIfSet("reason", req.Reason)
			return c
		},
		why: "there is no verb that acknowledges an interrupted workflow run from a request body",
		examples: []Action{
			{Params: map[string]string{"run": "wfr_01HX"},
				Body: []byte(`{"reason":"unmounted the snapshot by hand and checked the database is writable"}`)},
		},
	},
	// ------------------------------------------------------------ auth ---
	//
	// The twelve /auth routes are served by a separate router
	// (apps/common/auth/local, mounted at /api/v1/auth/), so a walk of the
	// engine router never sees them and this table had no entry for any.
	// Each now carries one. They are browser-session plumbing or an
	// account act with no terminal verb, and the reason says which.
	key("POST", "/auth/login"): {
		why: "a login opens a browser session, and a terminal holds no session: a routed command signs in with $RETND_API_USERNAME and $RETND_API_PASSWORD for the one invocation, so there is nothing to type",
	},
	key("POST", "/auth/enroll"): {
		why: "the first administrator is made by `" + WebBinary + " auth create-admin --username U --password-stdin`, offline, against the store, and it refuses while a server holds the store or once an administrator exists; there is no routed verb for it, because there is no engine account to sign in as yet",
	},
	key("POST", "/auth/password"): {
		build: func(Action) *cmd {
			// Both passwords are in the body and neither goes on the line:
			// the verb reads the current one and then the new one from stdin.
			return newCmd("auth", "password").bare("password-stdin")
		},
		examples: []Action{{}},
	},
	key("POST", "/auth/logout"): {
		why: "a logout ends a browser session, and a terminal holds none",
	},
	key("GET", "/auth/session"): {
		why: "a session read answers a browser's question about its own cookie, and a terminal holds no session to ask about",
	},
	key("POST", "/auth/forgot-password"): {
		why:               "there is no verb that asks for a password-reset email from a terminal; it is the browser's path for an account with a working mail server, and a terminal that can reach the engine at all can change the password with `" + Binary + " auth password`",
		namesShippedVerbs: []string{"auth"},
	},
	key("POST", "/auth/reset-password"): {
		why: "there is no verb that redeems a password-reset token from a terminal. An administrator locked out with no mail configured does have a way back in: `" + WebBinary + " auth set-password --username U --password-stdin`, offline, with the server stopped",
	},
	key("POST", "/auth/verify-email"): {
		build: func(Action) *cmd {
			return newCmd("auth", "verify-email").bare("token-stdin")
		},
		examples: []Action{{}},
	},
	key("POST", "/auth/verify-email/resend"): {
		build: func(Action) *cmd {
			return newCmd("auth", "verify-email").bare("resend")
		},
		examples: []Action{{}},
	},
	key("GET", "/auth/recovery"): {
		build:    func(Action) *cmd { return newCmd("auth", "recovery", "show") },
		examples: []Action{{}},
	},
	key("PATCH", "/auth/recovery"): {
		why: "the request body would not decode, so there is no command that says what it changed",
		build: func(a Action) *cmd {
			// The current password and the SMTP password are in the body
			// and stay there: the verb takes them from stdin. The values
			// that remain are an address and an endpoint an operator
			// typed, which are shown as stand-ins all the same, because
			// the line is exportable and a recovery address is the one
			// thing an account's owner may not want in a pasted log.
			var req struct {
				RecoveryEmail *string `json:"recoveryEmail"`
				Smtp          *struct {
					Host     string `json:"host"`
					Port     int    `json:"port"`
					Security string `json:"security"`
					Username string `json:"username"`
					From     string `json:"from"`
					Password string `json:"password"`
				} `json:"smtp"`
			}
			if !decode(a.Body, &req) {
				return nil
			}
			c := newCmd("auth", "recovery", "set").bare("current-password-stdin")
			if req.RecoveryEmail != nil {
				c.placeholderFlag("recovery-email", "the recovery address")
			}
			if s := req.Smtp; s != nil {
				c.placeholderFlag("smtp-host", "the SMTP host")
				if s.Port > 0 {
					c.flag("smtp-port", itoa(s.Port))
				}
				if s.Security != "" {
					c.placeholderFlag("smtp-security", "starttls, tls or none")
				}
				if s.Username != "" {
					c.placeholderFlag("smtp-username", "the SMTP username")
				}
				if s.From != "" {
					c.placeholderFlag("smtp-from", "the sender address")
				}
				if s.Password != "" {
					c.bare("smtp-password-stdin")
				}
			}
			return c
		},
		examples: []Action{
			{Body: []byte(`{"currentPassword":"x","recoveryEmail":"ops@example.org"}`)},
			{Body: []byte(`{"currentPassword":"x","smtp":{"host":"smtp.example.org","port":2525,"security":"tls","username":"mailer","from":"retnd@example.org","password":"y"}}`)},
		},
	},
	key("POST", "/auth/recovery/test"): {
		build:    func(Action) *cmd { return newCmd("auth", "recovery", "test") },
		examples: []Action{{}},
	},
}

// workflowEnvSetFlags puts one environment entry's value onto an `env
// set` command line, shared by the deployment-wide route and the per-set
// one because the two are the same operation on two lists and the verb
// takes the same four flags for both.
//
// It prints every source the request NAMED rather than choosing one, and
// on a real request that is exactly one flag: a variable is a name and
// ONE source, and both the route and the verb refuse a body or a command
// line naming two. So there is no precedence to express here, and
// expressing one would be worse than useless -- a builder that picked a
// winner would print a runnable command for a request the route had just
// refused, which is the one thing an echoed line must never do. A body
// that named nothing prints no value flag at all, for
// backupSetCreateCommand's reason: the line has to be the command that
// was asked for, and the verb refuses it the same way the route did.
//
// Two of the four are printed in full and two are placeholders, and the
// line between them is whether the flag carries a LOCATION or a VALUE:
//
//   - --secret-file is a path on the operator's own host and
//     --secret-env is the NAME of a variable in the engine's process.
//     Both are references the operator wrote themselves and has to be
//     able to retype, exactly as --credentials-file and --credentials-env
//     are one noun over.
//   - --secret-command is a program and its arguments, both the caller's
//     words, and `printf %s hunter2` is a valid secret command. --value
//     is the literal itself. Neither can be told apart from a credential
//     by anything this package can inspect, so each is named and its
//     content is not printed, which is what marks the line as not
//     runnable as printed.
//
// The verb takes --secret-command once per argv word rather than as one
// quoted string, so that an argv assembled from a command line cannot be
// re-split by a quoting mistake on its way to exec. That is invisible
// here, because the words never go on the line; it matters to the
// operator who fills the placeholder in.
func workflowEnvSetFlags(c *cmd, req apicontract.WorkflowEnvironmentVariableRequest) *cmd {
	if req.Value != nil {
		c.placeholderFlag("value", "the value this variable is set to")
	}
	c.flagIfSet("secret-file", req.Secret.File)
	c.flagIfSet("secret-env", req.Secret.Env)
	if len(req.Secret.Command) > 0 {
		c.placeholderFlag("secret-command", "the command that prints this value, one --secret-command per word")
	}

	return c
}

// workflowEnvSetExamples is the corpus both `env set` routes are driven
// with, one Action per value spelling.
//
// One helper because the two routes take the identical body and the
// parse test is only ever as good as this list is wide: a spelling no
// example carries is a spelling nothing proves the binary takes, which is
// how a duration renderer that ate a digit from half its input space
// stayed green (see seconds). The params differ between the two routes,
// so they are handed in.
func workflowEnvSetExamples(params map[string]string) []Action {
	bodies := []string{
		`{"value":"UTC"}`,
		`{"value":""}`,
		`{"secret":{"file":"/etc/retnd/pg.passphrase"}}`,
		`{"secret":{"env":"RETND_PG_PASSWORD"}}`,
		`{"secret":{"command":["vault","read","-field=password","secret/pg"]}}`,
	}
	out := make([]Action, 0, len(bodies))
	for _, body := range bodies {
		out = append(out, Action{Params: params, Body: []byte(body)})
	}

	return out
}

// backupSetCreateCommand is `backup-set create`, shared by POST
// /backup-sets and POST /system/first-run because they write the same
// thing: usage() puts the first config.yaml on this same verb (#176).
//
// Every string field is echoed only when the request carried it. The case
// that matters is a create REFUSED for a missing field: the line beside
// this one says what was missing, and this one has to be the command that
// was asked for, not --user followed by an empty string, which is a
// different command (an empty user rather than no user) and not one
// anybody can fill in and run.
func backupSetCreateCommand(spec apicontract.BackupSetSpec, runNow, acknowledgeRepoint bool) *cmd {
	c := newCmd("backup-set", "create", spec.SourceName+"/"+spec.Name)
	c.flagIfSet("host", spec.Host)
	if spec.Port > 0 {
		c.flag("port", itoa(spec.Port))
	}
	c.flagIfSet("user", spec.User)
	c.flagIfSet("remote-path", spec.RemotePath)
	c.flagIfSet("local-path", spec.LocalPath)
	// An id, never key material: the key itself was imported by its own
	// request and this names the copy this deployment already holds.
	c.flagIfSet("ssh-key-id", spec.SSHKeyID)
	// A known_hosts line is a PUBLIC host key. It is the one long value
	// on this line and it is deliberately printed rather than placed
	// behind a placeholder: an operator pasting this command needs to
	// pin the same key, and hiding it would make the command unrunnable
	// for no gain.
	c.flagIfSet("known-hosts-line", spec.KnownHostsLine)
	c.flagIfSet("completion-strategy", spec.CompletionStrategy)
	if len(spec.Include) > 0 {
		c.flag("include", strings.Join(spec.Include, ","))
	}
	if spec.StableForSeconds > 0 {
		c.flag("stable-for", seconds(int64(spec.StableForSeconds)))
	}
	if spec.StaleAfterSeconds > 0 {
		c.flag("stale-after", seconds(int64(spec.StaleAfterSeconds)))
	}
	if spec.ValidatorID != "" {
		c.flag("validator-id", spec.ValidatorID)
	}
	// EPIC K (#788). What makes the set incremental, and then what it
	// costs to prove. These are echoed for the reason every flag here
	// is, with one extra edge: a line that dropped --engine would not be
	// an incomplete reproduction of this set, it would be a working
	// command that makes a DIFFERENT kind of set under the same id.
	//
	// The uuid is echoed when the request carried one (#1151). It used to
	// be left out because `backup-set create` had no flag for it, which
	// made a printed command mint a second lineage beside the one the
	// request named. A request that carried none still prints none: the
	// service mints it, and a placeholder here would be a command that
	// forks the history on purpose.
	c.flagIfSet("engine", spec.Engine)
	c.flagIfSet("uuid", spec.Uuid)
	c.flagIfSet("source-mount-prefix", spec.SourceMountPrefix)
	c.flagIfSet("repository-domain", spec.RepositoryDomain)
	c.flagIfSet("source-consistency", spec.SourceConsistency)
	c.flagIfSet("verification-level", spec.VerificationLevel)
	if spec.VerificationSamplePercent > 0 {
		c.flag("verification-sample-percent", itoa(spec.VerificationSamplePercent))
	}
	// Zero is this cadence's own default (never), so an absent field and
	// a zero one are the same request and neither is worth a flag: the
	// create verb's flags default to 0 too.
	if spec.VerificationFullEverySeconds > 0 {
		c.flag("verification-full-every", seconds(spec.VerificationFullEverySeconds))
	}
	if spec.VerificationRestoreDrillEverySeconds > 0 {
		c.flag("verification-restore-drill-every", seconds(spec.VerificationRestoreDrillEverySeconds))
	}
	if spec.Disabled {
		c.bare("disabled")
	}
	if spec.ReadOnly {
		c.bare("read-only")
	}
	if runNow {
		c.bare("run")
	}
	if acknowledgeRepoint {
		c.bare("acknowledge-repoint")
	}
	return c
}

// mediumSpecFlags puts a storage destination's description onto a `medium`
// command line, shared by add, edit and the candidate preflight.
//
// One helper for the three because both surfaces already treat them as
// one: core/cmd/retnd declares a single flag set that all seven
// verbs read, and this API sends a single body shape to all three of
// these routes, on the reasoning that what is proven and what is saved
// must not be able to be different destinations.
//
// Two of these flags are printed differently from what the request
// carried, and both are corrections to a claim this doc used to make. It
// said no flag here could carry credential material, as a fact about
// apicontract.StorageMediumRequest rather than a rule applied here. That
// was true of the three credential spellings that name a reference (an id
// this deployment minted, a path, a variable NAME) and false of the other
// two fields a secret can reach this function in:
//
//   - Endpoint is a free string this contract deliberately does not
//     validate, and https://AKIA...:wJalr...@minio.internal:9000 is a
//     spelling rclone accepts. endpointWithoutUserinfo takes the
//     credential out and leaves the address.
//   - Credentials.Command is a program and its arguments, both the
//     caller's words. It is named with a placeholder and never printed.
//
// What holds that now is not this paragraph. It is
// TestNoRequestFieldReachesTheArgvUnlessItIsEchoedOnPurpose, which drives
// every builder with a body whose every string field, on every schema in
// the contract, carries a distinct canary, and fails on any canary that
// reaches an argv behind a flag nobody wrote an exemption for.
//
// flagIfSet throughout, for backupSetCreateCommand's reason: a field the
// request did not carry must not echo as a flag with an empty value after
// it, which is a different command from the one that was made. Nothing is
// lost on an edit, either, because this wire shape has plain strings
// rather than pointers, so the request itself cannot tell "leave the
// prefix alone" from "clear the prefix"; the line says exactly as much as
// the request did.
func mediumSpecFlags(c *cmd, req apicontract.StorageMediumRequest) *cmd {
	c.flagIfSet("type", req.Type)
	c.flagIfSet("region", req.Region)
	// The endpoint with any userinfo taken out of it. See
	// endpointWithoutUserinfo: a credential can be spelled into this
	// field, and this is the one flag here whose value is not printed
	// exactly as the request sent it.
	c.flagIfSet("endpoint", endpointWithoutUserinfo(req.Endpoint))
	c.flagIfSet("bucket", req.Bucket)
	c.flagIfSet("prefix", req.Prefix)
	c.flagIfSet("storage-class", req.StorageClass)
	c.flagIfSet("upload-verification", req.UploadVerification)
	return mediumCredentialFlags(c, req.Credentials)
}

// mediumCredentialFlags echoes a credentials reference: the three that name
// something this deployment already holds, and a placeholder for the fourth.
func mediumCredentialFlags(c *cmd, creds apicontract.StorageMediumCredentialsReference) *cmd {
	c.flagIfSet("credentials-id", creds.CredentialsID)
	c.flagIfSet("credentials-file", creds.File)
	c.flagIfSet("credentials-env", creds.Env)
	if len(creds.Command) > 0 {
		// Named and never printed, unlike its three siblings. Those name
		// a reference this deployment already holds; this one is a
		// program and its arguments, both of them the caller's words, and
		// `printf %s AKIA...:wJalr...` is a valid credentials command and
		// a credential on a command line. No test can tell that apart
		// from an innocent one, so the value does not go on the line at
		// all and the line says it is not runnable as printed.
		c.placeholderFlag("credentials-command", "the command that prints these credentials")
	}
	return c
}

// mediumConfigurationFlags echoes a configuration request as `medium
// configure` flags (#1154). The fields come out in the request's own order,
// which the API sorts by field id, so one configuration is one line.
//
// An endpoint field has its userinfo taken out for mediumSpecFlags' reason:
// a credential can be spelled into a URL, and this panel is
// copy-to-clipboard and exportable. Every other field is printed as sent,
// since a field value is configuration an operator typed and the manifest
// gives a credential no field to be typed into.
func mediumConfigurationFlags(c *cmd, req apicontract.MediumConfigurationRequest) *cmd {
	c.flagIfSet("backend", req.Backend)
	for _, f := range req.Fields {
		// A credential-kind field is the one thing a manifest declares that
		// is not a value an operator types into a command line. The API
		// refuses it in this list, so no accepted request carries one, and
		// a refused one must not put its content on an exportable panel.
		if f.Field == "credentials" {
			continue
		}
		value := f.Value
		if f.Field == "endpoint" {
			value = endpointWithoutUserinfo(value)
		}
		c.flag("field", f.Field+"="+value)
	}
	return mediumCredentialFlags(c, req.Credentials)
}

// mediumSkipFlag echoes issue #636's opt-out when the request carried it.
//
// It is separate from mediumSpecFlags because that function is shared with
// the CANDIDATE preflight, where the field means nothing: that route IS
// the check, so there is nothing for it to skip, and a `medium
// test-connection --candidate --no-verify` line would be an instruction
// nobody can mean.
//
// Bare rather than assigned, because the flag's own default is false: an
// operator retyping this line gets the skip by naming it and gets the
// check by leaving it off, which is the same shape `--candidate` beside
// it has.
func mediumSkipFlag(c *cmd, req apicontract.StorageMediumRequest) *cmd {
	if req.SkipConnectionCheck {
		return c.bare("no-verify")
	}
	return c
}

func itoa(v int) string     { return strconv.FormatInt(int64(v), 10) }
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// mustQuery builds an example's query string. It panics rather than
// returning an error because the only caller is this file's own literals,
// so a bad one is a bug in this file and not a runtime condition.
func mustQuery(raw string) url.Values {
	v, err := url.ParseQuery(raw)
	if err != nil {
		panic("cliecho: bad example query " + raw + ": " + err.Error())
	}
	return v
}

// onOff is the word the two backup-set toggles take.
//
// Two words and not true/false, 1/0 or yes/no: a toggle that accepts six
// spellings is a toggle whose scripts each pick a different one, and the
// verb itself refuses everything but these two.
func onOff(v bool) string {
	if v {
		return "on"
	}

	return "off"
}

// RecordedReasons is the written reason each route outside /api/v1 has no
// verb and no entry in the table above, and is not in api/v1/openapi.json.
// Nothing here is echoed: these are the two unauthenticated probes an
// orchestrator calls, which are infrastructure and not operator actions.
//
// It exists so that "registered, with no answer" has two ways to be
// resolved, an entry or a reason, and so the route-to-verb guard in
// apps/common/webhost (issue #1150) can tell a probe somebody thought about
// from a route somebody forgot.
func RecordedReasons() map[string]string {
	return map[string]string{
		"GET /health/live":  "an unauthenticated liveness probe for an orchestrator, deliberately outside /api/v1 and the spec; `" + WebBinary + " healthcheck --url` is the command that calls any probe",
		"GET /health/ready": "an unauthenticated readiness probe for an orchestrator, deliberately outside /api/v1 and the spec; `" + WebBinary + " healthcheck --url` is the command that calls any probe",
	}
}

// sourceTarget spells the source-host target (issues #1191 and #1193):
// `--to source:` for a fresh folder, `--to source:FOLDER` for a named one,
// the colon always there so a staging folder called "source" stays one, and
// --acknowledge-source-write when the request acknowledged its destination
// (the terminal acknowledges the destination it resolves and prints).
func sourceTarget(c *cmd, folder, acknowledged string) {
	c.flag("to", "source:"+folder)
	if acknowledged != "" {
		c.bare("acknowledge-source-write")
	}
}

// stagingName spells a staging name so the terminal cannot read it as
// something else (#1193): a name that starts with "-" (standard output, or a
// flag), "source:" (the source host) or "medium:" (another destination) is
// written ./NAME, which means the same place under the staging root.
func stagingName(name string) string {
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "source:") || strings.HasPrefix(name, "medium:") {
		return "./" + name
	}
	return name
}
