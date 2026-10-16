package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/service"
)

// `retnd assurance validator ...` (#1203): custom recovery-test validators,
// each an operator's NAME.local.sh attached to an artifact-name pattern, from
// a terminal, doing what Settings > Assurance does through
// /api/v1/assurance/validators.
//
// # Why under `assurance`
//
// The top-level `validators` verb is already taken by the catalog of ingest
// validators compiled into this binary (#1152), a different contract that
// runs at ingest and not in a recovery test. `assurance` already groups the
// recovery-test and scrub verbs, its dispatcher hands each subverb its own
// flags, and `assurance validator` mirrors the route's /assurance/validators,
// so an operator reading the activity feed's command line and the API
// reference finds the same noun in both.
//
// # Where each form goes
//
// The reads (list, patterns, script, add --dry-run) answer from this host's
// configuration and journal, the way `settings assurance due` does.
// default-script reads nothing at all: the script is compiled in. The writes
// (add, update, remove) go through openConfigWriteRoute, the door every
// configuration write in this binary uses: written here when nothing serves
// this deployment, handed to the engine when RETND_API_URL names one, and
// refused with the file untouched beside an engine this command cannot
// reach. Routed, add and update answer the engine's password step-up with
// the administrator's password this command already signs in with.

var assuranceValidatorVerbs = map[string]func(args []string) int{
	"list":           cmdAssuranceValidatorList,
	"add":            cmdAssuranceValidatorAdd,
	"update":         cmdAssuranceValidatorUpdate,
	"remove":         cmdAssuranceValidatorRemove,
	"script":         cmdAssuranceValidatorScript,
	"patterns":       cmdAssuranceValidatorPatterns,
	"default-script": cmdAssuranceValidatorDefaultScript,
}

const assuranceValidatorForms = "list, add, update, remove, script, patterns or default-script"

func cmdAssuranceValidator(args []string) int {
	// The form is the first argument that is one, wherever the flags sit,
	// as `assurance` itself finds its verb.
	for i, a := range args {
		if verb, ok := assuranceValidatorVerbs[a]; ok {
			return verb(append(append([]string{}, args[:i]...), args[i+1:]...))
		}
	}
	return usageError("assurance validator: expected one of %s", assuranceValidatorForms)
}

// customValidatorRoute is the custom validators' half of configWriteRoute.
// *service.BackupService satisfies it as it stands.
type customValidatorRoute interface {
	CreateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error)
	UpdateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error)
	RemoveCustomValidator(ctx context.Context, name string) (service.CustomValidator, error)
}

func cmdAssuranceValidatorList(args []string) int {
	fs, cfgPath := newFlagSet("assurance validator list")
	asJSON := fs.Bool("json", false, "print the API's ListCustomValidatorsResponse instead of text")
	set := fs.String("backup-set", "", "only the validators that apply to this source/backup-set, in the order a test tries them")
	artifact := fs.String("artifact", "", "also say which validator a recovery test of this backup (source/set/name) would run")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("assurance validator list takes no operands; narrow it with --backup-set")
	}
	ctx := context.Background()
	svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
	defer cleanup()
	if err != nil {
		return fail(err)
	}
	l, err := svc.ListCustomValidators(ctx, service.CustomValidatorsFilter{BackupSetID: *set, ArtifactID: *artifact})
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return emitWireJSON(service.CustomValidatorsWire(l))
	}
	if !l.Available {
		fmt.Printf("uploads: unavailable: %s\n", l.UnavailableReason)
	}
	if !l.RunnerConfigured {
		fmt.Println("note: no host workflow runner is configured, so a validator is stored and cannot run yet")
	}
	if len(l.Validators) == 0 {
		fmt.Println("no custom validators")
	}
	for _, v := range l.Validators {
		printCustomValidator(v)
	}
	if r := l.Resolution; r != nil {
		switch {
		case len(r.Ambiguous) > 0:
			fmt.Printf("%s: %s all match at one scope, so a test ends could_not_complete\n", r.ArtifactID, strings.Join(r.Ambiguous, ", "))
		case r.Validator != "":
			fmt.Printf("%s: a recovery test runs %s\n", r.ArtifactID, r.Validator)
		default:
			fmt.Printf("%s: no pattern applies; %s decides\n", r.ArtifactID, r.Fallback)
		}
	}
	return exitOK
}

