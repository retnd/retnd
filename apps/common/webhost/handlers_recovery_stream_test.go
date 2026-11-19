package webhost

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// A streamed download over HTTP (EPIC Q, owner decisions 3 and 4): the
// validators a client resumes with, a first chunk checked before the status
// line, and a body that is cut loudly rather than ended quietly.

// flakyReader serves data and fails with err once failAt bytes have gone.
type flakyReader struct {
	data   []byte
	pos    int64
	failAt int64
	err    error
}

func (r *flakyReader) Read(p []byte) (int, error) {
	if r.pos >= int64(len(r.data)) {
		return 0, io.EOF
	}
	if r.failAt >= 0 && r.pos >= r.failAt {
		return 0, r.err
	}
	end := int64(len(r.data))
	if r.failAt >= 0 && end > r.failAt {
		end = r.failAt
	}
	n := copy(p, r.data[r.pos:end])
	r.pos += int64(n)
	return n, nil
}

func (r *flakyReader) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.pos = off
	case io.SeekCurrent:
		r.pos += off
	case io.SeekEnd:
		r.pos = int64(len(r.data)) + off
	}
	return r.pos, nil
}

// validatedStream is a stream that behaves like the service's: it carries
// validators, remembers the failure of its reader and can be primed.
type validatedStream struct {
	fakeStream
	rd       *flakyReader
	etag     string
	sha      string
	primeErr error
	failure  error
}

func (s *validatedStream) Reader() io.ReadSeeker { return s.rd }
func (s *validatedStream) ETag() string          { return s.etag }
func (s *validatedStream) SHA256() string        { return s.sha }
func (s *validatedStream) Prime(int64) error     { return s.primeErr }
func (s *validatedStream) Failure() error {
	if s.rd.pos >= s.rd.failAt && s.rd.failAt >= 0 && s.rd.pos < int64(len(s.rd.data)) {
		return s.rd.err
	}
	return s.failure
}

func streamBackend(be *fakeRecoveryBackend, failAt int64, primeErr error) *validatedStream {
	sum := sha256.Sum256(be.content)
	hexSum := ""
	for _, b := range sum {
		hexSum += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&15])
	}
	rd := &flakyReader{data: be.content, failAt: failAt, err: &service.RecoveryError{Code: service.RecoveryLocalCopyCorrupt, Message: "a chunk did not match"}}
	return &validatedStream{
		fakeStream: fakeStream{f: be, r: bytes.NewReader(be.content)},
		rd:         rd, etag: `"rcv1-` + hexSum[:32] + `"`, sha: hexSum, primeErr: primeErr,
	}
}

