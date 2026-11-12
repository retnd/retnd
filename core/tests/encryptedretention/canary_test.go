package encryptedretention_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/agenttools"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/metrics"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/recovery"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/service"
)

// The canary corpus (#996, N2.6): a leak planted into one surface turns that
// surface's matrix row red and no other, and every surface keeps a control
// that proves its detector can see a planted value. The surfaces here are the
// ones a process in core/ can produce; CLI JSON (NV.23) is
// core/cmd/retnd/encryption_equivalence_996_test.go, an API response (NV.24)
// is apps/common/webhost/encryption_996_test.go (core may not import apps),
// and a generated screenshot (NV.32) is #998's.
//
// The material scanned for is read from the key files the real service
// minted, at run time. The planted canary is generated at run time too; no
// key or canary value is in this file, in a commit message or in a fixture.
//
// Written by reading the packages it drives; not run.

// ageIdentityShape is what an age identity looks like on a page. The bare
// prefix is not enough: the refusal for a secret that is not an identity
// quotes "AGE-SECRET-KEY-1" as what it expected, and that sentence is allowed.
var ageIdentityShape = regexp.MustCompile(`AGE-SECRET-KEY-(PQ-)?1[A-Z0-9]{20,}`)

// renderings is every way material could plausibly reach a text surface.
func renderings(m string) []string {
	raw := []byte(m)
	return []string{
		m,
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		hex.EncodeToString(raw),
	}
}

func carries(surface []byte, materials []string) bool {
	if ageIdentityShape.Match(surface) {
		return true
	}
	for _, m := range materials {
		for _, r := range renderings(m) {
			if bytes.Contains(surface, []byte(r)) {
				return true
			}
		}
	}
	return false
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.b.Bytes())
}

