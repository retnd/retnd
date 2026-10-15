package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/retnd/retnd/core/apicontract"
)

// Issue #1153: the SSH key store and the host-key probe from a terminal.
//
// `--ssh-key-id` took an id nothing on the terminal would print. The store
// belongs to the engine, so beside one these are the same calls the browser
// makes, and with nothing serving they act on this deployment's own store.
// No key material and no passphrase ever reaches argv, an output stream or a
// log line; both arrive as a file and a stdin line.

const keyPassphrase1153 = "key-passphrase-1153-not-real"

type key1153 struct {
	path        string
	fingerprint string
	body        string // a stretch of the PEM base64 that must never be printed
}

// writeKey1153 writes a throwaway ed25519 private key, protected by
// passphrase when one is given.
func writeKey1153(t *testing.T, dir, name, passphrase string) key1153 {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "ssh-key test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "ssh-key test", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	encoded := pem.EncodeToMemory(block)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(encoded)), "\n")
	return key1153{path: path, fingerprint: ssh.FingerprintSHA256(sshPub), body: lines[2]}
}

func noMaterial1153(t *testing.T, what string, k key1153, streams ...string) {
	t.Helper()
	for _, s := range streams {
		for _, secret := range []string{k.body, keyPassphrase1153, "PRIVATE KEY"} {
			if secret != "" && strings.Contains(s, secret) {
				t.Errorf("%s: key material or a passphrase reached an output stream: %q", what, s)
			}
		}
	}
}

var importedID1153 = regexp.MustCompile(`as (\S+)`)

func idFrom1153(t *testing.T, stdout string) string {
	t.Helper()
	m := importedID1153.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("the import output does not name the id the key was stored under (`... as <id>`): %q", stdout)
	}
	return m[1]
}

func body1153(out string) string {
	if i := strings.LastIndex(out, "\n{\n"); i >= 0 {
		return out[i+1:]
	}
	return out
}

func sameJSON1153(t *testing.T, got string, want any) {
	t.Helper()
	raw, _ := json.Marshal(want)
	var w, g map[string]any
	_ = json.Unmarshal(raw, &w)
	if err := json.Unmarshal([]byte(body1153(got)), &g); err != nil {
		t.Fatalf("--json did not print a JSON object: %v\n%s", err, got)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("--json is not the object the API returned, field for field\n got %v\nwant %v", g, w)
	}
}

// ------------------------------------------------------------ direct ---

