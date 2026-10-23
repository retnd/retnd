package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/service"
)

// `retnd ssh-key ...` and `retnd ssh host-key-probe` (issue #1153): the key
// store and the host-key probe from a terminal.
//
// `--ssh-key-id` has taken an id since #572 that nothing on a terminal would
// print, and the store could not be managed from a shell at all. These are
// the calls the setup wizard already makes.
//
// # Where they act
//
// Beside a serving engine they go to it, because the key store belongs to
// the process that will use the keys and a probe is only worth running from
// the machine the backups travel from. With nothing serving they act on this
// deployment's own files through the same service layer. On a host with no
// config.yaml yet they go to an engine still serving its first-run setup
// flow when this command has an address for it (#1254), and otherwise the
// reads and the probe work against this host's first-run surface and an
// import, with no configuration to put the key next to, says so.
//
// # What never appears
//
// Key bytes and passphrases. A key arrives as a FILE (`--file`), a
// passphrase as the first line of stdin (`--passphrase-stdin`), and neither
// is accepted as a flag, so neither is in the process list or the shell
// history. Nothing here prints either, and the service's refusals describe
// the shape of a problem without quoting the key. The output of an import is
// the algorithm, the fingerprint and the id: the things you compare, and the
// id `backup-set create --ssh-key-id` takes.

func cmdSSHKey(args []string) int {
	if len(args) == 0 {
		return usageError("ssh-key: expected `ssh-key list`, `ssh-key import` or `ssh-key discover`")
	}
	switch args[0] {
	case "list":
		return sshKeyList(args[1:])
	case "import":
		return sshKeyImport(args[1:])
	case "discover":
		return sshKeyDiscover(args[1:])
	default:
		return usageError("ssh-key: unknown subcommand %q (list, import and discover exist)", args[0])
	}
}

func cmdSSH(args []string) int {
	if len(args) == 0 {
		return usageError("ssh: expected `ssh host-key-probe HOST [--port N]`")
	}
	switch args[0] {
	case "host-key-probe":
		return sshHostKeyProbe(args[1:])
	default:
		return usageError("ssh: unknown subcommand %q (host-key-probe exists)", args[0])
	}
}

// openSSHKeyRoute settles where a key store verb acts.
//
// A host with no config.yaml is a first run, and where the key goes then
// depends on what is serving the journal --state-database would name
// ($STATE_DATABASE or the packaged path, since these verbs take no flag
// for it). An engine still on its first-run setup flow, with an address to
// reach it, is where the key store that matters lives, so every verb goes
// there (#1254): on a headless install that is the only way the key step
// of a first run can happen from a terminal. Otherwise the reads and the
// probe go to this host's own first-run surface, which holds a store and a
// scanner of its own, and an import is refused with the way forward.
// Everything else is the door every configuration write goes through,
// which is also what routes it to a serving engine.
func openSSHKeyRoute(ctx context.Context, cfgPath string, importing bool) (sshKeyRoute, func(), error) {
	configFile := config.ResolvePath(cfgPath)
	if _, err := os.Stat(configFile); errors.Is(err, os.ErrNotExist) {
		route, firstRunServing, err := sshKeyFirstRunRoute(ctx, "so this command uses its key store at %s")
		if err != nil {
			return nil, func() {}, err
		}
		if route != nil {
			return route, func() {}, nil
		}
		if importing {
			if firstRunServing {
				return nil, func() {}, fmt.Errorf("there is no configuration at %s yet, and a process is serving this deployment. If it is still on its first-run setup flow, the key store is its own and this command has no route to it: set $%s (with $%s and $%s) to the address it serves and run this again, or create the first backup set with `backup-set create --ssh-key-file ...`, which imports the key as part of it",
					configFile, routeEnvName(apiURLRoute), routeEnvName(apiUsernameRoute), routeEnvName(apiPasswordRoute))
			}
			return nil, func() {}, fmt.Errorf("there is no configuration at %s yet, so there is no key store to put a key in. Create the first backup set with `backup-set create --ssh-key-file ...`, which imports the key as part of it, or add one in the web interface", configFile)
		}
		abs, absErr := filepath.Abs(configFile)
		if absErr != nil {
			return nil, func() {}, absErr
		}
		firstRun, err := service.NewFirstRun(service.FirstRunDefaults{ConfigPath: abs})
		if err != nil {
			return nil, func() {}, err
		}
		return firstRun, func() {}, nil
	}
	route, cleanup, err := openConfigWriteRoute(ctx, cfgPath)
	if err != nil {
		return nil, func() {}, err
	}
	return route, cleanup, nil
}

