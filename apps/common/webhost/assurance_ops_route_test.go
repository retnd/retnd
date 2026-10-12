package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/core/service"
)

// EPIC Q, #980 and #1071: scrub now, cancel, and the output of the newest
// recovery test, as three routes.
//
// None of them is destructive: a scrub reads, a cancel stops a read, and the
// output route only reads. So they answer on a deployment whose destructive
// gate is shut, while POST /operations still does not, and the three tests
// below that say so each run both halves in one router.

type opsFakeBackend struct {
	*syncFakeBackend

	mu       sync.Mutex
	scrubs   []string
	scrubOpt []service.ScrubOptions
	cancels  []string
	reads    []string

	scrubErr, cancelErr, outputErr error
	started                        service.RecoveryTestStarted
	output                         service.RecoveryTestOutput
}

func newOpsFake() *opsFakeBackend {
	return &opsFakeBackend{
		syncFakeBackend: newSyncFakeBackend(),
		started:         service.RecoveryTestStarted{BackupSetID: "production/alpha", OperationID: "op-running", Coalesced: true},
		output: service.RecoveryTestOutput{
			BackupSetID: "production/alpha", Recorded: true, OperationID: "op-9", ArtifactID: "production/alpha/dump.sql",
			Medium: "local", State: "passed", RecordedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), Truncated: true,
			Records: []service.RecoveryTestOutputRecord{{Kind: "output", Text: "restore ok"}, {Kind: "truncation", Text: "[retnd] output truncated"}},
		},
	}
}

func (f *opsFakeBackend) StartScrub(_ context.Context, id string, opts service.ScrubOptions) (service.ScrubStarted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scrubs, f.scrubOpt = append(f.scrubs, id), append(f.scrubOpt, opts)
	if f.scrubErr != nil {
		return service.ScrubStarted{}, f.scrubErr
	}
	return service.ScrubStarted{BackupSetID: id, OperationID: "op-scrub"}, nil
}

func (f *opsFakeBackend) CancelAssuranceOperation(_ context.Context, id string) (service.AssuranceOperationCancelled, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, id)
	if f.cancelErr != nil {
		return service.AssuranceOperationCancelled{}, f.cancelErr
	}
	return service.AssuranceOperationCancelled{OperationID: id, Kind: "recovery_test", BackupSetID: "production/alpha", Status: "cancelling"}, nil
}

func (f *opsFakeBackend) RecoveryTestOutput(_ context.Context, id string) (service.RecoveryTestOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, id)
	if f.outputErr != nil {
		return service.RecoveryTestOutput{}, f.outputErr
	}
	return f.output, nil
}

func (f *opsFakeBackend) StartRecoveryTest(_ context.Context, _ string, _ service.RecoveryTestOptions) (service.RecoveryTestStarted, error) {
	return f.started, nil
}

func opsRouter(be BackupServiceClient, gate DestructiveGate) http.Handler {
	return NewRouter(RouterConfig{
		Platform:      allowingPlatform("alice"),
		Backend:       be,
		Gate:          gate,
		BinaryVersion: "test",
		Commit:        "test",
	})
}

