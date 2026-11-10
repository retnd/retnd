package service

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
)

// #993: the encryption surface at the service layer, where the CLI verbs and
// the HTTP routes meet. Every test is named for the claim it makes and carries
// the issue number so the report and the reproduction find each other.

// fileBacked gives a fixture's service the configuration file the policy
// writes and the key directory hang off. The fixture builds its service from
// a struct, so it has neither until a test asks.
func fileBacked(t *testing.T, f *recoveryFixture) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "poll_interval: 15m\nstate:\n  database: " + f.b.state.Load().inner.Config.State.Database + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f.b.configPath = path
	return dir
}

// materialOf reads the one managed key's bytes the way only a test may, so a
// canary scan knows what to look for.
func materialOf(t *testing.T, f *recoveryFixture, id string) string {
	t.Helper()
	var got string
	if err := f.b.keys.WithKey(context.Background(), id, func(key []byte) error {
		got = string(key)
		return nil
	}); err != nil {
		t.Fatalf("reading the key's material for the scan: %v", err)
	}
	if got == "" {
		t.Fatal("the key has no material to scan for")
	}
	return got
}

// renderings is every way key material could reach a text surface.
func renderings(material string) []string {
	raw := []byte(material)
	return []string{
		material,
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		hex.EncodeToString(raw),
	}
}

func carries(surface []byte, material string) bool {
	for _, r := range renderings(material) {
		if strings.Contains(string(surface), r) {
			return true
		}
	}
	return false
}

func TestKeyStatusCarriesNoMaterialAndACanaryInAnyFieldIsFound_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	ctx := context.Background()

	created, err := f.b.CreateEncryptionKey(ctx)
	if err != nil {
		t.Fatalf("CreateEncryptionKey: %v", err)
	}
	material := materialOf(t, f, created.ID)

	list, err := f.b.ListEncryptionKeys(ctx)
	if err != nil {
		t.Fatalf("ListEncryptionKeys: %v", err)
	}
	for name, v := range map[string]any{"the create answer": created, "the list": list} {
		raw, _ := json.Marshal(v)
		if carries(raw, material) {
			t.Errorf("%s carries the key's material: %s", name, raw)
		}
	}

	// The control, one field at a time: the same scan has to find a canary
	// planted in each string field of the status, or "no material" above
	// would be a scan that cannot see that field.
	typ := reflect.TypeOf(apicontract.EncryptionKeyStatus{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() != reflect.String {
			continue
		}
		planted := created
		reflect.ValueOf(&planted).Elem().Field(i).SetString(material)
		raw, _ := json.Marshal(planted)
		if !carries(raw, material) {
			t.Errorf("a canary planted in %s was not found by the scan", typ.Field(i).Name)
		}
	}
}

