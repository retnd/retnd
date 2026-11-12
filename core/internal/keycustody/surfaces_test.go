package keycustody_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// FR-63 and FR-64's "never rendered back": the response shapes this package
// exports, and the four surfaces a key could reach from here. Each surface
// has its own test and its own _WouldCatchOne control, so the matrix rows
// they back can go red one at a time and none can pass because nobody
// looked.

// canary is the planted material. It is a value no fixture produces, so
// finding it anywhere is a leak and not a coincidence; it appears in no
// issue, commit message or captured image.
var canary = []byte("PLANTED-KEY-MATERIAL-7f3a9c1e5b2d4860")

// exportedTypes lists every exported type declared in this package's
// non-test sources, so the table in the next test has to classify each one
// and a type added later cannot escape the enumeration.
func exportedTypes(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if ts.Name.IsExported() {
					names = append(names, ts.Name.Name)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// No route, CLI verb or JSON field returns existing key material: every
// exported shape is enumerated and classified. A shape is either
// material-free by construction (no field of a type that could hold a
// key, and no field named for one) or it is the Bundle, the one
// deliberate carrier, listed as such. A new exported type has to be
// classified here before the package compiles its tests green.
func TestEveryExportedShapeIsClassifiedAndOnlyTheBundleCarriesMaterial(t *testing.T) {
	carriers := map[string]bool{"Bundle": true}
	shapes := map[string]reflect.Type{
		"KeyRecord":    reflect.TypeFor[keycustody.KeyRecord](),
		"Receipt":      reflect.TypeFor[keycustody.Receipt](),
		"Probe":        reflect.TypeFor[keycustody.Probe](),
		"Provider":     reflect.TypeFor[keycustody.Provider](),
		"SetPolicy":    reflect.TypeFor[keycustody.SetPolicy](),
		"ExportRecord": reflect.TypeFor[keycustody.ExportRecord](),
		"Confirmation": reflect.TypeFor[keycustody.Confirmation](),
		"Bundle":       reflect.TypeFor[keycustody.Bundle](),
	}
	// Types that are not response shapes: interfaces, enums, the store and
	// the registry. They are classified here so the enumeration is total.
	fieldless := map[string]bool{
		"Format": true, "AgeFormat": true, "Registry": true, "JournalRegistry": true, "Store": true,
		"Status": true, "Resolvability": true,
	}

	for _, name := range exportedTypes(t) {
		if _, ok := shapes[name]; !ok && !fieldless[name] {
			t.Errorf("exported type %s is not classified: add it to shapes (a response shape) or fieldless, and say which it is", name)
		}
	}

	for name, typ := range shapes {
		hasMaterial := false
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.EqualFold(f.Name, "Material") || strings.Contains(strings.ToLower(f.Tag.Get("json")), "material") {
				hasMaterial = true
			}
			if f.Type == reflect.TypeFor[[]byte]() || f.Type == reflect.TypeFor[obs.Secret]() {
				hasMaterial = true
			}
		}
		if hasMaterial != carriers[name] {
			t.Errorf("%s: has a material-bearing field = %v, but the carrier list says %v", name, hasMaterial, carriers[name])
		}
	}
}

// The health section is what the API and CLI will render; it must have no
// field that could hold material at all.
func TestTheHealthSectionHasNoFieldThatCouldHoldMaterial(t *testing.T) {
	typ := reflect.TypeFor[health.KeyCustodyHealth]()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int64:
		case reflect.String, reflect.Slice, reflect.Pointer:
			if f.Type == reflect.TypeFor[[]byte]() {
				t.Errorf("KeyCustodyHealth.%s is []byte", f.Name)
			}
		default:
			t.Errorf("KeyCustodyHealth.%s is a %s; the section carries ids, times and verdicts only", f.Name, f.Type)
		}
	}
}

// ---- The four surfaces, each with a planted-canary control. ----

// mintedKeyAndMaterial is the real key whose material each surface test
// scans for, beside the planted canary each control scans for.
func mintedKeyAndMaterial(t *testing.T) (*keycustody.Store, keycustody.KeyRecord, []byte) {
	t.Helper()
	s := openStore(t, t.TempDir())
	rec, err := s.Create(context.Background(), fixedNow)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s, rec, materialOf(t, s, rec.ID)
}

