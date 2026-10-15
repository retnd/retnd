// Command retnd is the entry point for every execution mode this project
// supports (FR-1, FR-26). It is `retnd` to an operator and this directory is
// still cmd/retnd, because the image symlinks the old name beside
// the new one and a Go package path is not something anybody types; the
// constant that decides what this binary prints itself as, and the whole
// of that argument, are in core/cliecho/cliname.go.
//
// It is deliberately thin: every command
// below does nothing but parse its own flags, reach exactly one use case,
// and format the result for a terminal. Most reach it by building (or
// reusing) an internal/app.Service; the configuration writes go through
// core/service.BackupService instead; and four of those writes and the
// four read surfaces put their question to a running engine over its HTTP
// API when one is serving this deployment and this host has been told
// where it is (#543, #544). No business rule lives on any of those paths;
// see internal/app's package doc for why, and for what "business rule"
// means in this project.
package main

import (
	"fmt"
	"os"

	"github.com/retnd/retnd/core/cliecho"
)

// Set at build time with -ldflags (see container/Dockerfile).
var (
	version = "dev"
	commit  = "none"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is main's own logic pulled out into a function so it can return an
// exit code instead of calling os.Exit directly, which would skip every
// deferred cleanup (closing the state journal, flushing output) between
// here and wherever a subcommand's own os.Exit might otherwise have been
// tempted to live.
func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}

	name, rest := args[0], args[1:]
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, cliecho.Binary+": unknown command %q\n\n", name)
		usage()
		return exitUsage
	}
	return cmd(rest)
}

// commands is every verb this binary dispatches.
//
// A map rather than a switch because it is read as data as well as walked:
// the discoverability tests ask it whether a verb exists at all, which is
// half of the check that a verb an operator cannot find has not shipped.
// The other half is usage below, and the two are separate lists on purpose.
// A verb here and not there is dispatchable and undiscoverable, which is how
// `backup-set remove` went out, so the gap between them is something a test
// can see rather than something only a reader would notice.
//
// There is a third list, and it is the one that had nothing watching it:
// core/tests/compat's corpus, which pins what each verb prints. A verb can
// be in both lists here and in none of that, which is how `unconfigured`
// and `medium preflight` shipped with FR-35 protecting not one word of
// their text. TestUsage_EveryRegisteredCommandIsPinned holds all three
// against each other now (#549).
var commands = map[string]func([]string) int{
	"run":             cmdRun,
	"daemon":          cmdDaemon,
	"check":           cmdCheck,
	"status":          cmdStatus,
	"sources":         cmdSources,
	"backup-set":      cmdBackupSet,
	"activity":        cmdActivity,
	"artifacts":       cmdArtifacts,
	"fetch":           cmdFetch,
	"retention":       cmdRetention,
	"reconcile":       cmdReconcile,
	"validate":        cmdValidate,
	"scrub":           cmdScrub,
	"recovery-test":   cmdRecoveryTest,
	"assurance":       cmdAssurance,
	"operation":       cmdOperation,
	"system":          cmdSystem,
	"catalog":         cmdCatalog,
	"quarantine":      cmdQuarantine,
	"unconfigured":    cmdUnconfigured,
	"medium":          cmdMedium,
	"storage":         cmdStorage,
	"retry":           cmdRetry,
	"restore":         cmdRestore,
	"recover":         cmdRecover,
	"snapshot":        cmdSnapshot,
	"repository":      cmdRepository,
	"settings":        cmdSettings,
	"workflow":        cmdWorkflow,
	"workflow-runner": cmdWorkflowRunner,
	"version":         cmdVersion,
	"validators":      cmdValidators,
}

