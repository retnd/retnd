package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// #1609: #1546 made `daemon` and `retnd-web serve` refuse a deployment still
// mounted at its pre-0.5.0 path, and #1558 did the same for check, status and
// run. Every other verb that reads or writes the local configuration or
// journal still went to the new, empty path: `backup-set create` wrote a fresh
// config.yaml there, `ssh-key` and `backup-set test-connection --candidate`
// built a first-run surface on it, and the reads answered "no backup set
// configured yet" or "no such file" with no word about the catalogue at the
// old path.
//
// The table below is every verb that reaches the local configuration or
// journal, one row per way in (the shared helper or the direct read it goes
// through). Each row drives the real command in this process (runCLI), on
// #1546's deployment fixture, the way #1558's tests do.
//
// Falsify: take the check out of one of the shared helpers in setup.go,
// dataplane.go or remote.go; every row that comes through it goes red.

type everyCommand1609 struct {
	name string
	// argv is the invocation, with cfg as its --config. d is the fixture,
	// for a row that needs a path of its own beside it.
	argv func(t *testing.T, d deployment1546, cfg string) []string
	// follows is true for a verb that keeps running against a working
	// deployment (a live feed), so the controls that hand it one leave it
	// out.
	follows bool
}

const (
	set1609      = "production/postgres-primary"
	artifact1609 = "production/postgres-primary/backup.dump"
)

func fixed1609(args ...string) func(*testing.T, deployment1546, string) []string {
	return func(_ *testing.T, _ deployment1546, cfg string) []string {
		return append(append([]string{}, args...), "--config", cfg)
	}
}

var everyCommandRows1609 = []everyCommand1609{
	// openService
	{name: "status", argv: fixed1609("status")},
	{name: "artifacts", argv: fixed1609("artifacts")},
	{name: "retention", argv: fixed1609("retention")},
	{name: "reconcile", argv: fixed1609("reconcile")},
	{name: "validate", argv: fixed1609("validate", artifact1609)},
	{name: "scrub", argv: fixed1609("scrub", set1609)},
	{name: "retry", argv: fixed1609("retry", artifact1609)},
	{name: "retire", argv: fixed1609("retire", artifact1609)},
	{name: "quarantine revalidate", argv: fixed1609("quarantine", "revalidate", artifact1609)},
	{name: "unconfigured", argv: fixed1609("unconfigured")},
	{name: "catalog rebuild", argv: fixed1609("catalog", "rebuild", "--dry-run")},
	{name: "fetch", argv: fixed1609("fetch", "--source", "production", "--backup-set", "postgres-primary", "--dry-run")},

	// openBackupDataPlane
	{name: "run", argv: fixed1609("run")},

	// openBackupService and openBackupServiceLoggingTo
	{name: "sources --json", argv: fixed1609("sources", "--json")},
	{name: "artifacts --json", argv: fixed1609("artifacts", "--json")},
	{name: "backup-set show", argv: fixed1609("backup-set", "show", set1609)},
	{name: "retention apply", argv: fixed1609("retention", "apply", set1609, "--acknowledge")},
	{name: "recovery-test", argv: fixed1609("recovery-test", set1609)},
	{name: "storage", argv: fixed1609("storage")},
	{name: "medium list", argv: fixed1609("medium", "list")},
	{name: "quarantine list", argv: fixed1609("quarantine", "list")},
	{name: "recover", argv: fixed1609("recover", artifact1609)},
	{name: "restore", argv: fixed1609("restore", artifact1609, "--medium", "offsite", "--acknowledge")},
	{name: "snapshot list", argv: fixed1609("snapshot", "list", set1609)},
	{name: "workflow run list", argv: fixed1609("workflow", "run", "list")},
	{name: "assurance list", argv: fixed1609("assurance", "list")},
	{name: "encryption policy", argv: fixed1609("encryption", "policy")},
	{name: "repository health", argv: fixed1609("repository", "health")},

	// openConfigWriteRoute and openConfigWriteRouteTo
	{name: "settings", argv: fixed1609("settings")},
	{name: "backup-set patch", argv: fixed1609("backup-set", "patch", set1609, "--port", "2223")},
	{name: "backup-set remove", argv: fixed1609("backup-set", "remove", set1609)},
	{name: "backup-set enabled", argv: fixed1609("backup-set", "enabled", set1609, "off")},
	{name: "backup-set retention", argv: fixed1609("backup-set", "retention", set1609)},
	{name: "backup-set test-connection", argv: fixed1609("backup-set", "test-connection", set1609)},
	{name: "medium default", argv: fixed1609("medium", "default", "offsite")},
	{name: "assurance validators list", argv: fixed1609("assurance", "validators", "list")},
	{name: "encryption key list", argv: fixed1609("encryption", "key", "list")},
	{name: "agent audit", argv: fixed1609("agent", "audit")},
	{name: "agent confirmations list", argv: fixed1609("agent", "confirmations", "list")},

	// engineOnlyRemote: with no configuration at the new path these verbs
	// go looking for an engine to ask instead.
	{name: "agent token list", argv: fixed1609("agent", "token", "list")},
	{name: "activity --follow", argv: fixed1609("activity", "--follow"), follows: true},
	{name: "operation list", argv: fixed1609("operation", "list")},
	{name: "assurance cancel", argv: fixed1609("assurance", "cancel", "op-1609")},
	{name: "auth whoami", argv: fixed1609("auth", "whoami")},
	// The anonymous auth verbs ask hasLocalConfig themselves, and with
	// nothing at the new path they treat this host as having no deployment.
	{name: "auth forgot-password", argv: fixed1609("auth", "forgot-password", "--username", "admin")},
	{name: "backup-set edit-hold", argv: fixed1609("backup-set", "edit-hold", set1609)},

	// The reads that load the configuration or open the journal themselves.
	{name: "sources", argv: fixed1609("sources")},
	{name: "activity", argv: fixed1609("activity")},
	{name: "assurance show", argv: fixed1609("assurance", "show", "op-1609")},

	// The fresh-install branches: no config.yaml at the new path is a first
	// run to these, and they build a first-run surface on it.
	{name: "backup-set create", argv: func(t *testing.T, d deployment1546, cfg string) []string {
		return createArgs(cfg, writeTestPrivateKey(t), "api/postgres",
			"--state-database", filepath.Join(d.root, "state-created", "state.db"))
	}},
	{name: "backup-set test-connection --candidate", argv: fixed1609("backup-set", "test-connection", "--candidate",
		"--host", "127.0.0.1", "--port", "1", "--user", "backupuser", "--ssh-key-id", "key-1609",
		"--known-hosts-line", aKnownHostsLine, "--remote-path", "/srv/backups")},
	{name: "ssh-key list", argv: fixed1609("ssh-key", "list")},
	{name: "ssh-key discover", argv: fixed1609("ssh-key", "discover")},
	{name: "ssh-key import", argv: func(t *testing.T, d deployment1546, cfg string) []string {
		return []string{"ssh-key", "import", "--file", writeTestPrivateKey(t), "--config", cfg}
	}},

	// #1558's, kept here so the table is the whole list.
	{name: "check", argv: fixed1609("check")},
}

