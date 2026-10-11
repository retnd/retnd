package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/retnd/retnd/core/internal/model"
)

// The per-copy assurance record (migration 0016, EPIC Q, #978 and #980).
//
// # What it is
//
// A placements row says where a copy is and what class of check it last
// passed. This adds the rest of what a scheduler and a health verdict both
// need and had nowhere to read: when the copy is next due, what the last
// check cost, how many times running it has failed, and whether anyone has
// ever proven it usable as opposed to present.
//
// # Journal truth
//
// Every write here is keyed by a placements row that already exists, and
// RecordCopyScrub refuses a copy the journal does not know. A listing of a
// destination is evidence about a row; it is never a source of one, and
// there is no function in this file that would let it become one.
//
// # The rules the writes enforce, because a caller cannot be trusted to
//
//   - An existence result never moves content_verified_at, whatever it is
//     labelled. The class is a closed vocabulary and the age follows the
//     class, so there is no spelling of "a HEAD passed" that renders as
//     "the bytes were read".
//   - A weaker pass never downgrades the strongest class a copy has
//     achieved and never refreshes the age of a stronger one.
//   - A failure clears the claim it contradicts. A copy whose bytes no
//     longer hash to what was recorded is not content verified, and
//     leaving the last pass standing beside the failure would make the
//     record say both.
//   - A check that could not complete clears nothing. An endpoint that did
//     not answer is a fact about the endpoint, and erasing a content claim
//     because of it would be the same mistake as quarantining a good
//     backup because DNS had an afternoon.

// The scrub claim's states. They are the Go copy of the CHECK in
// 0016_copy_assurance.sql, and TestAssuranceVocabulariesAgreeWithTheSchema
// holds the two together. AssuranceUnknown is the zero value and the column
// default: a copy nobody has checked cannot read as anything better.
const (
	// AssuranceUnknown means nothing has checked this copy.
	AssuranceUnknown = "unknown"
	// AssurancePassed means the last check ran and the copy satisfied the
	// class it ran. LastCheckedClass says which, and an existence pass is
	// not content verification.
	AssurancePassed = "passed"
	// AssuranceFailed means the last check ran and the copy did not
	// satisfy it.
	AssuranceFailed = "failed"
	// AssuranceRequiresRestore means the copy is in an archive storage
	// class and cannot be read without a restore this product never
	// starts by itself.
	AssuranceRequiresRestore = "requires_restore"
	// AssuranceCouldNotComplete means the check could not be made.
	AssuranceCouldNotComplete = "could_not_complete"
)

// AssuranceStates is the scrub claim's whole vocabulary.
var AssuranceStates = []string{
	AssuranceUnknown, AssurancePassed, AssuranceFailed,
	AssuranceRequiresRestore, AssuranceCouldNotComplete,
}

// The recovery-test claim's states. See the 0016 header for what each
// means. RecoveryUnknown is the zero value.
const (
	RecoveryUnknown          = "unknown"
	RecoveryNotConfigured    = "not_configured"
	RecoveryNeverRun         = "never_run"
	RecoveryPassed           = "passed"
	RecoveryOverdue          = "overdue"
	RecoveryFailed           = "failed"
	RecoveryRefusedForCost   = "refused_for_cost"
	RecoveryCouldNotComplete = "could_not_complete"
)

// RecoveryStates is the recovery-test claim's whole vocabulary.
var RecoveryStates = []string{
	RecoveryUnknown, RecoveryNotConfigured, RecoveryNeverRun, RecoveryPassed,
	RecoveryOverdue, RecoveryFailed, RecoveryRefusedForCost, RecoveryCouldNotComplete,
}

// How a recovery-test validator's process ended, and how sure anyone is.
const (
	// RecoveryTerminationExited means the process ended on its own.
	RecoveryTerminationExited = "exited"
	// RecoveryTerminationKilledGroup means the timeout fired and the
	// process group was killed and observed gone.
	RecoveryTerminationKilledGroup = "killed_group"
	// RecoveryTerminationUnconfirmed means the kill was requested and its
	// effect was not observed.
	RecoveryTerminationUnconfirmed = "unconfirmed"
)

