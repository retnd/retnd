# EPIC Q: Artifact Recovery, Scheduled Scrubbing and Recoverability Assurance

## Status

**Type:** EPIC / Detailed implementation specification
**Repository:** `retnd/retnd`
**Parent / predecessor EPICs:** EPIC A (#1, backup engine), EPIC E (#232, storage mediums per retention tier), EPIC K (#779, embedded incremental engine), EPIC L (#807, scripted backup workflows)
**Primary implementation root:** `core/`
**Tracker issue:** #953 (sub-issues #976 through #988; this specification is #954)
**FR numbering:** this specification continues the product's FR series at **FR-67**, and claims through **FR-74**. FR-1 through FR-24 are defined in `docs/EPIC.md`; FR-25 is an unclaimed hole and stays one; FR-26 is the `version` command; FR-27 through FR-35 are EPIC E's; FR-36 through FR-48 are EPIC L's; FR-45 through FR-50 are claimed by EPIC O's open spec PR #904. The next free number after this block is **FR-75**, which EPIC P takes. Nothing here renumbers an existing FR.
**Measured on:** `main` at `1cd17849`, 2026-10-11. #953 was written against `e5a7d918`; that commit is an ancestor of this one, 1336 commits back, and every `path:line` below was re-read at `1cd17849`. Where a number moved or a claim stopped being true, section 2 says so.

**On the letter.** The source draft was written as "EPIC O, Artifact Recovery, Scheduled Scrubbing and Recoverability Assurance" on 2026-09-13. The letter `O` was taken by #898 (observability, open, five open sub-issues) before the draft was scheduled, so this epic is `Q` and every `O1`/`O2` task id in that draft is `Q1.x`/`Q2.x` here, in the draft's own order. Anybody holding the draft needs this sentence to reconcile the letters. The draft itself is not in the tree; #953 and the ten sub-issue bodies are the source for everything below.

**Reserved numbers.** Two ADRs, `0029` and `0030`; `docs/adr/` ends at `0023` and `0024` is reserved to #904, so they collide with nothing. One migration, `0016`; `core/migrations/` ends at `0013_workflow_lifecycle.sql` (`core/migrations/`), and `0014` and `0015` belong to the two epics landing beside this one (M and N).

This document was written before the implementation was finished, which is the order `docs/epic-checklist.md` section 1 asks for, but not before it started: ten implementation pull requests (#1050 to #1060, listed in section 5) were already open when I wrote it, and I read them. Section 9 records every place the issue text and what was built differ, and section 10 holds the questions I did not answer for the author of the code. The matrix, `docs/conformance/epic-q-matrix.md`, has every row `BLOCKED`, including rows whose proof exists on an open branch, because a row that went green in the pull request that wrote it would be the failure the matrix exists to prevent.

---

# 1. Purpose

An operator who has lost the thing they were backing up cannot get a backup back out of this product from either interface. `docs/recovery.md:18-21` says so: "restoring an artifact is not an automated command ... That is the permanent shape rather than a workaround", and `docs/site/reference.html:263` repeats it for the browser ("There is no restore here and there is no download"). Meanwhile the scheduled re-verification that would tell them whether the copy they cannot fetch is still good is fully written and never runs.

This EPIC does four things and holds one line.

- It hands an operator the retained bytes, from the terminal, from `api/v1` and from the browser, without making the product application-aware.
- It gives the scrub that already exists a driver, a bound and a per-copy durable record.
- It runs the operator's own validator against a recovered copy, in isolation.
- It keeps **Stored**, **Content verified** and **Recovery tested** as three separately-durable claims that no surface may collapse into one green tick.
- The line it holds: nothing is green because nobody looked, and nothing spends money without being asked.

---

# 2. Current state, measured on `main` at `1cd17849`

This is the section that decided the epic's shape. Three of the five review verdicts in section 3 turned on the draft describing work as missing that is already written, or as easy that the tree forbids. Every claim about the tree has a `path:line`. A claim I could not verify is written as unknown.

## 2.1 Getting bytes back

- Two verbs exist and neither is a download. `retnd restore` is FR-34's archival thaw: `core/service/restore.go:16-41` calls it "the one operation in this package that nothing here executes", records what was asked for, and re-derives progress from the provider on every read. The reference page says the same in its own words, "writes nothing anywhere" (`docs/site/reference.html:234`). #953 attributes that quotation to `restore.go`; it is on the reference page.
- `retnd snapshot restore` extracts a Kopia snapshot into a local directory on the engine host (`TargetPath`, `core/service/snapshotrestore.go:88-89`) and refuses any set whose engine is not `kopia` (`core/service/snapshotrestore.go:166`).
- No route in this repository streams a byte. A search of non-test Go under `apps/` and `core/` for `Content-Disposition` and `ServeContent` finds nothing, and the only `io.Copy` into a writer that the search surfaced is a test harness decorator (`core/tests/crashmatrix/harness/decorators.go:409`), not a `ResponseWriter`. I did not search `ui/` or the packaging tree, because neither can serve engine bytes. For an artifact-engine backup set there is no automated way to obtain the artifact from either interface.
- Three documents say this out loud and independently: `docs/recovery.md:18-21`, and `docs/site/reference.html:263` and `:274`. #953 cites the last two as `:264` and `:275`; they moved up one line since `e5a7d918`.

## 2.2 Submitting the work

- `POST /api/v1/operations` is registered with `requireCSRF, requireDestructiveGate(gate)` (`apps/common/webhost/router.go:395`). The only `DestructiveGate` this repository ships is `NotYetImplementedGate`, whose `Passed()` is `return false` (`apps/common/webhost/gate.go:111-114`), and its own comment says no flag can change that until #92 lands (`apps/common/webhost/gate.go:102-110`).
- So every action on that route is refused `403 DESTRUCTIVE_OPERATIONS_DISABLED` on every deployment this repository can build, the existing `restore_placement` thaw included. `scripts/e2e/tests-repo.pin:366-372` records the consequence measured in a browser: the cases that press a run button "cannot pass on ANY deployment this repository can build today". A recovery modelled as a seventh action on that route would ship refused.
- The read tier already exists beside it: `revalidateArtifact` and `reinstateArtifact` carry `requireCSRF` and not the gate (`apps/common/webhost/handlers_artifacts.go:282` and `:318`, with the reasoning at `:274-281`).

## 2.3 Who may read

- One authenticated identity tier. `capabilities.AuthContext` carries `Authenticated`, `Username`, `Mode` and `SessionExpiresAt` and no role (`apps/common/platform/capabilities/capabilities.go:85-90`).
- `core/service/edithold.go:34-39` already states the consequence for its own route: "Any authenticated caller can hold any backup set". Nothing in the tree can express "this operator may recover that set and not this one". Whether any provider adapter adds a role outside `AuthContext` is unknown; I found none by searching `apps/common/platform/capabilities/`.

## 2.4 Scheduled scrubbing

- `core/internal/revalidate` re-checks the four durable states, orders by oldest `UpdatedAt` first (`select.go:103-104`) and truncates to `MaxPerCycle` (`select.go:109-110`).
- `checks.go:75` fixes `automaticMediumClass = placement.Existence` as the strongest class an unattended pass may reach on a medium. The refusal reads `Class.CostsEgress()` at the moment of spend (`checks.go:167-168`, `core/internal/placement/ladder.go:121`).
- It separates checked from passed on purpose (`checks.go:22-31`), and says an existence check proves "an object of the right size is at that key" and not the bytes (`checks.go:36-38`).
- **It has no production caller.** The only Go file importing `core/internal/revalidate` is `core/tests/miniointegration/revalidate_test.go:17`, found by `grep -rln 'core/internal/revalidate"' core apps`. Every deployment can carry a `revalidation:` block per backup set (`core/internal/config/config.go:1000`, type at `:1342`), validated at `Load`, that nothing reads.
- Due-ness is a proxy. `SelectDue` compares `now.Sub(rec.UpdatedAt)` (`select.go:96`) against the artifact row, so FR-17 reconciliation or a catalog rebuild can reset the clock without a byte being read. Whether a rebuild really rewrites `updated_at` is #978's own claim and I did not trace it; the documentation of `SelectDue` (`select.go:62-70`) states the proxy and its weakness itself.

## 2.5 Per-copy assurance state

- Half of it is already a column. `placements` carries `verification_class` over a closed `CHECK (verification_class IN ('', 'content', 'attested', 'existence'))` and `verified_at` (`core/migrations/0007_placements.sql:51-63`), and the comment says the class is "the strongest class of verification this copy has actually ACHIEVED (FR-31), never the strongest one configured" (`:51-52`).
- There is no `next_due`, no failure record, no bytes-read or duration and no `requires_restore` state. So "when is this copy next due" and "how many times running has it failed" are unanswerable from the journal. Migrations end at `0013_workflow_lifecycle.sql` (`core/migrations/`).

## 2.6 Proving usability

- `lifecycle.RunRestoreCheck` runs an operator's executable against a local path under a fixed environment, its own process group, bounded captured output and a fail-closed timeout (`core/internal/lifecycle/restorecheck.go:65`). It is reached on demand by `retnd validate` (`core/internal/app/validate.go:292`, documented at `:94`) and configured per set as `revalidation.command` (`core/internal/config/config.go:1376-1382`).
- The incremental engine has its own ladder, four rungs ascending (`core/internal/model/verificationlevel.go:78-89`): `structural`, `content_sample`, `content_full`, `restore_drill` (`:51`, `:58`, `:69`, `:75`). #953 abbreviates the middle two as `sample` and `full`; the stored strings are the long ones. A per-set cadence `verification_restore_drill_every` exists (`core/internal/config/config.go:650-665`) and a run records the level it achieved (`core/internal/app/snapshotcycle.go:440` and `:799`).
- Nothing recovers a copy into an isolated location first, so the validator only ever sees the durable file in place. No result of any check is durable as a recovery-test claim distinct from a content claim. Nothing surfaces "a validator has never run here" as its own state.

## 2.7 Settings shape

- `Revalidation` is a field on `BackupSet` (`core/internal/config/config.go:1000`) with no deployment-wide default, so an operator configures every set or gets nothing.
- The incremental ladder is per-set too and defaults from a compile-time constant (`model.DefaultVerificationLevel`, `core/internal/model/verificationlevel.go:126`) rather than from configuration.
- Neither has the global-default half `docs/epic-checklist.md` section 2 asks for. The three-field pattern that does exist for retention is documented at `core/internal/config/config.go:1388-1403`, including the rule that any mutation of the global is followed by `Validate`.

## 2.8 Telling the operator

- 27 event constants, `EventValidation = "validation"` among them (`core/internal/obs/events.go:141`; the count is `grep -c '^	Event[A-Za-z]* *= "'`).
- A cursor-paged, append-only activity read (`core/service/activity.go:109-141`, with the cursor's reasoning at `:33`).
- Nine alert kinds, none about assurance (`core/internal/alert/alert.go:128-296`).
- Per-set gauges are built from `namePrefix + "backup_set_"` (`core/internal/metrics/metrics.go:63`, `:397`, `:418`). #953 also cites a legacy `backupd_` alias at `:88`; at `1cd17849` I find no `backupd_` string in `core/internal/metrics/`, so that alias is gone or I cannot find it, and I am treating #947's "deprecation window" as unknown.
- Health carries `VerificationStatus`, `VerificationLevel` and `VerificationAchieved` (`core/internal/health/health.go:500-513`) and `LastKnownGoodAt` (`:515-518`), computed in `core/internal/health/compute.go:323`. I read the first three as the incremental engine's. I found no field for an artifact-engine copy's content-verified age, and whether some other caller derives one is unknown.
- No event, alert kind or gauge names a scrub, a recovery or a recovery test.

## 2.9 What is not claimed

I did not measure runtime behaviour of any of the above (no process was run for this document). Everything in this section is a reading of the tree at `1cd17849`, and it is the code the claims are about, not the code on any of the open pull requests.

---

# 3. Adversarial Review, Five-Expert Panel

Same discipline as `docs/EPIC-E-alternative-storage.md` and `docs/EPIC-L-scripted-backup-workflows.md`: each reviewer was told to reject this EPIC if the design could create data loss, a credential leak, a misleading backup status, an untestable guard or a compatibility break. The five lenses were chosen for what the feature is: a read path that hands out backup data, a scheduler that can cost money and bandwidth, a claim of recoverability that could be false, a restore procedure performed when the normal system is already gone, and a status the UI will render as a reassuring green tick. All five returned REJECT first.

The issue records the verdicts and the nineteen findings. The grouping of findings under the five lenses below is mine, by subject. The findings are tracked one row each as `QC-01` through `QC-19` in `docs/conformance/epic-q-matrix.md`, in the order of the consensus table in section 3.6. Where the two could drift, the matrix is right.

## Expert 1, A Read Path That Hands Out Backup Data

### Initial verdict: REJECT

Critical findings: a recovery filed as a seventh `POST /operations` action ships refused (QC-01); there is no role, so a download route hands every authenticated caller every artifact (QC-02); a third restore-shaped verb invites the wrong one at 3am (QC-18); and reversing the "no download" promise is reversing a promise, not editing a sentence (QC-15).

### Consensus position: APPROVE AFTER REVISION

## Expert 2, A Scheduler That Can Cost Money And Bandwidth

### Initial verdict: REJECT

Critical findings: the operator set the cadence when everything was free (QC-08); a scheduled pass can only ever achieve `existence` against a medium (QC-03); due-ness is measured from the artifact row (QC-05); and an archived object, an offline destination and an unreachable medium are three different unreadable (QC-17).

### Consensus position: APPROVE AFTER REVISION

## Expert 3, A Claim Of Recoverability That Could Be False

### Initial verdict: REJECT

Critical findings: "recovery tested" proved by reachability is the lie this epic exists to prevent (QC-07); a scrub that enumerates a destination and calls what it finds the truth is a listing pretending to be the journal (QC-04); validator output is untrusted terminal input (QC-12); and Q must not borrow its verdict from M or N (QC-16).

### Consensus position: APPROVE AFTER REVISION

## Expert 4, A Restore Procedure Performed When The Normal System Is Already Gone

### Initial verdict: REJECT

Critical findings: a recovery a browser cannot complete is no recovery on a NAS appliance (QC-19); a crash with a staging directory present is an orphan that fills the disk quietly (QC-13); two engines, and a second drill would fork assurance truth (QC-11); and the per-set answer has to be written down per knob, including where it is "no" (QC-09).

### Consensus position: APPROVE AFTER REVISION

## Expert 5, A Status The UI Will Render As A Green Tick

### Initial verdict: REJECT

Critical findings: an assurance status must never be green because nobody looked (QC-06); a per-copy stage event per scrub over a thousand due copies is a volume decision, not a display one (QC-14); and mutating the global without re-resolving is the retention-override silent no-op (QC-10).

### Consensus position: APPROVE AFTER REVISION

## 3.6 Five-Expert Consensus

Each finding is a release requirement. Each gets a row in `docs/conformance/epic-q-matrix.md` with the mutation that must turn it red.

| ID | Finding | Required change |
|---|---|---|
| QC-01 | A recovery modelled as a `POST /operations` action ships refused: `router.go:395` puts that route behind `NotYetImplementedGate` (`gate.go:111-114`) | Recovery is its own route pair, a `POST` that mints a short-lived, single-use, audited recovery grant and a `GET` that streams it, registered with `requireCSRF` and not `requireDestructiveGate`, in the tier `handlers_artifacts.go:274-378` uses for reads. The gate stays shut and #92 stays its own work. A recovery is completable on a deployment whose gate reports false |
| QC-02 | There is no role, so a download route hands every authenticated caller every artifact (`capabilities.go:85-90`) | Say it out loud in the register `edithold.go:34-39` uses, and pay for it with audit rather than authorisation: every grant and every byte-range served writes an action-log row naming actor, artifact, copy, target and outcome, and the grant is short-lived, bound to the minting session and revoked by that session ending. An RBAC model is an explicit non-goal, not a deferral |
| QC-03 | A scheduled pass can only achieve `existence` against a medium (`revalidate/checks.go:75`), and `existence` is a HEAD | A class achieved without reading bytes is never recorded or rendered as content verification. The three claims get three durable homes and three renderings, and a planted mutation relabels an `existence` result as `content` and must turn a row red |
| QC-04 | A scrub that enumerates a destination and calls what it finds the truth is a listing pretending to be the journal | Every scrub reads its work list from `placements` rows and writes its verdict there. A listing is only evidence about a row that exists. An object at a key the journal does not know is a reconciliation finding; one the journal expects and the listing lacks is a failure with a count |
| QC-05 | `SelectDue` measures due-ness from `artifacts.updated_at` (`select.go:96`) | Due-ness moves onto the copy: migration 0016 adds per-copy due time, failure record, bytes read, duration and a `requires_restore` state beside `verification_class`/`verified_at`. A rebuilt catalog yields unknown, which is due, not fresh |
| QC-06 | An assurance status must never be green because nobody looked: five reasons produce "not a pass" and read as a pass if the status has two values | The status is an enumeration with its own rendering per value, and `unknown` is the zero value so an un-migrated or un-scrubbed copy cannot default to good. A control whose effect the code cannot state gets no tooltip entry (`docs/epic-checklist.md` section 5) |
| QC-07 | "Recovery tested" proved by reachability: a HEAD, a listing, a resolving signed URL and a completed thaw are all reachability | A recovery test recovers to an isolated path, re-reads the bytes end to end, requires the digest to equal the artifact's recorded digest, and only then runs the validator. Reachability is never a pass, and `RunRestoreCheck`'s contract (`restorecheck.go:65`) is reused |
| QC-08 | The scheduler can cost money, and the operator set its cadence when everything was free | Budgets are refusals at the moment of spend: concurrent full reads, per-destination concurrency, staging-disk reserve, scheduler work per tick, captured output per validator and a web-process memory ceiling during a download. Each is a named constant with a default, a refusal with an exit code, and a test that drives it over the line |
| QC-09 | The per-set answer has to be written down, per knob, including where it is "no" | Scrub cadence, strategy, recovery-test mode and validator reference are per set with inherit/off/custom. Staging root, staging TTL, concurrency ceilings and the web download memory budget are global only, and the per-set answer is `no` in writing |
| QC-10 | Mutating the global without re-resolving is the retention-override silent no-op (`config.go:1388-1403`) | The `PATCH` runs the same `Validate` and hot reload, every consumer reads the resolved field and never the pointer, and a global change is asserted to move an inheriting set through the route |
| QC-11 | Two engines, and a second drill would fork assurance truth (`config.go:650-665`, `snapshotcycle.go:440`) | Q does not run a second drill for `engine: kopia`. It reads the achieved level as the recovery-test claim for those sets. One vocabulary, two producers |
| QC-12 | Validator output is untrusted terminal input; EPIC L's `CR-08` is the row | Reuse EPIC L's record shape and bound. No raw control byte reaches a structural field, a notification body or the docked terminal, and a truncation marker is distinguishable from a forged one by kind. No new capture path is written |
| QC-13 | A crash with a staging directory present is an orphan that fills the disk quietly | Staging names are atomic and unique, TTL-swept and reconciled at startup the way `workflowrun` reconciles an interrupted run (`core/internal/workflowrun/recovery.go:89`). A cleanup failure is separately visible from a recovery failure |
| QC-14 | A per-copy stage event per scrub over a thousand due copies is a volume decision (`activity.go:109-141`) | One durable event per scrub pass with counts, plus one per failure; per-copy progress is live-only. The feed-volume number is stated and asserted against a 1000-copy fixture |
| QC-15 | Reversing `docs/recovery.md:18-21` is reversing a promise; two site paragraphs state it independently (`reference.html:263`, `:274`) | One boundary sentence lands in every place the old claim is made, found by asking what the deleted claim was split into rather than by sweeping for its words (checklist section 9, the #788 rule) |
| QC-16 | Q must not borrow its verdict from M or N | Q works with M and N off, and asserts it. With N on, a recovery decrypts and requires the plaintext digest to equal the artifact digest; ciphertext whose digest matches nothing is not a pass. With M on, a lock is metadata and a locked copy failing a content scrub is a failure |
| QC-17 | An unreadable copy must not look assured: archived, offline and unreachable are three different unreadable | `requires_restore` is its own state with its own rendering, and an unreadable copy never counts towards a satisfied policy. An expected-offline destination is P's contract and this epic does not fork it |
| QC-18 | A third restore-shaped verb invites the wrong one at 3am; the reference page already draws the line (`reference.html:234`) | The vocabulary is `recover` and `scrub`, never "restore". Neither existing verb is renamed or absorbed, and the reference page states all three side by side |
| QC-19 | A recovery a browser cannot complete is no recovery on a NAS appliance | The browser path is first-class: mint, stream, resume-safe progress, cancel and a visible failure. `docs/recovery-without-a-terminal.md` gains recovery as its own numbered section, and the docs say what to do when the catalog itself is gone |

---

# 4. Functional Requirements

Eight numbers, eight sentences, each cited by at least one sub-issue. The block is FR-67 through FR-74; the next free number after it is FR-75.

## FR-67, Artifact recovery

Artifact recovery is a durable, authenticated and audited operation that streams one chosen copy to one declared target with bounded memory, re-verifies the bytes on the way out, and never executes an application command. Cited by #976, #987.

## FR-68, Copy selection and cost

Copy selection resolves to the readable copy with the strongest recorded verification class, and refuses, rather than silently pays, any retrieval `placement.Class.CostsEgress()` marks chargeable unless the request carries an explicit cost acknowledgement. Cited by #976, #983.

## FR-69, The scrub has a driver

Scheduled scrubbing runs `core/internal/revalidate` from a real production driver on a bounded, jittered cadence, and a copy's due-ness is read from that copy's own record rather than inferred from the artifact row's `updated_at`. Cited by #978.

## FR-70, The per-copy assurance record

A copy's assurance record is durable and per copy (class achieved, when, next due, last failure, consecutive failures, bytes read, duration and a `requires_restore` state), and a class achieved without reading bytes is never recorded as content verification. Cited by #978, #980.

## FR-71, Recovery testing

Recovery testing recovers the newest known good copy to an isolated path, requires its digest to match the artifact's recorded digest, then runs the operator's validator through the existing untrusted-subprocess boundary, recording the exact artifact, copy and validator identity used. Cited by #980.

## FR-72, Three claims, never one tick

`Stored`, `Content verified` and `Recovery tested` stay three separately-durable claims, and every status distinguishes not looked at from looked at and passed: unknown, never configured, never run, overdue, refused for cost and could-not-complete each render as themselves and never as a pass. Cited by #980, #984, #988.

## FR-73, Settings, parity and observability

Every assurance and recovery knob has a deployment-wide default good enough to leave alone plus a per-backup-set `inherit`/override resolved by `Validate`, reachable identically from the CLI, `api/v1` and the browser, with every long-running stage on the activity feed and in the docked terminal. Cited by #982, #985.

## FR-74, Budgets are refusals

Recovery and scrubbing run inside written budgets (concurrent full reads, per-destination concurrency, staging reserve, scheduler work per tick, validator output and web-process memory during a download), and every budget is a refusal at the moment of spend rather than a documented intention. Cited by #983, #986.

## Core invariants

- **Three claims, three homes, never one tick.** Separately durable, separately aged, separately rendered. A checksum pass with a validator failure reads exactly as `Content verified / Recovery test failed`.
- **Nothing is green because nobody looked.** `unknown` is the zero value of every assurance status, and `unknown` is due.
- **A class is what was achieved, not what was asked for.** The `placements.verification_class` rule (`0007_placements.sql:51-52`) extends to every new surface; the automatic ceiling stays where `revalidate/checks.go:75` puts it.
- **Journal truth stays journal truth.** Work lists come from `placements` rows; a destination listing is evidence about a row, never a source of one.
- **Money is a refusal, not a warning.** No scheduled pass ever thaws.
- **The destructive gate stays shut.** Recovery is registered outside `requireDestructiveGate` and is completable with the gate reporting false. Nothing here implements, weakens or works around #92.
- **Authorisation is one tier, and the answer is audit.** No role model is introduced.
- **No secret, key, sensitive source path or provider error body** reaches CLI JSON, an API response, the activity feed, the docked terminal, a metric label, a screenshot, a support bundle or a log. `obs.Secret` (`core/internal/obs/secret.go:111`) is the existing mechanism.
- **The container contract does not move.** Distroless, non-root, read-only, `cap_drop: ALL`, `no-new-privileges`; no Docker socket, no host-root mount, no new privileged container, no shell added.
- **M and N are not evidence.** Q works with both off.
- **No rename.** No FR, metric family, event constant, route or existing verb is renamed or renumbered.

## The assurance vocabulary

Fixed here, before #976 or #980 starts, because both write into the same record.

- Content claim, per copy: `unknown` (zero value), `passed`, `failed`, `requires_restore`, `could_not_complete`, and the class that was actually checked (`''`, `existence`, `attested`, `content`). Only a check that read the bytes moves the content-verified age.
- Recovery-test claim, per copy: `unknown` (zero value), `not_configured`, `never_run`, `passed`, `overdue`, `failed`, `refused_for_cost`, `could_not_complete`, with the digest gate recorded as its own fact (never compared, matched, mismatched).
- The five not-a-pass reasons of FR-72 map to `not_configured`, `never_run`, `overdue`, `refused_for_cost` and `could_not_complete`.
- A cleanup failure is its own recorded state (`removed` or `failed`), and changes neither verdict.

---

# 5. Phases

Two phases, five issues each, numbered `Q<phase>.<n>`. The tracker is #953, the sub-issues #976 through #988, and #954 (this document) is the scaffold that opens them.

Dependency order: #954, then #978, then {#976, #980}, then #982, #983, #984, #985, then {#986, #987}, then #988. #978 goes first because the per-copy record is what the recovery resolver and the recovery test both write into, and settling its shape in this file and its columns in migration 0016 stops the two negotiating it.

## Pull requests already open

| Sub-issue | Pull request |
|---|---|
| #976 Q1.1 | #1053 |
| #978 Q1.2 | #1056 |
| #980 Q1.3 | #1059 |
| #982 Q1.4 | #1052 |
| #983 Q1.5 | #1060 |
| #984 Q2.1 | #1051 |
| #985 Q2.2 | #1057 |
| #987 Q2.4 | #1058 |
| #986 Q2.3, #988 Q2.5 | none yet |

Two more pull requests are open beside this epic and are not part of its numbering: #1055 (the docs-site contrast rule, #1054) and #1050 (a fresh install configures itself, #1049). Section 9 says where each touches this epic.

## Phase 1, recovery engine, scheduled assurance, validation and parity (`Q:phase-1`)

Each line names every `docs/epic-checklist.md` section the issue must satisfy and says what satisfying it means here.

- **Q1.1 #976, the recovery/export domain, its authorisation and the streaming engine** (FR-67, FR-68). A recovery domain plus a route pair, `POST` to mint a grant and `GET` to stream, with `requireCSRF` and outside `requireDestructiveGate`. Resolver prefers the strongest-verified readable copy and refuses chargeable egress without an acknowledgement; bounded buffers, atomic staging names, TTL sweep, startup reconcile; `retnd recover`. Satisfies section 3 (a `cliecho` entry for both routes, `recover` in the dispatch table and in `reference.html`, matching refusals and exit codes), section 6 (`openapi.json` first, both bindings regenerated, the streaming response in `docs/api/contract.md`), section 7 (work list and verdict from `placements` rows), section 11 (nothing sensitive on the new surface, and `docs/compliance/` because this is new egress of retained backup data) and section 12 (a mutation self-test per new guard).
- **Q1.2 #978, the scrub driver, the per-copy assurance record and migration 0016** (FR-69, FR-70). Wire `core/internal/revalidate` to a real loop for the first time, bounded globally and per destination, with jitter, capped backoff, respect for the single-flight lock and edit holds. Migration `0016` adds the per-copy record beside `verification_class`/`verified_at`. `retnd scrub`. Satisfies section 4 (an FR-23 catalog addition with a `Result`; one durable event per pass with counts, per-copy progress live-only, and the feed-volume number stated), section 7 (migration 0016; compat captures updated) and section 12 Fixtures (a fresh `verified_at` over changed bytes; a matching size over changed content; a rebuilt catalog with no verification age).
- **Q1.3 #980, the recovery-test runner and the three assurance claims** (FR-71, FR-72). Recover the newest known good copy to an isolated path, re-read it, require digest equality, then run the validator through `lifecycle.RunRestoreCheck`, capture bounded redacted output, record artifact, copy and validator identity, and remove the copy. For `engine: kopia` sets the claim is read off the existing achieved `restore_drill` level. Satisfies section 4, section 7 (the verdict lands in 0016's record), section 11 (bounded, redacted output) and section 12 and Fixtures (a validator that exits 0 having read nothing; one that hangs; one emitting 10 MiB with hostile sequences; one that double-forks).
- **Q1.4 #982, global and per-set assurance configuration, CLI/API parity and live observability** (FR-73). The global `assurance:` block, the per-set override in `Retention`'s three-field pattern, the re-resolve after mutating the global, and the written `no` for the host-scoped knobs. Satisfies section 2, section 3 (both directions, including the ungated one checked by hand and recorded), section 4 (all six bullets), section 6 and section 9 (the `[Unreleased]` CHANGELOG entry).
- **Q1.5 #983, fault injection, budgets and the cost/performance gate** (FR-74). Six budgets, each a named constant with a default, a refusal with a distinct exit code and a test that drives it over the line, and the adversarial scenarios from a corrupted remote copy to 1000+ simultaneously-due copies. Satisfies section 8 (staging reserve and download memory through the capability contract rather than one provider; compose files, templates, catalog and packaging manifests asserted unchanged), section 12 and section 12 Fixtures (values chosen against the wrong implementation).

### Phase 1 entry gate

- [x] The adversarial review is done and its nineteen findings are release requirements (section 3).
- [x] The EPIC issue and its sub-issues exist (#953, #976 through #988).
- [x] The letter `Q` is held only by #953 and #954. Checked on 2026-10-11 against every issue whose title begins `EPIC`; A to R are otherwise accounted for (M, N and P are open with scaffolds of their own).
- [x] FR-67 through FR-74 collide with nothing in the specs on `main`: `grep -rn 'FR-6[7-9]\|FR-7[0-4]' docs` finds no claim before this file, and FR-25 is still unclaimed.
- [x] `core/migrations/` still ends at `0013_workflow_lifecycle.sql` and `docs/adr/` at `0023-moving-the-repository-coordinates-once-and-last.md`, with `0024` reserved to #904.
- [x] The assurance vocabulary is fixed in section 4.
- [ ] This specification and `docs/conformance/epic-q-matrix.md` are merged with every row `BLOCKED`, each naming its sub-issue, its executable proof and its falsification. That is this pull request.

### Phase 1 exit gate

- [ ] A recovery completes end to end on a deployment whose `DestructiveGate.Passed()` reports false, and the same deployment still refuses `POST /api/v1/operations` with `403 DESTRUCTIVE_OPERATIONS_DISABLED`.
- [ ] Recovering a corrupted copy fails before any byte is presented as successful, on a fixture whose recorded size and `verification_class` are unchanged and whose bytes are not.
- [ ] A 4 GiB artifact recovers within a stated RSS ceiling in both the CLI and the web process, measured, with the number and the host in `docs/perf/`.
- [ ] A recovery grant is single-use, expires, is bound to its minting session and is refused after that session ends; each mint and each served range writes exactly one action-log row naming actor, artifact, copy and outcome.
- [ ] No `existence`-class result is ever recorded or rendered as content verification: relabelling one in a fixture turns a named row red.
- [ ] The scrub driver runs `core/internal/revalidate` on a jittered cadence with bounded global and per-destination concurrency, and `core/internal/revalidate` has a production importer for the first time.
- [ ] No copy is scrubbed twice under a concurrent scheduler tick and manual request, and a restart resumes due-state from migration 0016's columns rather than from `artifacts.updated_at`.
- [ ] A rebuilt catalog leaves every copy `unknown` and therefore due, never fresh.
- [ ] A scheduled pass never thaws: an archive-class copy is recorded `requires_restore` and the egress refusal fires at the moment of spend, with an explicit acknowledgement being the only way past it.
- [ ] A recovery test recovers to an isolated path, matches the digest against the artifact's recorded digest, then runs the validator, and a validator that exits 0 without reading the file does not produce a recovery-test pass.
- [ ] Checksum pass plus validator failure is recorded as `Content verified / Recovery test failed`, and a cleanup failure is recorded separately from both.
- [ ] The five not-a-pass reasons are five distinct recorded values, `unknown` is the zero value and due, and no read path returns a pass from `unknown`.
- [ ] With M and N absent every claim in this epic is still computable; with N present a plaintext digest mismatch fails the recovery and ciphertext existing proves nothing; with M present a locked copy failing a content scrub is a failure.
- [ ] `engine: kopia` sets get their recovery-test claim from the existing `restore_drill` level, and no second drill is run.
- [ ] Every budget is a refusal with an exit code, driven over its line in a test: concurrent full reads, per-destination concurrency, staging reserve, scheduler work per tick, validator output, web download memory.
- [ ] A crash with staging present leaves no orphan after the startup sweep, and disk-full while staging refuses rather than corrupting.
- [ ] The canonical deployment diff is empty: `container/compose.yaml`, the seven `apps/*/compose/retnd.y*ml` files, `apps/unraid/template/retnd.xml` and `retnd-ui.xml`, `apps/truenas/catalog/` and the four `distribution/packaging/*.json` manifests are byte-identical, and the container contract is still distroless, non-root, read-only, `cap_drop: ALL`, `no-new-privileges`.
- [ ] One durable activity event per scrub pass with counts plus one per failure, asserted against a 1000-copy fixture with the feed-volume number stated.
- [ ] No validator byte reaches a structural field, a notification body or the log API as a raw control byte, on EPIC L's `CR-08` corpus.
- [ ] Parity in both directions at the API and CLI: a `cliecho` entry per route, no gap sentence naming a shipped verb, `recover` and `scrub` in the dispatch table and in `reference.html`, matching refusals and exit codes (`TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap`, `TestNoGapClaimsAVerbThisBinaryShips`, `distribution/packaging/site_reference_test.go`, `core/cmd/retnd/routeparity_test.go`), and the ungated reverse direction checked by hand and recorded.
- [ ] The global default alone produces useful scrubbing on a config that mentions nothing new, a per-set `inherit`/`off`/`custom` resolves through `Validate`, a global `PATCH` moves an inheriting set through the route, and the host-scoped knobs carry their written `no`.
- [ ] Compat fixtures: one full, one omitting the block entirely and three invalid; a config that never mentions the new keys is byte-identical after a round trip and behaves as it did.
- [ ] `openapi.json` is authoritative with both bindings regenerated and drift green, and `docs/api/contract.md` describes the streaming response.

## Phase 2, design, web UI, e2e, docs and media (`Q:phase-2`)

- **Q2.1 #984, recovery and assurance mock-ups** (FR-72). Mandatory before any web implementation. The three claims as three things; the five not-a-pass states as five renderings; `requires_restore` and "no readable copy" as their own; the recover action and target chooser with the archival cost as a blocking decision; global and per-set assurance controls; the `.html` and `.png` note in `docs/design/` dated by its issue. Vocabulary is `Recover artifact`, `Export`, `Scrub`, `Recovery test`, never "Restore". Satisfies section 5 (design first, the global page and the per-set card, light, dark and narrow) and section 5 Tooltips (each control names a `TooltipId` or is marked deliberately unexplained, with the reason).
- **Q2.2 #985, the web UI, the docked terminal and reconnect continuity** (FR-73). The approved design implemented: settings card, per-set section, the three claims with their ages, recover with target chooser and download progress, scrub-now, operation detail with follow and cancel, a terminal line per stage, reconnect replaying from the durable cursor. The three ways this screen can lie (a stale "verified" chip, a failed download shown as success, a green tick reached from `unknown`) each get a test. Satisfies section 4, section 5, section 5 Tooltips and section 6.
- **Q2.3 #986, CLI/web equivalence and the Suite A/B/C matrix** (FR-74). Suites A, B and C in `retnd/retnd-tests` with `scripts/e2e/tests-repo.pin` moving in lockstep, every case also with M and N absent, and a recovery observed completing on a gate-shut deployment. Satisfies section 3 (Suite C is the equivalence evidence), section 12 and section 12 Fixtures.
- **Q2.4 #987, reversing the "no download" promise across every document that makes it** (FR-67). One boundary sentence replaces the three claims, found by asking what the deleted claim was split into. Plus the three assurance claims, how `auto` resolves, archival cost behaviour, validator trust, the three restore-shaped verbs side by side, recovery with the catalog gone, and ADRs 0029 and 0030. Satisfies section 9 and section 11.
- **Q2.5 #988, deterministic captures and the release gate** (FR-72). `docs/site/tools/capture-recovery-assurance.mjs` on the existing harness, against the mock API with the clock pinned and Playwright borrowed from the pinned `retnd-tests` checkout; one untouched script run first. Then the release gate, and a matrix with no `BLOCKED` row left. Satisfies section 10, section 12 and section 1.

### Phase 2 entry gate

- [ ] Phase 1 is exit-gated: every Phase 1 row of `docs/conformance/epic-q-matrix.md` is `PASS`, or `PARTIAL` with the reason written in the row.
- [ ] The design (#984) is approved and in `docs/design/`, because no web implementation starts before it.
- [ ] `scripts/e2e/tests-repo.pin` is at a `retnd-tests` commit that contains the Phase 1 CLI cases, so the pin and the product can move together.

### Phase 2 exit gate

- [ ] The five not-a-pass reasons render as five distinct states in the browser, and a test asserts there is no path from `unknown` to the pass rendering.
- [ ] The design note `docs/design/<issue>-recovery-assurance.html` and a matching `.png` exist with the reasoning and the date, all three claims appear as three separately-aged facts, and the archival cost warning is a blocking decision in the flow.
- [ ] Every new control has a registry tooltip and a three-part field-help entry with the effect read off the code, the tag allowlist and orphan checks are green, the opt-out preference is tested explicitly ON, and at least one control is refused an entry with the reason recorded.
- [ ] No validator byte can drive the docked terminal: OSC and window-control sequences are displayed inert and the truncation marker renders as a marker, on EPIC L's `CR-08` corpus.
- [ ] No web-only feature: every mutating control's equivalent command is printed by `CommandEcho.tsx`, a scrub that fails replaces a "Content verified" chip, and a cancelled or failed download cannot reach the success rendering.
- [ ] A reload mid-recovery restores the current stage from the durable feed and starts no second recovery, and a dropped live connection replays from its cursor with no duplicate terminal lines.
- [ ] Suites A, B and C land in `retnd/retnd-tests` with `scripts/e2e/tests-repo.pin` moving in lockstep, and each case fails against `main` before the product change and passes after.
- [ ] Parity in both directions holds at the browser too: Suite C proves at least one refusal and one success per surface with the same exit code on both routes.
- [ ] `docs/recovery.md`, `docs/recovery-without-a-terminal.md`, `README.md`, the site pages, `index.html#honest`, ADRs 0029 and 0030 and the CHANGELOG one-way-door entry have all landed, and the old "no download" claim is gone from every place that made it, `reference.html:263` and `:274` included.
- [ ] The split-not-renamed sweep over the deleted claim is recorded item by item, and `docs/recovery-without-a-terminal.md` gains recovery as its own numbered failure-and-fix section.
- [ ] `docs/compliance/` says the download route is new egress of customer data.
- [ ] `docs/site/screens/` is regenerated through `capture-recovery-assurance.mjs` against the mock API with a pinned clock, one untouched script run first, and no hand-taken image.
- [ ] Two consecutive runs of the capture script are byte-identical.
- [ ] Every new text and background pair meets WCAG AA in the light and the dark theme, with the measured ratios for the key pairs recorded in the pull request that adds the surface.
- [ ] `scripts/ci-local.sh` and the release-gate `needs` list are green, with a mutation self-test per new guard.
- [ ] `docs/conformance/epic-q-matrix.md` has no `BLOCKED` row, every `PASS` row's falsification has been run and watched to fail, and every `BY HAND` row says who checked what and when rather than borrowing a neighbouring green.

---

# 6. TDD Contract

Every guard below has a planted violation that has to be run and watched to fail before its row goes `PASS`. The full table, with test names and the mutation, is `docs/conformance/epic-q-matrix.md`; the guards here are numbered in the order of its `QV.n` rows.

| Guard | Planted violation that proves it fires |
|---|---|
| Recovery is outside the destructive gate and inside CSRF | register the mint route behind `requireDestructiveGate`; register it without `requireCSRF` |
| Chargeable reads need an explicit acknowledgement | choose a chargeable copy with no acknowledgement; thaw an archive copy with no acknowledgement |
| The digest gate on recovery output | deliver a copy that differs at the same size; deliver one of the wrong length; leave a partial file behind after a failure |
| Grant lifecycle | reuse a spent grant; redeem from another session; a grant that never expires; a grant that outlives its session |
| Audit rows | a mint that writes no action-log row; a served range that writes none |
| Staging containment | a target that climbs out of the staging root; one that passes through a symlink; staging that ignores its reserve; publishing a file the staging area never created |
| Bounded memory during a download | buffer the whole copy and run the 4 GiB harness against its ceilings |
| Incremental sets are pointed at their own verb | recover a `kopia` set as if it held files |
| `core/internal/revalidate` keeps a production importer | leave only `_test.go` importers |
| An existence result is never content | relabel a HEAD result as `content` in the write path |
| Due-ness is read from the copy | read it from `artifacts.updated_at` again |
| Unknown is the zero value and is due | default the new state column to `passed`, or treat a NULL due time as not due |
| An unattended pass never pays | let a scheduled pass reach a content read on a medium |
| The recovery-test digest gate | remove the digest comparison and run a validator that exits 0 without reading |
| The truncation marker is a kind, not text | classify the marker by its text |
| Budgets are refusals | a read budget that never refuses; a per-destination budget not applied; an unbounded scheduler pass; a validator bound that is no longer EPIC L's; an unenforced download ceiling |
| Config resolves through `Validate` | a consumer reading the override pointer; a global mutation with no re-resolve |
| The canonical deployment does not move | make the shipped container privileged |
| No secret reaches a surface | write a bearer grant or a resolved credential into a log, a refusal body or a metric label |
| The old "no download" claim stays gone | restore the sentence to `reference.html` |
| Tooltips and field help | an entry for a control whose effect the code cannot state; a preference tested only OFF |
| The matrix itself cannot lie | a row with no falsification, a row with no proof, a row green under another word, a row naming no sub-issue |

---

# 7. Checklist coverage map

One row per `docs/epic-checklist.md` section, Tooltips and Fixtures included. A section owned by nobody is an argued "not touched", never an omission.

| Section | Owner | What satisfying it means here |
|---|---|---|
| 1 The shape of the epic | #954, #988 | Two phases, five each, the cut list below, letter `Q`, FR-67 to FR-74 continuing the series, the five-lens panel, entry and exit gates as checkable claims, and `docs/conformance/epic-q-matrix.md` with every row falsified and watched to fail |
| 2 Settings: global default plus per-set override | #982 | The global default nobody has to set, the `XConfig *T` / resolved `X T` three-field pattern for scrub cadence, strategy, recovery-test mode and validator, the re-resolve after mutating the global, valid and invalid compat fixtures, and the written `no` for the host-scoped knobs |
| 3 No CLI/web functionality gap | #976, #982, #986 | A `cliecho` entry per new route, no gap sentence naming a shipped verb, `recover` and `scrub` in the dispatch table and in `reference.html`, matching refusals and exit codes on both routes, and the ungated reverse direction checked by hand and recorded |
| 4 Progress reaches terminal and feed | #978, #982, #985 | FR-23 catalog additions in `core/internal/obs/events.go`, a `Result` on every completion, `core/service/activity.go` and `liveactivity.go`, the four surfaces in `ui/shared/src/pages/` and `CommandEcho.tsx`, and the stated answer on feed volume |
| 5 Web UI design | #984, #985 | The design step first, the `.html` and `.png` note dated by issue in `docs/design/`, the control on `SettingsPage.tsx` and the per-set card beside `BackupSetRetentionCard.tsx`, light, dark and narrow, frontend gate green |
| 5 Tooltips | #984, #985 | A `tooltips.json` entry per new control naming its `TooltipId`, a three-part `fieldHelpCopy.ts` entry whose third part is the real effect read off the code, the tag allowlist, the opt-out preference tested explicitly ON, and a recorded refusal to write an entry for any control whose effect the code cannot state |
| 6 The API contract | #976, #982 | `api/v1/openapi.json` authoritative, both bindings regenerated, `scripts/api/check-contract-drift.sh` and the client-path check green, `contract.conformance.test.ts` green, and `docs/api/contract.md` carrying the new surface including the streaming response |
| 7 Storage, state, migrations | #978, #980 | Migration `0016` for the per-copy record; journal truth from `placements` rows and never a listing; the compat captures in `core/tests/compat/capture_*.go` updated. Section 10 holds the open question on altering an existing table |
| 8 Providers | #983, #986 | The capability contract (`apps/common/platform/capabilities/`) rather than one provider, since staging reserve and download memory are host facts; `apps/common/tests` conformance green; `docs/acceptance/` only where a gateway's buffering changes what an operator sees; compose files, templates, catalog and packaging manifests asserted unchanged |
| 9 Documentation | #987 | README, the site pages the change touches, `index.html#honest` saying what was and was not proven, `recovery.md`, `recovery-without-a-terminal.md`, `storage-mediums.md`, `runtime-contract.md`, `incremental-runbooks.md`, ADRs 0029 and 0030, the `[Unreleased]` CHANGELOG entry with the one-way door, package-doc baselines, the comments this epic falsifies, and the split-not-renamed sweep |
| 10 Screenshots and GIFs | #988 | `capture-recovery-assurance.mjs` under `docs/site/tools/` on the mock API with a pinned clock, Playwright borrowed from the pinned `retnd-tests` checkout, `docs/site/screens/` regenerated, one untouched script run first, and any store-listing screen this changes going back to outstanding in `docs/submission/screenshots.md` |
| 11 Compliance and supply chain | #976, #987 | Nothing sensitive on any new surface, NOTICE and the licence inventory only if the module graph moves, and `docs/compliance/` updated because a download route is new egress of customer data out of the deployment |
| 12 Gates | #983, #986, #988 | `scripts/ci-local.sh` green, a mutation self-test per new guard, any new CI job in the release gate's `needs` list (`scripts/tests/release-gate-covers-every-job.test.sh`), no new `RM_`/`BM_`/`bm_`/`rbm_` identifier, tests red before green |
| 12 Fixtures | #978, #983, #986 | Fixtures chosen against the wrong implementation: a copy whose `verified_at` is fresh and whose bytes are not, a copy whose class is `existence` while its size matches, a validator that exits 0 having read nothing, a rebuilt catalog with no verification age. Existing fixtures extended rather than multiplied, every generated artefact reproducible from a generator in the same commit |

Nothing is unowned. The two sections this epic could plausibly have skipped are 8 and 11, and both are claimed: 8 because a staging reserve and a download memory ceiling are host facts that belong in the capability contract, and 11 because a route that streams retained backup data off a deployment is new egress and `docs/compliance/` has to say so.

The contrast rule (WCAG AA in both themes, with the ratios measured and stated) applies wherever Q2.1, Q2.2 or Q2.5 add styled output: every text and background pair meets WCAG AA in both themes and the pull request states the measured ratios. This pull request adds none, since it is prose in Markdown. #1054 (open as #1055) adds the CI check for the docs site; whether Q's new pages fall under it depends on that landing first, and I have not verified it.

---

# 8. What I cut to fit two phases, and why

- **Recovery to an operator-specified SFTP destination.** The draft offers it "if the existing transport abstraction can do so without weakening host-key policy", and that conditional is the problem: `docs/ssh-setup.md` teaches operators to cripple the transfer account on purpose, so a write-out over it is either refused or a new capability probe and a new host-key custody decision, which is EPIC L's `CR-02` shape again. Recovery to the staging area and to a configured storage destination covers "put it somewhere else". **Follow-up issue**, not a third phase.
- **"Return to the original source host".** Cut outright, nothing follows it: it is the one target that reads as application restore, and offering it would undo the boundary sentence this epic exists to write.
- **Backend-signed direct download URLs.** They move authorisation, verification and audit off the engine and onto a provider, in the one epic whose invariant is that the engine proves the bytes. Nothing follows it: the proxy stream is the only path, and the memory budget is what makes that honest.
- **Scheduled archival thaw.** An operator may thaw explicitly today (FR-34) and may acknowledge a cost per recovery under this epic. A cadence that thaws unattended is a recurring bill set once, and `revalidate/checks.go:167-168` already refuses that shape. Nothing follows it.
- **Resumable recovery across a process restart.** A recovery that dies fails explicitly and orphans nothing; range-resume needs a durable transfer cursor and a re-authorisation story. **Follow-up issue.**
- **Recovery-testing every retained copy.** Newest known good only: testing every copy multiplies validator runs by the retention chain for a claim that changes with the newest copy. Nothing follows it.
- **Opt-in `attested` for unattended scrubs.** `core/internal/placement/gate.go:53-66` is explicit that the rung between `existence` and `content` costs no egress and is nonetheless opt-in per medium, because it trusts the endpoint's own checksum. Raising the automatic ceiling is a per-medium trust decision and belongs with whoever adds the field. **Follow-up issue**, owned by that person.
- **An alert-history or assurance-report UI.** Health verdicts and the activity feed carry the facts; a reporting surface is a different epic. Nothing follows it here.

No item above becomes a third phase.

---

# 9. Where the issue text and the implementation differ

Ten pull requests were open when I wrote this, and I read their diffs and tests. Where the issue says one thing and the code does another, this section records what was built. None of it is a judgement that the code is wrong; the issues are the contract and a reviewer decides which side moves. Items that are open questions rather than settled choices are in section 10.

## Decisions the code took

1. **The recovery-test validator is a workflow-style script, not a registry entry.** #1059 runs the operator's validator as a single `NAME.local.sh` script in `recovery-test/<source>/<set>` under the workflow root, through EPIC L's stage runner. The engine re-reads the copy, checks the digest, runs the script and records the claim. The script is told about the copy through `RECOVERY_TEST_FILE`, `RECOVERY_TEST_ARTIFACT`, `RECOVERY_TEST_MEDIUM`, `RECOVERY_TEST_DIGEST` and `RECOVERY_TEST_SIZE`, a `.remote.sh` is refused, and the staging directory has to be declared to the hook runner with `--hook-mount` or the validator fails and says so. #980 described the validator as run through `lifecycle.RunRestoreCheck`; the built path reuses EPIC L's machinery around it instead.
2. **Scrub and recovery-test failure notifications go through script hooks, not built-in alert code.** #1056 notifies by running the operator's scripts in `assurance-after/sets/<source>/<set>` and then `assurance-after/global`, on an episode opening and closing (`ASSURANCE_EVENT` is `scrub_failing`, `scrub_recovered`, `recovery_test_failed` or `recovery_test_recovered`). #978 asked for "one new `alert.Kind`" and #982 for new kinds. #1052 does add six kinds (`alert.AssuranceConditions`), and I found no caller for them in the diffs. Which of the two paths is the product's is for section 10.
3. **Recovery is not a seventh `/operations` action.** Built as `POST /api/v1/backups/{source}/{set}/{name}/recovery` to mint and `GET /api/v1/recoveries/{grant}` to stream, as the issues said. The stream honours a single byte range (`206`) and refuses several in one request; a standard-output target exists in the CLI beside the three the issue lists.
4. **A grant is single-use at first redemption, and continuation requests are then allowed until it is closed** (read off the grant test names: `TestGrantRedeemsOnceThenIsUsed`, `TestGrantContinuationIsAllowedUntilClosedAndIsNotFirst`). That is what makes a ranged download possible; the exit-gate wording "single-use" is satisfied in that sense.
5. **Audit instead of RBAC**, as the issues said: one action-log row per mint and per served range, a failed range writes a row saying so.
6. **Budgets are refusals**, with defaults and exit codes in `core/internal/recovery/budget` (#1060): concurrent full reads 4 (exit 32), per destination 2 (33), staging reserve 1 GiB (18), scheduler work per tick 50 copies (34), validator output 256 KiB (35), concurrent downloads 4 (36) and zero egress for a scheduled pass (37).
7. **Migration `0016_copy_assurance` is bigger than the issue listed.** Beyond the columns #978 names it adds a lease pair (`scrub_lease_until`, `scrub_lease_holder`) so two processes cannot scrub one copy, `last_checked_at`, `last_checked_class` and `content_verified_at` (because `verified_at` is written by an existence pass too), a full `recovery_*` column set for #980 (state, tested-at, next-due, failure reason, validator id and hash, digest-matched, termination, output-truncated, cleanup state, bytes, duration) and a new `scrub_passes` table of counts. The issue's `next_due_at`, `last_failure_reason`, `last_bytes_read`, `last_duration_ms` and `assurance_state` names were kept.
8. **The built recovery test covers local copies only.** #1059's `LocalRecoverer` copies a local file into a private directory and refuses a storage-medium copy with `ErrUnsupportedCopy`, leaving the medium case for the recovery domain's resolver. #980 said the recovered copy comes through Q1.1's staging contract. Whether a medium copy is ever recovery-tested end to end is therefore still to be wired.
9. **There is no recovery-test verb and no route to start one on demand.** #1057's tests state it ("there is no recovery-test verb to echo", and a server with no route to start a run offers the command and does not fake a run). A recovery test runs from `RecoveryTestTick`. #982 listed "run a recovery test for a set or artifact" among the parity operations and #985 a `Run recovery test` control. I found no caller of `RecoveryTestTick` in #1059's diff other than its tests, which is the same shape of defect this epic exists to fix for `core/internal/revalidate`; I have not confirmed it on a composed tree.
10. **There is no operations-cancel command**, which #1057 also states in a comment, against #985's "operation detail with follow and cancel".
11. **The scrub driver reads the per-set `revalidation:` block** (`bs.Revalidation`, as #1056's tests construct it), and the recovery test's cadence is a package constant of seven days (`DefaultRecoveryTestCadence` in #1059). Neither reads the resolved `assurance:` policy that #1052 builds, so the global default as built does not yet drive either of them. This is open question 3.
12. **Defaults disagree between pull requests.** #1052's config defaults are scrub cadence 7 days, recovery-test cadence 30 days, two concurrent full reads, one per destination and a 64 MiB web download budget; #1060's budget package defaults are four reads, two per destination, a 1 GiB staging reserve and four downloads; #1059's recovery-test cadence is seven days. The default staging root is `<state dir>/recovery-staging` in #1052 and `<set root>/.retnd-recovery-test` in #1059 unless a root is passed. Which numbers win is for whoever composes the branches.
13. **The scheduled-pass event count is bounded differently from the issue's wording.** #1056 makes a 1000-copy, 50-set pass one durable pass row and no per-copy persisted row (`TestScrub_AThousandDueCopiesAcrossFiftySetsIsOnePassRowAndNoFeedRows`); #1052 states two feed rows per healthy pass and a capped number for a failing one. The exact number the exit gate requires to be "stated" is for #978 and #982 to agree.

## Places where the issue text was wrong or stale about the tree

- #953's quotation "writes nothing anywhere" is on the reference page (`docs/site/reference.html:234`), not in `core/service/restore.go`.
- The incremental ladder's rungs are `content_sample` and `content_full`, not `sample` and `full` (`core/internal/model/verificationlevel.go:58`, `:69`).
- `reference.html` line references `:235`, `:264` and `:275` are now `:234`, `:263` and `:274`.
- #953's legacy `backupd_` metric alias at `metrics.go:88` is not in the package any more (section 2.8).
- #953 and #954 count EPIC O's FR block as FR-45 to FR-50, which overlaps EPIC L's FR-45 to FR-48 (`docs/EPIC-L-scripted-backup-workflows.md:255-292`). That overlap is between O and L, not between either and Q, and it is not mine to resolve, but it means the checklist's "next free number" line cannot be derived by adding up the specs on `main`. I bumped it to FR-75 as the issue asks.
- The two sub-issue titles and bodies that name a design tool describe a step, not a requirement of the repository; this document refers to that step as the design step of checklist section 5.

## Work beside this epic

- **#1049 / #1050** (a fresh install writes a default `config.yaml` and no longer treats "not configured yet" as an error). It matters to Q in one way: the default configuration it writes has `sources: []`, and an assurance default has to be useful on exactly the kind of configuration that mentions nothing new. #1050 changes `core/internal/config/validate.go` to accept zero sources; I did not check whether Q's resolver is happy with that.
- **#1054 / #1055** (a contrast CI check for the docs site). See section 7.

---

# 10. Open questions

I did not decide these. Each has a named owner who should.

## Open question 1: `validator_id` names a registry entry, but the engine runs the one script in a directory

#1052's `assurance.recovery_test.validator_id` (global and per set, with `required` and `custom` modes demanding one) must name a validator registered in FR-13's catalog: `assuranceValidatorsKnown` refuses an unregistered id at save time using `isRegisteredValidator` (`core/service/validator.go:489`). #1059's engine ignores the id. It looks for scripts in `recovery-test/<source>/<set>`, requires exactly one, runs it, and records the script's name and hash as the validator's identity. So a set can carry a configured `validator_id` that is never run, and a set with a script in the directory and no `validator_id` runs a validator the configuration does not mention.

The options I can see: make the id select a script in that directory; make the id select a registry entry the engine then materialises into the directory; drop `validator_id` and make "a script exists" the configuration; or keep both and define precedence. Each changes what `mode: required` means and what the compat fixture `60-invalid-assurance-required-without-validator.yaml` asserts. Owners: #982 and #980. The answer decides FR-71's "validator identity" and the exit-gate line on a validator that exits 0 reading nothing, so Phase 1 cannot exit before it.

## Open question 2: migration 0016 alters an existing table

FR-35's compat cell `03-migrated-schema` may gain tables and migrations and "may never change or drop one that already exists" (`core/tests/compat/compat_test.go:83-85`, and `docs/conformance/epic-e-matrix.md:42-45`). #1056's migration adds roughly thirty columns to the existing `placements` table and one index, and re-captured cell 03 (`core/tests/compat/testdata/medium-free-surfaces.json`, where the `placements` line changed). The new `scrub_passes` table is within the rule; the `ALTER TABLE placements` statements are not, on a plain reading. The migration's own header argues that `ADD COLUMN` is safe because nothing references `placements` by foreign key; that is an argument about SQLite behaviour, not about what FR-35 allows.

The options: amend the cell's rule to allow additive columns with defaults on a table (and say in FR-35's text why that is still "byte for byte" for a medium-free deployment); move the per-copy record into a new table keyed by placement id, at the cost of a join on the due scan; or accept the re-capture as a deliberate one-time exception recorded in the epic E matrix. Owners: #978, with the repository owner deciding whether FR-35 is being amended. This is a question about a promise made to existing deployments, which is why I did not settle it by reading the migration.

## Open question 3: the resolved assurance policy has no consumer yet

#1052 resolves the global and per-set policy (`Assurance` on `Config` and on each `BackupSet`, with an `assurance_test` that no consumer reads the pointers). In #1056 and #1059 the scrub driver reads `bs.Revalidation` and the recovery test reads a constant. As the pull requests stand, an operator can set `assurance.scrub.cadence` and nothing scrubs differently. This is the same shape as the unused `revalidation:` block that opened this epic, and the exit-gate line "a global `PATCH` moves an inheriting set through the route" is satisfied on the route and not yet on the behaviour. Owners: whoever composes #1052 with #1056 and #1059; the compose-before-merge check should include a test that a changed `assurance.scrub.cadence` changes what is due.

---

# 11. Compatibility and migration summary

- A deployment whose configuration mentions nothing new is unchanged in behaviour: no new key is required, the global default is computed, and a config that never names the new block round-trips byte for byte (FR-73's compat fixtures).
- `Load` uses `KnownFields(true)`, so an `assurance:` key cannot be parsed by an older build. Anyone who edits that block during an incident and then rolls back has to take it out first. #1052's CHANGELOG entry and #987's one-way-door entry say so, and the question is whether anything in the wizard's re-marshal writes the block unprompted (it must not).
- One forward migration, `0016`. An older binary meeting the new schema version fails closed under the existing unsupported-downgrade behaviour (`state.ErrUnknownSchemaVersion`). `core/internal/state/migrate.go:100-105` refuses a duplicate version and nothing else, so the gap left by `0014` and `0015` is safe if M or N is dropped or reordered. The FR block is not safe to leave unclaimed: if this epic were abandoned, FR-67 to FR-74 would be a second FR-25-shaped hole and its block goes back to the checklist's "next free number" line.
- The state strings the migration stores are a compatibility surface: a value may not be renamed to match a JSON convention elsewhere, and a value may not be removed once anything has written it. `unknown` as the zero value is part of that surface.
- Nothing is renamed: `retnd restore` stays FR-34's archival thaw and `retnd snapshot restore` stays the repository extract. The canonical deployment (compose file, per-platform compose files, templates, catalog and packaging manifests) must stay byte-identical, which is Phase 1's exit line and #983's test.
- Sibling epics landing beside this one specified together: M (#951, FR-51 to FR-58, migration 0014, ADRs 0025/0026), N (#967, FR-59 to FR-66, 0015, 0027/0028), Q (#953, FR-67 to FR-74, 0016, 0029/0030) and P (#955, FR-75 to FR-82, 0017, 0031/0032). M and N are independent and compose. Q works with both off and may not call a copy recoverable because ciphertext exists or because a lock exists. P composes with M's and N's effective policies rather than forking them, and an expected-offline destination satisfies no `required` policy.

---

# 12. Definition of done

The functional definition of done is the two exit gates in section 5, which the matrix tracks line by line. The checklist obligations are section 7. The rules that make this section worth reading sceptically:

- A line is ticked only when something that can be run proves it, and the matrix row says what and whether that proof is gated (something goes red if it stops being true) or by hand.
- No row is green because nobody looked. All rows land `BLOCKED` with this file, including rows whose proof sits on an open branch, and each flips in the pull request that lands its proof, never before.
- Nothing in the epic is done while an open issue carries its label. Closing #953 needs every sub-issue closed, the matrix without a `BLOCKED` row, and the three open questions in section 10 answered in writing.
- What this epic cannot prove from here is stated rather than rounded up: no recovery has been measured on provider hardware, and store-listing screenshots need real hardware, a real installation and a real SFTP source (`docs/submission/screenshots.md`). `index.html#honest` is where that stays honest.
