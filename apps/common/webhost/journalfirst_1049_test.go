package webhost

import (
	"context"
	"net/http"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Panel finding C7 on retnd#1050: settings answered first_run=true for a
// deployment that had run backups and then lost its sets, so the UI greeted
// it with the first-run guide.

type journalBackend struct {
	*settingsFakeBackend
	artifacts int64
}

func (b *journalBackend) JournalArtifactCount(context.Context) (int64, error) {
	return b.artifacts, nil
}

func firstRunFlag(t *testing.T, artifacts int64) bool {
	t.Helper()
	inner := newSettingsFakeBackend()
	inner.settings = service.DefaultSettings()
	router := NewRouter(RouterConfig{
		Platform:      allowingPlatform("alice"),
		Backend:       &journalBackend{settingsFakeBackend: inner, artifacts: artifacts},
		Gate:          NotYetImplementedGate{},
		BinaryVersion: "test",
		Commit:        "test",
	})
	var body struct {
		FirstRun bool `json:"first_run"`
	}
	rec := getJSONInto(t, router, "/api/v1/settings", &body)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings = %d: %s", rec.Code, rec.Body.String())
	}
	return body.FirstRun
}

func TestSettingsFirstRunIsFalseWhenTheJournalHoldsBackups(t *testing.T) {
	if firstRunFlag(t, 3) {
		t.Errorf("first_run is true although the journal holds 3 artifacts")
	}
}

func TestSettingsFirstRunStaysTrueOverAnEmptyJournal(t *testing.T) {
	if !firstRunFlag(t, 0) {
		t.Errorf("first_run is false for defaults over an empty journal")
	}
}
