package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/service"
)

// `retnd assurance validators ...` (#1203): custom recovery-test validators,
// each an operator's NAME.local.sh attached to an artifact-name pattern, from
// a terminal, doing what Settings > Assurance does through
// /api/v1/assurance/validators.
//
// # Why under `assurance`, and why the plural
//
// The top-level `validators` verb is already taken by the catalog of ingest
// validators compiled into this binary (#1152), a different contract that
// runs at ingest and not in a recovery test. `assurance` already groups the
// recovery-test and scrub verbs and its dispatcher hands each subverb its own
// flags. The noun is `validators`, the plural, because that is the route's
// (/assurance/validators), the issue's, and the API reference's: an operator
// reading the activity feed's command line should find the same word in all
// of them. The singular that shipped in the first draft is not a verb.
//
// # Where each form goes
//
// Every form that touches a deployment's validators goes to whichever process
// serves that deployment. With nothing serving, list, script, patterns and
// add --dry-run answer from this host's configuration and journal, and the
// writes land in the file. With an engine serving and RETND_API_URL naming it,
// the reads ask the engine (GET /assurance/validators and its siblings) and
// the writes are handed to it through openConfigWriteRoute; a write beside an
// engine this command cannot reach is refused with the file untouched, and a
// read beside one it cannot reach says so and answers from this host's files,
// which is the only thing it has. Routed, add and update answer the engine's
// password step-up with the administrator's password this command already
// signs in with. default-script reads nothing at all: the script is compiled
// in.
//
// # What update may leave out
//
// Nothing about where it goes. An update replaces the validator whole, so it
// has to say its scope: --backup-set S, or --global for every set. Leaving
// both out used to turn a set-scoped validator global, which widens what a
// script runs against by omission. The script may be left out: the command
// then asks for the stored script's hash and sends that as the thing to keep,
// so a script replaced by somebody else in between is a refusal and not a
// silent keep.

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

func cmdAssuranceValidators(args []string) int {
	// The form is the first argument that is one, wherever the flags sit,
	// as `assurance` itself finds its verb.
	for i, a := range args {
		if verb, ok := assuranceValidatorVerbs[a]; ok {
			return verb(append(append([]string{}, args[:i]...), args[i+1:]...))
		}
	}
	return usageError("assurance validators: expected one of %s", assuranceValidatorForms)
}

// customValidatorReader is what the reads need. *service.BackupService answers
// from this host's files and *engineRoute from the serving engine, and a read
// does not know which.
type customValidatorReader interface {
	ListCustomValidators(ctx context.Context, f service.CustomValidatorsFilter) (service.CustomValidators, error)
	CustomValidatorScript(ctx context.Context, name string) (service.ValidatorScript, error)
	CustomValidatorPatterns(ctx context.Context, backupSetID string) (service.CustomValidatorPatterns, error)
	PreviewCustomValidator(ctx context.Context, pattern, backupSetID, replacing string) (service.CustomValidatorPreview, error)
}

// customValidatorRoute is the custom validators' half of configWriteRoute: the
// reads (an update needs the stored script's hash from the process it is
// about to write to) and the three writes. *service.BackupService satisfies it
// as it stands.
type customValidatorRoute interface {
	customValidatorReader
	CreateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error)
	UpdateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error)
	RemoveCustomValidator(ctx context.Context, name string) (service.CustomValidator, error)
}

// openValidatorReader returns where a validators read goes: the serving
// engine when one serves this deployment and this command has a route to it,
// this host's own configuration and journal otherwise. It never takes the
// configuration-write claim; nothing here writes.
//
// asJSON sends the service's structured log to standard error, because
// standard output is then a document (or, for `script`, a script).
func openValidatorReader(ctx context.Context, cfgPath string, asJSON bool) (customValidatorReader, func(), error) {
	if engine, err := detectRunningEngine(cfgPath); err == nil && engine != nil {
		route, address, err := attachToEngine(ctx, engine)
		if err != nil {
			return nil, func() {}, err
		}
		if route != nil {
			fmt.Fprintf(os.Stderr, cliecho.Binary+": reading from the engine serving this deployment at %s\n", address)
			return route, func() {}, nil
		}
		fmt.Fprintf(os.Stderr, cliecho.Binary+": note: another process serves this deployment (%s) and %s does not name it, so this answers from this host's configuration and journal, which that process may not be using\n",
			engine.StateDatabase, apiURLEnv)
	}
	svc, cleanup, err := openBackupServiceForRead(ctx, cfgPath, asJSON)
	if err != nil {
		return nil, cleanup, err
	}
	return svc, cleanup, nil
}

