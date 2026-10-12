package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/service"
)

// `settings assurance` and `backup-set assurance`: the assurance policy
// (EPIC Q, Q1.4, #982) from a terminal, doing exactly what the browser's
// settings page and the per-set card do through /api/v1/settings/assurance,
// /api/v1/backup-sets/{source}/{set}/assurance and /api/v1/assurance/due.
//
// # Same service, same words
//
// Every write goes through the methods the route handlers call, so a patch
// the browser is refused for is refused here with the same sentence, and
// --json prints the SAME body shape the API serves (apicontract's own
// types), so a script can switch between the two without re-parsing.
//
// # Where a write goes
//
// Through openConfigWriteRoute, which is `settings patch`'s door: with
// nothing serving this deployment the change is written here and an engine
// started afterwards reads it, and beside a serving engine that this
// command has been told how to reach (RETND_API_URL with the administrator's
// credentials) the change is handed to the engine's PATCH route, so the
// engine owns the write, hot-reloads it, and its next-due moves with the
// answer. Beside a serving engine with no address the write is still
// refused with the file untouched, exit 3, because a change left in the file
// is one that process would never read. A refusal for the request itself
// (a cadence under the floor, a validator that does not exist) is the same
// sentence and the same exit code on both routes.
//
// # Zero clears
//
// An explicitly passed zero or empty value is a request, never an
// omission: --scrub-cadence 0 returns the cadence to the default, and
// --recovery-test-validator "" clears the validator. Every flag is read
// through fs.Visit for settings patch's reason.

const settingsAssuranceOperand = "assurance"

type assuranceFlags struct {
	scrubCadence  *time.Duration
	scrubStrategy *string
	rtMode        *string
	rtCadence     *time.Duration
	rtValidator   *string
	stagingRoot   *string
	stagingTTL    *time.Duration
	maxFullReads  *int
	maxPerDest    *int
	reset         *bool
}

// setScopeAssuranceFlags are the flags a backup set may carry. The rest are
// global only, and the per-set answer to each is no (see service.AssuranceHostKnob).
var setScopeAssuranceFlags = []string{"scrub-cadence", "scrub-strategy", "recovery-test-mode", "recovery-test-cadence", "recovery-test-validator"}

var globalOnlyAssuranceFlags = []string{"staging-root", "staging-ttl", "max-concurrent-full-reads", "max-per-destination"}

func declareAssuranceFlags(fs *flag.FlagSet) assuranceFlags {
	return assuranceFlags{
		scrubCadence:  fs.Duration("scrub-cadence", 0, "how long after a copy was last assured it is next due, e.g. 168h. Zero clears it back to the default; under 1h is refused"),
		scrubStrategy: fs.String("scrub-strategy", "", "what a scrub reads: auto, existence or content. Empty clears it"),
		rtMode:        fs.String("recovery-test-mode", "", "off, required, or (per set only) inherit or custom. Empty clears it. required needs a validator"),
		rtCadence:     fs.Duration("recovery-test-cadence", 0, "how often a recovery test is due, e.g. 720h. Per set, only with mode custom. Zero clears it"),
		rtValidator:   fs.String("recovery-test-validator", "", "the recovery-test script to run, by file name without .local.sh and never a path. Empty clears it"),
		stagingRoot:   fs.String("staging-root", "", "global only: where recovered copies are staged; an absolute path. Empty returns to the directory beside the state database"),
		stagingTTL:    fs.Duration("staging-ttl", 0, "global only: how long a staged leftover survives. Zero clears it to the default"),
		maxFullReads:  fs.Int("max-concurrent-full-reads", 0, "global only: the ceiling on full reads running at once. Zero clears it"),
		maxPerDest:    fs.Int("max-per-destination", 0, "global only: the ceiling on concurrent reads against one destination. Zero clears it"),
		reset:         fs.Bool("reset", false, "backup-set only: remove this set's own assurance block so it follows the deployment's policy again"),
	}
}

