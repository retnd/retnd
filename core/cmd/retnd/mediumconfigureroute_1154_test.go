package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1154: `medium configure` is a configuration write, so beside a serving
// engine it goes to the engine's PUT /storage-mediums/{id}/configuration
// (and POST .../configuration/preflight for --preflight) rather than into a
// file that process would never read (#538, #543). The fake engine serves
// both off the real BackupService, so what lands in its configuration is
// what core/service wrote.

// configurationBodies is what each fake engine received on the two routes,
// keyed by the engine, so a test can assert on the request the CLI sent and
// not only on what the engine did with it.
var configurationBodies sync.Map // *fakeEngine -> []apicontract.MediumConfigurationRequest

func (e *fakeEngine) recordConfigurationBody(body apicontract.MediumConfigurationRequest) {
	prev, _ := configurationBodies.Load(e)
	list, _ := prev.([]apicontract.MediumConfigurationRequest)
	configurationBodies.Store(e, append(list, body))
}

func (e *fakeEngine) receivedConfigurationBodies() []apicontract.MediumConfigurationRequest {
	prev, _ := configurationBodies.Load(e)
	list, _ := prev.([]apicontract.MediumConfigurationRequest)
	return list
}

func configurationFromWire(body apicontract.MediumConfigurationRequest) service.StorageMediumConfiguration {
	fields := make(map[string]string, len(body.Fields))
	for _, f := range body.Fields {
		fields[f.Field] = f.Value
	}
	return service.StorageMediumConfiguration{
		Backend: body.Backend,
		Fields:  fields,
		Credentials: service.StorageMediumCredentials{
			ID: body.Credentials.CredentialsID, File: body.Credentials.File,
			Env: body.Credentials.Env, Command: body.Credentials.Command,
		},
	}
}

func refuseMediumConfiguration(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrMediumNotFound):
		refuse(w, http.StatusNotFound, apicontract.ErrorCodeMediumNotFound, err.Error())
	case errors.Is(err, service.ErrInvalidRequest):
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
	case errors.Is(err, service.ErrStorageMediumNotProven):
		refuse(w, http.StatusConflict, apicontract.ErrorCodeMediumConnectionNotProven, err.Error())
	default:
		refuse(w, http.StatusInternalServerError, apicontract.ErrorCodeInternal, err.Error())
	}
}

func (e *fakeEngine) configureMedium(w http.ResponseWriter, r *http.Request, id string) {
	var body apicontract.MediumConfigurationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}
	e.recordConfigurationBody(body)
	medium, err := e.svc.ConfigureStorageMedium(r.Context(), id, configurationFromWire(body))
	if err != nil {
		refuseMediumConfiguration(w, err)
		return
	}
	writeJSON(w, http.StatusOK, service.StorageMediumWire(medium))
}

func (e *fakeEngine) preflightMediumConfiguration(w http.ResponseWriter, r *http.Request, id string) {
	var body apicontract.MediumConfigurationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, apicontract.ErrorCodeInvalidRequest, err.Error())
		return
	}
	e.recordConfigurationBody(body)
	report, err := e.svc.PreflightStorageMediumConfiguration(r.Context(), id, configurationFromWire(body))
	if err != nil {
		refuseMediumConfiguration(w, err)
		return
	}
	writeJSON(w, http.StatusOK, service.MediumPreflightWire(report))
}

