package state

import (
	"context"
	"errors"
	"testing"
)

// These are the journal-side tests for #1291 (encrypting plaintext retained
// copies that already exist). The opt-in is one row bound to ONE move id, so
// a later automatic move of the same artifact to the same medium never
// inherits it.
//
// They were written by reading the journal and have not been run. Each one
// names the defect that turns it red in its own comment.

// Red until RecordEncryptExisting and EncryptExistingConsented exist. Also
// red if consent were keyed by artifact instead of by move.
func TestAnOptInBelongsToTheMoveItWasRecordedFor_1291(t *testing.T) {
	ctx := context.Background()
	j, artifact := moveJournal(t)
	mv := planOne(t, j, artifact)

	if ok, err := j.EncryptExistingConsented(ctx, mv.ID); err != nil || ok {
		t.Fatalf("a move nobody opted in already reads as consented (ok=%v err=%v)", ok, err)
	}
	if err := j.RecordEncryptExisting(ctx, mv.ID, moveNow); err != nil {
		t.Fatalf("RecordEncryptExisting: %v", err)
	}
	if ok, err := j.EncryptExistingConsented(ctx, mv.ID); err != nil || !ok {
		t.Fatalf("the recorded opt-in reads back as not consented (ok=%v err=%v)", ok, err)
	}
	if ok, err := j.EncryptExistingConsented(ctx, mv.ID+1000); err != nil || ok {
		t.Fatalf("a different move id reads as consented (ok=%v err=%v)", ok, err)
	}
}

// Red if a second record errors (a resumed run records again) or writes a
// second row.
func TestRecordingAnOptInTwiceIsIdempotent_1291(t *testing.T) {
	ctx := context.Background()
	j, artifact := moveJournal(t)
	mv := planOne(t, j, artifact)
	for i := 0; i < 2; i++ {
		if err := j.RecordEncryptExisting(ctx, mv.ID, moveNow); err != nil {
			t.Fatalf("record #%d: %v", i+1, err)
		}
	}
	var n int
	if err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM encrypt_existing_consents WHERE move_id = ?`, mv.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d consent rows for one move (err=%v), want exactly one", n, err)
	}
}

// Red if an opt-in can be written for a move that does not exist, or for one
// that has already finished: a finished move's consent would be a standing
// permission nothing is waiting on.
func TestAnOptInCannotBeRecordedForAMoveThatIsGoneOrFinished_1291(t *testing.T) {
	ctx := context.Background()
	j, artifact := moveJournal(t)

	if err := j.RecordEncryptExisting(ctx, 987654, moveNow); !errors.Is(err, ErrMoveNotFound) {
		t.Fatalf("an unknown move id: err=%v, want ErrMoveNotFound", err)
	}

	mv := planOne(t, j, artifact)
	if _, err := j.AdvanceMove(ctx, MoveAdvance{MoveID: mv.ID, From: MovePlanned, To: MoveAbandoned, OccurredAt: moveNow, Error: "test"}); err != nil {
		t.Fatalf("abandoning the move: %v", err)
	}
	if err := j.RecordEncryptExisting(ctx, mv.ID, moveNow); err == nil {
		t.Fatal("an opt-in was recorded for an ABANDONED move")
	}
	if ok, err := j.EncryptExistingConsented(ctx, mv.ID); err != nil || ok {
		t.Fatalf("an ABANDONED move reads as consented (ok=%v err=%v)", ok, err)
	}
}