func TestADownloadCarriesAStrongETagAndTheRecordedDigest(t *testing.T) {
	router, be := newRecoveryRouter(t, alwaysPassGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.stream = func(f *fakeRecoveryBackend) service.RecoveryStream { return streamBackend(f, -1, nil) }
	rec := redeem(router, "tok_abc", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || strings.HasPrefix(etag, "W/") {
		t.Fatalf("ETag %q must be a strong validator", etag)
	}
	sum := sha256.Sum256(be.content)
	if want := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"; rec.Header().Get("Repr-Digest") != want {
		t.Fatalf("Repr-Digest %q, want %q", rec.Header().Get("Repr-Digest"), want)
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" || rec.Header().Get("Content-Length") != strconv.Itoa(len(be.content)) {
		t.Fatalf("a client cannot size or resume this: %v", rec.Header())
	}
}

func TestIfRangeWithTheETagResumesAndAStaleOneStartsOver(t *testing.T) {
	router, be := newRecoveryRouter(t, alwaysPassGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.stream = func(f *fakeRecoveryBackend) service.RecoveryStream { return streamBackend(f, -1, nil) }
	etag := redeem(router, "tok_abc", nil).Header().Get("ETag")

	rec := redeem(router, "tok_abc", map[string]string{"Range": "bytes=2500-", "If-Range": etag})
	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), be.content[2500:]) {
		t.Fatalf("resume: status %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Content-Range") != "bytes 2500-9999/10000" {
		t.Fatalf("Content-Range %q", rec.Header().Get("Content-Range"))
	}
	rec = redeem(router, "tok_abc", map[string]string{"Range": "bytes=2500-", "If-Range": `"rcv1-of-some-other-file"`})
	if rec.Code != http.StatusOK || rec.Body.Len() != len(be.content) {
		t.Fatalf("a stale If-Range must restart the download: status %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestAFirstChunkThatFailsVerificationIsAnErrorResponseNotA200(t *testing.T) {
	router, be := newRecoveryRouter(t, alwaysPassGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	bad := &service.RecoveryError{Code: service.RecoveryLocalCopyCorrupt, Message: "a chunk did not match"}
	be.stream = func(f *fakeRecoveryBackend) service.RecoveryStream { return streamBackend(f, -1, bad) }
	rec := redeem(router, "tok_abc", nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "RECOVERY_LOCAL_COPY_CORRUPT") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") != "" && rec.Body.Len() > 400 {
		t.Fatal("bytes of the artifact went out before the failure")
	}
	if len(be.served) != 1 || be.served[0].err == nil {
		t.Fatalf("the failure was not reported to the service: %+v", be.served)
	}
}

func TestAStreamThatFailsMidBodyIsCutNotEnded(t *testing.T) {
	router, be := newRecoveryRouter(t, alwaysPassGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.stream = func(f *fakeRecoveryBackend) service.RecoveryStream { return streamBackend(f, 4000, nil) }
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/recoveries/tok_abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ContentLength != int64(len(be.content)) {
		t.Fatalf("Content-Length %d: without it a short body cannot be told from a whole one", resp.ContentLength)
	}
	got, rerr := io.ReadAll(resp.Body)
	if rerr == nil {
		t.Fatalf("the client read %d of %d bytes and saw a clean end", len(got), len(be.content))
	}
	if !errors.Is(rerr, io.ErrUnexpectedEOF) {
		t.Logf("client error: %v", rerr)
	}
	if int64(len(got)) >= resp.ContentLength {
		t.Fatalf("got the whole body despite the failure")
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.served) != 1 || be.served[0].err == nil {
		t.Fatalf("the service was not told the download failed: %+v", be.served)
	}
}

// A request that asks for no bytes has nothing to serve, and so nothing to
// spend a grant on. A conditional GET that comes back 304 is how a client
// asks whether what it already holds is still the file. It used to reach the
// service as a whole-file range with nothing written, which the service reads
// as the whole artifact served and verified: the grant was spent and the
// download the client was about to make was refused (#1072).

// redeemWith is redeem with a method, recovering the panic a cut response
// raises so a failure here reads as a failure and not as a crashed test.
func redeemWith(router http.Handler, method, token string, hdr map[string]string) (rec *httptest.ResponseRecorder, aborted any) {
	req := httptest.NewRequest(method, "/api/v1/recoveries/"+token, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec = httptest.NewRecorder()
	func() {
		defer func() { aborted = recover() }()
		router.ServeHTTP(rec, req)
	}()
	return rec, aborted
}

// The route answers GET only, so a HEAD never reaches the grant: it is a 405
// from the router before RedeemRecovery is called. This pins that, because
// the day someone adds a HEAD route the handler below would send the service
// a whole-file range with nothing written, and the grant would go with it.
func TestAHeadRequestIsRefusedBeforeItTouchesTheGrant(t *testing.T) {
	router, be := newRecoveryRouter(t, alwaysPassGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.stream = func(f *fakeRecoveryBackend) service.RecoveryStream { return streamBackend(f, -1, nil) }

	rec, aborted := redeemWith(router, http.MethodHead, "tok_abc", nil)
	if aborted != nil {
		t.Fatalf("a HEAD aborted the response (%v)", aborted)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.redeemed) != 0 || len(be.served) != 0 {
		t.Fatalf("a HEAD (%d) redeemed %v and reported %+v: it must not reach the grant at all", rec.Code, be.redeemed, be.served)
	}
}

func TestAConditionalGetAnswered304SpendsNothing(t *testing.T) {
	router, be := newRecoveryRouter(t, alwaysPassGate{}, fakeAuthenticator{authenticated: true, username: "alice"})
	be.stream = func(f *fakeRecoveryBackend) service.RecoveryStream { return streamBackend(f, -1, nil) }
	etag := redeem(router, "tok_abc", nil).Header().Get("ETag")
	be.mu.Lock()
	be.served = nil
	be.mu.Unlock()

	rec, aborted := redeemWith(router, http.MethodGet, "tok_abc", map[string]string{"If-None-Match": etag})
	if aborted != nil || rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match with the current ETag = %d (aborted %v), want 304", rec.Code, aborted)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.served) != 0 {
		t.Fatalf("a 304 sent no byte but told the service it served %+v, and the service reads that as the whole file served and verified", be.served)
	}
}