// Whether the recovered copy was removed after a recovery test.
const (
	RecoveryCleanupRemoved = "removed"
	RecoveryCleanupFailed  = "failed"
)

// MaxScrubReasonBytes bounds the free text a scrub or a recovery test may
// store about a failure. The reason comes from checks that wrap errors from
// the filesystem and from storage mediums, and a stored value is read back
// by every surface that shows the copy, so it is bounded and stripped here
// rather than trusted to every caller.
const MaxScrubReasonBytes = 512

// Assurance is the per-copy assurance record, embedded in Placement.
type Assurance struct {
	// AssuranceState is the outcome of the most recent scrub of this copy.
	AssuranceState string

	// NextDueAt is when this copy is next owed a check. Nil means due now:
	// a copy a rebuild just wrote, a copy an upgrade just widened and a
	// copy a future code path forgot to fill all fall the same way.
	NextDueAt *time.Time

	// LastCheckedAt and LastCheckedClass are when a check of this copy last
	// ran to a verdict, and the class it ran.
	LastCheckedAt    *time.Time
	LastCheckedClass string

	// ContentVerifiedAt is the age "Content verified" renders from. It
	// moves only when a check that read the bytes passed.
	ContentVerifiedAt *time.Time

	// LastFailureAt and LastFailureReason are the most recent failure or
	// non-completion. They survive a later pass: only the streak resets.
	LastFailureAt       *time.Time
	LastFailureReason   string
	ConsecutiveFailures int

	// LastBytesRead and LastDurationMS are what the last check cost.
	LastBytesRead  *int64
	LastDurationMS *int64

	// Lease is who holds this copy for a scrub right now, if anyone.
	ScrubLeaseUntil  *time.Time
	ScrubLeaseHolder string

	// The recovery-test claim (#980). It is a separate home from the scrub
	// claim because passing one says nothing about the other.
	RecoveryState         string
	RecoveryTestedAt      *time.Time
	RecoveryNextDueAt     *time.Time
	RecoveryFailureReason string
	RecoveryValidatorID   string
	RecoveryValidatorHash string
	// RecoveryDigestMatched is the gate: nil means the digest was never
	// compared. A validator verdict means nothing unless this is true.
	RecoveryDigestMatched   *bool
	RecoveryTermination     string
	RecoveryOutputTruncated bool
	RecoveryCleanupState    string
	RecoveryBytesRead       *int64
	RecoveryDurationMS      *int64
}

// assuranceColumns is the tail of placementColumns, in the order
// assuranceFields.dests scans it.
const assuranceColumns = `
	assurance_state, next_due_at, last_checked_at, last_checked_class,
	content_verified_at, last_failure_at, last_failure_reason, consecutive_failures,
	last_bytes_read, last_duration_ms, scrub_lease_until, scrub_lease_holder,
	recovery_state, recovery_tested_at, recovery_next_due_at, recovery_failure_reason,
	recovery_validator_id, recovery_validator_hash, recovery_digest_matched,
	recovery_termination, recovery_output_truncated, recovery_cleanup_state,
	recovery_bytes_read, recovery_duration_ms`

type assuranceFields struct {
	state, checkedClass, failureReason, leaseHolder      string
	nextDue, checkedAt, contentAt, failureAt, leaseUntil sql.NullString
	failures                                             int
	bytesRead, durationMS                                sql.NullInt64

	recoveryState, recoveryFailureReason, validatorID, validatorHash string
	recoveryTermination, recoveryCleanup                             string
	recoveryTestedAt, recoveryNextDue                                sql.NullString
	digestMatched                                                    sql.NullInt64
	outputTruncated                                                  int
	recoveryBytes, recoveryDuration                                  sql.NullInt64
}

func (f *assuranceFields) dests() []any {
	return []any{
		&f.state, &f.nextDue, &f.checkedAt, &f.checkedClass,
		&f.contentAt, &f.failureAt, &f.failureReason, &f.failures,
		&f.bytesRead, &f.durationMS, &f.leaseUntil, &f.leaseHolder,
		&f.recoveryState, &f.recoveryTestedAt, &f.recoveryNextDue, &f.recoveryFailureReason,
		&f.validatorID, &f.validatorHash, &f.digestMatched,
		&f.recoveryTermination, &f.outputTruncated, &f.recoveryCleanup,
		&f.recoveryBytes, &f.recoveryDuration,
	}
}

