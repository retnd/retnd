package placement_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/placement"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC N, #992 (N1.11, NV.17): retiring a key is refused while an ACTIVE
// placement still records it, and the refusal names the copies. The journal
// side (two methods on state.Journal, listed in the PR) is stood in for by an
// in-memory double here, so these cells exercise RetireKey's own logic; the
// SQL is the journal owner's.

type fakeKeyJournal struct {
	keys    map[string]state.EncryptionKey
	copies  []fakeCopy
	retired []string

	// raceCopy, when set, appears between RetireKey's check and its write:
	// the write then fails the way the journal's in-transaction re-check does.
	raceCopy *fakeCopy
}

type fakeCopy struct {
	ref    model.CopyRef
	key    string
	status string
}

func (j *fakeKeyJournal) EncryptionKey(_ context.Context, id string) (state.EncryptionKey, error) {
	k, ok := j.keys[id]
	if !ok {
		return state.EncryptionKey{}, fmt.Errorf("%w: %s", state.ErrEncryptionKeyNotFound, id)
	}
	return k, nil
}

func (j *fakeKeyJournal) ActivePlacementsRecordingKey(_ context.Context, id string) ([]model.CopyRef, error) {
	var out []model.CopyRef
	for _, c := range j.copies {
		if c.key == id && c.status == state.PlacementActive {
			out = append(out, c.ref)
		}
	}
	return out, nil
}

func (j *fakeKeyJournal) RetireEncryptionKey(_ context.Context, id string, at time.Time) error {
	if j.raceCopy != nil {
		j.copies = append(j.copies, *j.raceCopy)
		j.raceCopy = nil
		return errors.New("state: the key is still recorded by an ACTIVE placement")
	}
	k := j.keys[id]
	k.Status, k.RetiredAt = state.EncryptionKeyRetired, &at
	j.keys[id] = k
	j.retired = append(j.retired, id)
	return nil
}

func ref(name, medium string) model.CopyRef {
	set := model.BackupSetID{Source: "production", Set: "pg"}
	return model.CopyRef{Artifact: model.ArtifactID{Set: set, Name: name}, Medium: medium}
}

func newKeyJournal() *fakeKeyJournal {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &fakeKeyJournal{
		keys: map[string]state.EncryptionKey{
			"kold":    {ID: "kold", Provider: state.KeyProviderManaged, Status: state.EncryptionKeySuperseded, CreatedAt: t0},
			"knew":    {ID: "knew", Provider: state.KeyProviderManaged, Status: state.EncryptionKeyActive, CreatedAt: t0},
			"kretire": {ID: "kretire", Provider: state.KeyProviderManaged, Status: state.EncryptionKeyRetired, CreatedAt: t0, RetiredAt: &t0},
		},
		copies: []fakeCopy{
			{ref("a.dump", "offsite"), "kold", state.PlacementActive},
			{ref("b.dump", "offsite"), "kold", state.PlacementActive},
			{ref("c.dump", "annual"), "kold", state.PlacementGone},
			{ref("d.dump", "offsite"), "knew", state.PlacementActive},
		},
	}
}

var retireAt = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// retireFn is what the assertions below run against, so the self-test can
// run the very same assertions against a mutant.
type retireFn func(ctx context.Context, j *fakeKeyJournal, id string) error

func realRetire(ctx context.Context, j *fakeKeyJournal, id string) error {
	return placement.RetireKey(ctx, j, nil, id, retireAt)
}

// refusalProblems is the whole claim of the refusal as a list of what is
// wrong with fn: empty means fn refuses a key two ACTIVE copies record, names
// both copies and not the GONE one nor another key's, and retires nothing.
func refusalProblems(fn retireFn) []string {
	var out []string
	j := newKeyJournal()
	err := fn(context.Background(), j, "kold")
	if err == nil {
		out = append(out, "retiring a key two ACTIVE copies record was permitted")
	}
	if len(j.retired) != 0 {
		out = append(out, "the key was retired anyway")
	}
	if err != nil {
		msg := err.Error()
		for _, want := range []string{"production/pg/a.dump", "production/pg/b.dump", "offsite"} {
			if !strings.Contains(msg, want) {
				out = append(out, "the refusal does not name "+want+": "+msg)
			}
		}
		for _, notWant := range []string{"c.dump", "d.dump"} {
			if strings.Contains(msg, notWant) {
				out = append(out, "the refusal names "+notWant+", which does not block this key")
			}
		}
		if !errors.Is(err, placement.ErrKeyInUse) {
			out = append(out, "the refusal is not ErrKeyInUse")
		}
		var inUse *placement.KeyInUseError
		if !errors.As(err, &inUse) || len(inUse.Copies) != 2 {
			out = append(out, "the refusal does not carry the two copies as data")
		}
	}
	return out
}

func TestRetiringAKeyAnActivePlacementRecordsIsRefusedNamingTheCopies_992(t *testing.T) {
	if p := refusalProblems(realRetire); len(p) != 0 {
		t.Fatalf("RetireKey: %s", strings.Join(p, "; "))
	}
}

// Mutation self-test (matrix NV.17, "permit retiring a recorded key"): the
// assertions above are run against a retire that skips the check, and they
// must flag it. If this goes green-to-red the guard has stopped guarding.
func TestTheRetirementRefusalSelfTestFlagsAMutantThatSkipsTheCheck_992(t *testing.T) {
	mutant := func(ctx context.Context, j *fakeKeyJournal, id string) error {
		return j.RetireEncryptionKey(ctx, id, retireAt)
	}
	if p := refusalProblems(mutant); len(p) == 0 {
		t.Fatal("a retire that never looks at the ACTIVE copies passed the refusal assertions, so they assert nothing")
	}
}