// usage is the text an operator reads when they type nothing, type
// something wrong, or ask for help, and it is the only place most of these
// verbs are described at all.
//
// Every line of it is pinned. core/tests/compat captures this block and
// compares it against a checked-in corpus under FR-35's fourth clause, so a
// line that is reworded, reflowed or re-indented here is a compatibility
// break somebody has to justify, not a tidy-up. The one thing that is
// allowed is growth: that cell is compared additive-only, because a new
// subcommand adds lines and changes nothing an operator was already reading.
//
// So a new verb is two edits, here and in commands above, and the usage half
// is the one that is easy to skip and impossible to notice missing from the
// inside. The flags are spelled out per verb rather than summarised because
// this is the only reference an operator on a terminal has. The trailing
// paragraphs carry the flag they all share, where a configuration change
// actually goes now that it can go two places, and how to tell this
// binary where the engine is, which is the only place those three
// environment variables are written down for somebody who is not reading
// route.go, and the exit codes, which #551 put here for the same reason:
// a status a wrapper script branches on is a contract, and a contract read
// off setup.go by whoever thought to look is not one.
func usage() {
	// The first line names the command and is therefore the one line of
	// this block that is not a literal. It is printed on its own rather
	// than spliced into the raw string below, so that the index of
	// commands stays one uninterrupted backticked chunk: readers of this
	// file include distribution/packaging's README-parity test, which
	// pulls the command list straight out of the source, and a raw string
	// broken in half by a concatenation is a list it silently reads as
	// empty.
	fmt.Fprintf(os.Stderr, "usage: %s <command> [flags]\n", cliecho.Binary)
	fmt.Fprint(os.Stderr, `
commands:
  run                                            perform one processing cycle and exit
  daemon                                         repeat the processing cycle at poll_interval. One engine per
                                                  deployment: it is refused rather than started if another
                                                  process is already serving that state database (#536)
  check                                          validate config and the state database, then exit
  status [--json]                                report process and backup-set health (FR-24). A set the manager
                                                  could not connect to prints a "halted:" line under it with the
                                                  reason and the fix. --json prints GET /system/health's object plus
                                                  "process" (#1146)
  sources                                        list configured sources and backup sets
  sources --json                                 the same read as ListBackupSetsResponse, every field of every set,
                                                  which is what GET /api/v1/backup-sets returns (#1151)
  backup-set create <source/backup-set> --host H --user U --remote-path P --local-path P
                    --ssh-key-file K|--ssh-key-id ID --known-hosts-line L|--trust-host-key
                    --completion-strategy rename|marker|stable [--include A,B] [--stable-for D]
                    [--stale-after D] [--validator-id V] [--disabled] [--read-only] [--run]
                    [--engine artifact|kopia] [--repository-domain D] [--source-consistency S]
                    [--verification-level L] [--verification-sample-percent N]
                    [--verification-full-every D] [--verification-restore-drill-every D]
                    [--uuid U] [--source-mount-prefix P]
                    [--no-verify]
                                                  create a backup set. Beside a serving engine this command has a
                                                  route to, that is POST /api/v1/backup-sets against the engine
                                                  (#543); with nothing serving, it is the same service layer that
                                                  route is built on, called here. On an instance with no config.yaml
                                                  yet this writes the first one instead (#176), and --state-database
                                                  names the journal it points at; that create has no route, so it is
                                                  refused while something serves that deployment (#536)
                                                  The connection is proven before anything is written, and the create is
                                                  refused when it cannot be: --no-verify writes it anyway, says in so many
                                                  words that nothing was proven, and leaves the set marked as unverified
                                                  until a connection test passes (#624)
                                                  --uuid is the durable key an incremental set's snapshot lineage
                                                  hangs off. Leave it off and one is minted and printed; pass it when
                                                  re-declaring a set whose configuration was lost, because a second
                                                  create without it starts a new lineage beside the old one and the
                                                  next run re-reads the whole source. --source-mount-prefix is the
                                                  leading part of --remote-path that is how this deployment reaches
                                                  the source, so moving a bind mount does not fork the lineage (#1151)
  backup-set patch <source/backup-set> [--host H] [--port N] [--user U] [--remote-path P] [--local-path P]
                    [--include "A,B"] [--completion-strategy S] [--stable-for D] [--stale-after D] [--validator-id ID]
                    [--ssh-key-file K|--ssh-key-id ID] [--known-hosts-line L|--trust-host-key]
                    [--source-consistency S] [--verification-level L] [--verification-sample-percent N]
                    [--verification-full-every D] [--verification-restore-drill-every D]
                    [--poll-interval D]
                    [--acknowledge-repoint] [--acknowledge-host-key-change]
                    [--no-verify]
                                                  change one configured backup set in place; only the flags you pass are
                                                  changed. Beside a serving engine this command has a route to, that is
                                                  PATCH /api/v1/backup-sets/{source}/{set} against the engine and takes
                                                  effect with no restart; with no route it is refused and the file is
                                                  left untouched (#350, #536, #543)
                                                  An edit that changes the host, port, user, key, trusted host key or
                                                  remote path is proven before it is written and refused when it cannot
                                                  be; --no-verify writes it anyway and marks the set unverified (#624)
                                                  --poll-interval D gives this set its own poll interval, 0 returns it
                                                  to the deployment's, and nothing under a minute is accepted (#1151)
  backup-set show <source/backup-set> [--json]   print one backup set whole: its host and key, the trusted host
                                                  keys, whether its connection was ever proven, its own poll interval
                                                  and the one in force, its engine, lineage key, repository domain,
                                                  verification budget and mount prefix. --json prints the BackupSet
                                                  object GET /api/v1/backup-sets/{source}/{set} returns. A read of this
                                                  host's configuration, checked against a serving engine the way
                                                  sources is (#1151)
  backup-set remove <source/backup-set>          take one backup set out of the configuration: DELETE
                                                  /api/v1/backup-sets/{source}/{set} against a serving engine this
                                                  command has a route to, and the same service layer that route is
                                                  built on when nothing is serving. Configuration only: the backups it
                                                  collected stay on storage and stay listed by artifacts, and creating
                                                  the set again with the same source and name takes them back (#391)
  backup-set test-connection <source/backup-set>
                                                  prove one configured backup set's source: resolve the host, connect,
                                                  check its host key against what this set trusts, offer the configured
                                                  key, authenticate, and list the remote folder. Six named outcomes, and
                                                  a non-zero exit when any of them fails. Beside a serving engine this
                                                  command has a route to, the check is made BY that engine, so its steps
                                                  reach the live feed rather than only this terminal; with nothing
                                                  serving it is made here. A check that passes clears the unverified mark
                                                  a --no-verify create or patch left on the set. "preflight" is the same
                                                  verb under the name the storage-destination side spells it (#596, #624)
  backup-set test-connection <source/backup-set> --json
                                                  print the API's TestConnectionResponse instead of text. The report is the
                                                  result whether or not the connection works, and the exit code stays the
                                                  verdict (1 when it fails). The mode line and the startup log go to stderr,
                                                  so the document is all of stdout (#1152)
  artifacts [--source S] [--backup-set B]        list journal artifacts
                                                  --backup-set takes the source/backup-set id sources, status and
                                                  retention name a backup set by, and a plain set name where one
                                                  source configures it. Not this list's own first column: that one
                                                  is the whole artifact id, a field longer, so drop the file name
                                                  off the end of it. A name two sources share names neither of
                                                  them, so it is refused with both ids rather than answered for one
                                                  of them (#569)
  artifacts <source/backup-set/name>             print one artifact's full detail, including the reason
                                                  recorded for a FAILED/QUARANTINED/QUARANTINED_LOST one (#284)
  artifacts <source/backup-set/name> --json      print that detail as the API's Artifact object. In either form the detail
                                                  also prints, for each copy with a record, what its scrubs and recovery
                                                  tests found: assurance_state, last_checked_at, last_failure_at,
                                                  last_failure_reason, consecutive_failures, recovery_state (readable
                                                  for a built-in structure pass), recovery_tested_at,
                                                  recovery_failure_reason and recovery_check (#1147)
  artifacts [--source S] [--backup-set B] --state S
                                                  list only the artifacts in one lifecycle state, e.g. QUARANTINED_LOST
  artifacts [--source S] [--backup-set B] --json
                                                  list as the API's ListArtifactsResponse (#1147)
  activity [--backup-set S] [--severity warn|error] [--limit N] [--json]
                                                  list recorded lifecycle events, newest first: the same durable
                                                  transition log the Web UI's Activity page draws. Beside a serving
                                                  engine this reads GET /api/v1/activity from it; with nothing
                                                  serving it reads this host's own journal (#598). --limit counts
                                                  MATCHING events, so a filter that narrows still fills it. --json
                                                  emits the wire objects unchanged, so a script parses the contract
                                                  rather than this table
  activity --before CURSOR [--backup-set S] [--severity warn|error] [--limit N] [--json]
                                                  read the page behind a cursor. --json prints next_cursor when a page was full,
                                                  and a text page names it on stderr as "more: --before N", so a script walks the
                                                  whole log a page at a time instead of raising --limit. A cursor that is not a
                                                  row id is refused, because the feed would read it as no cursor and hand back
                                                  the newest page again. A filtered page cut by --limit has no cursor, and says
                                                  why: the filter runs after the read, so a cursor could only skip the rest.
                                                  Not combinable with --follow (#1152)
  activity --follow [--backup-set S | --scope deployment] [--severity warn|error] [--limit N] [--json]
                                                  stream the LIVE feed instead, until interrupted: the serving
                                                  process's own event stream, which is what the Web UI's docked
                                                  terminal shows. A different feed from the one above rather than a
                                                  mode of it, so it needs a route to that process and refuses
                                                  without one instead of quietly reading the journal. The feed
                                                  starts again, and says so, if that process restarts (#573, #598).
                                                  --scope deployment narrows it to the log that names no backup
                                                  set (a cycle starting, a capacity check, what somebody just
                                                  clicked), which is the whole feed on a deployment with nothing
                                                  configured yet (#593)
  fetch --source S --backup-set B [--dry-run]    run one backup set's cycle on demand
                                                  --backup-set takes the source/backup-set id here too, and names
                                                  the source itself when it carries one, so --source is only
                                                  needed for the plain set name (#569)
  retention [--dry-run] [--timezone T] [--week-starts-on D] [--daily-days N] [--weekly-months N] [--monthly-months N] [--protect-last-known-good]
            [--tier NAME:GRANULARITY:KEEP[:WINDOW_UNIT]] [--tier-medium NAME=MEDIUM_ID]
                                                  preview GFS/last-known-good retention decisions. It deletes nothing in
                                                  either mode, so --dry-run is accepted and inert here; FR-20 deletion runs
                                                  through the API's retention preview/apply pair, against a reviewed plan_id.
                                                  Each retention flag overrides the loaded config's own resolved value for
                                                  this preview only.
                                                  --tier is repeatable and replaces the whole chain; --tier-medium says
                                                  where one of those tiers' copies go, so a supplied chain is previewed
                                                  against the destinations it names rather than silently against the local
                                                  backup root. A supplied chain that names none, beside a deployment whose
                                                  own chain does, says so before the plan (#595)
  retention <source/backup-set> [--dry-run] [the same retention override flags]
                                                  preview that one backup set's decisions instead of every configured
                                                  set's. An id that names no configured backup set is refused and
                                                  nothing is printed, rather than answered about a set nobody asked
                                                  about (#568)
  retention <source/backup-set> --json
                                                  print that backup set's RetentionPlan as GET .../retention/preview serves
                                                  it, plan_id included. The plan_id is minted by this process and applies
                                                  here and nowhere else. It names one backup set and takes no what-if flag,
                                                  because the API's plan is decided under the deployment's own policy; the
                                                  text form is unchanged (#1152)
  retention apply <source/backup-set> --acknowledge
                                                  delete the local copy of every backup in that set no retention
                                                  tier keeps, against the plan it prints first. FR-19's last known
                                                  good is never among them, and a set whose inventory or
                                                  configuration moved between the two is refused with nothing
                                                  deleted. --acknowledge is required (#602)
  reconcile                                      run FR-17 reconciliation for every backup set
  validate <source/backup-set/artifact>          re-check one artifact's durable local copy
  validate <source/backup-set/artifact> [--content]
                                                  where that copy is on a storage medium instead, check it there: the
                                                  strongest class that costs nothing, by default, and with --content a
                                                  full download and re-hash, which costs egress, so FR-31 makes it
                                                  something an operator asks for rather than something that happens
                                                  (#435)
  scrub [<source/backup-set> | <source/backup-set/artifact> [--medium ID]] [--content --allow-egress]
                                                  check the durable copies of what is named now, or every copy that is
                                                  due when nothing is named: the scrub the daemon runs on a schedule,
                                                  by hand. Each result prints the class that ran, and an existence
                                                  check is never called verification of the bytes. A copy in an archive
                                                  storage class is never read and no restore is started. --content
                                                  reads a copy on a storage medium in full, which costs egress, so it
                                                  also needs --allow-egress. Exit 1 when a copy failed, could not be
                                                  checked, was skipped (held for editing, or being checked by another
                                                  scrub) or was refused for cost (EPIC Q)
  recovery-test <source/backup-set> [--artifact <source/backup-set/name>] [--acknowledge-cost] [--json]
                                                  prove now that the newest known good copy can be got back and used:
                                                  the copy is staged by the recovery engine, checked against the
                                                  SHA-256 recorded when it was ingested, and only then handed to the
                                                  set's validator script (recovery-test/<source>/<set>, selected by the
                                                  policy's validator_id). A read that costs money is refused unless
                                                  --acknowledge-cost is given, and no provider restore is ever started.
                                                  Exit 0 when the copy passed, 10 when the read was refused for cost,
                                                  1 otherwise (failed, or could not be completed). The same test as
                                                  POST /api/v1/backup-sets/{source}/{set}/recovery-test, which starts
                                                  it in the background (EPIC Q)
                                                  --follow prints the test's stages on standard error as it passes
                                                  through them; the exit status is unchanged
  recovery-test <source/backup-set> --show-output [--json]
                                                  print what the newest recovery test of the set printed, as the
                                                  engine stored it: sanitised, bounded, one line each, with the
                                                  runner's own marker if the capture was cut. It reads and runs no
                                                  test, so it takes none of the flags above. The same body as
                                                  GET /api/v1/backup-sets/{source}/{set}/recovery-test/output
                                                  (EPIC Q)
  assurance cancel <operation-id> [--json]
                                                  stop a running scrub or recovery test by the operation id its start
                                                  returned. The operation lives in the memory of the process serving
                                                  this deployment, so this needs RETND_API_URL and the two credential
                                                  variables beside it and is refused without them. The same as
                                                  POST /api/v1/assurance/operations/{op}/cancel (EPIC Q)
  operation list [--backup-set S] [--action A] [--json]
                                                  list the operations the engine holds, newest first, so there is an id
                                                  to hand to assurance cancel. They live in the memory of the process
                                                  serving this deployment, so this needs RETND_API_URL and the two
                                                  credential variables beside it and is refused without them. The same
                                                  body as GET /api/v1/operations; the two filters narrow it here (#1149)
  operation show <operation-id> [--json] [--follow]
                                                  one operation as the engine holds it. --follow polls until it finishes,
                                                  prints each change of status on standard error, and exits 0 for
                                                  completed and 1 for anything else. The same as
                                                  GET /api/v1/operations/{id} (#1149)
  catalog rebuild [--dry-run]                    reconstruct a lost/corrupted state database from sidecar recovery manifests
  catalog rebuild [--dry-run] --json
                                                  print the API's CatalogReportResponse for the whole deployment instead of
                                                  one line per artifact. A manifest that could not be used is a failures
                                                  entry and still exits 1 (#1152)
  quarantine <revalidate|retry|reinstate> <source/backup-set/artifact> [--note T]
                                                  act on one quarantined artifact: revalidate re-checks it and moves
                                                  nothing; retry re-enters the pipeline from DISCOVERED; reinstate
                                                  trusts it again in place and forfeits any future remote delete
  quarantine list [--backup-set source/backup-set] [--json]
                                                  list the artifacts held in quarantine: id, reason, irrecoverable and
                                                  updated_at. The same as GET /api/v1/quarantine, which --json prints
                                                  (#1147)
  unconfigured                                   list the backup sets the journal remembers and the configuration no
                                                  longer names, what they still hold on storage, and the retention
                                                  policy governing them, which is none (#418)
  unconfigured clear <source/backup-set> [--acknowledge]
                                                  clear the .partial residue a removal stranded mid-transfer, and end
                                                  the journal rows nothing will ever advance. It never touches a
                                                  retained backup; without --acknowledge it only prints what it would do
  medium list [--json]                           list every storage destination this deployment declares, in
                                                  declaration order. No credential is reported, not even which of
                                                  the three sources a destination reads (#594)
  medium show <medium-id> [--json]               one destination, plus what the journal says is currently on it:
                                                  how many copies, which backup sets they belong to, and how many
                                                  of them are the only confirmed copy of their artifact anywhere
  medium import-credentials --stdin              read AWS shared-credentials text from standard input, write it
                                                  0600 beside this deployment's configuration, and print an id to
                                                  name it with. This is the only command here that ever holds a
                                                  secret and it takes it on stdin: there is deliberately no
                                                  --access-key-id and no --secret-access-key flag anywhere on this
                                                  surface, because a secret on a command line is in ps output for
                                                  every user on the box and in shell history
  medium add <medium-id> --bucket B [--type s3] [--region R] [--endpoint URL] [--prefix P]
             [--storage-class C] [--upload-verification readback|attested] [--no-verify]
             (--credentials-id ID | --credentials-file PATH | --credentials-env VAR | --credentials-command 'prog arg ...')
                                                  declare a storage destination without editing config.yaml. It
                                                  VERIFIES FIRST and writes nothing when verification fails, which
                                                  is what makes it scriptable across a fleet; --no-verify skips
                                                  that and says in its own output that nothing was proven. Every
                                                  credential flag names a REFERENCE and never material
                                                  a --no-verify destination is MARKED unverified in config.yaml and
                                                  stays marked until a test connection passes, so an operator who did
                                                  not type the command can still tell it apart from a proven one (#636)
  medium edit <medium-id> [the same flags]       replace a destination's description. A flag left off keeps what
                                                  the destination already says, and leaving the credential flags
                                                  off keeps the credential already configured, which it has to:
                                                  nothing here ever reports one back to be resubmitted
  medium remove <medium-id>                      un-declare a destination. Refused while any copy names it, and
                                                  the refusal lists the backup sets: removing the declaration would
                                                  not delete those copies, it would leave this deployment with no
                                                  bucket, no endpoint and no credential to reach them with (FR-30)
  medium preflight <medium-id>                   prove one declared storage medium actually works before a cycle
                                                  carrying a real backup does: it writes a probe object with the
                                                  medium's own storage class, reads it back byte for byte, checks
                                                  the class it landed in against the one the configuration claims,
                                                  asks whether the medium's declared upload_verification can
                                                  actually be achieved there, and deletes the probe. Exits non-zero
                                                  when any check fails (#443)
                                                  a check that PASSES clears a --no-verify destination's unverified
                                                  mark, which is a configuration write, so this verb goes where the
                                                  writes go: beside a serving engine it is carried out there, and is
                                                  refused when nothing says how to reach it. A failing check leaves
                                                  the mark alone (#636)
  medium preflight --candidate <medium-id> [the add flags]
                                                  the same eight checks against a destination that is NOT declared,
                                                  so a setup flow proves one before it is written down. It writes
                                                  nothing whatever the report says
  medium test-connection <medium-id>             the same check as medium preflight, under the name the rest of this
                                                  product uses for it. One check, two verbs, one implementation:
                                                  preflight is kept as an alias so anything scripted against it goes on
                                                  working, and this is the name the web UI's button and the command it
                                                  echoes now carry. It also answers for "local", the drive this
                                                  deployment's backups land on, reporting a missing path, a directory
                                                  the service user cannot write to, and a full filesystem as three
                                                  different failures rather than being unavailable because no network
  medium test-connection <medium-id> --json
                                                  print the API's MediumPreflightResponse instead of text, for
                                                  test-connection, preflight and preflight --candidate alike. The report is
                                                  the result whether or not the destination works and the exit code stays
                                                  the verdict; the mode line and the startup log go to stderr (#1152)
                                                  is involved (#622)
  medium default <medium-id>                     make this the destination a NEWLY CREATED retention tier starts on.
                                                  It moves that and nothing else: every tier that already names a
                                                  destination goes on naming it and no backup is relocated. "local" is
                                                  a legal id here and is what a deployment that has chosen nothing
                                                  already has, so moving the default to a bucket is reversible. The
                                                  destination that is the default cannot be removed, and removing down
                                                  to one destination makes that one the default (#622)
  medium configure <medium-id> --backend B --field ID=VALUE [--field ...] [--credentials-id ID | --credentials-file F | --credentials-env VAR | --credentials-command CMD] [--json]
                                                  declare or replace a destination in its backend's own field ids, as
                                                  medium backends lists them. This is the only way to configure a
                                                  local_volume (--field path=/mnt/usb), which medium add cannot, as
                                                  it has no --path. --backend is required for a destination that is
                                                  not declared yet. It REPLACES the whole field set, so leave out only
                                                  what you want unset, and an empty value is refused. The engine
                                                  proves the configuration before it writes and refuses when it fails.
                                                  Credentials are a reference, never material; with none named, the one
                                                  already configured is kept. Goes to the serving engine like medium add
                                                  (#1154)
  medium configure <medium-id> --backend B --field ID=VALUE ... --preflight [--json]
                                                  prove that configuration and write nothing, whatever the report says.
                                                  Exits 1 when the destination does not work (#1154)
  medium backends [--json]                       list the backends a destination can be an instance of, with each one's
                                                  field ids, which are required, what an unset one means and the probe
                                                  steps it runs, plus the rules an instance id follows. Reads the
                                                  registry built into this binary, so it needs no configuration. --json
                                                  prints GET /api/v1/backends (#1154)
  storage [--json]                               the capacity reading, which status does not print: this manager's own
                                                  consumption against the volume it writes to, the cap if one is set and
                                                  which of the two the gauge is drawn against, the headroom before the
                                                  next transfer is refused and what binds it, then each backup set's
                                                  level. Under the API's own field names; --json prints GET
                                                  /api/v1/system/storage. Exits 0 whenever the reading was taken,
                                                  whatever level it found; status is the verb that exits non-zero (#1154)
  retry <source/backup-set/artifact> [--note T]   put one FAILED backup back into the pipeline so it is attempted
                                                  again. FAILED means an attempt did not finish, which is not the
                                                  same thing as quarantine, so this is its own command and not a
                                                  fourth quarantine verb. Nothing does this automatically: a blind
                                                  re-transfer for a cause nothing has classified is a cost this
                                                  manager does not take on its own (#419). When the artifact's own
                                                  durable local copy is still intact this completes it in place
                                                  instead of re-fetching, and that also forfeits any future remote
                                                  delete, the same as quarantine reinstate (#662)
  restore <source/backup-set/artifact> --medium M [--days N] --acknowledge
                                                  ask the storage provider to make one archived copy readable again
                                                  (EPIC E, FR-34). --acknowledge is required rather than a --force
                                                  to skip, because a restore is billed and takes hours; --days
                                                  defaults to 7 and is bounded to 1..30. artifacts <id> lists
                                                  which medium each copy is on
  recover <source/backup-set/artifact> --to - | NAME | medium:ID[/KEY] [--copy M] [--acknowledge-cost] [--json]
  recover --set <source/backup-set> [--newest-only] [--since DATE] --to NAME | medium:ID[/PREFIX] [--preflight] [--acknowledge-cost] [--json]
  recover <id> <id> ... --to NAME | medium:ID[/PREFIX] [--preflight] [--acknowledge-cost] [--json]
                                                  get the retained bytes of one artifact back (EPIC Q, FR-67). It
                                                  reads one copy, checks all of it against the SHA-256 recorded when
                                                  the backup was ingested, and only then writes it: to standard
                                                  output (-), to a new file under the recovery staging root (NAME),
                                                  or to another configured storage destination, never replacing an
                                                  object. --copy defaults to the best readable copy, preferring the
                                                  strongest verification class and one that costs nothing to read.
                                                  A billed download, or a copy on an archive class that needs a
                                                  provider restore first, is refused naming the cost unless
                                                  --acknowledge-cost is given; an acknowledged archive request
                                                  starts exactly one restore and exits 31 with nothing recovered.
                                                  It runs no application command: this recovers the artifact, and
                                                  putting the application back stays yours. It is neither restore
                                                  (the archival thaw above, which writes nothing) nor snapshot
                                                  restore (a kopia restore point), and a kopia set is refused with
                                                  a sentence naming that verb
                                                  --follow prints the recovery's stages on standard error as it
                                                  passes through them, in the engine's order; the exit status is the
                                                  recovery's own
                                                  With --set (a whole backup set) or two or more ids, the same
                                                  recovery is done for every file into ONE destination, under a
                                                  per-set folder, never to standard output. A pre-flight comes
                                                  first: the number and size of the files, what is quarantined,
                                                  lost or needs a thaw, the cost of the whole set, and whether it
                                                  fits the destination (refused with the numbers if not, before a
                                                  byte is read). --newest-only and --since narrow a set;
                                                  --preflight stops after that. Each file is checked like a single
                                                  recovery; one failing does not stop the rest. A progress line per
                                                  file and a running total are printed, Ctrl-C cancels the run and
                                                  leaves no half-written file, and a manifest listing every file
                                                  with its digest is written beside them. Exits 0 when every row was
                                                  recovered and 32 when some were not (failed, needs a thaw,
                                                  quarantined or lost); a kopia set is refused naming snapshot restore
  backup-set enabled <source/backup-set> <on|off>
                                                  turn one already-configured backup set on or off, which
                                                  --disabled can only do at creation. "off" stops the scheduler
                                                  offering the set to the next cycle; a pass already inside it
                                                  finishes (#788)
  backup-set read-only <source/backup-set> <on|off>
                                                  declare whether this manager may ever delete this backup set's
                                                  remote originals, which --read-only can only do at creation.
                                                  "on" is the promise never to delete from the source; "off"
                                                  gives that promise up and is refused when the source itself
                                                  cannot support it (#282, #788)
  snapshot list <source/backup-set>              every snapshot run this incremental backup set has on record,
                                                  newest first: what each one scanned, read from the source,
                                                  wrote and reused, and what it proved. Five separate numbers
                                                  and deliberately no total, because a deduplicated run that
                                                  scanned 100 GB and stored 200 MB is not a 100 GB upload.
                                                  "not measured" is a real answer and never a zero (EPIC K)
  snapshot show <source/backup-set> <run-id>     one snapshot run in full, plus the transition log that says how
                                                  it reached the phase it is in
  snapshot holds <source/backup-set>             every unreleased hold over this set's snapshots: what is being
                                                  kept, why, and by whom
  snapshot retention <source/backup-set>         what snapshot retention would decide about each of this set's
                                                  snapshots right now -- KEEP, DELETE or REFUSE, each with the
                                                  reason. A preview; it deletes nothing
  snapshot list|holds|retention <source/backup-set> --json
  snapshot show <source/backup-set> <run-id> --json
                                                  print the API's ListSnapshotsResponse, ListSnapshotHoldsResponse,
                                                  SnapshotRetentionResponse or SnapshotResponse instead of text. A counter
                                                  nobody measured is null and never 0, and an empty list is an array (#1152)
  snapshot verify <source/backup-set> [--run R] [--level L] [--sample-percent N]
                                                  prove now that a restore point is readable, at a stated depth:
                                                  structural, content_sample, content_full or restore_drill.
                                                  Without --run it verifies the set's last known good snapshot,
                                                  and without --level it uses the level the set is configured
                                                  for. Exits non-zero when the verification finds damage
  snapshot restore <source/backup-set> --to DIR [--snapshot ID] [--path P] [--conflict refuse|skip|overwrite]
                                                  restore one snapshot, or one path inside it, into a local
                                                  directory. This is NOT restore above: that one asks a storage
                                                  provider to make an ARCHIVED COPY readable again, over hours
                                                  and at a cost. --conflict defaults to refuse, so nothing
                                                  already at the destination is replaced unless you say so
  snapshot hold <source/backup-set> --reason R [--run R]
                                                  stop retention deleting one snapshot until somebody releases
                                                  the hold. --reason is required: a hold nobody explained is one
                                                  nobody dares release, which makes it permanent by accident
  snapshot unhold <source/backup-set> <hold-id>  end one hold, by the id snapshot holds prints. Releasing
                                                  deletes nothing; it returns the snapshot to whatever the
                                                  retention policy already said about it
  repository create <repository-domain> --isolation shared|isolated
                    (--passphrase-file F | --passphrase-env V | --passphrase-command W ...)
                    [--description D] [--location L] [--owner this|another-instance]
                                                 declare a repository domain: the boundary an incremental backup
                                                  set stores its snapshots in. --isolation is required and has no
                                                  default, because sets sharing a domain share one encryption key,
                                                  one credential, one maintenance owner, one deduplication pool
                                                  and one corruption blast radius. The passphrase is a REFERENCE
                                                  in all three spellings and there is no flag to type one into;
                                                  repeat --passphrase-command once per argv word. Nothing is
                                                  stored yet: the repository itself is written the first time a
                                                  backup set stores a snapshot in the domain
  repository health                              every declared repository domain and its own verdict:
                                                  reachable, readable, writable, whether the declared passphrase
                                                  opens it, whether this machine's clock can be trusted for the
                                                  timestamps a repository reasons about, whether maintenance is
                                                  overdue, and the last snapshot and verification it holds. Exits
                                                  non-zero when a repository is FAILING
  repository maintenance <repository-domain>     who owns maintenance for one repository, when it last ran quick
                                                  and full, when it is next eligible, and whether it is overdue
                                                  or failing. Opens nothing, so it answers while the repository
                                                  itself is unreachable
  repository health|maintenance --json
                                                  print the API's ListRepositoriesResponse or RepositoryMaintenance object
                                                  instead of text. health still exits 1 when a repository is FAILING, so a
                                                  monitoring job that moves to --json keeps its alarm (#1152)
  settings [patch [--timezone T] [--week-starts-on D] [--protect-last-known-good=BOOL]
                   [--policy-file F] [--acknowledge-medium-disclosure]
                   [--cap-bytes N] [--warning-free-bytes N] [--critical-free-bytes N] [--safety-margin-bytes N]]
                                                  report the live retention/capacity settings, or change one in place.
                                                  --policy-file replaces the deployment's whole retention chain from a
                                                  file holding the contents of a config.yaml "retention:" block, with "-"
                                                  reading standard input, spelled the way backup-set retention spells it.
                                                  A chain that sends a tier somewhere new needs
                                                  --acknowledge-medium-disclosure, and without it the refusal carries the
                                                  disclosure (#595)
  settings patch --poll-interval D               set the deployment-wide poll interval, how often each source is checked
                                                  for new backup files. A backup set follows it unless it pins its own
                                                  with backup-set patch --poll-interval, and nothing under a minute is
                                                  accepted. settings prints it in a service: section (#1151)
  settings patch --tier-medium NAME=MEDIUM_ID [--acknowledge-medium-disclosure]
                                                  point one retention tier at a storage destination and leave the rest of
                                                  the chain exactly as it is. Repeatable, at most once per tier, and
                                                  refused when the chain has no tier of that name rather than inventing
                                                  one. MEDIUM_ID is a declared destination, or "local" for the drive
                                                  this deployment's backups already land on, which moves a tier back.
                                                  Sending a tier somewhere other than local for the first time needs
                                                  --acknowledge-medium-disclosure. This is the command the picker under a
                                                  tier in the web UI echoes (#622)
  settings --json
                                                  print the API's SettingsResponse (GET /settings) instead of text: the
                                                  resolved retention, capacity and service settings, the declared
                                                  destinations, the schema they are validated against and first_run. A read
                                                  only: settings patch refuses --json (#1152)
  backup-set edit-hold <source/backup-set> [--release]
                                                  report whether a backup set is held for editing, what taking the
                                                  hold stopped, and when the lease expires; --release gives it back
                                                  so the scheduler may run the set again rather than waiting for it
                                                  to lapse. A hold lives in the memory of the process serving this
                                                  deployment and does not survive it, so this needs a route to that
                                                  process and refuses without one. There is no verb that TAKES a
                                                  hold: a hold protects an editing session and a CLI edit is one
                                                  backup-set patch that either runs or does not (#350, #600)
  backup-set retention <source/backup-set> [--inherit] [--policy-file F]
                       [--timezone T] [--week-starts-on D]
                       [--daily-days N] [--weekly-months N] [--monthly-months N]
                       [--protect-last-known-good=BOOL] [--acknowledge-medium-disclosure]
                                                  report which retention policy this backup set is retained under and
                                                  where it came from; with a policy flag, give the set a whole policy of
                                                  its own; with --inherit, remove that policy so it is retained under the
                                                  deployment's again. An override replaces the deployment's whole chain
                                                  and is never merged with it, so it has to name a whole one.
                                                  --acknowledge-medium-disclosure is needed when the policy sends one of
                                                  this set's tiers somewhere new, and without it the refusal carries the
                                                  disclosure. The show form prints each tier's destination
                                                  Setting a policy and --inherit are handed to a serving engine when
                                                  RETND_API_URL names it (PUT and DELETE .../retention).
  backup-set retention <source/backup-set> --json
                                                  print the API's BackupSetRetention object instead of text, with override
                                                  present and null for a set that inherits. A read only: setting or clearing
                                                  a policy prints text and refuses --json (#1152)
  workflow-runner serve --runtime-dir DIR --workspace-dir DIR --secrets-dir DIR
                       [--docker PATH] [--hook-image REF] [--hook-bash PATH]
                       [--hook-network NAME] [--hook-user UID:GID] [--hook-mount PATH[:ro|:rw]]
                                                  run the host workflow runner: the out-of-container helper that
                                                  executes a backup set's .local.sh hooks on THIS machine. The engine
                                                  container is distroless, read-only and has no shell, so "local"
                                                  means the host and the host needs a process of its own. Each hook
                                                  runs in an EPHEMERAL DOCKER CONTAINER (#865) -- hardened, with no
                                                  network by default, as a non-root user, with only its own working
                                                  directory and whatever --hook-mount allows mounted, and never the
                                                  docker socket -- so this command needs a docker client and a
                                                  daemon it can reach, proved at startup or it refuses to serve.
                                                  It listens on a Unix socket inside --runtime-dir and on nothing
                                                  else: there is no TCP listener and no address to set.
                                                  --runtime-dir holds that socket and nothing else, because it is
                                                  the one directory the engine container mounts; the per-step
                                                  working directories go in --workspace-dir, which nothing mounts.
                                                  It refuses to run as root, refuses an engine from a different
                                                  release, and refuses a client that does not hold the credential
                                                  in --secrets-dir. The installer provisions all of them and
                                                  supervises this command (#809)
  workflow-runner status --runtime-dir DIR --workspace-dir DIR --secrets-dir DIR
                                                  ask that runner what it is: version, socket, the docker client and
                                                  hook image it proved at startup, the bash inside that image, the
                                                  host paths a hook can see, the account hooks run as, and what it
                                                  is running now. The same question the engine asks before it will
                                                  validate a .local.sh hook, so an answer here is the answer a
                                                  backup would get
  workflow run list [--backup-set S] [--limit N] [--json]
                                                  every hook run this deployment has on record, newest first: each run's
                                                  three statuses side by side (the backup's, the cleanup's and the
                                                  workflow's), because "the backup succeeded and the cleanup did not" means
                                                  a machine may be sitting quiesced with a good backup beside it, and a
                                                  surface that folded the three into one verdict would make exactly that
                                                  case unsayable (#813)
  workflow run show <run-id> [--json]            one run in full: the three statuses, whether its hooks were deliberately
                                                  bypassed, the first step that failed and the script it ran, how long it
                                                  took, and every step in execution order
  workflow run steps <run-id> [--json]           that run's steps alone, in plan order: where each sits in the five
                                                  stages, where it ran, its exit code where a process status was observed
                                                  at all, and whether this product proved the process was gone
  workflow run log <run-id> --step <step-id> [--follow] [--cursor N] [--limit N] [--json]
                                                  one step's captured output, with stdout and stderr kept apart, and this
                                                  product's own truncation marker rendered as its own rather than as
                                                  something the hook printed. Every read prints the cursor it reached, so
                                                  --cursor resumes an interrupted one with nothing printed twice and
                                                  nothing skipped; --follow waits for more until the step ends
  workflow recovery show [--json]                every run still waiting for a cleanup or for a person, oldest first,
                                                  because the question is how long a machine has been left like that.
                                                  Beside a serving engine it asks THAT process, which holds the refusals
                                                  a run will really be met with, when $RETND_API_URL says where
                                                  it is; otherwise it reads the journal's own rows, because this process
                                                  has no reconciled engine and a reconciliation pass from a terminal
                                                  would mark a serving engine's in-flight run interrupted
  workflow recovery resume-cleanup <run-id> [--json]
                                                  run the "after" hooks an interrupted run still owes, out of that run's
                                                  own captured bytes, re-verified against the hashes recorded when the plan
                                                  was taken, so an edit to the hook directory since the interruption
                                                  changes nothing about what runs. Exits non-zero when the run is still not
                                                  settled afterwards, because the backup set stays blocked. Beside a
                                                  serving engine it is handed to that process, or refused when nothing
                                                  says how to reach one: recovery state lives in that process's memory as
                                                  well as in the journal, so a resume performed here would unblock a set
                                                  it would go on refusing
  workflow recovery acknowledge <run-id> --reason "..."
                                                  record that a person dealt with an interrupted run by hand, and unblock
                                                  the backup set. --reason is required and a blank one is refused: the
                                                  whole value of the record is answering, six months later, why a set was
                                                  unblocked without its cleanup ever running. Nothing is executed. Routed
                                                  to a serving engine, or refused beside one this command cannot reach,
                                                  for resume-cleanup's reason
  settings workflow [--json]                     report the resolved deployment-wide hook configuration: the approved
                                                  root, the two global stages, the timeout a hook gets and whether that
                                                  came from the file or from the built-in default, the declared execution
                                                  connections, the host runner this process will try to reach, and the
                                                  deployment-wide environment
  settings workflow patch [--root P] [--before-dir D] [--after-dir D] [--script-timeout D]
                        [--max-script-size-bytes N]
                                                  change one of them in place. An explicitly empty value clears:
                                                  --before-dir "" disables that stage and --script-timeout 0 returns hooks
                                                  to the built-in bound, so a flag passed as its zero value is a request
                                                  rather than an omission. Beside a serving engine it is handed to that
                                                  process (PATCH /settings/workflow) when RETND_API_URL names it, and
                                                  refused with the file untouched when it does not. --json prints the
                                                  object the API returns for the same call
  settings workflow env list [--json]            the deployment-wide hook environment: each variable's name, and either
                                                  its literal value or WHERE its value is read from. A secret is a location
                                                  on this surface and never a value, in both directions: no flag takes
                                                  material and no output here can print any
  settings workflow env set NAME (--value V | --secret-file P | --secret-env VAR | --secret-command W ...)
                                                  configure one variable, naming exactly one source. Repeat
                                                  --secret-command once per argv word, so no quoting mistake can re-split a
                                                  program's path. --value "" configures an empty string, which is a
                                                  different thing from configuring nothing
                                                  Handed to a serving engine, like the patch above
  settings workflow env unset NAME               remove one variable. A name that is not configured is refused rather than
                                                  quietly succeeding: an "unset the credential" that did nothing is
                                                  indistinguishable from one that worked
                                                  Handed to a serving engine, like the patch above
  settings assurance [--json]                    report the resolved deployment-wide assurance policy: how often a copy is
                                                  re-read, what a scrub reads, whether a recovery test is required, and the
                                                  host-scoped settings that are global only, each with the reason a backup
                                                  set cannot override it. A config that sets none reports the defaults
  settings assurance patch [--scrub-cadence D] [--scrub-strategy S] [--recovery-test-mode M]
                        [--recovery-test-cadence D] [--recovery-test-validator ID]
                        [--staging-root P] [--staging-ttl D] [--max-concurrent-full-reads N]
                        [--max-per-destination N] [--json]
                                                  change the deployment's policy in place. An explicitly zero or empty value
                                                  clears that setting back to its default. Every set that inherits the
                                                  changed value decides under it on its next read. This write has no route
                                                  to a serving engine in this build, so it is refused beside one with the
                                                  file untouched
  settings assurance due [--backup-set ID] [--due | --overdue] [--json]
                                                  the scrub schedule, most overdue first: each copy's last-assured time, when
                                                  it is next due under its set's resolved cadence, and whether that has
                                                  passed. A copy nobody has looked at is overdue, never fine
  backup-set assurance <source/backup-set> [--json]
                                                  one backup set's resolved assurance policy and whether it is the set's own
  backup-set assurance <source/backup-set> [--scrub-cadence D] [--scrub-strategy S]
                          [--recovery-test-mode M] [--recovery-test-cadence D]
                          [--recovery-test-validator ID] | --reset
                                                  patch that set's own block, or --reset it to follow the deployment's policy
                                                  again. The host-scoped settings are refused here by name: they are global
                                                  only. Refused beside a serving engine, with the file untouched
  backup-set workflow <source/backup-set> [--json]
                                                  one backup set's hook configuration, resolved against the deployment's:
                                                  the stages it will actually run in execution order, which timeout wins
                                                  and whether this set pinned it, the connection its remote hooks run over,
                                                  and the name of every variable a hook of this set will be handed once
                                                  both layers are merged
  backup-set workflow patch <source/backup-set> [--before-dir D] [--after-dir D]
                          [--script-timeout D] [--exec-connection REF]
                                                  change that set's own block. An explicitly empty value clears, the same
                                                  rule the deployment-wide patch follows, and clearing the last field
                                                  removes the block rather than leaving one that configures nothing.
                                                  Beside a serving engine it is handed to that process (PATCH
                                                  .../workflow) when RETND_API_URL names it, and refused with the file
                                                  untouched when it does not. --json prints the object the API returns
  backup-set workflow env <source/backup-set> list|set NAME ...|unset NAME
                                                  the same three env verbs against one set's own layer, which wins over the
                                                  deployment's for a variable both configure. validate workflow is what
                                                  reports that a variable is configured in both
                                                  set and unset are handed to a serving engine like the patch above
  validate workflow <source/backup-set> [--json] everything this product can establish about a backup set's hooks without
                                                  running one: the resolved stage directories, the script names and their
                                                  ordering, each script's sha256 and the timeout it gets, the permissions
                                                  of every ancestor of a stage directory, the host runner, a bash -n parse
                                                  of every local and remote script, the execution connection and whether it
                                                  can really run a command, name conflicts and reserved names in the
                                                  environment, and whether each secret reference names something that
                                                  exists. Every check reports, skipped ones included, because a check
                                                  nobody ran is not a check that passed. Exits 1 when the hooks are
                                                  unsound, and says so in a line of its own when the SET is sound and its
                                                  hooks are not. No hook body is ever executed
  fetch --skip-workflow-scripts                  refused, deliberately, rather than ignored. This pass DOES run the set's
                                                  hooks -- it goes through the same reconciled five-stage lifecycle the
                                                  serving engine uses -- and a bypass is an administrator action recorded
                                                  against a session the engine minted, which this command cannot
                                                  establish. A bypass recorded against nobody is the audit trail #813
                                                  exists to prevent, so it is refused with the alternative named rather
                                                  than accepted and ignored
  version                                        report version information
  version --engine [--json]
                                                  also ask the engine serving this deployment for its own version, its
                                                  configuration revision, its deployment id, whether it is ready and what
                                                  paths it adopted. --json prints the API's VersionResponse alone. Needs a
                                                  route to the engine and refuses without one (#1152)
  validators [--json]                            list the application validators this binary carries, the ids
                                                  backup-set create --validator-id accepts. --json prints the API's
                                                  ListValidatorsResponse. Needs no configuration (#1152)
  system capabilities [--json]                   what the platform the engine runs on can do (native sign-in, a
                                                  storage picker, ...), as the engine declares it. Needs a route to the
                                                  engine and refuses without one (#1152)
                                                  Also reports whether the engine's destructive gate is open or shut and
                                                  why (destructive_operations). The gate is the engine's: this CLI has
                                                  none, so the verbs the web UI sends people to run when it is shut
                                                  (run, fetch, restore, snapshot restore|verify|hold|unhold, retention
                                                  apply) work here. --json prints the same body as
                                                  GET /api/v1/system/capabilities (#1165)
  system first-run [--json]                      which guided setup steps the engine counts as done, pending or
                                                  skipped (the administrator, the mail server, the first backup set),
                                                  the next one, and any problem with the configuration directory. status
                                                  prints the same steps, attributed to the engine, on a fresh install
                                                  when a route to it is set. Needs that route (#1152)

every command except version accepts --config (default /etc/retnd/config/config.yaml;
a directory resolves to config.yaml inside it, which is what packaging mounts)

a configuration write goes one of three ways, and says which on a "mode:" line. With nothing
serving this deployment it is written here, and an engine started afterwards reads it when it
starts. With something serving and a route to it, backup-set create, patch and remove,
settings patch, settings workflow patch and env set and unset, backup-set workflow patch and
env set and unset, backup-set retention (setting or clearing a policy) and medium
import-credentials, add, edit and remove hand the change to that
process over its API, so it takes effect at once and there is nothing to restart. medium
preflight --candidate goes the same way, because it is the check medium add runs before it
writes and has to happen where the write will. With something serving and no route, the
write is refused and nothing is written: there is still no config watcher and no SIGHUP reload in this build, so a
change left in the file is one the serving process would never read. One write has no route
at all and is refused beside a serving engine either way: the first config.yaml a create
writes on an instance that has none yet (#535, #543, #1148)

the route is three environment variables. RETND_API_URL is the engine's address,
either its own listener from inside its container (http://127.0.0.1:8080) or the published
Web UI port from a shell on the host; RETND_API_USERNAME and
RETND_API_PASSWORD are the administrator credentials the Web UI takes, held in
memory for the one invocation and written nowhere. Not flags, because a password on a command
line is in every process listing on the host. A routed create or patch reports every field it
wrote, stale_after included: the API carries stale_after_seconds now, so both routes print the
same set (#555). Only an engine older than that field serves none, and against one of those a
routed create prints stale_after as not reported rather than inventing a value

status, sources, artifacts and retention read rather than write, so a missing route never
refuses them; they announce which world the answer is about instead. "mode: engine-attached"
is a serving process that holds the same configuration and was asked the same question,
"mode: direct" is nothing serving, and "mode: unconfirmed" is an answer taken from
config.yaml that could not be checked against the process serving this deployment and can
disagree with it. A read refuses in three cases, and then nothing at all is printed: that
process holds a DIFFERENT configuration, the engine at the address it was given turns out to
be serving a different deployment, or it answers this command's own question differently
(#544, #555)

everything else is ordinary beside a running engine and announces no mode at all: run, fetch,
check, validate and the rest, settings and a backup-set retention that only reports included.
The one command a running engine refuses is daemon, for the reason its entry above gives

exit codes, so a script can branch on what happened rather than on the sentence it happened
to print:

  0   the command did what it was asked
  1   an ordinary failure: a configuration that will not load, a state database that will
      not open, a cycle that backed nothing up, a set or an artifact that is not there, a
      status short of HEALTHY, a route that was named and did not answer, and a route that
      answered for a DIFFERENT deployment than the one this command was typed at
      a --backup-set naming a set name two sources share is an ordinary failure too, and
      not a 2: it takes the loaded configuration to know it is ambiguous at all, and the
      same command line is exactly right on a deployment where one source has that name
      (#569)
  2   nothing ran: the command line was wrong (an unknown command, an unknown flag, a
      missing or surplus argument), or it asked for help rather than for work. A -h on a
      subcommand is here too and it is not a mistake: 2 says no command was carried out,
      and the reason is on stderr either way
      a value that is not shaped like a backup set id is here too, everywhere one is taken:
      retention's operand, backup-set create/patch/remove/retention, unconfigured clear, and
      --backup-set on artifacts and fetch all refuse it before they open anything. That is
      where the line between this row and the one above it falls: source/name with a file
      name still on the end is wrong wherever it is typed, and a name this configuration
      does not have is only wrong here (#569)
  3   another process is serving this deployment, so nothing was done: a configuration write
      refused because it would never reach that process, or a daemon refused rather than
      started beside one. `+cliecho.WebBinary+` serve answers the same way for the same
      reason, which matters because that is the binary this deployment's compose file
      runs, so a supervisor reads one code from either (#557). Read the sentence beside
      it before retrying in a loop. A
      supervisor still rolling the outgoing process gets this until it has let go, and
      waiting is the right answer; an engine this host was never given a route to goes on
      refusing for as long as it serves, and the answer there is to set the route or stop
      that process. Everything that only looks like it is 1, including a probe that could
      not be performed at all, a route that was named and did not answer, and a route that
      answered for a different deployment, because none of those gets better by waiting
      (#551, #555)
  10-36  recover only (EPIC Q), one code per reason so a script can branch without reading the
      sentence: 10 chargeable and not acknowledged, 11 no retained copy, 12 no such copy,
      13 nothing readable right now, 14 the copy on this host's disk failed its checksum,
      15 a remote copy failed its checksum, 16 no recorded SHA-256 to verify against,
      17 staging disk full, 18 below the staging free-space reserve, 19 a kopia set (use
      snapshot restore), 20 target not allowed, 21 target already exists, 22-26 grant
      refusals (unknown, used, expired, session ended, too many), 27 recovery unavailable,
      28 busy, 29 interrupted, 30 any other failure, 31 an archive restore was started and
      nothing was recovered yet, 32 too many full reads running on this host, 33 too many
      on that one destination, 36 too many downloads in progress (a budget refusal: try
      again when one finishes). In every refusal no byte was delivered anywhere
`)
}
