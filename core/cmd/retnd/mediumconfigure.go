package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/retnd/retnd/core/service"
)

// `medium configure` and `medium backends` (#1154, audit rows P1-9, 66, 67
// and 68).
//
// A destination used to be describable on a terminal only in S3's words:
// `medium add` and `medium edit` carry one flag per S3 field and none of
// them is a path, so a local_volume, the only destination a fresh install
// has, could not be configured from the command line at all. `configure`
// speaks the vocabulary the backend's manifest declares instead, which is
// what PUT /storage-mediums/{id}/configuration takes, and `backends` prints
// that vocabulary so the ids it takes are discoverable.
//
// # Which door each goes through
//
// `configure` is a configuration write and goes through withMediumRoute,
// exactly as `add` and `edit` do: handed to the serving engine where there
// is one, and refused where it cannot be, for #538's reason. `backends`
// reads the registry embedded in this binary and opens nothing, so it works
// against a configuration that does not load and beside any engine.
//
// # What the engine does that this does not
//
// Nothing here proves the destination before writing it. The engine runs
// the same eight-step check in front of the write and refuses when it fails
// (#636), so a CLI-side proof would be a second opinion about a bucket
// policy that can change between the two calls. `--preflight` is the
// separate, write-nothing form for an operator who wants the report first.
//
// # Credentials
//
// The same four references `add` takes and for the same reason: material
// never arrives on a command line (see mediumFlags). Without one, the
// credential already configured is kept.

// visitedAmong reports which of names were written on the command line, in
// the order the flag set visits them.
func visitedAmong(fs *flag.FlagSet, names ...string) []string {
	var named []string
	fs.Visit(func(fl *flag.Flag) {
		if contains(names, fl.Name) {
			named = append(named, fl.Name)
		}
	})
	return named
}

// configuration builds what `medium configure` submits out of the flags,
// or the sentence for why the command line is not one.
//
// A field is id=value with both halves present, and a repeated id is
// refused rather than last-one-wins: the API refuses it for the same
// reason, since nothing can say which of two values was meant. An empty
// value is refused too, because an unset optional field is left OUT of the
// list so what it means when unset stays the manifest's answer (#294), and
// an empty value written back would freeze a default into the file.
func (f mediumFlags) configuration() (service.StorageMediumConfiguration, error) {
	fields := make(map[string]string, len(f.fields.raw))
	for _, raw := range f.fields.raw {
		id, value, found := strings.Cut(raw, "=")
		switch {
		case !found:
			return service.StorageMediumConfiguration{}, fmt.Errorf("--field %q is not id=value; write the backend's field id, an equals sign and the value, e.g. --field path=/mnt/usb", raw)
		case id == "":
			return service.StorageMediumConfiguration{}, fmt.Errorf("--field %q names no field before the equals sign", raw)
		case value == "":
			return service.StorageMediumConfiguration{}, fmt.Errorf("--field %s= carries an empty value; leave a field you want unset out instead, so what it means when unset stays the backend's answer", id)
		}
		if _, repeated := fields[id]; repeated {
			return service.StorageMediumConfiguration{}, fmt.Errorf("--field %s is given twice and nothing can tell which value was meant", id)
		}
		fields[id] = value
	}

	cfg := service.StorageMediumConfiguration{Backend: *f.backend, Fields: fields}
	f.fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "credentials-id":
			cfg.Credentials.ID = *f.credentialsID
		case "credentials-file":
			cfg.Credentials.File = *f.credentialsFile
		case "credentials-env":
			cfg.Credentials.Env = *f.credentialsEnv
		case "credentials-command":
			cfg.Credentials.Command = strings.Fields(*f.credentialsCommand)
		}
	})
	return cfg, nil
}

