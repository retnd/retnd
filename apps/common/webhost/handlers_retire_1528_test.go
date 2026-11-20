package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1528, the API half. A FAILED backup whose source is gone needs a way
// out an operator (and, with a person's confirmation, an agent) can take, and
// the backups list needs a state filter and a cursor so an agent's view of it
// can always be narrowed below the result cap. The shapes are in
// docs/design/1528-retire-failed-artifacts.md.

func TestTheRetireRouteRetiresAndAnswersWithTheRecord_1528(t *testing.T) {
	rt := newReadSurfaceRouter(t)

	rec := rt.post(t, "/api/v1/backups/production/postgres/bad.dump/retire", `{"note":"the host deleted it"}`)
	mustStatus(t, rec, http.StatusOK)

	var got map[string]any
	decodeInto(t, rec, &got)
	if got["state"] != "RETIRED" {
		t.Errorf("state = %v, want RETIRED; body: %s", got["state"], rec.Body.String())
	}
	if _, ok := got["retirement"]; !ok {
		t.Errorf("the answer carries no retirement record; body: %s", rec.Body.String())
	}
}

func TestThePreviewSaysWhetherTheSourceIsStillThere_1528(t *testing.T) {
	rt := newReadSurfaceRouter(t)

	rec := rt.get(t, "/api/v1/backups/production/postgres/bad.dump/retire")
	mustStatus(t, rec, http.StatusOK)

	var got map[string]json.RawMessage
	decodeInto(t, rec, &got)
	for _, k := range []string{"state", "source_check", "source_check_sentence", "effects", "command"} {
		if _, ok := got[k]; !ok {
			t.Errorf("the preview has no %q, which the retire dialog needs from the engine; body: %s", k, rec.Body.String())
		}
	}
}

func TestTheBackupsListAlwaysCarriesACursor_1528(t *testing.T) {
	rt := newReadSurfaceRouter(t)

	rec := rt.get(t, "/api/v1/backups?limit=1")
	mustStatus(t, rec, http.StatusOK)

	var got map[string]json.RawMessage
	decodeInto(t, rec, &got)
	if _, ok := got["next_cursor"]; !ok {
		t.Errorf("GET /backups has no next_cursor, so an agent over the result cap has nothing to page with; body: %s", rec.Body.String())
	}
}

func TestTheBackupsListRefusesAStateItDoesNotKnow_1528(t *testing.T) {
	rt := newReadSurfaceRouter(t)

	rec := rt.get(t, "/api/v1/backups?state=NOT_A_STATE")
	mustStatus(t, rec, http.StatusBadRequest)
	if got := responseErrorCode(rec.Body.String()); got != "INVALID_REQUEST" {
		t.Errorf("error code = %q, want INVALID_REQUEST", got)
	}
}

// An agent may propose only the retirement the engine itself would make. The
// two acknowledgements are refused before a person is asked; the same
// proposal without them is put in front of a person as usual.
func TestAnAgentCannotSendARetireAcknowledgement_1528(t *testing.T) {
	const path = "/api/v1/backups/src/set-1/dump.tar/retire"
	for _, body := range []string{`{"accept_source_present":true}`, `{"no_successor":true}`, `{"no_successor":true,"accept_source_present":true}`} {
		rig := retireRig(t)
		rec := serve(rig.router, toolCall(http.MethodPost, path, body, operateToken, "retire_backup"))
		if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_RETIRE_ACK_REFUSED" {
			t.Errorf("%s = %d %s, want 403 AGENT_RETIRE_ACK_REFUSED", body, rec.Code, rec.Body.String())
		}
		if n := len(rig.backend.created); n != 0 {
			t.Errorf("%s created %d confirmations; a refused proposal must ask nobody", body, n)
		}
	}

	// Negative control: the conservative proposal reaches a person.
	for _, body := range []string{`{}`, `{"note":"the host deleted it","accept_source_present":false}`} {
		rig := retireRig(t)
		rec := serve(rig.router, toolCall(http.MethodPost, path, body, operateToken, "retire_backup"))
		if rec.Code != http.StatusConflict || responseErrorCode(rec.Body.String()) != "AGENT_CONFIRMATION_REQUIRED" {
			t.Errorf("%s = %d %s, want 409 AGENT_CONFIRMATION_REQUIRED", body, rec.Code, rec.Body.String())
		}
	}
}