func TestSSHKeyImportListAndNoMaterial1153_Direct(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	k := writeKey1153(t, t.TempDir(), "id_ed25519", "")

	code, stdout, stderr := runAuth1155(t, "", "ssh-key", "import", "--file", k.path, "--config", cfg)
	if code != 0 {
		t.Fatalf("ssh-key import exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, k.fingerprint) {
		t.Errorf("the import does not print the fingerprint to compare: %q", stdout)
	}
	id := idFrom1153(t, stdout)
	noMaterial1153(t, "ssh-key import", k, stdout, stderr)

	code, listOut, stderr := runAuth1155(t, "", "ssh-key", "list", "--config", cfg)
	if code != 0 {
		t.Fatalf("ssh-key list exited %d\n%s", code, stderr)
	}
	for _, want := range []string{id, "ssh-ed25519", k.fingerprint} {
		if !strings.Contains(listOut, want) {
			t.Errorf("ssh-key list does not print %q:\n%s", want, listOut)
		}
	}
	noMaterial1153(t, "ssh-key list", k, listOut, stderr)

	code, jsonOut, stderr := runAuth1155(t, "", "ssh-key", "list", "--json", "--config", cfg)
	if code != 0 {
		t.Fatalf("ssh-key list --json exited %d\n%s", code, stderr)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal([]byte(body1153(jsonOut)), &doc); err != nil || len(doc.Keys) != 1 {
		t.Fatalf("--json = %q (%v), want one key", jsonOut, err)
	}
	if doc.Keys[0]["id"] != id || doc.Keys[0]["fingerprint"] != k.fingerprint {
		t.Errorf("--json row = %v", doc.Keys[0])
	}
	if used, ok := doc.Keys[0]["used_by"].([]any); !ok || len(used) != 0 {
		t.Errorf("used_by = %v, want an empty array", doc.Keys[0]["used_by"])
	}
	for _, forbidden := range []string{"key_file", "path", "private_key_pem"} {
		if _, present := doc.Keys[0][forbidden]; present {
			t.Errorf("the row carries %q", forbidden)
		}
	}
}

func TestSSHKeyImportOfAPassphraseProtectedKey1153_Direct(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	k := writeKey1153(t, t.TempDir(), "id_enc", keyPassphrase1153)

	// Without the flag it is refused, and the refusal says what to do.
	code, stdout, stderr := runAuth1155(t, "", "ssh-key", "import", "--file", k.path, "--config", cfg)
	if code == 0 || code == exitUsage {
		t.Errorf("an encrypted key with no passphrase exited %d, want a plain failure", code)
	}
	if !strings.Contains(stderr, "--passphrase-stdin") {
		t.Errorf("the refusal does not name --passphrase-stdin: %q", stderr)
	}
	noMaterial1153(t, "refused import", k, stdout, stderr)

	// The wrong passphrase is refused and never echoed.
	code, stdout, stderr = runAuth1155(t, "not-the-passphrase\n", "ssh-key", "import", "--file", k.path, "--passphrase-stdin", "--config", cfg)
	if code == 0 {
		t.Error("a wrong passphrase was accepted")
	}
	if strings.Contains(stdout+stderr, "not-the-passphrase") {
		t.Errorf("a refused passphrase was echoed: %q %q", stdout, stderr)
	}

	// The right one, from stdin, imports it.
	code, stdout, stderr = runAuth1155(t, keyPassphrase1153+"\n", "ssh-key", "import", "--file", k.path, "--passphrase-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("import with the passphrase on stdin exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, k.fingerprint) {
		t.Errorf("fingerprint missing from %q", stdout)
	}
	noMaterial1153(t, "passphrase import", k, stdout, stderr)
}

func TestSSHKeyImportNeverTakesMaterialOrAPassphraseFromArgv1153(t *testing.T) {
	cfg := writeTestConfig(t)
	k := writeKey1153(t, t.TempDir(), "id_ed25519", "")
	for name, argv := range map[string][]string{
		"nothing to import":            {"ssh-key", "import"},
		"both a file and a candidate":  {"ssh-key", "import", "--file", k.path, "--candidate", "cand_1"},
		"a passphrase flag":            {"ssh-key", "import", "--file", k.path, "--passphrase", keyPassphrase1153},
		"the key itself as a flag":     {"ssh-key", "import", "--private-key", k.body},
		"a passphrase for a candidate": {"ssh-key", "import", "--candidate", "cand_1", "--passphrase-stdin"},
		"an operand":                   {"ssh-key", "import", "--file", k.path, "extra"},
		"list with an operand":         {"ssh-key", "list", "extra"},
		"discover with an operand":     {"ssh-key", "discover", "extra"},
		"no subcommand":                {"ssh-key"},
		"an unknown subcommand":        {"ssh-key", "frobnicate"},
	} {
		code, stdout, stderr := runAuth1155(t, keyPassphrase1153+"\n", append(argv, "--config", cfg)...)
		if code != exitUsage {
			t.Errorf("%s: exited %d, want the usage code", name, code)
		}
		noMaterial1153(t, name, k, stdout, stderr)
	}
}

func TestSSHKeyDiscoverAndImportACandidate1153_Direct(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	dir := t.TempDir()
	k := writeKey1153(t, dir, "id_ed25519", "")
	t.Setenv("RETND_SSH_DISCOVERY_DIR", dir)

	code, stdout, stderr := runAuth1155(t, "", "ssh-key", "discover", "--config", cfg)
	if code != 0 {
		t.Fatalf("ssh-key discover exited %d\n%s", code, stderr)
	}
	for _, want := range []string{dir, k.fingerprint} {
		if !strings.Contains(stdout, want) {
			t.Errorf("ssh-key discover does not print %q:\n%s", want, stdout)
		}
	}
	noMaterial1153(t, "ssh-key discover", k, stdout, stderr)

	code, jsonOut, stderr := runAuth1155(t, "", "ssh-key", "discover", "--json", "--config", cfg)
	if code != 0 {
		t.Fatalf("discover --json exited %d\n%s", code, stderr)
	}
	var doc apicontract.ListSSHKeyCandidatesResponse
	if err := json.Unmarshal([]byte(body1153(jsonOut)), &doc); err != nil {
		t.Fatalf("%v\n%s", err, jsonOut)
	}
	var candidate string
	for _, c := range doc.Candidates {
		if c.Path == k.path {
			candidate = c.ID
		}
	}
	if candidate == "" {
		t.Fatalf("the scan did not offer %s: %+v", k.path, doc.Candidates)
	}

	code, stdout, stderr = runAuth1155(t, "", "ssh-key", "import", "--candidate", candidate, "--config", cfg)
	if code != 0 {
		t.Fatalf("import --candidate exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, k.fingerprint) {
		t.Errorf("fingerprint missing from %q", stdout)
	}
	noMaterial1153(t, "import --candidate", k, stdout, stderr)

	// A stale or invented handle is refused by the same rule the API uses.
	code, _, stderr = runAuth1155(t, "", "ssh-key", "import", "--candidate", "cand_nonesuch", "--config", cfg)
	if code == 0 || code == exitUsage {
		t.Errorf("an unknown candidate exited %d, want a plain failure", code)
	}
	if !strings.Contains(stderr, "scan again") && !strings.Contains(stderr, "discover") {
		t.Errorf("the refusal does not say to scan again: %q", stderr)
	}
}

// ------------------------------------------------------------ routed ---

func TestSSHKeyVerbsAreRoutedToAnAttachedEngine1153(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	k := writeKey1153(t, t.TempDir(), "id_ed25519", "")

	listing := apicontract.ListSSHKeysResponse{Keys: []apicontract.SSHKey{{
		ID: "key_engine", Algorithm: "ssh-ed25519", Fingerprint: "SHA256:engineside", PublicKey: "ssh-ed25519 AAAA",
		ImportedAt: "2026-10-01T11:30:00Z", UsedBy: []string{"api/db"},
	}}}
	engine.handle("GET /ssh-keys", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, listing) })
	discovery := apicontract.ListSSHKeyCandidatesResponse{
		Locations:  []apicontract.SSHKeyDiscoveryLocation{{Path: "/etc/retnd", Kind: "mount", Found: 1}},
		Candidates: []apicontract.SSHKeyCandidate{{ID: "cand_9", Path: "/etc/retnd/id_ed25519", Location: "/etc/retnd", Algorithm: "ssh-ed25519", Fingerprint: "SHA256:cand", Selectable: true}},
	}
	engine.handle("GET /ssh/key-candidates", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, discovery) })
	cand := &recorder1155{}
	engine.handle("POST /ssh-keys/from-candidate", cand.handler(http.StatusCreated,
		apicontract.ImportSSHKeyResponse{ID: "key_from_cand", Algorithm: "ssh-ed25519", Fingerprint: "SHA256:cand"}))

	cliBefore := readFile(t, cfg)

	code, out, stderr := runAuth1155(t, "", "ssh-key", "list", "--config", cfg)
	if code != 0 || !strings.Contains(out, "key_engine") || !strings.Contains(out, "api/db") {
		t.Errorf("list beside an engine exited %d and printed %q\n%s", code, out, stderr)
	}
	code, out, _ = runAuth1155(t, "", "ssh-key", "list", "--json", "--config", cfg)
	if code != 0 {
		t.Fatalf("list --json exited %d", code)
	}
	sameJSON1153(t, out, listing)

	code, out, _ = runAuth1155(t, "", "ssh-key", "discover", "--json", "--config", cfg)
	if code != 0 {
		t.Fatalf("discover --json exited %d", code)
	}
	sameJSON1153(t, out, discovery)
	code, out, _ = runAuth1155(t, "", "ssh-key", "discover", "--config", cfg)
	if code != 0 || !strings.Contains(out, "/etc/retnd") || !strings.Contains(out, "cand_9") {
		t.Errorf("discover beside an engine exited %d and printed %q", code, out)
	}

	code, out, stderr = runAuth1155(t, "", "ssh-key", "import", "--candidate", "cand_9", "--config", cfg)
	if code != 0 || !strings.Contains(out, "key_from_cand") {
		t.Errorf("import --candidate beside an engine exited %d and printed %q\n%s", code, out, stderr)
	}
	if got := cand.only(t); !strings.Contains(got, `"candidate_id":"cand_9"`) {
		t.Errorf("the engine received %s, want the opaque handle and nothing else", got)
	}

	// A pasted file goes to the ENGINE's store, never this host's.
	code, out, stderr = runAuth1155(t, "", "ssh-key", "import", "--file", k.path, "--config", cfg)
	if code != 0 {
		t.Fatalf("import --file beside an engine exited %d\n%s", code, stderr)
	}
	noMaterial1153(t, "routed import", k, out, stderr)
	keys, err := engine.svc.ListSSHKeys(t.Context())
	if err != nil || len(keys) != 1 || keys[0].Fingerprint != k.fingerprint {
		t.Errorf("the engine's store holds %+v (%v), want the imported key", keys, err)
	}
	if readFile(t, cfg) != cliBefore {
		t.Error("the CLI wrote its own config.yaml as well as handing the work to the engine")
	}
}