func assuranceSetRequest(fs *flag.FlagSet, f assuranceFlags) service.UpdateBackupSetAssuranceRequest {
	var req service.UpdateBackupSetAssuranceRequest
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "scrub-cadence":
			req.ScrubCadence = f.scrubCadence
		case "scrub-strategy":
			req.ScrubStrategy = f.scrubStrategy
		case "recovery-test-mode":
			req.RecoveryTestMode = f.rtMode
		case "recovery-test-cadence":
			req.RecoveryTestCadence = f.rtCadence
		case "recovery-test-validator":
			req.RecoveryTestValidatorID = f.rtValidator
		}
	})
	return req
}

func assuranceGlobalRequest(fs *flag.FlagSet, f assuranceFlags) service.UpdateAssuranceSettingsRequest {
	var req service.UpdateAssuranceSettingsRequest
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "scrub-cadence":
			req.ScrubCadence = f.scrubCadence
		case "scrub-strategy":
			req.ScrubStrategy = f.scrubStrategy
		case "recovery-test-mode":
			req.RecoveryTestMode = f.rtMode
		case "recovery-test-cadence":
			req.RecoveryTestCadence = f.rtCadence
		case "recovery-test-validator":
			req.RecoveryTestValidatorID = f.rtValidator
		case "staging-root":
			req.StagingRoot = f.stagingRoot
		case "staging-ttl":
			req.StagingTTL = f.stagingTTL
		case "max-concurrent-full-reads":
			req.MaxConcurrentFullReads = f.maxFullReads
		case "max-per-destination":
			req.MaxPerDestination = f.maxPerDest
		}
	})
	return req
}

func secs(d time.Duration) int64 { return int64(d / time.Second) }

func assurancePolicyWire(p service.AssurancePolicy) (apicontract.AssuranceScrubPolicy, apicontract.AssuranceRecoveryTestPolicy) {
	return apicontract.AssuranceScrubPolicy{CadenceSeconds: secs(p.Scrub.Cadence), Strategy: p.Scrub.Strategy},
		apicontract.AssuranceRecoveryTestPolicy{CadenceSeconds: secs(p.RecoveryTest.Cadence), Mode: p.RecoveryTest.Mode, ValidatorID: p.RecoveryTest.ValidatorID}
}

func assuranceSettingsWire(s service.AssuranceSettings) apicontract.AssuranceSettingsResponse {
	scrub, rt := assurancePolicyWire(s.Policy)
	knobs := make([]apicontract.AssuranceHostKnob, 0, len(s.HostKnobs))
	for _, k := range s.HostKnobs {
		knobs = append(knobs, apicontract.AssuranceHostKnob{Name: k.Name, PerSet: k.PerSet, Reason: k.Reason, Value: k.Value})
	}
	return apicontract.AssuranceSettingsResponse{
		Configured: s.Configured, HostKnobs: knobs, MaxConcurrentFullReads: int64(s.MaxConcurrentFullReads),
		MaxPerDestination: int64(s.MaxPerDestination), RecoveryTest: rt, Scrub: scrub, StagingRoot: s.StagingRoot,
		StagingTtlSeconds: secs(s.StagingTTL),
	}
}

