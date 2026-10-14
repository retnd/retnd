package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/service"
)

// emitWireJSON writes one wire object to stdout, indented, the way every
// other `--json` here does (#1152). It is called only after the read has
// succeeded, so a refused read leaves stdout empty and a script that parses
// it never mistakes an error for a result.
//
// The encoder is the standard one with HTML escaping left on, which is what
// the web host's writeJSON does too, so a `<` in a reason reads the same on
// both surfaces.
func emitWireJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail(fmt.Errorf("writing the result: %w", err))
	}
	return exitOK
}

// openBackupServiceForRead is openBackupService for a read that may print
// JSON. With --json the structured log the service writes while opening (an
// invalid configuration is logged as an error record before it is returned)
// goes to stderr, because stdout is then data and one stray log record in it
// is a document a parser cannot read. Without --json it is exactly
// openBackupService, so the text forms print what they always did.
func openBackupServiceForRead(ctx context.Context, configPath string, asJSON bool) (*service.BackupService, func(), error) {
	var logTo io.Writer = os.Stdout
	if asJSON {
		logTo = os.Stderr
	}
	return openBackupServiceLoggingTo(ctx, configPath, readsConfig, logTo)
}

// jsonLogger is logger() for a verb whose standard output is a JSON document:
// the same structured sink at the same level, on standard error.
func jsonLogger() *obs.Logger {
	return obs.New(os.Stderr, obs.LevelFromEnv())
}