// Surface 1: the log stream. Every key event this package emits, and a
// custody refusal and a probe logged as attributes, through obs.Logger.
func TestNoKeyReachesTheLogStream(t *testing.T) {
	s, rec, material := mintedKeyAndMaterial(t)
	ctx := context.Background()
	var buf bytes.Buffer
	l := obs.New(&buf, obs.LevelDebug)

	l.KeyCreated(ctx, rec.ID, obs.ResultSuccess)
	fresh, err := s.Rotate(ctx, fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	l.KeyRotated(ctx, rec.ID, fresh.ID, obs.ResultSuccess)
	var out bytes.Buffer
	receipt, err := s.Export(ctx, fresh.ID, &out, fixedNow)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	l.KeyBundleExported(ctx, receipt.KeyID, receipt.Digest, obs.ResultSuccess)
	l.KeyBundleConfirmed(ctx, receipt.KeyID, obs.ResultSuccess)
	l.Event(ctx, obs.LevelInfo, "probe", "key probe", logAttr("probe", s.Probe(ctx, fresh.ID)))
	l.Event(ctx, obs.LevelInfo, "record", "key record", logAttr("record", fresh))
	l.Event(ctx, obs.LevelInfo, "receipt", "key receipt", logAttr("receipt", receipt))

	if buf.Len() == 0 {
		t.Fatalf("nothing was logged, so nothing was checked")
	}
	for _, m := range [][]byte{material, materialOf(t, s, fresh.ID)} {
		if carriesMaterial(buf.Bytes(), m) {
			t.Fatalf("the log stream carries key material")
		}
	}
	if !strings.Contains(buf.String(), obs.EventKeyBundleExported) || !strings.Contains(buf.String(), receipt.Digest) {
		t.Fatalf("the export event did not reach the log with its digest, so the operator could not match a bundle to it")
	}
}

func TestNoKeyReachesTheLogStream_WouldCatchOne(t *testing.T) {
	var buf bytes.Buffer
	l := obs.New(&buf, obs.LevelDebug)
	l.Event(context.Background(), obs.LevelInfo, "planted", "a planted leak", logAttr("key", string(canary)))
	if !carriesMaterial(buf.Bytes(), canary) {
		t.Fatalf("the scan did not catch a canary planted straight into the log stream, so the test above proves nothing")
	}
}

// Surface 2: a journal row. The reference description and key id reach
// state_transitions.detail; the material must not, and the whole database
// file (WAL included) is what gets scanned.
func TestNoKeyReachesAJournalRow(t *testing.T) {
	s, rec, material := mintedKeyAndMaterial(t)
	ctx := context.Background()
	dir := t.TempDir()
	j, err := state.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })

	ref, extMaterial := externalKeyFile(t)
	detail := fmt.Sprintf("encrypting with %s; external %s; probe %s",
		keycustody.Provider{Managed: rec.ID}.Describe(),
		keycustody.Provider{External: ref}.Describe(),
		s.Probe(ctx, rec.ID))
	recordTransition(t, j, detail)

	scanned := readAllFiles(t, dir)
	if carriesMaterial(scanned, material) || carriesMaterial(scanned, extMaterial) {
		t.Fatalf("the journal file carries key material")
	}
	if !bytes.Contains(scanned, []byte(rec.ID)) {
		t.Fatalf("the key id did not reach the journal, so the row records nothing an operator can match a key to")
	}
}

func TestNoKeyReachesAJournalRow_WouldCatchOne(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	j, err := state.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	recordTransition(t, j, "planted: "+string(canary))
	if !carriesMaterial(readAllFiles(t, dir), canary) {
		t.Fatalf("the scan did not catch a canary written into a journal row")
	}
}

// Surface 3: the activity feed. A key event's fields, as the feed stores
// them.
func TestNoKeyReachesTheActivityFeed(t *testing.T) {
	s, rec, material := mintedKeyAndMaterial(t)
	ctx := context.Background()
	dir := t.TempDir()
	j, err := state.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })

	var out bytes.Buffer
	receipt, err := s.Export(ctx, rec.ID, &out, fixedNow)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	fields, _ := json.Marshal(map[string]any{"key_id": receipt.KeyID, "digest": receipt.Digest, "probe": s.Probe(ctx, rec.ID)})
	appendActivity(t, j, obs.EventKeyBundleExported, "key bundle exported", string(fields))

	rows, err := j.RecentActivityEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("RecentActivityEvents: %v", err)
	}
	if len(rows) != 1 || rows[0].Event != obs.EventKeyBundleExported {
		t.Fatalf("rows = %+v, want the one export event", rows)
	}
	scanned := append(readAllFiles(t, dir), []byte(fmt.Sprintf("%+v", rows))...)
	if carriesMaterial(scanned, material) {
		t.Fatalf("the activity feed carries key material")
	}
}

func TestNoKeyReachesTheActivityFeed_WouldCatchOne(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	j, err := state.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	appendActivity(t, j, "planted", "planted", `{"key":"`+string(canary)+`"}`)
	rows, err := j.RecentActivityEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("RecentActivityEvents: %v", err)
	}
	scanned := append(readAllFiles(t, dir), []byte(fmt.Sprintf("%+v", rows))...)
	if !carriesMaterial(scanned, canary) {
		t.Fatalf("the scan did not catch a canary written into the activity feed")
	}
}