func (f *assuranceFields) build() (Assurance, error) {
	var out Assurance
	for _, c := range []struct {
		name string
		in   sql.NullString
		dst  **time.Time
	}{
		{"next_due_at", f.nextDue, &out.NextDueAt},
		{"last_checked_at", f.checkedAt, &out.LastCheckedAt},
		{"content_verified_at", f.contentAt, &out.ContentVerifiedAt},
		{"last_failure_at", f.failureAt, &out.LastFailureAt},
		{"scrub_lease_until", f.leaseUntil, &out.ScrubLeaseUntil},
		{"recovery_tested_at", f.recoveryTestedAt, &out.RecoveryTestedAt},
		{"recovery_next_due_at", f.recoveryNextDue, &out.RecoveryNextDueAt},
	} {
		t, err := scanOptionalTime(c.in)
		if err != nil {
			// A time that will not parse fails the read, for
			// buildPlacement's reason: a zero here would read as a very
			// old answer, or worse as a missing one, and a missing
			// next_due_at means due now.
			return Assurance{}, fmt.Errorf("state: stored placement %s is invalid: %w", c.name, err)
		}
		*c.dst = t
	}
	out.AssuranceState = f.state
	out.LastCheckedClass = f.checkedClass
	out.LastFailureReason = f.failureReason
	out.ConsecutiveFailures = f.failures
	out.LastBytesRead = scanOptionalInt64(f.bytesRead)
	out.LastDurationMS = scanOptionalInt64(f.durationMS)
	out.ScrubLeaseHolder = f.leaseHolder
	out.RecoveryState = f.recoveryState
	out.RecoveryFailureReason = f.recoveryFailureReason
	out.RecoveryValidatorID = f.validatorID
	out.RecoveryValidatorHash = f.validatorHash
	if f.digestMatched.Valid {
		matched := f.digestMatched.Int64 == 1
		out.RecoveryDigestMatched = &matched
	}
	out.RecoveryTermination = f.recoveryTermination
	out.RecoveryOutputTruncated = f.outputTruncated == 1
	out.RecoveryCleanupState = f.recoveryCleanup
	out.RecoveryBytesRead = scanOptionalInt64(f.recoveryBytes)
	out.RecoveryDurationMS = scanOptionalInt64(f.recoveryDuration)
	return out, nil
}

// DueAt reports whether this copy is owed a scrub at now. A copy with no
// recorded due time is due: absence of a schedule is never a reason to
// leave a copy alone.
func (a Assurance) DueAt(now time.Time) bool {
	return a.NextDueAt == nil || !a.NextDueAt.After(now)
}

// ScrubVerdict is what one scrub of one copy concluded.
type ScrubVerdict string

const (
	// ScrubPassed means the copy satisfied the class that ran.
	ScrubPassed ScrubVerdict = "passed"
	// ScrubFailed means the copy did not satisfy the class that ran.
	ScrubFailed ScrubVerdict = "failed"
	// ScrubCouldNotComplete means the check could not be made, which is a
	// fact about the endpoint and not about the copy.
	ScrubCouldNotComplete ScrubVerdict = "could_not_complete"
	// ScrubRequiresRestore means the copy cannot be read without a restore.
	// Class carries what was nonetheless achieved (existence, from a HEAD).
	ScrubRequiresRestore ScrubVerdict = "requires_restore"
	// ScrubNotChecked means nothing configured could produce a verdict for
	// this copy. It records no claim at all and only moves the due time, so
	// a copy nothing can check does not hog every cycle.
	ScrubNotChecked ScrubVerdict = "not_checked"
)

// CopyScrub is one scrub's result for one copy.
type CopyScrub struct {
	Artifact model.ArtifactID
	// Medium names the copy: MediumLocal or a configured medium id.
	Medium  string
	Verdict ScrubVerdict
	// Class is the class that RAN, never the class asked for. It is
	// required for every verdict that read or probed the copy, and empty
	// for ScrubCouldNotComplete and ScrubNotChecked.
	Class  string
	Reason string
	// At is when the check ran; NextDue is when the copy is next owed one.
	At      time.Time
	NextDue time.Time
	// BytesRead and Duration are what the check cost.
	BytesRead *int64
	Duration  time.Duration
}

