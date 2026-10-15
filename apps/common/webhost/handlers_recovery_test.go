package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// Recovery over HTTP (EPIC Q, #976): a POST that mints a grant and a GET
// that redeems it. The tests here hold the route tier (CSRF, no destructive
// gate), what crosses the boundary, the streaming headers, and that every
// refusal the service can make has a status and goes out under its own code.

type recoverySessionAuth struct {
	username string
	expires  string
}

func (a recoverySessionAuth) Authenticate(context.Context, capabilities.AuthRequest) (capabilities.AuthContext, error) {
	return capabilities.AuthContext{Authenticated: true, Username: a.username, Mode: capabilities.AuthModeLocalAccount, SessionExpiresAt: a.expires}, nil
}

type fakeRecoveryBackend struct {
	*syncFakeBackend

	mu         sync.Mutex
	minted     []service.RecoveryRequest
	mintSess   []service.RecoverySession
	redeemSess []service.RecoverySession
	redeemed   []string
	mintErr    error
	redeemErr  error
	thaw       *service.RecoveryThaw
	content    []byte
	result     *service.RecoveryResult
	served     []servedCall

	// redeemDelay stands in for the time the first redeem spends staging
	// and verifying a large copy before it can send a byte.
	redeemDelay time.Duration

	// stream, when set, builds the stream a redeem returns.
	stream func(f *fakeRecoveryBackend) service.RecoveryStream
}

type servedCall struct {
	start, end, written int64
	err                 error
}

func (f *fakeRecoveryBackend) MintRecovery(_ context.Context, s service.RecoverySession, r service.RecoveryRequest) (service.RecoveryGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted = append(f.minted, r)
	f.mintSess = append(f.mintSess, s)
	if f.mintErr != nil {
		return service.RecoveryGrant{}, f.mintErr
	}
	if f.thaw != nil {
		return service.RecoveryGrant{ArtifactID: r.ArtifactID, Copy: "cold", Thaw: f.thaw}, nil
	}
	n := int64(len(f.content))
	return service.RecoveryGrant{Token: "tok_abc", ExpiresAt: time.Date(2026, 10, 1, 12, 2, 0, 0, time.UTC),
		ArtifactID: r.ArtifactID, Copy: "local", Target: r.Target, SizeBytes: &n}, nil
}

