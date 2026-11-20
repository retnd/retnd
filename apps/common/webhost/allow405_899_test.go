package webhost

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #899 review: /metrics answers a wrong method itself, after the credential,
// so the refusal is counted rather than a bare 405. That must stay a property
// of /metrics. A method fallback set on the root mux is copied by chi into
// every subrouter, and a 405 anywhere else then loses the Allow header RFC 9110
// says it must carry.
func TestA405OutsideTheScrapeEndpointStillListsItsMethods(t *testing.T) {
	rig := newScrapeRig(t, scrapeBlockOn)
	token := rig.mint()

	for _, tc := range []struct{ method, path, want string }{
		{http.MethodDelete, "/api/v1/system/version", http.MethodGet},
		{http.MethodPut, "/api/v1/settings/observability", http.MethodPatch},
		{http.MethodDelete, "/health/live", http.MethodGet},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		rec := serve(rig.admin, csrfPaired(req))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", tc.method, tc.path, rec.Code)
			continue
		}
		if allow := strings.Join(rec.Header().Values("Allow"), ", "); !strings.Contains(allow, tc.want) {
			t.Errorf("%s %s answered 405 with Allow %q, want it to list %s", tc.method, tc.path, allow, tc.want)
		}
	}

	// And /metrics still answers a wrong method itself, once the credential
	// checks out: 405 with Allow: GET, counted as a refused method.
	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	req.RemoteAddr = "192.0.2.120:1000"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := serve(rig.public, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("POST /metrics with the credential = %d, Allow %q, Cache-Control %q, want 405, GET and no-store from the scrape handler", rec.Code, rec.Header().Get("Allow"), rec.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rig.svc.ScrapeRejectedMetrics(), `retnd_scrape_rejected_total{reason="method"} 1`) {
		t.Errorf("the refused method was not counted:\n%s", rig.svc.ScrapeRejectedMetrics())
	}
}