// The refusal is not a constant: once the last copy is GONE the same key
// retires, with the timestamp and status the registry expects.
func TestRetiringAKeyWhoseLastCopyIsGoneSucceeds_992(t *testing.T) {
	ctx := context.Background()
	j := newKeyJournal()
	if err := placement.RetireKey(ctx, j, nil, "kold", retireAt); err == nil {
		t.Fatal("control: the refusal did not fire with two ACTIVE copies")
	}
	for i := range j.copies {
		if j.copies[i].key == "kold" {
			j.copies[i].status = state.PlacementGone
		}
	}
	if err := placement.RetireKey(ctx, j, nil, "kold", retireAt); err != nil {
		t.Fatalf("retiring a key no ACTIVE placement records: %v", err)
	}
	k := j.keys["kold"]
	if k.Status != state.EncryptionKeyRetired || k.RetiredAt == nil || !k.RetiredAt.Equal(retireAt) {
		t.Errorf("key after retirement = %+v", k)
	}
}

// A copy being deleted (DELETE_PENDING) no longer needs the key: deletion
// needs none (N1.4). A DELETE_PENDING copy therefore does not block.
func TestADeletePendingCopyDoesNotBlockRetirement_992(t *testing.T) {
	j := newKeyJournal()
	for i := range j.copies {
		if j.copies[i].key == "kold" {
			j.copies[i].status = state.PlacementDeletePending
		}
	}
	if err := placement.RetireKey(context.Background(), j, nil, "kold", retireAt); err != nil {
		t.Fatalf("a DELETE_PENDING copy blocked retirement: %v", err)
	}
}

// The active key cannot be retired: new copies are written under it. Rotate
// first. This holds even with no copy recording it yet.
func TestTheActiveKeyCannotBeRetired_992(t *testing.T) {
	j := newKeyJournal()
	err := placement.RetireKey(context.Background(), j, nil, "knew", retireAt)
	if !errors.Is(err, placement.ErrKeyStillActive) {
		t.Fatalf("err = %v, want ErrKeyStillActive", err)
	}
	if len(j.retired) != 0 {
		t.Error("the active key was retired")
	}
}

func TestRetiringAnUnknownKeyIsRefused_992(t *testing.T) {
	j := newKeyJournal()
	err := placement.RetireKey(context.Background(), j, nil, "kmissing", retireAt)
	if !errors.Is(err, keycustody.ErrUnknownKey) {
		t.Fatalf("err = %v, want keycustody.ErrUnknownKey", err)
	}
}

func TestRetiringAnAlreadyRetiredKeyChangesNothing_992(t *testing.T) {
	j := newKeyJournal()
	if err := placement.RetireKey(context.Background(), j, nil, "kretire", retireAt); err != nil {
		t.Fatalf("an already retired key is not an error: %v", err)
	}
	if len(j.retired) != 0 {
		t.Error("an already retired key was written again")
	}
}

// A copy that lands between the check and the write is caught by the
// journal's own transactional re-check; RetireKey turns that refusal into the
// named one rather than an opaque error.
func TestACopyThatAppearsBetweenTheCheckAndTheWriteIsStillNamed_992(t *testing.T) {
	j := newKeyJournal()
	for i := range j.copies {
		if j.copies[i].key == "kold" {
			j.copies[i].status = state.PlacementGone
		}
	}
	j.raceCopy = &fakeCopy{ref("late.dump", "offsite"), "kold", state.PlacementActive}

	err := placement.RetireKey(context.Background(), j, nil, "kold", retireAt)
	if !errors.Is(err, placement.ErrKeyInUse) || !strings.Contains(fmt.Sprint(err), "late.dump") {
		t.Fatalf("err = %v, want ErrKeyInUse naming late.dump", err)
	}
}

// A long list is cut for reading, with the count of the rest, and the data on
// the error still carries every copy.
func TestALongRefusalListIsCutButCompleteAsData_992(t *testing.T) {
	j := newKeyJournal()
	for i := 0; i < 40; i++ {
		j.copies = append(j.copies, fakeCopy{ref(fmt.Sprintf("bulk-%02d.dump", i), "offsite"), "kold", state.PlacementActive})
	}
	err := placement.RetireKey(context.Background(), j, nil, "kold", retireAt)
	var inUse *placement.KeyInUseError
	if !errors.As(err, &inUse) || len(inUse.Copies) != 42 {
		t.Fatalf("err = %v, want 42 copies as data", err)
	}
	if !strings.Contains(err.Error(), "and 22 more") {
		t.Errorf("the message does not say how many were left out: %q", err.Error())
	}
}

// The retirement and its refusal are on the record, with the key id and a
// result, and nothing else about the key.
func TestRetirementIsLoggedWithAResult_992(t *testing.T) {
	var buf bytes.Buffer
	log := obs.New(&buf, obs.LevelDebug)
	j := newKeyJournal()
	_ = placement.RetireKey(context.Background(), j, log, "kold", retireAt)
	for i := range j.copies {
		j.copies[i].status = state.PlacementGone
	}
	if err := placement.RetireKey(context.Background(), j, log, "kold", retireAt); err != nil {
		t.Fatal(err)
	}
	events := stageEvents(t, &buf)[obs.EventKeyRetired]
	if len(events) != 2 || events[0]["result"] != string(obs.ResultError) || events[1]["result"] != string(obs.ResultSuccess) || events[1]["key_id"] != "kold" {
		t.Errorf("retirement events = %#v, want one error then one success naming kold", events)
	}
}
