package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/apiclient"
)

// `retnd system capabilities|first-run [--json]` (#1152): the two system
// reads that are facts about the serving process and nothing else.
//
// capabilities is what the platform the engine runs on can do (native
// sign-in, a storage picker, ...), declared by the platform adapter inside
// that process. first-run is which guided setup steps it counts as done: the
// administrator account and the mail server live in the web host, not in
// core, so this binary has no way to look.
//
// Both ask the engine and refuse without a route to it. Answering from this
// host would be inventing the platform's declaration or guessing at an
// administrator that may exist, and "none" printed for a question that has no
// meaning here would be a lie.

var systemVerbs = map[string]func(args []string) int{
	"capabilities": cmdSystemCapabilities,
	"first-run":    cmdSystemFirstRun,
	"staging":      cmdSystemStaging,
}

func systemVerbNames() []string { return []string{"capabilities", "first-run", "staging"} }

func cmdSystem(args []string) int {
	for _, a := range args {
		if verb, ok := systemVerbs[a]; ok {
			return verb(args)
		}
	}
	return usageError("system: expected a verb; the verbs are %s", strings.Join(systemVerbNames(), ", "))
}

// askEngine dials the engine this host was told about and runs one read.
func askEngine(verb, what string, ask func(ctx context.Context, c *apiclient.Client) error) int {
	client, err := dialEngine()
	if err != nil {
		return fail(fmt.Errorf("system %s: %w", verb, err))
	}
	if err := ask(context.Background(), client); err != nil {
		return fail(fmt.Errorf("asking the engine for %s: %w", what, err))
	}
	return exitOK
}

func systemFlags(verb string, args []string) (asJSON bool, code int) {
	fs, _ := newFlagSet("system " + verb)
	j := fs.Bool("json", false, "print the API's object instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return false, 2
	}
	if len(operands) != 1 || operands[0] != verb {
		return false, usageError("system %s takes no argument", verb)
	}
	return *j, exitOK
}

func cmdSystemCapabilities(args []string) int {
	asJSON, code := systemFlags("capabilities", args)
	if code != exitOK {
		return code
	}
	var got apicontract.CapabilitiesResponse
	if code := askEngine("capabilities", "its platform capabilities", func(ctx context.Context, c *apiclient.Client) (err error) {
		got, err = c.SystemCapabilities(ctx)
		return err
	}); code != exitOK {
		return code
	}
	if asJSON {
		return emitWireJSON(got)
	}
	fmt.Printf("platform: %s\n", got.Platform)
	fmt.Printf("native_auth: %v\n", got.NativeAuth)
	fmt.Printf("native_notifications: %v\n", got.NativeNotifications)
	fmt.Printf("storage_picker: %v\n", got.StoragePicker)
	fmt.Printf("embedded_window: %v\n", got.EmbeddedWindow)
	fmt.Printf("app_store_packaging: %v\n", got.AppStorePackaging)
	// Whether the engine's destructive gate is open (#1165): the terminal
	// has no gate of its own, so this is the way to read the engine's.
	gate := "destructive operations: " + got.DestructiveOperations.State
	if got.DestructiveOperations.Reason != "" {
		gate += " (" + got.DestructiveOperations.Reason + ")"
	}
	fmt.Println(gate)
	// What an open gate still asks for (#1163), in words a terminal user can
	// act on. A script wants the field names, and --json prints those.
	if got.DestructiveOperations.StepUpRequired {
		fmt.Println("in the web interface, restores ask for the administrator's password again")
	}
	if got.DestructiveOperations.InsecureTransportAckRequired {
		fmt.Println("this connection is not encrypted, so the web interface asks before a run, a retention apply or a restore goes ahead")
	}
	return exitOK
}

// cmdSystemStaging is `retnd system staging [PATH] [--json]` (#1179): the folders
// below the staging root, on the machine the engine runs on. It is the
// terminal's half of the folder browser behind "Staging path on the host", and
// what it prints is what a --to name under that root can be.
//
// It asks the engine and refuses without a route to it, like its siblings:
// listing this host's file system would answer for the wrong machine whenever
// the engine runs elsewhere. PATH is relative to the staging root, and the
// engine refuses one that climbs out or goes through a symbolic link.
func cmdSystemStaging(args []string) int {
	fs, _ := newFlagSet("system staging")
	asJSON := fs.Bool("json", false, "print the API's object instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return 2
	}
	// operands[0] is the verb; at most one folder follows it.
	if len(operands) < 1 || operands[0] != "staging" || len(operands) > 2 {
		return usageError("system staging takes at most one folder, relative to the staging root")
	}
	path := ""
	if len(operands) == 2 {
		path = operands[1]
	}
	var got apicontract.StagingDirectoryResponse
	if code := askEngine("staging", "its staging folders", func(ctx context.Context, c *apiclient.Client) (err error) {
		got, err = c.StagingDirectories(ctx, path)
		return err
	}); code != exitOK {
		return code
	}
	if *asJSON {
		return emitWireJSON(got)
	}
	printStagingDirectory(got)
	return exitOK
}

// printStagingDirectory is the text form of the engine's folder listing. The
// folders go to stdout, one per line and nothing else, each as the name a
// --to takes, so a script can read them as they are. Where they are, an empty
// folder and a cut listing are said on stderr, for the person reading.
func printStagingDirectory(got apicontract.StagingDirectoryResponse) {
	here := got.Path
	if here == "" {
		here = "(the root)"
	}
	fmt.Fprintf(os.Stderr, "staging root: %s\nfolder: %s\n", got.Root, here)
	if len(got.Entries) == 0 {
		fmt.Fprintln(os.Stderr, "no folders in here")
	}
	for _, e := range got.Entries {
		fmt.Println(e.Path)
	}
	if got.Truncated {
		fmt.Fprintln(os.Stderr, "this folder holds more folders than are listed; give a longer name to --to to reach one that is not shown")
	}
}

func cmdSystemFirstRun(args []string) int {
	asJSON, code := systemFlags("first-run", args)
	if code != exitOK {
		return code
	}
	var got apicontract.FirstRunStatusResponse
	if code := askEngine("first-run", "its first-run status", func(ctx context.Context, c *apiclient.Client) (err error) {
		got, err = c.FirstRunStatus(ctx)
		return err
	}); code != exitOK {
		return code
	}
	if asJSON {
		return emitWireJSON(got)
	}
	printFirstRunStatus(got)
	return exitOK
}

// printFirstRunStatus is the text form of the engine's first-run status.
func printFirstRunStatus(got apicontract.FirstRunStatusResponse) {
	fmt.Printf("configured: %v\n", got.Configured)
	fmt.Printf("next_step: %s\n", got.NextStep)
	fmt.Printf("administrator: %s\n", got.Steps.Administrator)
	fmt.Printf("email: %s\n", got.Steps.Email)
	fmt.Printf("backup_set: %s\n", got.Steps.BackupSet)
	if got.ConfigDir != "" {
		fmt.Printf("config_dir: %s\n", got.ConfigDir)
	}
	if got.ConfigDirProblem != "" {
		fmt.Printf("config_dir_problem: %s\n", got.ConfigDirProblem)
	}
}
