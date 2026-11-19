package webhost

import (
	"encoding/json"
	"net/http"
	"testing"
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
	for _, k := range []string{"source_check", "source_check_sentence", "proposed_successor", "refusal", "effects", "command"} {
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
