package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/app"
	"github.com/retnd/retnd/core/internal/state"
)

// Issue #1528's review: a retired backup in the backups list carries its
// record (who, the source check, what replaced it), so the Backups table can
// say "Retired, replaced by X" and the browser can notice a retirement the
// engine made on its own, without one preview request per row.
func TestAListedRetiredBackupCarriesItsRecord_1528(t *testing.T) {
	svc, _, _, _ := openRemovalFixtureService(t)
	ctx := context.Background()
	all, err := svc.ListArtifacts(ctx, ArtifactFilter{})
	if err != nil || len(all) == 0 {
		svc.state.Load().inner.RunCycle(ctx)
		all, err = svc.ListArtifacts(ctx, ArtifactFilter{})
	}
	if err != nil || len(all) == 0 {
		t.Fatalf("fixture: ListArtifacts = %d rows, err %v", len(all), err)
	}
	setID := all[0].BackupSetID
	id, err := app.ParseArtifactID(setID + "/old-retired.dump")
	if err != nil {
		t.Fatalf("ParseArtifactID: %v", err)
	}
	j := svc.state.Load().inner.Journal
	at := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)
	for _, tr := range []state.Transition{
		{Artifact: id, Key: "seed-d", From: "", To: "DISCOVERED", OccurredAt: at, RemotePath: "old-retired.dump"},
		{Artifact: id, Key: "seed-f", From: "DISCOVERED", To: "FAILED", OccurredAt: at, Detail: "copy_to_local: not_found: object not found"},
		{Artifact: id, Key: "seed-r", From: "FAILED", To: "RETIRED", OccurredAt: at,
			Retirement: &state.RetirementUpdate{ActorKind: state.RetiredAutomatically, Actor: "engine", SourceCheck: state.SourceAbsent, SupersededBy: all[0].Name}},
	} {
		if _, err := j.RecordTransition(ctx, tr); err != nil {
			t.Fatalf("seeding %s: %v", tr.To, err)
		}
	}
	all, err = svc.ListArtifacts(ctx, ArtifactFilter{})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	for _, a := range all {
		if a.ID != id.String() {
			continue
		}
		v := reflect.ValueOf(a).FieldByName("Retirement")
		if !v.IsValid() || v.IsNil() {
			t.Fatalf("the listed RETIRED backup carries no retirement record: %+v", a)
		}
		r := v.Elem()
		if r.FieldByName("ActorKind").String() != state.RetiredAutomatically || r.FieldByName("SupersededBy").String() != all[0].Name {
			t.Errorf("the record reads %+v", r.Interface())
		}
		return
	}
	t.Fatalf("%s is not in the list", id)
}