func TestSSHKeyImportOfAnEncryptedKeyReachesTheEngineWithItsPassphrase1153(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	k := writeKey1153(t, t.TempDir(), "id_enc", keyPassphrase1153)
	code, out, stderr := runAuth1155(t, keyPassphrase1153+"\n", "ssh-key", "import", "--file", k.path, "--passphrase-stdin", "--config", cfg)
	if code != 0 {
		t.Fatalf("exited %d\n%s", code, stderr)
	}
	noMaterial1153(t, "routed passphrase import", k, out, stderr)
	if keys, err := engine.svc.ListSSHKeys(t.Context()); err != nil || len(keys) != 1 {
		t.Errorf("the engine's store holds %+v (%v), want the key", keys, err)
	}
}

// -------------------------------------------------------- host-key probe ---

// startHostKeyServer1153 is an SSH server that does the key exchange and
// nothing else, which is all a probe asks of one.
func startHostKeyServer1153(t *testing.T) (port string, fingerprint string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				if conn, _, _, err := ssh.NewServerConn(c, cfg); err == nil {
					_ = conn.Close()
				}
			}()
		}
	}()
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	return p, ssh.FingerprintSHA256(signer.PublicKey())
}

func TestSSHHostKeyProbePrintsWhatAnsweredAndTrustsNothing1153_Direct(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	port, fingerprint := startHostKeyServer1153(t)
	before := readFile(t, cfg)

	code, stdout, stderr := runAuth1155(t, "", "ssh", "host-key-probe", "127.0.0.1", "--port", port, "--config", cfg)
	if code != 0 {
		t.Fatalf("ssh host-key-probe exited %d\n%s", code, stderr)
	}
	for _, want := range []string{fingerprint, "ssh-ed25519", "127.0.0.1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the probe does not print %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "nothing was trusted") {
		t.Errorf("the output does not say that nothing was trusted: %q", stdout)
	}
	if readFile(t, cfg) != before {
		t.Error("a probe changed config.yaml")
	}

	code, jsonOut, _ := runAuth1155(t, "", "ssh", "host-key-probe", "127.0.0.1", "--port", port, "--json", "--config", cfg)
	if code != 0 {
		t.Fatalf("--json exited %d", code)
	}
	var wire apicontract.HostKeyProbeResponse
	if err := json.Unmarshal([]byte(body1153(jsonOut)), &wire); err != nil || wire.Fingerprint != fingerprint || wire.KnownHostsLine == "" {
		t.Errorf("--json = %q (%v), want the wire object with the known_hosts line", jsonOut, err)
	}
}

