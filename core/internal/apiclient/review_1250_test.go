package apiclient

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1250's review: three things about what this client puts on the wire and
// on a terminal.

// M7: the key goes only on an operation the contract says takes one. The
// gate used to be "anything but none", so an endpoint the generator emitted
// with an empty or new value would have carried the key, sign-in included.
func TestClient1250Review_TheKeyGoesOnlyWhereTheContractAsksForIt(t *testing.T) {
	engine := newFakeEngine(t)
	client := engine.client(t, engine.start())
	ctx := withIdempotencyKey(context.Background(), "k-1")
	target, err := url.Parse(engine.start() + "/api/v1/somewhere")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		value string
		want  string
	}{
		{"required", "k-1"},
		{"optional", "k-1"},
		{"none", ""},
		{"", ""},
		{"something-new", ""},
	} {
		req, err := client.newRequest(ctx, apicontract.Endpoint{ID: "x", Method: "GET", IdempotencyKey: c.value}, target, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get(idempotencyKeyHeaderName); got != c.want {
			t.Errorf("an endpoint whose IdempotencyKey is %q carried %q, want %q", c.value, got, c.want)
		}
	}
}

// M8: a password with a character url.Parse chokes on used to come back in
// full, because redactURL returned the raw string when parsing failed and the
// refusal quoted the parse error, which quotes the URL again.
func TestClient1250Review_AnUnparseableAddressNeverPrintsItsUserinfo(t *testing.T) {
	for _, c := range []struct{ raw, secret string }{
		{"http://u:pa/ss@host", "pa/ss"},
		{"http://u:pa?ss@host", "pa?ss"},
		{"http://u:p@ss@host", "p@ss"},
		{"http://u:pa ss@host:80", "pa ss"},
	} {
		if got := redactURL(c.raw); strings.Contains(got, c.secret) {
			t.Errorf("redactURL(%q) = %q, which still carries the userinfo", c.raw, got)
		}
		_, err := New(Config{BaseURL: c.raw})
		if err == nil {
			t.Errorf("New(%q) accepted an address carrying a password", c.raw)
			continue
		}
		if strings.Contains(err.Error(), c.secret) {
			t.Errorf("New(%q) refused with %q, which carries the password", c.raw, err.Error())
		}
	}
	// The control: an address without userinfo that does not parse still
	// says what was given, since there is nothing in it to hide.
	if got := redactURL("http://host:bad port"); !strings.Contains(got, "host") {
		t.Errorf("an unparseable address with no userinfo was hidden too: %q", got)
	}
}

// The transport opt-in (#1250's remote mode): only a client told to
// acknowledge plain http sends the acknowledgement, and only on an operation
// behind the destructive gate, which is the only place the engine reads it.
func TestClient1250Review_TheInsecureTransportAckIsOptInAndGatedOnly(t *testing.T) {
	for _, ack := range []bool{false, true} {
		engine := newFakeEngine(t)
		client, err := New(Config{BaseURL: engine.start(), Username: engine.username, Password: engine.password, AcknowledgeInsecureTransport: ack})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := client.SubmitOperation(ctx, "k", apicontract.SubmitOperationRequest{Action: apicontract.ActionRunCycle, ConfigRevision: "r"}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ListOperations(ctx); err != nil {
			t.Fatal(err)
		}
		for _, r := range engine.requests() {
			want := ""
			if ack && r.Operation == "submitOperation" {
				want = "acknowledged"
			}
			if r.InsecureAck != want {
				t.Errorf("ack=%v: %s carried the acknowledgement %q, want %q", ack, r.Operation, r.InsecureAck, want)
			}
		}
	}
}
