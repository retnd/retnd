-- 0015_retained_copy_encryption: the key registry, the per-copy encryption
-- record with its stored-bytes digest, and the durable bundle confirmation
-- (EPIC N, N1.1, #989; FR-61, FR-63, FR-66).
--
-- WHY THIS EXISTS. Encrypting a retained copy needs three facts the journal
-- has nowhere to put. Which key a copy was written under, because a copy
-- that cannot name its key is not recoverable and a key cannot be retired
-- while a copy still needs it. The digest of the bytes ACTUALLY STORED,
-- because once a copy is ciphertext its bytes no longer hash to the
-- artifact's own digest. And whether the operator has confirmed holding a
-- disaster-recovery bundle for a managed key, because health.Report is
-- computed per call and a warning that vanishes on restart is not a
-- warning.
--
-- WHY A SIDE TABLE AND NOT COLUMNS ON placements. 0016's header has the
-- argument and this file leans on it rather than repeating it: placements is
-- exactly as 0013 left it, compat cell 03-migrated-schema stays purely
-- additive with no waiver, and placement_moves.source_placement_id still
-- references a table nothing rebuilt. The record is 1:1 with a copy and
-- optional, so it is a table of its own keyed by the placement, and ABSENCE
-- MEANS UNENCRYPTED: a copy written before this migration, a copy under a
-- set whose mode is off and a copy a future code path forgot all read the
-- same way, and nothing here backfills a row for any of them.
--
-- THE COMMENT THIS CORRECTS. 0007 says hash/hash_alg are "the content hash
-- recorded FOR THIS COPY". That file is frozen (migrate.go says why) so the
-- correction goes here: placements.hash has only ever held the ARTIFACT'S
-- digest, copied onto each copy by the move engine's destinationPlacement
-- and by lifecycle's measureCommitted. For a plaintext copy the two are
-- the same number and the sentence was harmless. For an encrypted copy
-- they differ, and writing the plaintext digest where the stored digest
-- belongs is #662's defect with a new cause. So the artifact's digest stays
-- in artifacts.local_hash and in placements.hash exactly as today, and the
-- stored-bytes digest is the NEW column below. internal/state refuses a
-- write that puts the same number in both.
--
-- NO KEY MATERIAL. Nothing in these three tables is secret. recipient is
-- the public half of a key (the string a sender needs, which reveals
-- nothing about the private half); the private half lives as a 0600 file
-- under the custody rule internal/secretref enforces, or outside this
-- product entirely for an external provider. A column for the material
-- itself does not exist and is not to be added: the journal is read by
-- support exports, compat captures and the recovery sidecar, none of which
-- may carry a key.
--
-- WHY THE VOCABULARIES ARE CHECKed. Same reason as 0016: each list is
-- closed on purpose, a status nobody named must not be storable, and
-- internal/state carries the Go copy with a test pinning the two together.
-- A new status is a migration and a deliberate edit, not a string.
--
-- THE NUMBER. 0014 is EPIC M's and is not in the tree; 0016 and 0018 landed
-- first. migrate applies any unapplied version whatever its order, so the
-- gap is safe, and this file depends on nothing but 0007's placements.

-- ---------------------------------------------------------------------
-- THE KEY REGISTRY (FR-63, FR-65)
-- ---------------------------------------------------------------------

-- One row per key this deployment has ever encrypted a copy under. A key is
-- never deleted: a copy records its key by id, and a retirement is a status
-- change (refused by internal/state while an ACTIVE copy still names the
-- key; that rule is #992's and the index below is what makes it cheap).
CREATE TABLE encryption_keys (
    -- id is the product's own stable identifier for the key, chosen at
    -- generation and never derived from the material.
    id          TEXT PRIMARY KEY,

    -- provider is who holds the private half: 'managed' for a key this
    -- product generated and keeps, 'external' for an operator's own
    -- file/env/command reference. The values are config's
    -- KeyProviderManaged / KeyProviderExternal.
    provider    TEXT NOT NULL CHECK (provider IN ('managed', 'external')),

    -- recipient is the public half, as the chosen format spells it (ADR
    -- 0027). It is what a clean-machine recovery procedure matches a bundle
    -- against, and it is empty for an external key the product has not yet
    -- resolved. Never the private half; see the header.
    recipient   TEXT NOT NULL DEFAULT '',

    -- status is 'active' (new copies may be written under it) or 'retired'
    -- (historical copies still decrypt through it, new ones are not
    -- written). A retired key has a retirement time and an active one has
    -- none; the CHECK below keeps the two columns from disagreeing.
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),

    created_at  TEXT NOT NULL,
    retired_at  TEXT,

    CHECK ((status = 'retired') = (retired_at IS NOT NULL))
);

-- ---------------------------------------------------------------------
-- THE PER-COPY ENCRYPTION RECORD (FR-60, FR-61)
-- ---------------------------------------------------------------------

-- One row per ENCRYPTED copy, keyed by its placement. No row is a plaintext
-- copy. ON DELETE CASCADE means the record dies with the copy it describes,
-- so a re-copy that writes different ciphertext at the same key can never
-- find an older attempt's digest standing beside it.
CREATE TABLE placement_encryption (
    placement_id    INTEGER PRIMARY KEY REFERENCES placements (id) ON DELETE CASCADE,

    -- format_id / format_version name the on-the-wire format the copy was
    -- written in, so a second format can be added later and told apart per
    -- copy rather than per deployment. The format is a property of the
    -- build (there is no algorithm knob), which is why it is recorded here
    -- and not configured anywhere.
    format_id       TEXT NOT NULL CHECK (format_id <> ''),
    format_version  INTEGER NOT NULL CHECK (format_version >= 1),

    -- key_id is the registry row the copy needs to be read back.
    key_id          TEXT NOT NULL REFERENCES encryption_keys (id),

    -- stored_hash / stored_hash_alg are the digest of the bytes AT THE
    -- KEY: the ciphertext, header included. This is the only column in
    -- the journal that holds a stored-bytes digest. It is written once, by
    -- the attempt that reached the durable phase advance, in the same
    -- write, and nothing ever compares it against a freshly encrypted
    -- object because ciphertext is not deterministic.
    stored_hash     TEXT NOT NULL CHECK (stored_hash <> ''),
    stored_hash_alg TEXT NOT NULL CHECK (stored_hash_alg <> ''),

    -- stored_size is the byte count at the key, which is strictly more
    -- than the plaintext for any authenticated format. A size equal to the
    -- plaintext's is the length-only fixed point the epic's fixtures are
    -- chosen to avoid, and internal/state refuses it.
    stored_size     INTEGER NOT NULL CHECK (stored_size >= 0)
);

-- "Does any ACTIVE copy still need this key" is the question a retirement
-- asks, and this is the read it takes.
CREATE INDEX idx_placement_encryption_key ON placement_encryption (key_id);

-- ---------------------------------------------------------------------
-- THE DURABLE BUNDLE CONFIRMATION (FR-66)
-- ---------------------------------------------------------------------

-- One row per key whose disaster-recovery bundle the operator has confirmed
-- holding. Absence is the degraded state: a managed key with no row here is
-- a deployment whose off-site copies are one disk failure from
-- unreadable, and health renders that for as long as the row is missing,
-- across restarts, because it is read from here and not computed.
CREATE TABLE recovery_bundle_confirmations (
    key_id        TEXT PRIMARY KEY REFERENCES encryption_keys (id),
    confirmed_at  TEXT NOT NULL,

    -- Who said so and which bundle they were holding, so a confirmation is
    -- a statement somebody can be asked about rather than a tick.
    confirmed_by  TEXT NOT NULL DEFAULT '',
    bundle_digest TEXT NOT NULL DEFAULT ''
);