// sshKeyFirstRunRoute is the first-run engine's key store, when something
// serving this deployment is still on its first run and this command has
// an address for it, and reports whether anything is serving at all so a
// refusal can say which way forward exists.
//
// It is attachFirstRun, the route a first configuration takes, with no
// claim around it: these verbs write no configuration, so there is nothing
// to hold one for. A configured engine beside a host with no config.yaml
// (a mistyped --config) gets no route, as it does for a create, and these
// verbs carry on against this host's own first-run surface exactly as
// before. A route that was named and is refused is reported, never
// downgraded, and it is said on stderr, so a --json document on stdout
// stays the only thing there.
//
// uses is the end of the mode line, with a %s for the address: what this
// command does with the engine it found, in the caller's words, since the
// candidate connection check (#1253) takes the same route for a different
// reason than a key verb.
func sshKeyFirstRunRoute(ctx context.Context, uses string) (*firstRunEngineRoute, bool, error) {
	stateDatabase := service.StateDatabaseDefault()
	engine, err := detectRunningEngineForJournal(stateDatabase)
	if err != nil {
		return nil, false, cannotTellError(err)
	}
	if engine == nil {
		return nil, false, nil
	}
	attachCtx, cancel := context.WithTimeout(ctx, firstRunAttachTimeout)
	defer cancel()
	route, address, err := attachFirstRun(attachCtx, engine)
	if err != nil {
		return nil, true, err
	}
	if route != nil {
		fmt.Fprintf(os.Stderr, "%s%s. A process is serving this deployment's first-run setup flow (state database %s), "+uses+".\n",
			modeLinePrefix, engineAttachedMode, engine.StateDatabase, address)
	}
	return route, true, nil
}

// readKeyPassphrase reads the first line of stdin when the flag says to,
// and is a no-op otherwise. An empty line is refused by name: a passphrase
// that is not there is the operator's mistake to hear about, not an empty
// string to try.
func readKeyPassphrase(wanted bool, verb string) (string, error) {
	if !wanted {
		return "", nil
	}
	lines, err := stdinLines(os.Stdin, 1)
	if err != nil {
		return "", fmt.Errorf("%s: reading the key passphrase from stdin: %w", verb, err)
	}
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("%s: stdin carried no key passphrase; pipe it in as the first line", verb)
	}
	return lines[0], nil
}

// withPassphraseHint turns a refusal that is about a missing passphrase
// into one that says how to supply it. It only adds a sentence: the original
// text is the service's and is kept whole.
func withPassphraseHint(err error, gave bool, flagName string) error {
	if err == nil || gave || !strings.Contains(strings.ToLower(err.Error()), "passphrase") {
		return err
	}
	return fmt.Errorf("%w. If this key is passphrase-protected, pass %s and pipe the passphrase in on stdin", err, flagName)
}

func sshKeyList(args []string) int {
	fs, cfgPath := newFlagSet("ssh-key list")
	asJSON := fs.Bool("json", false, "print the key store as JSON, the same object GET /api/v1/ssh-keys serves")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("ssh-key list: takes no operands, got %q", operands[0])
	}

	ctx := context.Background()
	route, cleanup, err := openSSHKeyRoute(ctx, *cfgPath, false)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	keys, err := route.ListSSHKeys(ctx)
	if err != nil && !errors.Is(err, service.ErrConfigNotFileBacked) {
		return fail(fmt.Errorf("ssh-key list: %w", err))
	}
	wire := service.SSHKeysToWire(keys)
	if *asJSON {
		return printVerbJSON("ssh-key list", wire)
	}
	printSSHKeys(wire)
	return exitOK
}