func cmdAssuranceValidatorList(args []string) int {
	fs, cfgPath := newFlagSet("assurance validators list")
	asJSON := fs.Bool("json", false, "print the API's ListCustomValidatorsResponse instead of text")
	set := fs.String("backup-set", "", "only the validators that apply to this source/backup-set, in the order a test tries them")
	artifact := fs.String("artifact", "", "also say which validator a recovery test of this backup (source/set/name) would run; narrows to that backup's set")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("assurance validators list takes no operands; narrow it with --backup-set")
	}
	ctx := context.Background()
	reader, cleanup, err := openValidatorReader(ctx, *cfgPath, *asJSON)
	defer cleanup()
	if err != nil {
		return fail(err)
	}
	l, err := reader.ListCustomValidators(ctx, service.CustomValidatorsFilter{BackupSetID: *set, ArtifactID: *artifact})
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return emitWireJSON(service.CustomValidatorsWire(l))
	}
	printCustomValidatorList(os.Stdout, l)
	return exitOK
}

// listingText makes a string safe to print in a listing. A backup's name is
// whatever the remote called it and a pattern is whatever somebody typed, and
// neither gets to move the cursor, repaint the screen, set the window title
// or split a line: control characters, and the invisible formatting
// characters that reorder text, are shown as their escapes instead.
func listingText(s string) string {
	clean := true
	for _, r := range s {
		if unsafeListingRune(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !unsafeListingRune(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			fmt.Fprintf(&b, "\\u%04x", r)
		}
	}
	return b.String()
}

func unsafeListingRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar
}

func listingTexts(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = listingText(s)
	}
	return out
}

