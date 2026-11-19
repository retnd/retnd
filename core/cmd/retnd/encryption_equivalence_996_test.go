package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
)

// #996, the CLI half of Suite C: the same encryption action driven through the
// two routes this binary has (the engine-attached one, which is the API, and
// the direct one) has to leave the same configuration behind, answer with the
// same object, exit with the same code and hand back a recovery bundle that
// opens a copy. The browser's half is ui/shared/e2e/996-encryption-reconnect.spec.ts
// and the route-to-verb table apps/common/webhost already pins (#993).
//
// These tests were written by reading the verbs, the service and the fake
// engine, and have not been run. Each names the defect that turns it red.

// cli996 runs one command in this process and returns its exit code and both
// streams, so a test can scan either.
func cli996(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var (
		code int
		out  string
	)
	errOut := captureStderr(t, func() {
		out = captureStdout(t, func() { code = run(args) })
	})
	return code, out, errOut
}

// writeStep is one encryption write, as argv without the configuration flag,
// and whether its answer is the same object on both routes. A key verb's
// answer carries an id and timestamps no two deployments share, so those are
// dropped before the comparison.
type writeStep struct {
	name string
	args []string
}

// everyEncryptionWrite is each mutating verb the CLI has, in an order in which
// every step is accepted: the key exists before a policy names it, a set's own
// block is written, replaced and removed, and the deployment's block is reset
// last. The engine's own refusals are routeparity_test.go's; this table is the
// accepted half, which no existing test covers.
var everyEncryptionWrite = []writeStep{
	{"create the managed key", []string{"encryption", "key", "create", "--json"}},
	{"require encryption deployment-wide with the managed key", []string{"encryption", "policy", "patch", "--mode", "required", "--managed", "--json"}},
	{"give one set its own required block", []string{"encryption", "backup-set", cliSet, "--mode", "required", "--managed", "--json"}},
	{"replace that block with off", []string{"encryption", "backup-set", cliSet, "--mode", "off", "--json"}},
	{"remove the set's own block", []string{"encryption", "backup-set", cliSet, "--reset", "--json"}},
	{"rotate the key", []string{"encryption", "key", "rotate", "--json"}},
	{"reset the deployment's block", []string{"encryption", "policy", "reset", "--json"}},
}

// scrubbedAnswer parses a --json answer and drops the fields that are one
// deployment's own: key ids and the instants they were minted at.
func scrubbedAnswer(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("the --json answer is not one JSON object: %v\n%s", err, stdout)
	}
	for _, k := range []string{"id", "created_at", "retired_at", "bundle_confirmed_at", "active_key_id"} {
		delete(m, k)
	}
	return m
}

// normalised is a configuration file with each deployment's own directory
// replaced, because the fixture writes absolute paths into it.
func normalised(raw string, dirs ...string) string {
	for _, d := range dirs {
		raw = strings.ReplaceAll(raw, d, "<root>")
	}
	return raw
}

type routeRun struct {
	code   int
	answer map[string]any
	config string
}

// runWrites drives steps against configPath and records, after each one, the
// exit code, the scrubbed answer and the configuration file that route wrote
// to (writtenConfig), normalised.
func runWrites(t *testing.T, steps []writeStep, configPath, writtenConfig string, dirs ...string) []routeRun {
	t.Helper()
	var runs []routeRun
	for _, s := range steps {
		args := append(append([]string{}, s.args...), "--config", configPath)
		code, out, errOut := cli996(t, args...)
		r := routeRun{code: code, config: normalised(readFile(t, writtenConfig), dirs...)}
		if code == exitOK {
			r.answer = scrubbedAnswer(t, out)
		} else {
			t.Errorf("%s exited %d: %s", s.name, code, errOut)
		}
		runs = append(runs, r)
	}
	return runs
}

