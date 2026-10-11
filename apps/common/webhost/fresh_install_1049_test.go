package webhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The API side of the first-run invariant (retnd#1049). An instance with
// no configuration answers every read outside the setup surface with
// 503 NOT_CONFIGURED (firstrun.go, writeNotConfigured). To the pages that
// read them, that is a service refusing: the Settings page's account
// recovery card renders it as a red ErrorState quoting
// "/api/v1/system/first-run" at the operator, and every other card says
// its subject is "part of a configuration this instance has not been
// given yet". None of that is an answer to "what are my settings" on a
// deployment whose settings are, honestly, the defaults.
//
// These cases ask for the answers a fresh install can truthfully give:
// the defaults the engine would write, marked as such; an empty health
// report; and a first-run status that says which setup step is next, so
// a page can lead rather than refuse.

func getJSONInto(t *testing.T, router http.Handler, path string, into any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("GET %s: decoding the 200 body: %v\n%s", path, err, rec.Body.String())
		}
	}
	return rec
}

func TestUnconfiguredInstance_SettingsReadAnswersTheDefaults(t *testing.T) {
	router := unconfiguredRouter(&fakeFirstRun{})

	var body struct {
		FirstRun bool `json:"first_run"`
		Service  struct {
			PollIntervalSeconds int `json:"poll_interval_seconds"`
		} `json:"service"`
	}
	rec := getJSONInto(t, router, "/api/v1/settings", &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings on an unconfigured instance = %d, want 200: a fresh install's settings are the engine's own defaults, not a service that is unavailable\nbody: %s", rec.Code, rec.Body.String())
	}
	if !body.FirstRun {
		t.Errorf("the defaults are not marked first_run, so a client cannot say \"nobody has changed these yet\"")
	}
	if body.Service.PollIntervalSeconds != 3600 {
		t.Errorf("service.poll_interval_seconds = %d, want 3600 (core/service.DefaultFirstRunPollInterval, the documented first-run cadence)", body.Service.PollIntervalSeconds)
	}
}

func TestUnconfiguredInstance_HealthReadAnswersAnEmptyReport(t *testing.T) {
	router := unconfiguredRouter(&fakeFirstRun{})

	var body struct {
		BackupSets []json.RawMessage `json:"backup_sets"`
	}
	rec := getJSONInto(t, router, "/api/v1/system/health", &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/system/health on an unconfigured instance = %d, want 200: nothing is being backed up yet, which is an empty report rather than an unavailable one\nbody: %s", rec.Code, rec.Body.String())
	}
	if len(body.BackupSets) != 0 {
		t.Errorf("backup_sets = %d entries on a fresh install, want 0", len(body.BackupSets))
	}
}

func TestFirstRunStatus_NamesTheSetupStepsAndTheNextOne(t *testing.T) {
	router := unconfiguredRouter(&fakeFirstRun{})

	var body struct {
		NextStep string            `json:"next_step"`
		Steps    map[string]string `json:"steps"`
	}
	rec := getJSONInto(t, router, "/api/v1/system/first-run", &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/system/first-run = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	// The guided experience is driven from this answer: which steps
	// exist, which are done or skipped, and the one to do next. With no
	// backup set anywhere, the next step is the first one, and the page
	// that reads this can say so instead of refusing.
	if body.NextStep != "backup_set" {
		t.Errorf("next_step = %q, want %q", body.NextStep, "backup_set")
	}
	if body.Steps["backup_set"] != "pending" {
		t.Errorf("steps.backup_set = %q, want %q\nbody: %s", body.Steps["backup_set"], "pending", rec.Body.String())
	}
	if _, ok := body.Steps["email"]; !ok {
		t.Errorf("steps carries no email entry; the email step has to be reportable as pending, skipped or done\nbody: %s", rec.Body.String())
	}
}
