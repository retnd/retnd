package webhost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/profile"
	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #993 over HTTP: the encryption routes, what each sends to the backend,
// what each refuses with, and that no response or refusal carries key
// material except the one route that exists to.

// bundleMaterial stands for a managed key's bytes. Only the export's fake
// answer holds it; every other route has to be able to answer without it.
const bundleMaterial = "AGE-SECRET-KEY-1CANARYCANARYCANARYCANARYCANARYCANARYCANARYCANARYCANARY"

type encryptionFakeBackend struct {
	*syncFakeBackend
	mu    sync.Mutex
	calls []string
	err   error
}

func newEncryptionFakeBackend() *encryptionFakeBackend {
	return &encryptionFakeBackend{syncFakeBackend: newSyncFakeBackend()}
}

func (f *encryptionFakeBackend) note(s string) error {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
	return f.err
}

func (f *encryptionFakeBackend) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

var sampleKey = apicontract.EncryptionKeyStatus{
	ID: "k0123456789abcdef", Status: "active", CreatedAt: "2026-11-01T00:00:00Z",
	Resolvable: true, Resolvability: "resolves",
}

func (f *encryptionFakeBackend) EncryptionPolicy(context.Context) (apicontract.EncryptionPolicy, error) {
	return apicontract.EncryptionPolicy{
		Mode: "required", KeyProvider: apicontract.EncryptionKeyProviderState{Kind: "external", Source: "command vault"},
		Sets: []apicontract.EncryptionSetPolicy{},
	}, f.note("policy")
}

func (f *encryptionFakeBackend) PatchEncryptionPolicy(_ context.Context, _ apicontract.EncryptionPolicyPatch) (apicontract.EncryptionPolicy, error) {
	return apicontract.EncryptionPolicy{Sets: []apicontract.EncryptionSetPolicy{}}, f.note("patch")
}

func (f *encryptionFakeBackend) ResetEncryptionPolicy(context.Context) (apicontract.EncryptionPolicy, error) {
	return apicontract.EncryptionPolicy{Sets: []apicontract.EncryptionSetPolicy{}}, f.note("reset")
}

func (f *encryptionFakeBackend) SetBackupSetEncryption(_ context.Context, id string, _ apicontract.EncryptionOverride) (apicontract.EncryptionSetPolicy, error) {
	return apicontract.EncryptionSetPolicy{BackupSetID: id}, f.note("set " + id)
}

func (f *encryptionFakeBackend) ClearBackupSetEncryption(_ context.Context, id string) (apicontract.EncryptionSetPolicy, error) {
	return apicontract.EncryptionSetPolicy{BackupSetID: id}, f.note("clear " + id)
}

func (f *encryptionFakeBackend) ListEncryptionKeys(context.Context) (apicontract.ListEncryptionKeysResponse, error) {
	return apicontract.ListEncryptionKeysResponse{Keys: []apicontract.EncryptionKeyStatus{sampleKey}}, f.note("keys")
}

func (f *encryptionFakeBackend) CreateEncryptionKey(context.Context) (apicontract.EncryptionKeyStatus, error) {
	return sampleKey, f.note("create")
}

func (f *encryptionFakeBackend) RotateEncryptionKey(context.Context) (apicontract.EncryptionKeyStatus, error) {
	return sampleKey, f.note("rotate")
}

func (f *encryptionFakeBackend) RetireEncryptionKey(_ context.Context, id string) (apicontract.EncryptionKeyStatus, error) {
	return sampleKey, f.note("retire " + id)
}

func (f *encryptionFakeBackend) ExportRecoveryBundleBytes(_ context.Context, id string) ([]byte, error) {
	return []byte(`{"key_id":"` + id + `","material":"` + bundleMaterial + `"}`), f.note("export " + id)
}

func (f *encryptionFakeBackend) ConfirmRecoveryBundle(_ context.Context, id, digest string) (apicontract.EncryptionKeyStatus, error) {
	return sampleKey, f.note("confirm " + id + " " + digest)
}

func (f *encryptionFakeBackend) TestEncryptionProvider(context.Context, apicontract.EncryptionProviderTestRequest) (apicontract.EncryptionProviderProbe, error) {
	return apicontract.EncryptionProviderProbe{Source: "command vault", State: "resolves"}, f.note("test")
}

func (f *encryptionFakeBackend) EncryptionCopies(_ context.Context, id string) (apicontract.ListEncryptionCopiesResponse, error) {
	return apicontract.ListEncryptionCopiesResponse{ArtifactID: id, Copies: []apicontract.EncryptionCopy{}}, f.note("copies " + id)
}

func (f *encryptionFakeBackend) StartDecryptVerify(_ context.Context, req apicontract.StartDecryptVerifyRequest) (service.ScrubStarted, error) {
	return service.ScrubStarted{BackupSetID: "src/set-1", OperationID: "op-1"}, f.note("verify " + req.ArtifactID)
}