func TestSSHHostKeyProbeIsRoutedAndDefaultsToPort221153(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	rec := &recorder1155{}
	reply := apicontract.HostKeyProbeResponse{Algorithm: "ssh-ed25519", Fingerprint: "SHA256:enginesaw", KnownHostsLine: "src.example ssh-ed25519 AAAA"}
	engine.handle("POST /ssh/host-key-probe", rec.handler(http.StatusOK, reply))
	before := readFile(t, cfg)

	code, stdout, stderr := runAuth1155(t, "", "ssh", "host-key-probe", "src.example", "--config", cfg)
	if code != 0 {
		t.Fatalf("exited %d\n%s", code, stderr)
	}
	var sent apicontract.HostKeyProbeRequest
	if err := json.Unmarshal([]byte(rec.only(t)), &sent); err != nil || sent.Host != "src.example" || sent.Port != 22 {
		t.Errorf("the engine received %+v (%v), want host src.example on port 22", sent, err)
	}
	if !strings.Contains(stdout, "SHA256:enginesaw") || !strings.Contains(stdout, "src.example ssh-ed25519 AAAA") {
		t.Errorf("the engine's answer is not printed:\n%s", stdout)
	}
	if readFile(t, cfg) != before {
		t.Error("a probe changed config.yaml")
	}
	for _, r := range engine.requests() {
		if strings.Contains(r, "/backup-sets") {
			t.Errorf("a probe touched %s", r)
		}
	}

	code, jsonOut, _ := runAuth1155(t, "", "ssh", "host-key-probe", "src.example", "--json", "--config", cfg)
	if code != 0 {
		t.Fatal("--json failed")
	}
	sameJSON1153(t, jsonOut, reply)
}

