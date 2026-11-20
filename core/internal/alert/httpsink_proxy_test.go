package alert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
)

// The refresh headroom config.Validate and NewHTTPSink demand is one delivery
// attempt, so the two numbers must be the same number.
func TestTheRefreshHeadroomIsOneDeliveryAttempt(t *testing.T) {
	if deliveryTimeout != config.WebhookDeliveryBound {
		t.Fatalf("deliveryTimeout %s, config.WebhookDeliveryBound %s: the headroom no longer covers one attempt", deliveryTimeout, config.WebhookDeliveryBound)
	}
}

// #1518 review: a plain-http webhook URL carries its secret in the path, and
// a forward proxy sees the whole URL of a plain-http request. So plain http
// never goes through a proxy from the environment. (https through a proxy is
// a CONNECT tunnel, which shows the proxy the host and nothing else.)
//
// Run it on its own to see it red: net/http reads the proxy variables once
// per process, so a test that made a request first would freeze them unset.
func TestWebhookPlainHTTPNeverGoesThroughAProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.example.invalid:3128")
	t.Setenv("http_proxy", "http://proxy.example.invalid:3128")
	s, err := NewHTTPSink(func(context.Context) (obs.Secret, error) { return obs.NewSecret("http://alertmanager:9093/x"), nil }, DialectGeneric, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	tr := s.client.Transport.(*http.Transport)
	if tr.Proxy == nil {
		return
	}
	req := httptest.NewRequest(http.MethodPost, "http://alertmanager:9093/api/v2/alerts", nil)
	if u, err := tr.Proxy(req); err != nil || u != nil {
		t.Fatalf("a plain-http webhook request goes to proxy %v (err %v): the proxy would see the URL's secret path", u, err)
	}
}
