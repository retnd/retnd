-- 0019_encrypt_existing_consents: the operator's opt-in to encrypt a retained
-- copy that already exists in the clear (#1291; follow-up to EPIC N, #967).
--
-- WHY THIS EXISTS. A plaintext copy on a medium under a backup set whose
-- retained-copy encryption is "required" is refused a medium-to-medium hop,
-- because the hop would stage and upload it in the clear. Encrypting it
-- instead is a different act: it reads every byte of a copy the operator
-- trusts today, writes a new one, and retires the old one. That must be
-- something an operator asked for, not something a policy change or a
-- retention cycle does on its own, and it must survive a crash, so the ask
-- is a durable fact and not a flag in the process that started it.
--
-- WHY IT IS BOUND TO A MOVE. The encrypted copy is made by an ordinary
-- journaled move (placement_moves), whose phases already carry the whole
-- safety argument: the source is retired only after the destination has been
-- decrypt-verified and recorded. The opt-in is one row naming THAT move. A
-- later ordinary move of the same artifact to the same medium is a different
-- move id and finds no row, so consent is never remembered per artifact or
-- per pair of mediums and a policy change can never turn into a rewrite.
--
-- WHY A SIDE TABLE. placement_moves is exactly as 0007 left it; a column on
-- it would make compat cell 03-migrated-schema non-additive. ABSENCE MEANS NO
-- OPT-IN, which is what every move written before this migration, and every
-- move a retention cycle plans, reads as.
--
-- NO SECRET. Nothing here is key material or a copy's content: a move id and
-- a time.

CREATE TABLE encrypt_existing_consents (
    move_id       INTEGER PRIMARY KEY REFERENCES placement_moves (id),
    consented_at  TEXT NOT NULL
);