func encryptionRouter(b BackupServiceClient, gate DestructiveGate, step StepUpVerifier) http.Handler {
	return validatorRouter(b, gate, step)
}

// everyEncryptionRequest is each route with a body it accepts.
var everyEncryptionRequest = []struct {
	name, method, path, body string
}{
	{"policy", http.MethodGet, "/api/v1/encryption/policy", ""},
	{"patch", http.MethodPatch, "/api/v1/encryption/policy", `{"mode":"required","key_provider":{"managed":true}}`},
	{"reset", http.MethodPost, "/api/v1/encryption/policy/reset", ""},
	{"set", http.MethodPut, "/api/v1/backup-sets/src/set-1/encryption", `{"mode":"required"}`},
	{"clear", http.MethodDelete, "/api/v1/backup-sets/src/set-1/encryption", ""},
	{"keys", http.MethodGet, "/api/v1/encryption/keys", ""},
	{"create", http.MethodPost, "/api/v1/encryption/keys", ""},
	{"rotate", http.MethodPost, "/api/v1/encryption/keys/rotate", ""},
	{"retire", http.MethodPost, "/api/v1/encryption/keys/k0123456789abcdef/retire", ""},
	{"confirm", http.MethodPost, "/api/v1/encryption/keys/k0123456789abcdef/confirm", ""},
	{"test", http.MethodPost, "/api/v1/encryption/providers/test", `{"managed_key_id":"k0123456789abcdef"}`},
	{"copies", http.MethodGet, "/api/v1/encryption/copies?artifact=src/set-1/a.dump", ""},
	{"verify", http.MethodPost, "/api/v1/encryption/verify", `{"artifact_id":"src/set-1/a.dump"}`},
}

func TestEveryEncryptionRouteReachesItsBackendMethod_993(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, NewProfileGate(profile.Generic), nil)
	for _, tc := range everyEncryptionRequest {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, ""))
		if rec.Code < 200 || rec.Code > 299 {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if got := len(b.called()); got != len(everyEncryptionRequest) {
		t.Errorf("the backend saw %d calls for %d routes: %v", got, len(everyEncryptionRequest), b.called())
	}
}

func TestABackendWithoutEncryptionAnswersUnavailable_993(t *testing.T) {
	router := encryptionRouter(newSyncFakeBackend(), alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodGet, "/api/v1/encryption/policy", "", ""))
	if rec.Code != http.StatusServiceUnavailable || responseErrorCode(rec.Body.String()) != "ENCRYPTION_UNAVAILABLE" {
		t.Errorf("got %d %s, want 503 ENCRYPTION_UNAVAILABLE", rec.Code, rec.Body.String())
	}
}

func TestEachEncryptionRefusalMapsToItsOwnStatusAndCode_993(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unknown key", service.ErrEncryptionKeyNotFound, http.StatusNotFound, "ENCRYPTION_KEY_NOT_FOUND"},
		{"a key a copy still records", service.ErrEncryptionKeyInUse, http.StatusConflict, "ENCRYPTION_KEY_IN_USE"},
		{"a key in the wrong state", service.ErrEncryptionKeyState, http.StatusConflict, "ENCRYPTION_KEY_STATE"},
		{"a key that does not resolve", service.ErrEncryptionKeyUnresolvable, http.StatusConflict, "ENCRYPTION_KEY_UNRESOLVABLE"},
		{"a deployment with no file", service.ErrConfigNotFileBacked, http.StatusServiceUnavailable, "ENCRYPTION_UNAVAILABLE"},
		{"a policy the file would refuse", service.ErrInvalidRequest, http.StatusBadRequest, "INVALID_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newEncryptionFakeBackend()
			b.err = tc.err
			router := encryptionRouter(b, alwaysPassGate{}, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/keys/rotate", "", ""))
			if rec.Code != tc.status || responseErrorCode(rec.Body.String()) != tc.code {
				t.Errorf("got %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
		})
	}
}

func TestARefusalNamesTheCopiesAKeyIsStillNeededFor_993(t *testing.T) {
	b := newEncryptionFakeBackend()
	b.err = &namedError{kind: service.ErrEncryptionKeyInUse, text: "refusing to retire key kold, 1 ACTIVE copies still record it: src/set-1/a.dump on warm"}
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/keys/kold/retire", "", ""))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "src/set-1/a.dump on warm") {
		t.Errorf("got %d %s, want 409 naming the copy", rec.Code, rec.Body.String())
	}
}

type namedError struct {
	kind error
	text string
}

func (e *namedError) Error() string        { return e.text }
func (e *namedError) Is(target error) bool { return target == e.kind }

func TestAnInlineKeyInAPolicyPatchIsRefusedAtTheDoor_993(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	for _, body := range []string{
		`{"key":"` + bundleMaterial + `"}`,
		`{"key_provider":{"key":"` + bundleMaterial + `"}}`,
		`{"key_provider":{"external":{"value":"` + bundleMaterial + `"}}}`,
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(http.MethodPatch, "/api/v1/encryption/policy", body, ""))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", body, rec.Code)
		}
		if strings.Contains(rec.Body.String(), bundleMaterial) {
			t.Errorf("the refusal echoes the key: %s", rec.Body.String())
		}
	}
	if len(b.called()) != 0 {
		t.Errorf("an inline key reached the backend: %v", b.called())
	}
}