func TestAPolicyThatRequiresEncryptionWithNoProviderIsRefusedAsTheFileWouldBe_993(t *testing.T) {
	svc, configPath := openTestService(t)
	before, _ := os.ReadFile(configPath)

	_, err := svc.PatchEncryptionPolicy(context.Background(), apicontract.EncryptionPolicyPatch{Mode: "required"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "retained_copy_encryption") {
		t.Errorf("the refusal does not name the config key at fault: %v", err)
	}
	if after, _ := os.ReadFile(configPath); string(after) != string(before) {
		t.Error("a refused patch changed the configuration file")
	}
}

func TestAnEmptyPolicyPatchIsRefusedWithoutTouchingTheFile_993(t *testing.T) {
	svc, configPath := openTestService(t)
	before, _ := os.ReadFile(configPath)
	if _, err := svc.PatchEncryptionPolicy(context.Background(), apicontract.EncryptionPolicyPatch{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if after, _ := os.ReadFile(configPath); string(after) != string(before) {
		t.Error("a refused patch changed the configuration file")
	}
}

func TestResettingThePolicyWritesTheBytesAConfigThatNeverMentionedItWould_993(t *testing.T) {
	svc, configPath := openTestService(t)
	ctx := context.Background()
	// A first write so the file is the one the service itself encodes.
	if _, err := svc.ResetEncryptionPolicy(ctx); err != nil {
		t.Fatalf("ResetEncryptionPolicy: %v", err)
	}
	baseline, _ := os.ReadFile(configPath)

	got, err := svc.PatchEncryptionPolicy(ctx, apicontract.EncryptionPolicyPatch{Mode: "required", KeyProvider: &apicontract.EncryptionKeyProvider{Managed: true}})
	if err != nil {
		t.Fatalf("PatchEncryptionPolicy: %v", err)
	}
	if got.Mode != "required" || got.KeyProvider.Kind != "managed" {
		t.Errorf("policy after the patch = %+v", got)
	}
	if mid, _ := os.ReadFile(configPath); string(mid) == string(baseline) {
		t.Fatal("the patch wrote nothing")
	}
	if _, err := svc.ResetEncryptionPolicy(ctx); err != nil {
		t.Fatalf("ResetEncryptionPolicy: %v", err)
	}
	if after, _ := os.ReadFile(configPath); string(after) != string(baseline) {
		t.Errorf("a reset did not restore the bytes the file had before encryption was mentioned:\n%s", after)
	}
}

func TestAKeyReferenceReadsBackAsItsDescriptionAndCannotRedeclareAKey_993(t *testing.T) {
	svc, _ := openTestService(t)
	const secretArg = "TOPSECRET-ARGUMENT"
	got, err := svc.PatchEncryptionPolicy(context.Background(), apicontract.EncryptionPolicyPatch{
		KeyProvider: &apicontract.EncryptionKeyProvider{External: &apicontract.EncryptionKeyReference{Command: []string{"vault", "kv", "get", secretArg}}},
	})
	if err != nil {
		t.Fatalf("PatchEncryptionPolicy: %v", err)
	}
	if got.KeyProvider.Kind != "external" || got.KeyProvider.Source != "command vault" {
		t.Errorf("the provider reads back as %+v, want the executable's description and nothing else", got.KeyProvider)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), secretArg) {
		t.Errorf("the read-back carries the command's argument: %s", raw)
	}

	// The round trip: what a GET returned, handed back as a reference, is
	// refused, because the reading is a description and has no field a
	// reference is made of.
	var readBack json.RawMessage
	readBack, _ = json.Marshal(got.KeyProvider)
	dec := json.NewDecoder(strings.NewReader(string(readBack)))
	dec.DisallowUnknownFields()
	var ref apicontract.EncryptionKeyReference
	if err := dec.Decode(&ref); err == nil {
		t.Errorf("a description read back decoded as a reference: %+v", ref)
	}
}

func TestEveryRefusalIsTheSameWordsOnEverySurface_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	ctx := context.Background()

	if _, err := f.b.RotateEncryptionKey(ctx); !errors.Is(err, ErrEncryptionKeyState) {
		t.Errorf("rotating with no key: err = %v, want ErrEncryptionKeyState", err)
	}
	k, err := f.b.CreateEncryptionKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.CreateEncryptionKey(ctx); !errors.Is(err, ErrEncryptionKeyState) {
		t.Errorf("a second create: err = %v, want ErrEncryptionKeyState", err)
	}
	if _, err := f.b.ConfirmRecoveryBundle(ctx, k.ID, ""); !errors.Is(err, ErrEncryptionKeyState) {
		t.Errorf("confirming with nothing exported: err = %v, want ErrEncryptionKeyState", err)
	}
	if _, err := f.b.ConfirmRecoveryBundle(ctx, "knope", ""); !errors.Is(err, ErrEncryptionKeyNotFound) {
		t.Errorf("confirming an unknown key: err = %v, want ErrEncryptionKeyNotFound", err)
	}
	// A classified refusal prints as the underlying words, whole.
	_, err = f.b.ConfirmRecoveryBundle(ctx, "knope", "")
	if err == nil || !strings.Contains(err.Error(), "knope") || strings.HasPrefix(err.Error(), "service: no managed key with this id: ") {
		t.Errorf("the refusal reads %q; it must be the key store's own sentence", err)
	}
}

func TestAnExportedBundleIsConfirmedByAStatementAndTheDigestIsChecked_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	ctx := context.Background()
	k, err := f.b.CreateEncryptionKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if k.BundleConfirmed {
		t.Fatal("a new key reports a confirmed bundle")
	}
	var sink strings.Builder
	receipt, err := f.b.ExportRecoveryBundle(ctx, k.ID, &sink)
	if err != nil {
		t.Fatalf("ExportRecoveryBundle: %v", err)
	}
	raw, _ := json.Marshal(receipt)
	if carries(raw, materialOf(t, f, k.ID)) {
		t.Errorf("the receipt carries the material: %s", raw)
	}
	if _, err := f.b.ConfirmRecoveryBundle(ctx, k.ID, "not-the-digest"); !errors.Is(err, ErrEncryptionKeyState) {
		t.Errorf("confirming a digest no export carries: err = %v, want ErrEncryptionKeyState", err)
	}
	got, err := f.b.ConfirmRecoveryBundle(ctx, k.ID, "")
	if err != nil {
		t.Fatalf("ConfirmRecoveryBundle: %v", err)
	}
	if !got.BundleConfirmed || got.BundleConfirmedAt == "" {
		t.Errorf("after confirming, the key reads %+v", got)
	}
}

// fakeKeyJournal is the journal's retirement half, with one ACTIVE copy
// recording the key.
type fakeKeyJournal struct {
	mu      sync.Mutex
	copies  []model.CopyRef
	retired []string
}

