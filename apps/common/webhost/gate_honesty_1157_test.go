package webhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1157: the destructive gate is shut in every build this repository
// ships, and the words it refuses with and the documents that describe the
// routes it guards have to say so.
//
// I checked the behaviour against a built retnd-web before writing any of
// this (the transcript is in the pull request): POST /operations, with every
// action, and POST .../retention/apply answer 403
// DESTRUCTIVE_OPERATIONS_DISABLED, and POST /backup-sets with run_immediately
// answers 201 with the set persisted and the refusal in run_error. The
// panel's open question, 403 or 201, is settled by that run: 201.
//
// So the status and the code stay exactly as they are. What changes is the
// sentence, which used to say "until the trusted-proxy authentication gate
// has been verified for this deployment" and so sounded like something an
// operator could do something about. Nothing in this release lets them: it
// is refused over HTTP in every build until #92 ships a verified gate, and
// the terminal is the way.

// shutGateRefusalWords are the three things the refusal has to say, as
// fragments so the test does not pin punctuation: that it is this release,
// that it is #92 which lifts it, and where to go instead.
var shutGateRefusalWords = []string{
	"refused over HTTP in this release",
	"#92",
	"run the equivalent `retnd` command on the host",
}

func shutGateRouter(t *testing.T) http.Handler {
	t.Helper()
	return NewRouter(RouterConfig{
		Platform:      allowingPlatform("alice"),
		Backend:       newBackupSetFakeBackend(),
		Gate:          NotYetImplementedGate{},
		BinaryVersion: "test",
		Commit:        "test",
	})
}