func send(router http.Handler, method, path, body string, withCSRF bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if withCSRF {
		attachValidCSRF(req)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func keysOf(t *testing.T, body string) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, body)
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestScrubNowAnswers202WithAnOperationIdOnAShutGateWhileOperationsStaysRefused(t *testing.T) {
	be := newOpsFake()
	router := opsRouter(be, NotYetImplementedGate{})

	if ops := submitOperation(t, router, "k1", `{"action":"run_cycle","config_revision":"x"}`); ops.Code != http.StatusForbidden || responseErrorCode(ops.Body.String()) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
		t.Fatalf("POST /operations on a shut gate = %d %s, want 403 DESTRUCTIVE_OPERATIONS_DISABLED", ops.Code, ops.Body.String())
	}
	rec := send(router, http.MethodPost, "/api/v1/backup-sets/production/alpha/scrub", "", true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST scrub = %d %s, want 202", rec.Code, rec.Body.String())
	}
	if got := fmt.Sprint(keysOf(t, rec.Body.String())); got != "[backup_set_id operation_id]" {
		t.Errorf("scrub answer has keys %s, want exactly backup_set_id and operation_id", got)
	}
	var doc scrubStartedResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if doc.BackupSetID != "production/alpha" || doc.OperationID != "op-scrub" {
		t.Errorf("answer = %s", rec.Body.String())
	}
	if len(be.scrubs) != 1 || be.scrubs[0] != "production/alpha" || be.scrubOpt[0].Actor != "alice" {
		t.Errorf("backend saw %v %+v, want one scrub of production/alpha asked by alice", be.scrubs, be.scrubOpt)
	}
	// It is a write: no CSRF pair, no scrub, and the refusal is the CSRF check's.
	if rec := send(router, http.MethodPost, "/api/v1/backup-sets/production/alpha/scrub", "", false); rec.Code != http.StatusForbidden || !strings.Contains(responseErrorCode(rec.Body.String()), "CSRF") {
		t.Errorf("scrub without a CSRF pair = %d %s, want a CSRF refusal", rec.Code, rec.Body.String())
	}
	if len(be.scrubs) != 1 {
		t.Errorf("a request with no CSRF pair reached the backend")
	}
}