// Surface 5 (#1348, FR-98): the agent audit table. A row records what an agent
// asked with, so the key id and a provider's description reach it as a tool's
// arguments, and the material must not. The whole database file (WAL
// included) is scanned.
func TestNoKeyReachesAnAgentAuditRow(t *testing.T) {
	s, rec, material := mintedKeyAndMaterial(t)
	ctx := context.Background()
	dir := t.TempDir()
	j, err := state.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })

	args, _ := json.Marshal(map[string]string{
		"key_id": rec.ID, "provider": keycustody.Provider{Managed: rec.ID}.Describe(), "probe": fmt.Sprint(s.Probe(ctx, rec.ID)),
	})
	if err := j.InsertAgentAudits(ctx, []state.AgentAudit{
		{At: fixedNow, TokenID: "agt_1", Tool: "encryption_state", Transport: "stdio", Arguments: string(args), Status: 200, CorrelationID: "cid_1"},
		{At: fixedNow, TokenID: "agt_1", Transport: "api", Arguments: "GET /encryption/keys", Status: 403, Code: "AGENT_ROUTE_DENIED", CorrelationID: "cid_2"},
	}); err != nil {
		t.Fatalf("InsertAgentAudits: %v", err)
	}
	rows, err := j.AgentAuditPage(ctx, state.AgentAuditFilter{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %+v (%v), want the two calls", rows, err)
	}
	scanned := append(readAllFiles(t, dir), []byte(fmt.Sprintf("%+v", rows))...)
	if carriesMaterial(scanned, material) {
		t.Fatalf("the agent audit table carries key material")
	}
	if !bytes.Contains(scanned, []byte(rec.ID)) {
		t.Fatalf("the key id did not reach the table, so the row records nothing an operator can match a key to")
	}
}

func TestNoKeyReachesAnAgentAuditRow_WouldCatchOne(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	j, err := state.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	if err := j.InsertAgentAudits(ctx, []state.AgentAudit{
		{At: fixedNow, TokenID: "agt_1", Tool: "planted", Transport: "stdio", Arguments: `{"set":"` + string(canary) + `"}`, Status: 200},
	}); err != nil {
		t.Fatalf("InsertAgentAudits: %v", err)
	}
	if !carriesMaterial(readAllFiles(t, dir), canary) {
		t.Fatalf("the scan did not catch a canary written into an agent audit row")
	}
}

// Surface 4: the operator-facing export. There is no support-bundle verb in
// this tree today; what a support transcript carries from this package is
// the health section and the export receipt rendered as JSON, which is
// what the API and CLI (#993) will return, so that is the surface scanned.
func TestNoKeyReachesTheHealthSectionOrReceiptJSON(t *testing.T) {
	s, rec, material := mintedKeyAndMaterial(t)
	ctx := context.Background()
	var out bytes.Buffer
	receipt, err := s.Export(ctx, rec.ID, &out, fixedNow)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	h, err := s.Health(ctx, true)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	keys, err := s.Keys(ctx)
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	report := health.Report{KeyCustody: h}
	js, err := json.Marshal(map[string]any{"report": report, "receipt": receipt, "keys": keys, "probe": s.Probe(ctx, rec.ID)})
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	if carriesMaterial(js, material) {
		t.Fatalf("the JSON export carries key material")
	}
	if !bytes.Contains(js, []byte(rec.ID)) || !bytes.Contains(js, []byte(receipt.Digest)) {
		t.Fatalf("the export does not carry the key id and digest an operator needs")
	}
}

func TestNoKeyReachesTheHealthSectionOrReceiptJSON_WouldCatchOne(t *testing.T) {
	js, err := json.Marshal(map[string]any{"report": health.Report{KeyCustody: health.KeyCustodyHealth{ActiveKeyID: string(canary)}}})
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	if !carriesMaterial(js, canary) {
		t.Fatalf("the scan did not catch a canary planted into the JSON export")
	}
}

// ---- helpers ----

func logAttr(key string, v any) slog.Attr { return slog.Any(key, v) }

func testArtifactRef(t *testing.T) model.ArtifactID {
	t.Helper()
	set, err := model.NewBackupSetID("production", "postgres-primary")
	if err != nil {
		t.Fatalf("NewBackupSetID: %v", err)
	}
	artifact, err := model.NewArtifactID(set, "backup-2026-10-24.dump.zst")
	if err != nil {
		t.Fatalf("NewArtifactID: %v", err)
	}
	return artifact
}

func recordTransition(t *testing.T, j *state.Journal, detail string) {
	t.Helper()
	ctx := context.Background()
	artifact := testArtifactRef(t)
	if _, err := j.Discover(ctx, artifact, "discover-1", "/incoming/backup.dump", state.RemoteIdentity{}, fixedNow); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, err := j.RecordTransition(ctx, state.Transition{
		Artifact:   artifact,
		Key:        "encrypt-attempt-1",
		From:       "DISCOVERED",
		To:         "FAILED",
		Detail:     detail,
		OccurredAt: fixedNow.Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordTransition: %v", err)
	}
}

func appendActivity(t *testing.T, j *state.Journal, event, msg, fields string) {
	t.Helper()
	if err := j.AppendActivityEvents(context.Background(), []state.ActivityEventRow{{
		Seq: 1, At: fixedNow, Level: "info", Event: event, Scope: "deployment", Message: msg, Result: "success", Fields: fields,
	}}); err != nil {
		t.Fatalf("AppendActivityEvents: %v", err)
	}
}

// readAllFiles concatenates every file under dir, so a WAL or a side file
// is scanned along with the main database.
func readAllFiles(t *testing.T, dir string) []byte {
	t.Helper()
	var all []byte
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
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