func printCustomValidator(v service.CustomValidator) {
	scope := "every set"
	if v.BackupSetID != "" {
		scope = v.BackupSetID
	}
	fmt.Printf("%s  %s  %s\n", v.Name, scope, v.Pattern)
	script := "MISSING, so a test it applies to cannot complete"
	if v.ScriptPresent {
		script = fmt.Sprintf("%d bytes, sha256 %s, written %s", v.ScriptSize, v.ScriptSHA256, v.UploadedAt.Format(time.RFC3339))
	}
	fmt.Printf("  script %s: %s\n", v.ScriptFile, script)
	if v.Matches > 0 {
		fmt.Printf("  matches %d backup(s) in %s, e.g. %s\n", v.Matches, strings.Join(v.MatchSets, ", "), v.ExampleName)
	} else {
		fmt.Println("  matches no backup yet")
	}
	if !v.Anchored {
		fmt.Println("  the pattern is not anchored with ^...$, so it matches anywhere in a name")
	}
	if r := v.LastResult; r != nil {
		fmt.Printf("  last test: %s at %s (%s)\n", r.State, r.At.Format(time.RFC3339), r.Artifact)
	}
}

// readScriptFile reads --script, bounded so a wrong path to something huge
// is refused rather than read.
func readScriptFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, service.MaxCustomValidatorScriptBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > service.MaxCustomValidatorScriptBytes {
		return "", fmt.Errorf("%s is larger than the %d bytes a validator may be", path, service.MaxCustomValidatorScriptBytes)
	}
	return string(b), nil
}

type validatorWriteFlags struct {
	asJSON  *bool
	pattern *string
	script  *string
	set     *string
	dryRun  *bool
}

func validatorWriteFlagSet(form string) (*validatorWriteFlags, func(args []string) ([]string, error), *string) {
	fs, cfgPath := newFlagSet(form)
	f := &validatorWriteFlags{
		asJSON:  fs.Bool("json", false, "print the API's body instead of text"),
		pattern: fs.String("pattern", "", "the Go RE2 regular expression matched against artifact names; `assurance validator patterns` suggests some"),
		script:  fs.String("script", "", "the NAME.local.sh file to upload"),
		set:     fs.String("backup-set", "", "scope it to this source/backup-set; without it the validator applies in every set"),
		dryRun:  fs.Bool("dry-run", false, "say what the pattern matches and what it collides with, and save nothing"),
	}
	return f, func(args []string) ([]string, error) { return parseFlagsAroundOperands(fs, args) }, cfgPath
}

func previewValidator(cfgPath, pattern, set, replacing string, asJSON bool) int {
	ctx := context.Background()
	svc, cleanup, err := openBackupServiceForRead(ctx, cfgPath, asJSON)
	defer cleanup()
	if err != nil {
		return fail(err)
	}
	p, err := svc.PreviewCustomValidator(ctx, pattern, set, replacing)
	if err != nil {
		return fail(err)
	}
	if asJSON {
		return emitWireJSON(service.CustomValidatorPreviewWire(p))
	}
	if !p.Valid {
		fmt.Printf("not a usable pattern: %s\n", p.Error)
		return exitFailure
	}
	fmt.Printf("matches %d backup(s)", p.Matches)
	if len(p.MatchSets) > 0 {
		fmt.Printf(" in %s", strings.Join(p.MatchSets, ", "))
	}
	fmt.Println()
	for _, s := range p.Sample {
		fmt.Printf("  %s\n", s)
	}
	if !p.Anchored {
		fmt.Println("the pattern is not anchored with ^...$, so it matches anywhere in a name")
	}
	for _, c := range p.Conflicts {
		fmt.Printf("collides with %s on %s: saving it would be refused\n", c.Validator, c.Example)
	}
	if len(p.Conflicts) > 0 {
		return exitFailure
	}
	return exitOK
}