// Every accepted encryption write leaves the same configuration, with the same
// answer and the same exit code, whether the CLI wrote it itself or sent it to
// an engine through the API. Red if a route drops a field (the provider, the
// mode, a set's block), if the engine-attached route answers a different
// object than the direct one, or if either exits non-zero for an accepted
// input.
func TestEveryEncryptionWriteLeavesTheSameConfigurationOnBothRoutes_996(t *testing.T) {
	clearTheRoute(t)

	direct := writeTestConfig(t)
	directRuns := runWrites(t, everyEncryptionWrite, direct, direct, filepath.Dir(direct))

	// The engine route: the CLI is pointed at its own configuration and
	// every write lands in the engine's file, which names the same
	// deployment (routeparity_test.go says why one journal and two files).
	cliSide := writeTestConfig(t)
	engine := startFakeEngineFor(t, cliSide)
	engine.attach(t)
	engineRuns := runWrites(t, everyEncryptionWrite, cliSide, engine.configPath,
		filepath.Dir(engine.configPath), filepath.Dir(cliSide))

	if len(directRuns) != len(everyEncryptionWrite) || len(engineRuns) != len(everyEncryptionWrite) {
		t.Fatalf("ran %d direct and %d engine steps, want %d each", len(directRuns), len(engineRuns), len(everyEncryptionWrite))
	}
	for i, s := range everyEncryptionWrite {
		d, e := directRuns[i], engineRuns[i]
		if d.code != e.code {
			t.Errorf("%s: exit %d directly and %d through the engine", s.name, d.code, e.code)
		}
		if !reflect.DeepEqual(d.answer, e.answer) {
			t.Errorf("%s: the two routes answered different objects\ndirect: %v\nengine: %v", s.name, d.answer, e.answer)
		}
		if d.config != e.config {
			t.Errorf("%s: the two routes left different configuration\ndirect:\n%s\nengine:\n%s", s.name, d.config, e.config)
		}
	}

	// The engine route really was the one taken: its own file changed and
	// the CLI-side file, which a routed write must not touch, never named
	// the block.
	if got := readFile(t, cliSide); strings.Contains(got, "retained_copy_encryption") {
		t.Errorf("a routed write changed the CLI's own configuration, so it never went through the engine:\n%s", got)
	}
	var sawPolicy bool
	for _, req := range engine.requests() {
		if strings.Contains(req, "/encryption/policy") {
			sawPolicy = true
		}
	}
	if !sawPolicy {
		t.Errorf("the engine saw no encryption policy request: %v", engine.requests())
	}
}

// The comparison above can fail: one route applying a step the other did not
// is seen as a difference in the file, so "equal" is a finding and not a
// default. Red if normalised flattens every configuration to the same string.
func TestTheEncryptionEquivalenceComparisonCanActuallyFail_996(t *testing.T) {
	clearTheRoute(t)
	a := writeTestConfig(t)
	b := writeTestConfig(t)
	steps := everyEncryptionWrite[:2]
	ra := runWrites(t, steps, a, a, filepath.Dir(a))
	rb := runWrites(t, steps[:1], b, b, filepath.Dir(b))
	if ra[1].config == rb[0].config {
		t.Fatal("a deployment that applied the policy patch and one that did not compare equal, so the equivalence test cannot see a dropped write")
	}
}