func (c CopyScrub) validate() error {
	if c.Artifact.Set.IsZero() || c.Artifact.Name == "" {
		return errors.New("state: a scrub result needs an artifact")
	}
	if c.Medium == "" {
		return errors.New("state: a scrub result needs the medium of the copy it is about")
	}
	if c.At.IsZero() || c.NextDue.IsZero() {
		return errors.New("state: a scrub result needs both the time it ran and the time the copy is next due")
	}
	switch c.Verdict {
	case ScrubRequiresRestore:
		// A copy refused before anything ran (a content scrub of an
		// archive-class object) ran no class, and that is allowed. One that
		// was HEADed first carries existence.
		if c.Class != "" && !validCheckClass(c.Class) {
			return fmt.Errorf("state: a requires_restore scrub result carries no class or a valid one, got %q", c.Class)
		}
	case ScrubPassed, ScrubFailed:
		if !validCheckClass(c.Class) {
			return fmt.Errorf("state: a %s scrub result needs the class that ran (content, attested or existence), got %q", c.Verdict, c.Class)
		}
	case ScrubCouldNotComplete, ScrubNotChecked:
		if c.Class != "" {
			return fmt.Errorf("state: a %s scrub result ran no class, got %q", c.Verdict, c.Class)
		}
	default:
		return fmt.Errorf("state: %q is not a scrub verdict", c.Verdict)
	}
	return nil
}

func validCheckClass(c string) bool {
	return c == VerificationContent || c == VerificationAttested || c == VerificationExistence
}

// classRank orders the ladder, strongest first. internal/placement owns the
// meaning of each rung; this package only needs the order, to decide whether
// a result may refresh a stronger class's age.
func classRank(c string) int {
	switch c {
	case VerificationContent:
		return 3
	case VerificationAttested:
		return 2
	case VerificationExistence:
		return 1
	default:
		return 0
	}
}

// ErrPlacementNotFound is returned when a write names a copy the journal
// has no row for.
var ErrPlacementNotFound = errors.New("state: no such placement")

// ErrPlacementRetired is returned when a verdict is written for a copy that
// is no longer ACTIVE. A scrub reads for a long time and retention may retire
// the copy meanwhile; the verdict is about bytes that are no longer a copy,
// and a "passed" or "failed" stamped on a GONE row is a claim about nothing.
var ErrPlacementRetired = errors.New("state: the copy is no longer active")

// ScrubClaimOverdue is the derived reading of a stored pass that is past its
// due time. It is never stored: a stored pass is a statement about the moment
// it was made, and what it means now is a function of the clock.
const ScrubClaimOverdue = "overdue"

// ScrubClaim is the scrub claim a surface should render at now: the stored
// state, except that a pass whose next check has fallen due reads overdue. A
// failure, an archived copy and an incomplete check keep their own readings
// however old they are, because ageing must not soften them.
func (a Assurance) ScrubClaim(now time.Time) string {
	st := a.AssuranceState
	if st == "" {
		return AssuranceUnknown
	}
	if st == AssurancePassed && !a.DueAt(now) {
		return AssurancePassed
	}
	if st == AssurancePassed {
		return ScrubClaimOverdue
	}
	return st
}