func cmdAssuranceValidatorAdd(args []string) int {
	f, parse, cfgPath := validatorWriteFlagSet("assurance validator add")
	operands, err := parse(args)
	if err != nil {
		return exitUsage
	}
	if *f.pattern == "" {
		return usageError("assurance validator add: --pattern is required; `assurance validator patterns` suggests one")
	}
	if *f.dryRun {
		if *f.script != "" {
			return usageError("assurance validator add --dry-run judges the pattern and reads no script; drop --script")
		}
		if len(operands) > 1 {
			return usageError("assurance validator add --dry-run takes at most one operand, the name")
		}
		return previewValidator(*cfgPath, *f.pattern, *f.set, "", *f.asJSON)
	}
	if len(operands) != 1 {
		return usageError("assurance validator add: expected exactly one name, NAME in NAME.local.sh")
	}
	if *f.script == "" {
		return usageError("assurance validator add: --script FILE is required; `assurance validator default-script --output FILE` is a documented one to start from")
	}
	body, err := readScriptFile(*f.script)
	if err != nil {
		return fail(fmt.Errorf("reading --script: %w", err))
	}
	return writeValidator(*cfgPath, *f.asJSON, func(ctx context.Context, r customValidatorRoute) (service.CustomValidator, error) {
		return r.CreateCustomValidator(ctx, service.CreateCustomValidatorRequest{Name: operands[0], Pattern: *f.pattern, BackupSetID: *f.set, Script: body})
	})
}

func cmdAssuranceValidatorUpdate(args []string) int {
	f, parse, cfgPath := validatorWriteFlagSet("assurance validator update")
	operands, err := parse(args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("assurance validator update: expected exactly one name")
	}
	if *f.pattern == "" {
		return usageError("assurance validator update: --pattern is required; an update replaces the pattern and the scope together")
	}
	if *f.dryRun {
		if *f.script != "" {
			return usageError("assurance validator update --dry-run judges the pattern and reads no script; drop --script")
		}
		return previewValidator(*cfgPath, *f.pattern, *f.set, operands[0], *f.asJSON)
	}
	body := ""
	if *f.script != "" {
		if body, err = readScriptFile(*f.script); err != nil {
			return fail(fmt.Errorf("reading --script: %w", err))
		}
	}
	return writeValidator(*cfgPath, *f.asJSON, func(ctx context.Context, r customValidatorRoute) (service.CustomValidator, error) {
		return r.UpdateCustomValidator(ctx, service.CreateCustomValidatorRequest{Name: operands[0], Pattern: *f.pattern, BackupSetID: *f.set, Script: body})
	})
}

func cmdAssuranceValidatorRemove(args []string) int {
	fs, cfgPath := newFlagSet("assurance validator remove")
	asJSON := fs.Bool("json", false, "print the removed validator as the API's body")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("assurance validator remove: expected exactly one name")
	}
	return writeValidator(*cfgPath, *asJSON, func(ctx context.Context, r customValidatorRoute) (service.CustomValidator, error) {
		v, err := r.RemoveCustomValidator(ctx, operands[0])
		if err == nil && !*asJSON {
			fmt.Printf("removed %s; tests it decided fall through to the next validator in the order\n", v.Name)
		}
		return v, err
	})
}

func writeValidator(cfgPath string, asJSON bool, do func(context.Context, customValidatorRoute) (service.CustomValidator, error)) int {
	ctx := context.Background()
	// With --json, stdout is the API's body and nothing else: the mode line
	// and the startup record go to stderr.
	var out io.Writer = os.Stdout
	log := logger()
	if asJSON {
		out, log = os.Stderr, jsonLogger()
	}
	route, cleanup, err := openConfigWriteRouteTo(ctx, cfgPath, out)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	logStartup(ctx, log, app.BuildVersionInfo(version, commit))
	v, err := do(ctx, route)
	if err != nil {
		return fail(err)
	}
	if asJSON {
		return emitWireJSON(service.CustomValidatorWire(v))
	}
	printCustomValidator(v)
	return exitOK
}

// writeScriptOut prints a script, or writes it to a file that must not exist
// yet: replacing a file the operator already has is not what "download"
// means.
func writeScriptOut(s service.ValidatorScript, output string, asJSON bool) int {
	if asJSON {
		return emitWireJSON(service.ValidatorScriptWire(s))
	}
	if output == "" {
		fmt.Print(s.Content)
		return exitOK
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fail(fmt.Errorf("%s already exists; name a new file", output))
		}
		return fail(err)
	}
	if _, err := f.WriteString(s.Content); err != nil {
		_ = f.Close()
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes, sha256 %s)\n", output, s.SizeBytes, s.SHA256)
	return exitOK
}

