package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/revalidate"
	"github.com/retnd/retnd/core/internal/state"
)

// Every field of ScrubSummary reaches the wire, and a summary built with
// every field non-zero comes out with every wire field non-zero. A fixture
// that leaves a field at zero would pass without the field ever being mapped,
// so the fixture is itself checked.
func TestScrubSummaryWire_CarriesEveryField(t *testing.T) {
	set, err := model.ParseBackupSetID("production/postgres-primary")
	if err != nil {
		t.Fatal(err)
	}
	art, err := model.NewArtifactID(set, "a.dump")
	if err != nil {
		t.Fatal(err)
	}
	sum := ScrubSummary{
		Trigger: state.ScrubByManual, Scope: "all due",
		Selected: 1, Checked: 2, Passed: 3, Failed: 4, CouldNotComplete: 5, RequiresRestore: 6,
		SkippedHeld: 7, SkippedLeased: 8, Refused: 9, BytesRead: 10, PassID: 11,
		Sets: []ScrubSetReport{{
			Set: set, Err: errors.New("stopped"),
			Report: revalidate.Report{
				Findings: []revalidate.Finding{{Artifact: art, Copies: []revalidate.CopyResult{
					{Medium: "local", Verdict: state.ScrubFailed, Class: placement.Content, Reason: "rotted", Refused: true},
				}}},
				Errors:  []revalidate.ArtifactError{{Artifact: art, Err: errors.New("no answer")}},
				Skipped: []revalidate.Skip{{Artifact: art, Medium: "m", Reason: revalidate.SkipLeased}},
			},
		}},
	}
	rv := reflect.ValueOf(sum)
	for i := 0; i < rv.NumField(); i++ {
		if rv.Field(i).IsZero() {
			t.Fatalf("the fixture leaves ScrubSummary.%s zero, so the mapping of it is unchecked", rv.Type().Field(i).Name)
		}
	}

	w := sum.Wire()
	wv := reflect.ValueOf(w)
	for i := 0; i < wv.NumField(); i++ {
		if wv.Field(i).IsZero() {
			t.Errorf("ScrubResult.%s is zero: the mapping drops what ScrubSummary carries", wv.Type().Field(i).Name)
		}
	}
	set0 := reflect.ValueOf(w.Sets[0])
	for i := 0; i < set0.NumField(); i++ {
		if set0.Field(i).IsZero() {
			t.Errorf("ScrubSetResult.%s is zero", set0.Type().Field(i).Name)
		}
	}
	c := reflect.ValueOf(w.Sets[0].Copies[0])
	for i := 0; i < c.NumField(); i++ {
		if c.Field(i).IsZero() {
			t.Errorf("ScrubCopyOutcome.%s is zero", c.Type().Field(i).Name)
		}
	}
}

func TestScrubSummaryWire_AnEmptyPassHasEmptyListsNotNulls(t *testing.T) {
	w := ScrubSummary{Trigger: state.ScrubBySchedule, Scope: "all due"}.Wire()
	if w.Sets == nil {
		t.Error("Sets is nil, which marshals as null")
	}
}
