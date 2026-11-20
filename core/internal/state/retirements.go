package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/obs"
)

// Issue #1528's record of a retirement. The state itself, RETIRED, lives on
// the artifacts row like every other state; this is what a person or an
// audit wants to know about it afterwards, and it is append-only because an
// artifact can be retired, come back when its source object reappears, and
// be retired again.
//
// RetiredState is spelled here rather than imported because this package
// does not depend on internal/lifecycle (the dependency runs the other way),
// and it is the one state name this package has to recognise: a retirement
// record is refused on a transition into anything else.
const RetiredState = "RETIRED"

// The two closed vocabularies a retirement record carries. They are checked
// here, before the write, because migration 0023 deliberately puts no CHECK
// on them (see that file for why).
const (
	RetiredAutomatically = "automatic"
	RetiredByOperator    = "operator"
	RetiredByAgent       = "agent"

	SourceAbsent  = "absent"
	SourcePresent = "present"
	SourceUnknown = "unknown"
)

// RetirementUpdate is what a caller attaches to a FAILED -> RETIRED
// transition. OccurredAt and the artifact come from the Transition.
type RetirementUpdate struct {
	// ActorKind is RetiredAutomatically, RetiredByOperator or RetiredByAgent.
	ActorKind string
	// Actor names who: "engine", the signed-in user, or "agent:<name>".
	Actor string
	// SourceCheck is what the source looked like when the retirement was
	// decided: SourceAbsent, SourcePresent or SourceUnknown. Only an
	// absent check lets discovery bring the artifact back when the object
	// reappears.
	SourceCheck string
	// SupersededBy is the name of the newer artifact in the same backup
	// set that replaces this one, or empty when the operator acknowledged
	// there is none.
	SupersededBy string
	// FailureReason is the FAILED transition's detail, carried so the
	// record still says what went wrong.
	FailureReason string
	// Note is an optional operator note.
	Note string
}

func (r RetirementUpdate) validate() error {
	switch r.ActorKind {
	case RetiredAutomatically, RetiredByOperator, RetiredByAgent:
	default:
		return fmt.Errorf("state: retirement actor kind %q is not one of automatic, operator, agent", r.ActorKind)
	}
	switch r.SourceCheck {
	case SourceAbsent, SourcePresent, SourceUnknown:
	default:
		return fmt.Errorf("state: retirement source check %q is not one of absent, present, unknown", r.SourceCheck)
	}
	return nil
}

// Retirement is one artifact_retirements row read back.
type Retirement struct {
	Artifact      model.ArtifactID
	RetiredAt     time.Time
	ActorKind     string
	Actor         string
	SourceCheck   string
	SupersededBy  string
	FailureReason string
	Note          string
}

func insertRetirement(ctx context.Context, tx *sql.Tx, artifactRowID int64, t Transition, redact *obs.Redactor) error {
	r := t.Retirement
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO artifact_retirements
		   (artifact_id, transition_key, retired_at, actor_kind, actor, source_check, superseded_by, failure_reason, note)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifactRowID, t.Key, formatTime(t.OccurredAt), r.ActorKind, r.Actor, r.SourceCheck, r.SupersededBy,
		redact.Filter(r.FailureReason), redact.Filter(r.Note),
	); err != nil {
		return fmt.Errorf("state: record retirement: %w", err)
	}
	return nil
}

// LatestRetirement returns the most recent retirement record for id, and
// false when the artifact has never been retired. It says nothing about
// whether the artifact is RETIRED now: a caller that needs both reads the
// row's state as well, because a retired artifact whose object reappeared
// keeps its old record.
func (j *Journal) LatestRetirement(ctx context.Context, id model.ArtifactID) (Retirement, bool, error) {
	var r Retirement
	var retiredAt string
	err := j.db.QueryRowContext(ctx,
		`SELECT r.retired_at, r.actor_kind, r.actor, r.source_check, r.superseded_by, r.failure_reason, r.note
		   FROM artifact_retirements r JOIN artifacts a ON a.id = r.artifact_id
		  WHERE a.source = ? AND a.backup_set = ? AND a.artifact_name = ?
		  ORDER BY r.id DESC LIMIT 1`,
		id.Set.Source, id.Set.Set, id.Name,
	).Scan(&retiredAt, &r.ActorKind, &r.Actor, &r.SourceCheck, &r.SupersededBy, &r.FailureReason, &r.Note)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Retirement{}, false, nil
	case err != nil:
		return Retirement{}, false, fmt.Errorf("state: read retirement for %s: %w", id, err)
	}
	at, err := parseTime(retiredAt)
	if err != nil {
		return Retirement{}, false, fmt.Errorf("state: retirement for %s: %w", id, err)
	}
	r.Artifact = id
	r.RetiredAt = at
	return r, true, nil
}
