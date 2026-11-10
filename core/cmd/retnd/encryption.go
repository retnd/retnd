package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/service"
)

// `retnd encryption ...` (#993, EPIC N): the terminal's whole control of
// retained-copy encryption, and the twin of every /api/v1/encryption route.
//
//	encryption policy [patch|reset]            deployment mode and key provider
//	encryption backup-set <source/set>         one set's own mode and provider
//	encryption key list|create|rotate|retire|export|confirm|import
//	encryption provider test                   does a key provider resolve now
//	encryption copies <source/set/artifact>    which copies are encrypted
//	encryption verify <source/set/artifact>    decrypt-verify, a content check
//	encryption encrypt-existing <source/set>   encrypt plaintext copies that already exist
//	encryption reencrypt --suite|--key|--deprecated   rewrite copies under the active suite and key
//	encryption decrypt <object> ...            open one copy to a file (#1289)
//	encryption suites                          the encryption suites this build has (#1315)
//
// # One author for every refusal
//
// Every verb here calls a core/service method that the route of the same
// name also calls, directly when nothing serves the deployment and through
// the engine when one does (openConfigWriteRoute), so `required` with no
// provider, a retirement a live copy blocks and a key that will not resolve
// are refused with one sentence and one exit status on both routes
// (routeparity_test.go). The reads are this host's own file and journal, as
// `backup-set retention` is.
//
// # Nothing here prints a key
//
// key list, key create and the rest print ids, timestamps, a status and
// whether the key resolves. A key reference is declared with --external-file,
// --external-env or --external-command and reads back as its description.
// `key export` is the one verb that moves key material, and it writes it to a
// file it creates (0600, never over an existing one) and prints the receipt.
// It has no stdout form on purpose.
//
// # Two verbs with no API route (#1289)
//
// `decrypt` and `key import` are host-local on purpose and have no twin under
// /api/v1/encryption. Both take key material as input, and a request body is
// a place material does not belong; `decrypt` also has to work where no
// deployment exists at all, which is the reason it exists. encryptiondecrypt.go
// has the rest.
//
// # --json
//
// --json prints the object the API returns for the same call and nothing
// else on standard output; the log lines this process writes go to standard
// error for that run. The one exception is key export, whose JSON is the
// receipt: the bundle only ever goes to the file.
var encryptionVerbs = map[string]func(args []string) int{
	"policy":     cmdEncryptionPolicy,
	"backup-set": cmdEncryptionBackupSet,
	"key":        cmdEncryptionKey,
	"provider":   cmdEncryptionProvider,
	"copies":     cmdEncryptionCopies,
	"verify":     cmdEncryptionVerify,
	"decrypt":    cmdEncryptionDecrypt,
	"suites":     cmdEncryptionSuites,

	"encrypt-existing": cmdEncryptionEncryptExisting,
	"reencrypt":        cmdEncryptionReencrypt,
}

func cmdEncryption(args []string) int {
	for i, a := range args {
		if verb, ok := encryptionVerbs[a]; ok {
			return verb(append(append([]string{}, args[:i]...), args[i+1:]...))
		}
	}
	return usageError("encryption: expected `encryption policy`, `encryption backup-set <source/set>`, `encryption key ...`, `encryption provider test`, `encryption copies <source/set/artifact>`, `encryption verify <source/set/artifact>`, `encryption encrypt-existing <source/set> --to <medium>`, `encryption reencrypt --suite <id> | --key <id> | --deprecated [--to <medium>]`, `encryption decrypt <object> ...` or `encryption suites`")
}

// providerFlags are the flags that declare a key provider, shared by the
// deployment's patch and a set's own block.
type encryptionProviderFlags struct {
	managed *bool
	file    *string
	env     *string
	command *string
}

func declareEncryptionProviderFlags(fs *flag.FlagSet) encryptionProviderFlags {
	return encryptionProviderFlags{
		managed: fs.Bool("managed", false, "the key is the one this deployment manages itself"),
		file:    fs.String("external-file", "", "the key is read from this file, which must be 0600 in a directory only you can write"),
		env:     fs.String("external-env", "", "the key is read from this environment variable"),
		command: fs.String("external-command", "", "the key is the standard output of this command (split on spaces, no quoting; it is run without a shell)"),
	}
}

