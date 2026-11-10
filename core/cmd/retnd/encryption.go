package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

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
//	encryption decrypt <object> ...            open one copy to a file (#1289)
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
}

func cmdEncryption(args []string) int {
	for i, a := range args {
		if verb, ok := encryptionVerbs[a]; ok {
			return verb(append(append([]string{}, args[:i]...), args[i+1:]...))
		}
	}
	return usageError("encryption: expected `encryption policy`, `encryption backup-set <source/set>`, `encryption key ...`, `encryption provider test`, `encryption copies <source/set/artifact>`, `encryption verify <source/set/artifact>` or `encryption decrypt <object> ...`")
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
		if len(passedEncryptionFlags(fs, "mode", "managed", "external-file", "external-env", "external-command")) > 0 {
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
		req.KeyProvider = prov.provider(fs)
		if req.Mode == "" && req.KeyProvider == nil {
			return usageError("encryption policy patch: name what to change: --mode, or one of --managed, --external-file, --external-env, --external-command")
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
		if len(passedEncryptionFlags(fs, "mode", "managed", "external-file", "external-env", "external-command")) > 0 {
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
		got, err = route.CreateEncryptionKey(ctx)
	case "rotate":
		got, err = route.RotateEncryptionKey(ctx)
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
	fmt.Printf("%s: %s, created %s, %s, %s\n", k.ID, k.Status, k.CreatedAt, k.Resolvability, bundle)
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
