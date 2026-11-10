# EPIC T: Encryption Suite Agility for Retained Copies

## Status

**Type:** EPIC / Detailed implementation specification
**Repository:** `retnd/retnd`
**Parent / predecessor EPICs:** EPIC N (#967, encrypted retained copies and key custody, which this epic extends), EPIC E (#232, storage mediums and the FR-31 verification ladder the encrypted copy rides on)
**Primary implementation root:** `core/`
**Tracker issue:** #1310 (sub-issues #1311 through #1320; this specification is #1311)
**FR numbering:** this specification claims **FR-83 through FR-90**. FR-1 through FR-24 are in `docs/EPIC.md`; FR-25 is an unclaimed hole and stays one; FR-26 is the `version` command; FR-27 through FR-35 are EPIC E's; FR-36 through FR-48 are EPIC L's; FR-45 through FR-50 are claimed by EPIC O's open spec PR #904; FR-51 through FR-58 are EPIC M's; FR-59 through FR-66 are EPIC N's; FR-67 through FR-74 are EPIC Q's; FR-75 through FR-82 are reserved to EPIC P (#955) in prose by the N and Q specifications. The next free number after this block is **FR-91**. Nothing here renumbers an existing FR.
**Measured on:** `main` at `477aec5a` (the merge of #1302, which put the whole EPIC N stack on `main`), read on 2026-11-10. #1310 was written against `feat/retroactive-encryption` and `bc085e06`; every `path:line` below was re-read at `477aec5a` and section 2 says where a number moved.

**Reserved numbers.** One ADR, **`0033`**. `docs/adr/` ends at `0030-three-claims-and-existence-is-not-content.md` and has no `0031` or `0032`, but both are reserved to EPIC P in prose (`docs/EPIC-N-encrypted-retained-copies.md` section 9 and `docs/EPIC-Q-recovery-and-assurance.md` section 11), and this repository honours a reservation in prose the way EPIC N honoured `0024` to `0026`. #1310 says "ADR 0031 (0033 if EPIC P lands 0031/0032 first)"; I read the reservation as already landed for that purpose, so the ADR is `docs/adr/0033-encryption-suite-agility.md` and every name in this document uses `0033`. One migration, **`0020`**: `core/migrations/` ends at `0019_encrypt_existing_consents.sql`, `0017` is EPIC P's, and `loadMigrations` refuses a duplicate version and nothing else (`core/internal/state/migrate.go`).

This document was written before any of the implementation exists, which is the order `docs/epic-checklist.md` section 1 asks for. The matrix, `docs/conformance/epic-t-matrix.md`, has every row `BLOCKED`, citing the epic (#1310) and naming the sub-issue it waits on.

---

# 1. Purpose

EPIC N gave retnd one encryption format: age v1 with one X25519 recipient, chosen by ADR 0027 and recorded per copy as `format_id = "age/v1"`. That was the right first step and it left three things undone: a choice of suites with a default I can defend in writing, a way to add a suite and retire an old one without orphaning a copy, and a plain answer to "how early can encryption happen relative to the moment retnd first sees a file".

This epic does four things and holds one line.

- The suite becomes a recorded, enumerated property of every encrypted copy, written by the committing attempt from a compile-time registry and never from config text.
- The build names one default suite, `age-pq/v1` (age v1, ML-KEM-768 + X25519 hybrid recipient), and every surface that names it prints the one sentence the code states about it.
- A key belongs to one suite, the active managed key is per suite, and a policy that cannot be honoured refuses the copy before any destructive step instead of downgrading.
- A suite has a lifecycle (supported, deprecated, retired) that keeps every historical copy readable while new copies move to the active suite.
- The line it holds: **I design no cryptography here.** Every suite is an established library's construction and that library's reader. No envelope, no chunk framing, no KDF, no nonce scheme of this project's own.

---

# 2. Current state, measured on `main` at `477aec5a`

| Surface | What is actually there | The gap |
|---|---|---|
| **The format id** | `copycrypt.FormatID = "age/v1"`, `FormatVersion = 1` (`core/internal/copycrypt/copycrypt.go:70-71`). The bytes are a plain age v1 stream from `age.Encrypt(out, rcpt)` with one X25519 recipient (`copycrypt.go:219`), parsed by `age.ParseX25519Recipient` inside `EncryptToStaging` (`copycrypt.go:191`) | A package constant. Nothing enumerates formats, nothing says which one is the default, and the encrypt path can only parse an X25519 recipient |
| **The custody seam** | `keycustody.Format` (`core/internal/keycustody/format.go:14`): `ID`, `Generate`, `Recipient`, `Open`, deliberately no `Seal`. `AgeFormat{}` (`core/internal/keycustody/age.go:23`) is the only implementation, `AgeFormatID = "age/v1"` (`age.go:13`) | Already the right interface. It has no version, no status, no description and no encrypt half, and `copycrypt` does not use it at all |
| **The copy record** | `placement_encryption.format_id TEXT NOT NULL CHECK (format_id <> '')` (`core/migrations/0015_retained_copy_encryption.sql:118`). The vocabulary is free text | No schema change is needed for a second suite id. `state.PlacementEncryption.validate` (`core/internal/state/encryption.go:349`) accepts any non-empty id, so nothing refuses an id no build can read |
| **The key registry** | `encryption_keys(id, provider, recipient, status, created_at, retired_at)` (`0015_retained_copy_encryption.sql:63`) and `idx_encryption_keys_one_active_managed ON (provider) WHERE provider = 'managed' AND status = 'active'` (`:98`) | No suite column. One active managed key deployment-wide, so a second suite has nowhere to put its key and a key's suite is implicit |
| **Key resolution in the engine** | `Engine.activeKey` (`core/internal/placement/encryption.go:112`) takes "the" active managed key from `Keys.Active` (`:134`, `core/internal/keycustody/store.go:136`) | "Whatever key is active", not "the active key of the policy's suite" |
| **What the copy records** | `verifyEncrypted` writes `FormatID: copycrypt.FormatID, FormatVersion: copycrypt.FormatVersion` (`placement/encryption.go:449`) | A constant, not the suite that produced the bytes |
| **The bundle** | `retnd-key-bundle/1` (`core/internal/keycustody/bundle.go:22`) carries `format`; `Bundle.Decrypt` refuses a mismatch (`bundle.go:198`) and so does `Store.Import` (`bundle.go:266`) | Already suite-shaped. The instructions are the age ones for every key |
| **The sidecar** | `recovery.ManifestEncryption{FormatID, FormatVersion, ...}` (`core/internal/recovery/manifest.go:225`), written by `placement/encryptedsidecar.go:83`; `catalog rebuild` reports `FormatID` (`core/internal/app/catalog.go:466`) | No notion of an unknown suite |
| **Config** | `retained_copy_encryption: {mode, key_provider}` (`core/internal/config/retainedcopyencryption.go:152`), resolved `RetainedCopyEncryption{Mode, KeyProvider}` (`:177`). `docs/encrypted-copies.md:87-88`: "There is no cipher, nonce length, chunk size, KDF cost or algorithm setting at either level, and none is planned". ADR 0027 (`docs/adr/0027-streaming-aead-format-and-the-upload-seam.md:131`): the format is a "property of the build, never a config field" | This epic reverses those two sentences and no other |
| **The library** | `filippo.io/age v1.3.2` direct and `filippo.io/hpke v0.4.0` indirect (`core/go.mod:6`, `:23`). Since v1.3.0 age ships a native `mlkem768x25519` hybrid recipient (`age1pq1...`, `AGE-SECRET-KEY-PQ-1...`) inside the same age v1 container the stock CLI reads | A second suite needs no new module. The age file key is 16 bytes, so every age suite is a 128-bit security level construction whatever the payload cipher |
| **The `age` CLI pin** | `scripts/ci/pins.env` pins no `age` binary today | #1310 and #1313 speak of "the `age` binary pinned in `scripts/ci/pins.env`". It does not exist yet; T1.3 adds the pin, and the external-reader vector skips (not passes) where the binary is absent |
| **Metrics** | `retained_copy_encryption_enabled` and `retained_copy_encryption_degraded` (`core/internal/metrics/metrics.go:645-646`) | There is no `retnd_encrypted_copies` family for a `suite` label to join; T1.5 adds one copy-count family with a `suite` label |
| **Where retnd first sees a file** | Discovery lists, `lifecycle.Transfer` pulls plaintext into `<name>.partial` through rclone `operations.Copy`, verify reads it back, commit keeps a plaintext local copy, and in the same cycle `RunHomeMoves` (`core/internal/app/cycle.go:371`) encrypts and uploads | Encryption enters after the plaintext local copy is verified. Earlier is a product-definition change (ADR 0033 D9) |
| **The gate** | `scripts/ci-local.sh` is the only gate (#1260) | A new check is a `gate_step` with a stub in `scripts/tests/ci-local-gate.test.sh` |

What moved since #1310 was written: the EPIC N stack merged (#1302 at `477aec5a`), so "measured on `feat/retroactive-encryption`" is now `main`; the record line the epic cites as `placement/encryption.go:380` is `:449`; and the ADR is `0033`, not `0031`, for the reason in the Status block.

---

# 3. Adversarial review, five-expert panel

Five lenses for what this feature is: a cryptographic choice presented to operators, an interface change on the data path, a schema change under a live key registry, an operator flow that mints keys, and a downgrade surface. Plus the skeptic, whose objections shaped the scope most.

## Expert 1, Applied Cryptography

### Initial verdict: REJECT

"Military-grade" has no standard behind it, and the obvious "stronger" default is a trap: age's file key is 16 bytes, so an age suite cannot honestly be called 256-bit, and ML-KEM-768 is NIST level 3, not CNSA 2.0's level 5. "AES-256-GCM from the standard library" means framing chunks, nonces and the final-segment flag myself, which is the bespoke construction EPIC N forbade. Mixing PQ and non-PQ recipients in one file is discouraged by the age spec.

### Consensus position: APPROVE AFTER REVISION

The default is `age-pq/v1`, and the only text about any suite is its `Describe()` sentence, which states the security level (128-bit), says "not AES, not FIPS-validated" and never says "military" or "256-bit". One recipient type per suite. The Tink AES-256 streaming suite is the only candidate that can carry "AES-256" without a bespoke container, and it is an owner decision (D2), not a default.

## Expert 2, A Go Interface on the Data Path

### Initial verdict: REJECT

`keycustody.Format` lacks `Seal` on purpose ("custody never encrypts"). Bolting `Seal` onto it breaks that rule; adding a second interface beside it gives two names for one thing. #1311's names table says `copycrypt.Suite` "embedding `keycustody.Format`", and #1312 says `keycustody.AgeFormat` becomes an alias of a type in `copycrypt`. Both together are an import cycle: `copycrypt` would import `keycustody` for the embedding and `keycustody` would import `copycrypt` for the alias.

### Consensus position: APPROVE AFTER REVISION

`copycrypt` stays a leaf package (it imports age and the standard library, nothing of this repository). `copycrypt.Suite` declares `keycustody.Format`'s four methods itself, plus `Version`, `Status`, `Describe` and `Seal`; `keycustody` asserts at compile time that every `copycrypt.Suite` is a `Format` (`var _ Format = copycrypt.Suite(nil)`), and `keycustody.AgeFormat` becomes an alias of the `copycrypt` adapter. Custody is handed a `Suite` but only ever calls the `Format` half, so "custody never encrypts" holds by what custody calls, stated in `format.go`. A new suite is one file and one table row, and a golden test pins the table.

## Expert 3, A Schema Change Under a Live Key Registry

### Initial verdict: REJECT

The unique index that makes "one active managed key" true is the only thing standing between a crashed rotation and two active keys. Dropping and recreating it is a window, and compat cell `03-migrated-schema` may read an index drop as non-additive.

### Consensus position: APPROVE AFTER REVISION

Migration `0020` adds `encryption_keys.suite_id TEXT NOT NULL DEFAULT 'age/v1'`, drops the index and recreates it over `(provider, suite_id)` with the same partial predicate, inside the migration's one transaction (so there is no window a writer can see). Every existing key reads `age/v1`, which is what it is. T1.2 checks the compat cell first and, if it refuses an index drop, falls back to the side table `encryption_key_suites(key_id PRIMARY KEY, suite_id)` with the per-suite rule in `state` on the write path, and says which in its pull request. `state` refuses a key or a copy record naming a suite the registry does not know.

## Expert 4, An Operator Flow That Mints Keys

### Initial verdict: REJECT

Every suite change is a new managed key and therefore a new unconfirmed bundle. A select that silently mints keys creates exactly the custody debt EPIC N spent an epic avoiding, and a new build whose default moved must not mint one behind the operator's back.

### Consensus position: APPROVE AFTER REVISION

No auto-rotation. A policy whose suite has no active key refuses the copy with "no active key for suite X; run `retnd encryption key rotate --suite X`", health carries it, and the policy patch answers `rotation_needed: true` with the exact command and writes nothing to the key store. The first cycle under a build whose default moved emits one `EncryptionDefaultSuiteChanged` event naming both ids, once. Rotation across suites supersedes the old suite's active key in the same durable write.

## Expert 5, A Downgrade Surface

### Initial verdict: REJECT

Each place a suite id is chosen is a place a weaker one can be substituted: the policy, the key, the copy record, the read side, the bundle, the sidecar and the surfaces.

### Consensus position: APPROVE AFTER REVISION

ADR 0033 D7 lists the seven and each gets a guard in section 6: an unknown or retired id refused at `Load`; a copy written only under the active key of the policy's suite; the record's id written from the registry and checked by `state`; the read side opening with the suite the record names; key import refusing an unknown suite; rebuild reporting "unknown suite" rather than plaintext or verified; and `Describe()` as the only sentence about a suite.

## The skeptic: keep age v1, add one suite, change nothing else

1. "A registry for two suites is ceremony." Accepted in part: the registry is a compile-time table over an interface that already exists. No dynamic loading, no `algorithm: auto`.
2. "A per-set suite is a per-set key and a per-set bundle." Accepted: the suite is global; the resolved field exists on `BackupSet` by inheritance only.
3. "Tink drags protobuf in for a suite nobody asked for." Accepted: Tink is not in this epic; its id is reserved.
4. "Encrypt-at-ingest buys nothing while the local copy is plaintext by definition." Accepted; ADR 0033 D9 says so and names the ciphertext-only local copy as a separate epic (D10).
5. "Deprecation machinery before anything is deprecated." Partly accepted: the status vocabulary and the write refusal are three constants and one check; re-encryption reuses #1291's machinery.

## 3.6 Consensus

| ID | Finding | Required change |
|---|---|---|
| TC-01 | No standard stands behind "military-grade", and age suites are 128-bit | `Describe()` is the only text about a suite; it states the level and contains neither "military" nor "256-bit"; a test pins it |
| TC-02 | An embedding plus an alias is an import cycle | `copycrypt` stays a leaf; `Suite` declares `Format`'s methods; `keycustody` asserts the assignment at compile time |
| TC-03 | The one-active index is the rotation's crash guard | Recreate it per suite inside migration `0020`'s transaction, or the side table; the compat cell decides, the pull request says which |
| TC-04 | A suite change mints a bundle to confirm | No auto-rotation; refusal with the rotate command; `rotation_needed` on the patch; one event when the build default moves |
| TC-05 | Seven places can substitute a weaker suite | One guard per place, each with a planted violation in section 6 |
| TC-06 | One vector suite, or every suite tests itself differently | `copycrypt/suitetest` runs the same groups over `Suites()`, with a planted truncation-accepting adapter as its anti-vacuity control |

---

# 4. Functional Requirements

## FR-83, The suite is recorded per copy

The suite is a recorded, enumerated property of a stored copy, written by the committing attempt from the registry's constants and never from config text.

## FR-84, One default, stated from code

The build names exactly one default suite, states its properties from code on every surface that names it, and never prints a claim the code cannot state.

## FR-85, Keys belong to suites

A key belongs to one suite; the active managed key is per suite; a copy is written only under the active key of the policy's suite.

## FR-86, Fail closed, never downgrade

A suite policy that cannot be honoured refuses the copy before any destructive step and is never substituted with a weaker suite.

## FR-87, Lifecycle

A deprecated suite keeps reading and writing with a degraded health state; a retired suite reads and never writes; no suite is removed while a copy records it.

## FR-88, Re-encryption orphans nothing

Re-encrypting a copy under the active suite retires the old copy only after the new one decrypt-verifies, and never puts plaintext on local disk.

## FR-89, Parity

Every suite surface has a CLI verb, an API route and a web control, with echoes, refusals and exit codes in parity.

## FR-90, One vector suite

One conformance vector suite runs against every registered suite, red first, and a new suite is one adapter plus one table row.

## Core invariants

- `age/v1` keeps its exact meaning for ever: age v1, one X25519 recipient, ChaCha20-Poly1305 payload. A suite id once written is never renamed.
- A configuration that writes nothing behaves as EPIC N does, except that a newly minted managed key is minted under the build's default suite.
- The suite that opens a copy is the one its record names, never the policy's.
- No key material on any suite surface, and a suite id (an enumerated string) is the only new metric label.

---

# 5. Phases, sub-issues and gates

Dependency order: T1.1 > T1.2 > T1.3 > T1.4 > T1.5; T2.1 and T2.2 on T1.5; T2.3 on T2.1; T2.4 and T2.5 last. Every pull request is stacked on its predecessor's branch and carries the "merges together" line.

## Phase 1, the registry, the per-copy suite, the default and the policy (`T:phase-1`)

- **T1.1 #1311, this specification, the matrix and ADR 0033** (FR-83, FR-84, FR-90). Section 1 of the checklist; no code.
- **T1.2 #1312, the suite registry, the recorded suite, keys per suite (migration `0020`)** (FR-83, FR-85, FR-90). Section 7 (migration, journal truth), section 12 (a self-test per new refusal).
- **T1.3 #1313, the second suite `age-pq/v1`, the shared vectors and the default** (FR-84, FR-90). Section 11 (no new module, stated), section 12 (the planted adapter).
- **T1.4 #1314, the global `suite` policy** (FR-85, FR-86). Section 2 (global field, re-resolve after a global mutation, fixtures `11-`, `64-invalid-`, `65-invalid-`, the `KnownFields(true)` door), section 4 (one event with a `Result`).
- **T1.5 #1315, CLI and API parity** (FR-89, FR-85, FR-84). Section 3 (both directions), section 6 (contract and both bindings), section 11 (no key material on any new surface).

### Phase 1 entry gate

- [x] FR-83 to FR-90 collide with nothing: `git grep -E 'FR-(8[3-9]|90)'` over `main` at `477aec5a` finds no claim.
- [x] `core/migrations/` has no `0020`, and `docs/adr/` has no `0031` to `0033`; `0031`/`0032` are EPIC P's in prose, so this epic takes `0033`.
- [x] EPIC N is on `main`, including #1291 (retroactive encryption) and #1289 (`retnd encryption decrypt`, key import).
- [ ] This specification and `docs/conformance/epic-t-matrix.md` are merged with every row `BLOCKED`. That is #1311.

### Phase 1 exit gate

- [ ] `copycrypt.Suites()` lists `age/v1` (supported) and `age-pq/v1` (default); a test pins the table and that exactly one default exists.
- [ ] Every row in `placement_encryption` written by this build carries the suite id the registry's constant names; planting a config string there turns a row red.
- [ ] A copy encrypted under `age-pq/v1` decrypts with the stock `age` CLI pinned in `scripts/ci/pins.env` using the exported bundle's material, and an `age/v1` identity cannot open it.
- [ ] Every key carries a suite (migration `0020`'s `encryption_key_suites`, written in the key's own transaction), every pre-migration key reads `age/v1`, a key under an unregistered suite is refused, and two active managed keys are impossible whatever their suites while a rotation across suites is one write.
- [ ] `suite: nonsense` and `suite: <retired>` fail `Load`; `suite: default` and a known id load; a config that writes nothing is byte-identical in behaviour to EPIC N.
- [ ] A `required` set whose policy suite has no active key refuses the copy before `SOURCE_DELETE_PENDING`, the local copy is untouched, health shows the reason, and nothing is written under another suite.
- [ ] `retnd encryption suites [--json]`, `GET /api/v1/encryption/suites`, `policy` showing the suite, `key create|rotate --suite` exist with `cliecho` entries, regenerated `openapi.json`, and the route-to-verb guard green.
- [ ] The shared vector suite is red against a planted broken adapter (truncation accepted) and green against both shipped suites.

## Phase 2, replacing a suite, and the surfaces (`T:phase-2`)

- **T2.1 #1316, suite lifecycle in the engine and health** (FR-87).
- **T2.2 #1317, re-encrypt copies under the active suite and key** (FR-88).
- **T2.3 #1318, web UI: suite select, rotate-then-confirm, per-copy chip, health, contrast measured** (FR-89, FR-84). Section 5 (design before UI), the contrast rule.
- **T2.4 #1319, e2e, equivalence, crypto-negative and recovery tests across two suites, with the pin** (FR-90).
- **T2.5 #1320, docs, CHANGELOG, captures and the gate step; the matrix with no `BLOCKED` row** (FR-84).

### Phase 2 entry gate

- [ ] The phase 1 exit gate is green and its matrix rows are `PASS`.
- [ ] The design for the Settings suite select and the rotate-then-confirm flow is reviewed before any UI code (checklist section 5).

### Phase 2 exit gate

- [ ] Marking a suite deprecated in the table turns health degraded with the count of active copies under it; marking it retired makes `Load` refuse the pinned id and the engine refuse a write, while decrypt-verify, recover and `decrypt` still work on its copies.
- [ ] `retnd encryption reencrypt --suite <id> [--dry-run]` lists and re-encrypts copies under another suite through the #1291 path; the source is retired only after decrypt-verify; a kill at each phase boundary reconciles.
- [ ] The Settings card shows the suite, its `Describe()` sentence and status, and changing it leads through rotate, export and confirm; per-copy chips show the suite id; every new text pair meets WCAG AA in both themes with ratios recorded.
- [ ] retnd-tests suites cover two suites end to end, the crypto negatives per suite, the cross-suite key refusal, rotation across suites, catalog loss and rebuild, and a clean-machine recovery per suite, and the pin moves in lockstep.
- [ ] `docs/encrypted-copies.md`, `recovery-without-a-terminal.md`, ADR 0027's one-line note, the CHANGELOG one-way door, captures and the `gate_step` are in; the matrix has no `BLOCKED` row and the canonical deployment is byte-identical.

---

# 6. TDD Contract

Every guard below lands red before the code that turns it green, and each has the planted violation that must make it fire. The matrix numbers them `TV.1` onwards, in this order.

| Guard | Planted violation that proves it fires |
|---|---|
| The suite table is pinned against a golden list | Change any suite's status or version without editing the golden list; `TestSuiteIDsAreStableAgainstTheGoldenList` must go red |
| Exactly one default, and it is supported | Mark a second suite default, or mark the default deprecated; `TestTheRegistryHasExactlyOneDefaultAndItIsSupported` must go red |
| The recorded suite comes from the registry, never config text | Write `cfg.Suite` text into `RecordPlacementEncryption`; `state` must refuse it with `ErrUnknownSuite` |
| An unknown suite on a record is never plaintext and never `content` | Treat an unknown `format_id` as `age/v1` on the read side; verify must report `ErrClassUnavailable` and rebuild "unknown suite" |
| One active managed key, whatever its suite | Drop `idx_encryption_keys_one_active_managed` or let a rotation across suites add before it supersedes; the second active key must be refused |
| A key names a registered suite | Hand `AddKey` an unregistered suite; it must be refused |
| A key of another suite is refused by `Open` and `Recipient` | Make one suite's adapter accept the other suite's identity; vector group 3 must go red |
| The shared vectors catch a truncation-accepting adapter | Plant an adapter whose reader returns EOF at a chunk boundary; vector group 2 must go red |
| `Describe()` carries no marketing words | Add "military-grade" or "256-bit" to a sentence; `TestDescribeSentencesCarryNoMarketingWords` must go red |
| An unknown or retired policy suite fails `Load` | Make `Validate` accept any non-empty id; fixtures `64-invalid-` and `65-invalid-` must load and the test must fail |
| No active key for the policy suite refuses before any destructive step | Fall back to any active managed key; `TestAnActiveKeyOfAnotherSuiteIsNotASubstitute` must go red and the crash cell must see a write |
| The default-changed event fires once per change | Emit it every cycle; `TestTheDefaultSuiteChangeEventFiresOnceAndOnlyOnAChange` must go red |
| Suite routes and verbs agree | Drop the `cliecho` entry for `GET /api/v1/encryption/suites`; the route-to-verb guard must name it |
| Key import refuses an unknown suite | Accept a bundle whose `format` names no registered suite; `TestKeyImportRefusesAnUnknownSuite` must go red |
| `decrypt` takes the suite from the record and refuses to guess | Default to `age/v1` with no sidecar and no record; `TestDecryptTakesTheSuiteFromTheRecordAndRefusesToGuess` must go red |
| No key material on any suite surface | Plant a canary identity in the suites response; the canary corpus row for that surface must go red |
| The `suite` metric label is an enumerated id | Label the copy-count family by key id; `TestNoKeyIdIsALabel` must go red |
| A retired suite never writes and still reads | Let the engine write under a retired suite, or refuse to open its copies; the T2.1 lifecycle test must go red either way |
| Re-encryption retires a source only after decrypt-verify | Retire the source before the new copy decrypt-verifies; the T2.2 kill-at-each-boundary cell must go red |

---

# 7. Checklist coverage map

| Checklist section | Where this epic meets it |
|---|---|
| 1, scaffold | This document, the matrix, ADR 0033; the checklist's next-free line moves to FR-91 |
| 2, config | T1.4: one global field, no per-set override, re-resolve after a global mutation, three fixtures, the `KnownFields(true)` door named in T2.5's CHANGELOG |
| 3, parity | T1.5 and T2.3: verb, route and control per surface, both directions |
| 4, events and progress | T1.4's `EncryptionDefaultSuiteChanged` with a `Result`; no new progress stage |
| 5, design before UI | T2.3 waits on a reviewed design |
| 6, contract and bindings | T1.5 regenerates `openapi.json` and both bindings |
| 7, durable state | T1.2's migration `0020` |
| 9, docs and ADR | ADR 0033 here; the docs it supersedes in T2.5 |
| 11, supply chain and secrets | No new module in phase 1 (stated in T1.3); the canary corpus extended in T1.5 |
| 12, guards and self-tests | Section 6, one planted violation per guard |

---

# 8. What I cut to fit two phases, and why

- **A ciphertext-only local copy** (`local_copy: ciphertext`). It is the real "encrypt at first sight", it reverses ADR 0027's rejection of option (c), it makes the key required to delete a source, and it touches nine plaintext readers. An epic of its own, after owner decision D10.
- **Encrypting in the same network pass as the pull.** Saves one disk read and gains no confidentiality while the local copy is plaintext; it would also replace rclone's `operations.Copy` on the production ingest path.
- **The Tink `AES256_GCM_HKDF_1MB` suite.** Reserved id `tink-aes256gcm-hkdf-1mb/v1`; shipped only if the owner takes D2(b).
- **Per-set suites, same-medium re-encryption, a `GOFIPS140` build mode, removing a retired suite from the binary.** Each needs a phase of its own and none is needed for the four requirements.

---

# 9. Cross-epic relations

| Tracker | Subject | FR block | Migration | ADRs |
|---|---|---|---|---|
| #967 EPIC N | encrypted retained copies and key custody | FR-59 to FR-66 | 0015, 0019 | 0027/0028 |
| #953 EPIC Q | recovery, scrubbing and assurance | FR-67 to FR-74 | 0016, 0018 | 0029/0030 |
| #955 EPIC P | removable, offline and air-gapped media | FR-75 to FR-82 | 0017 | 0031/0032 |
| #951 EPIC M | immutable / WORM retained copies | FR-51 to FR-58 | 0014 | 0025/0026 |
| **#1310 EPIC T** | encryption suite agility | FR-83 to FR-90 | 0020 | 0033 |

- **N.** This epic extends N and changes none of its names: `format_id` is the suite id, `age/v1` keeps its meaning, and ADR 0027 gets one superseding line in T2.5.
- **Q.** A recovery decrypts with the suite its record names; Q's "ciphertext is not a pass" contract is unchanged.
- **P.** An offline destination satisfies no `required` policy whatever its suite.
- **M.** Independent: a locked copy may be under any suite.

---

# 10. Non-goals

A ciphertext-only local copy. Per-set suites. Same-medium re-encryption. Any KMS or Vault provider. A FIPS build mode or any FIPS claim. Compression. Object-name confidentiality. Changing kopia-engine sets. Any cryptographic construction, envelope, KDF or nonce scheme of this project's own. Removing a suite from the binary. Renaming any FR, metric, event or route.

---

# 11. Compatibility and migration summary

- A configuration that never writes `suite:` resolves to the build's default and behaves as EPIC N does, except that a newly minted managed key is `age-pq/v1`. An existing active `age/v1` key is untouched and, with `suite: default`, the next copy is refused until the operator rotates (no auto-rotation).
- `suite:` under `KnownFields(true)` is a one-way door for a config that writes it; T2.5's CHANGELOG names the version.
- One forward migration, `0020`, additive: a column with a default and a recreated index. An older binary meeting it fails closed under `state.ErrUnknownSchemaVersion`.
- Suite ids and status values are compatibility surfaces: never renamed, never removed while a copy records them.
- A PQ recipient string is about 2000 characters and the `age-pq/v1` header about 1.4 KB. Fixtures and UI chips that assume a 62-character recipient or a 200-byte header move; `stored_size > plaintext_size` still holds.

---

# 12. Names the other issues depend on

| Thing | Name |
|---|---|
| Suite ids | `age/v1` (unchanged), `age-pq/v1`; reserved `tink-aes256gcm-hkdf-1mb/v1` |
| Status vocabulary | `supported`, `deprecated`, `retired` (`copycrypt.Status`) |
| Registry | `copycrypt.Suites()`, `copycrypt.Default()`, `copycrypt.Lookup(id)` |
| Interface | `copycrypt.Suite`: `keycustody.Format`'s four methods declared in `copycrypt` (no import), plus `Version()`, `Status()`, `Describe()`, `Seal()` |
| Adapters | `copycrypt.AgeX25519` (`age/v1`), `copycrypt.AgeMLKEM768X25519` (`age-pq/v1`); `keycustody.AgeFormat` is an alias of `copycrypt.AgeX25519` |
| Errors | `copycrypt.ErrUnknownSuite`, `state.ErrUnknownSuite`, `config.ErrRetiredSuite` |
| Vectors | `core/internal/copycrypt/suitetest` |
| Config | `retained_copy_encryption.suite` (`default` or an id); resolved `config.RetainedCopyEncryption.Suite` |
| Migration | `0020_encryption_key_suites.sql`: the side table `encryption_key_suites(key_id, suite_id)`, absence read as `age/v1` (#1312 found the column-and-index shape non-additive for compat cell `03-migrated-schema`) |
| Event | `EncryptionDefaultSuiteChanged` |
| Verbs | `retnd encryption suites`, `key create --suite`, `key rotate --suite`, `reencrypt --suite` |
| Routes | `GET /api/v1/encryption/suites`; `suite` and `suite_configured` on the policy read, `suite` on the patch |
| ADR | `docs/adr/0033-encryption-suite-agility.md` |

---

# 13. Decisions this scaffold took

- **ADR `0033`, not `0031`.** EPIC P's `0031`/`0032` are reserved in prose on `main`; see the Status block.
- **No embedding, no cycle.** #1311's names table says `Suite` embeds `keycustody.Format`, and #1312 says `keycustody.AgeFormat` aliases a `copycrypt` type. Both cannot hold, so `Suite` declares the four methods itself and `keycustody` asserts the assignment. The behaviour is the same and the "custody never encrypts" rule is stated where custody lives.
- **The `age` pin does not exist yet.** T1.3 adds it to `scripts/ci/pins.env`; until it does, the external-reader vector skips and its matrix row stays `BLOCKED`.
- **A side table, not a column (#1312).** Compat cell `03-migrated-schema` is additive-only over `sqlite_master`: `ALTER TABLE ... ADD COLUMN` rewrites the stored `CREATE TABLE` text of `encryption_keys`, and dropping the one-active index removes a line. So migration `0020` is the side table #1312 names as the fallback, and 0015's index stays, which keeps "one active managed key" deployment-wide. That is stricter than "one per suite" and is what ADR 0033 D3's flow produces anyway (a suite change is a rotation that supersedes the old suite's active key in the same write). The T1.4 exit line says so.
- **The fixture numbers.** #1314 names `10-encryption-suite-pinned.yaml`; `10-` holds EPIC N's `10-encryption-kopia-set-under-required.yaml`, so the valid fixture is `11-encryption-suite-pinned.yaml`. `64-` and `65-` are free (`63-invalid-encryption-required-without-provider.yaml` is the last invalid one).
- **The metric family does not exist yet.** T1.5 adds one copy-count family with a `suite` label rather than "a `suite` label on `retnd_encrypted_copies`".
- **The matrix guard.** I registered this pair in `core/tests/compat/matrix_test.go`, red, before the documents existed, as EPIC N did.

---

# 14. Definition of done

Both exit gates green, the matrix with no `BLOCKED` row and every `PASS` backed by a falsification that was run and watched to fail, `scripts/ci-local.sh` green with the new `gate_step`, and the canonical deployment byte-identical to what EPIC N ships. What this epic cannot prove from here is stated rather than rounded up: no post-quantum adversary is exercised, the `mlkem768x25519` stanza follows an Internet-Draft, and the clean-machine recovery is tested with the pinned `age` binary, not with every age release.