func (f *fakeRecoveryBackend) RedeemRecovery(_ context.Context, s service.RecoverySession, token string) (service.RecoveryStream, *service.RecoveryResult, error) {
	time.Sleep(f.redeemDelay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redeemed = append(f.redeemed, token)
	f.redeemSess = append(f.redeemSess, s)
	if f.redeemErr != nil {
		return nil, nil, f.redeemErr
	}
	if f.result != nil {
		return nil, f.result, nil
	}
	if f.stream != nil {
		return f.stream(f), nil, nil
	}
	return &fakeStream{f: f, r: bytes.NewReader(f.content)}, nil, nil
}

type fakeStream struct {
	f *fakeRecoveryBackend
	r *bytes.Reader
}

func (s *fakeStream) FileName() string      { return "dump.zst" }
func (s *fakeStream) Length() int64         { return s.r.Size() }
func (s *fakeStream) Reader() io.ReadSeeker { return s.r }
func (s *fakeStream) Close()                {}
func (s *fakeStream) Served(start, end, written int64, err error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.served = append(s.f.served, servedCall{start, end, written, err})
}

func newRecoveryRouter(t *testing.T, gate DestructiveGate, auth capabilities.Authenticator) (http.Handler, *fakeRecoveryBackend) {
	t.Helper()
	be := &fakeRecoveryBackend{syncFakeBackend: newSyncFakeBackend(), content: bytes.Repeat([]byte("0123456789"), 1000)}
	router := NewRouter(RouterConfig{
		Platform:      fakePlatformAdapter{caps: capabilities.PlatformCapabilities{NativeAuth: true}, auth: auth},
		Backend:       be,
		Gate:          gate,
		BinaryVersion: "test",
		Commit:        "test",
	})
	return router, be
}

const mintPath = "/api/v1/backups/production/postgres/dump.zst/recovery"

func mint(t *testing.T, router http.Handler, body string, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, mintPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if withCSRF {
		attachValidCSRF(req)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func redeem(router http.Handler, token string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/recoveries/"+token, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestARecoveryCompletesWhileTheDestructiveGateIsShutAndOperationsStillRefuses(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})

	ops := submitOperation(t, router, "k1", `{"action":"run_cycle","config_revision":"x"}`)
	if ops.Code != http.StatusForbidden || !strings.Contains(ops.Body.String(), "DESTRUCTIVE_OPERATIONS_DISABLED") {
		t.Fatalf("POST /operations on a shut gate = %d %s", ops.Code, ops.Body.String())
	}

	m := mint(t, router, `{"target":{"kind":"download"}}`, true)
	if m.Code != http.StatusCreated {
		t.Fatalf("mint = %d %s", m.Code, m.Body.String())
	}
	var grant map[string]any
	_ = json.Unmarshal(m.Body.Bytes(), &grant)
	token, _ := grant["grant"].(string)
	if token == "" || grant["download_url"] != "/api/v1/recoveries/"+token {
		t.Fatalf("grant body = %v", grant)
	}

	got := redeem(router, token, nil)
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), be.content) {
		t.Fatalf("redeem = %d, %d bytes", got.Code, got.Body.Len())
	}
}

func TestDownloadHeadersMakeItAnAttachmentThatIsNeverSniffedOrCached(t *testing.T) {
	router, _ := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	rec := redeem(router, "tok_abc", nil)
	h := rec.Header()
	if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment;") || !strings.Contains(h.Get("Content-Disposition"), "dump.zst") {
		t.Errorf("Content-Disposition = %q", h.Get("Content-Disposition"))
	}
	if h.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("Content-Type = %q", h.Get("Content-Type"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", h.Get("X-Content-Type-Options"))
	}
	if !strings.Contains(h.Get("Cache-Control"), "no-store") {
		t.Errorf("Cache-Control = %q", h.Get("Cache-Control"))
	}
	if h.Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges = %q", h.Get("Accept-Ranges"))
	}
}

func TestADownloadFilenameCannotInjectHeaders(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	_ = be
	rec := redeem(router, "tok_abc", nil)
	for k, vs := range rec.Header() {
		for _, v := range vs {
			if strings.ContainsAny(v, "\r\n") {
				t.Fatalf("header %s carries a line break", k)
			}
		}
	}
}

func TestMintCarriesCSRFAndRedeemNeedsOnlyTheSession(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	if rec := mint(t, router, `{"target":{"kind":"download"}}`, false); rec.Code != http.StatusForbidden {
		t.Fatalf("mint without CSRF = %d", rec.Code)
	}
	if len(be.minted) != 0 {
		t.Fatal("a CSRF-less mint reached the backend")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/recoveries/tok_abc", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a redeem is a GET and takes no CSRF pair: %d", rec.Code)
	}
}

func TestBothRoutesRefuseTheUnauthenticated(t *testing.T) {
	be := &fakeRecoveryBackend{syncFakeBackend: newSyncFakeBackend()}
	router := NewRouter(RouterConfig{Platform: noAuthWiredAdapter{}, Backend: be, Gate: NotYetImplementedGate{}, BinaryVersion: "t", Commit: "t"})
	if rec := mint(t, router, `{}`, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("mint = %d", rec.Code)
	}
	if rec := redeem(router, "tok", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("redeem = %d", rec.Code)
	}
	if len(be.minted)+len(be.redeemed) != 0 {
		t.Fatal("an unauthenticated request reached the backend")
	}
}

