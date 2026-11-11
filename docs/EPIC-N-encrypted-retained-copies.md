# EPIC N: Encrypted Retained Copies and Recoverable Key Custody

## Status

**Type:** EPIC / Detailed implementation specification
**Repository:** `retnd/retnd`
**Parent / predecessor EPICs:** EPIC A (#1, the artifact lifecycle), EPIC E (#232, storage mediums and the FR-31 verification ladder), EPIC K (#779, the engine split)
**Primary implementation root:** `core/`
**Tracker issue:** #967 (sub-issues #989 through #998; this specification is #968)
**FR numbering:** this specification claims **FR-59 through FR-66**. FR-1 through FR-24 are in `docs/EPIC.md`; FR-26 is the `version` command; FR-27 through FR-35 are EPIC E's; FR-36 through FR-48 are EPIC L's; FR-45 through FR-50 are claimed by EPIC O's open spec PR #904; FR-51 through FR-58 are EPIC M's (#951, open). The next free number after this block is **FR-67**, which EPIC Q took while this document was waiting to be written: FR-67 through FR-74 are EPIC Q's on `main` today, so the org-wide next free number is **FR-75**, which EPIC P takes. FR-25 is an unclaimed hole and stays one. Nothing here renumbers an existing FR.
**Measured on:** `main` at `bc085e06` (the 0.7.2 release merge), re-read on 2026-11-09. #967 was written against `e5a7d918`, which is not on this `main`'s history any more (the repository moved under EPIC R), and the first pass of this document read `283fd457`. Between those two only the release, install-script and provenance files moved, so no `core/`, `apps/`, `ui/` or `api/` line cited below shifted, and I re-read every one of them at `bc085e06` anyway. Where a number moved or a claim stopped being true, section 2 says so.

**Reserved numbers.** Two ADRs, `0027` and `0028`: claimed, none released. `docs/adr/` ends at `0023-moving-the-repository-coordinates-once-and-last.md` with `0029` and `0030` already landed by EPIC Q, so `0024` (EPIC O's, #904), `0025`/`0026` (EPIC M's) and `0027`/`0028` (this epic's) are the gap between them and collide with nothing. One migration, `0015`: claimed and used, for the key registry, the per-copy encryption metadata and the bundle-confirmation fact. `core/migrations/` holds `0013_workflow_lifecycle.sql`, then `0016_copy_assurance.sql` and `0018_activity_events.sql` (EPIC Q's); `0014` is EPIC M's and `0017` is EPIC P's. `loadMigrations` refuses a duplicate version and nothing else (`core/internal/state/migrate.go`), so the gaps are safe.

This document was written before any of the implementation exists, which is the order `docs/epic-checklist.md` section 1 asks for. The matrix, `docs/conformance/epic-n-matrix.md`, has every row `BLOCKED`, each naming the sub-issue it waits on, because a row that went green in the pull request that wrote it would be the failure the matrix exists to prevent.

---

# 1. Purpose

An operator who sends retained copies to object storage they do not own has no way to make those copies unreadable to whoever holds the bucket. The artifact-engine copy path writes the producer's bytes verbatim, and the only encryption this product has ever written is a whole-buffer envelope around an imported SSH key (`core/internal/transport/rclone/keyencryption.go`). `docs/EPIC-E-alternative-storage.md` cut exactly this work with the right reason, that key custody is its own custody problem with its own recovery story (lose the key, lose every offsite artifact), and that bolting it on there would rush the one part that must not be rushed. This epic is the unrushed version.

It does five things and holds one line.

- Encryption becomes a property of a **stored copy** on the artifact-engine path, so one artifact can have a plaintext local copy and an unreadable off-site one, chosen by a deployment-wide default with a per-set `inherit | off | required` override nobody has to fill in.
- The artifact's plaintext digest and the digest of the bytes actually stored become two columns, and no copy is called verified until a streaming decrypt has produced the plaintext digest the journal already holds.
- Managed key material goes under the custody rule the tree already enforces, is never rendered back, and gets stable ids, timestamps and a status.
- The absence of a disaster-recovery bundle becomes a durable degraded state rather than a silent risk, and a key rotates without orphaning a single catalogued copy.
- The format and the library are chosen by ADR before any production code.
- The line it holds: **I design no cryptography here.** No bespoke container, no bespoke KDF, no hand-rolled nonce scheme. Established AEAD primitives through a maintained library named in ADR 0027.

---

# 2. Current state, measured on `main` at `bc085e06`

This table decided the epic's shape. The original draft was written against surfaces that either do not exist under the names it used or exist with contracts that actively resist what it proposed. Every claim has a `path:line` re-read on 2026-11-09.

| Surface | What is actually there | The gap |
|---|---|---|
| **The stored-copy record** | `placements` (`core/migrations/0007_placements.sql`): `medium`, `location`, `size_bytes`, `hash`/`hash_alg`, `verification_class` closed to `''`/`content`/`attested`/`existence` (`:60`), `verified_at`, `status` in `ACTIVE`/`DELETE_PENDING`/`GONE`, `UNIQUE (artifact_id, medium)` (`:81`). Go side `state.Placement` / `state.PlacementUpdate` in `core/internal/state/placements.go`, upserted `ON CONFLICT (artifact_id, medium) DO UPDATE`. | The schema comment at `0007_placements.sql:45-47` calls `hash` "the content hash recorded FOR THIS COPY", and **nothing fills it that way.** `destinationPlacement` (`core/internal/placement/engine.go:1068`) copies the source row's hash "since a copy is the same bytes under the same hash" (`:1065-1067`); `measureCommitted` (`core/internal/lifecycle/commit.go:418`) takes `rec.LocalHash` and only re-reads the file when the transferred byte count disagrees with `os.Stat`. `transport.UploadResult` carries `Key` and `BytesUploaded` and **no digest, by design** (`core/internal/transport/medium.go:197`). So there is no stored-bytes digest today, and the draft's invented `artifact_digest`/`stored_copy_digest` names have real homes: `artifacts.local_hash` (`core/migrations/0001_init.sql:63`) and `placements.hash`. |
| **Verification** | The FR-31 ladder, `core/internal/placement/ladder.go`: `Content` streams `OpenObject` into `sha256` and compares to `p.Hash` (`verifyContent`, called at `:222`); `Attested` asks the endpoint; `Existence` is a HEAD at the recorded size. `Class.CostsEgress()` is true only for `Content` (`:121`). `Class.Proves()` (`:141`) is the operator-facing sentence. `ObjectChecksum` against an s3 medium is an `UnsupportedCapability` refusal on rclone v1.75.0 (`core/internal/transport/medium.go:313`), so `attested` does not exist on the only medium type there is. Unattended passes are ceilinged at `automaticMediumClass = placement.Existence` (`core/internal/revalidate/checks.go:77`), asserted by `TestTheAutomaticCeilingIsAClassThisPassMayRunAgainstEveryMedium` (`core/internal/revalidate/automaticclass_test.go:58`). EPIC Q's scrub driver now runs this on a cadence, so the ceiling is load-bearing in production for the first time. | Over an encrypted copy, `Content` would compare ciphertext to ciphertext and report `verification_class: content`, proving the bytes are the bytes we uploaded and saying **nothing** about recoverability, while `Proves()` tells the operator something stronger in the product's own words. And a decrypt-verify *is* `Content`, so it cannot become the automatic pass: EPIC E already cut automatic content re-verification because silent egress is a surprise bill, and `checks.go:215` and `:261` are the refusals that keep it so. |
| **The write path** | Local: `.partial`, `fsyncFile`, `linkWithoutClobbering` (`core/internal/lifecycle/commit.go:752`, `os.Link` rather than `os.Rename`, so a final-name collision fails instead of replacing), `removeIfExists`, `fsyncDir`. Remote: `MediumStore.UploadFromLocal` (`core/internal/transport/medium.go:303`) addressed by a local **path** on purpose, "a reader would put this process in the middle of every byte for no gain" (`:262-265`), with no temp key, no staging object and no multipart commit seam, and a documented contract that it **converges** on an occupied key (`:267-302`) because `MediumKey` is deterministic (`:434`, FR-28). | Encryption requires this process in the middle of every byte, which is the one thing that interface was written to avoid. The reader-shaped seams that do exist are `artifactstore.Local.Put(ctx, locator, r io.Reader)` (`core/internal/artifactstore/local.go:206`) and `MediumStore.OpenObject`; the only hash-while-writing pass in the tree is `io.MultiWriter(tmp, digest)` at `core/internal/backupengine/kopia/localrestore.go:481`. Convergence's safety argument, "the only thing that can be sitting at the key is an earlier attempt at the SAME artifact", assumes deterministic bytes, which ciphertext is not. |
| **Encryption that already exists** | Two places, neither reusable as-is. `core/internal/transport/rclone/keyencryption.go` is a whole-buffer AES-256-GCM envelope with `encryptedKeyMagicV1`/`V2`, a 16-byte salt and a 12-byte nonce (`:38`, `:43`), chosen for an SSH key and justified "specifically so this project does not take on a new crypto dependency for one feature" (`:59`). The kopia engine's repositories are encrypted by kopia's own format inside its write session, and the adapter refuses a repository with no passphrase source because "every repository this adapter creates is encrypted" (`core/internal/backupengine/kopia/repository.go:714`). | The key-file envelope buffers the whole plaintext and puts one nonce over the whole message: correct for tens of bytes, wrong for a multi-TB artifact. The kopia path shares no code with the artifact copy path and writes nothing to `placements`. So a `kopia`-engine set (`model.EngineKopia`, `core/internal/model/engine.go:62`; `UsesRepository()` at `:136`) is already encrypted at rest under its repository passphrase, and what a `required` policy means for such a set has to be answered in writing rather than shipped as a knob. |
| **Secret custody** | One operator-facing shape: `secretref.Ref` with `File`/`Env`/`Command`, exactly one of three (`core/internal/secretref/secretref.go:160`, `Validate` at `:200`), custody refusals on mode `&0o077` (`:473`), a symlinked final component and a group- or world-writable ancestor (`checkSecretFileCustody`, `:466`, sticky-bit exception at `:522`), 64 KiB bound, `Resolve` returning `obs.Secret` (`:322`). `TestRefFieldSetMatchesTheConfiguredOnes` (`core/internal/secretref/ref_test.go:49`) fails if `config.MediumCredentials`, `config.Passphrase`, `config.SecretSource` or `transport.MediumCredentials` grows a fourth field. There is deliberately no `UnmarshalJSON` on `Ref` (`:266`). `obs.Secret` forecloses the rendering paths it can (`core/internal/obs/secret.go:131-157`). Product-written secrets: the 0600-in-0700 opaque-reference vault at `apps/common/auth/local/secrets.go` (`:90`). | There is no key **registry**: no id, no creation or retirement timestamp, no status, nowhere to record which key a copy needs. `apps/common/auth/local` cannot import `core/internal/secretref` because it is a different Go module (`secrets.go:33`), so a managed key store cannot live there. And `config.KeyEncryption` (`core/internal/config/config.go:214`) already owns the phrase "the key that encrypts secret material this manager persists", so a second key concept must not reach for that name. |
| **Degraded state and refusals** | `health.Report` is **computed and non-durable** (`core/internal/health/health.go:616`, `NewReport` at `:661`); `Report.Workflow` (`:642`) is the precedent for a section added as a field the caller assigns, whose zero value reads "this deployment runs no workflows". `alert.Kind` is **thirteen** values pinned by `TestKindsAreExactlyTheDeliberatelyChosenConditions` (`core/internal/alert/mechanism_test.go:172`), from `StaleBackup` (`core/internal/alert/alert.go:128`) to EPIC Q's four scrub and recovery-test kinds (`:314-327`); each is true-while-it-holds, resolves on success and re-alerts on recurrence. The destructive seams: `lifecycle.RetainRemote` (`core/internal/lifecycle/retainremote.go:70`) versus `lifecycle.DeleteRemote` (`core/internal/lifecycle/remotedelete.go:265`), `Reclaimer.DeleteFromMedium` (`core/internal/placement/reclaim.go:101`), the `SOURCE_DELETE_PENDING` phase (`core/internal/placement/phases.go:23`). | **There is no `health.Warning` type, no warning-kind enumeration and no durable operator-facing notice record anywhere** in `core/service`, `apps/common/webhost` or `ui/shared/src/types`. A degraded condition today is a computed `health.State`, an in-memory `alert.Condition`, or a log line, none of which survives a restart. "A persistent warning until a recovery bundle is confirmed" therefore needs a durable fact, which is why this epic needs a migration at all. The issue said nine alert kinds; it is thirteen now, and the argument against growing the list is the same at thirteen. |
| **Cancellation** | `lifecycle.Transfer` on a `transport.Cancelled` records **nothing**: the row stays `TRANSFERRING` and the `.partial` is left on disk, cleared by the unconditional `os.Remove(partial)` before the next copy (`core/internal/lifecycle/transfer.go:419`); `core/internal/reconcile/reconcile.go:98-108` deliberately takes no action on the five pre-commit states. Remote: `Engine.discardDestination` (`core/internal/placement/engine.go:1414`) via `abandon` (`:1382`); staging: `sweepStagingTemps` (`core/internal/placement/staging.go:485`) over `stagingTempPrefix = ".artifactstore-"` (`:119`). Crash cells: `core/tests/crashmatrix/crash_matrix_test.go:738` and `:951`; `core/tests/movecrash/cells_test.go:324` with the anti-vacuity control at `:504`. | The cleanup story is sound and needs extending, not replacing. But there is **no memory or RSS assertion anywhere on this path**: not over `lifecycle.Transfer`, `Verify`, `Commit`, `readAndHashLocal`, `placement.verifyContent`, `artifactstore.Local.Put` or `UploadFromLocal`. The only comparable budget in the tree is `core/internal/backupengine/source/adapter_test.go:1055` (2 GiB streamed against `allocBudget = 256 << 20`, failing with "the object is being copied rather than streamed"). EPIC Q's recovery export added a second one for downloads, which is a shape to copy, not a cover for this path. |

What moved since #967 was written, beyond the table: EPIC Q landed in full (#953 closed), so the fixture numbers the issue hands this epic are taken (`07-` and `59-` are Q's assurance fixtures; this epic's are `09-` valid and `62-invalid-`, with `63-` the next invalid number after that; the valid fixtures run `01-` to `08-` and the invalid ones `50-` to `61-` on `main` today), the release gate's `needs` list the issue names no longer exists (#1260 made `scripts/ci-local.sh` the only gate, so the new check is a `gate_step` there), and the checklist's next free FR is `FR-75`, not `FR-49`.

---

# 3. Adversarial review, five-expert panel

Five lenses, chosen for what this feature actually is: a cryptographic format choice, a key-custody lifecycle, a streaming transform on the data path, a disaster-recovery procedure a human performs under stress, and a leak surface. All five returned REJECT first. The findings below are release requirements, not optional hardening, and each gets a row in `docs/conformance/epic-n-matrix.md` with the mutation that must turn it red.

## Expert 1, A Cryptographic Format Choice

### Initial verdict: REJECT

"We already have AES-GCM" is the wrong answer and it is sitting right there to be reached for. `keyencryption.go` buffers the whole plaintext and puts one 12-byte nonce over one message, and its own justification is that this project should not take a crypto dependency for one feature, which is pressure that argues the opposite way for a multi-TB artifact. The draft's `algorithm: auto` is a knob whose effect the code cannot state, which `docs/epic-checklist.md` section 5 then forbids explaining.

### Consensus position: APPROVE AFTER REVISION

ADR 0027 before any production code, with named criteria (streaming without buffering, authenticated header, tamper/truncation/reordering detection, a maintained audited Go implementation, recovery outside a running deployment, re-wrapping for rotation, binary size, licence) against named candidate formats. Chunked AEAD with per-chunk nonce derivation and chunk-index binding. `keyencryption.go` is cited as precedent for **key material** and ruled out for **artifact material**. No `algorithm` knob: the format is a property of the build and is recorded per stored copy as a format id and version, so a second format can be added later without a config field and without a one-way door.

## Expert 2, A Key-Custody Lifecycle

### Initial verdict: REJECT

A fourth secret source would break `TestRefFieldSetMatchesTheConfiguredOnes` and end "one custody model". A managed key cannot be protected by another key this process holds, and `apps/common/auth/local/secrets.go:36` already made that argument for the SMTP password: there is no key this process could hold that an attacker who can read a 0600 file in the state directory could not also read. And `config.KeyEncryption` already means "the key that encrypts secret material this manager persists" (#298); two key concepts sharing that name in one file read under `KnownFields(true)` is unrecoverable.

### Consensus position: APPROVE AFTER REVISION

The external key provider is `secretref.Ref`-shaped, file, env or command, exactly one, and nothing else; no KMS or Vault field in either phase. Managed key material is a 0600 file in a 0700 directory under the **same** rule `secretref.checkSecretFileCustody` enforces, the key store lives in `core/`, and the honest sentence is written down: the DR bundle is the only thing that changes the threat model, because it moves the key off the host. **One** new top-level key, `retained_copy_encryption:`; `key_encryption:` is neither extended nor moved, and ADR 0027 says why. Rotation changes the key for new copies only, every historical copy keeps the key id it was written under, and retiring a key still recorded by an `ACTIVE` placement is refused naming the copies.

## Expert 3, A Streaming Transform on the Data Path

### Initial verdict: REJECT

Ciphertext is not deterministic, and `UploadFromLocal` converges on an occupied key on the strength of it being "an earlier attempt at the SAME artifact". A re-copy writes different bytes at the same key, so a stored-bytes digest recorded by attempt one describes an object that is gone. `destinationPlacement` copies the plaintext hash onto the destination row and `localPlacementFor` stamps `content` from `rec.LocalHash`; under encryption both write a **plaintext** digest onto a **ciphertext** object, which is #662's defect ("two numbers that disagree about the same file are not a measurement") with a new cause. Nothing on this path has ever asserted bounded memory, and "streaming" is the whole claim.

### Consensus position: APPROVE AFTER REVISION

Two digests in the tree's own vocabulary: `artifacts.local_hash` stays the plaintext digest and is never overwritten; the stored-bytes digest is a **new** per-copy column (`placement_encryption.stored_hash`, a side table so `placements` and compat cell `03-migrated-schema` stay purely additive). The stored-bytes digest is written **only** by the attempt that reached the durable phase advance, in that same write; a re-copy re-derives it; nothing ever compares a recorded stored-bytes digest against a freshly encrypted object; nonce and stream state are never reused across attempts, and a retry is a fresh encryption, never a resumed one. `content` over an encrypted copy means a streaming decrypt whose plaintext digest equals `artifacts.local_hash`, an unresolvable key is `ErrClassUnavailable` and never a silent downgrade to `existence`, and `automaticMediumClass` stays `placement.Existence`, asserted. A multi-GiB fixture goes through encrypt and through decrypt-verify against an explicit allocation budget in the shape of `adapter_test.go:1055`, with the failure message naming the defect. The falsified comments at `0007_placements.sql:45-47` and `engine.go:1065-1067` are corrected in the same commit.

## Expert 4, A Recovery Procedure Performed Under Stress

### Initial verdict: REJECT

`health.Report` is computed per call and there is no `health.Warning` type, so "a persistent warning until a bundle is confirmed" has nowhere to live and would vanish on restart. "No confirmed recovery bundle" as an `alert.Condition` would ride the retry ladder forever, because only a human can resolve it, which is not the model any of the thirteen kinds follows. A `required` refusal that arrives after `SOURCE_DELETE_PENDING` is data loss, not a refusal. And a recovery procedure that needs a working deployment to learn which key a copy needs is not a recovery procedure.

### Consensus position: APPROVE AFTER REVISION

The bundle confirmation is a **durable** fact in migration 0015, surfaced through a new `Report` section field assigned by the caller exactly as `Report.Workflow` is, whose zero value reads "this deployment encrypts nothing". It is a **health** state, not an alert. Exactly one new `alert.Kind` is justified and it is the one that resolves by itself: a `required` policy whose key cannot be resolved, added as a deliberate edit to the pinned list with the reason in its own doc. The `required` refusal is evaluated **before** the first destructive step on both seams, `lifecycle.DeleteRemote` (and the `RetainRemote` branch that already exists for read-only sets) and `Reclaimer.DeleteFromMedium`, and the crash matrix gains cells that kill the process at each new transform and commit boundary. The **non-secret** encryption metadata (format id, format version, key id, plaintext digest, plaintext size, stored size) goes in the `recovery.Manifest` sidecar, so `catalog rebuild` identifies encrypted copies and names a missing key provider, and the documented clean-machine procedure needs the bundle, the shipped binary and `docs/recovery-without-a-terminal.md` and nothing else.

## Expert 5, A Leak Surface

### Initial verdict: REJECT

`obs.Secret` still leaks from an unexported struct field under `%+v`, pinned by `TestSecretInAnUnexportedFieldStillLeaks` (`core/internal/obs/secret_test.go:276`), and `obs.Redactor`'s needles are endpoint host, port and user only (`core/internal/obs/redact.go`), so it will never redact a key. One composite "no key anywhere" boolean hides a per-surface regression. A key-provider field that could be parsed back out of JSON is a second, undocumented way to declare a key, which is exactly why `secretref.Ref` has no `UnmarshalJSON`. `retained_copy_encryption:` under `KnownFields(true)` is a one-way door, and this is a feature an operator reaches for during an incident. "A per-set override for every knob" as a box-tick produces fields whose effect the code cannot state. And the object key is not confidential and cannot be made so here: `MediumKey` is `<prefix>/<source>/<set>/<artifact-name>`, and the draft's "safe suffix/version conventions" would fork the key and orphan every plaintext-era object.

### Consensus position: APPROVE AFTER REVISION

Every new type holding an unexported `obs.Secret` reasserts all six rendering interfaces, as `resolvedCredentials` and `keyEncryptionSecretCacheEntry` already do; key safety comes from never wrapping key material in anything renderable, not from redaction, and that boundary is stated. A canary corpus with a planted leak **per surface**: CLI JSON, API response, `state_transitions`, the activity feed and the live ring, metrics, the log stream, the recovery sidecar, the DR bundle manifest, the compat captures, and the generated screenshots, each planting turning **its own** matrix row red, with EPIC L's permanent `_WouldCatchOne` control as the pattern so absence can never pass vacuously, and no canary value in any issue, commit message or captured image. The API's key-reference field is write-only and reads back as its source description only; an inline key in config is refused at `Load` with a fixture beside `53-invalid-inline-medium-secret.yaml`. The door is named in the CHANGELOG with the version and every new field carries `omitempty`. The per-set answer is `mode` and `key_provider` and nothing else, with the refusals in writing: rotation cadence, format and chunk size are not properties of a backup set. The key layout does not change, and the epic states plainly that encryption protects object **content**, not object **names**.

## 3.6 Five-Expert Consensus

Each finding is a release requirement. Each gets a row in `docs/conformance/epic-n-matrix.md` with the mutation that must turn it red.

| ID | Finding | Required change |
|---|---|---|
| NC-01 | "We already have AES-GCM" is available to be reached for, and `keyencryption.go` buffers the whole plaintext under one nonce | ADR 0027 before any production code, named criteria against named candidate formats, chunked AEAD with per-chunk nonce derivation and chunk-index binding; `keyencryption.go` is precedent for key material and ruled out for artifact material; no bespoke container, KDF or nonce scheme |
| NC-02 | `algorithm: auto` is a knob whose effect the code cannot state | No `algorithm` knob. The format is a property of the build and is recorded per stored copy as a format id and version |
| NC-03 | A fourth secret source breaks `TestRefFieldSetMatchesTheConfiguredOnes` and ends one custody model | The external provider is `secretref.Ref`-shaped, file/env/command, exactly one, nothing else; no KMS or Vault field in either phase |
| NC-04 | A managed key cannot be protected by another key this process holds | A 0600 file in a 0700 directory under the same rule `checkSecretFileCustody` enforces; the key store lives in `core/`; the DR bundle is the only thing that changes the threat model, in writing |
| NC-05 | `config.KeyEncryption` already owns the phrase and the name, and `KnownFields(true)` makes a clash unrecoverable | One new top-level key, `retained_copy_encryption:`; `key_encryption:` is neither extended nor moved, with the reason in ADR 0027 |
| NC-06 | Ciphertext is not deterministic and `UploadFromLocal` converges on an occupied key | The stored-bytes digest is written only by the attempt that reached the durable phase advance; a re-copy re-derives it; nothing compares a recorded digest against a fresh encryption; a retry is a fresh encryption, never a resumed one |
| NC-07 | `destinationPlacement` and `localPlacementFor` write a plaintext digest onto a ciphertext object, which is #662 again | Two digests: `artifacts.local_hash` stays plaintext and is never overwritten; the stored-bytes digest is a new per-copy column; the two falsified comments are fixed in the same commit |
| NC-08 | A copy is not recoverable because ciphertext exists, and `Proves()` would be lying | `content` over an encrypted copy means a streaming decrypt whose plaintext digest equals `artifacts.local_hash`; an unresolvable key is `ErrClassUnavailable`, never a weaker class |
| NC-09 | A decrypt-verify is a `Content` check and `CostsEgress()` is true for exactly that class | `automaticMediumClass` stays `placement.Existence`, asserted; decrypt-verify is operator-initiated through the existing `CostsEgress()` refusal in `checkMediumPlacements`, extended rather than bypassed |
| NC-10 | The object key is not confidential and cannot be made so here without orphaning every plaintext-era object | The key layout does not change; encryption protects object content, not object names, and the docs say so with the reason |
| NC-11 | Nothing on this path has ever asserted bounded memory | A multi-GiB fixture through encrypt and through decrypt-verify against an explicit allocation budget, in the shape of `adapter_test.go:1055`, with the failure message naming the defect |
| NC-12 | `health.Report` is computed per call and there is no `health.Warning` type | The bundle confirmation is a durable fact in migration 0015, surfaced through a new `Report` section field assigned the way `Report.Workflow` is, whose zero value reads "this deployment encrypts nothing" |
| NC-13 | "No confirmed recovery bundle" as an alert rides the retry ladder forever, because only a human resolves it | A health state, not an alert. Exactly one new `alert.Kind`, the self-resolving one: a `required` policy whose key cannot be resolved, as a deliberate edit to the pinned list |
| NC-14 | A `required` refusal after `SOURCE_DELETE_PENDING` is data loss | The refusal is evaluated before the first destructive step on both seams, and the crash matrix gains cells at each new transform and commit boundary, with the anti-vacuity control |
| NC-15 | A recovery procedure that needs a working deployment to learn which key a copy needs is not one | The non-secret encryption metadata goes in the `recovery.Manifest` sidecar; `catalog rebuild` identifies encrypted copies and names a missing provider; the clean-machine procedure needs the bundle, the shipped binary (or the `age` CLI) and one document |
| NC-16 | `obs.Secret` leaks from an unexported field under `%+v` and the redactor will never redact a key | Every new type holding an unexported `obs.Secret` reasserts all six interfaces; key safety comes from never wrapping key material in anything renderable, and that boundary is stated |
| NC-17 | One composite "no key anywhere" boolean hides a per-surface regression | A canary corpus with a planted leak per surface, ten surfaces, each turning its own matrix row red, with a permanent negative control per surface; no canary value in any issue, commit message or image |
| NC-18 | A key-provider field parsed back out of JSON is a second, undocumented way to declare a key, and `retained_copy_encryption:` under `KnownFields(true)` is a one-way door | The API's key-reference field is write-only and reads back as its source description; an inline key in config is refused at `Load` with a fixture beside `53-invalid-inline-medium-secret.yaml`; the door is in the CHANGELOG and every new field carries `omitempty` |
| NC-19 | "A per-set override for every knob" produces fields whose effect the code cannot state | The per-set answer is `mode` and `key_provider`, and nothing else; rotation cadence, format and chunk size get a written `no` |

---

# 4. Functional Requirements

Eight numbers, one sentence each, every one cited by at least one sub-issue in section 5. The block is FR-59 through FR-66; the next number after it, FR-67, is EPIC Q's.

## FR-59, Encryption is a property of a stored copy

Retained-copy encryption is a property of a **stored copy**, set by a deployment-wide `retained_copy_encryption` default with a per-backup-set `inherit | off | required` override, and `off` preserves today's bytes and behaviour exactly.

## FR-60, The format is chosen by ADR and recorded per copy

The encryption format and its Go implementation are chosen by ADR 0027 against named criteria before any production code, no cryptographic primitive or container is designed here, and the format's identifier and version are **recorded per stored copy** rather than configured.

## FR-61, Two digests

A stored copy records the digest of **its own stored bytes** in a column distinct from the artifact's plaintext digest in `artifacts.local_hash`, and no plaintext digest is ever written as an encrypted copy's stored-bytes digest.

## FR-62, Recoverable means decrypted and matched

No copy is reported recoverable because ciphertext exists: `verification_class: content` over an encrypted copy requires a streaming decrypt whose plaintext digest equals `artifacts.local_hash`, and an unresolvable key yields `ErrClassUnavailable` rather than a weaker class.

## FR-63, Managed keys

Managed keys are generated with a CSPRNG, held under the same file-custody rule `core/internal/secretref` enforces, never rendered back in the clear on any surface after the one-time export, and carry stable ids with creation and retirement timestamps and a status.

## FR-64, External keys

An external key is declared only as a `secretref`-shaped `file`/`env`/`command` reference, whether it currently resolves is reportable without revealing it, and no resolved key is persisted in configuration, the journal, the activity feed, the recovery sidecar, a metric or a support export.

## FR-65, Rotation orphans nothing

Rotation changes the key used by **new** copies only, every historical copy stays decryptable through the key id recorded against it, and retiring a key is refused while any `ACTIVE` placement still records it.

## FR-66, Degraded state and the refusal before deletion

Managed encryption enabled without a confirmed disaster-recovery bundle is a **durable**, operator-visible degraded state, and a `required` policy that cannot resolve its key refuses the affected copy before any source or medium deletion step.

## Core invariants

- **`off` is byte-identical to today.** A configuration that never mentions `retained_copy_encryption:` produces the same bytes, the same `placements` rows, the same `verification_class` values and the same timings within an agreed budget, asserted rather than assumed.
- **Two digests, never one.** A surface that renders one where it means the other is a defect of the same family as #662.
- **Recoverable means decrypted and matched.** Never a weaker class wearing a stronger name.
- **`required` refuses before anything destructive.** Evaluated ahead of `lifecycle.DeleteRemote` and ahead of `Reclaimer.DeleteFromMedium`, proved by crash cells.
- **Rotation orphans nothing.**
- **No key on any surface.** One corpus, ten surfaces, ten plantings, ten rows.
- **Do not invent cryptography.**
- **Object names stay visible.** `MediumKey`'s FR-28 layout does not change and the docs say what that means for an untrusted bucket.
- **The canonical deployment does not move.** No new port, mount, compose edit, provider edit or packaging regeneration; still distroless, read-only, non-root, `cap_drop: ALL`, `no-new-privileges`, no Docker socket.
- **No rename of anything.** No FR, metric family, event constant or route is renamed. The legacy metric-prefix alias is #947's business; new metric families are additions under the existing prefix rules, with no key id as a label.
- **Producer artifacts are not rewritten.** Custody begins after the producer has made the artifact; encryption is a copy transform, never an artifact transform.

---

# 5. Phases, sub-issues and gates

Each sub-issue line names every `docs/epic-checklist.md` section it must satisfy and says what satisfying it means here. The dependency order is #968, then #989, then {#990, #991}, then #992, then #993, then #994, then #995, then {#996, #997, #998}. #990 and #991 are independent once ADR 0027 has fixed the format, the upload seam and the config shape. #994 gates #995 by the section 5 rule, not by convenience.

## Phase 1, the crypto decision, key custody, the copy pipeline and parity (`N:phase-1`)

- **N1.1 #989, ADR 0027, format selection and the domain/config model** (FR-59, FR-60). The named-criteria comparison and the chosen format and library (age v1 through `filippo.io/age`, X25519 identities, 64 KiB chunks, the encrypted staging pass as the upload seam; recovery needs the bundle and either the shipped binary or the `age` CLI); `retained_copy_encryption:` as one new top-level key with a per-set `mode`/`key_provider` override in the three-field pattern; migration `0015` for the key registry, the per-copy encryption metadata and the bundle-confirmation fact; the effective-policy resolver with re-resolve after a global mutation; `Validate` refusing `required` where no usable provider exists. Satisfies section 1 (ADR before code), section 2 (global default, per-set override, re-resolve, the `09-` valid and `62-invalid-` fixtures, the `KnownFields(true)` door), section 7 (migration `0015`; journal truth stays journal truth), section 9 (ADR 0027; the two comments this falsifies) and section 11 (the ADR adds no module, so `NOTICE` and `provenance/third-party-licenses.json` move with `filippo.io/age` in #991, not here). The inline-key refusal and its fixture are this issue's, per its acceptance criteria; #990 extends them to managed and external providers.
- **N1.2 #990, managed key lifecycle, the external resolver and the DR bundle** (FR-63, FR-64, FR-66). CSPRNG generation; 0600-in-0700 custody under `secretref`'s own rule; stable ids with creation and retirement timestamps and a status; a one-time export bundle with integrity metadata and human-readable instructions; a resolvability probe that never reveals material; the durable "no confirmed bundle" health state through a new `Report` section field; the one new `alert.Kind` for an unresolvable `required` key. Satisfies section 2 (`key_provider` default and per-set override; the fixture that refuses inline key material), section 4 (event constants for key create, rotate, bundle export and confirm, each with a `Result`), section 7 (the registry and the confirmation are durable state), section 11 (no key reaches disk unprotected or any log) and section 12 (a mutation self-test per new guard).
- **N1.3 #991, the streaming encrypt/decrypt copy transform and its verification** (FR-61, FR-62). The upload seam decision from ADR 0027 implemented; bounded memory with an explicit allocation budget; the stored-bytes digest written only by the attempt that committed; decrypt-verify ending at `artifacts.local_hash`; tamper, truncation, reorder and wrong-key all failing closed with no best-effort plaintext; cancellation extending `discardDestination` and `sweepStagingTemps` rather than replacing them. Satisfies section 4 (Encrypting, Uploading, Verifying and Decrypt-verifying as `app.Progress` stages with byte counters), section 11 (regenerates `NOTICE` and `provenance/third-party-licenses.json` when `filippo.io/age` enters `core/go.mod`), section 7 (the two-digest columns written under the existing phase discipline) and section 12 (crash cells at every new boundary, with the anti-vacuity control).
- **N1.4 #992, lifecycle, retention, catalog rebuild and rotation semantics** (FR-62, FR-65). Retention deletes encrypted objects by ordinary copy identity with no key needed; newest-known-good stays artifact-level; the sidecar carries the non-secret encryption metadata (including the last decrypt-verification time) and the key id; `catalog rebuild` identifies encrypted copies and names a missing provider; rotation affects new copies only; retiring a key still needed is refused, naming the copies. Satisfies section 4 (durable stages reach the feed; the cursor-paged volume answer stated), section 7 (sidecar and journal, compat captures), section 9 (`docs/recovery.md`, `docs/recovery-without-a-terminal.md`, `docs/storage-mediums.md`) and section 12 (the refusal's own self-test).
- **N1.5 #993, CLI, API, observability and security parity** (FR-59, FR-63, FR-64, FR-66). Policy show/patch/reset, per-set `inherit|off|required`, key status/list, managed-key create/rotate, bundle export/confirm, external-reference configure/test, per-copy encryption state, decrypt-verify dry run, JSON output; `cliecho` entries and matching refusals with matching exit codes; `openapi.json` with both bindings regenerated; metrics without key ids as labels. Satisfies section 3 (both directions, including the ungated command-with-no-browser-equivalent check), section 4 (`Result` on completion; `CommandEcho.tsx`; CLI and web cancellation converging on one durable operation, asserted row by row), section 6 (contract plus both generated bindings, drift green), section 11 (no resolved key in any response, error or label) and section 12 (`scripts/ci-local.sh`).

### Phase 1 entry gate

- [x] The letter `N` is held only by #967 and #968. Checked on 2026-10-24 against every issue whose title begins `EPIC`: A (#1), B (#81), C (#123), D (#177), E (#232), F (#265), G (#590), H (#623), I (#664), J (#728), K (#779), L (#807), M (#951), O (#898), P (#955), Q (#953), R (#885) and S (#1156) are taken.
- [x] FR-59 through FR-66 collide with nothing: `git grep -E 'FR-(5[0-9]|6[0-6])'` over `main` at `bc085e06` finds the checklist's own numbering sentence and EPIC Q's sibling-epics line that restates this block (neither claims a number for anything else), FR-45 through FR-50 are EPIC O's in #904, FR-51 through FR-58 are EPIC M's, FR-67 onward are EPIC Q's, and FR-25 is still a hole this epic does not fill.
- [x] `core/migrations/` has no `0015`; `0014` is EPIC M's, `0016` and `0018` are EPIC Q's, `0017` is EPIC P's. `docs/adr/` has no `0024` through `0028`; `0024` is EPIC O's, `0025`/`0026` are EPIC M's, `0027`/`0028` are this epic's.
- [x] The adversarial review is done and its nineteen findings are release requirements (section 3).
- [ ] This specification and `docs/conformance/epic-n-matrix.md` are merged with every row `BLOCKED`, each naming its sub-issue and its falsification. That is this pull request.
- [ ] ADR 0027 is merged before the first line of encryption code, and it names candidate formats and criteria rather than a decision already taken. That is #989.
- [ ] `main` green.

### Phase 1 exit gate

- [x] A configuration that never mentions `retained_copy_encryption:` loads byte-identically after a round trip, produces the same `placements` rows and the same `verification_class` values as before this epic, and stays inside the agreed timing budget.
- [ ] `mode: required` with no usable key provider is refused at `Load`, naming the config key at fault, with the same reason and exit code on both routes.
- [ ] `mode: required` with an unresolvable key refuses the affected copy **before** `lifecycle.DeleteRemote` and **before** `Reclaimer.DeleteFromMedium`, proved by a crash cell at each new transform and commit boundary in `core/tests/crashmatrix` and `core/tests/movecrash`, with the anti-vacuity control.
- [x] An encrypted copy's row carries a stored-bytes digest that is **not** `artifacts.local_hash`, `artifacts.local_hash` is never overwritten by an encryption step, and a planted write of the plaintext digest into the stored-bytes column turns its own row red.
- [x] A decrypt-verify produces a plaintext digest **equal to `artifacts.local_hash`** before any copy reaches `verification_class: content`, and a copy whose key cannot be resolved reports `ErrClassUnavailable` rather than any class at all.
- [x] A single-bit flip, a truncated tail, a reordered chunk, a swapped header and a wrong key each fail closed, and **no partial plaintext is ever written or returned** on any of the five.
- [x] A multi-GiB fixture encrypts and decrypt-verifies inside an explicit allocation budget, with the failure message naming the defect ("the artifact is being buffered rather than streamed").
- [ ] An interrupted upload cannot become a catalogued good copy: the row does not advance, the destination object is discarded by `discardDestination`, the staging area is empty after restart, and a re-copy is a fresh encryption whose stored-bytes digest is recorded only on the attempt that committed.
- [x] `revalidate.automaticMediumClass` is still `placement.Existence`, asserted, and an unattended pass never triggers a decrypt; decrypt-verify is operator-initiated through the existing `CostsEgress()` refusal.
- [x] A managed key is a CSPRNG-generated 0600 file in a 0700 directory under `core/`, refused under the same custody rule `checkSecretFileCustody` applies to an operator's secret file, with a stable id, creation and retirement timestamps and a status, and no surface renders it back after the one-time export.
- [x] Rotation produces a new active key, every pre-rotation copy still decrypt-verifies through its recorded key id, and retiring a key with an `ACTIVE` placement against it is refused **naming the copies**.
- [ ] Enabling managed encryption without a confirmed bundle produces a degraded state that **survives a restart**, carried by a new `health.Report` section field whose zero value reads "this deployment encrypts nothing", and the exported bundle decrypts a fixture copy on a clean machine with no state database present.
- [x] Exactly one new `alert.Kind` exists, for a `required` policy whose key cannot be resolved; it is a deliberate edit to the pinned list with its reason in its own doc, and the bundle-confirmation state is not an alert.
- [x] The `recovery.Manifest` sidecar carries the non-secret encryption metadata (format id, format version, key id, plaintext digest, plaintext size, stored size, last decrypt-verification time) and nothing secret, and `catalog rebuild` identifies an encrypted copy and names its missing key provider.
- [ ] The object key layout is unchanged: `MediumKey` still derives `<prefix>/<source>/<set>/<artifact-name>`, and the documentation states that encryption protects object content and not object names.
- [ ] Parity holds both ways at the API and CLI: a `cliecho` entry per new route, no gap sentence naming a shipped verb, every new verb in the dispatch table **and** `docs/site/reference.html`, refusals and exit codes matching on both routes, and the ungated reverse direction (a command with no browser equivalent) checked by hand and recorded.
- [ ] `openapi.json` is authoritative, both bindings are regenerated, drift and client-path checks are green, the key-reference field is write-only and reads back as its source description, and `docs/api/contract.md` carries the new surface.
- [x] Two compat config fixtures land (`09-` valid, `62-invalid-`), an inline key in config is refused at `Load` with a fixture beside `53-invalid-inline-medium-secret.yaml`, the compat captures are updated, and `core/migrations/` gains exactly `0015`.
- [ ] Every new `obs` event constant is an FR-23 catalog addition with a typed method and a `Result`, the four new `app.Progress` stages carry the existing byte counters, and the feed-volume answer is stated and asserted: one durable event per copy, never per chunk.
- [x] A CLI cancel and a browser cancel of the same operation leave identical durable state: the row does not advance, the destination object is discarded, the staging area is swept, and both surfaces report the same state after a reconnect.

## Phase 2, design, web UI, e2e, docs and media (`N:phase-2`)

- **N2.1 #994, design mock-ups and the security UX specification** (FR-59, FR-66). Mandatory before any web coding: the global card with smart defaults, the enable flow that asks no crypto-expert question, managed-key status and bundle warning and confirmation, per-set `Inherit / Off / Required`, per-copy indicators, rotation, the advanced reference-only provider flow, live Encrypting and Decrypting states, light, dark and narrow. **"Encrypted" and "verified recoverable" are different states, visually.** Satisfies section 5 (design first, against what already ships; the `.html` and matching `.png` note in `docs/design/`, dated by this issue) and section 5 Tooltips (which controls can be explained from the code, and the refusal list for those that cannot).
- **N2.2 #995, web UI implementation with live progress and Activity** (FR-59, FR-63, FR-66). Settings plus per-set card; key health; operation-based create, rotate and export-confirm; per-copy state on Backups and Backup detail; live progress in Active operations and the docked terminal; durable entries with correlation ids; reconnect restoring operation state; no key material in long-lived React state. Satisfies section 4 (`ActivityStrip.tsx`, `SetActivityPanel.tsx`, `DashboardActivity.tsx`, `useActivityFeed.ts`), section 5 (`SettingsPage.tsx` and the per-set card; frontend gate green), section 5 Tooltips (registry entries plus three-part `fieldHelpCopy.ts`, effect stated from the code, opt-out tested explicitly ON) and section 6 (the generated client consumed, not hand-written).
- **N2.3 #996, e2e, equivalence, crypto-negative and disaster-recovery tests** (FR-62, FR-64, FR-65, FR-66). Suites A, B and C in `retnd/retnd-tests` with `scripts/e2e/tests-repo.pin` moving in lockstep: managed enablement, external resolver, global and per-set override, plaintext local beside encrypted off-site for one artifact, wrong and missing key, tampered and truncated ciphertext, kill during encrypt and during upload, rotation, catalog loss and rebuild, clean-machine recovery from the bundle, browser reconnect, CLI and web equivalence, feature-off regression, plus the ten-surface canary corpus. Satisfies section 1 (a falsification per row), section 3 (equivalence proved, not asserted), section 11 (no credential on disk or in a log) and section 12 Fixtures (values chosen against the wrong implementation, existing fixtures extended rather than multiplied).
- **N2.4 #997, repository-wide docs, README and the docs site** (FR-60, FR-61). What is and is not encrypted; plaintext digest versus stored-bytes digest in the tree's own column names; smart defaults; managed versus external providers; bundle custody; rotation; loss-of-key consequences; the decrypt procedure with and without a working catalog; archive and thaw interaction; overhead; the explicit statement that producer artifacts are not rewritten; ADR 0028; the CHANGELOG entry with the one-way door. Satisfies section 3 (the ungated reverse-direction sweep, recorded as done by hand), section 9 (README, the site pages the change touches, the honest section, `install.md`, `deployment.md`, `runtime-contract.md`, `recovery.md`, `recovery-without-a-terminal.md`, `storage-mediums.md`, package-doc baselines, falsified comments) and section 11 (`docs/compliance/`, `docs/submission/`).
- **N2.5 #998, deterministic screenshots, GIFs and the release gate** (FR-59, FR-62). `docs/site/tools/capture-encryption.mjs` against the mock API with the clock pinned and Playwright borrowed from the pinned checkout; enable-with-defaults, bundle warning and confirmation, per-set inherit and required, the per-copy indicator, live Encrypting to Verifying, rotation, the missing-key degraded state, a decrypt-verify operation, dark and narrow variants; then the gate: the new `gate_step` in `scripts/ci-local.sh` (the only gate since #1260), the crypto-negative suites, the ten canary rows, the empty-diff assertion over the canonical deployment, the dedicated matrix checker, and a matrix with no `BLOCKED` row. Satisfies section 8 (the eleven providers: **nothing shipped changes**, asserted as an empty diff), section 10 (a capture step in the right script; run an unchanged script first; store-listing screenshots back to outstanding if a listed screen moved) and section 12 (a self-test per new guard, no new old-brand identifier).

### Phase 2 entry gate

- [ ] Phase 1 is exit-gated: every Phase 1 row of `docs/conformance/epic-n-matrix.md` is `PASS`, or `PARTIAL` with the reason written in the row.
- [ ] The design (#994) is in `docs/design/` with its `.html` and `.png`, because no web implementation starts before it.
- [ ] `scripts/e2e/tests-repo.pin` is at a `retnd-tests` commit that contains the Phase 1 CLI cases, so the pin and the product can move together.

### Phase 2 exit gate

- [ ] The design note `docs/design/994-retained-copy-encryption.html` and a matching `.png` exist with the reasoning and the date, in light, dark and narrow, and "Encrypted" and "verified recoverable" are two visually distinct states on every surface that shows either.
- [ ] `SettingsPage.tsx` carries the global control with its smart default and the per-set card carries `Inherit / Off / Required`; the frontend gate (typecheck, every provider typecheck, eslint, vitest, build) is green.
- [ ] Every explained control has a registry tooltip and a three-part `fieldHelpCopy.ts` entry with the effect read off the code, the tag allowlist and orphan checks are green, the opt-out preference is tested explicitly ON, and the controls refused an entry are listed with their reasons.
- [ ] Encrypting and Decrypt-verifying reach the global terminal, the per-set panel, the dashboard and the activity feed with byte counters, `CommandEcho.tsx` shows the equivalent command for every mutating control, a reload mid-operation restores the stage from the durable feed and starts no second operation, and no key material is held in long-lived React state.
- [ ] Suites A, B and C land in `retnd/retnd-tests` with `scripts/e2e/tests-repo.pin` moving in lockstep, each case fails against `main` before the product change and passes after, and Suite C proves CLI and web equivalence with the same exit code on both routes for at least one refusal and one success per surface.
- [ ] No key material appears on any of the ten surfaces (CLI JSON, API response, `state_transitions`, the activity feed and live ring, metrics, the log stream, the recovery sidecar, the DR bundle manifest, the compat captures, the generated screenshots), with a planted canary per surface turning its own ledger row red and a permanent negative control per surface so absence cannot pass vacuously; no metric carries a key id as a label.
- [ ] ADR 0027 and ADR 0028, the CHANGELOG entry naming the one-way door and the version, README, the site pages the change touches, `index.html#honest`, the prose docs in section 9 of the checklist and the two falsified comments have all landed, and the documentation states what is and is not encrypted, including that object names stay visible.
- [ ] `docs/compliance/` and `docs/submission/` say what is stored changed shape and that a key bundle is exported, and any store-listing screen this epic changes is back to outstanding in `docs/submission/screenshots.md`.
- [ ] `docs/site/screens/` is regenerated through `capture-encryption.mjs` against the mock API with a pinned clock and Playwright borrowed from the pinned checkout, one untouched capture script run first, no hand-taken image, and two consecutive runs are byte-identical.
- [ ] Every new text and background pair meets WCAG AA in the light and the dark theme, with the measured ratios for the key pairs recorded in the pull request that adds the surface.
- [x] The canonical deployment diff is **empty** across `container/compose.yaml`, the eleven files under `apps/*/compose/`, `apps/unraid/template/`, `apps/truenas/catalog/` and the four `distribution/packaging/*.json`, and the container contract is still distroless, non-root, read-only, `cap_drop: ALL`, `no-new-privileges`.
- [ ] `scripts/ci-local.sh` is green with the new check as its own `gate_step` and a stub for it in `scripts/tests/ci-local-gate.test.sh`, every new guard has a mutation self-test with a negative control, and `scripts/rename/check-brand-drift.sh` finds no new old-brand identifier.
- [ ] `docs/conformance/epic-n-matrix.md` has no `BLOCKED` row, every `PASS` row's falsification has been run and watched to fail, every `BY HAND` row says who checked what and when rather than borrowing a neighbouring green, and a dedicated checker in `core/tests/` holds the file to that with its own mutation self-test.

---

# 6. TDD Contract

Every guard below has a planted violation that has to be run and watched to fail before its row goes `PASS`. The full table, with test names and the mutation, is `docs/conformance/epic-n-matrix.md`; the guards here are numbered in the order of its `NV.n` rows, one row per guard, and the ten key-material surfaces are ten guards rather than one.

| Guard | Planted violation that proves it fires |
|---|---|
| One custody model: no fourth secret source | Add a fourth field to the external key provider's reference type; `TestRefFieldSetMatchesTheConfiguredOnes` must go red |
| An inline key in config is refused at `Load` | A fixture carrying key material inline beside `53-invalid-inline-medium-secret.yaml`; `Load` must refuse it naming the key |
| `required` with no usable provider is refused at `Load` | A config with `mode: required` and no `key_provider`; `Load` must refuse, naming `retained_copy_encryption`, with the same exit code on both routes |
| `key_encryption:` is untouched | Add a field to `config.KeyEncryption` or read it from the retained-copy path; the shape test and the ADR 0027 guard must go red |
| Two digests: the plaintext digest never lands in the stored-bytes column | Write `artifacts.local_hash` into an encrypted copy's stored-bytes column; the row-shape test must go red |
| The stored-bytes digest is written only by the committing attempt | Record the digest from an attempt that did not reach the durable phase advance; the crash cell at that boundary must go red |
| A retry is a fresh encryption | Resume an interrupted encryption from its stream state; the nonce-reuse assertion must go red |
| Decrypt-verify ends at `artifacts.local_hash` | Compare a ciphertext digest and call the copy `content`; the ladder test must go red, and `Proves()` must not be reachable for it |
| An unresolvable key is `ErrClassUnavailable` | Downgrade an unresolvable key to `existence`; the test that pins the refusal must go red |
| Tamper, truncation, reorder, swapped header and wrong key fail closed | Let any one of the five return or write a byte of plaintext; its own case must go red, and the control case proves the harness reads the output |
| Bounded memory | Buffer the whole artifact in encrypt or in decrypt-verify; the budget test must say "the artifact is being buffered rather than streamed" |
| The automatic ceiling stays `Existence` | Raise `automaticMediumClass`; `TestTheAutomaticCeilingIsAClassThisPassMayRunAgainstEveryMedium` must go red, and an unattended pass must never decrypt |
| `required` refuses before anything destructive | Evaluate the refusal after `SOURCE_DELETE_PENDING` or after `DeleteFromMedium` has started; the new crash cells must go red, with the anti-vacuity control proving they staged something |
| Cancellation leaves nothing behind | Leave the destination object after a cancel, or a staging temp after a restart; the cleanup assertions must go red |
| Managed key custody | Accept a managed key file at 0644, under a symlinked final component, or in a group-writable directory; the custody test must refuse each |
| A key is never rendered back | Render a key through any of `obs.Secret`'s six paths, or hold one in a type that reasserts none of them; the rendering test must go red |
| Rotation orphans nothing | Permit retiring a key an `ACTIVE` placement still records; the refusal test must go red, and a pre-rotation copy must still decrypt-verify |
| The degraded state is durable | Keep the bundle confirmation in memory; the restart test must go red |
| Exactly one new alert kind | Add a second kind, or make the bundle state an alert; `TestKindsAreExactlyTheDeliberatelyChosenConditions` must go red |
| The sidecar carries non-secret metadata and the key id | Add a field that looks like a secret; `TestManifestFieldsExcludeSecrets` must go red. Drop the key id; the rebuild test naming a missing provider must go red |
| The object key does not change | Change `MediumKey` for an encrypted copy; the medium contract suite must go red |
| The canonical deployment does not move | Change one compose, template, catalog or manifest file; `TestTheCanonicalDeploymentIsExactlyWhatItWasBeforeEpicQ`'s successor for this epic must name it |
| No key in CLI JSON | Plant the canary in any JSON a verb prints; the CLI surface row must go red on its own |
| No key in an API response | Plant the canary in any response or refusal body; the API surface row must go red on its own |
| No key in `state_transitions` | Plant the canary in a transition detail; the journal surface row must go red on its own |
| No key in the activity feed or the live ring | Plant the canary in a feed entry or a live line; the feed surface row must go red on its own |
| No key in a metric | Plant the canary, or a key id, as a label value; the metrics surface row must go red on its own |
| No key in the log stream | Plant the canary in a log line through every handler; the log surface row must go red on its own |
| No key in the recovery sidecar | Plant the canary in a manifest field; the sidecar surface row must go red on its own |
| No key in the DR bundle manifest | Plant the canary in the bundle's human-readable or integrity metadata; the bundle surface row must go red on its own |
| No key in the compat captures | Plant the canary in any `capture_*.go` output; the capture surface row must go red on its own |
| No key in a generated screenshot | Plant the canary in a rendered frame; the capture script's text check must go red on its own |
| Tooltips and field help | An entry for a control whose effect the code cannot state; a preference tested only OFF |
| Parity | A route with no `cliecho` entry; a gap sentence naming a shipped verb; a verb missing from `reference.html` |
| The matrix itself cannot lie | A row with no falsification, a row with no proof, a row green under another word, a `BLOCKED` row naming no sub-issue, a `BLOCKED` row citing a closed issue |

---

# 7. Checklist coverage map

One row per `docs/epic-checklist.md` section, Tooltips and Fixtures included. A section owned by nobody is argued here, never omitted.

| Section | Owner | What owning it means here |
|---|---|---|
| 1 The shape of the epic | #968, #998 | Two phases, five each, the cut list below; letter `N`; FR-59 to FR-66 continuing the series; the five-lens panel; entry and exit gates as checkable claims; the matrix with a falsification per row, every row `BLOCKED` at entry and none at exit |
| 2 Settings | #989, #990, #993 | One new top-level key with a default good enough that nobody sets it; per-set `mode` and `key_provider` in the pointer plus resolved plus `Validate` pattern; re-resolve after mutating the global, because the retention override flags were a silent no-op this exact way; a `09-` valid fixture and a `62-invalid-` one; the `KnownFields(true)` door in the CHANGELOG. The per-set answer is **no** for rotation cadence, format and chunk size, in writing |
| 3 CLI / web parity | #993, #997 | A `cliecho` entry or a `why` per new route, no gap sentence naming a shipped verb, every new verb in `core/cmd/retnd/main.go` **and** `docs/site/reference.html`, refusals matching on both routes with matching exit codes, and the ungated reverse direction swept by hand and recorded |
| 4 Progress and the activity feed | #991, #992, #993, #995 | New `obs` event constants as FR-23 catalog additions, never renames, each with a typed method and a `Result`; new `app.Progress` stages for Encrypting and Decrypt-verifying with the existing byte counters; the feed, the global terminal, the per-set panel and the dashboard; `CommandEcho.tsx`; a stated answer on cursor-paged volume, per copy and not per chunk |
| 5 Web UI design | #994, #995 | The design step first, in line with the interactive design the frontend was built from; the `.html` and matching `.png` note dated by #994; the control on `SettingsPage.tsx` and on the per-set card; frontend gate green |
| 5 Tooltips | #994 (decides), #995 (writes), #997 (records) | A registry entry and a three-part `fieldHelpCopy.ts` entry per explained control, effect stated from the code, opt-out tested explicitly ON. **And the refusal:** any control whose effect the code cannot state gets neither, which is why `algorithm: auto` was removed from the design rather than given help text |
| 6 The API contract | #993, #997 | `api/v1/openapi.json` authoritative, `core/apicontract/contract.gen.go` and `ui/shared/src/api/generated/contract.ts` regenerated, drift and client-path checks green, `docs/api/contract.md` updated. The key-reference field is write-only |
| 7 Storage, state, migrations | #989, #991, #992, #993 | Migration `0015`: the key registry, the per-copy encryption metadata and stored-bytes digest, and the durable bundle-confirmation fact. Placement and lifecycle answers keep coming from the journal. Compat captures updated (`core/tests/compat/capture_config.go`, `capture_state.go`, `capture_api.go`, `capture_cli.go`) |
| 8 Providers | #998, as an assertion of **no change** | Nothing here is platform behaviour: no `apps/common/platform/capabilities/` entry, no per-provider acceptance doc, no packaging or compose edit. Asserted as an empty diff across `container/compose.yaml`, the eleven files under `apps/*/compose/`, `apps/unraid/template/`, `apps/truenas/catalog/` and the four `distribution/packaging/*.json`; the cross-provider conformance suite stays green. Owned as a claim, not skipped |
| 9 Documentation | #989 (ADR 0027), #992, #997 (ADR 0028, CHANGELOG) | README; the site pages the change touches; the honest section saying no eleven-provider fleet and no real off-site bucket at scale were exercised; `install.md`, `deployment.md`, `runtime-contract.md`, `recovery.md`, `recovery-without-a-terminal.md`, `storage-mediums.md`; package-doc baselines; and the two comments this epic falsifies |
| 10 Screenshots and GIFs | #998 | `capture-encryption.mjs` under `docs/site/tools/`, mock API, clock pinned, Playwright borrowed from the pin; an unchanged script run first, because EPIC L's docs pass found four breaks in one afternoon and one of them produced a wrong picture rather than an error; store-listing screenshots back to outstanding if a listed screen moved |
| 11 Compliance and supply chain | #989, #990, #991, #996, #997 | NOTICE and the licence inventory regenerated by #991 when the crypto dependency moves the module graph (#989 only records that in the ADR); no key reaching an unprotected file or a log; `docs/compliance/` and `docs/submission/` updated, because what is stored changes shape |
| 12 Gates | #998, with per-issue self-tests in #990, #991, #992 | `scripts/ci-local.sh` green; the new check as its own `gate_step` with a stub in `scripts/tests/ci-local-gate.test.sh`; a mutation self-test per new guard, including negative controls so a guard that only ever fires is caught; no new old-brand identifier; tests red before green |
| 12 Fixtures | #996, with the config fixtures in #989 | One fixture per claim, including per refusal. Values chosen against the **wrong** implementation: a ciphertext whose length equals the plaintext's would pass a length-only check, so the corpus uses sizes that cross a chunk boundary and a final partial chunk; an all-zero plaintext is a fixed point for a broken keystream, so no fixture uses one. Existing fixtures extended rather than multiplied; anything generated reproducible from a generator in the same commit |

The contrast rule (WCAG AA in both themes, with the ratios measured and stated) applies wherever N2.1, N2.2 or N2.5 add styled output. This pull request adds none, since it is prose in Markdown.

---

# 8. What I cut to fit two phases, and why

- **A KMS or Vault key provider.** `secretref.Ref` is three sources and `TestRefFieldSetMatchesTheConfiguredOnes` fails on a fourth, so a KMS provider is a provider *interface* plus a *second* custody model: a genuinely separate decision, not a field. The file/env/command reference already reaches a KMS through an operator's own `command` resolver, which is the honest amount of support to ship without a provider abstraction nobody has exercised. Follow-up issue.
- **Re-encryption of historical copies on rotation.** A whole-catalogue rewrite with its own crash matrix, its own egress bill and its own "which key was this under" question at every boundary. Rotation here affects new copies and nothing else, which is a complete and defensible semantics rather than half of a larger one. Follow-up issue.
- **A second cipher for the kopia engine's repositories.** They are already encrypted by kopia's format. Adding a layer would buy nothing and would fork the passphrase custody story. What this epic owes that engine is a written answer to `required`, not a cipher. Nothing follows it.
- **Encrypted object-name confidentiality.** Changing `MediumKey` forks the key layout that makes convergence and restore-path derivation work, orphans every plaintext-era object, and would need its own migration and reconciliation story. Cut with the limitation documented rather than half-done. Nothing follows it here.
- **Automatic decrypt-verification on a schedule.** `Class.CostsEgress()` is true for exactly this check and EPIC E already decided that unattended egress is not a default anybody asked for. Operator-initiated, with the automatic ceiling asserted to have stayed where it was. Nothing follows it.
- **A key-usage audit surface beyond the activity feed.** Every mutation is already correlated and durable through `state_transitions` and the feed; a separate audit view is a reporting feature, and reporting features are how a two-phase epic becomes three. Nothing follows it.
- **Compression beside encryption.** There is no compression on the artifact path today, and adding one alongside a cipher would make every performance and correctness question ask two things at once. Nothing follows it.

Everything cut becomes a follow-up issue or nothing. It does not become a third phase.

---

# 9. Cross-epic relations

Four epics were specified together from drafts dated 2026-09-13 and reserve their numbers as one block. EPIC Q has since landed; the other three are open.

| Tracker | Subject | FR block | Migration | ADRs |
|---|---|---|---|---|
| #951 EPIC M | immutable / WORM retained copies | FR-51 to FR-58 | 0014 | 0025/0026 |
| **#967 EPIC N** | encrypted retained copies and recoverable key custody | FR-59 to FR-66 | 0015 | 0027/0028 |
| #953 EPIC Q | artifact recovery, scheduled scrubbing and recoverability assurance | FR-67 to FR-74 | 0016, 0018 | 0029/0030 |
| #955 EPIC P | first-class removable, offline and air-gapped media | FR-75 to FR-82 | 0017 | 0031/0032 |

- **M and N are independent and compose.** Both are properties of a stored copy; neither requires the other; an encrypted copy may be locked, a locked copy may be plaintext, and no code path in this epic reads or sets a lock.
- **Q must work with N off**, and it does today, since N does not exist. Q's `TestBytesThatDecryptToTheWrongDigestAreNotAPass` already pins the half of N's contract Q consumes: a recovery decrypts and requires the plaintext digest to equal the artifact digest, and ciphertext whose digest matches nothing is not a pass. N owes Q that contract and nothing else; Q restates none of it.
- **P composes with N's effective policy** rather than forking it, and an expected-offline destination must not make a `required` policy look satisfied: a copy that is owed is not a copy that is stored.
- **EPIC O's metrics.** New metric families are additions under the existing prefix rules, with no key id as a label, and nothing here renames or aliases a family.

An FR block is not safe to leave unclaimed. If this epic were abandoned, FR-59 to FR-66 would be a second FR-25-shaped hole and the block would go back to the checklist's "next free number" line rather than staying reserved.

---

# 10. Non-goals

Encrypting the producer's source filesystem or database. Replacing producer-native encryption. Home-grown cryptographic primitives, containers or KDFs. Deduplicating encrypted data. Any password-reset or recovery path that could recreate a lost key: losing the key loses the copies, and the product says so. Automatic re-encryption of historical copies during rotation. Application-aware restore. Confidentiality of object names. Encrypting what the kopia engine already encrypts. Renaming any FR, metric family, event constant or route.

---

# 11. Compatibility and migration summary

- A deployment whose configuration mentions nothing new is unchanged in behaviour and in bytes: no new key is required, the global default is `off`, and a config that never names `retained_copy_encryption:` round-trips byte for byte.
- `Load` uses `KnownFields(true)`, so a `retained_copy_encryption:` key cannot be parsed by an older build. Anyone who edits that block during an incident and then rolls back has to take it out first. #997's CHANGELOG entry names the door and the version, and every new field carries `omitempty` so nothing writes the block unprompted.
- One forward migration, `0015`. An older binary meeting the new schema version fails closed under the existing unsupported-downgrade behaviour (`state.ErrUnknownSchemaVersion`). The gap left by `0014` (M) and `0017` (P) is safe: `loadMigrations` refuses a duplicate version and nothing else.
- The format id and version stored per copy, the key status values and the key id are compatibility surfaces: a value may not be renamed to match a convention elsewhere, and a value may not be removed once anything has written it.
- Nothing is renamed. The canonical deployment stays byte-identical, which is Phase 2's exit line and #998's test.
- Existing `placements` rows are plaintext copies. Migration `0015` is a side-table migration (ADR 0027): the per-copy encryption metadata and the stored-bytes digest live in `placement_encryption`, one row per encrypted copy, so `placements` stays as `0013` left it, a copy with no row there reads "not encrypted", no existing row changes meaning and no backfill runs.

---

# 12. Decisions this scaffold took

The issue text named a few things the tree no longer agrees with, and where that happened I wrote what is true now rather than what the issue said, and recorded it here so a reader holding #968 can reconcile.

- **"FR-67 as the next free number."** It was, when #967 was written. EPIC Q then claimed FR-67 to FR-74 and `docs/epic-checklist.md` already says FR-75 is next. The Status block says both: FR-67 is the number after this block, and FR-75 is the org-wide next free number. Renumbering N to follow Q would renumber a block three other documents cite, which the checklist forbids.
- **The fixture numbers.** The issue hands this epic `07-` and `59-invalid-`; Q took both. This epic's are `09-` and `62-invalid-` (and `63-` if a second invalid fixture is needed): the `07-` and `59-` slots hold `07-assurance-global-and-per-set.yaml` and `59-invalid-assurance-cadence-below-floor.yaml`, and nothing else sits at `09-`, `62-` or `63-`.
- **The release gate's `needs` list.** Gone since #1260. The new check is a `gate_step` in `scripts/ci-local.sh` with a stub in `scripts/tests/ci-local-gate.test.sh`.
- **Nine alert kinds.** Thirteen now. The argument against a fourteenth that only a human can resolve is unchanged.
- **The matrix guard.** The issue says this scaffold touches nothing outside `docs/`. The task that commissioned it said to find the guards that check spec/matrix pairs and make them cover this pair, so I registered the pair in `core/tests/compat/matrix_test.go`'s table, which is the one place in the tree that holds a spec's exit-gate lines, its guard table and its matrix to each other and asks GitHub whether every `BLOCKED` row's issue is still open. That is a one-entry change to a test file, committed red before the documents existed. The dedicated checker with the Q-shaped rules (a `PASS` needs a run mutation and a date, a `BY HAND` row needs a name) is #998's, as it was for Q.
- **`e5a7d918`.** Not an ancestor of this `main`. Every `path:line` was re-read at `bc085e06`.

---

# 13. Definition of done

The functional definition of done is the two exit gates in section 5, which the matrix tracks line by line. The checklist obligations are section 7. The rules that make this section worth reading sceptically:

- A line is ticked only when something that can be run proves it, and the matrix row says what and whether that proof is gated or by hand.
- No row is green because nobody looked. All rows land `BLOCKED` with this file, and each flips in the pull request that lands its proof, never before.
- Nothing in the epic is done while an open issue carries its label. Closing #967 needs every sub-issue closed and the matrix without a `BLOCKED` row.
- What this epic cannot prove from here is stated rather than rounded up: no eleven-provider fleet and no real off-site bucket at scale were exercised, and store-listing screenshots need real hardware, a real installation and a real SFTP source. `index.html#honest` is where that stays honest.