// The retire answer records an operator as the actor, never an agent, when no
// agent replay is in play, and passes every field of the body through.
func TestTheRetireRouteCarriesTheBodyToTheEngine_1528(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	rec := rt.post(t, "/api/v1/backups/production/postgres/bad.dump/retire",
		`{"superseded_by":"new.dump","no_successor":false,"accept_source_present":true,"note":"junk"}`)
	mustStatus(t, rec, http.StatusOK)
	got := rt.backend.lastRetireRequest
	if got.SupersededBy != "new.dump" || !got.AcceptSourcePresent || got.NoSuccessor || got.Note != "junk" || got.ActorKind != "operator" {
		t.Errorf("the engine was asked %+v", got)
	}
}

// Each refusal the engine names reaches the client under its own code.
func TestTheRetireRouteAnswersEveryRefusalByName_1528(t *testing.T) {
	for _, code := range []string{"RETIRE_SOURCE_PRESENT", "RETIRE_SOURCE_UNCONFIRMED", "RETIRE_NO_SUCCESSOR", "RETIRE_SUCCESSOR_INVALID", "RETIRE_OWN_COPY_AT_FINAL_NAME"} {
		rt := newReadSurfaceRouter(t)
		rt.backend.errOnRetire = &service.RetireRefusal{Code: code, Sentence: "the engine's sentence"}
		rec := rt.post(t, "/api/v1/backups/production/postgres/bad.dump/retire", "")
		mustStatus(t, rec, http.StatusConflict)
		if got := responseErrorCode(rec.Body.String()); got != code {
			t.Errorf("error code = %q, want %q", got, code)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrArtifactNotFailed, http.StatusConflict, "ARTIFACT_NOT_FAILED"},
		{service.ErrArtifactNotFound, http.StatusNotFound, "ARTIFACT_NOT_FOUND"},
		{service.ErrBackupSetNotFound, http.StatusNotFound, "BACKUP_SET_NOT_FOUND"},
	} {
		rt := newReadSurfaceRouter(t)
		rt.backend.errOnRetire = tc.err
		rec := rt.post(t, "/api/v1/backups/production/postgres/bad.dump/retire", "")
		mustStatus(t, rec, tc.status)
		if got := responseErrorCode(rec.Body.String()); got != tc.code {
			t.Errorf("error code = %q, want %q", got, tc.code)
		}
	}
}

// retireRig is newConfirmationsRig over the real catalogue, so the proposal
// is resolved by core/agenttools' own retire_backup row and its
// AgentMayNotSet, not by a test's stand-in.
func retireRig(t *testing.T) *confirmationsRig {
	t.Helper()
	b := &confirmationsBackend{
		backupSetFakeBackend: newBackupSetFakeBackend(),
		fakeAgentAccess:      newFakeAgentAccess(),
		fakeConfirmations:    newFakeConfirmations(),
	}
	router := NewRouter(RouterConfig{
		Platform: allowingPlatform("alice"), Backend: b, Gate: alwaysPassGate{}, StepUp: &stubStepUp{user: "alice", password: stepUpPassword},
		Recorder: &recordingBackend{}, AgentTools: catalogueTools{}, BinaryVersion: "test", Commit: "test",
	})
	return &confirmationsRig{router: router, backend: b}
}