func TestTheSessionIsTheAuthenticatedActorAndItsLapseNotAnythingTheBodySays(t *testing.T) {
	exp := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, recoverySessionAuth{username: "alice", expires: exp.Format(time.RFC3339)})
	m := mint(t, router, `{"copy":"warm","acknowledged":true,"actor":"mallory","target":{"kind":"staging","path":"out/x"}}`, true)
	if m.Code != http.StatusBadRequest {
		t.Fatalf("an unknown body field (actor) must be refused, got %d", m.Code)
	}
	m = mint(t, router, `{"copy":"warm","acknowledged":true,"target":{"kind":"staging","path":"out/x"}}`, true)
	if m.Code != http.StatusCreated {
		t.Fatalf("mint = %d %s", m.Code, m.Body.String())
	}
	req, sess := be.minted[0], be.mintSess[0]
	if req.ArtifactID != "production/postgres/dump.zst" || req.Copy != "warm" || !req.AcknowledgeCost ||
		req.Target.Kind != "staging" || req.Target.Path != "out/x" {
		t.Fatalf("request crossed the boundary wrongly: %+v", req)
	}
	if sess.Actor != "alice" || !sess.Expires.Equal(exp) {
		t.Fatalf("session = %+v", sess)
	}
	redeem(router, "tok_abc", nil)
	if be.redeemSess[0].Actor != "alice" || !be.redeemSess[0].Expires.Equal(exp) {
		t.Fatalf("redeem session = %+v", be.redeemSess[0])
	}
}

func TestAnAcknowledgementIsNeverImpliedByAnAbsentField(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	mint(t, router, `{"target":{"kind":"download"}}`, true)
	if be.minted[0].AcknowledgeCost {
		t.Fatal("cost was acknowledged by saying nothing")
	}
}

func TestMintBodyIsBounded(t *testing.T) {
	router, _ := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	big := `{"target":{"kind":"staging","path":"` + strings.Repeat("a", 20<<10) + `"}}`
	if rec := mint(t, router, big, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body = %d", rec.Code)
	}
}

func TestAMintThatStartedAProviderRestoreSays202AndNamesTheOperation(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.thaw = &service.RecoveryThaw{OperationID: "op_9", Wait: "up to 48 hours", Billing: "billed by the provider"}
	rec := mint(t, router, `{"acknowledged":true,"target":{"kind":"download"}}`, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	var b map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if _, has := b["grant"]; has {
		t.Fatal("no grant exists until the copy is readable")
	}
	r, _ := b["restore"].(map[string]any)
	if r["operation_id"] != "op_9" || r["wait"] == "" || r["billing"] == "" {
		t.Fatalf("restore block = %v", b["restore"])
	}
}

func TestARangeIsServedAs206AndAccountedExactly(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	rec := redeem(router, "tok_abc", map[string]string{"Range": "bytes=100-199"})
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 100 {
		t.Fatalf("= %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 100-199/10000" {
		t.Fatalf("Content-Range = %q", cr)
	}
	if len(be.served) != 1 || be.served[0] != (servedCall{100, 199, 100, nil}) {
		t.Fatalf("served = %+v", be.served)
	}
}

func TestAWholeDownloadIsAccountedAsZeroToLast(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	redeem(router, "tok_abc", nil)
	if len(be.served) != 1 || be.served[0] != (servedCall{0, 9999, 10000, nil}) {
		t.Fatalf("served = %+v", be.served)
	}
}

func TestAClientThatGoesAwayMidDownloadIsReportedAsAFailedRange(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/recoveries/tok_abc", nil)
	w := &failingWriter{ResponseRecorder: httptest.NewRecorder(), after: 5000}
	// A body cut short of its Content-Length aborts the response, which is
	// what a server turns into a dropped connection.
	func() {
		defer func() {
			if r := recover(); r != http.ErrAbortHandler {
				t.Fatalf("a download cut at 50%% must abort the response, recovered %v", r)
			}
		}()
		router.ServeHTTP(w, req)
	}()
	if len(be.served) != 1 || be.served[0].err == nil {
		t.Fatalf("a broken pipe at 50%% must reach the service as a failure: %+v", be.served)
	}
	if be.served[0].written >= 10000 {
		t.Fatalf("written = %d", be.served[0].written)
	}
}

func TestMultipleRangesInOneRequestAreRefusedNotHalfServed(t *testing.T) {
	router, _ := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("= 206: a multipart answer was started and then aborted (%v)", r)
		}
	}()
	rec := redeem(router, "tok_abc", map[string]string{"Range": "bytes=0-9,20-29"})
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("= %d", rec.Code)
	}
}

