package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Q4 of the EPIC U panel (#1364): CallRoute hands back the response's
// correlation id beside the body, and nothing else the client does changes.
//
// Falsify: return an empty string, or read the id from the request.
func TestCallRouteReturnsTheResponsesCorrelationID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-Id", "cid-from-engine")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL, BearerToken: "rat_test_token_0000000000000000000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	body, id, err := c.CallRoute(context.Background(), "GET", "/activity", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "cid-from-engine" || string(body) != `{"events":[]}` {
		t.Errorf("CallRoute = %q, %q; want the body and cid-from-engine", body, id)
	}
}
