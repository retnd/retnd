package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/keycustody"
)

// #993: the encryption verbs, driven through the real dispatcher.

// runCapturing runs one command and returns its exit code and standard
// output, with the engine route cleared so it runs here.
func runCapturing(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = run(args) })
	return code, out
}

func keyMaterialIn(t *testing.T, configPath string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(keycustody.DirBeside(configPath), "*.key"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one managed key file beside the config, found %v (%v)", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func carriesAnyRendering(surface, material string) bool {
	raw := []byte(material)
	for _, r := range []string{
		material,
		base64.StdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		hex.EncodeToString(raw),
	} {
		if strings.Contains(surface, r) {
			return true
		}
	}
	return false
}

func TestEncryptionIsADispatchedVerb_993(t *testing.T) {
	if _, ok := commands["encryption"]; !ok {
		t.Fatal("encryption is not in the dispatch table, so the reference page, the usage block and the echo have nothing to name")
	}
}

func TestEveryEncryptionVerbsJSONCarriesNoMaterialAndACanaryInAnyFieldIsFound_993(t *testing.T) {
	clearTheRoute(t)
	configPath := writeTestConfig(t)

	if code, out := runCapturing(t, "encryption", "key", "create", "--json", "--config", configPath); code != exitOK {
		t.Fatalf("key create exited %d: %s", code, out)
	}
	material := keyMaterialIn(t, configPath)

	for _, args := range [][]string{
		{"encryption", "key", "list", "--json", "--config", configPath},
		{"encryption", "policy", "--json", "--config", configPath},
	} {
		code, out := runCapturing(t, args...)
		if code != exitOK {
			t.Errorf("%v exited %d: %s", args, code, out)
		}
		if carriesAnyRendering(out, material) {
			t.Errorf("%v prints the key's material:\n%s", args, out)
		}
	}

	// The key status carries ids, timestamps, status and resolvability.
	_, out := runCapturing(t, "encryption", "key", "list", "--json", "--config", configPath)
	var list apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Keys) != 1 {
		t.Fatalf("key list --json is not the API's object: %v\n%s", err, out)
	}
	k := list.Keys[0]
	if k.ID == "" || k.Status != "active" || k.CreatedAt == "" || !k.Resolvable {
		t.Errorf("the status lacks what it is for: %+v", k)
	}

	// The control, one field at a time.
	typ := reflect.TypeOf(k)
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() != reflect.String {
			continue
		}
		planted := k
		reflect.ValueOf(&planted).Elem().Field(i).SetString(material)
		raw, _ := json.Marshal(planted)
		if !carriesAnyRendering(string(raw), material) {
			t.Errorf("a canary planted in %s was not found by the scan", typ.Field(i).Name)
		}
	}
}

func TestAnExportedBundleGoesToAFileCreatedPrivateAndIsNeverPrinted_993(t *testing.T) {
	clearTheRoute(t)
	configPath := writeTestConfig(t)
	if code, out := runCapturing(t, "encryption", "key", "create", "--config", configPath); code != exitOK {
		t.Fatalf("key create exited %d: %s", code, out)
	}
	material := keyMaterialIn(t, configPath)
	_, listed := runCapturing(t, "encryption", "key", "list", "--json", "--config", configPath)
	var list apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(listed), &list); err != nil {
		t.Fatal(err)
	}
	id := list.Keys[0].ID
	dest := filepath.Join(t.TempDir(), "bundle.json")

	code, out := runCapturing(t, "encryption", "key", "export", id, "--out", dest, "--config", configPath)
	if code != exitOK {
		t.Fatalf("export exited %d: %s", code, out)
	}
	if carriesAnyRendering(out, material) {
		t.Errorf("the export printed the material:\n%s", out)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("no bundle file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the bundle file is %v, want 0600", info.Mode().Perm())
	}
	if raw, _ := os.ReadFile(dest); !strings.Contains(string(raw), material) {
		t.Error("the bundle file does not hold the key it exists to carry")
	}

	// Never over a file that exists, and a refusal records no second export.
	if code, _ := runCapturing(t, "encryption", "key", "export", id, "--out", dest, "--config", configPath); code == exitOK {
		t.Error("an export overwrote a file that was already there")
	}
	// No --out, no export: there is no way to print one.
	if code, _ := runCapturing(t, "encryption", "key", "export", id, "--config", configPath); code != exitUsage {
		t.Errorf("an export with no --out exited %d, want %d", code, exitUsage)
	}
}

func TestAConfirmationNeedsAnExportAndThenClearsTheDegradedState_993(t *testing.T) {
	clearTheRoute(t)
	configPath := writeTestConfig(t)
	runCapturing(t, "encryption", "key", "create", "--config", configPath)
	_, listed := runCapturing(t, "encryption", "key", "list", "--json", "--config", configPath)
	var list apicontract.ListEncryptionKeysResponse
	if err := json.Unmarshal([]byte(listed), &list); err != nil {
		t.Fatal(err)
	}
	id := list.Keys[0].ID

	if code, _ := runCapturing(t, "encryption", "key", "confirm", id, "--config", configPath); code != exitFailure {
		t.Errorf("confirming with nothing exported exited %d, want %d", code, exitFailure)
	}
	dest := filepath.Join(t.TempDir(), "bundle.json")
	runCapturing(t, "encryption", "key", "export", id, "--out", dest, "--config", configPath)
	code, out := runCapturing(t, "encryption", "key", "confirm", id, "--json", "--config", configPath)
	if code != exitOK {
		t.Fatalf("confirm exited %d: %s", code, out)
	}
	var got apicontract.EncryptionKeyStatus
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.BundleConfirmed {
		t.Errorf("after confirming, the key reads %+v (%v)", got, err)
	}
}

func TestAProviderThatDoesNotResolveExitsNonZeroAndNamesItsSourceNotItsContent_993(t *testing.T) {
	clearTheRoute(t)
	configPath := writeTestConfig(t)
	missing := filepath.Join(t.TempDir(), "no-such-key")
	code, out := runCapturing(t, "encryption", "provider", "test", "--external-file", missing, "--json", "--config", configPath)
	if code != exitFailure {
		t.Errorf("a provider that does not resolve exited %d, want %d", code, exitFailure)
	}
	var probe apicontract.EncryptionProviderProbe
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatalf("not the API's object: %v\n%s", err, out)
	}
	if probe.State == "resolves" || !strings.Contains(probe.Source, missing) {
		t.Errorf("probe = %+v", probe)
	}
}

func TestRequiredWithNoProviderIsRefusedAndTheFileIsUntouched_993(t *testing.T) {
	clearTheRoute(t)
	configPath := writeTestConfig(t)
	before := readFile(t, configPath)
	code, _ := runCapturing(t, "encryption", "policy", "patch", "--mode", "required", "--config", configPath)
	if code != exitFailure {
		t.Errorf("exited %d, want %d", code, exitFailure)
	}
	if readFile(t, configPath) != before {
		t.Error("a refused patch changed config.yaml")
	}
}

func TestThePolicyFlagsOnAReadAreRefusedRatherThanIgnored_993(t *testing.T) {
	clearTheRoute(t)
	configPath := writeTestConfig(t)
	if code, _ := runCapturing(t, "encryption", "policy", "--mode", "required", "--config", configPath); code != exitUsage {
		t.Errorf("a flag that writes, on a command that reads, exited %d, want %d", code, exitUsage)
	}
}