// say and sayln write a line of a listing. A listing to a terminal or a buffer
// has nothing useful to do with a write error, and the callers that care
// (standard output) already see it in the exit of the process, so it is
// dropped here once instead of at every line.
func say(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func sayln(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }

// printCustomValidatorList is the text form of the list.
func printCustomValidatorList(w io.Writer, l service.CustomValidators) {
	if !l.Available {
		say(w, "uploads: unavailable: %s\n", listingText(l.UnavailableReason))
	}
	if l.DirectoryRefused != "" {
		say(w, "directory refused: %s\n  no validator script can run until that is fixed\n", listingText(l.DirectoryRefused))
	}
	if !l.RunnerConfigured {
		sayln(w, "note: no host workflow runner is configured, so a validator is stored and cannot run yet")
	}
	if len(l.Validators) == 0 {
		sayln(w, "no custom validators")
	}
	for _, v := range l.Validators {
		printCustomValidator(w, v)
	}
	for _, a := range l.Ambiguities {
		say(w, "ambiguous: %s in %s: the patterns of %s all match it at one scope, so a recovery test of it ends could_not_complete; narrow one of them\n",
			listingText(a.Example), listingText(a.BackupSetID), strings.Join(listingTexts(a.Validators), ", "))
	}
	if r := l.Resolution; r != nil {
		switch {
		case len(r.Ambiguous) > 0:
			say(w, "%s: %s all match at one scope, so a test ends could_not_complete\n", listingText(r.ArtifactID), strings.Join(listingTexts(r.Ambiguous), ", "))
		case r.Validator != "":
			say(w, "%s: a recovery test runs %s\n", listingText(r.ArtifactID), listingText(r.Validator))
		default:
			say(w, "%s: no pattern applies; %s decides\n", listingText(r.ArtifactID), listingText(r.Fallback))
		}
	}
}

func printCustomValidator(w io.Writer, v service.CustomValidator) {
	scope := "every set"
	if v.BackupSetID != "" {
		scope = listingText(v.BackupSetID)
	}
	say(w, "%s  %s  %s\n", listingText(v.Name), scope, listingText(v.Pattern))
	script := "MISSING, so a test it applies to cannot complete"
	if v.ScriptPresent {
		script = fmt.Sprintf("%d bytes, sha256 %s, written %s", v.ScriptSize, listingText(v.ScriptSHA256), v.UploadedAt.Format(time.RFC3339))
	}
	say(w, "  script %s: %s\n", listingText(v.ScriptFile), script)
	say(w, "  env %s\n", listingText(v.Env))
	switch v.ScriptState {
	case service.ScriptStatePinned:
		say(w, "  pinned in config.yaml at sha256 %s\n", listingText(v.ScriptPinnedSHA256))
	case service.ScriptStateUnpinned:
		sayln(w, "  not pinned in config.yaml (written before pinning); it runs as it did, and saving the validator again pins it")
	case service.ScriptStateEdited:
		say(w, "  EDITED ON DISK: config.yaml pins sha256 %s, so a test this validator applies to ends could_not_complete without running it; read the script and send it again (update --script) to approve it\n", listingText(v.ScriptPinnedSHA256))
	}
	if v.Matches > 0 {
		say(w, "  matches %d backup(s) in %s, e.g. %s\n", v.Matches, strings.Join(listingTexts(v.MatchSets), ", "), listingText(v.ExampleName))
	} else {
		sayln(w, "  matches no backup yet")
	}
	if !v.Anchored {
		sayln(w, "  the pattern is not anchored with ^...$, so it matches anywhere in a name")
	}
	if r := v.LastResult; r != nil {
		say(w, "  last test: %s at %s (%s)\n", listingText(r.State), r.At.Format(time.RFC3339), listingText(r.Artifact))
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
	global  *bool
	env     *string
	dryRun  *bool
}

func validatorWriteFlagSet(form string) (*validatorWriteFlags, func(args []string) ([]string, error), *string) {
	fs, cfgPath := newFlagSet(form)
	f := &validatorWriteFlags{
		asJSON:  fs.Bool("json", false, "print the API's body instead of text"),
		pattern: fs.String("pattern", "", "the Go RE2 regular expression matched against artifact names; `assurance validators patterns` suggests some"),
		script:  fs.String("script", "", "the NAME.local.sh file to upload"),
		set:     fs.String("backup-set", "", "scope it to this source/backup-set"),
		global:  fs.Bool("global", false, "apply it in every backup set"),
		env:     fs.String("env", "", "the environment its script starts with: minimal (the default; the RECOVERY_TEST_* variables, RETND_* facts and PATH) or workflow (the backup set's workflow environment too, secrets included)"),
		dryRun:  fs.Bool("dry-run", false, "say what the pattern matches and what it collides with, and save nothing"),
	}
	return f, func(args []string) ([]string, error) { return parseFlagsAroundOperands(fs, args) }, cfgPath
}

// checkValidatorEnv refuses an --env that is not exactly one of the two
// values before anything is read or written; the service refuses it again, so
// this is only the earlier and friendlier of two doors.
func checkValidatorEnv(form, env string) (int, bool) {
	if env == "" || env == "minimal" || env == "workflow" {
		return exitOK, true
	}
	return usageError("%s: --env %q must be exactly minimal or workflow", form, env), false
}

func previewValidator(cfgPath, pattern, set, replacing string, asJSON bool) int {
	ctx := context.Background()
	reader, cleanup, err := openValidatorReader(ctx, cfgPath, asJSON)
	defer cleanup()
	if err != nil {
		return fail(err)
	}
	p, err := reader.PreviewCustomValidator(ctx, pattern, set, replacing)
	if err != nil {
		return fail(err)
	}
	if asJSON {
		return emitWireJSON(service.CustomValidatorPreviewWire(p))
	}
	if !p.Valid {
		fmt.Printf("not a usable pattern: %s\n", listingText(p.Error))
		return exitFailure
	}
	fmt.Printf("matches %d backup(s)", p.Matches)
	if len(p.MatchSets) > 0 {
		fmt.Printf(" in %s", strings.Join(listingTexts(p.MatchSets), ", "))
	}
	fmt.Println()
	for _, s := range p.Sample {
		fmt.Printf("  %s\n", listingText(s))
	}
	if !p.Anchored {
		fmt.Println("the pattern is not anchored with ^...$, so it matches anywhere in a name")
	}
	for _, c := range p.Conflicts {
		fmt.Printf("collides with %s on %s: saving it would be refused\n", listingText(c.Validator), listingText(c.Example))
	}
	if len(p.Conflicts) > 0 {
		return exitFailure
	}
	return exitOK
}

func cmdAssuranceValidatorAdd(args []string) int {
	f, parse, cfgPath := validatorWriteFlagSet("assurance validators add")
	operands, err := parse(args)
	if err != nil {
		return exitUsage
	}
	if *f.pattern == "" {
		return usageError("assurance validators add: --pattern is required; `assurance validators patterns` suggests one")
	}
	if *f.global && *f.set != "" {
		return usageError("assurance validators add: --global and --backup-set say opposite things; give one")
	}
	if code, ok := checkValidatorEnv("assurance validators add", *f.env); !ok {
		return code
	}
	if *f.dryRun {
		if *f.script != "" {
			return usageError("assurance validators add --dry-run judges the pattern and reads no script; drop --script")
		}
		if len(operands) > 1 {
			return usageError("assurance validators add --dry-run takes at most one operand, the name")
		}
		return previewValidator(*cfgPath, *f.pattern, *f.set, "", *f.asJSON)
	}
	if len(operands) != 1 {
		return usageError("assurance validators add: expected exactly one name, NAME in NAME.local.sh")
	}
	if *f.script == "" {
		return usageError("assurance validators add: --script FILE is required; `assurance validators default-script --output FILE` is a documented one to start from")
	}
	body, err := readScriptFile(*f.script)
	if err != nil {
		return fail(fmt.Errorf("reading --script: %w", err))
	}
	return writeValidator(*cfgPath, *f.asJSON, func(ctx context.Context, r customValidatorRoute) (service.CustomValidator, error) {
		return r.CreateCustomValidator(ctx, service.CreateCustomValidatorRequest{Name: operands[0], Pattern: *f.pattern, BackupSetID: *f.set, Script: body, Env: *f.env})
	})
}

func cmdAssuranceValidatorUpdate(args []string) int {
	f, parse, cfgPath := validatorWriteFlagSet("assurance validators update")
	operands, err := parse(args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("assurance validators update: expected exactly one name")
	}
	if *f.pattern == "" {
		return usageError("assurance validators update: --pattern is required; an update replaces the validator whole")
	}
	switch {
	case *f.global && *f.set != "":
		return usageError("assurance validators update: --global and --backup-set say opposite things; give one")
	case !*f.global && *f.set == "":
		return usageError("assurance validators update: say where it applies, --backup-set SOURCE/SET or --global. An update replaces the validator whole, and leaving the scope out used to widen a set-scoped validator to every set")
	}
	if code, ok := checkValidatorEnv("assurance validators update", *f.env); !ok {
		return code
	}
	if *f.dryRun {
		if *f.script != "" {
			return usageError("assurance validators update --dry-run judges the pattern and reads no script; drop --script")
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
		req := service.CreateCustomValidatorRequest{Name: operands[0], Pattern: *f.pattern, BackupSetID: *f.set, Script: body, Env: *f.env}
		if body == "" {
			// Keep the stored script, and say which one: read from the very
			// process the update is about to go to, so a script replaced in
			// between is refused and not kept.
			stored, err := r.CustomValidatorScript(ctx, operands[0])
			if err != nil {
				return service.CustomValidator{}, err
			}
			req.ScriptSHA256 = stored.SHA256
		}
		return r.UpdateCustomValidator(ctx, req)
	})
}

func cmdAssuranceValidatorRemove(args []string) int {
	fs, cfgPath := newFlagSet("assurance validators remove")
	asJSON := fs.Bool("json", false, "print the removed validator as the API's body")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("assurance validators remove: expected exactly one name")
	}
	return writeValidator(*cfgPath, *asJSON, func(ctx context.Context, r customValidatorRoute) (service.CustomValidator, error) {
		v, err := r.RemoveCustomValidator(ctx, operands[0])
		if err == nil && !*asJSON {
			fmt.Printf("removed %s; its script was moved to the recovery-test-validators.superseded directory beside the stage directory, and tests it decided fall through to the next validator in the order\n", listingText(v.Name))
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
	printCustomValidator(os.Stdout, v)
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
	fs, cfgPath := newFlagSet("assurance validators script")
	asJSON := fs.Bool("json", false, "print the API's ValidatorScriptResponse instead of the script")
	output := fs.String("output", "", "write the script to this new file instead of standard output")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("assurance validators script: expected exactly one name")
	}
	ctx := context.Background()
	reader, cleanup, err := openValidatorReader(ctx, *cfgPath, true)
	defer cleanup()
	if err != nil {
		return fail(err)
	}
	s, err := reader.CustomValidatorScript(ctx, operands[0])
	if err != nil {
		return fail(err)
	}
	return writeScriptOut(s, *output, *asJSON)
}

func cmdAssuranceValidatorPatterns(args []string) int {
	fs, cfgPath := newFlagSet("assurance validators patterns")
	asJSON := fs.Bool("json", false, "print the API's CustomValidatorPatternsResponse instead of text")
	set := fs.String("backup-set", "", "suggest from this source/backup-set's backups alone")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("assurance validators patterns takes no operands; narrow it with --backup-set")
	}
	ctx := context.Background()
	reader, cleanup, err := openValidatorReader(ctx, *cfgPath, *asJSON)
	defer cleanup()
	if err != nil {
		return fail(err)
	}
	p, err := reader.CustomValidatorPatterns(ctx, *set)
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
			taken = "  (validator " + listingText(s.Validator) + ")"
		}
		fmt.Printf("%5d  %s  e.g. %s  in %s%s\n", s.Matches, listingText(s.Pattern), listingText(s.Example), strings.Join(listingTexts(s.MatchSets), ", "), taken)
	}
	return exitOK
}

