package webhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retnd/retnd/apps/common/platform/capabilities"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/service"
)

// Recovering several backups, or a whole set, to one destination over HTTP
// (#1106, #1107): POST /api/v1/recoveries. The tests hold the route tier, the
// body that crosses the boundary, the pre-flight answer and the streamed
// progress, with the service replaced by a double that records what it was
// asked.

type fakeManyBackend struct {
	*fakeRecoveryBackend

	mu       sync.Mutex
	planned  []service.RecoverManyRequest
	ran      []service.RecoverManyRequest
	sessions []service.RecoverySession
	ctxs     []context.Context

	plan    service.RecoverManyPlan
	planErr error
	result  service.RecoverManyResult
	runErr  error

	// hold, when set, makes a run wait on it after the plan line and before
	// the first file, so a test can look at what the client has seen so far.
	hold chan struct{}

	actions []cliecho.APIAction
}

// RecordAPIAction makes this backend an ActionRecorder, so the action-log
// middleware wraps the route the way it does in a deployment.
func (f *fakeManyBackend) RecordAPIAction(_ context.Context, a cliecho.APIAction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, a)
}

func (f *fakeManyBackend) PlanRecoverMany(ctx context.Context, req service.RecoverManyRequest) (service.RecoverManyPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.planned = append(f.planned, req)
	return f.plan, f.planErr
}

func (f *fakeManyBackend) RecoverMany(ctx context.Context, sess service.RecoverySession, req service.RecoverManyRequest, progress func(service.RecoverManyProgress)) (service.RecoverManyResult, error) {
	f.mu.Lock()
	f.ran = append(f.ran, req)
	f.sessions = append(f.sessions, sess)
	f.ctxs = append(f.ctxs, ctx)
	plan, planErr, result, runErr := f.plan, f.planErr, f.result, f.runErr
	f.mu.Unlock()
	if planErr != nil {
		return service.RecoverManyResult{}, planErr
	}
	if progress != nil {
		progress(service.RecoverManyProgress{Kind: "plan", Plan: &plan, Total: len(plan.Items), TotalBytes: plan.Totals.Bytes})
		if f.hold != nil {
			select {
			case <-f.hold:
			case <-ctx.Done():
				return result, ctx.Err()
			}
		}
		for i, it := range result.Items {
			progress(service.RecoverManyProgress{Kind: "start", Index: i + 1, Total: len(result.Items), Item: it, TotalBytes: plan.Totals.Bytes})
			progress(service.RecoverManyProgress{Kind: "item", Index: i + 1, Total: len(result.Items), Item: it, DoneBytes: int64(i+1) * 10, TotalBytes: plan.Totals.Bytes})
		}
	}
	return result, runErr
}

func newManyRouter(t *testing.T) (http.Handler, *fakeManyBackend) {
	t.Helper()
	be := &fakeManyBackend{fakeRecoveryBackend: &fakeRecoveryBackend{syncFakeBackend: newSyncFakeBackend()}}
	size := int64(10)
	be.plan = service.RecoverManyPlan{
		Set: "production/postgres", Target: service.RecoveryTarget{Kind: "staging", Path: "out"}, Folder: "out/production/postgres",
		Items: []service.RecoverManyItem{
			{ArtifactID: "production/postgres/a.zst", Name: "a.zst", Status: service.RecoverManyReady, Copy: "local", SizeBytes: &size},
			{ArtifactID: "production/postgres/b.zst", Name: "b.zst", Status: service.RecoverManyReady, Copy: "local", SizeBytes: &size},
		},
		Totals: service.RecoverManyTotals{Count: 2, Ready: 2, Bytes: 20},
	}
	be.result = service.RecoverManyResult{
		OperationID: "op-1", Set: "production/postgres", Target: be.plan.Target, Folder: be.plan.Folder,
		Items: []service.RecoverManyItem{
			{ArtifactID: "production/postgres/a.zst", Name: "a.zst", Status: service.RecoverManyRecovered, Copy: "local", Bytes: 10, SHA256: "aa", Path: "/s/out-1-a.zst"},
			{ArtifactID: "production/postgres/b.zst", Name: "b.zst", Status: service.RecoverManyFailed, Copy: "local", Code: service.RecoveryLocalCopyCorrupt, Message: "does not match"},
		},
		Totals:       service.RecoverManyTotals{Count: 2, Recovered: 1, Failed: 1, Bytes: 10},
		ManifestPath: "/s/out-2-recovery-manifest.json",
	}
	router := NewRouter(RouterConfig{
		Platform:      fakePlatformAdapter{caps: capabilities.PlatformCapabilities{NativeAuth: true}, auth: recoverySessionAuth{username: "alice"}},
		Backend:       be,
		Recorder:      be,
		Gate:          NotYetImplementedGate{},
		BinaryVersion: "test",
		Commit:        "test",
	})
	return router, be
}