// provider builds the provider the flags name, or nil when none was passed.
// It does not decide whether what was named is legal: "exactly one" is
// config.Validate's, so the refusal is the one a hand-edited file gets.
func (p encryptionProviderFlags) provider(fs *flag.FlagSet) *apicontract.EncryptionKeyProvider {
	var out *apicontract.EncryptionKeyProvider
	touch := func() *apicontract.EncryptionKeyProvider {
		if out == nil {
			out = &apicontract.EncryptionKeyProvider{}
		}
		return out
	}
	external := func() *apicontract.EncryptionKeyReference {
		o := touch()
		if o.External == nil {
			o.External = &apicontract.EncryptionKeyReference{}
		}
		return o.External
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "managed":
			touch().Managed = *p.managed
		case "external-file":
			external().File = *p.file
		case "external-env":
			external().Env = *p.env
		case "external-command":
			external().Command = strings.Fields(*p.command)
		}
	})
	return out
}

// openEncryptionWrite opens the route a write goes through: the engine when
// one serves this deployment and a route to it was named, this process
// otherwise. With --json the log goes to standard error so the document is
// the only thing on standard output.
func openEncryptionWrite(ctx context.Context, cfgPath string, asJSON bool) (configWriteRoute, func(), error) {
	if asJSON {
		return openConfigWriteRouteTo(ctx, cfgPath, os.Stderr)
	}
	return openConfigWriteRoute(ctx, cfgPath)
}

// encryptionAnswer prints v as the API's own object, or runs text.
func encryptionAnswer(asJSON bool, v any, text func()) int {
	if asJSON {
		return emitWireJSON(v)
	}
	text()
	return exitOK
}

func cmdEncryptionPolicy(args []string) int {
	fs, cfgPath := newFlagSet("encryption policy")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	mode := fs.String("mode", "", "patch: off or required")
	suite := fs.String("suite", "", "patch: default, or a suite id to pin (see `encryption suites`)")
	prov := declareEncryptionProviderFlags(fs)
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	sub := ""
	switch len(operands) {
	case 0:
	case 1:
		sub = operands[0]
	default:
		return usageError("encryption policy: expected `policy`, `policy patch` or `policy reset`")
	}
	ctx := context.Background()

	switch sub {
	case "":
		if len(passedEncryptionFlags(fs, "mode", "suite", "managed", "external-file", "external-env", "external-command")) > 0 {
			return usageError("encryption policy: the flags change the policy; write `encryption policy patch` to change it")
		}
		svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		got, err := svc.EncryptionPolicy(ctx)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, got, func() { printEncryptionPolicy(got) })
	case "patch":
		var req apicontract.EncryptionPolicyPatch
		req.Mode = *mode
		req.Suite = *suite
		req.KeyProvider = prov.provider(fs)
		if req.Mode == "" && req.Suite == "" && req.KeyProvider == nil {
			return usageError("encryption policy patch: name what to change: --mode, --suite, or one of --managed, --external-file, --external-env, --external-command")
		}
		route, cleanup, err := openEncryptionWrite(ctx, *cfgPath, *asJSON)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		got, err := route.PatchEncryptionPolicy(ctx, req)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, got, func() { printEncryptionPolicy(got) })
	case "reset":
		if len(passedEncryptionFlags(fs, "mode", "suite", "managed", "external-file", "external-env", "external-command")) > 0 {
			return usageError("encryption policy reset: removes the deployment's block, so it takes no policy flags")
		}
		route, cleanup, err := openEncryptionWrite(ctx, *cfgPath, *asJSON)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		got, err := route.ResetEncryptionPolicy(ctx)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, got, func() { printEncryptionPolicy(got) })
	}
	return usageError("encryption policy: %q is not `patch` or `reset`", sub)
}

