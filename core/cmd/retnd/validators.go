package main

import (
	"fmt"

	"github.com/retnd/retnd/core/service"
)

// cmdValidators is `retnd validators [--json]` (#1152): the catalog of
// application validators this binary carries, which GET /validators serves
// to the web UI's picker and which a terminal could only learn from the
// refusal `backup-set create --validator-id` gives for an id that is not one.
//
// The catalog is compiled into the binary, so this reads no configuration and
// opens no journal, and it works on an instance nobody has configured yet,
// which is exactly when somebody is choosing an id for the first set.
func cmdValidators(args []string) int {
	fs, _ := newFlagSet("validators")
	asJSON := fs.Bool("json", false, "print the API's ListValidatorsResponse instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return 2
	}
	if len(operands) != 0 {
		return usageError("validators takes no argument")
	}
	if *asJSON {
		return emitWireJSON(service.ValidatorsWire())
	}
	for _, v := range service.RegisteredValidators() {
		fmt.Printf("%s\n  %s\n", v.ID, v.Summary)
	}
	return exitOK
}
