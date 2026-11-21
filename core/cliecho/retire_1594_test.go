package cliecho

import (
	"net/url"
	"strings"
	"testing"
)

// Issue #1594: retiring a FAILED backup whose own copy at its final name could
// not be verified, with its source gone, takes the operator's
// accept_unverified_copy acknowledgement. The browser's terminal line has to
// carry it, on the action and on the preview, or the line it shows is not the
// command that does what the dialog is about to do.

func TestTheUnverifiedCopyAcknowledgementIsEchoed_1594(t *testing.T) {
	params := map[string]string{"source": "cicd-pipeline", "set": "var-backups", "name": "dpkg.diversions.5.gz"}

	post := Echo(Action{Method: "POST", Route: "/backups/{source}/{set}/{name}/retire", Params: params,
		Body: []byte(`{"accept_unverified_copy":true,"no_successor":true}`)}).Shell()
	if !strings.Contains(post, "--accept-unverified-copy") || !strings.Contains(post, "--no-successor") {
		t.Errorf("the retirement echoes %q, want --accept-unverified-copy and --no-successor", post)
	}

	get := Echo(Action{Method: "GET", Route: "/backups/{source}/{set}/{name}/retire", Params: params,
		Query: url.Values{"accept_unverified_copy": {"true"}}}).Shell()
	if !strings.Contains(get, "--dry-run") || !strings.Contains(get, "--accept-unverified-copy") {
		t.Errorf("the preview echoes %q, want --dry-run and --accept-unverified-copy", get)
	}

	plain := Echo(Action{Method: "POST", Route: "/backups/{source}/{set}/{name}/retire", Params: params, Body: []byte(`{}`)}).Shell()
	if strings.Contains(plain, "--accept-unverified-copy") {
		t.Errorf("a retirement without the acknowledgement echoes it anyway: %q", plain)
	}
}