func TestEveryRefusalHasItsOwnStatusAndGoesOutUnderItsOwnCode(t *testing.T) {
	want := map[string]int{
		service.RecoveryCostNotAcknowledged: http.StatusConflict,
		service.RecoveryNoCopy:              http.StatusConflict,
		service.RecoveryCopyNotFound:        http.StatusNotFound,
		service.RecoveryNotReadable:         http.StatusConflict,
		service.RecoveryLocalCopyCorrupt:    http.StatusInternalServerError,
		service.RecoveryRemoteCopyCorrupt:   http.StatusBadGateway,
		service.RecoveryNoDigest:            http.StatusConflict,
		service.RecoveryNoSpace:             http.StatusInsufficientStorage,
		service.RecoveryBelowReserve:        http.StatusInsufficientStorage,
		service.RecoveryGrantUnknown:        http.StatusNotFound,
		service.RecoveryGrantUsed:           http.StatusGone,
		service.RecoveryGrantExpired:        http.StatusGone,
		service.RecoveryGrantSessionEnded:   http.StatusForbidden,
		service.RecoveryTooManyGrants:       http.StatusTooManyRequests,
		service.RecoveryTargetInvalid:       http.StatusBadRequest,
		service.RecoveryTargetExists:        http.StatusConflict,
		service.RecoveryUseSnapshotRestore:  http.StatusConflict,
		service.RecoveryUnavailable:         http.StatusServiceUnavailable,
		service.RecoveryBusy:                http.StatusConflict,
		service.RecoveryInterrupted:         http.StatusServiceUnavailable,
		service.RecoveryFailed:              http.StatusInternalServerError,
		service.RecoveryReadsBusy:           http.StatusTooManyRequests,
		service.RecoveryDestinationBusy:     http.StatusTooManyRequests,
		service.RecoveryDownloadsBusy:       http.StatusTooManyRequests,
		// Issue #1191.
		service.RecoverySourceUnavailable:     http.StatusConflict,
		service.RecoverySourceReadOnly:        http.StatusConflict,
		service.RecoverySourceNotAcknowledged: http.StatusConflict,
	}
	for _, code := range service.RecoveryCodes() {
		status, ok := want[code]
		if !ok {
			t.Errorf("code %s has no expected status here: the table and the service have drifted", code)
			continue
		}
		router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
		be.mintErr = &service.RecoveryError{Code: code, Message: "sentence for " + code}
		be.redeemErr = be.mintErr
		for name, rec := range map[string]*httptest.ResponseRecorder{
			"mint":   mint(t, router, `{"target":{"kind":"download"}}`, true),
			"redeem": redeem(router, "tok", nil),
		} {
			if rec.Code != status {
				t.Errorf("%s %s: status = %d, want %d", name, code, rec.Code, status)
			}
			var b errorResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &b)
			if b.Error.Code != code || !strings.Contains(b.Error.Message, "sentence for") {
				t.Errorf("%s %s: body = %s", name, code, rec.Body.String())
			}
		}
	}
	if len(want) != len(service.RecoveryCodes()) {
		t.Errorf("table has %d codes, service %d", len(want), len(service.RecoveryCodes()))
	}
}

