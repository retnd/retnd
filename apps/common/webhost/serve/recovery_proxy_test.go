package serve_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
