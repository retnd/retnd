# Retiring a FAILED backup whose source is gone (#1528)

`cicd-pipeline/forge` said FAILING for two weeks while a good backup arrived every morning. Two
artifacts sat in `FAILED`, their source runs long since deleted by the host's own retention, and
`retry` was the only verb that touched them: it puts the row back in the pipeline, where it can only
fail again. This note is the design for getting out of that state on purpose, written before the
code, and kept as the record of what was decided and why.

The short version... `RETIRED` is a new lifecycle state, reached only from `FAILED`, recorded in the
same SQLite transaction as a retirement row that says who retired it, why, what the source looked
like at that moment and which newer backup replaces it. An operator gets `retnd retire` and the
matching route. The engine retires on its own only on fresh, classified evidence that the object is
gone and that a newer good backup exists, and it undoes its own retirement if the object ever comes
back. Health stops counting `RETIRED` as a failure and reports it as `retired_count`. The artifact
list routes get a state filter and a cursor, so the MCP tools that read them can always narrow.

## What changed from the issue's design, and why

The issue's proposal survived mostly intact. Six things moved, each because a reviewer could show
the original sentence would be false in the tree as it stands:

1. **"Its failure was classified as the source being gone" is not evidence this engine has.** A
   `FAILED` row carries no category, only the free-text detail of its last transition, and
   failure-safety invariant 12 forbids any decision that parses that text. The forge's second
   artifact also shows why the historical reason is the wrong question: it failed with
   `permission_denied`, and its source is just as gone. The automatic predicate reads **fresh,
   classified evidence from now** instead: the set's own discovery listing for this cycle does not
   show the object, and a direct `Stat` of its remote path answers with the transport category
   `not_found`. The old reason is recorded on the retirement row for the audit and never branched
   on.