func (j *fakeKeyJournal) EncryptionKey(_ context.Context, id string) (state.EncryptionKey, error) {
	return state.EncryptionKey{ID: id, Provider: state.KeyProviderManaged, Status: state.EncryptionKeySuperseded, CreatedAt: time.Now().UTC()}, nil
}

func (j *fakeKeyJournal) ActivePlacementsRecordingKey(context.Context, string) ([]model.CopyRef, error) {
	return j.copies, nil
}

func (j *fakeKeyJournal) RetireEncryptionKey(_ context.Context, id string, _ time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.retired = append(j.retired, id)
	return nil
}

func TestRetiringAKeyAnActiveCopyRecordsIsRefusedNamingTheCopyAndClassified_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	set, _ := model.NewBackupSetID("production", "postgres")
	art, _ := model.NewArtifactID(set, "dump.sql")
	j := &fakeKeyJournal{copies: []model.CopyRef{{Artifact: art, Medium: "warm"}}}
	f.b.retireJournal = j

	_, err := f.b.RetireEncryptionKey(context.Background(), "kold")
	if !errors.Is(err, ErrEncryptionKeyInUse) {
		t.Fatalf("err = %v, want ErrEncryptionKeyInUse", err)
	}
	if !strings.Contains(err.Error(), "production/postgres/dump.sql on warm") {
		t.Errorf("the refusal does not name the copy: %v", err)
	}
	if len(j.retired) != 0 {
		t.Errorf("the key was retired anyway: %v", j.retired)
	}
}

func TestRetiringWithAJournalThatCannotRetireRefusesRatherThanSkippingTheCheck_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	// No retireJournal: the journal is a *state.Journal, which does not carry
	// the retirement transaction until #989 lands. When it does, this test is
	// the one to delete, and TestRetiringAKeyAnActiveCopyRecords... stands.
	if _, ok := any(f.b.journal).(interface {
		ActivePlacementsRecordingKey(context.Context, string) ([]model.CopyRef, error)
	}); ok {
		t.Skip("the journal now carries the retirement transaction; the in-use refusal test covers it")
	}
	if _, err := f.b.RetireEncryptionKey(context.Background(), "kany"); !errors.Is(err, ErrEncryptionRetireUnavailable) {
		t.Errorf("err = %v, want ErrEncryptionRetireUnavailable, never a retirement without the check", err)
	}
}

// encryptedFixture seeds one COMPLETE backup with an encrypted copy on warm,
// written under key.
func encryptedFixture(t *testing.T) (*recoveryFixture, model.ArtifactID, apicontract.EncryptionKeyStatus) {
	t.Helper()
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	ctx := context.Background()
	key, err := f.b.CreateEncryptionKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := seedComplete(t, f, "enc.dump", "warm")
	if err := f.b.journal.RecordPlacementEncryption(ctx, a, "warm", state.PlacementEncryption{
		FormatID: "age", FormatVersion: 1, KeyID: key.ID,
		StoredHash: strings.Repeat("e", 64), StoredHashAlg: "sha256", StoredSize: int64(len(f.data)) + 200,
	}); err != nil {
		t.Fatalf("seeding the encrypted copy: %v", err)
	}
	return f, a, key
}

func TestEncryptionCopiesSaysWhichCopiesAreEncryptedAndWhetherTheirKeyResolves_993(t *testing.T) {
	f, a, key := encryptedFixture(t)
	got, err := f.b.EncryptionCopies(context.Background(), a.String())
	if err != nil {
		t.Fatalf("EncryptionCopies: %v", err)
	}
	if len(got.Copies) != 1 {
		t.Fatalf("copies = %+v", got.Copies)
	}
	c := got.Copies[0]
	if !c.Encrypted || c.KeyID != key.ID || c.Format != "age" || c.KeyResolvable == nil || !*c.KeyResolvable {
		t.Errorf("the encrypted copy reads %+v", c)
	}
	raw, _ := json.Marshal(got)
	if carries(raw, materialOf(t, f, key.ID)) {
		t.Errorf("the per-copy state carries the material: %s", raw)
	}
}

func TestADecryptVerifyWhoseRecordedKeyDoesNotResolveIsRefusedBeforeAnyRead_993(t *testing.T) {
	f, a, key := encryptedFixture(t)
	keyFile := filepath.Join(keycustody.DirBeside(f.b.configPath), key.ID+".key")
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	stall := &stallOpenStore{recoveryStore: f.store, opened: make(chan struct{})}
	f.b.state.Load().inner.MediumStore = stall

	_, err := f.b.StartDecryptVerify(context.Background(), apicontract.StartDecryptVerifyRequest{ArtifactID: a.String(), AcknowledgeEgress: true})
	if !errors.Is(err, ErrEncryptionKeyUnresolvable) {
		t.Fatalf("err = %v, want ErrEncryptionKeyUnresolvable", err)
	}
	select {
	case <-stall.opened:
		t.Error("a medium was read before the refusal")
	default:
	}
}