// The state filter narrows, and a limit walks the whole list in pages through
// next_cursor with nothing repeated and nothing missed, ending on null.
func TestTheBackupsListFiltersByStateAndPagesWithACursor_1528(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	var all []service.Artifact
	for i, st := range []string{"FAILED", "COMPLETE", "FAILED", "RETIRED", "FAILED", "COMPLETE", "FAILED"} {
		a := testArtifactFixture
		a.Name = string(rune('a'+i)) + ".dump"
		a.ID = "production/postgres/" + a.Name
		a.State = st
		all = append(all, a)
	}
	rt.backend.artifacts = all

	type page struct {
		Artifacts []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"artifacts"`
		NextCursor *string `json:"next_cursor"`
	}
	seen := map[string]bool{}
	before := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("the cursor never reached the end")
		}
		target := "/api/v1/backups?state=FAILED&limit=2"
		if before != "" {
			target += "&before=" + before
		}
		rec := rt.get(t, target)
		mustStatus(t, rec, http.StatusOK)
		var p page
		decodeInto(t, rec, &p)
		for _, a := range p.Artifacts {
			if a.State != "FAILED" {
				t.Errorf("the state filter let %s (%s) through", a.ID, a.State)
			}
			if seen[a.ID] {
				t.Errorf("%s came back on two pages", a.ID)
			}
			seen[a.ID] = true
		}
		if p.NextCursor == nil {
			break
		}
		before = *p.NextCursor
	}
	if len(seen) != 4 {
		t.Errorf("paging saw %d FAILED backups, want 4: %v", len(seen), seen)
	}

	// A comma list, and a cursor nobody issued.
	rec := rt.get(t, "/api/v1/backups?state=FAILED,RETIRED")
	mustStatus(t, rec, http.StatusOK)
	var p page
	decodeInto(t, rec, &p)
	if len(p.Artifacts) != 5 || p.NextCursor != nil {
		t.Errorf("FAILED,RETIRED without a limit: %d rows, cursor %v; want 5 and null", len(p.Artifacts), p.NextCursor)
	}
	rec = rt.get(t, "/api/v1/backups?limit=2&before=!!not-base64!!")
	mustStatus(t, rec, http.StatusBadRequest)
}

// JSON field names are matched without regard to case by every decoder
// downstream, so the proposer's refusal has to be too: {"No_Successor":true}
// must not reach a person (the #1536 backend review's S3).
func TestAnAgentCannotSendAnAcknowledgementInAnotherCase_1528(t *testing.T) {
	for _, body := range []string{`{"No_Successor":true}`, `{"ACCEPT_SOURCE_PRESENT":true}`} {
		rig := retireRig(t)
		rec := serve(rig.router, toolCall(http.MethodPost, "/api/v1/backups/src/set-1/dump.tar/retire", body, operateToken, "retire_backup"))
		if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_RETIRE_ACK_REFUSED" {
			t.Errorf("%s = %d %s, want 403 AGENT_RETIRE_ACK_REFUSED", body, rec.Code, rec.Body.String())
		}
	}
}

// The replay of an approved agent request is refused an acknowledgement too,
// whatever the proposer let through: the second guard, on its own.
func TestAReplayedAgentRetirementCannotCarryAnAcknowledgement_1528(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backups/production/postgres/bad.dump/retire", strings.NewReader(`{"no_successor":true}`))
	req.Header.Set("Content-Type", "application/json")
	attachValidCSRF(req)
	req = req.WithContext(context.WithValue(req.Context(), replayContextKey{}, service.AgentPrincipal{Name: "triage"}))
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || responseErrorCode(rec.Body.String()) != "AGENT_RETIRE_ACK_REFUSED" {
		t.Fatalf("replayed acknowledgement = %d %s, want 403 AGENT_RETIRE_ACK_REFUSED", rec.Code, rec.Body.String())
	}
	if rt.backend.lastRetired != "" {
		t.Errorf("the engine was asked to retire %q", rt.backend.lastRetired)
	}
}

// A limit that is not a number is refused like a state that is not one,
// rather than answered with the whole list, which is the answer that
// overflowed the agent's cap in the first place (the #1536 backend review's S9).
func TestTheBackupsListRefusesALimitThatIsNotANumber_1528(t *testing.T) {
	rt := newReadSurfaceRouter(t)
	rec := rt.get(t, "/api/v1/backups?limit=fifty")
	mustStatus(t, rec, http.StatusBadRequest)
}