// passedEncryptionFlags returns the named flags that were actually passed.
func passedEncryptionFlags(fs *flag.FlagSet, names ...string) []string {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var got []string
	fs.Visit(func(f *flag.Flag) {
		if want[f.Name] {
			got = append(got, f.Name)
		}
	})
	return got
}

func cmdEncryptionBackupSet(args []string) int {
	fs, cfgPath := newFlagSet("encryption backup-set")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	mode := fs.String("mode", "", "inherit, off or required")
	reset := fs.Bool("reset", false, "remove this set's own block so it follows the deployment's policy again")
	prov := declareEncryptionProviderFlags(fs)
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 {
		return usageError("encryption backup-set: expected exactly one backup set id, source/name")
	}
	id := operands[0]
	if !isBackupSetID(id) {
		return usageError("encryption backup-set: %q is not a backup set id; a backup set id is exactly source/name", id)
	}
	provider := prov.provider(fs)
	writes := *mode != "" || provider != nil
	if *reset && writes {
		return usageError("encryption backup-set: --reset removes this set's own block and --mode and the provider flags write one; pass one or the other")
	}
	ctx := context.Background()

	if !*reset && !writes {
		svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		policy, err := svc.EncryptionPolicy(ctx)
		if err != nil {
			return fail(err)
		}
		for _, s := range policy.Sets {
			if s.BackupSetID == id {
				return encryptionAnswer(*asJSON, s, func() { printEncryptionSet("", s) })
			}
		}
		return fail(fmt.Errorf("%w: %s", service.ErrBackupSetNotFound, id))
	}

	route, cleanup, err := openEncryptionWrite(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	var got apicontract.EncryptionSetPolicy
	if *reset {
		got, err = route.ClearBackupSetEncryption(ctx, id)
	} else {
		if *mode == "" {
			return usageError("encryption backup-set: a provider needs --mode required alongside it")
		}
		got, err = route.SetBackupSetEncryption(ctx, id, apicontract.EncryptionOverride{Mode: *mode, KeyProvider: provider})
	}
	if err != nil {
		return fail(err)
	}
	return encryptionAnswer(*asJSON, got, func() { printEncryptionSet("", got) })
}

func cmdEncryptionKey(args []string) int {
	fs, cfgPath := newFlagSet("encryption key")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	out := fs.String("out", "", "export: the file to write the bundle to; created 0600, and never over a file that exists")
	digest := fs.String("digest", "", "confirm: the digest the export printed; omitted confirms the newest export of the key")
	bundlePath := fs.String("bundle", "", "import: the recovery bundle to register the key from")
	suite := fs.String("suite", "", "create, rotate: the suite to mint the key under; omitted, the policy's pinned suite, else the active key's suite (rotate) or the build default (create)")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) == 0 {
		return usageError("encryption key: expected `key list`, `key create`, `key rotate`, `key retire <id>`, `key export <id> --out FILE`, `key confirm <id>` or `key import --bundle FILE`")
	}
	sub, rest := operands[0], operands[1:]
	needsID := sub == "retire" || sub == "export" || sub == "confirm"
	switch {
	case sub == "list" || sub == "create" || sub == "rotate" || sub == "import":
		if len(rest) != 0 {
			return usageError("encryption key %s: takes no operand", sub)
		}
	case needsID:
		if len(rest) != 1 {
			return usageError("encryption key %s: expected exactly one key id", sub)
		}
	default:
		return usageError("encryption key: %q is not list, create, rotate, retire, export, confirm or import", sub)
	}
	if *out != "" && sub != "export" {
		return usageError("encryption key %s: --out is for export", sub)
	}
	if sub == "export" && *out == "" {
		return usageError("encryption key export: --out FILE is required; a bundle is written to a file and never printed")
	}
	if *digest != "" && sub != "confirm" {
		return usageError("encryption key %s: --digest is for confirm", sub)
	}
	if *bundlePath != "" && sub != "import" {
		return usageError("encryption key %s: --bundle is for import", sub)
	}
	if *suite != "" && sub != "create" && sub != "rotate" {
		return usageError("encryption key %s: --suite is for create and rotate", sub)
	}
	if sub == "import" && *bundlePath == "" {
		return usageError("encryption key import: --bundle FILE is required")
	}
	ctx := context.Background()

	if sub == "import" {
		return cmdEncryptionKeyImport(ctx, *cfgPath, *bundlePath, *asJSON)
	}

	if sub == "list" {
		svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		got, err := svc.ListEncryptionKeys(ctx)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, got, func() { printEncryptionKeys(got) })
	}

	// The file is claimed before the export, so an exported bundle always
	// has somewhere to go and a path that is taken is refused before a
	// bundle is recorded as exported.
	var file *os.File
	if sub == "export" {
		file, err = os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fail(fmt.Errorf("creating %s: %w", *out, err))
		}
		defer file.Close()
	}

	route, cleanup, err := openEncryptionWrite(ctx, *cfgPath, *asJSON)
	if err != nil {
		if file != nil {
			_ = os.Remove(*out)
		}
		return fail(err)
	}
	defer cleanup()

	var got apicontract.EncryptionKeyStatus
	switch sub {
	case "create":
		got, err = route.CreateEncryptionKey(ctx, *suite)
	case "rotate":
		got, err = route.RotateEncryptionKey(ctx, *suite)
	case "retire":
		got, err = route.RetireEncryptionKey(ctx, rest[0])
	case "confirm":
		got, err = route.ConfirmRecoveryBundle(ctx, rest[0], *digest)
	case "export":
		var raw []byte
		raw, err = route.ExportRecoveryBundleBytes(ctx, rest[0])
		defer func() {
			for i := range raw {
				raw[i] = 0
			}
		}()
		if err == nil {
			if _, werr := file.Write(raw); werr != nil {
				err = fmt.Errorf("writing %s: %w", *out, werr)
			} else if cerr := file.Sync(); cerr != nil {
				err = fmt.Errorf("writing %s: %w", *out, cerr)
			}
		}
		if err != nil {
			_ = os.Remove(*out)
			return fail(err)
		}
		var receipt struct {
			KeyID    string `json:"key_id"`
			Digest   string `json:"digest"`
			Exported string `json:"exported"`
		}
		if jerr := json.Unmarshal(raw, &receipt); jerr != nil {
			return fail(fmt.Errorf("reading back the bundle's receipt: %w", jerr))
		}
		r := apicontract.RecoveryBundleReceipt{KeyID: receipt.KeyID, Digest: receipt.Digest, ExportedAt: receipt.Exported}
		return encryptionAnswer(*asJSON, r, func() {
			fmt.Printf("wrote the recovery bundle for key %s to %s (digest %s)\n", r.KeyID, *out, r.Digest)
			fmt.Printf("keep that file somewhere other than this host, then run: %s encryption key confirm %s\n", cliecho.Binary, r.KeyID)
		})
	}
	if err != nil {
		return fail(err)
	}
	return encryptionAnswer(*asJSON, got, func() { printEncryptionKey(got) })
}