// The bundle an export returns through the API opens a copy exactly as the one
// the CLI writes itself does. The engine route decodes the answer into the
// contract's RecoveryBundle and writes it back out, so a field the contract
// type renamed, reformatted or dropped would change what the digest covers and
// leave a bundle that ReadBundle refuses, found only on the day of a recovery.
// Red if the re-encoded bundle fails its own integrity check or does not open a
// copy sealed to the key.
func TestABundleExportedThroughTheEngineOpensACopyLikeADirectOne_996(t *testing.T) {
	clearTheRoute(t)
	plaintext := bytes.Repeat([]byte("a copy that has to come back, byte for byte. "), 4000)

	open := func(t *testing.T, bundlePath string) {
		t.Helper()
		f, err := os.Open(bundlePath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, err := keycustody.ReadBundle(f)
		if err != nil {
			t.Fatalf("the exported bundle is refused by its own reader: %v", err)
		}
		// Sealed under the suite the bundle names (#1313: a fresh key is
		// age-pq/v1), through that suite's own library construction.
		suite, ok := copycrypt.Lookup(b.FormatID)
		if !ok {
			t.Fatalf("the bundle names suite %q, which the registry does not have", b.FormatID)
		}
		var sealed bytes.Buffer
		w, err := suite.Seal(&sealed, b.Recipient)
		if err != nil {
			t.Fatalf("the bundle's recipient does not parse: %v", err)
		}
		if _, err := w.Write(plaintext); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := b.Decrypt(suite, &sealed, &got); err != nil {
			t.Fatalf("the bundle does not open a copy sealed to its recipient: %v", err)
		}
		if !bytes.Equal(got.Bytes(), plaintext) {
			t.Fatal("the bundle opened the copy to something other than the plaintext")
		}
	}

	export := func(t *testing.T, cfg string) string {
		t.Helper()
		code, out, errOut := cli996(t, "encryption", "key", "create", "--json", "--config", cfg)
		if code != exitOK {
			t.Fatalf("key create exited %d: %s", code, errOut)
		}
		var key apicontract.EncryptionKeyStatus
		if err := json.Unmarshal([]byte(out), &key); err != nil || key.ID == "" {
			t.Fatalf("key create did not answer a key: %v\n%s", err, out)
		}
		bundlePath := filepath.Join(t.TempDir(), "bundle.json")
		if code, _, errOut := cli996(t, "encryption", "key", "export", key.ID, "--out", bundlePath, "--json", "--config", cfg); code != exitOK {
			t.Fatalf("key export exited %d: %s", code, errOut)
		}
		info, err := os.Stat(bundlePath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("the bundle file is %v (%v), want a file only the owner can read", info, err)
		}
		return bundlePath
	}

	t.Run("direct", func(t *testing.T) {
		open(t, export(t, writeTestConfig(t)))
	})
	t.Run("through the engine", func(t *testing.T) {
		cliSide := writeTestConfig(t)
		engine := startFakeEngineFor(t, cliSide)
		engine.attach(t)
		open(t, export(t, cliSide))
		var sawExport bool
		for _, req := range engine.requests() {
			if strings.HasSuffix(req, "/export") {
				sawExport = true
			}
		}
		if !sawExport {
			t.Fatalf("the engine never served an export, so this ran the direct route: %v", engine.requests())
		}
	})
}

// ---- NV.23: no key in CLI JSON ----

// allMaterial reads every managed key file beside configPath, the material the
// scans below look for. It reads them from disk so the scan does not depend on
// a verb behaving.
func allMaterial(t *testing.T, configPath string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(keycustody.DirBeside(configPath), "*.key"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no managed key files beside %s (%v), so there is no material to look for", configPath, err)
	}
	var out []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.TrimSpace(string(raw)))
	}
	return out
}

// ageIdentityShape is what an age identity looks like on a page. The bare
// prefix is not enough: the refusal for a key that is not an identity quotes
// "AGE-SECRET-KEY-1" as the thing it expected, and that sentence is allowed.
var ageIdentityShape = regexp.MustCompile(`AGE-SECRET-KEY-(PQ-)?1[A-Z0-9]{20,}`)

// leaksAny is the one predicate every NV.23 check and control shares: any
// rendering of any key's material, or anything shaped like an age identity.
func leaksAny(surface string, materials []string) bool {
	if ageIdentityShape.MatchString(surface) {
		return true
	}
	for _, m := range materials {
		if carriesAnyRendering(surface, m) {
			return true
		}
	}
	return false
}

