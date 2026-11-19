package webhost

import (
	"encoding/json"
	"net/http"
	"testing"
)

// retnd#1049: an instance that could not write its default configuration
// because the directory is read-only has to say which directory, in the
// first-run answer the guide reads. The sentence comes from the first-run
// surface, so this package only carries it.

type unwritableFirstRun struct {
	fakeFirstRun
	dir, message string
}

func (u *unwritableFirstRun) ConfigDirProblem() (string, string) { return u.dir, u.message }

func TestFirstRunStatus_NamesAnUnwritableConfigDirectory(t *testing.T) {
	fr := &unwritableFirstRun{
		dir:     "/etc/retnd/config",
		message: "The configuration directory /etc/retnd/config is not writable, so retnd cannot write its default configuration there. Mount it writable, then restart retnd.",
	}
	router := NewRouter(RouterConfig{
		Platform:      allowingPlatform("alice"),
		FirstRun:      fr,
		BinaryVersion: "test",
		Commit:        "test",
	})
	var body map[string]any
	rec := getJSONInto(t, router, "/api/v1/system/first-run", &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/system/first-run = %d\n%s", rec.Code, rec.Body.String())
	}
	if body["config_dir"] != fr.dir {
		t.Errorf("config_dir = %v, want %q\n%s", body["config_dir"], fr.dir, rec.Body.String())
	}
	if body["config_dir_problem"] != fr.message {
		t.Errorf("config_dir_problem = %v, want the first-run surface's sentence\n%s", body["config_dir_problem"], rec.Body.String())
	}
}

func TestFirstRunStatus_SaysNothingAboutTheDirectoryWhenItIsFine(t *testing.T) {
	router := unconfiguredRouter(&fakeFirstRun{})
	var body map[string]json.RawMessage
	rec := getJSONInto(t, router, "/api/v1/system/first-run", &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/system/first-run = %d", rec.Code)
	}
	for _, key := range []string{"config_dir", "config_dir_problem"} {
		if _, present := body[key]; present {
			t.Errorf("%s is present with no problem to report: %s", key, rec.Body.String())
		}
	}
}
