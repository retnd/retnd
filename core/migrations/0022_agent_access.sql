-- 0022_agent_access: the first principal that is not a person (EPIC U, #1345;
-- ADR 0034). Three tables, all new, nothing touched.
--
-- WHY ONE MIGRATION FOR THREE TABLES. The epic adds exactly one migration so
-- its closing check can say so. agent_tokens is filled by this migration's
-- pull request, agent_audit by U1.5 and agent_confirmations by U2.1; the
-- tables land together so no later pull request has to add a second file.
--
-- NO SECRET. agent_tokens.hash is the SHA-256 of a token that was shown once;
-- the plaintext is never stored, and prefix is its first eight characters,
-- enough for an operator to tell two tokens apart and for the store to find
-- the row to compare against. A token's status is read from this row on
-- every request, never cached past one.

CREATE TABLE agent_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL CHECK (name <> ''),
    prefix       TEXT NOT NULL CHECK (length(prefix) = 8),
    hash         TEXT NOT NULL CHECK (length(hash) = 64),
    scope        TEXT NOT NULL CHECK (scope IN ('read', 'operate', 'configure')),
    owner        TEXT NOT NULL CHECK (owner <> ''),
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);

-- A name is the agent:<name> principal an operator reads in the feed, so it
-- is unique among the tokens that can still authenticate. A revoked token
-- frees its name.
CREATE UNIQUE INDEX agent_tokens_live_name ON agent_tokens (name) WHERE revoked_at IS NULL;
CREATE INDEX agent_tokens_prefix ON agent_tokens (prefix);

-- Filled by U2.1 (confirmations). The columns are the spec's section 12.
CREATE TABLE agent_confirmations (
    id         TEXT PRIMARY KEY,
    token_id   TEXT NOT NULL REFERENCES agent_tokens (id),
    tool       TEXT NOT NULL,
    route      TEXT NOT NULL,
    method     TEXT NOT NULL,
    body       BLOB NOT NULL,
    command    TEXT NOT NULL,
    backup_set TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'denied', 'expired')),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    decided_at TEXT,
    decided_by TEXT
);

CREATE INDEX agent_confirmations_pending ON agent_confirmations (token_id, state);

-- One row per agent call. U1.5 writes the calls; this issue writes the
-- refusals the authenticator makes. token_id is empty when the presented
-- token matched no row, which is itself a fact worth keeping.
CREATE TABLE agent_audit (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    at              TEXT NOT NULL,
    token_id        TEXT NOT NULL DEFAULT '',
    tool            TEXT NOT NULL DEFAULT '',
    transport       TEXT NOT NULL,
    arguments       TEXT NOT NULL DEFAULT '',
    status          INTEGER NOT NULL,
    code            TEXT NOT NULL DEFAULT '',
    correlation_id  TEXT NOT NULL DEFAULT '',
    confirmation_id TEXT NOT NULL DEFAULT ''
);

CREATE INDEX agent_audit_token ON agent_audit (token_id, seq);