// exemptVerbs1609 are the verbs with no row above, and why. Every other verb
// in main.go's commands table needs one (TestCLI_1609_EveryVerbIsInTheTable).
var exemptVerbs1609 = map[string]string{
	"daemon":          "refuses through refusedAtPreRenamePath; a long-running child process, so TestDaemon_1546_* drive it",
	"version":         "reads no configuration and no journal; must never refuse",
	"validators":      "prints the validators compiled into this build; reads nothing local",
	"ssh":             "host-key-probe asks a host on the network and reads nothing local",
	"system":          "asks the engine at RETND_API_URL (dialEngine) and reads nothing local",
	"workflow-runner": "a sidecar that only reads an optional script-size bound and tolerates no configuration by design (#176)",
}

// fixture1609 is #1546's deployment, with STATE_DATABASE pointed inside it so
// nothing a command does with the default journal reaches the host's
// /data/state. The default has no retnd component, so it is never part of
// the check either.
func fixture1609(t *testing.T) deployment1546 {
	t.Helper()
	d := deployment1546{root: t.TempDir()}
	t.Setenv("STATE_DATABASE", filepath.Join(d.root, "state-default", "state.db"))
	return d
}

func assertNothingWrittenAtTheCurrentPath1609(t *testing.T, name string, d deployment1546) {
	t.Helper()
	if _, err := os.Stat(d.currentConfig()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("retnd %s left something at %s (stat: %v); a refusal reads and writes nothing", name, d.currentConfig(), err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(d.currentConfig()), "ssh_keys")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("retnd %s created a key store beside %s", name, d.currentConfig())
	}
}

func TestCLI_1609_OnlyTheFormerPathHoldsTheCatalogue_EveryCommandRefuses(t *testing.T) {
	for _, row := range everyCommandRows1609 {
		t.Run(row.name, func(t *testing.T) {
			d := fixture1609(t)
			d.populate(t, d.formerConfig(), "old")

			code, stdout, stderr := runCLI(t, row.argv(t, d, d.currentConfig())...)
			assertCLIRefused1558(t, row.name, code, stdout, stderr,
				"refusing to start",
				d.formerConfig(),
				d.currentConfig(),
				"--config "+d.formerConfig(),
			)
			assertNothingWrittenAtTheCurrentPath1609(t, row.name, d)
		})
	}
}

func TestCLI_1609_BothPathsPopulated_EveryCommandRefusesNamingTheConflict(t *testing.T) {
	for _, row := range everyCommandRows1609 {
		t.Run(row.name, func(t *testing.T) {
			d := fixture1609(t)
			d.populate(t, d.currentConfig(), "new")
			d.populate(t, d.formerConfig(), "old")
			before := readFile(t, d.currentConfig())

			code, stdout, stderr := runCLI(t, row.argv(t, d, d.currentConfig())...)
			assertCLIRefused1558(t, row.name, code, stdout, stderr,
				"refusing to start",
				"two different",
				d.currentConfig(),
				d.formerConfig(),
			)
			if after := readFile(t, d.currentConfig()); after != before {
				t.Errorf("retnd %s changed %s while refusing", row.name, d.currentConfig())
			}
		})
	}
}