func TestCancelAnswers202OnAShutGateWhileOperationsStaysRefused(t *testing.T) {
	be := newOpsFake()
	router := opsRouter(be, NotYetImplementedGate{})

	if ops := submitOperation(t, router, "k1", `{"action":"run_cycle","config_revision":"x"}`); ops.Code != http.StatusForbidden {
		t.Fatalf("POST /operations on a shut gate = %d, want 403", ops.Code)
	}
	rec := send(router, http.MethodPost, "/api/v1/assurance/operations/op-77/cancel", "", true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST cancel = %d %s, want 202", rec.Code, rec.Body.String())
	}
	if got := fmt.Sprint(keysOf(t, rec.Body.String())); got != "[backup_set_id kind operation_id status]" {
		t.Errorf("cancel answer has keys %s", got)
	}
	var doc map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if doc["operation_id"] != "op-77" || doc["kind"] != "recovery_test" || doc["status"] != "cancelling" || doc["backup_set_id"] != "production/alpha" {
		t.Errorf("answer = %s", rec.Body.String())
	}
	if len(be.cancels) != 1 || be.cancels[0] != "op-77" {
		t.Errorf("backend cancelled %v", be.cancels)
	}
	if rec := send(router, http.MethodPost, "/api/v1/assurance/operations/op-77/cancel", "", false); rec.Code != http.StatusForbidden || !strings.Contains(responseErrorCode(rec.Body.String()), "CSRF") {
		t.Errorf("cancel without a CSRF pair = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecoveryTestOutputIsReadableOnAShutGateWhileOperationsStaysRefused(t *testing.T) {
	be := newOpsFake()
	router := opsRouter(be, NotYetImplementedGate{})

	if ops := submitOperation(t, router, "k1", `{"action":"run_cycle","config_revision":"x"}`); ops.Code != http.StatusForbidden {
		t.Fatalf("POST /operations on a shut gate = %d, want 403", ops.Code)
	}
	rec := send(router, http.MethodGet, "/api/v1/backup-sets/production/alpha/recovery-test/output", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET output = %d %s, want 200", rec.Code, rec.Body.String())
	}
	want := `{"artifact_id":"production/alpha/dump.sql","backup_set_id":"production/alpha","medium":"local","operation_id":"op-9",` +
		`"recorded":true,"recorded_at":"2026-10-10T12:00:00Z","records":[{"kind":"output","text":"restore ok"},` +
		`{"kind":"truncation","text":"[retnd] output truncated"}],"state":"passed","truncated":true}`
	var gotAny, wantAny any
	_ = json.Unmarshal(rec.Body.Bytes(), &gotAny)
	_ = json.Unmarshal([]byte(want), &wantAny)
	if fmt.Sprint(gotAny) != fmt.Sprint(wantAny) {
		t.Errorf("body = %s\nwant   %s", rec.Body.String(), want)
	}
	if len(be.reads) != 1 || be.reads[0] != "production/alpha" {
		t.Errorf("backend read %v", be.reads)
	}
}

func TestRecoveryTestOutputBeforeAnyTestIsRecordedFalseWithEmptyFieldsAndTheSameShape(t *testing.T) {
	be := newOpsFake()
	be.output = service.RecoveryTestOutput{BackupSetID: "production/alpha"}
	router := opsRouter(be, alwaysPassGate{})
	rec := send(router, http.MethodGet, "/api/v1/backup-sets/production/alpha/recovery-test/output", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if got := fmt.Sprint(keysOf(t, rec.Body.String())); got != "[artifact_id backup_set_id medium operation_id recorded recorded_at records state truncated]" {
		t.Errorf("keys = %s, want the same nine keys as a recorded answer: the shape never changes", got)
	}
	want := `{"artifact_id":"","backup_set_id":"production/alpha","medium":"","operation_id":"","recorded":false,"recorded_at":"","records":[],"state":"","truncated":false}`
	var gotAny, wantAny any
	_ = json.Unmarshal(rec.Body.Bytes(), &gotAny)
	_ = json.Unmarshal([]byte(want), &wantAny)
	if fmt.Sprint(gotAny) != fmt.Sprint(wantAny) || !strings.Contains(rec.Body.String(), `"records":[]`) {
		t.Errorf("body = %s\nwant   %s (an empty array, never null)", rec.Body.String(), want)
	}
}

func TestACoalescedRecoveryTestStartSaysSoAndNamesTheRunningOperation(t *testing.T) {
	be := newOpsFake()
	router := opsRouter(be, NotYetImplementedGate{})
	rec := send(router, http.MethodPost, "/api/v1/backup-sets/production/alpha/recovery-test", `{}`, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("= %d %s, want 202", rec.Code, rec.Body.String())
	}
	var doc struct {
		OperationID string `json:"operation_id"`
		Coalesced   bool   `json:"coalesced"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if doc.OperationID != "op-running" || !doc.Coalesced {
		t.Errorf("body = %s, want the running operation with coalesced true", rec.Body.String())
	}
}

func TestAssuranceOperationRoutesMapTheirRefusalsToTheirCodes(t *testing.T) {
	cases := []struct {
		name   string
		set    func(*opsFakeBackend, error)
		method string
		path   string
		err    error
		status int
		code   string
	}{
		{"a scrub already running", func(b *opsFakeBackend, e error) { b.scrubErr = e }, http.MethodPost, "/api/v1/backup-sets/production/alpha/scrub", service.ErrScrubRunning, 409, "OPERATION_ALREADY_RUNNING"},
		{"a scrub of an unknown set", func(b *opsFakeBackend, e error) { b.scrubErr = e }, http.MethodPost, "/api/v1/backup-sets/production/nope/scrub", service.ErrBackupSetNotFound, 404, "BACKUP_SET_NOT_FOUND"},
		{"a scrub of a set that cannot be scrubbed", func(b *opsFakeBackend, e error) { b.scrubErr = e }, http.MethodPost, "/api/v1/backup-sets/production/alpha/scrub", fmt.Errorf("%w: it is incremental", service.ErrInvalidRequest), 400, "INVALID_REQUEST"},
		{"a scrub that failed inside", func(b *opsFakeBackend, e error) { b.scrubErr = e }, http.MethodPost, "/api/v1/backup-sets/production/alpha/scrub", errors.New("sqlite: database is locked at /secret/path"), 500, "INTERNAL"},
		{"a cancel of nothing running", func(b *opsFakeBackend, e error) { b.cancelErr = e }, http.MethodPost, "/api/v1/assurance/operations/gone/cancel", service.ErrOperationNotFound, 404, "OPERATION_NOT_FOUND"},
		{"a cancel that failed inside", func(b *opsFakeBackend, e error) { b.cancelErr = e }, http.MethodPost, "/api/v1/assurance/operations/x/cancel", errors.New("boom /secret/path"), 500, "INTERNAL"},
		{"the output of an unknown set", func(b *opsFakeBackend, e error) { b.outputErr = e }, http.MethodGet, "/api/v1/backup-sets/production/nope/recovery-test/output", service.ErrBackupSetNotFound, 404, "BACKUP_SET_NOT_FOUND"},
		{"the output of a malformed set", func(b *opsFakeBackend, e error) { b.outputErr = e }, http.MethodGet, "/api/v1/backup-sets/production/alpha/recovery-test/output", fmt.Errorf("%w: bad id", service.ErrInvalidRequest), 400, "INVALID_REQUEST"},
		{"the output read failing inside", func(b *opsFakeBackend, e error) { b.outputErr = e }, http.MethodGet, "/api/v1/backup-sets/production/alpha/recovery-test/output", errors.New("boom /secret/path"), 500, "INTERNAL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be := newOpsFake()
			tc.set(be, tc.err)
			rec := send(opsRouter(be, NotYetImplementedGate{}), tc.method, tc.path, "", true)
			if rec.Code != tc.status || responseErrorCode(rec.Body.String()) != tc.code {
				t.Errorf("= %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
			if strings.Contains(rec.Body.String(), "/secret/path") || strings.Contains(rec.Body.String(), "sqlite") {
				t.Errorf("an internal error text reached the client: %s", rec.Body.String())
			}
		})
	}
}

// The same three routes against the real service, behind a shut gate.
func TestAssuranceOperationRoutesAgainstTheRealServiceOnAShutGate(t *testing.T) {
	configPath := writeAssuranceRouteConfig(t)
	svc, cleanup, err := service.Open(context.Background(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	router := opsRouter(svc, NotYetImplementedGate{})

	if ops := submitOperation(t, router, "k1", `{"action":"run_cycle","config_revision":"x"}`); ops.Code != http.StatusForbidden {
		t.Fatalf("POST /operations on a shut gate = %d, want 403", ops.Code)
	}

	rec := send(router, http.MethodPost, "/api/v1/backup-sets/production/alpha/scrub", "", true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("scrub = %d %s", rec.Code, rec.Body.String())
	}
	var started scrubStartedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.BackupSetID != alphaSet || len(started.OperationID) < 8 {
		t.Errorf("scrub answer = %s (%v)", rec.Body.String(), err)
	}

	if rec := send(router, http.MethodPost, "/api/v1/backup-sets/production/nope/scrub", "", true); rec.Code != http.StatusNotFound || responseErrorCode(rec.Body.String()) != "BACKUP_SET_NOT_FOUND" {
		t.Errorf("scrub of an unknown set = %d %s", rec.Code, rec.Body.String())
	}
	if rec := send(router, http.MethodPost, "/api/v1/assurance/operations/no-such-op/cancel", "", true); rec.Code != http.StatusNotFound || responseErrorCode(rec.Body.String()) != "OPERATION_NOT_FOUND" {
		t.Errorf("cancel of an unknown operation = %d %s", rec.Code, rec.Body.String())
	}
	rec = send(router, http.MethodGet, "/api/v1/backup-sets/production/alpha/recovery-test/output", "", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"recorded":false`) {
		t.Errorf("output before any test = %d %s, want 200 with recorded false", rec.Code, rec.Body.String())
	}
	if rec := send(router, http.MethodGet, "/api/v1/backup-sets/production/nope/recovery-test/output", "", false); rec.Code != http.StatusNotFound {
		t.Errorf("output of an unknown set = %d %s", rec.Code, rec.Body.String())
	}
}
