package main

import (
	"encoding/json"
	"fmt"
	"os"
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