func TestSSHHostKeyProbeUsageAndFailure1153(t *testing.T) {
	cfg := writeTestConfig(t)
	for name, argv := range map[string][]string{
		"no host":             {"ssh", "host-key-probe"},
		"two hosts":           {"ssh", "host-key-probe", "a.example", "b.example"},
		"a port out of range": {"ssh", "host-key-probe", "a.example", "--port", "70000"},
		"no subcommand":       {"ssh"},
		"an unknown one":      {"ssh", "frobnicate"},
	} {
		if code, _, _ := runAuth1155(t, "", append(argv, "--config", cfg)...); code != exitUsage {
			t.Errorf("%s: exited %d, want the usage code", name, code)
		}
	}
	// Nothing answers on a closed port: a plain failure naming the target.
	clearInheritedRouteSettings()
	port, _ := startHostKeyServer1153(t)
	closed := func() string {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		_, p, _ := net.SplitHostPort(ln.Addr().String())
		_ = ln.Close()
		return p
	}()
	_ = port
	code, _, stderr := runAuth1155(t, "", "ssh", "host-key-probe", "127.0.0.1", "--port", closed, "--config", cfg)
	if code == 0 || code == exitUsage || !strings.Contains(stderr, "127.0.0.1") {
		t.Errorf("a probe of a closed port exited %d: %q", code, stderr)
	}
}

// ------------------------------------------- --ssh-key-file with a passphrase ---

// The audit did not know what `--ssh-key-file` does with a passphrase-
// protected key. It passed "" as the passphrase, so the service refused it
// with no way for the operator to supply one. --ssh-key-passphrase-stdin is
// that way.
func TestBackupSetCreateWithAPassphraseProtectedKeyFile1153(t *testing.T) {
	clearInheritedRouteSettings()
	cfg := writeTestConfig(t)
	k := writeKey1153(t, t.TempDir(), "id_enc", keyPassphrase1153)

	args := createArgs(cfg, k.path, "api/encrypted")
	code, stdout, stderr := runAuth1155(t, "", args...)
	if code == 0 {
		t.Fatal("an encrypted key with no passphrase created a set")
	}
	if !strings.Contains(stderr, "--ssh-key-passphrase-stdin") {
		t.Errorf("the refusal does not name --ssh-key-passphrase-stdin: %q", stderr)
	}
	noMaterial1153(t, "refused create", k, stdout, stderr)

	args = createArgs(cfg, k.path, "api/encrypted", "--ssh-key-passphrase-stdin")
	code, stdout, stderr = runAuth1155(t, keyPassphrase1153+"\n", args...)
	if code != 0 {
		t.Fatalf("create with the passphrase on stdin exited %d\n%s", code, stderr)
	}
	noMaterial1153(t, "create with a passphrase", k, stdout, stderr)
	if cfgText := readFile(t, cfg); strings.Contains(cfgText, keyPassphrase1153) {
		t.Error("the passphrase was written into config.yaml")
	}
	if !strings.Contains(readFile(t, cfg), "api/encrypted") && !strings.Contains(readFile(t, cfg), "encrypted") {
		t.Error("the set was not created")
	}

	// A passphrase as a flag is a usage error, and is not echoed.
	args = createArgs(cfg, k.path, "api/other", "--ssh-key-passphrase", keyPassphrase1153)
	code, stdout, stderr = runAuth1155(t, "", args...)
	if code != exitUsage {
		t.Errorf("a passphrase flag exited %d, want the usage code", code)
	}
	noMaterial1153(t, "passphrase flag", k, stdout, stderr)

	// The flag means nothing without a key file to unlock.
	args = createArgs(cfg, k.path, "api/third", "--ssh-key-id", "key_x", "--ssh-key-passphrase-stdin")
	if code, _, _ := runAuth1155(t, keyPassphrase1153+"\n", args...); code != exitUsage {
		t.Errorf("--ssh-key-passphrase-stdin beside --ssh-key-id and --ssh-key-file exited %d, want the usage code", code)
	}
}

func TestBackupSetCreateWithAPassphraseProtectedKeyFileIsRouted1153(t *testing.T) {
	engine, cfg := attachedEngine1155(t)
	k := writeKey1153(t, t.TempDir(), "id_enc", keyPassphrase1153)
	args := createArgs(cfg, k.path, "api/encrypted", "--ssh-key-passphrase-stdin")
	code, stdout, stderr := runAuth1155(t, keyPassphrase1153+"\n", args...)
	if code != 0 {
		t.Fatalf("exited %d\n%s", code, stderr)
	}
	noMaterial1153(t, "routed create", k, stdout, stderr)
	keys, err := engine.svc.ListSSHKeys(t.Context())
	if err != nil || len(keys) != 1 {
		t.Errorf("the engine's store holds %+v (%v), want the unlocked key", keys, err)
	}
}