func cmdEncryptionProvider(args []string) int {
	fs, cfgPath := newFlagSet("encryption provider")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	managedKey := fs.String("managed-key", "", "test: a managed key id")
	prov := declareEncryptionProviderFlags(fs)
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 || operands[0] != "test" {
		return usageError("encryption provider: expected `provider test` with --managed-key ID or one of --external-file, --external-env, --external-command")
	}
	req := apicontract.EncryptionProviderTestRequest{ManagedKeyID: *managedKey}
	if p := prov.provider(fs); p != nil {
		req.External = p.External
	}
	if req.ManagedKeyID == "" && req.External == nil {
		return usageError("encryption provider test: name what to test, --managed-key ID or an external reference")
	}
	ctx := context.Background()
	route, cleanup, err := openEncryptionWrite(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	got, err := route.TestEncryptionProvider(ctx, req)
	if err != nil {
		return fail(err)
	}
	code := encryptionAnswer(*asJSON, got, func() {
		fmt.Printf("%s: %s\n", got.Source, got.State)
		if got.Reason != "" {
			fmt.Printf("  %s\n", got.Reason)
		}
	})
	if code == exitOK && got.State != "resolves" {
		return exitFailure
	}
	return code
}

func cmdEncryptionCopies(args []string) int {
	fs, cfgPath := newFlagSet("encryption copies")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 || strings.Count(operands[0], "/") != 2 {
		return usageError("encryption copies: expected exactly one backup id, source/set/name")
	}
	ctx := context.Background()
	svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	got, err := svc.EncryptionCopies(ctx, operands[0])
	if err != nil {
		return fail(err)
	}
	return encryptionAnswer(*asJSON, got, func() {
		fmt.Printf("%s\n", got.ArtifactID)
		for _, c := range got.Copies {
			if !c.Encrypted {
				fmt.Printf("  %s: plaintext\n", c.Medium)
				continue
			}
			resolves := "unknown"
			if c.KeyResolvable != nil {
				resolves = "does not resolve"
				if *c.KeyResolvable {
					resolves = "resolves"
				}
			}
			fmt.Printf("  %s: encrypted (%s v%d), key %s %s, %d bytes stored\n", c.Medium, c.Format, c.FormatVersion, c.KeyID, resolves, c.StoredSize)
		}
	})
}

func cmdEncryptionVerify(args []string) int {
	fs, cfgPath := newFlagSet("encryption verify")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	medium := fs.String("medium", "", "check only the copy on this medium")
	allowEgress := fs.Bool("allow-egress", false, "acknowledge that reading a copy off a medium may be billed")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 || strings.Count(operands[0], "/") != 2 {
		return usageError("encryption verify: expected exactly one backup id, source/set/name")
	}
	req := apicontract.StartDecryptVerifyRequest{ArtifactID: operands[0], Medium: *medium, AcknowledgeEgress: *allowEgress}
	ctx := context.Background()
	route, cleanup, err := openEncryptionWrite(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	// Beside an engine the check is the engine's operation, which is what
	// lets the browser's Cancel and `assurance cancel` stop the same run.
	// With nothing serving it runs here, to the end.
	svc, local := route.(*service.BackupService)
	if !local {
		started, err := route.StartDecryptVerify(ctx, req)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, apicontract.ScrubStarted{BackupSetID: started.BackupSetID, OperationID: started.OperationID}, func() {
			fmt.Printf("decrypt-verify of %s started as operation %s\n", req.ArtifactID, started.OperationID)
			fmt.Printf("follow it with: %s assurance show %s\nstop it with:   %s assurance cancel %s\n", cliecho.Binary, started.OperationID, cliecho.Binary, started.OperationID)
		})
	}
	got, err := svc.RunDecryptVerify(ctx, req)
	if err != nil {
		return fail(err)
	}
	status := exitOK
	if got.Failed+got.CouldNotComplete+got.SkippedHeld+got.SkippedLeased+got.Refused > 0 {
		status = exitFailure
	}
	if *asJSON {
		if code := emitWireJSON(got); code != exitOK {
			return code
		}
		return status
	}
	for _, set := range got.Sets {
		for _, c := range set.Copies {
			line := c.Verdict
			if c.Class != "" {
				line += " (" + c.Class + ")"
			}
			if c.Reason != "" {
				line += ": " + c.Reason
			}
			fmt.Printf("%s on %s: %s\n", c.ArtifactID, c.Medium, line)
		}
	}
	fmt.Printf("decrypt-verify: %d passed, %d failed, %d could not be checked, %d refused; %d bytes read\n",
		got.Passed, got.Failed, got.CouldNotComplete, got.Refused, got.BytesRead)
	return status
}

func printEncryptionProvider(p apicontract.EncryptionKeyProviderState) string {
	switch p.Kind {
	case "managed":
		return "managed key"
	case "external":
		return "external key from " + p.Source
	}
	return "none"
}

func printEncryptionSet(indent string, s apicontract.EncryptionSetPolicy) {
	own := "inherited"
	if s.IsOverride {
		own = "its own block, mode " + s.Mode
	}
	fmt.Printf("%s%s: %s (%s), key provider: %s\n", indent, s.BackupSetID, s.EffectiveMode, own, printEncryptionProvider(s.EffectiveKeyProvider))
	if s.SatisfiedBy == service.SatisfiedByRepository {
		fmt.Printf("%s  %s\n", indent, service.SatisfiedByRepositoryStatement)
	}
}

func printEncryptionPolicy(p apicontract.EncryptionPolicy) {
	fmt.Printf("retained-copy encryption: %s\n", p.Mode)
	fmt.Printf("  key provider: %s\n", printEncryptionProvider(p.KeyProvider))
	if p.Suite != "" {
		fmt.Printf("  suite: %s (configured: %s)\n", p.Suite, p.SuiteConfigured)
	}
	switch {
	case p.RotationNeeded && p.RotateCommand != "":
		fmt.Printf("  no active key for suite %s yet, so copies are refused until you run: %s\n", p.SuiteConfigured, p.RotateCommand)
	case p.RotationNeeded:
		fmt.Printf("  the external key is not a %s key, so copies are refused until the reference yields one\n", p.SuiteConfigured)
	}
	fmt.Printf("  backup sets:\n")
	for _, s := range p.Sets {
		printEncryptionSet("    ", s)
	}
}

func printEncryptionKey(k apicontract.EncryptionKeyStatus) {
	bundle := "no confirmed recovery bundle"
	if k.BundleConfirmed {
		bundle = "recovery bundle confirmed " + k.BundleConfirmedAt
	}
	fmt.Printf("%s: %s, suite %s, created %s, %s, %s\n", k.ID, k.Status, k.Suite, k.CreatedAt, k.Resolvability, bundle)
	if k.Reason != "" {
		fmt.Printf("  %s\n", k.Reason)
	}
}

func printEncryptionKeys(l apicontract.ListEncryptionKeysResponse) {
	if len(l.Keys) == 0 {
		fmt.Println("no managed keys")
		return
	}
	for _, k := range l.Keys {
		printEncryptionKey(k)
	}
}

// cmdEncryptionEncryptExisting is `encryption encrypt-existing <source/set> --to
// MEDIUM` (#1291): encrypt the plaintext retained copies that already sit on a
// medium, under a set whose encryption is required, onto MEDIUM, retiring each
// plaintext copy only after its replacement has been decrypt-verified. It is
// the only thing that does this; a policy change never does.
//
// --dry-run is the same question the run asks, answered per backup with the
// refusal's code and reason, and it plans, reads and writes nothing. Beside a
// serving engine the run starts that engine's operation (stop it with
// `assurance cancel`, which is also what the browser's Cancel calls); with
// nothing serving it runs here, to the end, and an interrupt stops it the same
// way: before the new copy is verified the plaintext copy is exactly as it
// was.
func cmdEncryptionEncryptExisting(args []string) int {
	fs, cfgPath := newFlagSet("encryption encrypt-existing")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	to := fs.String("to", "", "the storage medium the encrypted copies go to; it has to differ from the medium each plaintext copy is on")
	artifact := fs.String("artifact", "", "only this backup of the set, source/set/name")
	dryRun := fs.Bool("dry-run", false, "say what a run would do with each copy, and why not where it would not, without doing anything")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 1 || strings.Count(operands[0], "/") != 1 {
		return usageError("encryption encrypt-existing: expected exactly one backup set, source/set")
	}
	if *to == "" {
		return usageError("encryption encrypt-existing: --to MEDIUM is required, because an encrypted copy cannot sit beside its plaintext source on the same medium")
	}
	req := apicontract.StartEncryptExistingRequest{BackupSetID: operands[0], ArtifactID: *artifact, DestinationMedium: *to}
	ctx := context.Background()

	if *dryRun {
		svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		plan, err := svc.PlanEncryptExisting(ctx, req.BackupSetID, req.ArtifactID, req.DestinationMedium)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, plan, func() {
			for _, c := range plan.Candidates {
				if c.Verdict == "refused" {
					fmt.Printf("%s on %s: refused (%s): %s\n", c.ArtifactID, c.SourceMedium, c.Refusal, c.Reason)
					continue
				}
				fmt.Printf("%s on %s: would be encrypted onto %s\n", c.ArtifactID, c.SourceMedium, req.DestinationMedium)
			}
			fmt.Printf("dry run: %d would be encrypted, %d refused; nothing was changed\n", plan.WouldEncrypt, plan.Refused)
		})
	}

	route, cleanup, err := openEncryptionWrite(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	svc, local := route.(*service.BackupService)
	if !local {
		started, err := route.StartEncryptExisting(ctx, req)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, apicontract.ScrubStarted{BackupSetID: started.BackupSetID, OperationID: started.OperationID}, func() {
			fmt.Printf("encrypting the existing copies of %s onto %s started as operation %s\n", req.BackupSetID, req.DestinationMedium, started.OperationID)
			fmt.Printf("stop it with: %s assurance cancel %s\n", cliecho.Binary, started.OperationID)
		})
	}

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	got, err := svc.RunEncryptExisting(runCtx, req)
	if err != nil {
		return fail(err)
	}
	status := exitOK
	if got.Refused > 0 || got.Abandoned > 0 || runCtx.Err() != nil {
		status = exitFailure
	}
	if *asJSON {
		if code := emitWireJSON(got); code != exitOK {
			return code
		}
		return status
	}
	for _, o := range got.Outcomes {
		line := o.Phase
		if o.Refusal != "" {
			line = "refused (" + o.Refusal + ")"
		}
		if o.Reason != "" {
			line += ": " + o.Reason
		}
		fmt.Printf("%s: %s\n", o.ArtifactID, line)
	}
	fmt.Printf("encrypt-existing: %d planned, %d resumed, %d completed, %d abandoned, %d refused\n",
		got.Planned, got.Resumed, got.Completed, got.Abandoned, got.Refused)
	if runCtx.Err() != nil {
		fmt.Println("stopped: a copy that was not yet verified was discarded and its plaintext copy is untouched; run it again to carry on")
	}
	return status
}