func printSSHKeys(wire apicontract.ListSSHKeysResponse) {
	if len(wire.Keys) == 0 {
		fmt.Println("no keys in this deployment's store. `ssh-key import --file F` adds one; `ssh-key discover` shows the keys this machine could offer")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tALGORITHM\tFINGERPRINT\tIMPORTED\tUSED BY")
	for _, k := range wire.Keys {
		used := "nothing"
		if len(k.UsedBy) > 0 {
			used = strings.Join(k.UsedBy, ", ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k.ID, orDash(k.Algorithm), orDash(k.Fingerprint), orDash(k.ImportedAt), used)
	}
	_ = w.Flush()
	for _, k := range wire.Keys {
		if k.PassphraseProtected {
			fmt.Printf("%s is passphrase-protected, and its public half cannot be read without the passphrase\n", k.ID)
		}
		if k.Problem != "" {
			fmt.Printf("%s: %s\n", k.ID, k.Problem)
		}
	}
}

func sshKeyDiscover(args []string) int {
	fs, cfgPath := newFlagSet("ssh-key discover")
	asJSON := fs.Bool("json", false, "print the scan as JSON, the same object GET /api/v1/ssh/key-candidates serves")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("ssh-key discover: takes no operands, got %q", operands[0])
	}

	ctx := context.Background()
	route, cleanup, err := openSSHKeyRoute(ctx, *cfgPath, false)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	found, err := route.DiscoverSSHKeyCandidates(ctx)
	if err != nil {
		return fail(fmt.Errorf("ssh-key discover: %w", err))
	}
	wire := service.SSHDiscoveryToWire(found)
	if *asJSON {
		return printVerbJSON("ssh-key discover", wire)
	}
	printSSHDiscovery(wire)
	return exitOK
}

// printSSHDiscovery lists every place the scan looked before it lists what
// it found, because an empty candidate list has two readings, "you have no
// keys" and "I could not look where your keys are", and the locations are
// what tell them apart.
func printSSHDiscovery(wire apicontract.ListSSHKeyCandidatesResponse) {
	fmt.Println("looked in:")
	for _, l := range wire.Locations {
		switch {
		case l.Problem != "":
			fmt.Printf("  %s (%s): %s\n", l.Path, l.Kind, l.Problem)
		default:
			fmt.Printf("  %s (%s): %d found\n", l.Path, l.Kind, l.Found)
		}
	}
	if len(wire.Candidates) == 0 {
		fmt.Println("no keys found")
		return
	}
	fmt.Println("keys:")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tPATH\tALGORITHM\tFINGERPRINT\tSTATE")
	for _, c := range wire.Candidates {
		state := "importable with `ssh-key import --candidate " + c.ID + "`"
		switch {
		case c.InStore:
			state = "already in the store as " + c.InStoreID
		case !c.Selectable:
			state = "not importable: " + orDash(c.Reason)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", c.ID, c.Path, orDash(c.Algorithm), orDash(c.Fingerprint), state)
	}
	_ = w.Flush()
}

func sshKeyImport(args []string) int {
	fs, cfgPath := newFlagSet("ssh-key import")
	file := fs.String("file", "", "path to the SSH PRIVATE KEY to import. Read once and copied into the key store; the original is left alone")
	candidate := fs.String("candidate", "", "the id `ssh-key discover` printed for a key this machine already holds")
	passphraseStdin := fs.Bool("passphrase-stdin", false, "the key is passphrase-protected, and its passphrase is the first line of stdin")
	asJSON := fs.Bool("json", false, "print the reference as JSON, the same object the API returns")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("ssh-key import: takes no operands, got %q", operands[0])
	}
	switch {
	case *file == "" && *candidate == "":
		return usageError("ssh-key import: name the key with --file F (a private key file) or --candidate ID (one `ssh-key discover` listed)")
	case *file != "" && *candidate != "":
		return usageError("ssh-key import: --file and --candidate are alternatives; pass one")
	case *candidate != "" && *passphraseStdin:
		return usageError("ssh-key import: --passphrase-stdin belongs to --file. A candidate is read on the machine that holds it, and one that needs a passphrase is not importable")
	}

	var raw []byte
	if *file != "" {
		raw, err = os.ReadFile(*file)
		if err != nil {
			return fail(fmt.Errorf("ssh-key import: reading the key at %s: %w", *file, err))
		}
	}
	passphrase, err := readKeyPassphrase(*passphraseStdin, "ssh-key import")
	if err != nil {
		return fail(err)
	}

	ctx := context.Background()
	route, cleanup, err := openSSHKeyRoute(ctx, *cfgPath, true)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	var ref service.SSHKeyRef
	if *candidate != "" {
		ref, err = route.ImportSSHKeyCandidate(ctx, *candidate)
		if err != nil {
			if errors.Is(err, service.ErrSSHKeyCandidateNotFound) || strings.Contains(err.Error(), "SSH_KEY_CANDIDATE_NOT_FOUND") {
				return fail(fmt.Errorf("ssh-key import: no such key candidate; run `ssh-key discover` and use an id from the current listing"))
			}
			return fail(fmt.Errorf("ssh-key import: %w", err))
		}
	} else {
		ref, err = route.ImportSSHKey(ctx, raw, passphrase)
		for i := range raw {
			raw[i] = 0
		}
		if err != nil {
			return fail(fmt.Errorf("ssh-key import: %w", withPassphraseHint(err, passphrase != "", "--passphrase-stdin")))
		}
	}
	if *asJSON {
		return printVerbJSON("ssh-key import", apicontract.ImportSSHKeyResponse{ID: ref.ID, Algorithm: ref.Algorithm, Fingerprint: ref.Fingerprint})
	}
	fmt.Printf("imported %s key %s as %s\n", ref.Algorithm, ref.Fingerprint, ref.ID)
	return exitOK
}

func sshHostKeyProbe(args []string) int {
	fs, cfgPath := newFlagSet("ssh host-key-probe")
	port := fs.Int("port", defaultSSHPort, "the SSH port to probe")
	asJSON := fs.Bool("json", false, "print the answer as JSON, the same object the API returns")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("ssh host-key-probe: expected exactly one HOST, got %d", len(operands))
	}
	if *port < 1 || *port > 65535 {
		return usageError("ssh host-key-probe: --port %d is out of range (1 to 65535)", *port)
	}
	host := operands[0]

	ctx := context.Background()
	route, cleanup, err := openSSHKeyRoute(ctx, *cfgPath, false)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	probe, err := route.ProbeHostKey(ctx, host, *port)
	if err != nil {
		return fail(fmt.Errorf("ssh host-key-probe: probing %s:%d for its host key: %w", host, *port, err))
	}
	if *asJSON {
		return printVerbJSON("ssh host-key-probe", apicontract.HostKeyProbeResponse{
			Algorithm: probe.Algorithm, Fingerprint: probe.Fingerprint, KnownHostsLine: probe.KnownHostsLine,
		})
	}
	fmt.Printf("%s:%d offered a %s host key\n", host, *port, probe.Algorithm)
	fmt.Printf("fingerprint: %s\n", probe.Fingerprint)
	fmt.Printf("known_hosts line: %s\n", probe.KnownHostsLine)
	fmt.Println("nothing was trusted. Compare the fingerprint with one you know, then pass the line to `backup-set create --known-hosts-line`, or let `--trust-host-key` probe and pin it")
	return exitOK
}
