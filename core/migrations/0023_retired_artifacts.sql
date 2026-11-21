-- Widen the artifacts.state CHECK to admit RETIRED, and record who retired
-- what (issue #1528).
--
-- WHY THIS EXISTS. A FAILED artifact whose source object is gone can never be
-- fetched again, and before this migration the only verb that touched one was
-- retry, which can only fail again. RETIRED is the state such an artifact is
-- moved to, reachable only from FAILED (core/internal/lifecycle), and this is
-- the same drift 0002 and 0006 fixed: the state machine and this CHECK are two
-- lists, and internal/state's TestJournalAcceptsExactlyTheStatesTheMachineDefines
-- catches them disagreeing.
--
-- SQLite cannot alter a CHECK constraint in place, so this recreates the
-- table exactly as 0006 did, with the same columns in the same order.
--
-- artifact_retirements is the record of each retirement, appended in the
-- same transaction as the FAILED -> RETIRED transition and never updated: an
-- artifact can be retired, come back when its source object reappears, and be
-- retired again, and each of those is a row. transition_key is the
-- idempotency key of the transition the row belongs to, so a replayed call
-- finds the row it already wrote. As in 0012, no vocabulary column carries a
-- CHECK: actor_kind (automatic, operator, agent) and source_check (absent,
-- present, unknown) are validated by internal/state before the write, and a
-- CHECK here would have to be widened by recreating this table, which is the
-- cost 0002 and 0006 already paid for artifacts.state.

CREATE TABLE artifacts_new (
    id                    INTEGER PRIMARY KEY,

    source                TEXT NOT NULL,
    backup_set            TEXT NOT NULL,
    artifact_name         TEXT NOT NULL,

    remote_path           TEXT NOT NULL,
    local_path            TEXT NOT NULL DEFAULT '',

    state                 TEXT NOT NULL DEFAULT 'DISCOVERED'
                          CHECK (state IN (
                              'DISCOVERED',
                              'TRANSFERRING',
                              'TRANSFERRED',
                              'VERIFYING',
                              'VERIFIED',
                              'COMMITTING',
                              'COMMITTED',
                              'REMOTE_DELETE_PENDING',
                              'COMPLETE',
                              'REMOTE_RETAINED',
                              'FAILED',
                              'QUARANTINED',
                              'QUARANTINED_LOST',
                              'RETIRED'
                          )),

    discovered_at         TEXT NOT NULL,
    updated_at            TEXT NOT NULL,

    remote_size           INTEGER,
    remote_mtime          TEXT,
    remote_hash           TEXT NOT NULL DEFAULT '',
    remote_hash_alg       TEXT NOT NULL DEFAULT '',
    remote_backend_id     TEXT NOT NULL DEFAULT '',

    transfer_bytes        INTEGER,
    transfer_checksummed  INTEGER NOT NULL DEFAULT 0 CHECK (transfer_checksummed IN (0, 1)),

    local_hash            TEXT NOT NULL DEFAULT '',
    local_hash_alg        TEXT NOT NULL DEFAULT '',
    validation_passed     INTEGER CHECK (validation_passed IN (0, 1)),
    validation_detail     TEXT NOT NULL DEFAULT '',

    retry_count           INTEGER NOT NULL DEFAULT 0,
    last_error            TEXT NOT NULL DEFAULT '',
    next_retry_at         TEXT,

    remote_deleted_at     TEXT,
    remote_delete_error   TEXT NOT NULL DEFAULT '',

    retention_tier        TEXT NOT NULL DEFAULT '',
    retention_expires_at  TEXT,

    UNIQUE (source, backup_set, artifact_name)
);

INSERT INTO artifacts_new SELECT * FROM artifacts;

DROP TABLE artifacts;

ALTER TABLE artifacts_new RENAME TO artifacts;

CREATE INDEX idx_artifacts_state ON artifacts (state);
CREATE INDEX idx_artifacts_backup_set ON artifacts (source, backup_set);

CREATE TABLE artifact_retirements (
    id              INTEGER PRIMARY KEY,
    artifact_id     INTEGER NOT NULL REFERENCES artifacts (id),
    transition_key  TEXT NOT NULL UNIQUE,
    retired_at      TEXT NOT NULL,
    actor_kind      TEXT NOT NULL,
    actor           TEXT NOT NULL DEFAULT '',
    source_check    TEXT NOT NULL,
    superseded_by   TEXT NOT NULL DEFAULT '',
    failure_reason  TEXT NOT NULL DEFAULT '',
    note            TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_artifact_retirements_artifact ON artifact_retirements (artifact_id);