func TestAnArtifactThatIsNotThereIs404NotAnInternalError(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.mintErr = fmt.Errorf("%w: x", service.ErrArtifactNotFound)
	rec := mint(t, router, `{"target":{"kind":"download"}}`, true)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "ARTIFACT_NOT_FOUND") {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnUnexpectedErrorIsNeverEchoedToTheClient(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.mintErr = errors.New("open /srv/secret/path/credentials.json: permission denied")
	rec := mint(t, router, `{"target":{"kind":"download"}}`, true)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "/srv/secret") {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
}

func TestAServerSideTargetAnswersWithAJSONResultAndNeverStreams(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.result = &service.RecoveryResult{ArtifactID: "production/postgres/dump.zst", Copy: "local",
		Target: service.RecoveryTarget{Kind: "staging", Path: "x"}, Path: "/state/recovery/rcv-1-x", Bytes: 10, SHA256: strings.Repeat("a", 64)}
	rec := redeem(router, "tok_abc", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("= %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var b map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if b["bytes"].(float64) != 10 || b["sha256"] == "" || b["path"] == "" {
		t.Fatalf("body = %v", b)
	}
}

func TestTheRecoveryRoutesAreRegisteredInTheTierTheyClaim(t *testing.T) {
	if !destructiveGateExemptRoutes["POST /api/v1/backups/{source}/{set}/{name}/recovery"] {
		t.Fatal("the mint is exempt from the destructive gate on purpose, and the exemption must be written down with its reason")
	}
}

type failingWriter struct {
	*httptest.ResponseRecorder
	after int
	n     int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.n+len(p) > f.after {
		return 0, io.ErrClosedPipe
	}
	f.n += len(p)
	return f.ResponseRecorder.Write(p)
}

// The server's write timeout is 30 seconds for the whole response
// (serve/run.go), which is right for every JSON route and wrong for a
// download: a 4 GiB artifact over a LAN takes longer than that, and the
// connection would be cut at the deadline with the bytes half sent. A
// loopback measurement hides it, which is why it is a test of its own.

func TestADownloadSlowerThanTheServersWriteTimeoutStillCompletes(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.content = bytes.Repeat([]byte("x"), 32<<20)

	srv := httptest.NewUnstartedServer(router)
	srv.Config.WriteTimeout = 400 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/recoveries/tok_abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got int
	buf := make([]byte, 4<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		got += n
		if rerr != nil {
			if rerr != io.EOF {
				t.Fatalf("download cut after %d of %d bytes: %v", got, len(be.content), rerr)
			}
			break
		}
		time.Sleep(100 * time.Millisecond) // eight reads: about 800ms, and the kernel buffers hold only a few MB of it, so the server is still writing well past its 400ms timeout
	}
	if got != len(be.content) {
		t.Fatalf("got %d of %d bytes", got, len(be.content))
	}
}

func TestAClientThatStopsReadingIsCutOffAfterTheIdleBound(t *testing.T) {
	old := recoveryWriteIdle
	recoveryWriteIdle = 300 * time.Millisecond
	defer func() { recoveryWriteIdle = old }()

	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.content = bytes.Repeat([]byte("x"), 64<<20)

	srv := httptest.NewUnstartedServer(router)
	srv.Config.WriteTimeout = 400 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/recoveries/tok_abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.Sleep(1500 * time.Millisecond) // a client that stalls entirely
	n, _ := io.Copy(io.Discard, resp.Body)
	if n >= int64(len(be.content)) {
		t.Fatal("a client that stalled past the idle bound was still served the whole file: a stalled connection would hold its staged copy forever")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		be.mu.Lock()
		got := len(be.served)
		var err error
		if got > 0 {
			err = be.served[0].err
		}
		be.mu.Unlock()
		if got > 0 {
			if err == nil {
				t.Fatal("the abandoned download was reported as a success")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the abandoned download was never reported to the service, so its grant and staged file would stay")
}

func TestStagingThatTakesLongerThanTheWriteTimeoutDoesNotCostTheDownload(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.redeemDelay = 700 * time.Millisecond // verifying a big remote copy

	srv := httptest.NewUnstartedServer(router)
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/recoveries/tok_abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil || n != int64(len(be.content)) {
		t.Fatalf("got %d of %d bytes, err %v: the write deadline expired while the copy was being verified", n, len(be.content), err)
	}
}

type countingRecorder struct {
	mu    sync.Mutex
	calls int
}

func (c *countingRecorder) RecordAPIAction(context.Context, cliecho.APIAction) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
}

func TestAMintIsAuditedOnceByItsOwnRowAndNotAgainByTheGenericRecorder(t *testing.T) {
	be := &fakeRecoveryBackend{syncFakeBackend: newSyncFakeBackend(), content: []byte("x")}
	rec := &countingRecorder{}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: be, Gate: alwaysPassGate{},
		BinaryVersion: "t", Commit: "t", Recorder: rec,
	})

	// The positive control: the same router and recorder do record an
	// ordinary non-GET, so a count of zero below means something.
	submitOperation(t, router, "k1", `{"action":"run_cycle","config_revision":"`+be.ConfigRevision()+`"}`)
	if rec.calls != 1 {
		t.Fatalf("the recorder saw %d rows for an ordinary POST, want 1: the control is broken", rec.calls)
	}

	before := rec.calls
	if m := mint(t, router, `{"target":{"kind":"download"}}`, true); m.Code != http.StatusCreated {
		t.Fatalf("mint = %d", m.Code)
	}
	if rec.calls != before {
		t.Fatalf("the generic recorder wrote %d extra row(s) for a mint that audits itself: one mint must be one row", rec.calls-before)
	}

	// A refusal before the handler runs is still recorded: that is why the
	// flag is raised by the handler and not decided from the route.
	mint(t, router, `{"target":{"kind":"download"}}`, false) // no CSRF pair
	if rec.calls != before+1 {
		t.Fatalf("a CSRF refusal on the mint route wrote %d rows, want 1", rec.calls-before)
	}
}

type capturedLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturedLogger) Event(_ context.Context, _ slog.Level, event, msg string, attrs ...slog.Attr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := event + " " + msg
	for _, a := range attrs {
		line += " " + a.Key + "=" + a.Value.String()
	}
	c.lines = append(c.lines, line)
}

func TestAGrantIsNeverWrittenToTheLogWhenARedeemFails(t *testing.T) {
	be := &fakeRecoveryBackend{syncFakeBackend: newSyncFakeBackend(), content: []byte("x")}
	be.redeemErr = errors.New("something broke underneath")
	lg := &capturedLogger{}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: be, Gate: NotYetImplementedGate{},
		BinaryVersion: "t", Commit: "t", Logger: lg,
	})
	rec := redeem(router, "SECRETGRANTTOKEN123", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("= %d", rec.Code)
	}
	if len(lg.lines) == 0 {
		t.Fatal("the failure was not logged at all, so the check below proves nothing")
	}
	for _, l := range lg.lines {
		if strings.Contains(l, "SECRETGRANTTOKEN123") {
			t.Fatalf("a bearer grant reached the log: %s", l)
		}
	}
}

func TestAnUnsatisfiableRangeIsReportedAsARefusalNotAFailedDownload(t *testing.T) {
	router, be := newRecoveryRouter(t, NotYetImplementedGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	rec := redeem(router, "tok_abc", map[string]string{"Range": "bytes=99999999-"})
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("= %d", rec.Code)
	}
	if len(be.served) != 1 || !errors.Is(be.served[0].err, service.ErrRecoveryRangeNotSatisfiable) {
		t.Fatalf("served = %+v: a client's bad offset must not read as the download failing, because that spends the grant", be.served)
	}
}