// RecordCopyScrub durably writes one scrub's result onto the copy it is
// about, releases that copy's lease, and does both in one statement so a
// crash cannot leave a verdict written and the lease held, or the reverse.
//
// It writes nothing to state_transitions. A pass over a thousand copies
// would append a thousand rows to a feed that is cursor-paged and never
// pruned; the artifact-level consequence of a failure (a quarantine) is
// the caller's own lifecycle transition, and the per-pass counts are
// RecordScrubPass's one row.
func (j *Journal) RecordCopyScrub(ctx context.Context, c CopyScrub) error {
	if err := c.validate(); err != nil {
		return err
	}
	reason := boundReason(c.Reason)
	at := formatTime(c.At)
	next := formatTime(c.NextDue)
	var durMS any
	if c.Verdict != ScrubNotChecked {
		durMS = c.Duration.Milliseconds()
	}

	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: record scrub of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	var (
		id            int64
		curClass      string
		curVerifiedAt sql.NullString
		curContent    sql.NullString
		curStatus     string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT p.id, p.verification_class, p.verified_at, p.content_verified_at, p.status
		  FROM placements p JOIN artifacts a ON a.id = p.artifact_id
		 WHERE a.source = ? AND a.backup_set = ? AND a.artifact_name = ? AND p.medium = ?`,
		c.Artifact.Set.Source, c.Artifact.Set.Set, c.Artifact.Name, c.Medium,
	).Scan(&id, &curClass, &curVerifiedAt, &curContent, &curStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s has no copy on %q", ErrPlacementNotFound, c.Artifact, c.Medium)
	}
	if err != nil {
		return fmt.Errorf("state: record scrub of %s on %s: %w", c.Artifact, c.Medium, err)
	}

	if curStatus != PlacementActive {
		return fmt.Errorf("%w: %s on %q is %s", ErrPlacementRetired, c.Artifact, c.Medium, curStatus)
	}

	switch c.Verdict {
	case ScrubNotChecked:
		_, err = tx.ExecContext(ctx, `
			UPDATE placements SET next_due_at = ?, scrub_lease_until = NULL, scrub_lease_holder = ''
			 WHERE id = ?`, next, id)

	case ScrubCouldNotComplete:
		_, err = tx.ExecContext(ctx, `
			UPDATE placements SET
				assurance_state = 'could_not_complete', next_due_at = ?,
				last_failure_at = ?, last_failure_reason = ?,
				consecutive_failures = consecutive_failures + 1,
				last_duration_ms = ?,
				scrub_lease_until = NULL, scrub_lease_holder = ''
			 WHERE id = ?`, next, at, reason, durMS, id)

	case ScrubFailed:
		// The failure contradicts every claim of the class it ran and of
		// every weaker one it implies: a copy that is not there at the
		// right size is not content verified either. So all three of the
		// class, its age and the content age are cleared together.
		_, err = tx.ExecContext(ctx, `
			UPDATE placements SET
				assurance_state = 'failed', next_due_at = ?,
				last_checked_at = ?, last_checked_class = ?,
				last_failure_at = ?, last_failure_reason = ?,
				consecutive_failures = consecutive_failures + 1,
				verification_class = '', verified_at = NULL, content_verified_at = NULL,
				last_bytes_read = ?, last_duration_ms = ?,
				scrub_lease_until = NULL, scrub_lease_holder = ''
			 WHERE id = ?`, next, at, c.Class, at, reason, optionalInt64(c.BytesRead), durMS, id)

	case ScrubRequiresRestore:
		if c.Class == "" {
			_, err = tx.ExecContext(ctx, `
				UPDATE placements SET
					assurance_state = 'requires_restore', next_due_at = ?,
					consecutive_failures = 0, last_duration_ms = ?,
					scrub_lease_until = NULL, scrub_lease_holder = ''
				 WHERE id = ?`, next, durMS, id)
			break
		}
		fallthrough

	default: // ScrubPassed, and ScrubRequiresRestore after a HEAD
		state := AssurancePassed
		if c.Verdict == ScrubRequiresRestore {
			state = AssuranceRequiresRestore
		}
		newClass, newVerified := curClass, nullStringAny(curVerifiedAt)
		if classRank(c.Class) >= classRank(curClass) {
			newClass, newVerified = c.Class, at
		}
		// The content age follows the class and nothing else. Only a
		// result that read the bytes may set it.
		contentAt := nullStringAny(curContent)
		if c.Class == VerificationContent {
			contentAt = at
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE placements SET
				assurance_state = ?, next_due_at = ?,
				last_checked_at = ?, last_checked_class = ?,
				verification_class = ?, verified_at = ?, content_verified_at = ?,
				consecutive_failures = 0,
				last_bytes_read = ?, last_duration_ms = ?,
				scrub_lease_until = NULL, scrub_lease_holder = ''
			 WHERE id = ?`,
			state, next, at, c.Class, newClass, newVerified, contentAt,
			optionalInt64(c.BytesRead), durMS, id)
	}
	if err != nil {
		return fmt.Errorf("state: record scrub of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: record scrub of %s on %s: %w", c.Artifact, c.Medium, err)
	}
	return nil
}