func TestOnlyTheExportRouteCarriesKeyMaterialAndAPlantedOneInAnyRefusalIsFound_993(t *testing.T) {
	// Every route but the export, answering and refusing.
	for _, refusal := range []error{nil, errors.New("an internal failure"), service.ErrEncryptionKeyInUse, service.ErrInvalidRequest} {
		b := newEncryptionFakeBackend()
		b.err = refusal
		router := encryptionRouter(b, alwaysPassGate{}, nil)
		for _, tc := range everyEncryptionRequest {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, ""))
			if strings.Contains(rec.Body.String(), bundleMaterial) {
				t.Errorf("%s (refusal %v) carries key material: %s", tc.name, refusal, rec.Body.String())
			}
		}
	}

	// The control: material planted in a refusal's text is echoed (the
	// handler prints the service's own words whole), so this scan is the
	// guard that turns that route's row red.
	b := newEncryptionFakeBackend()
	b.err = &namedError{kind: service.ErrEncryptionKeyState, text: "a planted " + bundleMaterial}
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/keys/rotate", "", ""))
	if !strings.Contains(rec.Body.String(), bundleMaterial) {
		t.Error("a canary planted in a refusal was not found, so the scan above proves nothing about refusals")
	}
}

func TestTheBundleExportIsGatedAndAnswersAFileThatIsNeverCached_993(t *testing.T) {
	const path = "/api/v1/encryption/keys/k0123456789abcdef/export"

	t.Run("a shut gate refuses and nothing is exported", func(t *testing.T) {
		b := newEncryptionFakeBackend()
		router := encryptionRouter(b, NotYetImplementedGate{}, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(http.MethodPost, path, "", ""))
		if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "DESTRUCTIVE_OPERATIONS_DISABLED" {
			t.Errorf("got %d %s", rec.Code, rec.Body.String())
		}
		if len(b.called()) != 0 {
			t.Error("a shut gate still exported a bundle")
		}
	})
	t.Run("an open gate without the password asks for it", func(t *testing.T) {
		b := newEncryptionFakeBackend()
		router := encryptionRouter(b, alwaysPassGate{}, &stubStepUp{user: "alice", password: stepUpPassword})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(http.MethodPost, path, "", ""))
		if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "STEP_UP_REQUIRED" {
			t.Errorf("got %d %s", rec.Code, rec.Body.String())
		}
		if len(b.called()) != 0 {
			t.Error("an export without the password reached the backend")
		}
	})
	t.Run("where no password check is wired it fails closed", func(t *testing.T) {
		b := newEncryptionFakeBackend()
		router := encryptionRouter(b, alwaysPassGate{}, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(http.MethodPost, path, "", stepUpPassword))
		if rec.Code != http.StatusServiceUnavailable || responseErrorCode(rec.Body.String()) != "STEP_UP_UNAVAILABLE" {
			t.Errorf("got %d %s", rec.Code, rec.Body.String())
		}
		if len(b.called()) != 0 {
			t.Error("an export reached the backend with no way to check the password")
		}
	})
	t.Run("with the password the answer is an uncached attachment", func(t *testing.T) {
		b := newEncryptionFakeBackend()
		router := encryptionRouter(b, alwaysPassGate{}, &stubStepUp{user: "alice", password: stepUpPassword})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(http.MethodPost, path, "", stepUpPassword))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("Cache-Control = %q, want no-store", cc)
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "k0123456789abcdef") {
			t.Errorf("Content-Disposition = %q, want an attachment named for the key id", cd)
		}
		if !strings.Contains(rec.Body.String(), bundleMaterial) {
			t.Error("the export did not carry the bundle")
		}
	})
	t.Run("a key id that could break a header is left out of the file name", func(t *testing.T) {
		b := newEncryptionFakeBackend()
		router := encryptionRouter(b, alwaysPassGate{}, &stubStepUp{user: "alice", password: stepUpPassword})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/keys/k%22%0d%0aX-Evil:%201/export", "", stepUpPassword))
		if strings.Contains(rec.Header().Get("Content-Disposition"), "Evil") {
			t.Errorf("a hostile id reached the header: %q", rec.Header().Get("Content-Disposition"))
		}
	})
}

func TestTheConfirmationAcceptsNoKeyMaterial_993(t *testing.T) {
	b := newEncryptionFakeBackend()
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/keys/k0123456789abcdef/confirm", `{"digest":"abc","material":"`+bundleMaterial+`"}`, ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a confirmation carrying material was answered %d, want 400", rec.Code)
	}
	if len(b.called()) != 0 {
		t.Errorf("it reached the backend: %v", b.called())
	}
}