func postMany(t *testing.T, router http.Handler, body string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/recoveries", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		attachValidCSRF(req)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func ndjson(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line is not JSON: %q", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

const manySetBody = `{"set":"production/postgres","target":{"kind":"staging","path":"out"},"acknowledged":true}`

func TestRecoverManyCarriesCSRFAndRefusesTheUnauthenticated(t *testing.T) {
	router, be := newManyRouter(t)
	if rec := postMany(t, router, manySetBody, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF = %d, want 403", rec.Code)
	}
	if len(be.ran)+len(be.planned) != 0 {
		t.Fatal("a CSRF-less request reached the service")
	}
	un := NewRouter(RouterConfig{Platform: noAuthWiredAdapter{}, Backend: be, Gate: NotYetImplementedGate{}, BinaryVersion: "t", Commit: "t"})
	if rec := postMany(t, un, manySetBody, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", rec.Code)
	}
	if len(be.ran)+len(be.planned) != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}
}

func TestRecoverManyBodyCrossesTheBoundaryAsTheServiceRequest(t *testing.T) {
	router, be := newManyRouter(t)
	rec := postMany(t, router, `{"set":"production/postgres","newest_only":true,"since":"2026-10-01T00:00:00Z","copy":"warm","acknowledged":true,"target":{"kind":"storage","medium":"other","key":"back"}}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if len(be.ran) != 1 {
		t.Fatalf("service ran %d times", len(be.ran))
	}
	r := be.ran[0]
	if r.Set != "production/postgres" || !r.NewestOnly || !r.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) ||
		r.Copy != "warm" || !r.AcknowledgeCost || r.Target.Kind != "storage" || r.Target.Medium != "other" || r.Target.Key != "back" {
		t.Fatalf("request = %+v", r)
	}
	if be.sessions[0].Actor != "alice" {
		t.Fatalf("session = %+v", be.sessions[0])
	}
	rec = postMany(t, router, `{"artifacts":["production/postgres/a.zst","production/postgres/b.zst"],"target":{"kind":"staging","path":"o"}}`, true)
	if rec.Code != http.StatusOK || len(be.ran) != 2 || len(be.ran[1].Artifacts) != 2 || be.ran[1].Set != "" {
		t.Fatalf("by-id request = %d, %+v", rec.Code, be.ran)
	}
}

// Issue #1251, the review of #1256: `recover --set --until` had no API twin.
// until is an instant on the wire, like since; the bare-date rule is the
// terminal's.
func TestIssue1251_RecoverManyUntilCrossesTheBoundary(t *testing.T) {
	router, be := newManyRouter(t)
	rec := postMany(t, router, `{"set":"production/postgres","since":"2026-10-01T00:00:00Z","until":"2026-10-07T23:59:59Z","target":{"kind":"staging","path":"o"}}`, true)
	if rec.Code != http.StatusOK || len(be.ran) != 1 {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if r := be.ran[0]; !r.Until.Equal(time.Date(2026, 10, 7, 23, 59, 59, 0, time.UTC)) || !r.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("request = %+v", r)
	}
	for name, body := range map[string]string{
		"bad until":          `{"set":"a/b","until":"tomorrow","target":{"kind":"staging","path":"o"}}`,
		"until before since": `{"set":"a/b","since":"2026-10-07T00:00:00Z","until":"2026-10-01T00:00:00Z","target":{"kind":"staging","path":"o"}}`,
	} {
		if rec := postMany(t, router, body, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(be.ran)+len(be.planned) != 1 {
		t.Fatal("a refused request reached the service")
	}
}

func TestRecoverManyRefusesAMalformedBodyBeforeTheService(t *testing.T) {
	router, be := newManyRouter(t)
	for name, body := range map[string]string{
		"unknown field":       `{"set":"a/b","actor":"mallory","target":{"kind":"staging","path":"o"}}`,
		"neither set nor ids": `{"target":{"kind":"staging","path":"o"}}`,
		"both set and ids":    `{"set":"a/b","artifacts":["a/b/c"],"target":{"kind":"staging","path":"o"}}`,
		"bad since":           `{"set":"a/b","since":"yesterday","target":{"kind":"staging","path":"o"}}`,
		"not json":            `{`,
	} {
		rec := postMany(t, router, body, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(be.ran)+len(be.planned) != 0 {
		t.Fatal("a malformed request reached the service")
	}
}

func TestRecoverManyPreflightAnswersThePlanAndRunsNothing(t *testing.T) {
	router, be := newManyRouter(t)
	rec := postMany(t, router, `{"set":"production/postgres","preflight":true,"target":{"kind":"staging","path":"out"}}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if len(be.ran) != 0 || len(be.planned) != 1 {
		t.Fatalf("ran %d, planned %d", len(be.ran), len(be.planned))
	}
	var plan struct {
		Set    string `json:"set"`
		Folder string `json:"folder"`
		Items  []struct {
			ArtifactID string `json:"artifact_id"`
			Status     string `json:"status"`
		} `json:"items"`
		Totals struct {
			Count int   `json:"count"`
			Ready int   `json:"ready"`
			Bytes int64 `json:"bytes"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Set != "production/postgres" || plan.Folder != "out/production/postgres" || len(plan.Items) != 2 || plan.Totals.Ready != 2 || plan.Totals.Bytes != 20 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestRecoverManyRefusedBeforeItStartsIsAnErrorResponseNotAStream(t *testing.T) {
	router, be := newManyRouter(t)
	be.planErr = &service.RecoveryError{Code: service.RecoveryCostNotAcknowledged, Message: "2 files, 30000 bytes are billed to read"}
	rec := postMany(t, router, manySetBody, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "RECOVERY_COST_NOT_ACKNOWLEDGED") || !strings.Contains(rec.Body.String(), "30000") {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "ndjson") {
		t.Fatalf("a refusal was sent as a stream: %s", rec.Header().Get("Content-Type"))
	}
}

func TestRecoverManyPreflightThatWouldBeRefusedAnswersThePlanAndSaysWhy(t *testing.T) {
	router, be := newManyRouter(t)
	be.planErr = &service.RecoveryError{Code: service.RecoveryNoSpace, Message: "the files need 20 bytes and the staging disk has 5 bytes free"}
	rec := postMany(t, router, `{"set":"production/postgres","preflight":true,"target":{"kind":"staging","path":"out"}}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	var plan struct {
		Items   []map[string]any `json:"items"`
		Refusal struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"refusal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 || plan.Refusal.Code != "RECOVERY_NO_SPACE" || !strings.Contains(plan.Refusal.Message, "20 bytes") {
		t.Fatalf("plan = %+v", plan)
	}
	// A pre-flight that could not plan at all is still an error response.
	be.plan.Items = nil
	rec = postMany(t, router, `{"set":"production/postgres","preflight":true,"target":{"kind":"staging","path":"out"}}`, true)
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("an empty plan with a refusal = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecoverManyStreamsOnePlanLineProgressPerFileAndOneResult(t *testing.T) {
	router, _ := newManyRouter(t)
	rec := postMany(t, router, manySetBody, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", rec.Header())
	}
	lines := ndjson(t, rec.Body.Bytes())
	var kinds []string
	for _, l := range lines {
		kinds = append(kinds, l["event"].(string))
	}
	if strings.Join(kinds, ",") != "plan,start,item,start,item,result" {
		t.Fatalf("events = %v", kinds)
	}
	item2 := lines[4]
	if item2["index"].(float64) != 2 || item2["total"].(float64) != 2 || item2["done_bytes"].(float64) != 20 || item2["total_bytes"].(float64) != 20 {
		t.Fatalf("second item event = %v", item2)
	}
	res := lines[5]["result"].(map[string]any)
	totals := res["totals"].(map[string]any)
	if res["operation_id"] != "op-1" || totals["recovered"].(float64) != 1 || totals["failed"].(float64) != 1 || res["manifest_path"] != "/s/out-2-recovery-manifest.json" {
		t.Fatalf("result = %v", res)
	}
	failed := res["items"].([]any)[1].(map[string]any)
	if failed["status"] != "failed" || failed["code"] != "RECOVERY_LOCAL_COPY_CORRUPT" {
		t.Fatalf("failed item = %v", failed)
	}
}

func TestRecoverManyCancelledRunEndsWithAResultThatSaysSo(t *testing.T) {
	router, be := newManyRouter(t)
	be.result.Cancelled = true
	be.runErr = &service.RecoveryError{Code: service.RecoveryInterrupted, Message: "interrupted"}
	rec := postMany(t, router, manySetBody, true)
	lines := ndjson(t, rec.Body.Bytes())
	last := lines[len(lines)-1]
	if last["event"] != "result" || last["result"].(map[string]any)["cancelled"] != true {
		t.Fatalf("last line = %v", last)
	}
}

func TestRecoverManyHandsTheRequestContextToTheServiceSoACancelReachesTheRun(t *testing.T) {
	router, be := newManyRouter(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/recoveries", strings.NewReader(manySetBody)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	router.ServeHTTP(httptest.NewRecorder(), req)
	cancel()
	if len(be.ctxs) != 1 {
		t.Fatalf("service ran %d times", len(be.ctxs))
	}
	if be.ctxs[0].Err() == nil {
		t.Fatal("the service was not given the request's context, so closing the connection cannot cancel the run")
	}
}

// A run is minutes long, so the plan line has to reach the client while the
// run is still going. Through the real router (the action-log middleware
// wraps every POST) and a real connection, the first line must arrive before
// the backend has finished.
func TestRecoverManyStreamReachesTheClientWhileTheRunIsStillGoing(t *testing.T) {
	router, be := newManyRouter(t)
	be.hold = make(chan struct{})
	srv := httptest.NewServer(router)
	defer srv.Close()
	defer func() {
		select {
		case <-be.hold:
		default:
			close(be.hold)
		}
	}()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/recoveries", strings.NewReader(manySetBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	// The request goes in a goroutine: a response that is buffered until the
	// run ends never returns its headers either, so Do itself would block.
	first := make(chan string, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			first <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, `"event":"plan"`) {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no line reached the client while the run was held: the response is buffered until the run ends")
	}
}

// The terminal shows the command an action is equivalent to, built from the
// request by core/cliecho and recorded by the action-log middleware, so a
// recovery of a set prints `retnd recover --set ...`.
func TestRecoverManyIsRecordedWithTheCommandItIsEquivalentTo(t *testing.T) {
	router, be := newManyRouter(t)
	rec := postMany(t, router, manySetBody, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.actions) != 1 {
		t.Fatalf("%d actions recorded, want 1", len(be.actions))
	}
	a := be.actions[0]
	if a.Route != "/recoveries" || a.Command != "retnd recover --set production/postgres --to out --acknowledge-cost" {
		t.Fatalf("recorded %+v", a)
	}
}
