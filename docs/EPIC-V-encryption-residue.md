# EPIC V: Encrypted Retained Copies, the Cut List Closed Out

## Status

**Type:** EPIC / Detailed implementation specification
**Repository:** `retnd/retnd`
**Parent / predecessor EPICs:** EPIC N (#967, encrypted retained copies and key custody, whose section 8 is the list this epic judges), EPIC T (#1310, suite agility, which already built the re-encryption engine), EPIC Q (#953, scheduled scrubbing and recovery tests), EPIC E (#232, the FR-31 ladder and the rule that unattended egress is operator-initiated)
**Primary implementation root:** `core/internal/keycustody/`, `core/internal/placement/`, `core/service/`, `core/cmd/retnd/`
**Tracker issue:** #1512 (sub-issues #1513 through #1517: V1.1 #1513, V1.2 #1514, V1.3 #1515, V2.1 #1516, V2.2 #1517; this specification is #1511)
**FR numbering:** this specification claims **FR-105 through FR-107**. FR-1 through FR-24 are in `docs/EPIC.md`; FR-25 is an unclaimed hole and stays one; FR-26 is the `version` command; FR-27 through FR-35 are EPIC E's; FR-36 through FR-48 are EPIC L's; FR-51 through FR-58 are EPIC M's; FR-59 through FR-66 are EPIC N's; FR-67 through FR-74 are EPIC Q's; FR-75 through FR-82 are reserved to EPIC P; FR-83 through FR-90 are EPIC T's; FR-91 through FR-98 are EPIC U's; FR-99 through FR-104 are EPIC O's. `git grep -E 'FR-(10[5-9]|1[1-9][0-9])'` over `origin/main` finds only `docs/epic-checklist.md` saying FR-105 is the next free number, and no open issue or pull request claims one. The next free number after this block is **FR-108**. Nothing here renumbers an existing FR.
**Shape:** TWO phases, **five** sub-issues (not ten), per `docs/epic-checklist.md` section 1. Five is what survived; section 6 says why the other five slots stay empty.
**Measured on:** `origin/main` at `37711f3c` (the merge of EPIC O's specification, #904), read on 2026-11-19. EPIC N's own numbers are partly stale and section 2 says where.

**Reserved numbers.** The epic letter is **`V`**: `git grep` over `main`, every `epic-*` label, every issue title and every branch name finds `A` to `U` and nothing for `V`, and no prose mentions `EPIC V` or a `V1.1` style id (`H`, `I` and `J` are taken too: #623, #664, #728). One ADR, **`0036`**: `docs/adr/` ends at `0034-an-agent-is-a-scoped-client-of-the-api.md`, `0035` is EPIC O's (claimed in its merged specification, no file yet), and the gaps at `0024` to `0026`, `0031` and `0032` belong to open epics M and P and are left alone. **No migration**: `core/migrations/` is at `0022_agent_access.sql` and this epic adds none (section 8), so `0023` stays free. **No compat fixture and no config key**: the compat fixtures run `01` to `12` and `50` to `66`, EPIC O claims `13`, `14` and `67` to `69`, and this epic claims none, which section 8 argues. If an owner decision adds a config field, it takes `15` (valid) and `70` (invalid).

This document was written before any implementation exists, which is the order `docs/epic-checklist.md` section 1 asks for. The matrix, `docs/conformance/epic-v-matrix.md`, has every row `BLOCKED`, each naming the sub-issue it waits on.

---

# 1. Purpose

EPIC N cut seven things to fit two phases and said, of each, that it became a follow-up issue or nothing. Nobody has gone back and asked which. This epic does that, and the answer is that **most of the list should not be built**.

I judged each of the seven against the code on `origin/main` and against what an operator who keeps encrypted copies actually needs. Two survive as work, one of them small. Five are rejected, and the rejections are written down here with their reasons and held by guards, so that nobody re-proposes them from a stale reading of EPIC N's section 8.

What survives is the part of EPIC N's story an operator hits the day a key is compromised or retired: moving an external key's copies off the old key, and being able to see how much is left to move. The re-encryption engine exists already. What is missing is the plumbing around it.

The line this epic holds, as EPIC N held it: **I design no cryptography here**, and I add no feature whose benefit I cannot state from the code.

---

# 2. Current state, measured on `origin/main` at `37711f3c`

| Surface | What is actually there | The gap |
|---|---|---|
| **Re-encryption** | Built. EPIC T's `retnd encryption reencrypt` (`core/cmd/retnd/encryption.go`, `core/service/reencrypt.go`) selects every ACTIVE encrypted copy by exactly one of `--suite`, `--key` or `--deprecated`, plans with `--dry-run`, and runs through `placement.Engine.EncryptExisting` with `Plan.Reencrypt` (`core/internal/placement/retroencrypt.go`): the source is retired only after the new copy decrypt-verifies, it moves medium to medium only, one run at a time (`ErrReencryptRunning`), and nothing calls it on its own (`reencrypt.go:36`). | Nothing links it to a rotation. After a rotation the operator is on their own to know how many copies still sit under the old key. `docs/encrypted-copies.md:537` says so: "no automatic rotation". |
| **External key rotation** | `keycustody.Store.RotateExternal` (`core/internal/keycustody/externalregistry.go:113`) supersedes an external key's row and registers the new one in one durable write. | It has **no caller outside its tests** (`git grep RotateExternal` over non-test files finds only its own definition). `docs/encrypted-copies.md:301` still says "there is no command that rotates an external key yet", and describes the workaround: point the reference at another key, keep the old reference declared. Whether the old external row can then be retired is not something I could establish from the code: `RetireKey` refuses the active key (`core/internal/placement/keyretire.go:59`), and an external key whose reference moved on is only superseded if something calls `RotateExternal`. V1.2 starts from a red test that answers it. |
| **The rotation backlog** | `retnd_retained_copy_encrypted_copies{suite}` counts ACTIVE encrypted copies per suite (`core/internal/metrics/metrics.go:657`). `RetireKey` refuses a key an ACTIVE placement still records and names up to twenty copies (`keyretire.go:68`). | No count of copies **per key status**. After a rotation nobody can ask "how many copies are still under a key that is no longer active", which is the one number a key-compromise response needs. The retire refusal names copies but not the command that moves them. |
| **The command key source** | `secretref.Ref` is `file`, `env` or `command`, exactly one (`core/internal/secretref/secretref.go:160`), pinned by `TestRefFieldSetMatchesTheConfiguredOnes`; a `command` runs with a 15 second bound in its own process group (`secretref.go:100`, `:579`). `docs/encrypted-copies.md:534` says an external `command` "can reach" a KMS or Vault. | The sentence is a claim with no worked example and no test behind it. How many times a `command` key source runs per scrub, verify or re-encryption pass (a KMS call per copy is a bill and a rate limit) is not documented or asserted. |
| **Unattended reads of encrypted copies** | `automaticMediumClass = placement.Existence` (`core/internal/revalidate/checks.go:77`), asserted by `TestTheAutomaticCeilingIsAClassThisPassMayRunAgainstEveryMedium`, with a refusal at `:215` that fires if anyone raises it. The scheduled recovery test refuses a read that costs money (`core/internal/recoverytest/recoverytest.go:403`), and `costOf` treats only `MediumTypeLocalDir` as free (`:686`). | Not a gap I will close: section 3 item 5. |
| **Recovering an encrypted copy** | `recover` does not decrypt (`docs/recovery.md:682`); `retnd encryption decrypt` opens one copy at a time and `docs/recovery-without-a-terminal.md` section 6 documents the clean-machine procedure. Nothing under `core/service/recover*.go` or `core/internal/recoverytest/` references `copycrypt` or a placement's encryption record. | Not a gap I will close either: the capability exists as a deliberate, documented, per-copy step. Making `recover` decrypt would widen where plaintext lands on disk for no new capability. |
| **The kopia engine** | A `kopia` set is already encrypted by its repository passphrase; `required` on it answers `satisfied_by: "repository"` (`core/service/encryption.go:139`, `docs/encrypted-copies.md:38`) and the move engine skips it (`core/service/reencrypt.go:115`). | Nothing. The written answer EPIC N owed exists. |
| **Object names** | `MediumKey` derives `<prefix>/<source>/<set>/<artifact-name>` (FR-28), pinned by `TestMediumKeyLayout` (`core/internal/transport/medium_test.go:140`); ADR 0028 and `docs/encrypted-copies.md:42` state that a bucket holder can read names. | Nothing to build; section 3 item 4. |
| **Key lifecycle facts** | Durable, in `encryption_keys`, `recovery_bundle_exports` and `recovery_bundle_confirmations` (migration `0015`); created, rotated, exported, confirmed and retired are also `obs` events (`core/internal/obs/events.go:379-399`) in the activity feed, which keeps 30 days and a row bound (`core/internal/state/activity_events.go:214`). | Nothing I can name that an operator cannot do today; section 3 item 6. |
| **Compression** | None on the artifact copy path (`git grep -iE 'compress|zstd|gzip'` over `core/internal/copycrypt`, `core/internal/placement` and `core/internal/lifecycle` finds nothing). | Nothing, by design; section 3 item 7. |
| **Numbers that moved since EPIC N** | `core/migrations/` is at `0022` (EPIC N's table says `0013`); the product and every command are `retnd`; FR-99 to FR-104 are EPIC O's; the compat configs run `01` to `12` and `50` to `66`, with `09`, `10` and `11` being EPIC N's and T's encryption fixtures; the gate is `scripts/ci-local.sh` and nothing else (#1260). | Cited as they are now. |

---

# 3. The seven cut items, judged one by one

Each gets a disposition that is one of **built** (it becomes work in this epic), **rejected** (it is not built, and the reason is the content of this section), or **already built** (EPIC N or a later epic did it, and this epic adds at most a guard). Where something is rejected, a guard holds it rejected, so the decision is a test and not a sentence somebody forgets.

| # | Cut item | Disposition | Reason |
|---|---|---|---|
| 1 | A KMS or Vault key provider | **Rejected as a provider. Built as a worked, tested example of the existing `command` source** (V1.1). | A provider interface is a second custody model, and `TestRefFieldSetMatchesTheConfiguredOnes` fails on a fourth source for exactly that reason. The `command` source already reaches a KMS or Vault, so the honest deliverable is one example that is run in a test (against a stub command, since nobody here operates a real KMS) with its failure modes pinned: a timeout or non-zero exit is an unresolvable key and never a downgrade, and neither stderr nor the key reaches an error, a log line or a response. An interface nobody has exercised would be the less honest product. EPIC T already lists "any KMS or Vault provider" as a non-goal. |
| 2 | Re-encryption of historical copies on rotation | **Already built, explicit-run only** (EPIC T #1317). **The automatic variant is rejected. The plumbing around the built part is the work** (V1.2, V1.3). | The engine, its decrypt-verify-before-retire rule and its crash cells are EPIC T's. Re-encrypting automatically on rotation would be unattended egress for every copy at once, with a bill the operator did not approve (the EPIC E rule), and a key-compromise response is a judgement about order and urgency a cron cannot make. What is missing is real and small: the external key's rotation has no command, the remaining backlog is invisible, and the retire refusal does not say how to clear it. This is the one item with real value, because it is what an operator does after a key is compromised or when a key has to be retired. |
| 3 | A second cipher for the kopia engine's repositories | **Rejected.** | Kopia's own format already encrypts the repository under a passphrase, a second layer protects against nobody the first does not, and it would fork the passphrase custody story. The written answer to `required` on such a set is `satisfied_by: "repository"` and that is shipped. A guard holds it: the copy transform has no branch for a repository-engine set. |
| 4 | Encrypted object-name confidentiality | **Rejected. I looked for a defensible version and there is none.** | An opaque or keyed-hash name replaces the derived `MediumKey`, so convergence on an occupied key and restore-path derivation (both depend on the name being computable from the artifact) stop working unless the name is a deterministic keyed function, and then a listing can no longer be mapped to an artifact without the key, so a clean-machine `catalog rebuild` has no way to start. Every plaintext-era object in the bucket is orphaned from the rule that finds it. And the gain is partial at best: object sizes and timestamps stay visible and identify an artifact to anyone who knows the schedule. A half-hidden bucket that cannot be rebuilt without a key is worse for an operator than a visible one, and ADR 0028 already tells them to choose the destination knowing names are readable. A guard holds it: `MediumKey` for an encrypted copy is the plaintext-era value. |
| 5 | Automatic decrypt-verification on a schedule | **Rejected.** | EPIC E decided that unattended egress is not a default anybody asked for, and `Class.CostsEgress()` is true for exactly this check. A bounded, opt-in, budgeted sampling mode is the same bill with a timer: the operator who opts in has to pick a budget they cannot size without knowing the object sizes, and a sample of one object in fifty says almost nothing about the other forty-nine. The check already exists, operator-initiated, as `retnd encryption verify --allow-egress`. For the one free medium type (`local_dir`) the question is not about encryption: an unattended content read there would apply to plaintext copies too, which is a general assurance decision for EPIC Q's territory, not an encryption feature, and I do not smuggle it in here. The guard that holds it is the existing ceiling assertion, re-asserted. |
| 6 | A key-usage audit surface beyond the activity feed | **Rejected.** | I asked what an operator could not do today. Key create, rotate, export, confirm and retire are durable facts in three tables and are `obs` events in the feed; a per-copy decrypt-verify is a durable operation and an event. The things a new surface would add are who ran a decrypt, which the product has no actor for outside an agent token's `agent_audit` row, and a retention longer than the feed's 30 days, which is a retention decision and not an audit surface. A second ledger of key use would be a reporting feature with its own store and its own retention to defend. Nothing an operator is stuck without. |
| 7 | Compression beside encryption | **Rejected.** | Backup artifacts are already compressed by their producers (`.zst`, `.gz`, database dumps), so compressing before encrypting buys almost nothing and compressing after encrypting buys exactly nothing, because ciphertext does not compress. Compress-then-encrypt also leaks information through the output length, which is precisely the content signal encryption exists to withhold, and it would add a third representation to the two-digest model (plaintext digest, stored digest) and break the stored-size arithmetic the existence check relies on. A guard holds it: the copy path never compresses, asserted as stored size equal to plaintext size plus the suite's fixed overhead. |

The verdict in one sentence: **two of seven are worth building, one of those is a documented test, and the epic is correspondingly small.**

---

# 4. Adversarial review, five lenses

I argued this epic from five lenses, and the first verdicts were harsher than the epic's size suggests, because the temptation with a "cut list" is to build the list. Each lens was told to reject any item whose benefit I could not state from the code, and to reject any design whose failure mode destroys a copy.

## Lens 1, a scope sceptic

**Initial verdict: REJECT.** Seven items, ten slots: the shape invites padding, and a KMS interface, a scheduled sampler and an audit view each read as plausible and each has no operator who is blocked without it.
**Consensus: APPROVE AFTER REVISION.** Five of seven rejected with reasons in section 3, each held by a guard so the rejection cannot silently reverse. Five sub-issues, not ten.

## Lens 2, key compromise and retirement

**Initial verdict: REJECT.** "Re-encryption exists" is not "a rotation can be completed". An operator who rotates an external key and cannot retire the old one has not responded to a compromise. A re-encryption run that retires the old ciphertext does not unring a bell either: a bucket with versioning keeps the old object.
**Consensus: APPROVE AFTER REVISION.** V1.2 starts from a red test that says whether the old external row can be retired, and makes it so. V1.3 makes the backlog countable and names the way out. V2.2's documentation says plainly that re-encryption protects future reads of the new object and not copies a provider retains.

## Lens 3, the re-encryption crash matrix and its egress bill

**Initial verdict: REJECT** any new run mode that was not designed against its crash cells and its bill.
**Consensus: APPROVE AFTER REVISION.** This epic adds **no new run mode**. The engine, its kill-at-each-boundary cells and its operator-initiated rule are EPIC T's and are re-read, not rebuilt. V1.3 adds only a read-only estimate to `--dry-run` (bytes to read and bytes to write, per medium) if the plan does not already state it, so the bill is visible before the run. No scheduler, no automatic run, no run on rotation.

## Lens 4, secret safety of the command source

**Initial verdict: REJECT.** A worked KMS example is a place a key leaks (the command's stderr, its argv, an error that echoes output) and a place a rate limit bites (a call per copy).
**Consensus: APPROVE AFTER REVISION.** The example is run against a stub in a test, never against a real service. Its failure modes are pinned: timeout and non-zero exit are `ErrKeyUnresolvable`, never a weaker class; the command's stderr and stdout reach no error, log line, metric, response or screenshot; how often the command runs per pass is measured, documented and bounded, and if it is per copy the example says so and V1.1 adds a per-run cache that cannot outlive the run, because a KMS call per copy of a thousand-copy pass is the failure.

## Lens 5, parity, docs and gates

**Initial verdict: REJECT** a CLI verb with no browser control, a route with no `cliecho` entry, a rotation surface with no tooltip, and a matrix a closing check cannot hold.
**Consensus: APPROVE AFTER REVISION.** Both directions of parity are owned (V1.2, V1.3 and V2.1), the design precedes the UI, and V2.2 carries the gate and the closing check in the shape of EPICs N, T, U and O.

## Consensus findings, release requirements

| ID | Finding | Required change |
|---|---|---|
| VC-01 | Seven cut items, none judged since | Section 3's table, one disposition each, each rejected item held by a guard |
| VC-02 | A rejected item can return from a stale reading of EPIC N section 8 | ADR 0036 records the seven dispositions and the reasons; a guard per rejection (V-02, V-05, V-06, V-07, V-08) |
| VC-03 | A KMS provider is a second custody model | No provider and no fourth `Ref` field; `command` plus a worked, tested example; a planted fourth field turns the field-set test red |
| VC-04 | A command source can leak through stderr, argv or an error | The leak rows: stderr and output absent from every error, log, metric, response |
| VC-05 | A KMS call per copy is a bill and a rate limit | Resolution count per pass measured, documented and bounded |
| VC-06 | An external key's old row may never be retirable | V1.2: a red test first, then `RotateExternal` wired through service, CLI and API |
| VC-07 | After a rotation the remaining backlog is invisible | Per-key-status copy counts on CLI, API, health and metrics, with no key id as a label |
| VC-08 | The retire refusal does not say how to clear it | The refusal names `retnd encryption reencrypt --key <id>` |
| VC-09 | A re-encryption plan does not state its bill | `--dry-run` states bytes to read and write per medium, or V1.3 adds it |
| VC-10 | No new run mode, no automatic run | Held by the existing assertion that nothing calls `reencrypt` on its own, extended to rotation |
| VC-11 | Re-encryption does not recall copies a provider retains | Said in the documentation, with the reason |
| VC-12 | Compression must not creep in | Stored size equals plaintext size plus fixed suite overhead, asserted per suite |

---

# 5. Functional Requirements

## FR-105, The cut list is closed by decision and held by guards

Each of EPIC N's seven cut items has a recorded disposition in ADR 0036; a rejected item is held by a guard that goes red if it appears (a fourth `secretref.Ref` source, a changed `MediumKey` for an encrypted copy, a cipher branch for a repository-engine set, a compressing copy path, a raised automatic ceiling); and the `command` key source has a worked example run in a test, whose failure modes are fail-closed and leak nothing.

## FR-106, An external key can be rotated

Moving an external key to a new reference supersedes the old key's row and registers the new one in one durable write, copies recorded against the old key keep decrypting through any declared reference that still yields it, and the old key can be retired once no ACTIVE copy records it.

## FR-107, The rotation backlog is visible and has a way out

The number of ACTIVE encrypted copies recorded against each key that is no longer active is reportable on the CLI, the API, the health report and as a metric (never with a key id as a label); the retire refusal names `retnd encryption reencrypt --key <id>`; and `reencrypt --dry-run` states the bytes it would read and write per medium. Nothing starts a re-encryption on its own.

## Core invariants

- **Nothing here changes a stored byte.** No copy is re-encrypted by this epic's code on its own, no format changes, no key layout changes, and an `off` or `required` deployment that never rotates an external key behaves exactly as before.
- **Re-encryption stays operator-initiated.** Nothing calls it on rotation, on a schedule or on a health state.
- **No key on any surface.** The new surfaces carry key ids (not secret) and counts, never material, and no key id is a metric label.
- **A rejected item stays rejected by a test, not by a sentence.**
- **The canonical deployment does not move.** No port, mount, compose, provider or packaging edit.
- **No migration, no config key.** The backlog is computed from `placement_encryption` and `encryption_keys`; rotation is an operation, not a setting.
- **No rename of anything.** No FR, metric family, event constant or route.

---

# 6. What I cut, and why the epic is small

EPIC N cut seven items and this epic rejects or already-built-closes five of them. Five slots of the ten allowed stay empty on purpose, and I will not fill them: a sub-issue with no operator who is blocked without it is padding, and `docs/epic-checklist.md` section 1 asks for a cut list precisely so that a phase is not filled for its own sake.

Things I considered and did not take, so a reviewer can tell an absent item from an overlooked one:

- **A decrypting `recover`.** Rejected in section 2. The per-copy decrypt exists and is documented as the clean-machine procedure.
- **A bulk "decrypt everything" verb, key import over the API.** Listed in `docs/encrypted-copies.md:530-540` as not yet done. Each widens where plaintext or key material travels and has no operator blocked on it. Not built, not tracked here.
- **A key-rotation reminder or an automatic rotation.** `rotation_needed` already exists for suite changes (`core/service/encryption.go:318`); a cadence is the per-set answer EPIC N wrote "no" to.
- **Same-medium re-encryption.** EPIC T's non-goal, for a reason that holds: it needs a plaintext window or double storage on the one medium.

Everything cut becomes a follow-up issue or nothing. Here, it is nothing, and the dispositions are the record.

---

# 7. Phases, sub-issues and gates

Each line names every `docs/epic-checklist.md` section the sub-issue must satisfy and what satisfying it means here. Dependency order: #1511 (this specification), then #1513, then {#1514, #1515}, then #1516, then #1517. #1514 and #1515 are independent once ADR 0036 has fixed the dispositions. #1516's design gates its UI by the checklist's section 5 rule.

## Phase 1, decision, external key rotation and the backlog (`V:phase-1`)

- **V1.1 #1513, ADR 0036, the seven dispositions and the command key provider as a worked, tested example** (FR-105). The ADR; the stub-command example with the failure modes of VC-04 pinned; the measurement and bounding of how often a `command` key source runs per pass (VC-05); the guards of VC-02 and VC-12 (the field-set test re-asserted with a planted fourth field, `MediumKey` unchanged for an encrypted copy, no cipher branch for a repository-engine set, stored size equals plaintext size plus fixed suite overhead per registered suite, the automatic ceiling re-asserted). Satisfies section 1 (the dispositions as a decision a later reader can find), section 9 (ADR 0036; the `docs/encrypted-copies.md` sentences at `:301` and `:534` made true with the example), section 11 (no key reaches a log, an error or a screenshot) and section 12 (a mutation self-test per guard, with a negative control each).
- **V1.2 #1514, external key rotation** (FR-106). Starts from a red test that answers whether an external key whose reference moved can be retired; wires `keycustody.Store.RotateExternal` through the service, a CLI verb and an API route, in the shape of the managed rotation; the crash boundary of the one durable write; copies under the old key keep opening while a declared reference yields it. Satisfies section 3 (a `cliecho` entry, no gap sentence naming a shipped verb, the verb in the dispatch table and in `docs/site/reference.html`, matching refusals and exit codes), section 4 (the existing key events cover a rotation; no new high-volume event, asserted), section 6 (`openapi.json` with both bindings, the new route a named row in `core/agenttools`, on the deny list as a mutation), section 7 (no migration, journal truth) and section 12.
- **V1.3 #1515, the rotation backlog and the way out** (FR-107). The per-key-status copy count in the service, health, CLI, API and as one unlabelled metric family; the retire refusal naming the command; the `--dry-run` byte estimate if the plan lacks it; the re-read of EPIC T's kill-at-each-boundary cells for `reencrypt`, and the assertion that nothing calls `reencrypt` on rotation. Satisfies section 3, section 4 (`Result` on completion; the cursor-paged feed unaffected, asserted), section 6 (contract, bindings, agent catalogue row) and section 12.

### Phase 1 entry gate

- [ ] The letter `V` is held by nothing else. Checked 2026-11-19 against `docs/EPIC-*.md`, every `epic-*` label, every issue title and every branch name.
- [ ] FR-105 through FR-107 collide with nothing; the next free number was FR-105 on `origin/main` at `37711f3c`.
- [ ] `docs/adr/` has no `0036`; `core/migrations/` has no `0023`, and this epic adds none.
- [ ] The adversarial review is done and its twelve findings are release requirements (section 4).
- [ ] This specification and `docs/conformance/epic-v-matrix.md` are merged with every row `BLOCKED`, each naming its sub-issue and its falsification. That is #1511.
- [ ] `main` green.

### Phase 1 exit gate

- [ ] ADR 0036 records all seven dispositions with their reasons, and a guard per rejection exists and has been watched to fail against a planted violation.
- [ ] A `command` key source runs in a test against a stub, fails closed on a timeout and on a non-zero exit as `ErrKeyUnresolvable`, and leaks neither output nor stderr to any error, log, metric or response; the number of runs per pass is documented and asserted.
- [ ] `TestRefFieldSetMatchesTheConfiguredOnes` still goes red when a fourth source is planted.
- [ ] An external key rotation supersedes the old row and registers the new one in one write, a kill between the two leaves exactly one active key, and every pre-rotation copy still decrypt-verifies through a declared reference.
- [ ] The old external key can be retired once no ACTIVE copy records it, and is refused naming the copies before that.
- [ ] The per-key-status backlog count is correct after a rotation (including zero), is on the CLI, the API, the health report and as a metric with no key id label, and the retire refusal names `retnd encryption reencrypt --key <id>`.
- [ ] `reencrypt --dry-run` states bytes to read and write per medium.
- [ ] Nothing in this epic starts a re-encryption on its own, asserted.
- [ ] Parity holds both ways at the API and CLI for the new verb and route: a `cliecho` entry, an agent-catalogue row, matching refusals and exit codes, the verb in `reference.html`.
- [ ] `core/migrations/` still ends at `0022_agent_access.sql`; no config field and no compat fixture were added.

## Phase 2, design, web UI, end to end, documentation and the gate (`V:phase-2`)

- **V2.1 #1516, design note and web UI for external key rotation and the backlog** (FR-106, FR-107). The design note first (`docs/design/1516-...` as `.html` and `.png`, light, dark and narrow, "backlog still under an old key" as a state visibly distinct from "encrypted" and from "verified recoverable"), then the control on the keys card and the backlog on the key list, with `CommandEcho.tsx` showing the equivalent command. Satisfies section 3 (the browser direction, checked by hand), section 5 (design first; `SettingsPage.tsx`; frontend gate green), section 5 Tooltips (a registry entry and a three-part `fieldHelpCopy.ts` entry per explained control, effect read off the code, opt-out tested ON; a control whose effect the code cannot state gets neither) and section 6 (the generated client consumed).
- **V2.2 #1517, end-to-end, documentation, captures and the release gate** (FR-105, FR-106, FR-107). The `retnd-tests` suite for rotate then `reencrypt --key` then retire, for a managed and an external key, with `scripts/e2e/tests-repo.pin` moving in lockstep; the documentation including the honest sentence that re-encryption does not recall copies a provider retains (VC-11), the CHANGELOG, README and site pages the change touches, `index.html#honest`; the capture step; the new `gate_step` and its stub; the closing check and the matrix registered in `core/tests/compat/matrix_test.go`, which means reshaping the matrix to `V1.n` gate ids, `VV.n` ledger ids and a `### Phase 1 exit gate` heading in the same pull request, exactly as EPIC O defers it. Also corrects the sentence in `docs/epic-checklist.md` section 1 so the next free number reads FR-108. Satisfies section 1, section 8 (an empty diff over the canonical deployment, asserted with `scripts/release/contract-unchanged.sh`), section 9, section 10, section 11 (`docs/compliance/` and `docs/submission/`, where what is stored does not change, said so) and section 12.

### Phase 2 entry gate

- [ ] Phase 1 is exit-gated: every Phase 1 row of `docs/conformance/epic-v-matrix.md` is `PASS`, or `PARTIAL` with the reason written in the row.
- [ ] The design note is in `docs/design/` with its `.html` and `.png`, because no web implementation starts before it.
- [ ] `scripts/e2e/tests-repo.pin` is at a `retnd-tests` commit that contains the Phase 1 CLI cases.

### Phase 2 exit gate

- [ ] The design note and its `.png` exist with the reasoning and the date, in light, dark and narrow; every new text and background pair meets WCAG AA in both themes, with the measured ratios for the key pairs recorded in the pull request that adds the surface.
- [ ] The keys card carries the external rotation control and the backlog count; the frontend gate is green; every explained control has a registry tooltip and a three-part help entry, and the controls refused one are listed with their reasons.
- [ ] A suite in `retnd/retnd-tests` runs rotate, `reencrypt --key`, retire for a managed and an external key, each case failing against `main` before the change and passing after, and CLI and web agree on at least one refusal and one success.
- [ ] No key material appears on the new surfaces, with a planted canary turning its own row red and a permanent negative control.
- [ ] ADR 0036, the CHANGELOG entry naming the issue and what an existing deployment sees, README, the site pages the change touches, `index.html#honest`, the prose docs and the two sentences in `docs/encrypted-copies.md` have all landed, and the documentation states what re-encryption does not do.
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
| The command source fails closed | Downgrade a timeout to a weaker class, or echo stderr in an error; the example's own cases go red |
| An external rotation is one write | Supersede before registering, or the reverse; the kill cell leaves two active or no active key and goes red |
| The old key's copies still open | Drop the old key's row on rotation; a pre-rotation copy fails to decrypt-verify and goes red |
| The backlog count | Count retired copies as zero, or count by suite instead of key status; the backlog test goes red |
| No key id as a label | Emit the key id as a label; the label-vocabulary test goes red |
| Nothing starts a re-encryption on its own | Call `reencrypt` from `RotateEncryptionKey`; the assertion goes red |
| The matrix itself cannot lie | A row with no falsification, a `BLOCKED` row naming no sub-issue, a `BLOCKED` row citing a closed issue |

Negative controls, because a guard that only ever fires is as useless as one that never does: a `command` source that returns a valid key resolves; a rotation with no copies under the old key retires cleanly; a backlog of zero is reported as zero rather than as absent.

---

# 9. Checklist coverage map

| Section | Owner | What owning it means here |
|---|---|---|
| 1 The shape of the epic | #1511, #1517 | Two phases, five sub-issues, the cut list as section 3, letter `V`, FR-105 to FR-107, five lenses, gates as checkable claims, a matrix with every row `BLOCKED` at entry and none at exit |
| 2 Settings | #1511 argues it, no owner | **No setting is added.** Rotation is an operation. Backlog is computed. There is no global default or per-set override to write, and the written answer is no, with the reason (nothing here is a property of a backup set). No fixture is claimed |
| 3 CLI / web parity | #1514, #1515, #1516 | A `cliecho` entry per new route, no gap sentence naming a shipped verb, the verb in the dispatch table and in `reference.html`, matching refusals, and the ungated browser direction checked by hand and recorded |
| 4 Progress and the feed | #1514, #1515 | Rotation is already `key_rotated`; no new high-volume event, asserted; the backlog is a read, not a progress stream |
| 5 Web UI design | #1516 | The design note first, then the keys card control; frontend gate green |
| 5 Tooltips | #1516 | A registry and three-part entry per explained control, effect stated from the code, opt-out tested ON, and a refusal list |
| 6 The API contract | #1514, #1515 | `openapi.json`, both bindings regenerated, drift green, `docs/api/contract.md`, the new route in `core/agenttools` |
| 7 Storage, state, migrations | #1514, #1515 | **No migration.** Asserted at the exit gate: `core/migrations/` ends at `0022`. Journal truth stays journal truth |
| 8 Providers | #1517, as an assertion of no change | Empty diff over the canonical deployment; no capability, acceptance or packaging edit |
| 9 Documentation | #1513 (ADR), #1517 | ADR 0036, `encrypted-copies.md`, README, site, CHANGELOG, the honest section, package-doc baselines, the two sentences this epic falsifies |
| 10 Screenshots and GIFs | #1517 | A capture step in the right script, mock API, clock pinned, an unchanged script run first; no store-listing screen changes, stated |
| 11 Compliance | #1513, #1517 | No key in a log or error; `docs/compliance/` and `docs/submission/` say what is stored did not change |
| 12 Gates | #1513, #1517 | `scripts/ci-local.sh`; the new `gate_step` and stub; a mutation self-test and negative control per guard; no new former-name identifier; tests red before green |
| 12 Fixtures | #1513, #1517 | Values chosen against the wrong implementation: a backlog fixture with copies under two retired keys and one active (a count by suite would pass a single-key fixture), a stub command that fails only on its second call (a per-pass cache would hide it) |

The contrast rule applies wherever V2.1 adds styled output. This pull request adds none, since it is prose in Markdown.

---

# 10. Non-goals

A KMS or Vault provider or any fourth key source. A second cipher for repository-engine sets. Object-name confidentiality. Compression of any kind. Scheduled or sampled decrypt-verification that costs egress. A separate key-usage ledger. Automatic re-encryption, on rotation or on a schedule. Same-medium re-encryption. A decrypting `recover`. Any cryptographic construction of this project's own. Renaming any FR, metric family, event constant or route.

---

# 11. Compatibility and migration summary

- A deployment that never rotates an external key and never runs `reencrypt` sees nothing: no new behaviour, no new file, no schema change.
- No migration, no config key, so there is no new one-way door under `KnownFields(true)`.
- The new verb and route are additions. Nothing is renamed.
- The new metric family counts copies and carries no key id as a label.

---

# 12. Decisions this specification took

- **Five rejections.** Items 3, 4, 5, 6 and 7 are rejected, and item 1 is rejected as a provider. If the owner wants any reopened, it needs a new epic with an operator who is blocked without it.
- **Item 5's local-disk case.** An unattended content read of a `local_dir` medium would be a general assurance change, not an encryption one. I left it for EPIC Q's territory and did not file it.
- **No config key.** The backlog and the rotation are operations, so there is no knob to place globally or per set.
- **EPIC N's table is stale in places** (migrations, the product name, FR ranges, the gate). Section 2 cites the current values and I did not edit EPIC N.
- **FR-105 is the next free number.** Correcting the checklist's sentence to FR-108 is V2.2's, as EPIC O deferred its own.

---

# 13. Definition of done

The functional definition of done is the two exit gates in section 7, which the matrix tracks line by line. The rules that make this section worth reading sceptically:

- A line is ticked only when something that can be run proves it, and the matrix row says what and whether that proof is gated or by hand.
- No row is green because nobody looked. All rows land `BLOCKED` with this file and each flips in the pull request that lands its proof.
- Nothing in the epic is done while an open issue carries its label. Closing #1512 needs every sub-issue closed and a matrix without a `BLOCKED` row.
- What this epic cannot prove from here is stated rather than rounded up: no real KMS or Vault was operated, and no eleven-provider fleet or real off-site bucket was exercised. `index.html#honest` is where that stays honest.