// world is one deployment that has really enrolled a key, exported and
// confirmed its bundle, rotated, tested a provider, retired the old key and
// been refused twice, through the same service the engine runs.
type world struct {
	t         *testing.T
	ctx       context.Context
	dir       string
	cfg       string
	dbPath    string
	log       *syncBuffer
	svc       *service.BackupService
	keyIDs    []string
	materials []string
	bundle    []byte
	refusals  []string
	answers   []byte
	// agentToken is the plaintext of a token minted in the world (#1348). It
	// is a credential, so it joins materials: no surface may carry it either.
	agentToken string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	remote, local := filepath.Join(dir, "remote"), filepath.Join(dir, "local")
	for _, d := range []string{remote, local} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(remote, "backup.dump"), []byte("canary corpus payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "state.db")
	cfg := filepath.Join(dir, "config.yaml")
	yaml := "poll_interval: 15m\nstate:\n  database: " + dbPath + "\nsources:\n  - id: production\n    backup_sets:\n" +
		"      - id: postgres-primary\n        remote:\n          type: local\n        remote_path: " + remote + "\n" +
		"        local_path: " + local + "\n        include:\n          - \"*.dump\"\n        completion:\n" +
		"          strategy: rename\n        stale_after: 24h\nretention:\n  timezone: UTC\n  week_starts_on: monday\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &world{t: t, ctx: ctx, dir: dir, cfg: cfg, dbPath: dbPath, log: &syncBuffer{}}
	svc, closeFn, err := service.OpenWithLog(ctx, cfg, w.log)
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	t.Cleanup(func() { _ = closeFn() })
	w.svc = svc

	var answers []any
	must := func(what string, v any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		answers = append(answers, v)
	}
	refused := func(what string, v any, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s was accepted, so no refusal was captured", what)
		}
		w.refusals = append(w.refusals, err.Error())
	}

	k1, err := svc.CreateEncryptionKey(ctx, "")
	must("create", k1, err)
	pol, err := svc.PatchEncryptionPolicy(ctx, apicontract.EncryptionPolicyPatch{Mode: "required", KeyProvider: &apicontract.EncryptionKeyProvider{Managed: true}})
	must("policy", pol, err)
	w.bundle, err = svc.ExportRecoveryBundleBytes(ctx, k1.ID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	conf, err := svc.ConfirmRecoveryBundle(ctx, k1.ID, "")
	must("confirm", conf, err)
	k2, err := svc.RotateEncryptionKey(ctx, "")
	must("rotate", k2, err)
	probe, err := svc.TestEncryptionProvider(ctx, apicontract.EncryptionProviderTestRequest{ManagedKeyID: k2.ID})
	must("provider test", probe, err)
	_, err = svc.RetireEncryptionKey(ctx, k2.ID)
	refused("retiring the active key", nil, err)
	_, err = svc.ConfirmRecoveryBundle(ctx, "knosuchkey", "")
	refused("confirming an unknown key", nil, err)
	retired, err := svc.RetireEncryptionKey(ctx, k1.ID)
	must("retire the superseded key", retired, err)
	list, err := svc.ListEncryptionKeys(ctx)
	must("list", list, err)
	w.keyIDs = []string{k1.ID, k2.ID}

	// EPIC U (#1348): an agent token and a trail of calls built from what the
	// key surfaces really said, so the token list and the audit page have
	// real rows to be scanned. The calls are a read, a mutation and a
	// refusal, over the three transports.
	minted, err := svc.MintAgentToken(ctx, service.MintAgentTokenRequest{Name: "triage", Owner: "cli"})
	must("mint an agent token", minted.AgentToken, err)
	w.agentToken = minted.Token
	for _, c := range []service.AgentCall{
		{TokenID: minted.AgentToken.ID, TokenName: "triage", Tool: "encryption_state", Transport: "stdio", Status: 200, CorrelationID: "cid_canary_1",
			Arguments: map[string]string{"key_id": k2.ID, "provider": keycustody.Provider{Managed: k2.ID}.Describe()}},
		{TokenID: minted.AgentToken.ID, TokenName: "triage", Tool: "run_backup_set", Transport: "http", Mutating: true, Status: 202, CorrelationID: "cid_canary_2",
			BackupSetID: "production/postgres-primary", Arguments: map[string]string{"backup_set": "production/postgres-primary"}},
		{TokenID: minted.AgentToken.ID, TokenName: "triage", Transport: "api", Refused: true, Status: 403, Code: "AGENT_SCOPE_INSUFFICIENT", CorrelationID: "cid_canary_3",
			Arguments: map[string]string{"method": "POST", "path": "/backup-sets"}},
	} {
		if err := svc.RecordAgentCall(ctx, c); err != nil {
			t.Fatalf("recording an agent call: %v", err)
		}
	}
	// ListAgentAudit waits for the writer, so the rows are on disk before the
	// journal row below is scanned.
	if _, _, err := svc.ListAgentAudit(ctx, service.AgentAuditQuery{}); err != nil {
		t.Fatalf("flushing the audit trail: %v", err)
	}
	w.answers, err = json.Marshal(answers)
	if err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob(filepath.Join(keycustody.DirBeside(cfg), "*.key"))
	if err != nil || len(files) != 2 {
		t.Fatalf("expected the two managed key files, found %v (%v)", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		w.materials = append(w.materials, strings.TrimSpace(string(raw)))
	}

	w.materials = append(w.materials, w.agentToken)

	// A journal transition whose detail is built from what the operator-facing
	// surfaces really said: the refusals' own text and a provider's description.
	j, err := state.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer func() { _ = j.Close() }()
	set, err := model.NewBackupSetID("production", "postgres-primary")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := model.NewArtifactID(set, "backup-2026-11-01.dump")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if _, err := j.Discover(ctx, artifact, "discover-1", "/incoming/backup.dump", state.RemoteIdentity{}, at); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	detail := fmt.Sprintf("encrypting under %s; %s; %s", k2.ID, keycustody.Provider{Managed: k2.ID}.Describe(), strings.Join(w.refusals, "; "))
	if _, err := j.RecordTransition(ctx, state.Transition{
		Artifact: artifact, Key: "encrypt-attempt-1", From: "DISCOVERED", To: "FAILED", Detail: detail, OccurredAt: at.Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordTransition: %v", err)
	}
	return w
}

// surface is one row of the corpus: the bytes the real producers made on one
// surface, a marker proving the collector reached something real, and the
// detector that row uses.
type surface struct {
	row, name string
	marker    func(w *world) string
	collect   func(w *world) []byte
	leaks     func(w *world, b []byte) bool
}

func anyMaterial(w *world, b []byte) bool { return carries(b, w.materials) }

// materialOrShape is the detector of the agent rows (UV.16): the key files'
// own material in every rendering, and the shapes the projection looks for
// (an identity, a recipient of either suite, a PEM private key marker), which
// agenttools shares so a shape added for one is looked for by both.
func materialOrShape(w *world, b []byte) bool {
	if anyMaterial(w, b) {
		return true
	}
	for _, shape := range agenttools.CanaryShapes {
		if shape.Match(b) {
			return true
		}
	}
	return false
}

func walkAll(t *testing.T, dir string, skip func(path string) bool) []byte {
	t.Helper()
	var all []byte
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skip(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		all = append(all, b...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return all
}

var surfaces = []surface{
	{
		row: "NV.25", name: "the journal, state_transitions included (WAL and side files too)",
		marker: func(w *world) string { return w.keyIDs[1] },
		collect: func(w *world) []byte {
			keyDir := filepath.Join(w.dir, keycustody.DirName)
			return walkAll(w.t, w.dir, func(p string) bool { return p == keyDir })
		},
		leaks: anyMaterial,
	},
	{
		row: "NV.26", name: "the activity feed and the live ring",
		marker: func(*world) string { return "" },
		collect: func(w *world) []byte {
			events, _, err := w.svc.ListActivity(w.ctx, 500, "")
			if err != nil {
				w.t.Fatalf("ListActivity: %v", err)
			}
			live, err := w.svc.LiveActivity(w.ctx, service.LiveActivityRequest{})
			if err != nil {
				w.t.Fatalf("LiveActivity: %v", err)
			}
			feed, _ := json.Marshal(events)
			ring, _ := json.Marshal(live)
			return append(feed, ring...)
		},
		leaks: anyMaterial,
	},
	{
		row: "NV.27", name: "the metrics scrape (material, or any key id anywhere on it)",
		marker: func(*world) string { return "retnd_retained_copy_encryption_enabled" },
		collect: func(w *world) []byte {
			j, err := state.Open(w.ctx, w.dbPath)
			if err != nil {
				w.t.Fatalf("state.Open: %v", err)
			}
			defer func() { _ = j.Close() }()
			store, err := keycustody.Open(keycustody.DirBeside(w.cfg), keycustody.NewJournalRegistry(j), keycustody.AgeFormat{})
			if err != nil {
				w.t.Fatalf("keycustody.Open: %v", err)
			}
			h, err := store.Health(w.ctx, true)
			if err != nil {
				w.t.Fatalf("Health: %v", err)
			}
			r := health.NewReport(health.ProcessHealth{}, nil, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
			r.KeyCustody = h
			return []byte(metrics.Render(r))
		},
		leaks: func(w *world, b []byte) bool {
			if anyMaterial(w, b) {
				return true
			}
			for _, id := range w.keyIDs {
				if bytes.Contains(b, []byte(id)) {
					return true
				}
			}
			return false
		},
	},
	{
		row: "NV.28", name: "the log stream of the real service",
		marker:  func(*world) string { return "key" },
		collect: func(w *world) []byte { return w.log.bytes() },
		leaks:   anyMaterial,
	},
	{
		row: "NV.29", name: "the recovery sidecar of a copy really encrypted under the key",
		marker: func(w *world) string { return `"key_id": "` + w.keyIDs[1] + `"` },
		collect: func(w *world) []byte {
			j, err := state.Open(w.ctx, w.dbPath)
			if err != nil {
				w.t.Fatalf("state.Open: %v", err)
			}
			defer func() { _ = j.Close() }()
			store, err := keycustody.Open(keycustody.DirBeside(w.cfg), keycustody.NewJournalRegistry(j), keycustody.AgeFormat{})
			if err != nil {
				w.t.Fatalf("keycustody.Open: %v", err)
			}
			rec, ok, err := store.Active(w.ctx)
			if err != nil || !ok {
				w.t.Fatalf("no active key (ok=%v err=%v)", ok, err)
			}
			// The copy is sealed under the active key's own suite: since #1313
			// a first key is age-pq/v1, so assuming age/v1 here would fail
			// before the sidecar this surface is about was ever written.
			suiteID := rec.SuiteID
			if suiteID == "" {
				suiteID = copycrypt.AgeX25519ID
			}
			suite, ok := copycrypt.Lookup(suiteID)
			if !ok {
				w.t.Fatalf("the active key names suite %q, which the registry does not have", suiteID)
			}
			plain := fixture(31)
			s, err := copycrypt.EncryptToStaging(w.ctx, filepath.Join(w.t.TempDir(), "staging"), "backup-2026-11-01.dump", bytes.NewReader(plain), suite, rec.Recipient, sum(plain))
			if err != nil {
				w.t.Fatalf("EncryptToStaging: %v", err)
			}
			m := recovery.Manifest{
				FormatVersion: recovery.CurrentFormatVersion, Source: "production", BackupSet: "postgres-primary",
				ArtifactName: "backup-2026-11-01.dump", RemotePath: "incoming/backup-2026-11-01.dump",
				ReceivedTimestamp: time.Date(2026, 11, 1, 3, 5, 0, 0, time.UTC), RetentionTimestamp: time.Date(2026, 11, 1, 2, 59, 0, 0, time.UTC),
				SizeBytes: s.PlaintextSize, Checksum: s.PlaintextHash, ChecksumAlgorithm: "sha256",
				Placements: []recovery.ManifestPlacement{{
					Medium: "offsite_s3", Location: "retnd/production/postgres-primary/backup-2026-11-01.dump",
					Checksum: s.PlaintextHash, ChecksumAlgorithm: "sha256", VerificationClass: "content", Status: "ACTIVE",
					Encryption: &recovery.ManifestEncryption{
						FormatID: s.SuiteID, FormatVersion: s.Version, KeyID: rec.ID,
						PlaintextDigest: s.PlaintextHash, PlaintextDigestAlgorithm: "sha256", PlaintextSize: s.PlaintextSize,
						StoredDigest: s.StoredHash, StoredDigestAlgorithm: "sha256", StoredSize: s.StoredSize,
					},
				}},
			}
			out, err := recovery.EncodeManifest(m)
			if err != nil {
				w.t.Fatalf("EncodeManifest: %v", err)
			}
			return out
		},
		leaks: anyMaterial,
	},
	{
		row: "NV.30", name: "the bundle's human-readable and integrity metadata (everything but the material field)",
		marker: func(w *world) string { return w.keyIDs[0] },
		collect: func(w *world) []byte {
			var m map[string]any
			if err := json.Unmarshal(w.bundle, &m); err != nil {
				w.t.Fatalf("the bundle is not JSON: %v", err)
			}
			if _, ok := m["material"]; !ok {
				w.t.Fatal("the bundle has no material field, so there is nothing to leave out")
			}
			delete(m, "material")
			out, _ := json.Marshal(m)
			return out
		},
		leaks: anyMaterial,
	},
	// EPIC U (#1348, UV.16): the surfaces an agent's calls leave behind. The
	// two transports and the confirmation record are rows of their own once
	// U1.4's transports and U2.1's confirmations produce bytes to scan; these
	// two are the ones this branch can produce.
	{
		row: "UV.16 token list", name: "the agent token list, as GET /agent/tokens answers it",
		marker: func(*world) string { return `"prefix"` },
		collect: func(w *world) []byte {
			tokens, err := w.svc.ListAgentTokens(w.ctx)
			if err != nil {
				w.t.Fatalf("ListAgentTokens: %v", err)
			}
			out := apicontract.ListAgentTokensResponse{Tokens: make([]apicontract.AgentToken, 0, len(tokens))}
			for _, tk := range tokens {
				out.Tokens = append(out.Tokens, tk.Contract())
			}
			b, _ := json.Marshal(out)
			return b
		},
		leaks: materialOrShape,
	},
	{
		row: "UV.16 audit page", name: "the agent audit trail, as GET /agent/audit and retnd agent audit answer it",
		marker: func(*world) string { return "cid_canary_1" },
		collect: func(w *world) []byte {
			entries, _, err := w.svc.ListAgentAudit(w.ctx, service.AgentAuditQuery{Limit: service.MaxAgentAuditLimit})
			if err != nil {
				w.t.Fatalf("ListAgentAudit: %v", err)
			}
			out := apicontract.ListAgentAuditResponse{Entries: make([]apicontract.AgentAuditEntry, 0, len(entries))}
			for _, e := range entries {
				out.Entries = append(out.Entries, e.Contract())
			}
			b, _ := json.Marshal(out)
			return b
		},
		leaks: materialOrShape,
	},
	{
		row: "NV.31", name: "the committed compat corpus and configuration fixtures",
		marker: func(*world) string { return "encryption" },
		collect: func(w *world) []byte {
			return walkAll(w.t, filepath.Join("..", "compat", "testdata"), func(string) bool { return false })
		},
		leaks: anyMaterial,
	},
}

// Every surface the real service and the real transform produce is clean, and
// the collector reached something real on each (a clean scan of an empty
// surface is the failure this guards). One subtest per matrix row, so a row
// goes red alone. The material scanned for is the key files' own, read after
// the whole workflow ran.
func TestNoKeyMaterialOnAnySurface_996(t *testing.T) {
	w := newWorld(t)
	if bundleCarries := anyMaterial(w, w.bundle); !bundleCarries {
		t.Fatal("the whole bundle does not carry the key, so no scan of its metadata can be told apart from a scan of nothing")
	}
	for _, s := range surfaces {
		t.Run(s.row, func(t *testing.T) {
			got := s.collect(w)
			if len(got) == 0 {
				t.Fatalf("%s collected nothing", s.name)
			}
			if m := s.marker(w); m != "" && !bytes.Contains(got, []byte(m)) {
				t.Fatalf("%s did not carry %q, so the collector is not reading the real surface:\n%.400s", s.name, m, got)
			}
			if s.leaks(w, got) {
				t.Fatalf("key material reached %s", s.name)
			}
		})
	}
}

// The control, one row at a time, in every rendering the detector knows and
// in the shape of an identity: the canary is generated here at run time, is
// planted into that row's real surface, and the row's own detector finds it.
// The metrics row also finds a key id planted as a label value. Red if a
// detector stops reading a rendering, or a collector stops returning the
// bytes it scans.
func TestNoKeyMaterialOnAnySurface_996_WouldCatchOne(t *testing.T) {
	w := newWorld(t)
	canary := "AGE-SECRET-KEY-1" + strings.ToUpper(hex.EncodeToString([]byte(fmt.Sprint(time.Now().UnixNano()))))
	w.materials = append(w.materials, canary)
	for _, s := range surfaces {
		t.Run(s.row, func(t *testing.T) {
			real := s.collect(w)
			for _, r := range renderings(canary) {
				planted := append(bytes.Clone(real), []byte("\nplanted "+r+"\n")...)
				if !s.leaks(w, planted) {
					t.Fatalf("a canary planted on %s as %.24q... was not found", s.name, r)
				}
			}
		})
	}
	t.Run("NV.27 key id as a label", func(t *testing.T) {
		s := surfaces[2]
		planted := append(s.collect(w), []byte("retnd_retained_copy_key_bundle_confirmed{key_id=\""+w.keyIDs[1]+"\"} 1\n")...)
		if !s.leaks(w, planted) {
			t.Fatal("a key id planted as a label value was not found")
		}
	})
}

// A canary planted on one surface turns that surface's row red and no other
// row: every other detector, run over its own real bytes in the same world,
// stays quiet. Red if two rows share a detector or a collector reads another
// row's surface, which is how a composite "no key anywhere" boolean hides a
// per-surface regression.
func TestAPlantingTurnsExactlyItsOwnRowRed_996_WouldCatchOne(t *testing.T) {
	w := newWorld(t)
	canary := "AGE-SECRET-KEY-1" + strings.ToUpper(hex.EncodeToString([]byte("planted-"+fmt.Sprint(time.Now().UnixNano()))))
	w.materials = append(w.materials, canary)
	clean := make([][]byte, len(surfaces))
	for i, s := range surfaces {
		clean[i] = s.collect(w)
	}
	for i, planted := range surfaces {
		var red []string
		for j, s := range surfaces {
			b := clean[j]
			if i == j {
				b = append(bytes.Clone(b), []byte("\n"+canary+"\n")...)
			}
			if s.leaks(w, b) {
				red = append(red, s.row)
			}
		}
		if len(red) != 1 || red[0] != planted.row {
			t.Errorf("a canary planted on %s turned %v red, want exactly [%s]", planted.row, red, planted.row)
		}
	}
}

// UV.16 (#1348, FR-98): a canary planted in a tool's arguments, as an
// identity and as a recipient of either suite, is projected out before the
// audit table stores it, so the audit row stays clean and every other row
// does too. Remove the projection from the writer and the audit row goes red
// and no other. The row's own detector seeing a planted value is the generic
// control above.
func TestAPlantedToolArgumentNeverReachesTheAuditRow_1348(t *testing.T) {
	w := newWorld(t)
	stamp := strings.ToUpper(hex.EncodeToString([]byte(fmt.Sprint(time.Now().UnixNano()))))
	identity := "AGE-SECRET-KEY-1" + stamp
	recipient := "age1pq1qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	w.materials = append(w.materials, identity, recipient)
	if err := w.svc.RecordAgentCall(w.ctx, service.AgentCall{
		TokenID: "agt_x", Tool: "backup_set_show", Transport: "stdio", Status: 200,
		Arguments: map[string]any{"set": identity, "also": []string{recipient, "-----BEGIN OPENSSH PRIVATE KEY-----"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range surfaces {
		if strings.HasPrefix(s.row, "UV.16") && s.leaks(w, s.collect(w)) {
			t.Errorf("a canary planted in a tool argument reached %s", s.row)
		}
	}
	var page string
	for _, s := range surfaces {
		if s.row == "UV.16 audit page" {
			page = string(s.collect(w))
		}
	}
	if !strings.Contains(page, agenttools.Withheld) {
		t.Errorf("the audit page does not show the marker, so the planted call may not have been recorded:\n%.400s", page)
	}
}

// Every row of the corpus is held by the control tests above, and a row with
// no detector would pass them by never failing. The error names the row.
func TestEveryCanaryRowHasADetectorAndItsControlsExist_1348(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "canary_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			have[fn.Name.Name] = true
		}
	}
	for _, control := range []string{"TestNoKeyMaterialOnAnySurface_996_WouldCatchOne", "TestAPlantingTurnsExactlyItsOwnRowRed_996_WouldCatchOne"} {
		if !have[control] {
			t.Errorf("the corpus's control %s is gone, so no row has a _WouldCatchOne", control)
		}
	}
	seen := map[string]bool{}
	for _, s := range surfaces {
		if s.leaks == nil || s.collect == nil {
			t.Errorf("row %s has no detector or no collector, so it cannot catch a planted value", s.row)
		}
		if seen[s.row] {
			t.Errorf("row %s appears twice, so a planting would turn two rows red under one name", s.row)
		}
		seen[s.row] = true
	}
	for _, want := range []string{"UV.16 token list", "UV.16 audit page"} {
		if !seen[want] {
			t.Errorf("the corpus has no %s row", want)
		}
	}
}