// Every encryption verb, on --json, and every refusal it can print, on both
// streams, carries no key material. The one place material leaves is the
// bundle file `key export --out` writes, and the test proves that file does
// hold it (so the scan has something to find) while stdout and stderr do not.
// Red if any verb prints an identity, a rendering of one, or a bundle.
func TestNoEncryptionVerbPrintsKeyMaterialOnEitherStream_996(t *testing.T) {
	clearTheRoute(t)
	cfg := writeTestConfig(t)
	run996 := func(args ...string) (int, string, string) {
		return cli996(t, append(args, "--config", cfg)...)
	}

	code, out, errOut := run996("encryption", "key", "create", "--json")
	if code != exitOK {
		t.Fatalf("key create exited %d: %s", code, errOut)
	}
	var first apicontract.EncryptionKeyStatus
	if err := json.Unmarshal([]byte(out), &first); err != nil || first.ID == "" {
		t.Fatalf("key create did not answer a key: %v\n%s", err, out)
	}
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")

	type invocation struct {
		args []string
		// refused is true for an input the verb must refuse, so the scan
		// reaches the refusal text and not only the success path.
		refused bool
	}
	calls := []invocation{
		{args: []string{"encryption", "key", "list", "--json"}},
		{args: []string{"encryption", "provider", "test", "--managed-key", first.ID, "--json"}},
		{args: []string{"encryption", "policy", "patch", "--mode", "required", "--managed", "--json"}},
		{args: []string{"encryption", "policy", "--json"}},
		{args: []string{"encryption", "backup-set", cliSet, "--mode", "required", "--managed", "--json"}},
		{args: []string{"encryption", "backup-set", cliSet, "--json"}},
		{args: []string{"encryption", "key", "rotate", "--json"}},
		{args: []string{"encryption", "key", "export", first.ID, "--out", bundlePath, "--json"}},
		{args: []string{"encryption", "key", "confirm", first.ID, "--json"}},
		{args: []string{"encryption", "key", "confirm", "knosuchkey", "--json"}, refused: true},
		{args: []string{"encryption", "key", "retire", "knosuchkey", "--json"}, refused: true},
		{args: []string{"encryption", "copies", "api/postgres/none.dump", "--json"}, refused: true},
		{args: []string{"encryption", "verify", "api/postgres/none.dump", "--json"}, refused: true},
		{args: []string{"encryption", "policy", "patch", "--mode", "sometimes", "--json"}, refused: true},
		{args: []string{"encryption", "backup-set", cliSet, "--reset", "--json"}},
		{args: []string{"encryption", "policy", "reset", "--json"}},
	}

	materials := allMaterial(t, cfg)
	var scanned int
	for _, c := range calls {
		code, out, errOut := run996(c.args...)
		if c.refused && code == exitOK {
			t.Errorf("%v was accepted, so the refusal path was not scanned", c.args)
		}
		if !c.refused && code != exitOK {
			t.Errorf("%v exited %d: %s", c.args, code, errOut)
		}
		for stream, text := range map[string]string{"stdout": out, "stderr": errOut} {
			scanned++
			if leaksAny(text, materials) {
				t.Errorf("%v prints key material on %s:\n%s", c.args, stream, text)
			}
		}
	}
	// A rotation minted a second key after the scan list was read.
	materials = allMaterial(t, cfg)
	if len(materials) < 2 {
		t.Fatalf("expected two keys after a rotation, found %d", len(materials))
	}

	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("the export wrote no bundle file: %v", err)
	}
	if !leaksAny(string(bundle), materials) {
		t.Fatal("the bundle file does not hold the key, so a scan finding nothing on stdout proves nothing about what export leaves out")
	}
	if scanned < 2*len(calls) {
		t.Fatalf("scanned %d streams for %d calls", scanned, len(calls))
	}
}

// The control for NV.23: a canary planted in one verb's output, in each rendering
// the scan knows, and in the shape of an identity, is found. Red if leaksAny
// stops reading renderings or the identity prefix.
func TestNoEncryptionVerbPrintsKeyMaterialOnEitherStream_996_WouldCatchOne(t *testing.T) {
	canary996 := "AGE-SECRET-KEY-1PLANTEDPLANTEDPLANTEDPLANTEDPLANTEDPLANTEDPLANTEDPLANT"
	for _, planted := range []string{
		`{"id":"k0123456789abcdef","status":"` + canary996 + `"}`,
		`{"note":"` + hexOf(canary996) + `"}`,
		`encryption: ` + b64Of(canary996),
	} {
		if !leaksAny(planted, []string{canary996}) {
			t.Errorf("a canary planted as %q was not found", planted)
		}
	}
	if leaksAny(`{"id":"k0123456789abcdef","status":"active"}`, []string{canary996}) {
		t.Error("the scan flags an ordinary key status, so the check above would be red for the wrong reason")
	}
}

func hexOf(s string) string { return hex.EncodeToString([]byte(s)) }
func b64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
