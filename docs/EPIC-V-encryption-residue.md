# EPIC V: Encrypted Retained Copies, the Cut List Closed Out

## Status

**Type:** EPIC / Detailed implementation specification
**Repository:** `retnd/retnd`
**Parent / predecessor EPICs:** EPIC N (#967, encrypted retained copies and key custody, whose section 8 is the list this epic judges), EPIC T (#1310, suite agility, which already built the re-encryption engine), EPIC Q (#953, scheduled scrubbing and recovery tests), EPIC E (#232, the FR-31 ladder and the rule that unattended egress is operator-initiated)
**Primary implementation root:** `core/internal/keycustody/`, `core/internal/placement/`, `core/internal/secretref/`, `core/internal/config/`, `core/service/`, `core/cmd/retnd/`
**Tracker issue:** #1512 (sub-issues #1513 through #1517: V1.1 #1513, V1.2 #1514, V1.3 #1515, V2.1 #1516, V2.2 #1517; this specification is #1511)
**Designs:** `docs/design/EPIC-V-detailed-designs.md` (#1526, PR #1535), one per sub-issue, read against the code. Where this specification and the designs disagree, section 12 records which one won and why; after the 2026-11-19 revision they should not disagree.
**FR numbering:** this specification claims **FR-105 through FR-107**. FR-1 through FR-24 are in `docs/EPIC.md`; FR-25 is an unclaimed hole and stays one; FR-26 is the `version` command; FR-27 through FR-35 are EPIC E's; FR-36 through FR-48 are EPIC L's; FR-51 through FR-58 are EPIC M's; FR-59 through FR-66 are EPIC N's; FR-67 through FR-74 are EPIC Q's; FR-75 through FR-82 are reserved to EPIC P; FR-83 through FR-90 are EPIC T's; FR-91 through FR-98 are EPIC U's; FR-99 through FR-104 are EPIC O's. `git grep -E 'FR-(10[5-9]|1[1-9][0-9])'` over `origin/main` finds only `docs/epic-checklist.md` saying FR-105 is the next free number, and no open issue or pull request claims one. The next free number after this block is **FR-108**. Nothing here renumbers an existing FR.
**Shape:** TWO phases, **five** sub-issues (not ten), per `docs/epic-checklist.md` section 1. Five is what survived; section 6 says why the other five slots stay empty.
**Measured on:** `origin/main` at `37711f3c` (the merge of EPIC O's specification, #904), read on 2026-11-19. EPIC N's own numbers are partly stale and section 2 says where.

**Reserved numbers.** The epic letter is **`V`**: `git grep` over `main`, every `epic-*` label, every issue title and every branch name finds `A` to `U` and nothing for `V`, and no prose mentions `EPIC V` or a `V1.1` style id (`H`, `I` and `J` are taken too: #623, #664, #728). One ADR, **`0036`**: `docs/adr/` ends at `0034-an-agent-is-a-scoped-client-of-the-api.md`, `0035` is EPIC O's (claimed in its merged specification, no file yet), and the gaps at `0024` to `0026`, `0031` and `0032` belong to open epics M and P and are left alone. **No migration**: `core/migrations/` is at `0022_agent_access.sql` and this epic adds none (section 7), so `0023` stays free. **One opt-in config key, `retained_copy_encryption.previous_external_keys`, with compat fixtures `15` (valid) and `70` (invalid)**: the compat fixtures run `01` to `12` and `50` to `66`, EPIC O claims `13`, `14` and `67` to `69`, and no branch on `origin` holds a `15-*` or `70-*` fixture. Section 11 says why the key exists and why it is a bounded one-way door.

This document was written before any implementation exists, which is the order `docs/epic-checklist.md` section 1 asks for. The matrix, `docs/conformance/epic-v-matrix.md`, has every row `BLOCKED`, each naming the sub-issue it waits on.

---

# 1. Purpose

EPIC N cut seven things to fit two phases and said, of each, that it became a follow-up issue or nothing. Nobody has gone back and asked which. This epic does that, and the answer is that **most of the list should not be built**.

I judged each of the seven against the code on `origin/main` and against what an operator who keeps encrypted copies actually needs. Two survive as work, one of them small. Five are rejected, and the rejections are written down here with their reasons and held by guards, so that nobody re-proposes them from a stale reading of EPIC N's section 8.

What survives is the part of EPIC N's story an operator hits the day a key is compromised or retired: taking an external key out of service so it can actually be retired, moving its copies off it, and being able to see how much is left to move. The re-encryption engine exists already. What is missing is the lifecycle and the plumbing around it.

The line this epic holds, as EPIC N held it: **I design no cryptography here**, and I add no feature whose benefit I cannot state from the code.

---

# 2. Current state, measured on `origin/main` at `37711f3c`

| Surface | What is actually there | The gap |
|---|---|---|
| **Re-encryption** | Built. EPIC T's `retnd encryption reencrypt` (`core/cmd/retnd/encryption.go`, `core/service/reencrypt.go`) selects every ACTIVE encrypted copy by exactly one of `--suite`, `--key` or `--deprecated`, plans with `--dry-run`, and runs through `placement.Engine.EncryptExisting` with `Plan.Reencrypt` (`core/internal/placement/retroencrypt.go`): the source is retired only after the new copy decrypt-verifies, it moves medium to medium only, one run at a time (`ErrReencryptRunning`), and nothing calls it on its own (`reencrypt.go:36`). | Nothing links it to a rotation. After a rotation the operator is on their own to know how many copies still sit under the old key. `docs/encrypted-copies.md:537` says so: "no automatic rotation". |
| **External key lifecycle** (F-1) | `RegisterExternal` writes every external key `active` (`core/internal/keycustody/externalregistry.go:97`) and returns an existing row whatever its status (`:92-93`). Migration `0015` constrains only managed keys to one active row (`core/migrations/0015_retained_copy_encryption.sql:94-100`), so several external keys may be active by design. | **External keys never leave `active`, so one can never be retired.** `RetireKey` refuses an active key (`core/internal/placement/keyretire.go:111`; `:59` is the sentinel), and its refusal says "rotate to a new key", which does nothing for an external key. The only path to `superseded` was `keycustody.Store.RotateExternal`, which has no caller outside its own test, models a single active external slot the schema does not have, and is deleted by V1.2 (section 12, D-2). |
| **Where an old external reference lives** (F-2) | The declared references are the deployment's provider plus each set's override (`core/service/keycustody.go:72-93`), and a set override is only legal under `required`. | **Nowhere to keep the old reference.** With one global reference and sets inheriting it, replacing the reference drops the old one, and every copy under the old key stops opening. `docs/encrypted-copies.md:295` says to "keep the old reference declared under a different name or path", which is only achievable by giving every set its own override. `docs/encrypted-copies.md:301` still says "there is no command that rotates an external key yet". |
| **The rotation backlog** | `retnd_retained_copy_encrypted_copies{suite}` counts ACTIVE encrypted copies per suite (`core/internal/metrics/metrics.go:657`). **The per-key count already exists on the API**: `EncryptionKeyStatus.CopiesRecording` (`core/apicontract/contract.gen.go:3213`), filled per key in `keyStatus` (`core/service/encryption.go:465-469`) and drawn in the keys card's Copies column. `RetireKey` refuses a key an ACTIVE placement still records and names up to twenty copies (`keyretire.go:68`). | The CLI's `key list` does not print the count (`core/cmd/retnd/encryption.go:626-635`), and says "no managed keys" for a list that holds external ones too. The retire refusal names copies but not the command that moves them, and the dry run states no bytes. |
| **The command key source** | `secretref.Ref` is `file`, `env` or `command`, exactly one (`core/internal/secretref/secretref.go:160`), pinned by `TestRefFieldSetMatchesTheConfiguredOnes`; a `command` runs with a 15 second bound in its own process group (`secretref.go:100`, `:579`). `docs/encrypted-copies.md:534` says an external `command` "can reach" a KMS or Vault. | The sentence is a claim with no worked example and no test behind it. |
| **How often a `command` runs** (F-4, F-5) | Every `WithKey` on an external id walks the declared references and runs each until one matches (`externalregistry.go:144-171`). `keyStatus` calls `store.Probe` for every key (`core/service/encryption.go:460`), which for an external key resolves every declared reference, although `core/service/keycustody.go:234-236` says such resolution is "for the alert tick only, never for a request". | By the designs' reading, a `command` source runs **about ten times per re-encrypted copy** (three per copy in a dry run), and **on every load of the key list** (`GET /encryption/keys`, `retnd encryption key list`). A one to three second resolver times ten per copy is hours of wall time over a thousand copies, before any rate limit. |
| **A command's stderr** (F-6) | `secretref` puts a failing command's stderr in its error on purpose (`secretref.go:106-111`, `:627`). | That error is copied into `withExternalRecord`'s refusal (`externalregistry.go:169`), then into `ExternalKeyAtRisk.Reason`, the health report and the alert detail the notifier sends, and into the feed. A resolver with `set -x` would print the key itself to stderr, and that line would leave the machine by email. |
| **Unattended reads of encrypted copies** | `automaticMediumClass = placement.Existence` (`core/internal/revalidate/checks.go:77`), asserted by `TestTheAutomaticCeilingIsAClassThisPassMayRunAgainstEveryMedium`, with a refusal at `:215` that fires if anyone raises it. The scheduled recovery test refuses a read that costs money (`core/internal/recoverytest/recoverytest.go:403`), and `costOf` treats only `MediumTypeLocalDir` as free (`:686`). | Not a gap I will close: section 3 item 5. |
| **Recovering an encrypted copy** | `recover` does not decrypt (`docs/recovery.md:682`); `retnd encryption decrypt` opens one copy at a time and `docs/recovery-without-a-terminal.md` section 6 documents the clean-machine procedure. Nothing under `core/service/recover*.go` or `core/internal/recoverytest/` references `copycrypt` or a placement's encryption record. | Not a gap I will close either: the capability exists as a deliberate, documented, per-copy step. Making `recover` decrypt would widen where plaintext lands on disk for no new capability. |
| **The kopia engine** | A `kopia` set is already encrypted by its repository passphrase; `required` on it answers `satisfied_by: "repository"` (`core/service/encryption.go:139`, `docs/encrypted-copies.md:38`) and the move engine skips it (`core/service/reencrypt.go:115`). | Nothing. The written answer EPIC N owed exists. |
| **Object names** | `MediumKey` derives `<prefix>/<source>/<set>/<artifact-name>` (FR-28), pinned by `TestMediumKeyLayout` (`core/internal/transport/medium_test.go:140`); ADR 0028 and `docs/encrypted-copies.md:42` state that a bucket holder can read names. | Nothing to build; section 3 item 4. |
| **Key lifecycle facts** | Durable, in `encryption_keys`, `recovery_bundle_exports` and `recovery_bundle_confirmations` (migration `0015`); created, rotated, exported, confirmed and retired are also `obs` events (`core/internal/obs/events.go:379-399`) in the activity feed, which keeps 30 days and a row bound (`core/internal/state/activity_events.go:214`). | Nothing I can name that an operator cannot do today; section 3 item 6. |
| **Compression** | None on the artifact copy path (`git grep -iE 'compress|zstd|gzip'` over `core/internal/copycrypt`, `core/internal/placement` and `core/internal/lifecycle` finds nothing). | Nothing, by design; section 3 item 7. |
| **Re-encrypting a recent artifact** (F-10) | Re-encryption is medium to medium only (`retroencrypt.go:209-215`), and the move engine refuses an artifact with two ACTIVE placements (`core/internal/placement/engine.go:513-517`). | Recent artifacts that still have their plaintext local copy cannot be re-encrypted until that copy expires. In a compromise response an operator was stuck waiting. That was a new run mode, so I cut it from this epic and filed it as #1545; it has since landed as `retnd encryption reencrypt --from-local` (PR #1634, section 6). |
| **Numbers that moved since EPIC N** | `core/migrations/` is at `0022` (EPIC N's table says `0013`); the product and every command are `retnd`; FR-99 to FR-104 are EPIC O's; the compat configs run `01` to `12` and `50` to `66`, with `09`, `10` and `11` being EPIC N's and T's encryption fixtures; the gate is `scripts/ci-local.sh` and nothing else (#1260). | Cited as they are now. |

---

# 3. The seven cut items, judged one by one

Each gets a disposition that is one of **built** (it becomes work in this epic), **rejected** (it is not built, and the reason is the content of this section), or **already built** (EPIC N or a later epic did it, and this epic adds at most a guard). Where something is rejected, a guard holds it rejected, so the decision is a test and not a sentence somebody forgets.

| # | Cut item | Disposition | Reason |
|---|---|---|---|
| 1 | A KMS or Vault key provider | **Rejected as a provider. Built as a worked, tested example of the existing `command` source** (V1.1). | A provider interface is a second custody model, and `TestRefFieldSetMatchesTheConfiguredOnes` fails on a fourth source for exactly that reason. The `command` source already reaches a KMS or Vault, so the honest deliverable is one example that is run in a test (against a stub command, since nobody here operates a real KMS) with its failure modes pinned: a timeout or non-zero exit is an unresolvable key and never a downgrade, stdout never surfaces, and stderr surfaces only on the interactive `provider test` answer (VC-04). The same item owns the two costs the designs found: the source runs about ten times per re-encrypted copy and on every key-list load (F-4, F-5). An interface nobody has exercised would be the less honest product. EPIC T already lists "any KMS or Vault provider" as a non-goal. |
| 2 | Re-encryption of historical copies on rotation | **Already built, explicit-run only** (EPIC T #1317). **The automatic variant is rejected. The plumbing around the built part is the work** (V1.2, V1.3). | The engine, its decrypt-verify-before-retire rule and its crash cells are EPIC T's. Re-encrypting automatically on rotation would be unattended egress for every copy at once, with a bill the operator did not approve (the EPIC E rule), and a key-compromise response is a judgement about order and urgency a cron cannot make. What is missing is the **external key lifecycle**: a reference kept for reading (so the old copies keep opening after the provider moves on), a `supersede` verb (so an external key can leave `active` and later be retired), and an engine refusal to write under a non-active external key. Around it, the small plumbing: the CLI does not print the per-key count the API already returns, the retire refusal does not say how to clear it, and the dry run does not state its bill. This is the one item with real value, because it is what an operator does after a key is compromised or when a key has to be retired. |
| 3 | A second cipher for the kopia engine's repositories | **Rejected.** | Kopia's own format already encrypts the repository under a passphrase, a second layer protects against nobody the first does not, and it would fork the passphrase custody story. The written answer to `required` on such a set is `satisfied_by: "repository"` and that is shipped. A guard holds it: the copy transform has no branch for a repository-engine set. |
| 4 | Encrypted object-name confidentiality | **Rejected. I looked for a defensible version and there is none.** | An opaque or keyed-hash name replaces the derived `MediumKey`, so convergence on an occupied key and restore-path derivation (both depend on the name being computable from the artifact) stop working unless the name is a deterministic keyed function, and then a listing can no longer be mapped to an artifact without the key, so a clean-machine `catalog rebuild` has no way to start. Every plaintext-era object in the bucket is orphaned from the rule that finds it. And the gain is partial at best: object sizes and timestamps stay visible and identify an artifact to anyone who knows the schedule. A half-hidden bucket that cannot be rebuilt without a key is worse for an operator than a visible one, and ADR 0028 already tells them to choose the destination knowing names are readable. A guard holds it: `MediumKey` for an encrypted copy is the plaintext-era value. |
| 5 | Automatic decrypt-verification on a schedule | **Rejected.** | EPIC E decided that unattended egress is not a default anybody asked for, and `Class.CostsEgress()` is true for exactly this check. A bounded, opt-in, budgeted sampling mode is the same bill with a timer: the operator who opts in has to pick a budget they cannot size without knowing the object sizes, and a sample of one object in fifty says almost nothing about the other forty-nine. The check already exists, operator-initiated, as `retnd encryption verify --allow-egress`. For the one free medium type (`local_dir`) the question is not about encryption: an unattended content read there would apply to plaintext copies too, which is a general assurance decision for EPIC Q's territory, not an encryption feature, and I do not smuggle it in here. The guard that holds it is the existing ceiling assertion, re-asserted. |
| 6 | A key-usage audit surface beyond the activity feed | **Rejected.** | I asked what an operator could not do today. Key create, rotate, export, confirm and retire are durable facts in three tables and are `obs` events in the feed; a per-copy decrypt-verify is a durable operation and an event. The things a new surface would add are who ran a decrypt, which the product has no actor for outside an agent token's `agent_audit` row, and a retention longer than the feed's 30 days, which is a retention decision and not an audit surface. A second ledger of key use would be a reporting feature with its own store and its own retention to defend. Nothing an operator is stuck without. |
| 7 | Compression beside encryption | **Rejected.** | Backup artifacts are already compressed by their producers (`.zst`, `.gz`, database dumps), so compressing before encrypting buys almost nothing and compressing after encrypting buys exactly nothing, because ciphertext does not compress. Compress-then-encrypt also leaks information through the output length, which is precisely the content signal encryption exists to withhold, and it would add a third representation to the two-digest model (plaintext digest, stored digest) and break the stored-size arithmetic the existence check relies on. A guard holds it: the copy path never compresses, asserted as stored size equal to plaintext size plus the suite's fixed overhead. |

The verdict in one sentence: **two of seven are worth building, one of those is a documented test plus the resolver costs it uncovered, and the epic is correspondingly small.**

---

# 4. Adversarial review, five lenses

I argued this epic from five lenses, and the first verdicts were harsher than the epic's size suggests, because the temptation with a "cut list" is to build the list. Each lens was told to reject any item whose benefit I could not state from the code, and to reject any design whose failure mode destroys a copy.

## Lens 1, a scope sceptic

**Initial verdict: REJECT.** Seven items, ten slots: the shape invites padding, and a KMS interface, a scheduled sampler and an audit view each read as plausible and each has no operator who is blocked without it.
**Consensus: APPROVE AFTER REVISION.** Five of seven rejected with reasons in section 3, each held by a guard so the rejection cannot silently reverse. Five sub-issues, not ten.

## Lens 2, key compromise and retirement

**Initial verdict: REJECT.** "Re-encryption exists" is not "a rotation can be completed". An operator who rotates an external key and cannot retire the old one has not responded to a compromise. A re-encryption run that retires the old ciphertext does not unring a bell either: a bucket with versioning keeps the old object.
**Consensus: APPROVE AFTER REVISION.** The code answered the question this lens opened with: an external key can never be retired today (F-1), and in the ordinary deployment its old reference has nowhere to live (F-2). V1.2 starts from a red test that shows it, then adds the lifecycle: keep the old reference for reading, supersede, drain, retire, forget, each step refused when done out of order. V1.3 prints the backlog the API already counts and names the way out. V2.2's documentation says plainly that re-encryption protects future reads of the new object and not copies a provider retains, and that an artifact which still has a local copy cannot be moved yet (#1545).

## Lens 3, the re-encryption crash matrix and its egress bill

**Initial verdict: REJECT** any new run mode that was not designed against its crash cells and its bill.
**Consensus: APPROVE AFTER REVISION.** This epic adds **no new run mode**. The engine, its kill-at-each-boundary cells and its operator-initiated rule are EPIC T's and are cited, not rebuilt. The read-only estimate in `--dry-run` (bytes to read and an upper bound on bytes to write and read back, per medium, plus a count of copies with no recorded size) landed early with #1545, so the bill is visible before the run and V1.3 adds nothing to it. No scheduler, no automatic run, no run on rotation. The one run mode an operator would want (re-encrypting an artifact from its local plaintext copy) is exactly the kind this lens rejects without its own crash matrix, so it is #1545 and not here.

## Lens 4, secret safety of the command source

**Initial verdict: REJECT.** A worked KMS example is a place a key leaks (the command's stderr, its argv, an error that echoes output) and a place a rate limit bites (a call per copy).
**Consensus: APPROVE AFTER REVISION.** The example is run against a stub in a test, never against a real service. Its failure modes are pinned: timeout and non-zero exit are `ErrKeyUnresolvable`, never a weaker class; stdout never surfaces; stderr surfaces only on `provider test` and never in an alert, a health reason, a feed row, a log line or a metric (today it reaches all of those, F-6). The count is measured: about ten runs per re-encrypted copy (F-4), so V1.1 adds a resolution session that runs each distinct `command` reference at most once per operation and dies with it, and the key list stops running commands at all (F-5).

## Lens 5, parity, docs and gates

**Initial verdict: REJECT** a CLI verb with no browser control, a route with no `cliecho` entry, a rotation surface with no tooltip, and a matrix a closing check cannot hold.
**Consensus: APPROVE AFTER REVISION.** Both directions of parity are owned (V1.2, V1.3 and V2.1), the design precedes the UI, and V2.2 carries the gate and the closing check in the shape of EPICs N, T, U and O.

## Consensus findings, release requirements

| ID | Finding | Required change |
|---|---|---|
| VC-01 | Seven cut items, none judged since | Section 3's table, one disposition each, each rejected item held by a guard |
| VC-02 | A rejected item can return from a stale reading of EPIC N section 8 | ADR 0036 records the seven dispositions and the reasons; a guard per rejection (V-02, V-06, V-07, V-08, V-09) |
| VC-03 | A KMS provider is a second custody model | No provider and no fourth `Ref` field; `command` plus a worked, tested example; a planted fourth field turns the field-set test red, and a planted third provider kind on `config.RetainedCopyKeyProvider` turns `TestTheKeyProviderHasExactlyTwoKinds_1513` red, since that is where a KMS provider would actually arrive |
| VC-04 | A command source can leak through stderr, argv or an error | Stdout never surfaces. Stderr surfaces only on `provider test`, never in an alert, a health reason, a feed row, a log line or a metric. Any token on stderr that parses as a key under a registered suite is withheld on `provider test` too |
| VC-05 | A `command` run per copy is wall time, a rate limit and audit-log noise | At most one run per distinct `command` reference per operation, measured and asserted. The fixture is a stub that fails on its second call: it proves the cache exists, because only an uncached path calls it twice |
| VC-06 | An external key can never be retired, and the old reference has nowhere to live | V1.2: a red test first, then a reference kept for reading (`previous_external_keys`, written by `--keep-previous`), a `supersede` verb, and the engine refusal of VC-14 |
| VC-07 | After a rotation the remaining backlog is invisible on the CLI | The per-key copy count the API already returns, printed by `key list` with a footer naming the way out |
| VC-08 | The retire refusal does not say how to clear it | The refusal names `retnd encryption reencrypt --key <id>` |
| VC-09 | A re-encryption plan does not state its bill | `--dry-run` states bytes to read and write per medium (landed with #1545, PR #1634) |
| VC-10 | No new run mode, no automatic run | Held by the existing assertion that nothing calls `reencrypt` on its own, extended to rotation |
| VC-11 | Re-encryption does not recall copies a provider retains | Said in the documentation, with the reason |
| VC-12 | Compression must not creep in | Stored size equals plaintext size plus fixed suite overhead, asserted per suite |
| VC-13 | The key list runs a `command` on every page load (F-5) | `GET /encryption/keys` and `key list` run no command: an external key's `resolvability` comes from the last alert pass, or is `not_checked` with `checked_at` absent and a fixed `reason` |
| VC-14 | The engine would write new copies under a superseded external key (F-3) | Nothing is written under a superseded external key: the copy is refused before staging, and the at-risk alert names the set and the key |

---

# 5. Functional Requirements

## FR-105, The cut list is closed by decision and held by guards

Each of EPIC N's seven cut items has a recorded disposition in ADR 0036; a rejected item is held by a guard that goes red if it appears (a fourth `secretref.Ref` source or a third key provider kind, a changed `MediumKey` for an encrypted copy, a cipher branch for a repository-engine set, a compressing copy path, a raised automatic ceiling); and the `command` key source has a worked example run in a test, whose failure modes are fail-closed; it runs at most once per distinct reference per operation, the key list runs it never, stdout never surfaces, and stderr surfaces only on `provider test`.

## FR-106, An external key can be superseded and retired

An external key can be superseded once no key provider yields it; nothing new is written under a superseded external key; its copies keep opening through a reference declared for reading; it can be retired once no ACTIVE copy records it.

## FR-107, The rotation backlog is visible and has a way out

The number of ACTIVE encrypted copies recorded against each key is reportable on the CLI and the API, per key and never per suite; the retire refusal names `retnd encryption reencrypt --key <id> --dry-run`; and `reencrypt --dry-run` states the bytes it would read and write per medium. Nothing starts a re-encryption on its own.

## Core invariants

- **Nothing here changes a stored byte.** No copy is re-encrypted by this epic's code on its own, no format changes, no key layout changes, and an `off` or `required` deployment that never supersedes an external key behaves exactly as before, apart from the key list no longer running commands.
- **Re-encryption stays operator-initiated.** Nothing calls it on rotation, on supersede, on a schedule or on a health state.
- **No key on any surface.** The new surfaces carry key ids (not secret), reference descriptions (`Ref.String`, which leaves a command's arguments out) and counts, never material. No key id is a metric label, and this epic adds no metric at all.
- **A rejected item stays rejected by a test, not by a sentence.**
- **The canonical deployment does not move.** No port, mount, compose, provider or packaging edit.
- **No migration; one opt-in config key, deployment-wide, read only for decrypting; a file that never used `--keep-previous` re-marshals byte for byte.** The key is `retained_copy_encryption.previous_external_keys`. The backlog is computed from `placement_encryption` and `encryption_keys`; supersede is a status change `0015`'s CHECK already allows.
- **No rename of anything.** No FR, metric family, event constant or route. One event constant is added, `key_superseded`.

---

# 6. What I cut, and why the epic is small

EPIC N cut seven items and this epic rejects or already-built-closes five of them. Five slots of the ten allowed stay empty on purpose, and I will not fill them: a sub-issue with no operator who is blocked without it is padding, and `docs/epic-checklist.md` section 1 asks for a cut list precisely so that a phase is not filled for its own sake.

Things I considered and did not take, so a reviewer can tell an absent item from an overlooked one:

- **A decrypting `recover`.** Rejected in section 2. The per-copy decrypt exists and is documented as the clean-machine procedure.
- **A bulk "decrypt everything" verb, key import over the API.** Listed in `docs/encrypted-copies.md:530-540` as not yet done. Each widens where plaintext or key material travels and has no operator blocked on it. Not built, not tracked here.
- **A key-rotation reminder or an automatic rotation.** `rotation_needed` already exists for suite changes (`core/service/encryption.go:318`); a cadence is the per-set answer EPIC N wrote "no" to.
- **Same-medium re-encryption.** EPIC T's non-goal, for a reason that holds: it needs a plaintext window or double storage on the one medium.
- **Re-encrypting an artifact that still has a local plaintext copy: cut, became issue #1545 because it is a new run mode, and has since landed as `reencrypt --from-local` (PR #1634).** An operator in a compromise response is stuck without it (F-10), so this one is not nothing. It uploads from the local plaintext copy the engine already holds and adds no new place plaintext lands, and it needed a crash matrix of its own, which is why it was built outside V.
- **A health field and a metric for the backlog: cut, no reader.** A backlog under a superseded key is the normal state after every routine rotation, so nothing can alert on it and the dashboard cannot degrade on it. The per-key count is on the API (`copies_recording`) and V1.3 puts it on the CLI.
- **A "rotate external" route doing the config move and the supersede in one call.** Two durable writes to two stores (config and journal), and a crash between them leaves a state the route's re-run cannot recognise, because the old reference is no longer the provider. The browser chains the two explicit steps instead (V2.1).

Everything cut becomes a follow-up issue or nothing. Here, one became an issue (#1545, since landed) and the rest are nothing, and the dispositions are the record.

---

# 7. Phases, sub-issues and gates

Each line names every `docs/epic-checklist.md` section the sub-issue must satisfy and what satisfying it means here. Dependency order: #1511 (this specification), then #1513, then {#1514, #1515}, then #1516, then #1517. #1514 and #1515 are independent once ADR 0036 has fixed the dispositions. #1516's design gates its UI by the checklist's section 5 rule.

## Phase 1, decision, external key lifecycle and the backlog (`V:phase-1`)

- **V1.1 #1513, ADR 0036, the seven dispositions and the command key provider as a worked, tested example** (FR-105). The ADR, which also records the resolution session (D2), where stderr may appear (D3) and the external key lifecycle (D4) so a reader finds them in one place. The resolution session: one `command` run per distinct reference per operation, in memory only, opened by the function that owns the operation and ended with it, failures cached too. `secretref.CommandError` (whose `Error()` keeps today's text byte for byte) and `secretref.Brief` (the same chain without stderr); every error leaving `keycustody.WithExternalKey` goes through `Brief`; `keycustody.DiagnoseExternal` keeps the full error for `provider test` only, with any stderr token that a registered suite accepts as a key replaced. The key list reads the alert pass's last answer for external keys instead of resolving, so `EncryptionKeyStatus` in `api/v1/openapi.json` gains the value `not_checked` for `resolvability` (a documented string today, "resolves, unreachable or not_declared", not an enum, so the description and every consumer that switches on it grow by one) and a `checked_at` timestamp. The `reason` for `not_checked` is a fixed sentence, never anything a resolver printed. The stub-command example and the `docs/encrypted-copies.md` section that replaces `:301` and `:534`. The guards cite the existing controls by label (`a fourth source field on the external key reference`, `the object key of a copy gains an encrypted suffix`, `the automatic revalidation ceiling raised to content`, `NV an unresolvable key downgraded to an existence pass`) and add only three: the kopia selector control, the compression size equation per registered suite, and the provider-kind shape test `TestTheKeyProviderHasExactlyTwoKinds_1513`. Satisfies section 1 (the dispositions as a decision a later reader can find), section 6 (the new `resolvability` value and `checked_at`), section 9 (ADR 0036; the two sentences; the `keycustody` package doc gains the session paragraph), section 11 (no key reaches a log, an error or a screenshot, now including stderr) and section 12 (a mutation self-test per new guard, with a negative control each).
- **V1.2 #1514, external key lifecycle: a reference kept for reading, supersede, and the engine refusal** (FR-106). Starts from a red test showing an external key whose reference moved cannot be retired and its refusal points at a verb that cannot help. The config field `retained_copy_encryption.previous_external_keys` (at most 16 entries, each exactly one source, none equal to a provider reference, deployment-wide only, never written under), appended by `externalRefs()` so its references are read for decrypting and nothing else; compat fixtures `15` and `70`; the one-way door sentence in the CHANGELOG. `keep_previous` on `PATCH /encryption/policy` and `PUT /backup-sets/{source}/{set}/encryption` (the replaced reference moves into the list in the same atomic config write), `forget_previous {index, source}` (refused while that reference still opens ACTIVE copies). The route `POST /encryption/keys/{id}/supersede` and `retnd encryption key supersede <id>`, refused while any writer (the deployment's provider or any set's override, whatever the mode) yields the key. The engine refusal: nothing new is written under a non-active external key, refused before staging, and `Resolvability` reports it so the alert names the set. One new event, `key_superseded`, with a `Result` on success and refusal, and its sentence in `ui/shared/src/encryption/feedText.ts`; the same file's `key_retired` sentence says "managed key retired" (`feedText.ts:60-61`), which becomes false the first time an external key retires, so it loses the word "managed". `provider` on `EncryptionKeyStatus`. `RetireKey`'s refusal for an active external key names `supersede`. The unused external rotation method on `keycustody.Store` deleted (D-2), its useful half kept as `TestACopyUnderASupersededExternalKeyOpensThroughAKeptReference_1514`. Satisfies section 2 (one deployment-wide field, no per-set override, with the reason; the one-way door sentence; fixtures `15` and `70`), section 3 (a `cliecho` entry, no gap sentence naming a shipped verb, the verb in the dispatch table and in `docs/site/reference.html`, matching refusals and exit codes), section 4 (`key_superseded`, low volume, so paging is unaffected), section 6 (`openapi.json` with both bindings, the route in the agent catalogue's keys row and covered by the existing deny pattern), section 7 (no migration: `superseded` is already in `0015`'s CHECK) and section 12.
- **V1.3 #1515, the rotation backlog and the way out** (FR-107). `retnd encryption key list` prints `N ACTIVE copies` per key (including 0) and a footer naming `reencrypt --key <id> --dry-run` when any non-active key has copies; the retire refusal names the dry run; the source guard that nothing starts a re-encryption on its own. `media`, `sizes_unknown`, the size bound and the CLI lines of the dry run already landed with #1545 (PR #1634), so V1.3 owns only the retire hint, the key-list print, the guard that nothing starts a re-encryption on its own and its mutation plant. EPIC T's kill-at-each-boundary cells for `reencrypt` are cited in the matrix as they stand. No health field and no metric (section 6). Satisfies section 3 (the CLI print; the refusal identical on both routes), section 4 (nothing new in the feed; a dry run is a read), section 6 (the retire refusal identical on both routes), section 7 (no migration, the count is computed) and section 12.

### Phase 1 entry gate

- [ ] The letter `V` is held by nothing else. Checked 2026-11-19 against `docs/EPIC-*.md`, every `epic-*` label, every issue title and every branch name.
- [ ] FR-105 through FR-107 collide with nothing; the next free number was FR-105 on `origin/main` at `37711f3c`.
- [ ] `docs/adr/` has no `0036`; `core/migrations/` has no `0023`, and this epic adds none.
- [ ] The adversarial review is done and its fourteen findings are release requirements (section 4).
- [ ] This specification and `docs/conformance/epic-v-matrix.md` are merged with every row `BLOCKED`, each naming its sub-issue and its falsification. That is #1511.
- [ ] `main` green.

### Phase 1 exit gate

- [ ] ADR 0036 records all seven dispositions with their reasons, and a guard per rejection exists and has been watched to fail against a planted violation.
- [ ] A `command` key source runs in a test against a stub, fails closed on a timeout and on a non-zero exit as `ErrKeyUnresolvable`, runs at most once per distinct reference per operation (asserted with a counting stub), never surfaces its stdout, and surfaces its stderr only on `provider test`, with a key-shaped token withheld even there.
- [ ] `GET /encryption/keys` runs no command; an external key no alert pass has probed reads `not_checked`.
- [ ] `TestRefFieldSetMatchesTheConfiguredOnes` and `TestTheKeyProviderHasExactlyTwoKinds_1513` still go red when a fourth source or a third provider kind is planted.
- [ ] Supersede is refused while a writer yields the key; a copy under a superseded external key is refused before staging; the alert names it; and every pre-supersede copy still decrypt-verifies through a reference kept for reading.
- [ ] The old external key can be retired once no ACTIVE copy records it, and is refused naming the copies before that; forgetting its kept reference is refused while it still opens ACTIVE copies.
- [ ] The per-key backlog count is correct after a supersede or a rotation (including zero) on the CLI and the API, and the retire refusal names `retnd encryption reencrypt --key <id> --dry-run`.
- [x] `reencrypt --dry-run` states bytes to read and write per medium, and counts the copies with no recorded size instead of guessing (landed with #1545, PR #1634).
- [ ] Nothing in this epic starts a re-encryption on its own, asserted.
- [ ] Parity holds both ways at the API and CLI for the new verb, route and fields: a `cliecho` entry, an agent-catalogue row, matching refusals and exit codes, the verb in `reference.html`.
- [ ] `core/migrations/` still ends at `0022_agent_access.sql`; the only compat fixtures added are `15` and `70`; the compat capture of a config that never used `--keep-previous` is byte-identical.

## Phase 2, design, web UI, end to end, documentation and the gate (`V:phase-2`)

- **V2.1 #1516, design note and web UI for the external key lifecycle and the backlog** (FR-106, FR-107). The design note first (`docs/design/1516-...` as `.html` and `.png`, light, dark and narrow). Then: the "Retired" bug (F-8: `KEY_STATUS_WORDS` draws `superseded` as "Retired" next to a Retire button, `ui/shared/src/encryption/EncryptionCard.tsx:81`, `:339`), fixed with three distinct words, Active, Superseded and Retired; a Holder column fed by V1.2's `provider` (the table is labelled "Managed keys" though it lists both); the supersede control, offered from durable state (an active external key no provider yields) so a closed tab re-offers it; the keep checkbox on the external dialog; the "Kept for reading" readout with forget; re-encrypt by key from the key row, with V1.3's byte lines; a word on the card for `not_checked`; and the captures, moved here from V2.2 because this is the pull request that changes the screens. The backlog is a count on the key row, not a per-copy chip: a per-copy marker would sit on every copy written before any routine rotation, for ever. `CommandEcho.tsx` shows the equivalent command for every action. Satisfies section 3 (the browser direction, checked by hand), section 5 (design first; `SettingsPage.tsx`; frontend gate green), section 5 Tooltips (a registry entry and a three-part `fieldHelpCopy.ts` entry per explained control, effect read off the code, opt-out tested ON; a control whose effect the code cannot state gets neither), section 6 (the generated client consumed) and section 10 (the capture step, mock API, clock pinned, an unchanged script run first).
- **V2.2 #1517, end-to-end, documentation and the release gate** (FR-105, FR-106, FR-107). The `retnd-tests` suite for the external lifecycle (keep, supersede, `reencrypt --key`, retire, forget; out-of-order refusals; an interrupted run) and the managed rotate, `reencrypt --key`, retire, with `scripts/e2e/tests-repo.pin` moving in lockstep. **The managed case passes on `main` except for the retire hint and the dry-run bytes, and those two assertions are its red**; the external cases fail on `main` outright (no verb, no field). The documentation: the two procedures ("Retiring an external key" and "After a key is compromised"); the four honest sentences (re-encryption writes to another medium and needs one; it reads about twice the data; it cannot move an artifact that still has a local copy, see #1545; it does not recall copies a provider retains, VC-11); the CHANGELOG, README and site pages the change touches, `index.html#honest`; the new `gate_step` and its stub; the closing check (which also holds the compat fixtures to `15` and `70` and nothing more) and the matrix registered in `core/tests/compat/matrix_test.go`, which means reshaping the matrix to `V1.n` gate ids, `VV.n` ledger ids and a `### Phase 1 exit gate` heading in the same pull request, exactly as EPIC O defers it. Also corrects the sentence in `docs/epic-checklist.md` section 1 so the next free number reads FR-108. Captures are V2.1's. Satisfies section 1, section 8 (an empty diff over the canonical deployment, asserted with `scripts/release/contract-unchanged.sh`), section 9, section 11 (`docs/compliance/` and `docs/submission/`: what is stored does not change, apart from references kept in config, where references already live) and section 12.

### Phase 2 entry gate

- [ ] Phase 1 is exit-gated: every Phase 1 row of `docs/conformance/epic-v-matrix.md` is `PASS`, or `PARTIAL` with the reason written in the row.
- [ ] The design note is in `docs/design/` with its `.html` and `.png`, because no web implementation starts before it.
- [ ] `scripts/e2e/tests-repo.pin` is at a `retnd-tests` commit that contains the Phase 1 CLI cases.
- [x] The local-copy run mode (#1545) exists and shipped as `reencrypt --from-local` (PR #1634), so V2.2's documentation points at it rather than promises it.

### Phase 2 exit gate

- [ ] The design note and its `.png` exist with the reasoning and the date, in light, dark and narrow; every new text and background pair meets WCAG AA in both themes, with the measured ratios for the key pairs recorded in the pull request that adds the surface.
- [ ] The keys card draws Superseded and Retired as different words, shows the holder, and carries supersede, keep, forget and re-encrypt by key; the frontend gate is green; every explained control has a registry tooltip and a three-part help entry, and the controls refused one are listed with their reasons; the captures are re-recorded in the same pull request.
- [ ] A suite in `retnd/retnd-tests` runs the external lifecycle and the managed rotate, `reencrypt --key`, retire; each external case fails against `main` before the change, the managed case fails only on the retire hint and the dry-run bytes, and all pass after; CLI and web agree on at least one refusal and one success.
- [ ] No key material appears on the new surfaces, with a planted canary turning its own row red and a permanent negative control.
- [ ] ADR 0036, the CHANGELOG entry naming the issue, what an existing deployment sees and the one-way door sentence for `previous_external_keys`, README, the site pages the change touches, `index.html#honest`, the prose docs, the two procedures and the two sentences in `docs/encrypted-copies.md` have all landed, and the documentation states what re-encryption does not do.
- [ ] The canonical deployment diff is empty across `container/compose.yaml`, the eleven files under `apps/*/compose/`, `apps/unraid/template/`, `apps/truenas/catalog/` and the four `distribution/packaging/*.json`.
- [ ] `scripts/ci-local.sh` is green with the new check as its own `gate_step` and a stub in `scripts/tests/ci-local-gate.test.sh`; every new guard has a mutation self-test with a negative control.
- [ ] `docs/conformance/epic-v-matrix.md` has no `BLOCKED` row, every `PASS` row's falsification has been run and watched to fail, every `BY HAND` row says who checked what and when, and a closing check in the shape of `scripts/release/check-epic-u-closed.sh` holds the file to that.

---

# 8. TDD contract and planted violations

Every guard has a planted violation that must be run and watched to fail before its row goes `PASS`. The full table with test names is `docs/conformance/epic-v-matrix.md`.

| Guard | Planted violation that proves it fires |
|---|---|
| One custody model, no fourth secret source | Add a fourth field to `secretref.Ref`; `TestRefFieldSetMatchesTheConfiguredOnes` goes red |
| Object names unchanged | Change `MediumKey` for an encrypted copy; the medium contract suite goes red |
| No cipher branch for a repository-engine set | Route a `kopia` set through the copy transform; the `satisfied_by: "repository"` test goes red |
| No compression | Compress before or after the transform; stored size no longer equals plaintext size plus the fixed overhead |
| The automatic ceiling stays `Existence` | Raise `automaticMediumClass`; the ceiling test and the refusal at `checks.go:215` go red |
| The command source fails closed | Downgrade a timeout to a weaker class; the example's own cases go red |
| One run per reference per operation | `WithExternalKey` ignores the session, or a failed resolution is not cached; the counting stub sees a second run and goes red |
| The key list runs no command | Restore `store.Probe` for external rows ("the key list probes an external key again"); `TestTheKeyListRunsNoCommand_1513` goes red |
| Stderr stays off durable surfaces | `Brief` returns the full text ("a command's stderr in the brief error"); the stderr canary appears in the alert detail, health reason, feed or log and goes red |
| A key on stderr is withheld | Drop the redaction ("a key on stderr shown by provider test"); a fixture identity printed to stderr reaches the `provider test` answer and goes red |
| Nothing is written under a superseded external key | Delete the status check in `externalKey` ("a superseded external key written under"); the next copy is staged and the test goes red |
| Supersede checks every writer | Skip the writer check ("supersede ignores the writers"); a set override still yielding the key is superseded anyway and goes red. The fixture has the deployment provider on the new key and one set's override on the old, so a check of the deployment provider alone fails |
| The old key's copies still open | Drop the append in `externalRefs()` ("kept references not declared for reading"); a copy under the superseded key fails to decrypt-verify and goes red |
| The backlog count | Count by suite instead of by key ("the backlog counted by suite"); a fixture of three keys of one suite with 4, 7 and 5 copies goes red |
| Nothing starts a re-encryption on its own | Call `reencrypt` from `RotateEncryptionKey`; the assertion goes red |
| The matrix itself cannot lie | A row with no falsification, a `BLOCKED` row naming no sub-issue, a `BLOCKED` row citing a closed issue |

Negative controls, because a guard that only ever fires is as useless as one that never does: a `command` source that returns a valid key resolves; a session over a `file` reference reads the file every call (the cache is `command` only); superseding a key no reference yields succeeds; superseding twice writes once; a key with no copies retires cleanly; forgetting a reference that no longer resolves is allowed; a patch without `keep_previous` writes the config it writes today, byte for byte; a backlog of zero is reported as zero rather than as absent.

---

# 9. Checklist coverage map

| Section | Owner | What owning it means here |
|---|---|---|
| 1 The shape of the epic | #1511, #1517 | Two phases, five sub-issues, the cut list as section 3, letter `V`, FR-105 to FR-107, five lenses, gates as checkable claims, a matrix with every row `BLOCKED` at entry and none at exit |
| 2 Settings | #1514 | **One deployment-wide field, no per-set override, with the reason**: `retained_copy_encryption.previous_external_keys`, written only by `--keep-previous`, read only for decrypting. The per-set answer is no, because a reference kept for reading is a property of a key, and any set's copy may be under any key. The one-way door sentence goes in the CHANGELOG. Fixtures `15` (valid: a `file` and a `command` entry) and `70` (invalid: one entry naming two sources) |
| 3 CLI / web parity | #1514, #1515, #1516 | A `cliecho` entry per new route, no gap sentence naming a shipped verb, the verb in the dispatch table and in `reference.html`, matching refusals, and the ungated browser direction checked by hand and recorded |
| 4 Progress and the feed | #1514, #1515 | **One new event, `key_superseded`, with a `Result` on success and refusal; low volume**, so paging is unaffected. `key_rotated` cannot be reused: its pinned meaning is a new active key superseding the previous one, and a supersede has no new key. The backlog is a read, not a progress stream |
| 5 Web UI design | #1516 | The design note first, then the keys card control; frontend gate green |
| 5 Tooltips | #1516 | A registry and three-part entry per explained control, effect stated from the code, opt-out tested ON, and a refusal list |
| 6 The API contract | #1513, #1514, #1515 | `openapi.json`, both bindings regenerated, drift green, `docs/api/contract.md`: the supersede route, `keep_previous`, `forget_previous`, the kept list on the policy view, `provider`, `not_checked`, `checked_at` (all on `EncryptionKeyStatus` or the policy), the new route in `core/agenttools` |
| 7 Storage, state, migrations | #1514, #1515 | **No migration.** `superseded` is already in `0015`'s CHECK. Asserted at the exit gate: `core/migrations/` ends at `0022`. Journal truth stays journal truth |
| 8 Providers | #1517, as an assertion of no change | Empty diff over the canonical deployment; no capability, acceptance or packaging edit |
| 9 Documentation | #1513 (ADR), #1517 | ADR 0036, `encrypted-copies.md`, README, site, CHANGELOG, the honest section, package-doc baselines, the two sentences this epic falsifies |
| 10 Screenshots and GIFs | #1516 | A capture step in `docs/site/tools/capture-encryption.mjs`, mock API, clock pinned, an unchanged script run first, in the pull request that changes the screens; `encryption-08-rotated` and `encryption-21-key-copies-count` re-recorded because their word changes; no store-listing screen changes, stated |
| 11 Compliance | #1513, #1517 | No key in a log or error; `docs/compliance/` and `docs/submission/` say what is stored did not change |
| 12 Gates | #1513, #1517 | `scripts/ci-local.sh`; the new `gate_step` and stub; a mutation self-test and negative control per guard; no new former-name identifier; tests red before green |
| 12 Fixtures | #1513, #1514, #1515 | Values chosen against the wrong implementation: a backlog fixture of three keys of one suite, two superseded with 4 and 7 copies and one active with 5 (a count by suite gives 16 for every key); a stub command that fails on its second call (it proves the cache exists, because only an uncached path calls it twice); a writer-check fixture with a set override on the old key (a check of the deployment provider alone passes the wrong implementation) |

The contrast rule applies wherever V2.1 adds styled output. This pull request adds none, since it is prose in Markdown.

---

# 10. Non-goals

A KMS or Vault provider or any fourth key source. A second cipher for repository-engine sets. Object-name confidentiality. Compression of any kind. Scheduled or sampled decrypt-verification that costs egress. A separate key-usage ledger. Automatic re-encryption, on rotation or on a schedule. Same-medium re-encryption. A decrypting `recover`. Any cryptographic construction of this project's own. Renaming any FR, metric family, event constant or route. A health field or metric for the backlog. Automatic supersede on a reference change. An un-supersede (a key superseded after a compromise must not come back because somebody pasted an old path; an operator who superseded the wrong key mints or points at a new one). Re-encrypting an artifact with a local plaintext copy (issue #1545).

---

# 11. Compatibility and migration summary

- A deployment that never supersedes an external key and never runs `reencrypt` sees almost nothing: no new file, no schema change. What it does see: the key list stops running `command` resolvers and may say `not_checked` until the first alert pass; a failing resolver's stderr leaves alerts, health and the feed; the keys card says Superseded where it said Retired.
- No migration; one opt-in config key, `retained_copy_encryption.previous_external_keys`, deployment-wide, read only for decrypting. It is a one-way door under `KnownFields(true)`: an older build cannot load a file that carries it. Only a deployment that uses `--keep-previous` writes it, `omitempty` keeps every other file byte for byte, and the way back is deleting the key by hand, which the CHANGELOG says along with the version.
- The new verb, route, fields and the `key_superseded` event are additions. Nothing is renamed. One unused method is deleted (D-2); it had no caller outside its own test.
- No new metric family.

---

# 12. Decisions this specification took

- **Five rejections.** Items 3, 4, 5, 6 and 7 are rejected, and item 1 is rejected as a provider. If the owner wants any reopened, it needs a new epic with an operator who is blocked without it.
- **Item 5's local-disk case.** An unattended content read of a `local_dir` medium would be a general assurance change, not an encryption one. I left it for EPIC Q's territory and did not file it.
- **EPIC N's table is stale in places** (migrations, the product name, FR ranges, the gate). Section 2 cites the current values and I did not edit EPIC N.
- **FR-105 is the next free number.** Correcting the checklist's sentence to FR-108 is V2.2's, as EPIC O deferred its own.

## The six decisions from the designs, answered 2026-11-19

The designs (`docs/design/EPIC-V-detailed-designs.md`, #1526) read each sub-issue against the code and came back with six questions. These are the answers, and the sections above are written to them.

| # | Question | Answer | Why |
|---|---|---|---|
| D-1 | One config field, `retained_copy_encryption.previous_external_keys`? | **Yes, as designed in V1.2.** One deployment-wide list, written only by an explicit `--keep-previous`, read only for decrypting; at most 16 entries; each names exactly one source; none equal to a provider reference (the same file, env or command argv, which is what `keycustody.refKey` hashes); forget by `{index, source}` so two racing edits cannot remove the wrong one. Compat fixtures `15` and `70`. | Without it an external key cannot be rotated in the ordinary deployment (F-2) and can never be retired (F-1). The door is accepted because it is opt-in and `omitempty`. A reference in `state.db` or a separate state file would be argv the daemon executes, configured outside the file an operator reviews. Passing the old reference to one `reencrypt` run instead leaves scrubs saying "Key missing" and the at-risk alert firing for weeks on every routine rotation. |
| D-2 | Delete the unused external rotation method on `keycustody.Store` (section 2 names it)? | **Yes.** | No caller outside its test, and it encodes a one-active-slot model migration `0015` does not have. Wiring it would still drop the old reference from config, so F-2 would be untouched. Its useful half moves into `TestACopyUnderASupersededExternalKeyOpensThroughAKeptReference_1514`. |
| D-3 | One new event, `key_superseded`? | **Yes.** One constant, one `Logger` method, one feed sentence ("External key x3f… superseded: nothing new is written under it"), a `Result` on success and refusal, low volume. | `key_rotated` means a new active key superseding the previous one, and its feed text says "managed key rotated: new copies use…". A supersede has no new key, so reusing it would make the feed lie. |
| D-4 | Re-encrypt artifacts that still have a local plaintext copy? | **A follow-up issue, #1545, outside V (since landed as `--from-local`, PR #1634).** | An operator in a compromise response is stuck without it (F-10), so it is not nothing. It is a new run mode with its own crash cells, and both phases are full. The issue says the upload reads the local plaintext copy the engine already holds and adds no new place plaintext lands. |
| D-5 | Drop the backlog health field and metric? | **Yes.** The CLI print, the retire hint and the dry-run bytes stay. | Nonzero is the normal state after every routine rotation, so nothing can alert on it and the dashboard cannot degrade on it. The per-key count is already on the API. |
| D-6 | Add `not_checked` to the key list's `resolvability`? | **Yes**, plus `checked_at`. | It is what lets `GET /encryption/keys` stop running resolvers on every load (F-5). `checked_at` is a timestamp, and the `reason` for `not_checked` is a fixed sentence, never anything a resolver printed. V2.1 owes it a word on the card. |

Dissent worth keeping: one view was that the field should wait until a second deployment with external keys exists, shipping the per-run reference first; it lost because that fallback's failure mode lands on the one deployment that does. Another wanted D-4 inside V2.2; it lost on the epic's size. A third wanted an unlabelled gauge of copies under non-active keys for fleet graphs; it lost because nobody could name a threshold to alert on.

---

# 13. Definition of done

The functional definition of done is the two exit gates in section 7, which the matrix tracks line by line. The rules that make this section worth reading sceptically:

- A line is ticked only when something that can be run proves it, and the matrix row says what and whether that proof is gated or by hand.
- No row is green because nobody looked. All rows land `BLOCKED` with this file and each flips in the pull request that lands its proof.
- Nothing in the epic is done while an open issue carries its label. Closing #1512 needs every sub-issue closed and a matrix without a `BLOCKED` row.
- What this epic cannot prove from here is stated rather than rounded up: no real KMS or Vault was operated, and no eleven-provider fleet or real off-site bucket was exercised. `index.html#honest` is where that stays honest.