func cmdAssuranceValidatorScript(args []string) int {
	fs, cfgPath := newFlagSet("assurance validator script")
	asJSON := fs.Bool("json", false, "print the API's ValidatorScriptResponse instead of the script")
	output := fs.String("output", "", "write the script to this new file instead of standard output")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("assurance validator script: expected exactly one name")
	}
	ctx := context.Background()
	svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, true)
	defer cleanup()
	if err != nil {
		return fail(err)
	}
	s, err := svc.CustomValidatorScript(ctx, operands[0])
	if err != nil {
		return fail(err)
	}
	return writeScriptOut(s, *output, *asJSON)
}

func cmdAssuranceValidatorPatterns(args []string) int {
	fs, cfgPath := newFlagSet("assurance validator patterns")
	asJSON := fs.Bool("json", false, "print the API's CustomValidatorPatternsResponse instead of text")
	set := fs.String("backup-set", "", "suggest from this source/backup-set's backups alone")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("assurance validator patterns takes no operands; narrow it with --backup-set")
	}
	ctx := context.Background()
	svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
	defer cleanup()
	if err != nil {
		return fail(err)
	}
	p, err := svc.CustomValidatorPatterns(ctx, *set)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return emitWireJSON(service.CustomValidatorPatternsWire(p))
	}
	if len(p.Patterns) == 0 {
		fmt.Println("no backups to suggest a pattern from")
	}
	for _, s := range p.Patterns {
		taken := ""
		if s.Validator != "" {
			taken = "  (validator " + s.Validator + ")"
		}
		fmt.Printf("%5d  %s  e.g. %s  in %s%s\n", s.Matches, s.Pattern, s.Example, strings.Join(s.MatchSets, ", "), taken)
	}
	return exitOK
}

// cmdAssuranceValidatorDefaultScript reads no configuration: the script is
// compiled in, so it works before anything is configured.
func cmdAssuranceValidatorDefaultScript(args []string) int {
	fs, _ := newFlagSet("assurance validator default-script")
	asJSON := fs.Bool("json", false, "print the API's ValidatorScriptResponse instead of the script")
	output := fs.String("output", "", "write the script to this new file instead of standard output")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("assurance validator default-script takes no operands")
	}
	return writeScriptOut(service.DefaultCustomValidatorScript(), *output, *asJSON)
}

// The wire to service half for the routed writes.
func customValidatorFromWire(w apicontract.CustomValidator) service.CustomValidator {
	v := service.CustomValidator{
		Name: w.Name, Pattern: w.Pattern, Anchored: w.Anchored, BackupSetID: w.BackupSetID, Scope: w.Scope,
		ScriptFile: w.ScriptFile, ScriptPresent: w.ScriptPresent, ScriptSHA256: w.ScriptSha256, ScriptSize: w.ScriptSizeBytes,
		Matches: int(w.MatchCount), MatchSets: append([]string{}, w.MatchSets...), ExampleName: w.ExampleName,
	}
	if t, err := time.Parse(time.RFC3339, w.UploadedAt); err == nil {
		v.UploadedAt = t
	}
	if r := w.LastResult; r != nil {
		at, _ := time.Parse(time.RFC3339, r.At)
		v.LastResult = &service.CustomValidatorResult{State: r.State, At: at, Artifact: r.ArtifactID}
	}
	return v
}

// CreateCustomValidator is POST /assurance/validators.
func (r *engineRoute) CreateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error) {
	w, err := r.client.CreateCustomValidator(ctx, apicontract.CreateCustomValidatorRequest{
		Name: req.Name, Pattern: req.Pattern, BackupSetID: req.BackupSetID, Script: req.Script,
	})
	if err != nil {
		return service.CustomValidator{}, err
	}
	return customValidatorFromWire(w), nil
}

// UpdateCustomValidator is PUT /assurance/validators/{name}.
func (r *engineRoute) UpdateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error) {
	w, err := r.client.UpdateCustomValidator(ctx, req.Name, apicontract.UpdateCustomValidatorRequest{
		Pattern: req.Pattern, BackupSetID: req.BackupSetID, Script: req.Script,
	})
	if err != nil {
		return service.CustomValidator{}, err
	}
	return customValidatorFromWire(w), nil
}

// RemoveCustomValidator is DELETE /assurance/validators/{name}.
func (r *engineRoute) RemoveCustomValidator(ctx context.Context, name string) (service.CustomValidator, error) {
	w, err := r.client.RemoveCustomValidator(ctx, name)
	if err != nil {
		return service.CustomValidator{}, err
	}
	return customValidatorFromWire(w), nil
}
