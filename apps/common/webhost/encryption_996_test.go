package webhost

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"regexp"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// #996, NV.24: no key in an API response. The whole response is scanned, status
// line and headers included, in every rendering a leak could take and in the
// shape of an identity, over every encryption route that is not the export,
// under every refusal the backend can raise. The #993 test beside this one
// scans bodies for one literal; this one is the row's own proof and has its
// own control.
//
// Written by reading the router and the fakes; not run.

var identityShape996 = regexp.MustCompile(`AGE-SECRET-KEY-(PQ-)?1[A-Z0-9]{20,}`)

func leaks996(surface, material string) bool {
	if identityShape996.MatchString(surface) {
		return true
	}
	raw := []byte(material)
	for _, r := range []string{
		material,
		base64.StdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		hex.EncodeToString(raw),
	} {
		if strings.Contains(surface, r) {
			return true
		}
	}
	return false
}

func TestNoEncryptionResponseCarriesKeyMaterialInAnyRendering_996(t *testing.T) {
	var scanned int
	for _, refusal := range []error{nil, errors.New("an internal failure"), service.ErrEncryptionKeyInUse, service.ErrEncryptionKeyState, service.ErrInvalidRequest} {
		b := newEncryptionFakeBackend()
		b.err = refusal
		router := encryptionRouter(b, alwaysPassGate{}, nil)
		for _, tc := range everyEncryptionRequest {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, validatorRequest(tc.method, tc.path, tc.body, ""))
			dump, err := httputil.DumpResponse(rec.Result(), true)
			if err != nil {
				t.Fatal(err)
			}
			scanned++
			if leaks996(string(dump), bundleMaterial) {
				t.Errorf("%s (refusal %v) carries key material:\n%s", tc.name, refusal, dump)
			}
		}
	}
	if scanned != 5*len(everyEncryptionRequest) {
		t.Fatalf("scanned %d responses, want %d", scanned, 5*len(everyEncryptionRequest))
	}
}

// The control: a canary planted in a refusal's text, and one planted in a
// response header, are each found by the same detector over the same dump.
// Red if the detector reads only bodies or only the literal.
func TestNoEncryptionResponseCarriesKeyMaterialInAnyRendering_996_WouldCatchOne(t *testing.T) {
	b := newEncryptionFakeBackend()
	b.err = &namedError{kind: service.ErrEncryptionKeyState, text: "a planted " + hex.EncodeToString([]byte(bundleMaterial))}
	router := encryptionRouter(b, alwaysPassGate{}, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, validatorRequest(http.MethodPost, "/api/v1/encryption/keys/rotate", "", ""))
	dump, _ := httputil.DumpResponse(rec.Result(), true)
	if !leaks996(string(dump), bundleMaterial) {
		t.Error("a hex-rendered canary in a refusal was not found")
	}

	hdr := httptest.NewRecorder()
	hdr.Header().Set("X-Debug", base64.StdEncoding.EncodeToString([]byte(bundleMaterial)))
	hdr.WriteHeader(http.StatusOK)
	dump, _ = httputil.DumpResponse(hdr.Result(), true)
	if !leaks996(string(dump), bundleMaterial) {
		t.Error("a canary planted in a response header was not found")
	}
}