func nullStringAny(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

// boundReason makes free text safe to store and to show: no control byte of
// any kind survives (an escape sequence in a stored reason reaches a
// terminal the day somebody prints the copy), the text is cut on a rune
// boundary at MaxScrubReasonBytes, and a cut is marked.
func boundReason(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError, r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	const marker = " [truncated]"
	if len(out) <= MaxScrubReasonBytes {
		return out
	}
	cut := MaxScrubReasonBytes - len(marker)
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + marker
}

// ClaimCopyScrub takes the lease on one copy for holder until now+ttl, and
// reports whether it got it.
//
// The lease is what makes "one scrub per copy" true across processes. A
// scheduler tick inside the daemon and a manual `retnd scrub` are different
// processes over one journal file, so no in-memory lock can be the thing
// that stops them both reading the same bytes. The claim is a compare and
// swap on the value it observed, not an ordering comparison in SQL: the
// stored times are RFC 3339 text and do not sort chronologically once a
// fractional part drops trailing zeros, so the comparison is made on parsed
// times in Go and only the swap is made in SQL.
//
// A lease expires. A holder that died does not make its copy unscrubbable,
// and the same holder asking again renews rather than conflicts. Only an
// ACTIVE copy can be claimed; a GONE or DELETE_PENDING copy is not
// something a scrub has any business reading, and false with a nil error is
// the answer.
func (j *Journal) ClaimCopyScrub(ctx context.Context, a model.ArtifactID, medium, holder string, now time.Time, ttl time.Duration) (bool, error) {
	if holder == "" {
		return false, errors.New("state: a scrub lease needs a holder")
	}
	if ttl <= 0 {
		return false, errors.New("state: a scrub lease needs a positive ttl")
	}

	var (
		id           int64
		status       string
		leaseUntil   sql.NullString
		currentOwner string
	)
	err := j.db.QueryRowContext(ctx, `
		SELECT p.id, p.status, p.scrub_lease_until, p.scrub_lease_holder
		  FROM placements p JOIN artifacts a ON a.id = p.artifact_id
		 WHERE a.source = ? AND a.backup_set = ? AND a.artifact_name = ? AND p.medium = ?`,
		a.Set.Source, a.Set.Set, a.Name, medium,
	).Scan(&id, &status, &leaseUntil, &currentOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("state: claim scrub of %s on %s: %w", a, medium, err)
	}
	if status != PlacementActive {
		return false, nil
	}
	if leaseUntil.Valid {
		until, perr := parseTime(leaseUntil.String)
		if perr != nil {
			// A lease that cannot be read is treated as held: refusing a
			// scrub is cheap, and reading the same bytes twice is the
			// thing the lease exists to prevent.
			return false, nil
		}
		if until.After(now) && currentOwner != holder {
			return false, nil
		}
	}

	res, err := j.db.ExecContext(ctx, `
		UPDATE placements SET scrub_lease_until = ?, scrub_lease_holder = ?
		 WHERE id = ? AND status = 'ACTIVE'
		   AND COALESCE(scrub_lease_until, '') = ? AND scrub_lease_holder = ?`,
		formatTime(now.Add(ttl)), holder, id, leaseUntil.String, currentOwner)
	if err != nil {
		return false, fmt.Errorf("state: claim scrub of %s on %s: %w", a, medium, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("state: claim scrub of %s on %s: %w", a, medium, err)
	}
	return n == 1, nil
}

// ReleaseCopyScrub gives a lease back without recording a result, for the
// holder that claimed a copy and then decided not to scrub it (an edit hold,
// a cancelled context). It only releases a lease holder owns.
func (j *Journal) ReleaseCopyScrub(ctx context.Context, a model.ArtifactID, medium, holder string) error {
	_, err := j.db.ExecContext(ctx, `
		UPDATE placements SET scrub_lease_until = NULL, scrub_lease_holder = ''
		 WHERE scrub_lease_holder = ? AND medium = ?
		   AND artifact_id = (SELECT id FROM artifacts WHERE source = ? AND backup_set = ? AND artifact_name = ?)`,
		holder, medium, a.Set.Source, a.Set.Set, a.Name)
	if err != nil {
		return fmt.Errorf("state: release scrub of %s on %s: %w", a, medium, err)
	}
	return nil
}

// Who started a scrub pass.
const (
	ScrubBySchedule = "schedule"
	ScrubByManual   = "manual"
)

// ScrubPass is the durable record of one scrub pass: counts, and never a
// copy. A pass over a thousand due copies is one row.
type ScrubPass struct {
	ID          int64
	TriggeredBy string
	// Scope says what the pass was asked to cover ("all-due", a backup set
	// id, an artifact id or a copy), in words an operator reads.
	Scope      string
	StartedAt  time.Time
	FinishedAt time.Time

	Selected, Checked, Passed, Failed int
	CouldNotComplete, RequiresRestore int
	SkippedHeld, SkippedLeased        int
	BytesRead                         int64
}

// RecordScrubPass appends one pass record and returns its id.
func (j *Journal) RecordScrubPass(ctx context.Context, p ScrubPass) (int64, error) {
	if p.TriggeredBy != ScrubBySchedule && p.TriggeredBy != ScrubByManual {
		return 0, fmt.Errorf("state: %q is not a scrub trigger", p.TriggeredBy)
	}
	for name, n := range map[string]int{
		"selected": p.Selected, "checked": p.Checked, "passed": p.Passed, "failed": p.Failed,
		"could_not_complete": p.CouldNotComplete, "requires_restore": p.RequiresRestore,
		"skipped_held": p.SkippedHeld, "skipped_leased": p.SkippedLeased,
	} {
		if n < 0 {
			return 0, fmt.Errorf("state: a scrub pass cannot count %d %s", n, name)
		}
	}
	if p.BytesRead < 0 {
		return 0, errors.New("state: a scrub pass cannot have read a negative number of bytes")
	}
	res, err := j.db.ExecContext(ctx, `
		INSERT INTO scrub_passes (
			triggered_by, scope, started_at, finished_at, selected, checked, passed, failed,
			could_not_complete, requires_restore, skipped_held, skipped_leased, bytes_read
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.TriggeredBy, boundReason(p.Scope), formatTime(p.StartedAt), formatTime(p.FinishedAt),
		p.Selected, p.Checked, p.Passed, p.Failed, p.CouldNotComplete, p.RequiresRestore,
		p.SkippedHeld, p.SkippedLeased, p.BytesRead)
	if err != nil {
		return 0, fmt.Errorf("state: record scrub pass: %w", err)
	}
	return res.LastInsertId()
}

// ListScrubPasses returns the most recent passes, newest first.
func (j *Journal) ListScrubPasses(ctx context.Context, limit int) ([]ScrubPass, error) {
	if limit <= 0 {
		return nil, errors.New("state: ListScrubPasses needs a positive limit")
	}
	rows, err := j.db.QueryContext(ctx, `
		SELECT id, triggered_by, scope, started_at, finished_at, selected, checked, passed, failed,
		       could_not_complete, requires_restore, skipped_held, skipped_leased, bytes_read
		  FROM scrub_passes ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("state: list scrub passes: %w", err)
	}
	defer rows.Close()
	var out []ScrubPass
	for rows.Next() {
		var (
			p                 ScrubPass
			started, finished string
		)
		if err := rows.Scan(&p.ID, &p.TriggeredBy, &p.Scope, &started, &finished, &p.Selected, &p.Checked,
			&p.Passed, &p.Failed, &p.CouldNotComplete, &p.RequiresRestore, &p.SkippedHeld, &p.SkippedLeased, &p.BytesRead); err != nil {
			return nil, fmt.Errorf("state: scan scrub pass: %w", err)
		}
		if p.StartedAt, err = parseTime(started); err != nil {
			return nil, fmt.Errorf("state: stored scrub pass started_at %q is invalid: %w", started, err)
		}
		if p.FinishedAt, err = parseTime(finished); err != nil {
			return nil, fmt.Errorf("state: stored scrub pass finished_at %q is invalid: %w", finished, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