// mediumConfigure is `medium configure <medium-id> --backend B --field k=v
// ... [--credentials-*] [--preflight] [--json]`.
func mediumConfigure(ctx context.Context, cfgPath, id string, f mediumFlags) int {
	// Before anything is opened, so a command line that cannot mean
	// anything is refused without touching a configuration or an engine.
	cfg, err := f.configuration()
	if err != nil {
		return usageError("medium configure: %v", err)
	}

	body := func(route configWriteRoute) int {
		if *f.preflight {
			report, err := route.PreflightStorageMediumConfiguration(ctx, id, cfg)
			if err != nil {
				return fail(err)
			}
			if *f.json {
				if code := printMediumJSON(service.MediumPreflightWire(report)); code != 0 {
					return code
				}
			} else {
				printMediumReport(report)
				fmt.Println("nothing was written: a configuration preflight changes no destination whatever the report says")
			}
			if !report.OK {
				return 1
			}
			return 0
		}

		medium, err := route.ConfigureStorageMedium(ctx, id, cfg)
		if err != nil {
			return fail(err)
		}
		if *f.json {
			return printMediumJSON(service.StorageMediumWire(medium))
		}
		printMedium(medium)
		return 0
	}

	// --json promises standard output is one JSON value, so the mode line
	// and the startup log go to standard error instead.
	if *f.json {
		return withMediumRouteQuiet(ctx, cfgPath, body)
	}
	return withMediumRoute(ctx, cfgPath, body)
}

// mediumBackends is `medium backends [--json]`: every backend a destination
// can be an instance of, with the field ids `medium configure` takes for
// each, and the rules an instance id follows.
//
// It reads the registry embedded in this binary, the same call GET
// /backends makes, so the two cannot list different catalogues.
func mediumBackends(_ context.Context, _, _ string, f mediumFlags) int {
	catalog, err := service.RegisteredBackends()
	if err != nil {
		return fail(err)
	}
	if *f.json {
		return printMediumJSON(service.BackendsWire(catalog))
	}

	fmt.Printf("instance_id_pattern: %s\n", catalog.InstanceIDPattern)
	fmt.Printf("reserved_instance_id: %s\n", catalog.ReservedInstanceID)
	for _, b := range catalog.Backends {
		fmt.Printf("%s\n", b.ID)
		fmt.Printf("  label: %s\n", b.Label)
		fmt.Printf("  summary: %s\n", b.Summary)
		fmt.Printf("  role: %s\n", b.Role)
		fmt.Printf("  configurable: %t\n", b.Configurable)
		if !b.Configurable {
			fmt.Println("    registered and served, but every layer that would store or dial an instance of it refuses it by name")
		}
		fmt.Println("  fields:")
		for _, field := range b.Fields {
			fmt.Printf("    %s%s\n", field.ID, describeBackendField(field))
			if field.Help != "" {
				fmt.Printf("      %s\n", field.Help)
			}
		}
		fmt.Println("  probe:")
		for _, step := range b.Probe {
			if step.Run {
				fmt.Printf("    %s\n", step.Step)
			} else {
				fmt.Printf("    %s: skipped, %s\n", step.Step, step.Reason)
			}
		}
	}
	if len(catalog.Unregistered) > 0 {
		names := append([]string(nil), catalog.Unregistered...)
		sort.Strings(names)
		fmt.Printf("unregistered: %s\n", strings.Join(names, ", "))
		fmt.Println("  understood by this build's transport layer and declared by no manifest, so no instance of one can exist")
	}
	return 0
}

// describeBackendField renders the parenthesised half of one field line:
// its kind, whether it is required, the values an enum offers and what
// leaving it unset means.
func describeBackendField(f service.BackendField) string {
	parts := []string{f.Kind}
	if f.Required {
		parts = append(parts, "required")
	}
	if len(f.Values) > 0 {
		values := make([]string, 0, len(f.Values))
		for _, v := range f.Values {
			values = append(values, v.Value)
		}
		parts = append(parts, "one of "+strings.Join(values, " | "))
	}
	if f.UnsetMeans != "" {
		parts = append(parts, "unset means "+f.UnsetMeans)
	}
	text := " (" + strings.Join(parts, ", ") + ")"
	if f.Label != "" {
		text += ": " + f.Label
	}
	return text
}