// cmdAssuranceValidatorDefaultScript reads no configuration: the script is
// compiled in, so it works before anything is configured.
func cmdAssuranceValidatorDefaultScript(args []string) int {
	fs, _ := newFlagSet("assurance validators default-script")
	asJSON := fs.Bool("json", false, "print the API's ValidatorScriptResponse instead of the script")
	output := fs.String("output", "", "write the script to this new file instead of standard output")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("assurance validators default-script takes no operands")
	}
	return writeScriptOut(service.DefaultCustomValidatorScript(), *output, *asJSON)
}

// The wire to service half, for the routed forms.
func customValidatorFromWire(w apicontract.CustomValidator) service.CustomValidator {
	v := service.CustomValidator{
		Name: w.Name, Pattern: w.Pattern, Anchored: w.Anchored, BackupSetID: w.BackupSetID, Scope: w.Scope, Env: w.Env,
		ScriptFile: w.ScriptFile, ScriptPresent: w.ScriptPresent, ScriptSHA256: w.ScriptSha256, ScriptSize: w.ScriptSizeBytes,
		ScriptPinnedSHA256: w.PinnedSha256, ScriptState: w.ScriptState,
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

func customValidatorsFromWire(w apicontract.ListCustomValidatorsResponse) service.CustomValidators {
	out := service.CustomValidators{
		Validators: make([]service.CustomValidator, 0, len(w.Validators)),
		Directory:  w.Directory, WorkflowRoot: w.WorkflowRoot, Available: w.Available, UnavailableReason: w.UnavailableReason,
		WorkflowRootDefault: w.WorkflowRootDefault, DirectoryRefused: w.DirectoryRefused, RunnerConfigured: w.RunnerConfigured,
		MaxValidators: int(w.MaxValidators), MaxScriptBytes: w.MaxScriptBytes, MaxPatternBytes: int(w.MaxPatternBytes),
	}
	for _, v := range w.Validators {
		out.Validators = append(out.Validators, customValidatorFromWire(v))
	}
	for _, a := range w.Ambiguities {
		out.Ambiguities = append(out.Ambiguities, service.CustomValidatorAmbiguity{
			BackupSetID: a.BackupSetID, Validators: append([]string{}, a.Validators...), Example: a.Example,
		})
	}
	if r := w.Resolution; r != nil {
		out.Resolution = &service.CustomValidatorResolution{
			ArtifactID: r.ArtifactID, Validator: r.Validator, Ambiguous: append([]string{}, r.Ambiguous...), Fallback: r.Fallback,
		}
	}
	return out
}

// ListCustomValidators is GET /assurance/validators.
func (r *engineRoute) ListCustomValidators(ctx context.Context, f service.CustomValidatorsFilter) (service.CustomValidators, error) {
	w, err := r.client.ListCustomValidators(ctx, f.BackupSetID, f.ArtifactID)
	if err != nil {
		return service.CustomValidators{}, err
	}
	return customValidatorsFromWire(w), nil
}

// CustomValidatorScript is GET /assurance/validators/{name}/script.
func (r *engineRoute) CustomValidatorScript(ctx context.Context, name string) (service.ValidatorScript, error) {
	w, err := r.client.CustomValidatorScript(ctx, name)
	if err != nil {
		return service.ValidatorScript{}, err
	}
	return service.ValidatorScript{FileName: w.FileName, SHA256: w.Sha256, SizeBytes: w.SizeBytes, Content: w.Content}, nil
}

// CustomValidatorPatterns is GET /assurance/validator-patterns.
func (r *engineRoute) CustomValidatorPatterns(ctx context.Context, backupSetID string) (service.CustomValidatorPatterns, error) {
	w, err := r.client.CustomValidatorPatterns(ctx, backupSetID)
	if err != nil {
		return service.CustomValidatorPatterns{}, err
	}
	out := service.CustomValidatorPatterns{BackupSetID: w.BackupSetID, Patterns: make([]service.CustomValidatorPattern, 0, len(w.Patterns))}
	for _, p := range w.Patterns {
		out.Patterns = append(out.Patterns, service.CustomValidatorPattern{
			Pattern: p.Pattern, Example: p.Example, Matches: int(p.MatchCount), MatchSets: append([]string{}, p.MatchSets...), Validator: p.Validator,
		})
	}
	return out, nil
}

// PreviewCustomValidator is POST /assurance/validator-preview.
func (r *engineRoute) PreviewCustomValidator(ctx context.Context, pattern, backupSetID, replacing string) (service.CustomValidatorPreview, error) {
	w, err := r.client.PreviewCustomValidator(ctx, apicontract.PreviewCustomValidatorRequest{Pattern: pattern, BackupSetID: backupSetID, Replacing: replacing})
	if err != nil {
		return service.CustomValidatorPreview{}, err
	}
	out := service.CustomValidatorPreview{
		Valid: w.Valid, Error: w.Error, Anchored: w.Anchored, Matches: int(w.MatchCount),
		MatchSets: append([]string{}, w.MatchSets...), Sample: append([]string{}, w.Sample...),
	}
	for _, c := range w.Conflicts {
		out.Conflicts = append(out.Conflicts, service.CustomValidatorConflict{Validator: c.Validator, Example: c.Example})
	}
	return out, nil
}

// CreateCustomValidator is POST /assurance/validators.
func (r *engineRoute) CreateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error) {
	w, err := r.client.CreateCustomValidator(ctx, apicontract.CreateCustomValidatorRequest{
		Name: req.Name, Pattern: req.Pattern, BackupSetID: req.BackupSetID, Script: req.Script, Env: req.Env,
	})
	if err != nil {
		return service.CustomValidator{}, err
	}
	return customValidatorFromWire(w), nil
}

// UpdateCustomValidator is PUT /assurance/validators/{name}.
func (r *engineRoute) UpdateCustomValidator(ctx context.Context, req service.CreateCustomValidatorRequest) (service.CustomValidator, error) {
	w, err := r.client.UpdateCustomValidator(ctx, req.Name, apicontract.UpdateCustomValidatorRequest{
		Pattern: req.Pattern, BackupSetID: req.BackupSetID, Script: req.Script, ScriptSha256: req.ScriptSHA256, Env: req.Env,
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
