package main

import (
	"context"
	"fmt"
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
	"browse":       cmdSystemBrowse,
	"capabilities": cmdSystemCapabilities,
	"first-run":    cmdSystemFirstRun,
}

func systemVerbNames() []string { return []string{"browse", "capabilities", "first-run"} }

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
	return exitOK
}

// cmdSystemBrowse is `retnd system browse [PATH] [--json]` (#1179): the folders
// below the staging root, on the machine the engine runs on. It is the
// terminal's half of the folder browser behind "Folder under the staging root on the host", and
// what it prints is what a --to name under that root can be.
//
// It asks the engine and refuses without a route to it, like its siblings:
// listing this host's file system would answer for the wrong machine whenever
// the engine runs elsewhere. PATH is relative to the staging root, and the
// engine refuses one that climbs out or goes through a symbolic link.
func cmdSystemBrowse(args []string) int {
	fs, _ := newFlagSet("system browse")
	asJSON := fs.Bool("json", false, "print the API's object instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return 2
	}
	// operands[0] is the verb; at most one folder follows it.
	if len(operands) < 1 || operands[0] != "browse" || len(operands) > 2 {
		return usageError("system browse takes at most one folder, relative to the staging root")
	}
	path := ""
	if len(operands) == 2 {
		path = operands[1]
	}
	var got apicontract.HostDirectoryResponse
	if code := askEngine("browse", "its folders", func(ctx context.Context, c *apiclient.Client) (err error) {
		got, err = c.HostDirectory(ctx, path)
		return err
	}); code != exitOK {
		return code
	}
	if *asJSON {
		return emitWireJSON(got)
	}
	printHostDirectory(got)
	return exitOK
}

// printHostDirectory is the text form of the engine's folder listing. Each
// folder is printed as the name a --to takes, one per line, so the output can
// be read by eye and by a script alike.
func printHostDirectory(got apicontract.HostDirectoryResponse) {
	fmt.Printf("root: %s\n", got.Root)
	here := got.Path
	if here == "" {
		here = "(the root)"
	}
	fmt.Printf("folder: %s\n", here)
	if len(got.Entries) == 0 {
		fmt.Println("no folders in here")
	}
	for _, e := range got.Entries {
		fmt.Println(e.Path)
	}
	if got.Truncated {
		fmt.Println("(truncated: this folder holds more folders than are listed)")
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