func backupSetAssuranceWire(s service.BackupSetAssurance) apicontract.BackupSetAssurance {
	scrub, rt := assurancePolicyWire(s.Policy)
	return apicontract.BackupSetAssurance{BackupSetID: s.BackupSetID, IsOverride: s.Override, RecoveryTest: rt, Scrub: scrub}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printAssurancePolicy(p service.AssurancePolicy) {
	fmt.Printf("  scrub: every %s, strategy %s\n", p.Scrub.Cadence, p.Scrub.Strategy)
	fmt.Printf("  recovery test: %s, every %s, validator %s\n", p.RecoveryTest.Mode, p.RecoveryTest.Cadence, orDash(p.RecoveryTest.ValidatorID))
}

func printAssuranceSettings(s service.AssuranceSettings) {
	if s.Configured {
		fmt.Println("assurance policy (set in config.yaml):")
	} else {
		fmt.Println("assurance policy (the defaults; config.yaml sets none):")
	}
	printAssurancePolicy(s.Policy)
	fmt.Println("global only (a backup set cannot override these):")
	for _, k := range s.HostKnobs {
		fmt.Printf("  %s: %s\n      no per-set override: %s\n", k.Name, orDash(k.Value), k.Reason)
	}
}

func printBackupSetAssurance(s service.BackupSetAssurance) {
	origin := "inherits the deployment's policy"
	if s.Override {
		origin = "has its own assurance block"
	}
	fmt.Printf("%s %s:\n", s.BackupSetID, origin)
	printAssurancePolicy(s.Policy)
}

// cmdSettingsAssurance is every `settings assurance ...` form: show, patch
// and due.
func cmdSettingsAssurance(args []string) int {
	fs, cfgPath := newFlagSet("settings assurance")
	asJSON := fs.Bool("json", false, "emit JSON, the same body the API serves, instead of text")
	f := declareAssuranceFlags(fs)
	dueSet := fs.String("backup-set", "", "due only: narrow the schedule to one source/backup-set")
	dueOnly := fs.Bool("due", false, "due only: list only copies whose next-due time has passed")
	overdueOnly := fs.Bool("overdue", false, "due only: list only copies a whole cadence past due, or never assured")

	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) == 0 || operands[0] != settingsAssuranceOperand {
		return usageError(`settings assurance: expected "assurance" as the first argument`)
	}
	rest := operands[1:]
	ctx := context.Background()

	switch {
	case len(rest) == 0:
		if named := visitedFlags(fs, "config", "json"); len(named) > 0 {
			return usageError("settings assurance: --%s changes nothing on a read; `settings assurance patch --%s ...` is the form that writes", strings.Join(named, ", --"), named[0])
		}
		svc, cleanup, err := openBackupService(ctx, *cfgPath, readsConfig)
		defer cleanup()
		if err != nil {
			return fail(err)
		}
		s, err := svc.AssuranceSettings(ctx)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return printWorkflowJSON(assuranceSettingsWire(s))
		}
		printAssuranceSettings(s)
		return 0

	case rest[0] == "patch":
		if len(rest) != 1 {
			return usageError("settings assurance patch takes no argument; %q is one", rest[1])
		}
		if code := refuseWorkflowFlagsExcept(fs, "settings assurance patch", append(append([]string(nil), setScopeAssuranceFlags...), globalOnlyAssuranceFlags...), "config", "json"); code != exitOK {
			return code
		}
		req := assuranceGlobalRequest(fs, f)
		if req == (service.UpdateAssuranceSettingsRequest{}) {
			return usageError("settings assurance patch: name at least one setting; a patch that changes nothing would rewrite and reload the configuration to no effect")
		}
		route, cleanup, err := openConfigWriteRoute(ctx, *cfgPath)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		logStartup(ctx, logger(), app.BuildVersionInfo(version, commit))
		s, err := route.UpdateAssuranceSettings(ctx, req)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return printWorkflowJSON(assuranceSettingsWire(s))
		}
		printAssuranceSettings(s)
		return 0

	case rest[0] == "due":
		if len(rest) != 1 {
			return usageError("settings assurance due takes no argument; %q is one", rest[1])
		}
		if code := refuseWorkflowFlagsExcept(fs, "settings assurance due", []string{"backup-set", "due", "overdue"}, "config", "json"); code != exitOK {
			return code
		}
		if *dueOnly && *overdueOnly {
			return usageError("settings assurance due: --due and --overdue are two filters; pass one")
		}
		svc, cleanup, err := openBackupService(ctx, *cfgPath, readsConfig)
		defer cleanup()
		if err != nil {
			return fail(err)
		}
		items, err := svc.AssuranceDue(ctx, service.AssuranceDueFilter{BackupSetID: *dueSet, DueOnly: *dueOnly, OverdueOnly: *overdueOnly})
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			out := apicontract.AssuranceDueResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Items: []apicontract.AssuranceDueItem{}}
			for _, it := range items {
				w := apicontract.AssuranceDueItem{ArtifactID: it.ArtifactID, ArtifactName: it.ArtifactName, BackupSetID: it.BackupSetID,
					Basis: it.Basis, CadenceSeconds: secs(it.Cadence), Due: it.Due, NextDueAt: it.NextDueAt.UTC().Format(time.RFC3339), Overdue: it.Overdue}
				if !it.LastAssuredAt.IsZero() {
					w.LastAssuredAt = it.LastAssuredAt.UTC().Format(time.RFC3339)
				}
				out.Items = append(out.Items, w)
			}
			return printWorkflowJSON(out)
		}
		if len(items) == 0 {
			fmt.Println("nothing is scheduled: no scrub-eligible copy matches")
			return 0
		}
		for _, it := range items {
			state := "ok"
			switch {
			case it.Overdue:
				state = "OVERDUE"
			case it.Due:
				state = "due"
			}
			last := "never"
			if !it.LastAssuredAt.IsZero() {
				last = it.LastAssuredAt.UTC().Format(time.RFC3339)
			}
			fmt.Printf("%-8s %s  next %s  last %s (%s)  every %s\n", state, it.ArtifactID, it.NextDueAt.UTC().Format(time.RFC3339), last, it.Basis, it.Cadence)
		}
		return 0
	}
	return usageError("settings assurance has no %q form; it has show, patch and due", rest[0])
}