// cmdEncryptionSuites is `encryption suites [--json]` (EPIC T, #1315): the
// suites this build has, each with its status, whether it is the default,
// the one sentence any surface may print about it, and how many active
// copies and live keys are under it. It reads the journal only, so it opens
// no key directory. GET /encryption/suites answers with the same body.
func cmdEncryptionSuites(args []string) int {
	fs, cfgPath := newFlagSet("encryption suites")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("encryption suites: takes no operand")
	}
	ctx := context.Background()
	svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	got, err := svc.ListEncryptionSuites(ctx)
	if err != nil {
		return fail(err)
	}
	return encryptionAnswer(*asJSON, got, func() { printEncryptionSuites(got) })
}

func printEncryptionSuites(l apicontract.ListEncryptionSuitesResponse) {
	for _, s := range l.Suites {
		def := ""
		if s.Default {
			def = ", default for a first key"
		}
		// When a deprecated suite retires, or which version retired it,
		// comes from the table and is never typed here (#1316).
		if s.RetirementVersion != "" {
			switch s.Status {
			case "retired":
				def += ", retired in " + s.RetirementVersion
			default:
				def += ", retirement announced for version " + s.RetirementVersion
			}
		}
		fmt.Printf("%s (v%d): %s%s, %d active copies, %d keys\n", s.ID, s.Version, s.Status, def, s.ActiveCopies, s.Keys)
		fmt.Printf("  %s\n", s.Describe)
	}
}

