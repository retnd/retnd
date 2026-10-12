-- 0016_copy_assurance: the per-copy assurance record (EPIC Q, #978 and
-- #980).
--
-- WHY THIS EXISTS. Two questions have no durable answer today. "When is
-- this copy next due for a look" is answered from the ARTIFACT row's
-- updated_at (revalidate.SelectDue), and that row is touched by startup
-- reconciliation and by a catalog rebuild, so a copy nobody re-read can
-- look freshly verified. And "has this copy ever been proven usable, as
-- distinct from present" has no home at all. Both are facts about a COPY,
-- and a copy is a placements row.
--
-- WHY A SIDE TABLE AND NOT COLUMNS ON placements. An earlier draft of this
-- file added thirty columns to placements. Two things were wrong with that.
-- Its header said nothing references placements, and that is false:
-- placement_moves.source_placement_id REFERENCES placements (id) (0007).
-- And it moved the corpus's FR-35 promise (compat cell 03-migrated-schema:
-- an existing table may never change), which could only be got past by
-- re-capturing the pinned line by hand, so the next change to placements
-- needed another exception. The record is 1:1 with a copy and optional, so
-- it is a table of its own, keyed by the placement:
--
--   * placements is exactly as 0013 left it, and cell 03 stays purely
--     additive with no waiver;
--   * a row is created the first time a scrub or a recovery test writes
--     one, and ABSENCE MEANS UNKNOWN AND DUE, so a copy this migration has
--     never touched, a copy a catalog rebuild just wrote and a copy a
--     future code path forgot are all owed a check;
--   * ON DELETE CASCADE plus a DELETE in the placement upsert means a claim
--     dies with the bytes it was made about (the panel's C5);
--   * the closed state vocabularies below sit on a table nothing references,
--     so widening one later (EPICs M, N and P are expected to add states)
--     rebuilds this table and never placements.
--
-- The one thing this table does not hold is the verification class and its
-- age. verification_class and verified_at are 0007's, they stay on
-- placements, and a scrub writes them there as it always has.
--
-- THE THREE CLAIMS, THREE HOMES. Stored is the placements row itself.
-- Content verified is verification_class + content_verified_at +
-- assurance_state (the scrub claim, below). Recovery tested is the
-- recovery_* columns (the recovery-test claim, below). They are separate
-- columns with separate ages because the first rule of this epic is that
-- no surface may collapse them into one green tick: a HEAD that found an
-- object of the right size is last_checked_class = 'existence', and it
-- must never be able to move content_verified_at.
--
-- THE ZERO VALUE IS THE ONE THAT CANNOT LOOK GOOD. Every state column
-- defaults to 'unknown', every age column to NULL and every counter to 0,
-- and a copy with no row at all reads the same. A NULL next_due_at is DUE,
-- not "never due". Nothing below backfills a time, for the reason 0007
-- refused to backfill verified_at: there is no honest value to put there,
-- and putting one in would make every upgraded copy claim a check it never
-- had.
--
-- WHY THE VOCABULARIES ARE CHECKed HERE WHEN 0012 REFUSED TO. 0012 left its
-- state strings to Go because they were going to grow. These are the
-- opposite case: the point of each list is that it is closed, a state
-- nobody has named must not be storable, and the column's zero value is
-- the load-bearing one. internal/state carries the Go copy of each list
-- and a test pins the two together.
--
-- A FUTURE REBUILD OF placements. Rebuilding placements (drop, create,
-- copy) is done the way 0002 and 0006 rebuilt artifacts, inside
-- suspendForeignKeys, and that now protects this table's rows as well as
-- placement_moves's: with foreign keys on, the DROP would cascade-delete
-- every claim.
--
-- THE NUMBER. 0014 and 0015 are reserved by the two epics landing beside
-- this one and are not in the tree yet. migrate applies any unapplied
-- version whatever its order, so a gap is safe, and this file is
-- independent of both of them. 0016 has not been published, so this file
-- is the only version of it there is.
--
CREATE TABLE placement_assurance (
    placement_id INTEGER PRIMARY KEY REFERENCES placements (id) ON DELETE CASCADE,

    -- -----------------------------------------------------------------
    -- THE SCRUB CLAIM (#978)
    -- -----------------------------------------------------------------

    -- assurance_state is the outcome of the most recent scrub of THIS copy.
    --   unknown             nothing has checked this copy (the zero value)
    --   passed              the last check ran and the copy satisfied the
    --                       class it ran; last_checked_class says WHICH
    --                       class, and 'existence' is not content
    --                       verification
    --   failed              the last check ran and the copy did not
    --                       satisfy it
    --   requires_restore    the copy sits in an archive storage class, so
    --                       its bytes cannot be read without a restore this
    --                       product never starts on its own
    --   could_not_complete  the check could not be made (the endpoint did
    --                       not answer); a fact about the endpoint and not
    --                       about the copy, and it never quarantines
    --                       anything
    assurance_state TEXT NOT NULL DEFAULT 'unknown'
        CHECK (assurance_state IN ('unknown', 'passed', 'failed', 'requires_restore', 'could_not_complete')),

    -- next_due_at is when this copy is next owed a check. NULL is due now.
    next_due_at TEXT,

    -- last_checked_at / last_checked_class: when a check of this copy last
    -- ran to a verdict, and which class it ran. A check that could not
    -- complete records neither, because nothing was checked.
    last_checked_at    TEXT,
    last_checked_class TEXT NOT NULL DEFAULT ''
        CHECK (last_checked_class IN ('', 'content', 'attested', 'existence')),

    -- content_verified_at is the age "Content verified" renders from. It
    -- moves only when a check that READ THE BYTES passed. verified_at
    -- cannot be that age: it is "when verification_class was last
    -- achieved", an existence pass writes it too, and a copy whose
    -- verified_at is fresh while its bytes are not is the fixture this
    -- column exists to be wrong about.
    content_verified_at TEXT,

    -- The failure record. consecutive_failures counts verdicts of 'failed'
    -- and 'could_not_complete' in a row; any pass resets it. Health reads
    -- it, and a transient blip is the same count as a persistent one on
    -- purpose, because the threshold is what tells them apart.
    last_failure_at      TEXT,
    last_failure_reason  TEXT NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),

    -- What the last check cost. NULL is "not recorded", and 0 is a real
    -- answer.
    last_bytes_read  INTEGER,
    last_duration_ms INTEGER,

    -- The lease that makes two scrubbers of one copy impossible. A
    -- scheduler tick and a manual `retnd scrub` are different processes
    -- sharing this file, so an in-memory lock cannot be what stops them; a
    -- conditional UPDATE here can. A lease expires, so a process that died
    -- holding one does not make its copy unscrubbable.
    scrub_lease_until  TEXT,
    scrub_lease_holder TEXT NOT NULL DEFAULT '',

    -- -----------------------------------------------------------------
    -- THE RECOVERY-TEST CLAIM (#980)
    -- -----------------------------------------------------------------

    -- recovery_state is the outcome of the most recent recovery test of
    -- this copy, a different column from assurance_state because passing
    -- one says nothing about the other.
    --   unknown            nothing has evaluated this claim (the zero value)
    --   not_configured     no validator is declared; not a pass, not a failure
    --   never_run          a validator is declared and has never run here
    --   passed             the recovered bytes matched the artifact's digest
    --                      AND the declared validator accepted them
    --   overdue            the last pass is older than its cadence allows
    --   failed             the digest did not match, or the validator
    --                      rejected the bytes
    --   refused_for_cost   the test needs a read that costs money and
    --                      nobody acknowledged the cost
    --   could_not_complete the test could not be carried out
    recovery_state TEXT NOT NULL DEFAULT 'unknown'
        CHECK (recovery_state IN ('unknown', 'not_configured', 'never_run', 'passed', 'overdue', 'failed', 'refused_for_cost', 'could_not_complete')),

    recovery_tested_at      TEXT,
    recovery_next_due_at    TEXT,
    recovery_failure_reason TEXT NOT NULL DEFAULT '',

    -- Which validator said so. The id is the operator's own name for it
    -- and never a path; the hash is of the validator's bytes, so a changed
    -- validator is a different recorded identity and an old pass is not
    -- silently inherited by it.
    recovery_validator_id   TEXT NOT NULL DEFAULT '',
    recovery_validator_hash TEXT NOT NULL DEFAULT '',

    -- recovery_digest_matched is the gate: NULL means the digest was never
    -- compared, 1 means the recovered bytes hashed to the artifact's
    -- recorded digest, 0 means they did not. A validator verdict means
    -- nothing unless this is 1.
    recovery_digest_matched INTEGER CHECK (recovery_digest_matched IN (0, 1)),

    -- How the validator's process ended, and whether anyone can be sure. A
    -- validator that hangs is killed with its process group, and
    -- 'unconfirmed' says the kill was requested and its effect was not
    -- observed, which is a different fact from 'killed_group'.
    recovery_termination TEXT NOT NULL DEFAULT ''
        CHECK (recovery_termination IN ('', 'exited', 'killed_group', 'unconfirmed')),

    recovery_output_truncated INTEGER NOT NULL DEFAULT 0 CHECK (recovery_output_truncated IN (0, 1)),

    -- Whether the recovered copy was removed afterwards. It is its own
    -- column because a cleanup failure and a test failure mean different
    -- things: one says the claim is unproven, the other says a disk is
    -- filling, and neither may overwrite the other.
    recovery_cleanup_state TEXT NOT NULL DEFAULT ''
        CHECK (recovery_cleanup_state IN ('', 'removed', 'failed')),

    recovery_bytes_read  INTEGER,
    recovery_duration_ms INTEGER
);

-- The due scan reads next_due_at. Absence of a row also means due, so the
-- scan is a LEFT JOIN; this index serves the rows that do exist.
CREATE INDEX idx_placement_assurance_next_due ON placement_assurance (next_due_at);

-- ---------------------------------------------------------------------
-- THE DURABLE PASS RECORD (#978)
-- ---------------------------------------------------------------------

-- scrub_passes is one row per scrub PASS, with counts. It has no artifact
-- and no placement column on purpose: the activity feed is cursor-paged and
-- append-only and nothing prunes it, so a pass over a thousand due copies
-- must cost one row here and, in state_transitions, only a row for each
-- artifact a failure actually quarantined. Per-copy progress is live-only.
CREATE TABLE scrub_passes (
    id                  INTEGER PRIMARY KEY,
    triggered_by        TEXT NOT NULL CHECK (triggered_by IN ('schedule', 'manual')),
    scope               TEXT NOT NULL,
    started_at          TEXT NOT NULL,
    finished_at         TEXT NOT NULL,
    selected            INTEGER NOT NULL CHECK (selected >= 0),
    checked             INTEGER NOT NULL CHECK (checked >= 0),
    passed              INTEGER NOT NULL CHECK (passed >= 0),
    failed              INTEGER NOT NULL CHECK (failed >= 0),
    could_not_complete  INTEGER NOT NULL CHECK (could_not_complete >= 0),
    requires_restore    INTEGER NOT NULL CHECK (requires_restore >= 0),
    skipped_held        INTEGER NOT NULL CHECK (skipped_held >= 0),
    skipped_leased      INTEGER NOT NULL CHECK (skipped_leased >= 0),
    bytes_read          INTEGER NOT NULL CHECK (bytes_read >= 0)
);
