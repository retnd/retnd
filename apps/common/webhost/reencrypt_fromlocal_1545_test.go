package webhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1545 over HTTP: from_local rides the dry run's query and the run's body,
// and nothing about the routes changes (no new route, so the agent catalogue
// still names both as refusals and an agent can neither plan nor start it).

type fromLocalSpy struct {
	*encryptionFakeBackend
	planned, started []apicontract.StartReencryptRequest
}

func (s *fromLocalSpy) PlanReencrypt(ctx context.Context, req apicontract.StartReencryptRequest) (apicontract.ReencryptPlan, error) {
	s.planned = append(s.planned, req)
	return s.encryptionFakeBackend.PlanReencrypt(ctx, req)
}

func (s *fromLocalSpy) StartReencrypt(ctx context.Context, req apicontract.StartReencryptRequest) (service.ScrubStarted, error) {
	s.started = append(s.started, req)
	return s.encryptionFakeBackend.StartReencrypt(ctx, req)
}

// Red until the query value is read: the backend must see FromLocal on the dry
// run, and not see it when the value is absent or false.
func TestTheReencryptDryRunRouteCarriesFromLocal_1545(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"?key=kold&from_local=true", true},
		{"?key=kold&from_local=false", false},
		{"?key=kold", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			spy := &fromLocalSpy{encryptionFakeBackend: newEncryptionFakeBackend()}
			rec := httptest.NewRecorder()
			encryptionRouter(spy, alwaysPassGate{}, nil).ServeHTTP(rec, validatorRequest(http.MethodGet, "/api/v1/encryption/reencrypt"+tc.query, "", ""))
			if rec.Code != http.StatusOK || len(spy.planned) != 1 || spy.planned[0].FromLocal != tc.want {
				t.Fatalf("%d %s, backend saw %+v, want FromLocal=%v", rec.Code, rec.Body.String(), spy.planned, tc.want)
			}
		})
	}
}

// Anything but true or false is refused, the way deprecated is, and never
// guessed at: this flag changes where bytes are read from.
func TestTheReencryptDryRunRefusesAMalformedFromLocal_1545(t *testing.T) {
	spy := &fromLocalSpy{encryptionFakeBackend: newEncryptionFakeBackend()}
	rec := httptest.NewRecorder()
	encryptionRouter(spy, alwaysPassGate{}, nil).ServeHTTP(rec, validatorRequest(http.MethodGet, "/api/v1/encryption/reencrypt?key=kold&from_local=yes", "", ""))
	if rec.Code != http.StatusBadRequest || responseErrorCode(rec.Body.String()) != "INVALID_REQUEST" {
		t.Fatalf("got %d %s, want 400 INVALID_REQUEST", rec.Code, rec.Body.String())
	}
	if len(spy.planned) != 0 {
		t.Fatalf("a malformed flag reached the backend: %+v", spy.planned)
	}
}

// The run's body carries it too, and still answers 202.
func TestTheReencryptRunRouteCarriesFromLocal_1545(t *testing.T) {
	spy := &fromLocalSpy{encryptionFakeBackend: newEncryptionFakeBackend()}
	rec := httptest.NewRecorder()
	encryptionRouter(spy, NewProfileGate(profile.Generic), nil).ServeHTTP(rec,
		validatorRequest(http.MethodPost, "/api/v1/encryption/reencrypt", `{"key_id":"kold","destination_medium":"offsite","from_local":true}`, ""))
	if rec.Code != http.StatusAccepted || len(spy.started) != 1 || !spy.started[0].FromLocal || !strings.Contains(rec.Body.String(), "op-17") {
		t.Fatalf("%d %s, backend saw %+v, want 202 and FromLocal", rec.Code, rec.Body.String(), spy.started)
	}
}