func TestADecryptVerifyOfABackupWithNoEncryptedCopyIsRefused_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	a := seedComplete(t, f, "plain.dump", "warm")
	_, err := f.b.StartDecryptVerify(context.Background(), apicontract.StartDecryptVerifyRequest{ArtifactID: a.String()})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if _, err := f.b.StartDecryptVerify(context.Background(), apicontract.StartDecryptVerifyRequest{ArtifactID: "production/postgres/nothing.dump"}); !errors.Is(err, ErrArtifactNotFound) {
		t.Errorf("an unknown backup: err = %v, want ErrArtifactNotFound", err)
	}
}

// stallOpenStore holds a medium read until the operation's context ends.
type stallOpenStore struct {
	*recoveryStore
	opened chan struct{}
	once   sync.Once
}

func (s *stallOpenStore) OpenObject(ctx context.Context, _ transport.Medium, _ string) (io.ReadCloser, error) {
	s.once.Do(func() { close(s.opened) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// N1.20: the terminal's cancel and the browser's cancel are one call on one
// durable operation. Both surfaces reach CancelAssuranceOperation (the CLI's
// `assurance cancel` and the browser's Cancel are the same route), so this
// compares, row by row, what a cancel leaves behind for an operation the
// terminal started (a decrypt-verify) against one the browser started (a
// scrub): the answer, the record read back after a reconnect, and the claim
// on the copy.
func TestACancelFromEitherSurfaceLeavesTheSameDurableState_993(t *testing.T) {
	ctx := context.Background()
	f, a, _ := encryptedFixture(t)
	stall := &stallOpenStore{recoveryStore: f.store, opened: make(chan struct{})}
	f.b.state.Load().inner.MediumStore = stall

	started, err := f.b.StartDecryptVerify(ctx, apicontract.StartDecryptVerifyRequest{ArtifactID: a.String(), AcknowledgeEgress: true})
	if err != nil {
		t.Fatalf("StartDecryptVerify: %v", err)
	}
	select {
	case <-stall.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the decrypt-verify never reached the medium")
	}
	cancelledA, err := f.b.CancelAssuranceOperation(ctx, started.OperationID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	idle(t, f)
	recA, err := f.b.AssuranceOperation(ctx, started.OperationID)
	if err != nil {
		t.Fatalf("reading the cancelled operation back: %v", err)
	}
	claimA := claimOf(t, f, a, "warm")

	// The same copy, scrubbed and cancelled the way the browser does.
	stall2 := &stallOpenStore{recoveryStore: f.store, opened: make(chan struct{})}
	f.b.state.Load().inner.MediumStore = &stallStatStore{stallOpenStore: stall2}
	startedB, err := f.b.StartScrub(ctx, "production/postgres", ScrubOptions{Actor: "alice"})
	if err != nil {
		t.Fatalf("StartScrub: %v", err)
	}
	select {
	case <-stall2.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the scrub never reached the medium")
	}
	cancelledB, err := f.b.CancelAssuranceOperation(ctx, startedB.OperationID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	idle(t, f)
	recB, err := f.b.AssuranceOperation(ctx, startedB.OperationID)
	if err != nil {
		t.Fatalf("reading the cancelled operation back: %v", err)
	}
	claimB := claimOf(t, f, a, "warm")

	for _, row := range []struct {
		name string
		a, b any
	}{
		{"the answer's status", cancelledA.Status, cancelledB.Status},
		{"the answer's kind", cancelledA.Kind, cancelledB.Kind},
		{"the answer's backup set", cancelledA.BackupSetID, cancelledB.BackupSetID},
		{"the record's state", recA.State, recB.State},
		{"the record's stage", recA.Stage, recB.Stage},
		{"the claim on the copy", claimA.AssuranceState, claimB.AssuranceState},
	} {
		if !reflect.DeepEqual(row.a, row.b) {
			t.Errorf("row %q differs between the two surfaces: %v and %v", row.name, row.a, row.b)
		}
	}
	if recA.State != AssuranceOpCancelled {
		t.Errorf("the cancelled decrypt-verify reads back as %q, want cancelled", recA.State)
	}
}

// stallStatStore stalls the existence check too, because a scrub's first
// read of a copy on a medium is a stat.
type stallStatStore struct{ *stallOpenStore }

func (s *stallStatStore) StatObject(ctx context.Context, _ transport.Medium, _ string) (transport.ObjectInfo, error) {
	s.once.Do(func() { close(s.opened) })
	<-ctx.Done()
	return transport.ObjectInfo{}, ctx.Err()
}
