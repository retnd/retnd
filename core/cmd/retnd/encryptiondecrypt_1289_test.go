package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/recovery"
)

// #1289: `retnd encryption decrypt` and `retnd encryption key import`, the
// two verbs a lost-host recovery needs from the shipped binary. They are
// driven through the real dispatcher, with a real managed key, a real
// bundle exported by the real verb and a real encrypted copy written by the
// real transform.

// plaintextMarker is in every fixture plaintext so a scan of standard
// output, standard error and the destination can say whether plaintext
// leaked anywhere it should not have.
const plaintextMarker = "PLAINTEXT-MARKER-1289-"

// runAll runs one command and returns its exit code with standard output and
// standard error kept apart, because the claims here are about each.
func runAll(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run(args) })
	})
	return code, stdout, stderr
}

// decryptFixture is a deployment with one managed key, its exported bundle,
// one encrypted copy and the sidecar that describes it, each in a directory
// of its own so a leftover file anywhere is visible.
type decryptFixture struct {
	configPath string
	keyID      string
	material   string
	bundle     string // path of the exported bundle
	object     string // path of the encrypted copy
	sidecar    string // path of its sidecar
	outDir     string // an empty directory to recover into
	out        string // outDir/recovered.dump
	plaintext  []byte
	plainHash  string
	storedHash string
}

func newDecryptFixture(t *testing.T) decryptFixture {
	t.Helper()
	clearTheRoute(t)
	f := decryptFixture{configPath: writeTestConfig(t)}
	if code, out := runCapturing(t, "encryption", "key", "create", "--config", f.configPath); code != exitOK {
		t.Fatalf("key create exited %d: %s", code, out)
	}
	f.material = keyMaterialIn(t, f.configPath)
	_, listed := runCapturing(t, "encryption", "key", "list", "--json", "--config", f.configPath)
	var list apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(listed), &list); err != nil || len(list.Keys) != 1 {
		t.Fatalf("key list: %v\n%s", err, listed)
	}
	f.keyID = list.Keys[0].ID

	f.bundle = filepath.Join(t.TempDir(), "bundle.json")
	if code, out := runCapturing(t, "encryption", "key", "export", f.keyID, "--out", f.bundle, "--config", f.configPath); code != exitOK {
		t.Fatalf("key export exited %d: %s", code, out)
	}
	raw, err := os.ReadFile(f.bundle)
	if err != nil {
		t.Fatal(err)
	}
	b, err := keycustody.ReadBundle(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}

	// Three full chunks and a bit, so a truncation and a flipped byte both
	// land inside authenticated payload, past the header.
	f.plaintext = append([]byte(plaintextMarker), bytes.Repeat([]byte("retained artifact bytes\n"), 3*65536/24+40)...)
	sum := sha256.Sum256(f.plaintext)
	f.plainHash = hex.EncodeToString(sum[:])

	objDir := t.TempDir()
	staged, err := copycrypt.EncryptToStaging(context.Background(), objDir, "backup-2026-11-01.dump", bytes.NewReader(f.plaintext), b.Recipient, f.plainHash)
	if err != nil {
		t.Fatalf("EncryptToStaging: %v", err)
	}
	f.object, f.storedHash = staged.Path, staged.StoredHash

	m := recovery.Manifest{
		FormatVersion: recovery.CurrentFormatVersion,
		Source:        "production", BackupSet: "postgres-primary", ArtifactName: "backup-2026-11-01.dump",
		RemotePath:        "incoming/backup-2026-11-01.dump",
		ReceivedTimestamp: time.Date(2026, 11, 1, 3, 5, 0, 0, time.UTC), RetentionTimestamp: time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC),
		SizeBytes: staged.PlaintextSize,
		Placements: []recovery.ManifestPlacement{{
			Medium: "offsite", Location: "retnd/production/postgres-primary/backup-2026-11-01.dump", Status: "ACTIVE",
			Encryption: &recovery.ManifestEncryption{
				FormatID: "age/v1", FormatVersion: 1, KeyID: f.keyID,
				PlaintextDigest: f.plainHash, PlaintextDigestAlgorithm: "sha256", PlaintextSize: staged.PlaintextSize,
				StoredDigest: f.storedHash, StoredDigestAlgorithm: "sha256", StoredSize: staged.StoredSize,
			},
		}},
	}
	enc, err := recovery.EncodeManifest(m)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	f.sidecar = filepath.Join(t.TempDir(), "backup-2026-11-01.dump.manifest.json")
	if err := os.WriteFile(f.sidecar, enc, 0o600); err != nil {
		t.Fatal(err)
	}

	f.outDir = t.TempDir()
	f.out = filepath.Join(f.outDir, "recovered.dump")
	return f
}

