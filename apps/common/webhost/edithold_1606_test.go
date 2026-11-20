package webhost

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1606 over the real router and the real service: a renewal that
// lands after the release is refused with 409 EDIT_HOLD_GONE and leaves
// the set unheld, and two holders of the same set are two leases. The fake
// backends elsewhere in this package cannot prove any of this, because the
// bug lived in the registry behind them.

const editHold1606Path = "/api/v1/backup-sets/production/alpha/edit-hold"

// editHold1606Router serves one configured backup set from a real
// service.Open, the way retnd-web does.
func editHold1606Router(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "poll_interval: 15m\n" +
		"state:\n  database: " + filepath.Join(dir, "state.db") + "\n" +
		"sources:\n  - id: production\n    backup_sets:\n" +
		"      - id: alpha\n" +
		"        remote:\n          type: local\n" +
		"        remote_path: " + remote + "\n" +
		"        local_path: " + filepath.Join(dir, "local") + "\n" +
		"        include:\n          - \"*.dump\"\n" +
		"        completion:\n          strategy: rename\n" +
		"        stale_after: 24h\n" +
		"retention:\n  timezone: UTC\n  week_starts_on: monday\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, cleanup, err := service.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	return opsRouter(svc, NotYetImplementedGate{})
}

// take1606 takes a hold with no token and returns the token it answered.
func take1606(t *testing.T, router http.Handler) string {
	t.Helper()
	rec := send(router, http.MethodPost, editHold1606Path, "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("take = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode take: %v", err)
	}
	if body.Token == "" {
		t.Fatalf("the take answered no holder token: %s", rec.Body.String())
	}
	return body.Token
}

func tokenBody(token string) string {
	b, _ := json.Marshal(map[string]string{"token": token})
	return string(b)
}

func held1606(t *testing.T, router http.Handler) bool {
	t.Helper()
	rec := send(router, http.MethodGet, editHold1606Path, "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET edit-hold = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Held bool `json:"held"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return body.Held
}

func TestEditHold1606_ARenewalAfterTheReleaseIsRefusedWithEditHoldGone(t *testing.T) {
	router := editHold1606Router(t)
	token := take1606(t, router)

	if rec := send(router, http.MethodPost, editHold1606Path+"/release", tokenBody(token), true); rec.Code != http.StatusNoContent {
		t.Fatalf("release = %d %s", rec.Code, rec.Body.String())
	}

	rec := send(router, http.MethodPost, editHold1606Path, tokenBody(token), true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a renewal after the release = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var refusal struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if refusal.Error.Code != "EDIT_HOLD_GONE" {
		t.Errorf("refusal code = %q, want EDIT_HOLD_GONE: %s", refusal.Error.Code, rec.Body.String())
	}
	if held1606(t, router) {
		t.Error("the set is held after the late renewal was refused, so the renewal put the hold back")
	}
}

func TestEditHold1606_ARenewalWithTheLiveTokenKeepsTheSameToken(t *testing.T) {
	router := editHold1606Router(t)
	token := take1606(t, router)

	rec := send(router, http.MethodPost, editHold1606Path, tokenBody(token), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("renewal = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Token   string          `json:"token"`
		Stopped json.RawMessage `json:"stopped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode renewal: %v", err)
	}
	if body.Token != token {
		t.Errorf("the renewal answered token %q, want %q", body.Token, token)
	}
	if string(body.Stopped) != "null" {
		t.Errorf("a renewal of a live lease answered stopped = %s, want null", body.Stopped)
	}
}

func TestEditHold1606_ReleasingOneTabsTokenLeavesTheOtherTabsHold(t *testing.T) {
	router := editHold1606Router(t)
	first, second := take1606(t, router), take1606(t, router)
	if first == second {
		t.Fatalf("two takes answered the same token %q", first)
	}

	if rec := send(router, http.MethodPost, editHold1606Path+"/release", tokenBody(first), true); rec.Code != http.StatusNoContent {
		t.Fatalf("release first = %d %s", rec.Code, rec.Body.String())
	}
	if !held1606(t, router) {
		t.Fatal("releasing the first tab's hold released the second tab's as well")
	}
	if rec := send(router, http.MethodPost, editHold1606Path, tokenBody(second), true); rec.Code != http.StatusOK {
		t.Errorf("the second tab's renewal after the first left = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// The backwards-compatible path, said out loud: a release with no body
// gives back every hold on the set, the way it did before tokens.
func TestEditHold1606_AReleaseWithNoBodyGivesBackEveryHold(t *testing.T) {
	router := editHold1606Router(t)
	first, second := take1606(t, router), take1606(t, router)

	if rec := send(router, http.MethodPost, editHold1606Path+"/release", "", true); rec.Code != http.StatusNoContent {
		t.Fatalf("release with no body = %d %s", rec.Code, rec.Body.String())
	}
	if held1606(t, router) {
		t.Fatal("the set is still held after a release with no token")
	}
	for _, token := range []string{first, second} {
		if rec := send(router, http.MethodPost, editHold1606Path, tokenBody(token), true); rec.Code != http.StatusConflict {
			t.Errorf("a renewal after a release of every hold = %d %s, want 409", rec.Code, rec.Body.String())
		}
	}
}

// A body that is not JSON is refused rather than read as "no token": a
// renewal that was quietly turned into a fresh take would be the bug
// again, by another road.
func TestEditHold1606_ABodyThatIsNotJSONIsRefused(t *testing.T) {
	router := editHold1606Router(t)
	for _, path := range []string{editHold1606Path, editHold1606Path + "/release"} {
		rec := send(router, http.MethodPost, path, "token=abc", true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s with a body that is not JSON = %d %s, want 400", path, rec.Code, rec.Body.String())
		}
	}
	if held1606(t, router) {
		t.Error("a refused take still held the set")
	}
}

// A token key that is present but empty is a client that meant to name a
// holder and had an unset variable or a lost value. Reading it as "no
// token" would turn its renewal into a fresh take, and its release into
// one that gives back every holder's hold.
func TestEditHold1606_AnEmptyTokenIsRefusedNotReadAsNoToken(t *testing.T) {
	router := editHold1606Router(t)
	rec := send(router, http.MethodPost, editHold1606Path, `{"token":""}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("take with an empty token = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if held1606(t, router) {
		t.Error("a take with an empty token still held the set")
	}

	other := take1606(t, router)
	rec = send(router, http.MethodPost, editHold1606Path+"/release", `{"token":""}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("release with an empty token = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if !held1606(t, router) {
		t.Error("a release with an empty token gave back another holder's hold")
	}
	if rec := send(router, http.MethodPost, editHold1606Path, tokenBody(other), true); rec.Code != http.StatusOK {
		t.Errorf("the other holder's renewal = %d, want 200", rec.Code)
	}
}