func postJSON(t *testing.T, router http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertSaysShutGateWords(t *testing.T, label, got string) {
	t.Helper()
	for _, want := range shutGateRefusalWords {
		if !strings.Contains(got, want) {
			t.Errorf("%s: the refusal does not say %q\n  got: %s", label, want, got)
		}
	}
}

func TestGateRefusal1157_EveryGatedRouteSaysThisReleaseRefusesAndWhereToGo(t *testing.T) {
	router := shutGateRouter(t)
	for _, tc := range []struct{ name, path, body string }{
		{"run_cycle", "/api/v1/operations", `{"action":"run_cycle","config_revision":"rev-1"}`},
		{"restore_snapshot", "/api/v1/operations", `{"action":"restore_snapshot","config_revision":"rev-1","snapshot_restore":{"backup_set_id":"a/b","target_path":"/t"}}`},
		{"retention apply", "/api/v1/backup-sets/a/b/retention/apply", `{"plan_id":"p"}`},
	} {
		rec := postJSON(t, router, tc.path, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403 (the status does not change)", tc.name, rec.Code)
		}
		var env struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if env.Error.Code != "DESTRUCTIVE_OPERATIONS_DISABLED" {
			t.Errorf("%s: code = %q, want DESTRUCTIVE_OPERATIONS_DISABLED (the code does not change)", tc.name, env.Error.Code)
		}
		assertSaysShutGateWords(t, tc.name, env.Error.Message)
	}
}

// run_immediately is the odd one out: the set is persisted, the response is
// a 201, and the refusal travels in run_error. The sentence is the same one,
// because an operator who saw it worded two ways would think they were two
// problems.
func TestGateRefusal1157_RunImmediatelyCarriesTheSameWordsInRunError(t *testing.T) {
	router := shutGateRouter(t)
	body := strings.TrimSuffix(strings.TrimSpace(validCreateBody), "}") + `,"run_immediately":true}`
	rec := postJSON(t, router, "/api/v1/backup-sets", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (the set is persisted and only the run is refused): %s", rec.Code, rec.Body.String())
	}
	var got struct {
		RunError string `json:"run_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertSaysShutGateWords(t, "run_error", got.RunError)
}

// The contract is what a client author reads. Each guarded operation has to
// say who it is shut for (a gateway profile, until #92), who it is open for,
// and where the terminal does the same work. #1163 reworded this: it used to
// say every build of the release refuses, and that stopped being true the
// day the gate opened for local-account profiles.
func TestGateRefusal1157_TheContractSaysWhoTheGateIsShutFor(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "v1", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Description string   `json:"description"`
			Gate        bool     `json:"x-destructive-gate"`
			TargetKinds []string `json:"x-destructive-gate-target-kinds"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	gated := 0
	for path, methods := range doc.Paths {
		for method, op := range methods {
			if !op.Gate && len(op.TargetKinds) == 0 {
				continue
			}
			gated++
			label := strings.ToUpper(method) + " " + path
			for _, want := range []string{"gateway profile", "local-account", "#92", "retnd"} {
				if !strings.Contains(op.Description, want) {
					t.Errorf("%s is destructive-gated but its description does not say %q\n  got: %q", label, want, op.Description)
				}
			}
		}
	}
	// POST /operations, retention/apply and #1193's write check are gated
	// outright; the mint, POST /recoveries and the redeem are gated for the
	// source-host target kind, and say so the same way.
	// #1203 adds the custom validator upload and its edit: both put code
	// where the engine runs it on the host.
	if gated != 8 {
		t.Errorf("found %d gated operations, want 8 (POST /operations, retention/apply, the source-host write check, the three recovery routes gated per target kind, and the custom validator upload and edit): the list this test guards moved", gated)
	}

	create := string(doc.Components.Schemas["CreateBackupSetRequest"])
	i := strings.Index(create, `"run_immediately"`)
	if i < 0 {
		t.Fatal("CreateBackupSetRequest has no run_immediately")
	}
	field := create[i:]
	if j := strings.Index(field, `"acknowledge_repoint"`); j > 0 {
		field = field[:j]
	}
	for _, want := range []string{"gateway profile", "#92", "run_error"} {
		if !strings.Contains(field, want) {
			t.Errorf("run_immediately does not say %q\n  got: %s", want, field)
		}
	}
}

// The reference, the web UI page and the no-terminal guide are where an
// operator reads about this before they meet the refusal.
func TestGateRefusal1157_TheDocsSayWhatIsTerminalOnlyInThisRelease(t *testing.T) {
	read := func(parts ...string) string {
		b, err := os.ReadFile(filepath.Join(append([]string{"..", "..", ".."}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	reference := read("docs", "site", "reference.html")
	i := strings.Index(reference, "What the web interface can run, by profile")
	if i < 0 {
		t.Fatal(`docs/site/reference.html has no "What the web interface can run, by profile" section (it was "Terminal-only in this release" until #1163)`)
	}
	section := reference[i:min(len(reference), i+9000)]
	for _, want := range []string{
		"retnd snapshot restore", "retnd snapshot verify", "retnd snapshot hold", "retnd snapshot unhold",
		"retnd retention apply", "retnd restore", "retnd run", "retnd fetch", "#92",
	} {
		if !strings.Contains(section, want) {
			t.Errorf(`the "Terminal-only in this release" section does not name %q`, want)
		}
	}
	// `run` and `fetch` open the deployment in the operator's own process,
	// and the runtime check showed they refuse while the web service is
	// serving it. A section that sent people to them without saying so would
	// replace one dead end with another.
	if !strings.Contains(section, "stop the web service") {
		t.Error(`the section does not say run and fetch need the web service stopped first`)
	}
	for _, f := range [][]string{{"docs", "site", "web-ui.html"}, {"docs", "recovery-without-a-terminal.md"}} {
		text := read(f...)
		if !strings.Contains(text, "gateway profile") || !strings.Contains(text, "terminal") {
			t.Errorf("%s does not say which operations are terminal-only on a gateway profile", filepath.Join(f...))
		}
	}
	if !strings.Contains(read("CHANGELOG.md"), "#1157") {
		t.Error("CHANGELOG.md has no entry for #1157")
	}
}