// cmdBackupSetAssurance is `backup-set assurance <source/backup-set>`: show
// the resolved policy, patch the set's own, or --reset it to inherit.
func cmdBackupSetAssurance(args []string) int {
	fs, cfgPath := newFlagSet("backup-set assurance")
	asJSON := fs.Bool("json", false, "emit JSON, the same body the API serves, instead of text")
	f := declareAssuranceFlags(fs)

	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 2 || operands[0] != "assurance" {
		return usageError(`backup-set assurance: expected "assurance <source/backup-set>" and exactly one backup set id`)
	}
	id := operands[1]
	if !isBackupSetID(id) {
		return usageError("backup-set assurance: %q is not a backup set id; a backup set id is exactly source/name", id)
	}
	if named := visitedFlags(fs, append(append([]string(nil), setScopeAssuranceFlags...), "config", "json", "reset")...); len(named) > 0 {
		return usageError("backup-set assurance: --%s is global only, and the per-set answer is no: it is a property of the host or the process, which a per-set value could not be honoured by. Use `settings assurance patch`", strings.Join(named, ", --"))
	}
	req := assuranceSetRequest(fs, f)
	named := req != (service.UpdateBackupSetAssuranceRequest{})
	if *f.reset && named {
		return usageError("backup-set assurance: --reset removes this set's own block and the other flags write one; pass one or the other")
	}

	ctx := context.Background()
	var got service.BackupSetAssurance
	if *f.reset || named {
		route, cleanup, err := openConfigWriteRoute(ctx, *cfgPath)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		logStartup(ctx, logger(), app.BuildVersionInfo(version, commit))
		if *f.reset {
			got, err = route.ResetBackupSetAssurance(ctx, id)
		} else {
			got, err = route.UpdateBackupSetAssurance(ctx, id, req)
		}
		if err != nil {
			return fail(err)
		}
	} else {
		svc, cleanup, err := openBackupService(ctx, *cfgPath, readsConfig)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		got, err = svc.BackupSetAssurance(ctx, id)
		if err != nil {
			return fail(err)
		}
	}
	if *asJSON {
		return printWorkflowJSON(backupSetAssuranceWire(got))
	}
	printBackupSetAssurance(got)
	return 0
}