2. **The retirement can be undone by the evidence that justified it.** A temporarily empty mount
   lists nothing and stats `not_found`, so any single-pass rule can retire a live object. Rather
   than a multi-pass delay (which needs its own durable observation table and is still wrong for an
   outage longer than the delay), `RETIRED` gets exactly one exit: back to `DISCOVERED`, taken only
   by discovery, only when the same remote path is listed and proven complete again, and only when
   the retirement recorded the source as confirmed absent. A retirement made over a present or
   unreachable source (the operator's explicit acknowledgement) stays put. So the state is terminal
   for everything except the one fact that would make it wrong.
3. **"One transaction with the state transition" cannot include deleting the `.partial` file.** A
   file unlink is not part of a SQLite transaction. The transition and the retirement row commit
   together; the leftover `.partial` is removed after the commit, and every cycle sweeps the
   `.partial` of any `RETIRED` row in the set, so a crash between the two leaves `RETIRED` with a
   file that the next pass removes. Nothing writes that `.partial` once the row is `RETIRED`.
4. **A FAILED row whose own durable copy sits at its final name is never retired** (issue #662's
   shape). `retry` rescues that row in place; retiring it would orphan a real backup file that no
   retention pass would ever account for. Both paths refuse it, and the operator path says "retry
   instead".
5. **An agent may only propose the conservative retirement.** The two acknowledgements
   (`accept_source_present`, `no_successor`) are exactly the cases where a failure might still
   mean something, and a confirmation dialog is a weak barrier against approval fatigue. The tool
   exists at `operate` scope and waits for a person, as asked, but the catalogue declares those two
   body fields as ones an agent may not set, and the proposer refuses them with
   `AGENT_RETIRE_ACK_REFUSED` before a person is ever asked.
6. **The dialog needs a read before the write.** The issue's design had only the action. The web
   UI (designed separately) has to show whether the source object is still there and which backup
   would replace it before anybody clicks, and the browser must not compute either (checklist
   section 3). So there is a preview route, `GET /backups/{source}/{set}/{name}/retire`, which runs
   the same probe and returns the same refusal the action would, plus engine-owned sentences. Its
   terminal twin is `retnd retire --dry-run`, and an agent reads it through a read-scope tool.

What did **not** change: `RETIRED` is a state and not a flag (the issue's reasoning holds: every
reader of `FAILED` would otherwise have to remember a second column, which is how this bug would
come back); `retry` keeps its meaning and is never automatic; nothing is deleted from the journal.

## The review

Seven reviewers, each with one lens, each asked to reject first. Their initial verdicts and the
consensus per decision follow. Dissents that survived are recorded rather than smoothed over.

### 1. Lifecycle state machine and crash safety

**Initial verdict: reject.** The issue promises an atomic retirement that includes a file delete,
which SQLite cannot give. A terminal state collides with `UNIQUE (source, backup_set,
artifact_name)`: a retired row blocks re-ingesting the same name forever if the object reappears.
The `artifacts.state` CHECK constraint lists thirteen states, so writing `RETIRED` fails at runtime
without a migration (`TestJournalAcceptsExactlyTheStatesTheMachineDefines` exists because this
exact drift happened for `QUARANTINED_LOST`). `reconcile.reconcileOne` returns an error for any state
it has no case for, and `TestNoStateIsALeak` requires every state to have a successor.

**Consensus.**
- Edges: `FAILED -> RETIRED` and `RETIRED -> DISCOVERED`. Nothing else enters or leaves.
- `RETIRED` is not exceptional (`IsExceptionalState` false: it is the end of a failure, not one),
  not a durable restore point, holds no local copy (`StatesWithNoLocalCopy`), and is not in
  `noAutomaticExit` because its one exit is taken by discovery.
- The transition and the retirement row are one `state.Transition` with a new `Retirement` field,
  written inside `RecordTransition`'s transaction exactly as `Placement` already is. The `From`
  check inside that transaction is the compare-and-swap: a concurrent `retry` and a retirement
  cannot both win.
- Migration **`0023_retired_artifacts.sql`**. `0014` (EPIC M) and `0017` (EPIC P) stay reserved
  holes; `0022` is the last landed; no open pull request carries `0023`. It rebuilds `artifacts` the
  way `0006` did to widen the CHECK, and creates `artifact_retirements`, an append-only table keyed
  by the transition's idempotency key, with no CHECK on its vocabulary columns for the reason
  `0012` gives.
- Reconcile treats `RETIRED` as `noAction`, beside `FAILED` and the quarantine states.
- `scripts/release/check-epic-u-closed.sh` counted migrations since EPIC U's base and would turn red
  on `0023`; it gets an `EPIC_U_TIP` the way #1407 gave the T script `EPIC_T_TIP`.

**Surviving dissent:** none on the edges. This reviewer would have preferred the retirement fields
as columns on `artifacts` (one read instead of a join); the table won because the history is
append-only and a row can be retired, brought back and retired again.

### 2. Health and operator semantics

**Initial verdict: reject.** "Health stops counting `RETIRED`" is not enough on its own:
`retained()` counts every state except the two quarantine ones toward `retained_count` (#1186), so
a retired row would be reported as a backup this deployment holds, which is false. The
`failures` metric gauge, the per-cycle `terminalFailure` count and `liveactivity`'s terminal-failure
classification each keep their own list. And in a set whose artifacts are distinct data rather than
successive dumps, an automatic retirement turns a lost file into a green verdict.

**Consensus.**
- `buildAggregate` gains `case lifecycle.Retired: agg.retiredCount++`; `RETIRED` contributes
  nothing to `failures`, `StuckFailures`, `hasStuckFailure` or `hasRetryingFailure`, and is
  excluded from `retained_count`.
- `BackupSetHealth.RetiredCount`, JSON `retired_count`, through service, webhost, openapi, both
  bindings, and a `retired` metrics gauge. `retnd status` prints it when non-zero.
- The verdict: a set whose only problem was a stuck `FAILED` row that is now `RETIRED` reads
  HEALTHY. `RepeatedFailure` alerts clear on the next evaluation because they key on `Failing` and
  on `Failures`.
- A transition into `RETIRED` is logged at warn for an automatic retirement and info for an
  operator's, with its own event `artifact_retired` (and `artifact_unretired` for the way back), so
  `activity --severity warn` finds every retirement the engine made by itself.

**Surviving dissent:** this reviewer wanted an automatic retirement to hold the set at DEGRADED for
seven days, so that a lost file in a collection-style set is noticed. Rejected because the owner's
stated expectation is HEALTHY, because nothing an operator can do changes the outcome once the
source is gone, and because the warn-level event, the activity feed and `retired_count` keep it
visible. Recorded so it can be revisited if a collection-style set ever loses something unnoticed.

### 3. The automatic-retirement safety case

**Initial verdict: reject.** The issue's predicate depends on a classification nothing records
(see change 1); a single listing races a temporarily empty mount; "newer good artifact" has no
definition; the #662 shape would be retired and its real file orphaned; a permission failure on a
file that still exists must never qualify; and nothing says what happens for read-only or
kopia-engine sets.

**Consensus: the exact predicate.** During a backup set's cycle, after discovery and the per-artifact
walk, the engine retires artifact *A* if and only if every one of these holds:

| # | Condition | Evidence | Why |
|---|---|---|---|
| P1 | *A* is `FAILED` | read, then re-checked as the transaction's `From` | only a failure can be retired |
| P2 | the set runs the artifact engine and is configured | the cycle only reaches this pass for such sets | kopia sets store snapshots and never have artifact rows of their own; their old rows from before an engine switch are left to the operator verb |
| P3 | a fresh listing of the source, taken once per set per pass and shared by every row the pass asks about, succeeded and shows no object with *A*'s identity: not at *A*'s remote path and not under its basename anywhere else | the pass's own listing (a failed listing is "unknown", never absent) | the listing is what discovery itself trusts, and discovery's identity is the basename: a file moved into `archive/` is still listed, so it is not gone |
| P4 | a `Stat` of *A*'s remote path, made after that listing, fails with transport category `not_found` | `transport.CategoryOf(err) == transport.NotFound` | classified per object; `permission_denied`, `transient`, `authentication`, `unclassified` and success all mean "not proven absent" |
| P5 | *A* has no durable copy at its final name | `rec.LocalPath != FinalArtifactPath(...)` or nothing is there | #662: `retry` rescues that row; retiring would orphan a real backup |
| P6 | a successor *B* exists | another row in the same set, `IsDurableRestorePoint(B.state)`, `B.discovered_at > A.discovered_at`, and when both rows carry `remote_mtime`, `B.remote_mtime > A.remote_mtime`; the newest such *B* by `discovered_at` (then name) is recorded | the failure has been superseded by a backup this deployment holds and vouches for |
| P7 | the cycle is still live | `ctx.Err() == nil` and no stop reason for the set | an edit hold or shutdown wins |

When it holds, one transaction moves *A* `FAILED -> RETIRED` and appends the retirement row
`{actor_kind: automatic, actor: engine, source_check: absent, superseded_by: B, failure_reason:
<A's last FAILED detail, as already stored>}`. Then *A*'s `.partial` is removed if present.

**The way back.** When a later discovery pass lists *A*'s remote path and proves it complete (it
lands in `AlreadyKnown` because the row exists) while *A* is `RETIRED` and its latest retirement
recorded `source_check: absent`, the same cycle moves *A* `RETIRED -> DISCOVERED` and counts it as a
new attempt (the retry count goes up, as `retry` and a quarantine release do), so the walk that
follows builds a fresh attempt key and transfers it like any new arrival. Without the count the walk
would present the failed attempt's own key, the journal would read it as a replay, and the row would
sit at DISCOVERED for good; `TestARetiredObjectThatReappearsReentersThePipeline_1528` asserts the
row reaches a durable backup, not just that it left RETIRED.

That count is spent on every return, so a mount that flaps (empty on one cycle, back on the next,
for a row whose object is genuinely gone each time it fails) spends one retry per flap: fail,
retire, come back, fail again. I'm fine with that, with one effect worth knowing about... the count
is FR-22's shared one, so verify's `StallBudget` reads it too: a row that has flapped three times
reaches QUARANTINED three hash stalls sooner, exactly as a row released from quarantine three times
does today (verify.go argues that direction is the right one, and I agree). The unclaimed-copy
budget in transfer.go is not shortened, because it also needs a charge from the same stay in
TRANSFERRING. Each flap needs the object to come back listed and complete, which a mount that is
merely empty does not do, and nothing deletes anything at a threshold. A deployment that sees the
count climb has a source that keeps losing and finding the same file, which is worth seeing.

**Passes.** One pass, two independent probes, plus reversibility. The listing and the `Stat` are
separate requests answered by separate code paths in the adapter, and a wrong answer from both is
undone by the object's return.

**Per kind of set.**
- Ordinary sets (delete after commit): the successor is usually `COMPLETE`; the source being empty
  between dumps is normal and fine, because P4 asks about one object and not about the directory.
- Read-only sets: the successor is `REMOTE_RETAINED`, which `IsDurableRestorePoint` includes.
- Kopia-engine sets: never reach the pass (P2).

**Never auto-retired:** anything not `FAILED` (P1); a failure on an object that still exists, or that
the source could not be asked about (P4); the #662 shape (P5); a set with no newer good backup,
which covers "the only copy" (P6). A `FAILED` row is never a copy at all, so "the newest good copy"
cannot be touched by construction; `TestRetireTouchesOnlyFailedRows_1528` pins that.

**Surviving dissent:** this reviewer wanted two observations at least an hour apart before an
automatic retirement. Answered by the way back: a premature retirement costs the cycles in which
the object was missing, and the cycle that lists it again puts the row back and backs it up. A
delay would add a durable observation table for a case the reversal already covers.

### 4. API, CLI and cliecho parity

**Initial verdict: reject.** The `retnd` CLI opens the journal itself (like `retry`), so a rule
written in the HTTP handler would be a second, drifting copy. The issue names no refusal codes, no
response body and no exit codes, and the acknowledgement it mentions has no name.

**Consensus.**
- All of the rule lives in `internal/app` (`PreviewRetirement`, `RetireArtifact`, `RetireSweep`);
  service, handler and CLI only translate.
- `GET /api/v1/backups/{source}/{set}/{name}/retire` (operationId `previewRetirement`) returns a
  `RetirementPreview`. `POST` on the same path (operationId `retireArtifact`, CSRF, no destructive
  gate since nothing is deleted but a `.partial`) takes `RetireArtifactRequest` and returns
  `RetireArtifactResponse`.
- Refusals, the same on both routes and in the preview's `refusal.code`:

| HTTP | Code | When | Overridable |
|---|---|---|---|
| 404 | `ARTIFACT_NOT_FOUND` | no such row | no |
| 404 | `BACKUP_SET_NOT_FOUND` | the set is no longer configured (#391) | no |
| 409 | `ARTIFACT_NOT_FAILED` | the row is not `FAILED` | no |
| 409 | `RETIRE_OWN_COPY_AT_FINAL_NAME` | #662 shape: retry instead | no |
| 409 | `RETIRE_SOURCE_PRESENT` | the object is listed or stats successfully: retry instead | `accept_source_present` |
| 409 | `RETIRE_SOURCE_UNCONFIRMED` | listing or `Stat` failed with anything but `not_found` | `accept_source_present` |
| 409 | `RETIRE_NO_SUCCESSOR` | no newer durable backup in the set | `no_successor` |
| 409 | `RETIRE_SUCCESSOR_INVALID` | `superseded_by` names a row that is not in the set, not a durable restore point, or not newer | no |
| 403 | `AGENT_RETIRE_ACK_REFUSED` | an agent sent either acknowledgement | no |

- CLI: `retnd retire <source/set/artifact> [--superseded-by NAME] [--no-successor]
  [--accept-source-present] [--note TEXT] [--dry-run]`. A refusal exits 1 with the same sentence
  the API returns, as `retry` does; `--dry-run` prints the preview and exits 0.
- `cliecho` entries for both routes (the GET echoes `--dry-run`), dispatch-table entry, the
  `reference.html` row, the usage block pinned in compat cell `06b`, openapi plus both bindings.

**Surviving dissent:** none.

### 5. MCP and agent safety

**Initial verdict: reject.** `backups_list` cannot page because `ListArtifactsResponse` carries no
`next_cursor`, which is the only signal `agentmcp` pages on; it has no state filter. A retire tool
can be used to make a set look green, and the catalogue's own precedent refuses reinstatement to
agents (`whyDestructive`).

**Consensus.**
- `GET /backups` and `GET /quarantine` gain `state` (one state or a comma list, each validated
  against the lifecycle vocabulary, `INVALID_REQUEST` otherwise), keep `setId`, and gain `limit`
  and `before`. The response always carries `next_cursor` (null when there is nothing more), so
  `agentmcp`'s existing halving loop applies. With `limit` the order is `(source, set, name)` and
  the cursor is that key of the last row, opaque base64url: keyset paging is stable while rows are
  added between pages. Without `limit` the answer and its order are exactly what they were.
- `retire_backup` (operate, `POST .../retire`), with `AgentMayNotSet: accept_source_present,
  no_successor` on its catalogue row. The proposer refuses those fields before creating a
  confirmation, and the handler refuses them again on replay as an agent (defence in depth).
- `backup_retirement_show` (read, `GET .../retire`). Untrusted fields: `artifact`, `failure_reason`,
  `proposed_successor.name`, `retirement.superseded_by`, `retirement.note`,
  `retirement.failure_reason`. Every sentence the engine writes into the preview is built from fixed
  templates and never interpolates a remote string.
- Audit: the confirmation, the decision and the replay rows come from the existing flow; the
  retirement row records `actor_kind: agent` and `actor: agent:<name>` when it was an approved
  replay, `operator` with the session's user otherwise.
- The route and tool walk for the last three weeks is in "Audit of recent routes" below.

**Surviving dissent:** this reviewer would refuse the retire tool outright under `whyDestructive`.
Overruled because nothing is deleted, the agent can only propose the retirement the engine itself
would have made, and a person approves the exact echoed command.

### 6. The web UI contract

**Initial verdict: reject.** The issue says the PR carries a Retire action in the web UI. The UI is
being designed separately and comes later; what this PR owes it is a contract under which the
browser computes nothing.

**Consensus.** The API and the TypeScript contract types first; the dialog itself followed in the same
pull request once the design landed (`ui/shared/src/pages/RetireDialog.tsx`, the Retire… button on a
failed backup's page, the Retire… item in the Backups row menu, the retirement card on a retired
backup's page and the retired count on a set card). Where the design and the engine disagree the
engine wins: one pass that finds the object gone retires it (the design says three checks), and the
acknowledgement is `--no-successor`. The dialog reads everything from the preview:

- `source_check`: `absent` | `present` | `unknown`, and `source_check_sentence`, exactly one of:
  - absent: "The source object is gone: this backup set's listing does not show it and the source answered not found for it."
  - present: "The source object is still there, so this backup can still be fetched. Retry it instead."
  - unknown: "The source could not be asked about this object (`<category>`), so this manager cannot say whether it is gone."
- `proposed_successor`: `{name, state, discovered_at}`, absent when there is none.
- `successor_candidates`: every newer good backup, newest first, for the dialog's "Replaced by"
  choice; picking another asks the preview again with `superseded_by`, so the effects and the
  terminal line shown are the engine's for exactly what will be sent. Ticking the no-successor
  acknowledgement asks again with `no_successor` the same way.
- `refusal`: `{code, sentence, overridable_with}`, absent when the action would go through, where
  `overridable_with` is `accept_source_present`, `no_successor` or empty.
- `effects`, in this order, each included only when true:
  - "The backup record stays, marked RETIRED, with who retired it, when and why."
  - "This backup set stops counting it as a failure. It shows under retired instead."
  - "The leftover partial download for this backup is removed."
  - "Nothing on the source is touched and no backup copy is deleted."
  - "If the source object appears again, the next cycle puts this backup back into the pipeline."
  - "This manager will not fetch this object again." (only with `accept_source_present`)
- `retirement` (present only when already `RETIRED`): `{actor_kind, actor, retired_at, source_check,
  superseded_by, failure_reason, note}`.
- `source_check_category`: for `unknown`, the transport category behind it; empty otherwise.

The three objects are absent rather than null because the generated TypeScript types them as
optional, and a wire value the types do not admit is how a dialog breaks in a way no compiler sees.
- `command`: the `retnd retire` line the action is equivalent to, for `CommandEcho`.

**Surviving dissent:** none. The design's own copy may shorten these sentences; if it does, the
engine changes and the UI keeps reading.

### 7. Conformance, test strategy, and whether each piece should exist

**Initial verdict: reject several pieces.** A per-set `auto_retire` knob: no, nobody asked, and it
would be a one-way door in `config.yaml`. A second "unretire" verb: no, the reversal covers the
honest case. A conformance matrix: yes, because this touches a migration, a route, a verb, an MCP
tool and health, which is epic-sized, but as a small file and not registered in
`core/tests/compat/matrix_test.go` (that table is for epics with phase gates). Planted violations:
yes, each with a negative control, run by a self-test the gate runs.

**Consensus.** `docs/conformance/issue-1528-matrix.md`, rows `R.n` and planted violations `RV.n`;
`scripts/retire/selftest.sh` plants each violation into a scratch copy of the tree, runs the named
test, needs it red, and needs it green on the unplanted copy; `gate_step` lines in
`scripts/ci-local.sh` with stubs in `scripts/tests/ci-local-gate.test.sh`.

**Surviving dissent:** the skeptic still thinks the preview route might be one route too many. It
stays because the dialog cannot be honest without it, and because `--dry-run` is the operator's way
to look before acting.

## State diagram

```
DISCOVERED ... COMMITTING --(permanent error)--> FAILED --retry (operator)--> DISCOVERED
                                                   |
                                                   | retire: operator, agent (confirmed),
                                                   | or the engine when P1..P7 hold
                                                   v
                                                RETIRED --(discovery lists the same object again,
                                                   ^       retirement recorded source absent)--> DISCOVERED
                                                   |
                                     (no other way in or out)
```

## Crash table

The operation is: read the row; probe the source; one transaction (`FAILED -> RETIRED` plus the
retirement row); remove the `.partial`. Each sweep and the way back are single transitions.

| Crash point | Journal after restart | Disk | What the next pass does |
|---|---|---|---|
| before the probe | `FAILED`, no retirement row | `.partial` as left | evaluates again from scratch |
| after the probe, before the transaction | `FAILED`, no retirement row | unchanged | evaluates again; the probe is repeated, never trusted from memory |
| inside the transaction | `FAILED`, no retirement row (rolled back) | unchanged | as above |
| after commit, before the unlink | `RETIRED` plus its retirement row | `.partial` still there | `RetireSweep` removes it; idempotent |
| after the unlink | `RETIRED` plus its retirement row | clean | nothing |
| replayed call with the same key | unchanged (`Applied: false`) | unchanged | nothing; the key is per decision, so a second click is a new decision and is refused as `ARTIFACT_NOT_FAILED` |
| way back, inside its transaction | `RETIRED` (rolled back) | n/a | next discovery repeats it |
| way back, after commit | `DISCOVERED`, retry count one higher | n/a | the walk transfers it under a fresh attempt key |

A transaction that half-writes is impossible by construction (one `RecordTransition`), and
`TestARetirementAndItsRecordCommitOrRollBackTogether_1528` plants a failure between the two writes
to prove it.

## Audit of recent routes and tools

I walked every `/api/v1` route and agent tool added since 2026-10-29. The catalogue guard
(`TestEveryAPIRouteIsAToolOrANamedRefusal`) already holds every route to a tool or a named
refusal, so the gap was not coverage but size: the list routes whose answers grow with history.

| Tool | Route | Grows with | Before | After |
|---|---|---|---|---|
| `backups_list` | `GET /backups` | every artifact ever | no filter but `setId`, no cursor: dead end | `state`, `setId`, `limit`, `before`, `next_cursor` |
| `quarantine_list` | `GET /quarantine` | quarantined artifacts | no cursor | `setId`, `limit`, `before`, `next_cursor` |
| `activity` | `GET /activity` | events | cursor | unchanged |
| `workflow_runs_list` | `GET /workflow-runs` | runs | `limit`, `backup_set` | unchanged: narrowable, not a dead end |
| `assurance_due` | `GET /assurance/due` | copies | `backup_set`, `status` | unchanged: narrowable |
| `backup_sets_list` | `GET /backup-sets` | configured sets | none | unchanged: bounded by configuration |

## Test plan, red first

The first commit on the branch after this note holds the red tests (`Local-Gate: red`); the fix
turned them green. Every name below exists in the tree.

**The forge reproduction and its negative control** (`core/internal/app/retire_1528_test.go`):
- `TestTheForgeSetGoesFromFailingToHealthyWhenTheSourceIsGone_1528`: built through the real cycle.
  One dump fails with `not_found`, one with `permission_denied`, a newer dump is backed up, the host
  deletes the two failed ones. Health reads FAILING, the next cycle retires both, health reads
  HEALTHY with `failures` 0 and `retired_count` 2, and both records are still there as `RETIRED`.
- `TestNothingIsRetiredWhileTheSourceIsStillThere_1528`: the same set with the failed objects still
  on the source. Nothing changes and the set stays FAILING.

**The predicate, one test per condition** (same file): `TestAPermissionFailureOnAPresentFileIsNeverAutoRetired_1528`
and `TestATransientStatIsNotAbsence_1528` (P4), `TestNoNewerGoodBackupMeansNoAutomaticRetirement_1528`,
`TestAnOlderGoodBackupIsNotASuccessor_1528` and `TestAQuarantinedSuccessorDoesNotCount_1528` (P6),
`TestTheOwnCopyAtTheFinalNameIsNeverRetired_1528` (P5), `TestRetireTouchesOnlyFailedRows_1528` (P1),
`TestARetiredObjectThatReappearsReentersThePipeline_1528` and
`TestAnAcknowledgedRetirementStaysWhenTheObjectIsListed_1528` (the way back),
`TestTheLeftoverPartialIsRemovedWithTheRetirement_1528`.

**The operator path**: `TestOperatorRetireRefusesWhileTheSourceIsPresent_1528`,
`TestOperatorRetireWithoutASuccessorNeedsTheAcknowledgement_1528`,
`TestANamedSuccessorMustBeNewerGoodAndInTheSameSet_1528`, `TestThePreviewAndTheActionRefuseAlike_1528`
(`core/internal/app`); `TestTheRetireRouteRetiresAndAnswersWithTheRecord_1528`,
`TestThePreviewSaysWhetherTheSourceIsStillThere_1528`, `TestTheRetireRouteCarriesTheBodyToTheEngine_1528`,
`TestTheRetireRouteAnswersEveryRefusalByName_1528` (`apps/common/webhost`);
`TestRetireIsDiscoverableFromTheCommandLine_1528` (`core/cmd/retnd`).

**Lifecycle and journal**: `TestRetiredIsAStateTheMachineKnows_1528`, `TestRetiredIsReachableOnlyFromFailed_1528`,
`TestRetiredLeavesOnlyForDiscovered_1528`, `TestRetiredIsNotExceptionalAndHoldsNoCopy_1528`
(`core/internal/lifecycle`); `TestMigration0023KeepsEveryRowAndAdmitsRetired_1528` and
`TestARetirementAndItsRecordCommitOrRollBackTogether_1528` (`core/internal/state`), plus the existing
guards that went red the moment the state existed without its migration
(`TestJournalAcceptsExactlyTheStatesTheMachineDefines`, `TestEveryStateIsClassifiedAsHoldingALocalCopyOrNot`,
`TestAllStatesAreValidAndDistinct`, `TestEveryLifecycleState_IsVisibleToTheCycleVerdict`).

**Health and reconcile**: `TestRetiredIsCountedApartAndNeverFails_1528` (with a FAILED control) and
`TestRetiredIsNotARetainedBackup_1528` (`core/internal/health`); `TestReconcileLeavesRetiredAlone_1528`.

**MCP and listing**: `TestTheBackupsListAlwaysCarriesACursor_1528`,
`TestTheBackupsListRefusesAStateItDoesNotKnow_1528`, `TestTheBackupsListFiltersByStateAndPagesWithACursor_1528`,
`TestAnAgentCannotSendARetireAcknowledgement_1528` (with the conservative proposal as its control),
and the catalogue's own guards (`TestEveryAPIRouteIsAToolOrANamedRefusal`,
`TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap`, `TestTheCatalogueHoldsTheSpecsNamesAndNothingElse`).
The halving loop that turns `next_cursor` into smaller pages is `agentmcp`'s and already pinned by
`TestAnOversizeAnswerIsPagedWithTheRoutesCursorAndNeverTruncated`.

**Crash cells**, in process, because the SIGKILL harness drives only the pipeline:
`TestCrash_RetireBeforeCommitLeavesFailed_1528` (the write refused inside its transaction: the row
stays FAILED, no record, the `.partial` untouched, and the next cycle retires it) and
`TestCrash_RetireAfterCommitBeforeUnlinkIsSwept_1528` (RETIRED with a `.partial` still on disk: the
next cycle sweeps it).

**Planted violations**: `scripts/retire/selftest.sh` plants each `RV.n` row of
`docs/conformance/issue-1528-matrix.md` in a scratch copy of the tree and needs the named test red,
then green on the unplanted copy. `scripts/ci-local.sh` runs it in the `selftest-recovery` shard and
its anchor check (`--check-anchors`, builds nothing) in the static shard; both are stubbed in
`scripts/tests/ci-local-gate.test.sh`.

## Risks

- **A migration in what would otherwise be a patch release.** 0.8.0 refuses a `state.db` that has
  applied `0023`, like every release with a migration. This is a one-way door and the CHANGELOG says
  so; the owner decides whether it ships as 0.8.1 or waits for 0.9.0.
- **A collection-style set loses a file quietly.** See health's dissent. Mitigated by the warn
  event and the count, not by the verdict.
- **A source that renames objects in place** (compresses `x.tar` to `x.tar.gz`) looks like a gone
  object with a newer successor, and is retired. That is the correct description of what happened
  to the old name.
- **Keyset paging misses rows inserted before the cursor between pages.** Acceptable for a read
  the agent can repeat; the alternative (offset paging) duplicates and skips under the same race.
