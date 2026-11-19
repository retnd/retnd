-- 0020_encryption_key_suites: which encryption suite each key belongs to
-- (EPIC T, #1312; ADR 0033 D3).
--
-- WHY THIS EXISTS. Until EPIC T a key was an age X25519 identity by
-- construction and its suite was implicit. With a second suite (age-pq/v1,
-- #1313) a key's suite is the one thing that says which copies it can write
-- and open, so it is recorded, durably, beside the key.
--
-- WHY A SIDE TABLE AND NOT A COLUMN. ADR 0033 D3 asked for
-- `encryption_keys.suite_id` and an index recreated per suite, with this
-- table as the fallback if compat cell 03-migrated-schema refused that. It
-- does: the cell is additive-only over sqlite_master, ALTER TABLE ... ADD
-- COLUMN rewrites the stored CREATE TABLE text of encryption_keys, and
-- dropping idx_encryption_keys_one_active_managed removes a line an existing
-- deployment has. So encryption_keys and its index stay exactly as 0015 left
-- them, and the suite lives here.
--
-- WHAT THAT MEANS FOR THE ACTIVE-KEY RULE. 0015's index still allows one
-- active managed key deployment-wide, whatever its suite. That is stricter
-- than "one per suite" and it is what ADR 0033 D3's flow produces anyway:
-- changing the suite is a rotation into it, which supersedes the old suite's
-- active key in the same write, so there is always exactly one key new
-- copies go under. internal/state enforces the suite vocabulary on the write
-- path (an id copycrypt's registry does not have is refused), the way
-- keycustody's registry.go enforces the key status vocabulary.
--
-- ABSENCE MEANS age/v1. Every key written before this migration is an age
-- X25519 identity, so a key with no row here reads as 'age/v1', in one place
-- (internal/state's encryptionKeyColumns). No backfill runs and no existing
-- row changes meaning. Every key written after it gets a row in the same
-- transaction as the key itself.
--
-- NO SECRET. A key id and a suite id, both safe to render anywhere.

CREATE TABLE encryption_key_suites (
    key_id   TEXT PRIMARY KEY REFERENCES encryption_keys (id),

    -- suite_id is a copycrypt suite id ('age/v1', 'age-pq/v1'). Not CHECKed
    -- here for the reason 0012 and 0013 give: widening a CHECK in this
    -- journal is a table rebuild, widening a Go table is a line and a test.
    suite_id TEXT NOT NULL CHECK (suite_id <> '')
);
