package serve_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/retnd/retnd/apps/common/webhost/serve"
)

// The UI host is the front door in the two-container topology, so a browser
// recovering a backup goes through its reverse proxy. That proxy bounds the
// wait for the engine's first response byte at five seconds and the UI host's
// server bounds a whole response at thirty, both right for a JSON API and
// both fatal to a download: the engine verifies the whole copy before it
// sends a byte, which takes as long as the copy does to read, and then
// streams it. These tests hold the recovery route to its own bounds without
// loosening the ones every other route has.

func uiOver(t *testing.T, upstream *url.URL, headerTimeout, writeTimeout time.Duration) *httptest.Server {
	t.Helper()
	h := serve.NewUI(serve.UIConfig{
		Upstream:                   upstream,
		StaticFS:                   fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("shell")}},
		ProxyResponseHeaderTimeout: headerTimeout,
	})
	srv := httptest.NewUnstartedServer(h)
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestUI_ARecoveryMayTakeLongerThanTheProxyHeaderTimeoutToBegin(t *testing.T) {
	upstream := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // verifying the copy
		_, _ = io.WriteString(w, "the recovered bytes")
	})
	ui := uiOver(t, upstream, 100*time.Millisecond, 0)

	resp, err := http.Get(ui.URL + "/api/v1/recoveries/tok_abc")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "the recovered bytes" {
		t.Fatalf("a recovery that spends 500ms verifying was cut by the proxy: %d %q", resp.StatusCode, body)
	}

	// The control: the bound still holds for every other route, so this is
	// not a test that passes because the timeout stopped working.
	resp, err = http.Get(ui.URL + "/api/v1/system/version")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("an ordinary route that takes 500ms is %d, want 502: the proxy's header timeout must stay in force off the recovery route", resp.StatusCode)
	}
}

func TestUI_ARecoveryIsNotCutByTheUIHostsWholeResponseWriteTimeout(t *testing.T) {
	upstream := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		time.Sleep(400 * time.Millisecond) // verifying: longer than the write timeout on its own
		for i := 0; i < 4; i++ {
			_, _ = io.WriteString(w, strings.Repeat("x", 1000))
			fl.Flush()
			time.Sleep(150 * time.Millisecond)
		}
	})
	ui := uiOver(t, upstream, 0, 300*time.Millisecond)

	const cut = "the UI host's 300ms write timeout cut a download that was still being written"
	resp, err := http.Get(ui.URL + "/api/v1/recoveries/tok_abc")
	if err != nil {
		t.Fatalf("%s: %v", cut, err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil || n != 4000 {
		t.Fatalf("got %d of 4000 bytes, err %v: %s", n, err, cut)
	}
}

func TestUI_ADownloadsGrantIsNeverLoggedWhenTheProxyFails(t *testing.T) {
	rec := &recordingLogger{}
	ui := httptest.NewServer(serve.NewUI(serve.UIConfig{
		Upstream: deadUpstream(t),
		StaticFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("shell")}},
		Logger:   rec,
	}))
	defer ui.Close()

	resp, err := http.Get(ui.URL + "/api/v1/recoveries/SECRETGRANTTOKEN123")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	failures := rec.named("proxy_error")
	if len(failures) == 0 {
		t.Fatal("the failure was not logged at all, so the check below proves nothing")
	}
	for _, f := range failures {
		for k, v := range f.attrs {
			if strings.Contains(v, "SECRETGRANTTOKEN123") {
				t.Fatalf("a bearer grant reached the log in %q: %q", k, v)
			}
		}
	}
}

// The abort hop. A browser that cancels a download closes its connection to
// the UI host; the proxy has to carry that to the engine, or the engine keeps
// reading a multi-gigabyte copy off disk (or a billed provider) and writing
// it into a socket nobody is draining. The upstream here never ends on its own,
// so it only stops if the cancel reaches it. The engine's own half of this
// (an aborted response is a failed range on the service) is in webhost's
// handlers_recovery_test.go; this is the hop in between.
func TestUI_ABrowserThatAbortsMidDownloadStopsTheUpstreamStream(t *testing.T) {
	var (
		inflight atomic.Int32
		sent     atomic.Int64
		first    sync.Once
		started  = make(chan struct{})
	)
	upstream := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		inflight.Add(1)
		defer inflight.Add(-1)
		fl := w.(http.Flusher)
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for r.Context().Err() == nil {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			fl.Flush()
			first.Do(func() { close(started) })
			if err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	ui := uiOver(t, upstream, 0, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ui.URL+"/api/v1/recoveries/tok_abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := io.CopyN(io.Discard, resp.Body, 256<<10); err != nil {
		t.Fatalf("the download did not start: %v", err)
	}

	// The control: while the browser is there the stream really is running,
	// so "it stopped" below is the abort and not a stream that was never going.
	before := sent.Load()
	time.Sleep(100 * time.Millisecond)
	if sent.Load() == before || inflight.Load() != 1 {
		t.Fatalf("the upstream was not streaming before the abort (sent %d -> %d, %d in flight)", before, sent.Load(), inflight.Load())
	}

	cancel() // the browser's cancel button
	_ = resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for inflight.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := inflight.Load(); n != 0 {
		t.Fatalf("the proxy kept the engine streaming after the browser went away (%d handlers still in flight, %d bytes sent)", n, sent.Load())
	}
	settled := sent.Load()
	time.Sleep(200 * time.Millisecond)
	if sent.Load() != settled {
		t.Fatalf("the upstream wrote %d more bytes after it stopped", sent.Load()-settled)
	}
}