// cmdEncryptionReencrypt is `encryption reencrypt --suite ID | --key ID |
// --deprecated --to MEDIUM` (#1317): rewrite the encrypted retained copies
// the selector names under the active suite and key, each onto MEDIUM,
// retiring each old copy only after its replacement has been decrypt-verified.
// The old ciphertext is streamed through its own suite and key straight into
// the new one; no plaintext is written to local disk. It is the only thing
// that does this; a key rotation or a suite deprecation never does.
//
// --dry-run lists the selected copies grouped by suite and by key, each with
// its destination (the --to medium, or the first other configured medium when
// --to is left out) and, where a run would not rewrite it, the refusal's code
// and reason. It plans, reads and writes nothing. A run needs --to, so a
// medium is never chosen for you. Beside a serving engine the run starts that
// engine's operation (stop it with `assurance cancel`, which is also what the
// browser's Cancel calls); with nothing serving it runs here, to the end, and
// an interrupt stops it the same way.
func cmdEncryptionReencrypt(args []string) int {
	fs, cfgPath := newFlagSet("encryption reencrypt")
	asJSON := fs.Bool("json", false, "print the object the API returns instead of text")
	suite := fs.String("suite", "", "select every copy under this suite id")
	keyID := fs.String("key", "", "select every copy under this key id")
	deprecated := fs.Bool("deprecated", false, "select every copy under a deprecated or retired suite")
	to := fs.String("to", "", "the storage medium the new copies go to; it has to differ from the medium each copy is on")
	dryRun := fs.Bool("dry-run", false, "list the selected copies by suite and key, with what a run would do with each and why not where it would not, without doing anything")
	operands, err := parseFlagsAroundOperands(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(operands) != 0 {
		return usageError("encryption reencrypt: takes no operands; select copies with --suite, --key or --deprecated")
	}
	req := apicontract.StartReencryptRequest{SuiteID: *suite, KeyID: *keyID, Deprecated: *deprecated, DestinationMedium: *to}
	ctx := context.Background()

	if *dryRun {
		svc, cleanup, err := openBackupServiceForRead(ctx, *cfgPath, *asJSON)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		plan, err := svc.PlanReencrypt(ctx, req)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, plan, func() {
			for _, g := range plan.Groups {
				fmt.Printf("suite %s, key %s:\n", g.SuiteID, g.KeyID)
				for _, c := range g.Candidates {
					if c.Verdict == "refused" {
						fmt.Printf("  %s on %s: refused (%s): %s\n", c.ArtifactID, c.SourceMedium, c.Refusal, c.Reason)
						continue
					}
					fmt.Printf("  %s on %s: would be re-encrypted onto %s\n", c.ArtifactID, c.SourceMedium, c.DestinationMedium)
				}
			}
			fmt.Printf("dry run: %d would be re-encrypted, %d refused; nothing was changed\n", plan.WouldReencrypt, plan.Refused)
		})
	}

	if *to == "" {
		return usageError("encryption reencrypt: --to MEDIUM is required for a run, because a new copy cannot sit beside its source on the same medium and a medium is never chosen for you; --dry-run proposes one")
	}
	route, cleanup, err := openEncryptionWrite(ctx, *cfgPath, *asJSON)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	svc, local := route.(*service.BackupService)
	if !local {
		started, err := route.StartReencrypt(ctx, req)
		if err != nil {
			return fail(err)
		}
		return encryptionAnswer(*asJSON, apicontract.ScrubStarted{BackupSetID: started.BackupSetID, OperationID: started.OperationID}, func() {
			fmt.Printf("re-encrypting the selected copies onto %s started as operation %s\n", req.DestinationMedium, started.OperationID)
			fmt.Printf("stop it with: %s assurance cancel %s\n", cliecho.Binary, started.OperationID)
		})
	}

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	got, err := svc.RunReencrypt(runCtx, req)
	if err != nil {
		return fail(err)
	}
	status := exitOK
	if got.Refused > 0 || got.Abandoned > 0 || runCtx.Err() != nil {
		status = exitFailure
	}
	if *asJSON {
		if code := emitWireJSON(got); code != exitOK {
			return code
		}
		return status
	}
	for _, o := range got.Outcomes {
		line := o.Phase
		if o.Refusal != "" {
			line = "refused (" + o.Refusal + ")"
		}
		if o.Reason != "" {
			line += ": " + o.Reason
		}
		fmt.Printf("%s: %s\n", o.ArtifactID, line)
	}
	fmt.Printf("reencrypt: %d planned, %d resumed, %d completed, %d abandoned, %d refused\n",
		got.Planned, got.Resumed, got.Completed, got.Abandoned, got.Refused)
	if runCtx.Err() != nil {
		fmt.Println("stopped: a copy that was not yet verified was discarded and the copy it was made from is untouched; run it again to carry on")
	}
	return status
}
