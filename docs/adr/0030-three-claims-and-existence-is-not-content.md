# ADR 0030: Three assurance claims, and why a class achieved without reading bytes is never content verification

- Status: accepted for the documents, and **depends on the code** in EPIC Q Phase 1 (#978 and
  #980 for the record and the runner, #982 for the configuration). Until those merge this
  describes specified behaviour.
- Date: 2026-10-10
- Scope: EPIC Q (#953), FR-70 and FR-72. Recorded by Q2.4 (#987).

## Context

A backup product shows one word about a copy and the operator reads it as one fact. The word
here was "verified", and it has been covering at least three very different amounts of
evidence:

- the journal holds a placement for the artifact (it is **stored**);
- a check read the bytes and they matched the recorded digest (**content** was verified);
- somebody recovered the newest good copy somewhere safe and their own validator accepted it
  (a **recovery was tested**).

The scrub that already exists (`core/internal/revalidate`) can only ever reach `existence`
against a storage medium when it runs unattended (`automaticMediumClass = placement.Existence`,
`core/internal/revalidate/checks.go:75`), and `existence` is a HEAD: it says an object of the
right size is at that key. `placements.verification_class` is already explicit that it records
"the strongest class this copy has actually ACHIEVED (FR-31), never the strongest one
configured" (`core/migrations/0007_placements.sql:51-63`).

There is a second pressure. Five different reasons produce "this is not a pass": never
configured, never run, overdue, refused for cost, and could not complete. A model with two
states, good and bad, renders four of them as good, and that is the failure this ADR exists
to prevent: nothing is green because nobody looked.

## Decision

1. **Three claims, three durable homes, three renderings.** Stored is the placement row. Content
   verified is the achieved class and the time a check that read bytes last passed. Recovery
   tested is its own set of columns. No surface collapses them into one tick, and a checksum
   pass with a validator failure reads exactly as `Content verified / Recovery test failed`.
2. **A class achieved without reading bytes is never content verification.** An `existence`
   result is recorded and rendered as an existence result, and it cannot move the content
   age. The unattended ceiling stays where it is, because raising it to `attested` would
   trust an endpoint's own checksum, which is a per-medium decision the scrub is not given.
3. **`unknown` is the zero value, and `unknown` is due.** An un-migrated copy, a copy a catalog
   rebuild just wrote and a copy a future code path forgot to fill all fall toward "owed a
   check", never toward "fine". The not-a-pass states are separate values, each with its own
   rendering, and a copy in `requires_restore` never counts toward a satisfied policy.
4. **A recovery test is a recovery, then a digest, then a validator.** It recovers the newest
   known good copy to an isolated path, requires the digest of what it read to equal the
   artifact's recorded digest, and only then runs the operator's validator through the
   existing untrusted-subprocess boundary. Reachability (a HEAD, a listing, a resolving URL, a
   completed thaw) is never a pass. A validator that exits 0 without opening the file does not
   produce one, because the digest gate is what the pass is recorded against.
5. **Journal truth stays journal truth.** A scrub's work list is `placements` rows, and a
   destination listing is only ever evidence about a row, never a source of rows.
6. **One vocabulary for two engines.** An `engine: kopia` set already has a verification
   ladder with a `restore_drill` rung. It is read as that set's recovery-test claim, and no
   second drill is run, because a second drill would fork the truth.
7. **This does not borrow from M or N.** A lock existing (EPIC M, #951) is metadata about
   retention, and ciphertext existing (EPIC N, #967) says nothing about whether it decrypts
   to the recorded plaintext. Every claim here is computable with both absent. With N present
   the plaintext digest match is the pass condition.

## Consequences

- The three claims need three columns' worth of storage per copy, and the migration that adds
  them deliberately backfills nothing: there is no honest time to put in, and putting one in
  would make every upgraded copy claim a check it never had.
- A user-visible honesty cost: a set with assurance off reads "not configured", not a tick.
  An operator who liked a dashboard that was all green will find fewer green things on it,
  and that is the intended outcome.
- The scheduled pass cannot tell an operator their data is intact. Only a content read can,
  and a content read of a chargeable copy needs somebody to agree to the cost.

## Alternatives considered

- **One rolled-up tick with a tooltip.** Rejected: the tooltip is where the information goes
  to be missed, and the roll-up is exactly where a checksum pass and a validator failure get
  averaged into green.
- **Raising the unattended ceiling to `attested`.** Rejected here; it is a trust decision per
  medium and not this pass's to make.
- **A second drill for incremental sets.** Rejected: see decision 6.
