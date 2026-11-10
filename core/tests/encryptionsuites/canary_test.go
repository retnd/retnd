package encryptionsuites_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/metrics"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/service"
)

// The ten-surface canary corpus (#996, core/tests/encryptedretention) extended
// with the surfaces EPIC T adds (#1319): the suites listing, the policy answer
// that names a suite and says a rotation is needed, the receipt of a rotation
// across suites, the per-suite health section and its metrics, an age-pq/v1
// key's bundle metadata, and the re-encryption dry run. Each surface keeps a
// control that proves its detector sees a planted value, and a planting turns
// exactly its own row red.
//
// The detector knows both identity shapes. An age-pq/v1 identity starts
// "AGE-SECRET-KEY-PQ-1", which the EPIC N shape ("AGE-SECRET-KEY-1" followed
// by the Bech32 body) does not match, so the EPIC N corpus is blind to the
// shape of a PQ identity and finds one only through the material renderings.
// This corpus closes that.
//
// Material scanned for is read from the key files the real service minted,
// at run time; the canary is generated at run time. No key or canary value is
// in this file. Written by reading; not run.

var identityShapes = regexp.MustCompile(`AGE-SECRET-KEY-(PQ-)?1[A-Z0-9]{20,}`)

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
	if identityShapes.Match(surface) {
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

// suiteWorld is one deployment that started on age/v1, patched its policy to
// age-pq/v1 (and was told to rotate), rotated across suites, exported the new
// key's bundle and asked for a re-encryption dry run, through the service the
// engine runs.
type suiteWorld struct {
	t         *testing.T
	ctx       context.Context
	dir, cfg  string
	dbPath    string
	svc       *service.BackupService
	keyIDs    []string // age/v1 first, then age-pq/v1
	materials []string
	answers   map[string][]byte // surface name -> what the service answered
	bundle    []byte
}

func newSuiteWorld(t *testing.T) *suiteWorld {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	remote, local := filepath.Join(dir, "remote"), filepath.Join(dir, "local")
	for _, d := range []string{remote, local} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
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
	svc, closeFn, err := service.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	t.Cleanup(func() { _ = closeFn() })

	w := &suiteWorld{t: t, ctx: ctx, dir: dir, cfg: cfg, dbPath: dbPath, svc: svc, answers: map[string][]byte{}}
	keep := func(name string, v any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		w.answers[name] = b
	}

	k1, err := svc.CreateEncryptionKeyUnder(ctx, ageV1)
	keep("create under age/v1", k1, err)
	pol, err := svc.PatchEncryptionPolicy(ctx, apicontract.EncryptionPolicyPatch{Mode: "required", KeyProvider: &apicontract.EncryptionKeyProvider{Managed: true}, Suite: agePQV1})
	keep("policy patch to age-pq/v1", pol, err)
	suites, err := svc.ListEncryptionSuites(ctx)
	keep("suites list", suites, err)
	k2, err := svc.RotateEncryptionKeyUnder(ctx, agePQV1)
	keep("rotate across suites", k2, err)
	w.bundle, err = svc.ExportRecoveryBundleBytes(ctx, k2.ID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	plan, err := svc.PlanReencrypt(ctx, apicontract.ReencryptRequest{Suite: ageV1, DryRun: true})
	keep("re-encrypt dry run", plan, err)
	w.keyIDs = []string{k1.ID, k2.ID}

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
	var pq bool
	for _, m := range w.materials {
		pq = pq || strings.HasPrefix(m, "AGE-SECRET-KEY-PQ-1")
	}
	if !pq {
		t.Fatal("control: neither key file holds an age-pq/v1 identity, so this corpus never sees one")
	}
	return w
}

type suiteSurface struct {
	name    string
	marker  func(w *suiteWorld) string
	collect func(w *suiteWorld) []byte
	leaks   func(w *suiteWorld, b []byte) bool
}

func anyMaterial(w *suiteWorld, b []byte) bool { return carries(b, w.materials) }

func answer(name string) func(w *suiteWorld) []byte {
	return func(w *suiteWorld) []byte { return w.answers[name] }
}

var suiteSurfaces = []suiteSurface{
	{
		name:    "suites list",
		marker:  func(*suiteWorld) string { return agePQV1 },
		collect: answer("suites list"),
		leaks:   anyMaterial,
	},
	{
		name:    "policy patch to age-pq/v1",
		marker:  func(*suiteWorld) string { return "rotation_needed" },
		collect: answer("policy patch to age-pq/v1"),
		leaks:   anyMaterial,
	},
	{
		name:    "rotate across suites",
		marker:  func(w *suiteWorld) string { return w.keyIDs[1] },
		collect: answer("rotate across suites"),
		leaks:   anyMaterial,
	},
	{
		name:    "re-encrypt dry run",
		marker:  func(w *suiteWorld) string { return ageV1 },
		collect: answer("re-encrypt dry run"),
		leaks:   anyMaterial,
	},
	{
		name:   "per-suite health and its metrics (material, or a key id anywhere)",
		marker: func(*suiteWorld) string { return `suite="` + agePQV1 + `"` },
		collect: func(w *suiteWorld) []byte {
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
			r := health.NewReport(health.ProcessHealth{}, nil, time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
			r.KeyCustody = h
			section, _ := json.Marshal(h.Suites)
			return append(section, []byte(metrics.Render(r))...)
		},
		leaks: func(w *suiteWorld, b []byte) bool {
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
		name:   "an age-pq/v1 bundle's metadata (everything but the material field)",
		marker: func(w *suiteWorld) string { return agePQV1 },
		collect: func(w *suiteWorld) []byte {
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
}

func TestNoKeyMaterialOnAnySuiteSurface_1319(t *testing.T) {
	w := newSuiteWorld(t)
	if !anyMaterial(w, w.bundle) {
		t.Fatal("the whole age-pq/v1 bundle does not carry its key, so a scan of its metadata cannot be told apart from a scan of nothing")
	}
	for _, s := range suiteSurfaces {
		t.Run(s.name, func(t *testing.T) {
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

// The control per surface, in every rendering and in both identity shapes.
// The PQ shape is planted bare (no material list entry) so only the shape
// detector can find it: red if the detector reverts to the EPIC N shape.
func TestNoKeyMaterialOnAnySuiteSurface_1319_WouldCatchOne(t *testing.T) {
	w := newSuiteWorld(t)
	stamp := strings.ToUpper(hex.EncodeToString([]byte(fmt.Sprint(time.Now().UnixNano()))))
	canary := "AGE-SECRET-KEY-1" + stamp
	pqShape := "AGE-SECRET-KEY-PQ-1" + stamp
	w.materials = append(w.materials, canary)
	for _, s := range suiteSurfaces {
		t.Run(s.name, func(t *testing.T) {
			real := s.collect(w)
			for _, r := range renderings(canary) {
				if !s.leaks(w, append(bytes.Clone(real), []byte("\nplanted "+r+"\n")...)) {
					t.Fatalf("a canary planted on %s as %.24q... was not found", s.name, r)
				}
			}
			if !s.leaks(w, append(bytes.Clone(real), []byte("\n"+pqShape+"\n")...)) {
				t.Fatalf("an age-pq/v1 identity shape planted on %s was not found", s.name)
			}
		})
	}
	t.Run("a key id as a suite metric label", func(t *testing.T) {
		s := suiteSurfaces[4]
		planted := append(s.collect(w), []byte(`retnd_encrypted_copies{suite="`+agePQV1+`",key_id="`+w.keyIDs[1]+`"} 1`+"\n")...)
		if !s.leaks(w, planted) {
			t.Fatal("a key id planted as a label value was not found")
		}
	})
}

func TestAPlantingTurnsExactlyItsOwnSuiteRowRed_1319_WouldCatchOne(t *testing.T) {
	w := newSuiteWorld(t)
	canary := "AGE-SECRET-KEY-PQ-1" + strings.ToUpper(hex.EncodeToString([]byte("planted-"+fmt.Sprint(time.Now().UnixNano()))))
	clean := make([][]byte, len(suiteSurfaces))
	for i, s := range suiteSurfaces {
		clean[i] = s.collect(w)
	}
	for i, planted := range suiteSurfaces {
		var red []string
		for j, s := range suiteSurfaces {
			b := clean[j]
			if i == j {
				b = append(bytes.Clone(b), []byte("\n"+canary+"\n")...)
			}
			if s.leaks(w, b) {
				red = append(red, s.name)
			}
		}
		if len(red) != 1 || red[0] != planted.name {
			t.Errorf("a canary planted on %q turned %v red, want exactly that row", planted.name, red)
		}
	}
}