// decryptArgs is the whole recovery with a bundle and a sidecar.
func (f decryptFixture) decryptArgs(extra ...string) []string {
	return append([]string{"encryption", "decrypt", f.object, "--bundle", f.bundle, "--sidecar", f.sidecar, "--out", f.out}, extra...)
}

// leaks reports every way the key or the plaintext reached a text surface.
func (f decryptFixture) leaks(surface string) []string {
	var found []string
	if carriesAnyRendering(surface, f.material) {
		found = append(found, "key material")
	}
	if strings.Contains(surface, plaintextMarker) {
		found = append(found, "plaintext")
	}
	return found
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The round trip: the recovered file is the artifact, it is private, it is
// the only thing in the destination directory, and nothing that was printed
// carries the key or the plaintext.
func TestDecryptRoundTripsThroughTheBundleAndTheSidecar_1289(t *testing.T) {
	f := newDecryptFixture(t)
	code, stdout, stderr := runAll(t, f.decryptArgs()...)
	if code != exitOK {
		t.Fatalf("decrypt exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	got, err := os.ReadFile(f.out)
	if err != nil {
		t.Fatalf("no recovered file: %v", err)
	}
	if !bytes.Equal(got, f.plaintext) {
		t.Fatalf("the recovered file is not the artifact (%d bytes, want %d)", len(got), len(f.plaintext))
	}
	if info, _ := os.Stat(f.out); info.Mode().Perm() != 0o600 {
		t.Errorf("the recovered file is %v, want 0600", info.Mode().Perm())
	}
	if names := dirNames(t, f.outDir); len(names) != 1 || names[0] != "recovered.dump" {
		t.Errorf("the destination directory holds %v, want only the recovered file (no temp name left)", names)
	}
	if !strings.Contains(stdout, f.plainHash) {
		t.Errorf("the success line does not say which plaintext digest was verified:\n%s", stdout)
	}
	for name, surface := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if l := f.leaks(surface); len(l) != 0 {
			t.Errorf("%s carries %v:\n%s", name, l, surface)
		}
	}
}

// An age identity file works in place of the bundle, with the digest given
// by hand instead of a sidecar. The identity file carries a comment line,
// as `age-keygen` writes one.
func TestDecryptRoundTripsThroughAnIdentityFileAndExplicitDigests_1289(t *testing.T) {
	f := newDecryptFixture(t)
	identity := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identity, []byte("# created: 2026-11-01T00:00:00Z\n"+f.material+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAll(t, "encryption", "decrypt", f.object, "--identity", identity,
		"--digest", f.plainHash, "--stored-digest", f.storedHash, "--out", f.out)
	if code != exitOK {
		t.Fatalf("decrypt exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got, _ := os.ReadFile(f.out); !bytes.Equal(got, f.plaintext) {
		t.Fatalf("the recovered file is not the artifact")
	}
	if l := f.leaks(stdout + stderr); len(l) != 0 {
		t.Errorf("output carries %v", l)
	}
}

// Every way a recovery can fail leaves nothing in the destination: no
// plaintext file, no partial one, no temp name. A truncated copy matters
// most, because its early chunks authenticate and a streaming writer would
// already have put them on disk.
func TestADecryptThatFailsLeavesNothingBehindAndSaysWhy_1289(t *testing.T) {
	stranger := newDecryptFixture(t) // a second deployment with its own key

	cases := []struct {
		name   string
		mutate func(t *testing.T, f *decryptFixture) []string // returns the args
		want   string                                         // a phrase the refusal must carry
	}{
		{"truncated, no stored digest to catch it first", func(t *testing.T, f *decryptFixture) []string {
			cut(t, f.object, 40)
			return []string{"encryption", "decrypt", f.object, "--bundle", f.bundle, "--digest", f.plainHash, "--out", f.out}
		}, "failed authentication"},
		{"truncated by more than a chunk", func(t *testing.T, f *decryptFixture) []string {
			cut(t, f.object, 64*1024+100) // drops the whole final chunk and some of the one before
			return []string{"encryption", "decrypt", f.object, "--bundle", f.bundle, "--digest", f.plainHash, "--out", f.out}
		}, "failed authentication"},
		{"tampered payload", func(t *testing.T, f *decryptFixture) []string {
			flip(t, f.object, 100_000)
			return []string{"encryption", "decrypt", f.object, "--bundle", f.bundle, "--digest", f.plainHash, "--out", f.out}
		}, "failed authentication"},
		{"truncated, caught by the sidecar's stored digest", func(t *testing.T, f *decryptFixture) []string {
			cut(t, f.object, 40)
			return f.decryptArgs()
		}, "stored"},
		{"tampered, caught by the sidecar's stored digest", func(t *testing.T, f *decryptFixture) []string {
			flip(t, f.object, 100_000)
			return f.decryptArgs()
		}, "stored"},
		{"a bundle holding another key", func(t *testing.T, f *decryptFixture) []string {
			return []string{"encryption", "decrypt", f.object, "--bundle", stranger.bundle, "--digest", f.plainHash, "--out", f.out}
		}, "does not open this object"},
		{"a bundle for a key the sidecar does not name", func(t *testing.T, f *decryptFixture) []string {
			return []string{"encryption", "decrypt", f.object, "--bundle", stranger.bundle, "--sidecar", f.sidecar, "--out", f.out}
		}, "written under key"},
		{"the wrong plaintext digest", func(t *testing.T, f *decryptFixture) []string {
			return []string{"encryption", "decrypt", f.object, "--bundle", f.bundle, "--digest", strings.Repeat("0", 64), "--out", f.out}
		}, "plaintext digest"},
		{"the wrong stored digest", func(t *testing.T, f *decryptFixture) []string {
			return []string{"encryption", "decrypt", f.object, "--bundle", f.bundle, "--digest", f.plainHash, "--stored-digest", strings.Repeat("0", 64), "--out", f.out}
		}, "stored"},
		{"an object that is not an age file", func(t *testing.T, f *decryptFixture) []string {
			if err := os.WriteFile(f.object, []byte("this is not an encrypted copy"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{"encryption", "decrypt", f.object, "--bundle", f.bundle, "--digest", f.plainHash, "--out", f.out}
		}, "failed authentication"},
	}

	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDecryptFixture(t)
			args := tc.mutate(t, &f)
			code, stdout, stderr := runAll(t, args...)
			if code != exitFailure {
				t.Fatalf("exited %d, want %d\nstdout: %s\nstderr: %s", code, exitFailure, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("the refusal does not say %q:\n%s", tc.want, stderr)
			}
			if names := dirNames(t, f.outDir); len(names) != 0 {
				t.Errorf("a failed decrypt left %v in the destination directory", names)
			}
			if _, err := os.Stat(f.out); err == nil {
				t.Errorf("a failed decrypt left %s", f.out)
			}
			for name, surface := range map[string]string{"stdout": stdout, "stderr": stderr} {
				if l := f.leaks(surface); len(l) != 0 {
					t.Errorf("%s carries %v:\n%s", name, l, surface)
				}
				if strings.Contains(surface, stranger.material) {
					t.Errorf("%s carries the other key's material", name)
				}
			}
			seen[tc.name] = stderr
		})
	}

	// A wrong key, a wrong digest and a failed authentication are three
	// different refusals, so an operator can tell which thing to go and fix:
	// each carries its own phrase and neither of the other two.
	phrases := map[string]string{
		"a bundle holding another key": "does not open this object",
		"the wrong plaintext digest":   "plaintext digest differs",
		"tampered payload":             "failed authentication",
	}
	for name, own := range phrases {
		for otherName, other := range phrases {
			if name != otherName && strings.Contains(seen[name], other) {
				t.Errorf("the refusal for %q also says %q", name, other)
			}
		}
		if !strings.Contains(seen[name], own) {
			t.Errorf("the refusal for %q does not say %q", name, own)
		}
	}
}

func cut(t *testing.T, path string, n int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-n], 0o600); err != nil {
		t.Fatal(err)
	}
}

func flip(t *testing.T, path string, at int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[at] ^= 0x01
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A file that is already at the destination is never replaced and never
// touched: a recovery that overwrites last week's good recovery on a typo
// is worse than one that refuses.
func TestDecryptRefusesToReplaceAFileThatIsThere_1289(t *testing.T) {
	f := newDecryptFixture(t)
	if err := os.WriteFile(f.out, []byte("already here"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAll(t, f.decryptArgs()...)
	if code != exitFailure {
		t.Fatalf("exited %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, f.out) {
		t.Errorf("the refusal does not name the file in the way:\n%s", stderr)
	}
	if got, _ := os.ReadFile(f.out); string(got) != "already here" {
		t.Errorf("the existing file was changed: %q", got)
	}
	if names := dirNames(t, f.outDir); len(names) != 1 {
		t.Errorf("the destination directory holds %v", names)
	}
}

// Nothing in the arguments can make the plaintext go to standard output: it
// is a file or it is nothing.
func TestDecryptHasNoStandardOutputForm_1289(t *testing.T) {
	f := newDecryptFixture(t)
	for _, dest := range []string{"-", "/dev/stdout"} {
		args := []string{"encryption", "decrypt", f.object, "--bundle", f.bundle, "--sidecar", f.sidecar, "--out", dest}
		code, stdout, _ := runAll(t, args...)
		if code == exitOK {
			t.Errorf("--out %s was accepted", dest)
		}
		if strings.Contains(stdout, plaintextMarker) {
			t.Errorf("--out %s put plaintext on standard output", dest)
		}
	}
}

// Wrong shapes of the command are usage errors that decrypt nothing.
func TestDecryptRefusesAnIncompleteCommand_1289(t *testing.T) {
	f := newDecryptFixture(t)
	for name, args := range map[string][]string{
		"no key source":         {"encryption", "decrypt", f.object, "--sidecar", f.sidecar, "--out", f.out},
		"two key sources":       {"encryption", "decrypt", f.object, "--bundle", f.bundle, "--identity", f.bundle, "--sidecar", f.sidecar, "--out", f.out},
		"no digest source":      {"encryption", "decrypt", f.object, "--bundle", f.bundle, "--out", f.out},
		"two digest sources":    {"encryption", "decrypt", f.object, "--bundle", f.bundle, "--sidecar", f.sidecar, "--digest", f.plainHash, "--out", f.out},
		"no destination":        {"encryption", "decrypt", f.object, "--bundle", f.bundle, "--sidecar", f.sidecar},
		"no object":             {"encryption", "decrypt", "--bundle", f.bundle, "--sidecar", f.sidecar, "--out", f.out},
		"a malformed digest":    {"encryption", "decrypt", f.object, "--bundle", f.bundle, "--digest", "not-hex", "--out", f.out},
		"stored digest, no key": {"encryption", "decrypt", f.object, "--stored-digest", f.storedHash, "--digest", f.plainHash, "--out", f.out},
	} {
		code, _, stderr := runAll(t, args...)
		if code != exitUsage {
			t.Errorf("%s: exited %d, want %d\n%s", name, code, exitUsage, stderr)
		}
		if names := dirNames(t, f.outDir); len(names) != 0 {
			t.Errorf("%s: left %v in the destination", name, names)
		}
	}
}

// A bundle that was edited is refused before any key is used.
func TestDecryptRefusesABundleWhoseIntegrityDigestFails_1289(t *testing.T) {
	f := newDecryptFixture(t)
	raw, err := os.ReadFile(f.bundle)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), f.keyID, "k0000000000000000", 1)
	if edited == string(raw) {
		t.Fatal("fixture: the key id is not in the bundle")
	}
	if err := os.WriteFile(f.bundle, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAll(t, f.decryptArgs()...)
	if code != exitFailure || !strings.Contains(stderr, "integrity") {
		t.Fatalf("exited %d, stderr %q, want a refusal naming the bundle's integrity digest", code, stderr)
	}
	if names := dirNames(t, f.outDir); len(names) != 0 {
		t.Errorf("left %v in the destination", names)
	}
}

// The control for the scans above: they can fail. A surface that carries the
// key, in any of the renderings, or the plaintext, is found; a clean one is
// not. Without this, every "carries nothing" assertion in this file would
// also pass over a scan that looks for nothing.
func TestTheLeakScanCanFail_1289(t *testing.T) {
	f := decryptFixture{material: "AGE-SECRET-KEY-1CANARYCANARYCANARYCANARYCANARYCANARYCANARYCANARYCANARY"}
	for name, surface := range map[string]string{
		"the key as is":     "decrypting with " + f.material,
		"the key in base64": "k=" + b64(f.material),
		"the key in hex":    "k=" + hex.EncodeToString([]byte(f.material)),
		"the plaintext":     "first bytes: " + plaintextMarker + "abc",
	} {
		if len(f.leaks(surface)) == 0 {
			t.Errorf("the scan missed a planted leak: %s", name)
		}
	}
	if l := f.leaks("decrypted 123 bytes to /tmp/out, digest abc"); len(l) != 0 {
		t.Errorf("the scan flags a clean line: %v", l)
	}
}

// Import: a clean deployment registers the key from the bundle, without
// printing it, and says so plainly when run again.
func TestKeyImportRegistersTheBundleAndIsIdempotent_1289(t *testing.T) {
	f := newDecryptFixture(t)
	clean := writeTestConfig(t) // a second deployment that has never seen the key

	code, stdout, stderr := runAll(t, "encryption", "key", "import", "--bundle", f.bundle, "--config", clean)
	if code != exitOK {
		t.Fatalf("import exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	for name, surface := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if carriesAnyRendering(surface, f.material) {
			t.Errorf("import %s carries the key:\n%s", name, surface)
		}
	}
	if !strings.Contains(stdout, f.keyID) {
		t.Errorf("import does not say which key it registered:\n%s", stdout)
	}

	keyFile := filepath.Join(keycustody.DirBeside(clean), f.keyID+".key")
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("no key file after import: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the imported key file is %v, want 0600", info.Mode().Perm())
	}
	if got := keyMaterialIn(t, clean); got != f.material {
		t.Errorf("the imported key file does not hold the exported key")
	}

	_, listed, _ := runAll(t, "encryption", "key", "list", "--json", "--config", clean)
	var list apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(listed), &list); err != nil || len(list.Keys) != 1 {
		t.Fatalf("key list after import: %v\n%s", err, listed)
	}
	k := list.Keys[0]
	if k.ID != f.keyID || k.Status != "superseded" || !k.Resolvable || !k.BundleConfirmed {
		t.Errorf("the imported key reads %+v, want %s superseded, resolvable and confirmed", k, f.keyID)
	}

	// Again: the same exit status, the same single key, the same file.
	code, stdout, stderr = runAll(t, "encryption", "key", "import", "--bundle", f.bundle, "--config", clean)
	if code != exitOK {
		t.Fatalf("second import exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "already") {
		t.Errorf("the second import does not say the key was already registered:\n%s", stdout)
	}
	_, listed, _ = runAll(t, "encryption", "key", "list", "--json", "--config", clean)
	var again apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(listed), &again); err != nil || len(again.Keys) != 1 {
		t.Fatalf("key list after a second import: %v\n%s", err, listed)
	}
	if files, _ := filepath.Glob(filepath.Join(keycustody.DirBeside(clean), "*")); len(files) != 1 {
		t.Errorf("the key directory holds %v after two imports, want one file", files)
	}
}

// Import takes what the export verb wrote and refuses what it did not.
func TestKeyImportRefusesABundleThatWasEditedAndRegistersNothing_1289(t *testing.T) {
	f := newDecryptFixture(t)
	clean := writeTestConfig(t)
	raw, err := os.ReadFile(f.bundle)
	if err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(t.TempDir(), "edited.json")
	if err := os.WriteFile(edited, []byte(strings.Replace(string(raw), f.keyID, "k0000000000000000", 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runAll(t, "encryption", "key", "import", "--bundle", edited, "--config", clean)
	if code != exitFailure {
		t.Fatalf("exited %d, want %d", code, exitFailure)
	}
	if carriesAnyRendering(stdout+stderr, f.material) {
		t.Errorf("the refusal carries the key")
	}
	if files, _ := filepath.Glob(filepath.Join(keycustody.DirBeside(clean), "*.key")); len(files) != 0 {
		t.Errorf("a refused import left %v", files)
	}
	_, listed, _ := runAll(t, "encryption", "key", "list", "--json", "--config", clean)
	var list apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(listed), &list); err != nil || len(list.Keys) != 0 {
		t.Errorf("a refused import registered %+v (%v)", list.Keys, err)
	}
}

func TestKeyImportNeedsABundle_1289(t *testing.T) {
	clearTheRoute(t)
	clean := writeTestConfig(t)
	for name, args := range map[string][]string{
		"no bundle":      {"encryption", "key", "import", "--config", clean},
		"an operand":     {"encryption", "key", "import", "k0123456789abcdef", "--config", clean},
		"--out with it":  {"encryption", "key", "import", "--bundle", "x", "--out", "y", "--config", clean},
		"--bundle alone": {"encryption", "key", "list", "--bundle", "x", "--config", clean},
	} {
		if code, _, _ := runAll(t, args...); code != exitUsage {
			t.Errorf("%s: exited %d, want %d", name, code, exitUsage)
		}
	}
}

// The two verbs are in the usage text and on the reference page, so an
// operator who reads either finds them. The reference page is checked here
// because the page-parity test only compares top-level commands.
func TestTheDecryptAndImportVerbsAreDocumentedWhereOperatorsLook_1289(t *testing.T) {
	usageText := captureStderr(t, usage)
	site, err := os.ReadFile("../../../docs/site/reference.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"encryption decrypt", "encryption key import"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage() does not describe `%s`", want)
		}
	}
	for _, want := range []string{"decrypt &lt;", "key import"} {
		if !strings.Contains(string(site), want) {
			t.Errorf("reference.html does not describe %q", want)
		}
	}
	// The gap every parity guard cannot see: neither verb has an API twin,
	// and the usage text says so rather than leaving it to be assumed.
	if !strings.Contains(usageText, "no API route") {
		t.Errorf("usage() does not say these two verbs have no API route")
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Nothing the operator reads still says there is no command that decrypts a
// copy, or that the bundle's instructions are ahead of the code. Those
// sentences were true before #1289 and are the ones that would stay behind.
func TestNoPageStillSaysNoCommandDecryptsACopy_1289(t *testing.T) {
	stale := []string{
		"no `retnd` command that decrypts",
		"no `retnd` command decrypts",
		"has no command of its own that decrypts",
		"product has no command that decrypts",
		"ahead of the code today",
		"and `retnd recover` will not do it for you",
	}
	pages := []string{
		"README.md", "docs/encrypted-copies.md", "docs/recovery.md", "docs/recovery-without-a-terminal.md",
		"docs/compliance/recovery-egress.md", "docs/site/index.html", "docs/site/reference.html",
	}
	for _, page := range pages {
		raw, err := os.ReadFile(filepath.Join("../../..", page))
		if err != nil {
			t.Fatalf("%s: %v", page, err)
		}
		text := strings.Join(strings.Fields(string(raw)), " ")
		for _, phrase := range stale {
			if strings.Contains(text, phrase) {
				t.Errorf("%s still says %q", page, phrase)
			}
		}
	}

	// The changelog keeps its history, so only [Unreleased] is held to it.
	raw, err := os.ReadFile("../../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	unreleased, _, _ := strings.Cut(string(raw), "\n## [0.")
	text := strings.Join(strings.Fields(unreleased), " ")
	for _, phrase := range stale {
		if strings.Contains(text, phrase) {
			t.Errorf("CHANGELOG [Unreleased] still says %q", phrase)
		}
	}
	if !strings.Contains(text, "encryption decrypt") || !strings.Contains(text, "key import") {
		t.Errorf("CHANGELOG [Unreleased] does not mention the two new verbs")
	}
}

// The parity matrix says, in a row of its own, that the two verbs have no API
// twin and why, so the reverse direction of N1.16 is a recorded decision and
// not a gap nobody looked at.
func TestTheMatrixRecordsTheTwoVerbsWithNoAPITwin_1289(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/conformance/epic-n-matrix.md")
	if err != nil {
		t.Fatal(err)
	}
	var row string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "encryption decrypt") && strings.Contains(line, "key import") {
			row = line
		}
	}
	if row == "" {
		t.Fatal("epic-n-matrix.md has no row naming `encryption decrypt` and `key import`")
	}
	for _, want := range []string{"no API route", "encryptiondecrypt_1289_test.go"} {
		if !strings.Contains(row, want) {
			t.Errorf("the matrix row does not say %q:\n%s", want, row)
		}
	}
}