// The controls. A deployment at the current path, the same deployment
// reached with --config <old path> (the refusal's own advice), and a fresh
// install with nothing anywhere are never refused. Each command keeps its
// ordinary answer, whatever exit that is for the fixture.
func TestCLI_1609_OnlyTheCurrentPathPopulated_IsNotRefused(t *testing.T) {
	for _, row := range everyCommandRows1609 {
		if row.follows {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			d := fixture1609(t)
			d.populate(t, d.currentConfig(), "new")

			code, _, stderr := runCLI(t, row.argv(t, d, d.currentConfig())...)
			if strings.Contains(stderr, "refusing to start") {
				t.Errorf("retnd %s refused a deployment at the current path (exit %d)\n%s", row.name, code, stderr)
			}
		})
	}
}

func TestCLI_1609_ConfigFlagAtTheFormerPath_IsNotRefused(t *testing.T) {
	for _, row := range everyCommandRows1609 {
		if row.follows {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			d := fixture1609(t)
			d.populate(t, d.formerConfig(), "old")

			code, _, stderr := runCLI(t, row.argv(t, d, d.formerConfig())...)
			if strings.Contains(stderr, "refusing to start") {
				t.Errorf("retnd %s --config %s refused; that is the refusal's own advice (exit %d)\n%s", row.name, d.formerConfig(), code, stderr)
			}
			if _, err := os.Stat(d.currentConfig()); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("retnd %s --config %s wrote at the current path %s", row.name, d.formerConfig(), d.currentConfig())
			}
		})
	}
}

func TestCLI_1609_NeitherPathPopulated_IsAnOrdinaryFreshInstall(t *testing.T) {
	for _, row := range everyCommandRows1609 {
		if row.follows {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			d := fixture1609(t)

			code, stdout, stderr := runCLI(t, row.argv(t, d, d.currentConfig())...)
			if strings.Contains(stderr, "refusing to start") {
				t.Errorf("retnd %s refused a fresh install (exit %d)\n%s", row.name, code, stderr)
			}
			switch row.name {
			case "backup-set create":
				if code != 0 {
					t.Errorf("backup-set create on a fresh install = exit %d, want the first configuration written\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
				}
				if _, err := os.Stat(d.currentConfig()); err != nil {
					t.Errorf("backup-set create on a fresh install wrote nothing at %s: %v", d.currentConfig(), err)
				}
			case "ssh-key list", "ssh-key discover":
				// The two key-store reads on a host with no configuration
				// and nothing serving go to this host's own first-run
				// surface. They used to build it with no state database,
				// which NewFirstRun refuses, so a fresh install got
				// "service: first run needs a state database path".
				if code != 0 {
					t.Errorf("retnd %s on a fresh install = exit %d, want this host's empty first-run store\nstdout:\n%s\nstderr:\n%s", row.name, code, stdout, stderr)
				}
			}
		})
	}
}

// --help, help and version never refuse, on any verb, even with --config
// naming a deployment the check would refuse. --help is parsed before
// anything else runs, so it must come back as a usage answer.
func TestCLI_1609_HelpAndVersionNeverRefuse(t *testing.T) {
	for _, row := range everyCommandRows1609 {
		t.Run(row.name+" --help", func(t *testing.T) {
			d := fixture1609(t)
			d.populate(t, d.formerConfig(), "old")

			argv := append(row.argv(t, d, d.currentConfig()), "--help")
			code, _, stderr := runCLI(t, argv...)
			if strings.Contains(stderr, "refusing to start") {
				t.Errorf("retnd %s refused (exit %d)\n%s", strings.Join(argv, " "), code, stderr)
			}
			assertNothingWrittenAtTheCurrentPath1609(t, row.name+" --help", d)
		})
	}
	for _, argv := range [][]string{{"version"}, {"help"}, {"--help"}, {"-h"}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			fixture1609(t)
			code, _, stderr := runCLI(t, argv...)
			if strings.Contains(stderr, "refusing to start") {
				t.Errorf("retnd %s refused (exit %d)\n%s", strings.Join(argv, " "), code, stderr)
			}
			if argv[0] == "version" && code != 0 {
				t.Errorf("retnd version = exit %d, want 0\n%s", code, stderr)
			}
		})
	}
}

// The next verb added to main.go has to say which way it reaches the
// deployment, or why it reaches none; otherwise it would quietly be the
// command this issue was about.
func TestCLI_1609_EveryVerbIsInTheTable(t *testing.T) {
	covered := map[string]bool{}
	for _, row := range everyCommandRows1609 {
		covered[strings.Fields(row.name)[0]] = true
	}
	var missing []string
	for verb := range commands {
		if !covered[verb] && exemptVerbs1609[verb] == "" {
			missing = append(missing, verb)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("verbs with no row in everyCommandRows1609 and no reason in exemptVerbs1609: %v", missing)
	}
	for verb := range exemptVerbs1609 {
		if _, ok := commands[verb]; !ok {
			t.Errorf("exemptVerbs1609 names %q, which main.go no longer dispatches", verb)
		}
	}
}
