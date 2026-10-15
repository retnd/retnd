package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/app"
)

// cmdVersion is FR-26's `version` command: it reports the retnd
// version, the embedded rclone version, the Go version and the build
// commit, in that order, exactly as the EPIC's CLI section lists them.
// See internal/app.BuildVersionInfo's doc for how the rclone version is
// obtained without this binary, or internal/app, ever importing rclone
// directly.
//
// # Why a flag set with no flags in it
//
// version is the one command the usage banner excludes from --config, and
// it takes no operand either. It used to have no flag set at all, which
// meant it ignored whatever it was given and succeeded, while every other
// command in this binary answers a flag it does not know with exit 2
// (issue #189). An empty flag.FlagSet, with the same ContinueOnError
// newFlagSet uses, gives version that same refusal and the same message
// shape without giving it a flag it does not want. "Does not accept
// --config" and "ignores --config" are different promises, and only the
// first one is documented: a script that guesses at `version --json` used
// to get exit 0 and human-readable text, and it is better told here than
// left to fail wherever it tries to parse that.
func cmdVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	engine := fs.Bool("engine", false, "also ask the engine serving this deployment for its own version, configuration revision, deployment id and readiness (needs a route to it)")
	asJSON := fs.Bool("json", false, "with --engine, print the engine's VersionResponse (GET /system/version) instead of text")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return usageError("version takes no arguments")
	}
	if *asJSON && !*engine {
		return usageError("version: --json prints the engine's object (GET /system/version), and this binary's own version is not one of the API's objects; ask for it with `version --engine --json`")
	}

	if *asJSON {
		// The document is the whole of stdout, so the binary's own four lines
		// are not printed in front of it.
		answer, err := askEngineVersion()
		if err != nil {
			return fail(err)
		}
		return emitWireJSON(answer)
	}

	info := app.BuildVersionInfo(version, commit)
	fmt.Printf(cliecho.Binary+" %s\n", info.BinaryVersion)
	fmt.Printf("rclone %s\n", info.RcloneVersion)
	fmt.Printf("go %s\n", info.GoVersion)
	fmt.Printf("commit %s\n", info.Commit)
	if !*engine {
		return 0
	}

	answer, err := askEngineVersion()
	if err != nil {
		return fail(err)
	}
	fmt.Println("engine:")
	fmt.Printf("  api_version: %s\n", answer.APIVersion)
	fmt.Printf("  engine_version: %s\n", answer.EngineVersion)
	fmt.Printf("  core_version: %s\n", answer.CoreVersion)
	fmt.Printf("  commit: %s\n", answer.Commit)
	fmt.Printf("  go_version: %s\n", answer.GoVersion)
	fmt.Printf("  config_revision: %s\n", answer.ConfigRevision)
	fmt.Printf("  deployment_id: %s\n", answer.DeploymentID)
	fmt.Printf("  ready: %v\n", answer.Ready)
	fmt.Printf("  configured: %v\n", answer.Configured)
	for _, p := range answer.AdoptedPaths {
		fmt.Printf("  adopted_path: %s is served from %s, renamed from %s\n", p.What, p.Serving, p.Renamed)
	}
	return 0
}

// askEngineVersion is GET /system/version put to the engine this host has
// been told about. The binary's own version is a fact about this file and
// needs no engine; the engine's is a fact about a running process (which
// configuration it holds, which deployment it serves, whether it is ready)
// and nothing in this one can know it.
func askEngineVersion() (apicontract.VersionResponse, error) {
	client, err := dialEngine()
	if err != nil {
		return apicontract.VersionResponse{}, err
	}
	answer, err := client.SystemVersion(context.Background())
	if err != nil {
		return apicontract.VersionResponse{}, fmt.Errorf("asking the engine for its version: %w", err)
	}
	if answer.AdoptedPaths == nil {
		// An array on the contract even when nothing was adopted.
		answer.AdoptedPaths = []apicontract.AdoptedPath{}
	}
	return answer, nil
}