func TestIssue1154_ConfigureBesideAnEngineChangesTheEnginesConfigurationNotTheCLIs(t *testing.T) {
	// Two files, one deployment: the only arrangement in which "the engine's
	// file changed and the CLI's did not" can be asserted at all.
	cliConfig := writeTestConfig(t)
	e := startFakeEngineFor(t, cliConfig)
	e.attach(t)
	dir := t.TempDir()

	var code int
	out := captureStdout(t, func() {
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume",
			"--field", "path=" + dir, "--field", "prefix=retnd", "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("medium configure beside an engine = %d, want 0:\n%s", code, out)
	}
	if !containsRequest(e.requests(), "PUT /storage-mediums/usb_disk/configuration") {
		t.Errorf("the engine never received PUT /storage-mediums/usb_disk/configuration; it saw %v", e.requests())
	}
	bodies := e.receivedConfigurationBodies()
	if len(bodies) != 1 || bodies[0].Backend != "local_volume" {
		t.Fatalf("the engine received %+v, want one request naming backend local_volume", bodies)
	}
	got := map[string]string{}
	for _, f := range bodies[0].Fields {
		got[f.Field] = f.Value
	}
	if got["path"] != dir || got["prefix"] != "retnd" || len(got) != 2 {
		t.Errorf("the request carried fields %v, want exactly path=%s and prefix=retnd", got, dir)
	}
	// Asked of the ENGINE, not of the printed report.
	engineSide := readFile(t, e.configPath)
	if !strings.Contains(engineSide, "usb_disk") || !strings.Contains(engineSide, dir) {
		t.Errorf("the engine's configuration does not carry the destination:\n%s", engineSide)
	}
	if strings.Contains(readFile(t, cliConfig), "usb_disk") {
		t.Error("the CLI wrote the destination into its own file as well as sending it, so the engine and the file now disagree")
	}
}

func TestIssue1154_ConfigureForwardsACredentialReferenceAndNeverAValue(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)
	t.Setenv("S12_FAKE_CREDENTIALS", "not-read-by-the-cli")

	captureStdout(t, func() {
		run([]string{"medium", "configure", "cold", "--backend", "s3",
			"--field", "bucket=acme", "--field", "region=eu-central-1",
			"--credentials-env", "S12_FAKE_CREDENTIALS", "--config", cliConfig})
	})
	bodies := e.receivedConfigurationBodies()
	if len(bodies) != 1 {
		t.Fatalf("the engine received %d configuration requests, want 1 (it saw %v)", len(bodies), e.requests())
	}
	if bodies[0].Credentials.Env != "S12_FAKE_CREDENTIALS" {
		t.Errorf("the credentials reference was dropped on the way: %+v", bodies[0].Credentials)
	}
	if bodies[0].Credentials.CredentialsID != "" || bodies[0].Credentials.File != "" || len(bodies[0].Credentials.Command) != 0 {
		t.Errorf("the request names credentials the operator did not: %+v", bodies[0].Credentials)
	}
}

func TestIssue1154_ConfigurePreflightBesideAnEngineUsesThePreflightRouteAndWritesNothing(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)
	before := readFile(t, e.configPath)

	var code int
	out := captureStdout(t, func() {
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume", "--field", "path=" + t.TempDir(), "--preflight", "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("medium configure --preflight beside an engine = %d:\n%s", code, out)
	}
	reqs := e.requests()
	if !containsRequest(reqs, "POST /storage-mediums/usb_disk/configuration/preflight") {
		t.Errorf("the engine never received the preflight route; it saw %v", reqs)
	}
	if containsRequest(reqs, "PUT /storage-mediums/usb_disk/configuration") {
		t.Errorf("--preflight also sent the write: %v", reqs)
	}
	if after := readFile(t, e.configPath); after != before {
		t.Errorf("--preflight changed the engine's configuration:\n%s", after)
	}
}

func TestIssue1154_ConfigureTheEnginesRefusalComesBackAndExitsNonZero(t *testing.T) {
	cliConfig, e := oneServedDeployment(t)
	before := readFile(t, e.configPath)
	var code int
	captureStdout(t, func() {
		// `path` omitted: the engine refuses it by the manifest's rule.
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume", "--field", "prefix=x", "--config", cliConfig})
	})
	if code == 0 {
		t.Error("a configure the engine refused exited 0")
	}
	if !containsRequest(e.requests(), "PUT /storage-mediums/usb_disk/configuration") {
		t.Errorf("the refusal did not come from the engine: it never received the write (it saw %v)", e.requests())
	}
	if readFile(t, e.configPath) != before {
		t.Error("a refused configure changed the engine's configuration")
	}
}
