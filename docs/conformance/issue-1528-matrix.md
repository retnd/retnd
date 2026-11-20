# Issue #1528 conformance matrix: retiring a FAILED backup whose source is gone

One row per claim `docs/design/1528-retire-failed-artifacts.md` makes, each tied to the test that goes
red if the claim stops being true, plus the mutation that has to be run and watched to fail before the
row may say `PASS`. It follows the epic matrices' layout (`docs/conformance/epic-t-matrix.md`), at the
size of one issue. It has no phase gates, so it is not registered in `core/tests/compat/matrix_test.go`;
what holds it to the tree is `scripts/retire/selftest.sh`, which plants every `RV.n` violation below into
a scratch copy of the tree and needs the named test red, and its negative controls green.

## Reading a row

| Column | Means |
|---|---|
| ID | `R.n` for a claim, `RV.n` for a planted violation |
| Claim | What is asserted |
| Executable proof | `<package dir>:<TestName>` |
| Falsification | The mutation that must turn the proof red |
| Status | `BLOCKED (#1528)` until the falsification has run; `PASS (date)` once it has been watched to fail; `PARTIAL` when only one half was |

## Claims (`R.n`)

| ID | Claim | Executable proof | Falsification (to run, and watch fail) | Status |
|---|---|---|---|---|
| R.1 | The forge set goes from FAILING to HEALTHY when its failed sources are gone and a newer good backup exists, and both records stay as `RETIRED` | `core/internal/app:TestTheForgeSetGoesFromFailingToHealthyWhenTheSourceIsGone_1528` | Skip the automatic pass (RV.1) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.1 planted, the named test went red, then green unplanted) |
| R.2 | Nothing is retired while the failed object is still on the source | `core/internal/app:TestNothingIsRetiredWhileTheSourceIsStillThere_1528` | Drop the listing check and the `Stat` check (RV.2) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.2 planted, the named test went red, then green unplanted) |
| R.3 | Only `not_found` from `Stat` proves absence; `permission_denied` and `transient` do not | `core/internal/app:TestAPermissionFailureOnAPresentFileIsNeverAutoRetired_1528`, `core/internal/app:TestATransientStatIsNotAbsence_1528` | Treat any `Stat` error as absence (RV.3) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.3 planted, the named test went red, then green unplanted) |
| R.4 | A failure with no newer durable backup in the set is never retired automatically | `core/internal/app:TestNoNewerGoodBackupMeansNoAutomaticRetirement_1528`, `core/internal/app:TestAnOlderGoodBackupIsNotASuccessor_1528`, `core/internal/app:TestAQuarantinedSuccessorDoesNotCount_1528` | Accept any durable row as the successor, older or not (RV.4) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.4 planted, the named test went red, then green unplanted) |
| R.5 | The #662 shape (the row's own copy at its final name) is never retired | `core/internal/app:TestTheOwnCopyAtTheFinalNameIsNeverRetired_1528` | Remove the final-name check (RV.5) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.5 planted, the named test went red, then green unplanted) |
| R.6 | A retired object that reappears goes back into the pipeline | `core/internal/app:TestARetiredObjectThatReappearsReentersThePipeline_1528` | Skip the way back in discovery (RV.6) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.6 planted, the named test went red, then green unplanted) |
| R.7 | The leftover `.partial` is removed with the retirement, and swept after a crash between commit and unlink | `core/internal/app:TestTheLeftoverPartialIsRemovedWithTheRetirement_1528`, `core/internal/app:TestCrash_RetireAfterCommitBeforeUnlinkIsSwept_1528` | Skip the unlink (RV.7) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.7 planted, the named test went red, then green unplanted) |
| R.8 | `RETIRED` is reached only from `FAILED`, leaves only for `DISCOVERED`, is not exceptional and holds no copy | `core/internal/lifecycle:TestRetiredIsReachableOnlyFromFailed_1528`, `core/internal/lifecycle:TestRetiredLeavesOnlyForDiscovered_1528`, `core/internal/lifecycle:TestRetiredIsNotExceptionalAndHoldsNoCopy_1528` | Add a `COMMITTED -> RETIRED` edge (RV.8) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.8 planted, the named test went red, then green unplanted) |
| R.9 | Migration `0023` keeps every row and admits `RETIRED`, and the journal's CHECK matches the machine | `core/internal/state:TestMigration0023KeepsEveryRowAndAdmitsRetired_1528`, `core/internal/state:TestJournalAcceptsExactlyTheStatesTheMachineDefines` | Leave `RETIRED` out of the CHECK in a scratch copy of `0023` | PASS (2026-11-19, red first: before `0023` existed the CHECK refused `RETIRED` and `TestMigration0023KeepsEveryRowAndAdmitsRetired_1528` failed on exactly that) |
| R.10 | The transition and its retirement row commit or roll back together | `core/internal/state:TestARetirementAndItsRecordCommitOrRollBackTogether_1528`, `core/internal/app:TestCrash_RetireBeforeCommitLeavesFailed_1528` | Ignore a failed record write so the transition commits without it (RV.9) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.9 planted, the named test went red, then green unplanted) |
| R.11 | Health counts `RETIRED` apart (`retired_count`), never as a failure, and never as a retained backup | `core/internal/health:TestRetiredIsCountedApartAndNeverFails_1528` | Count `RETIRED` under `failures` (RV.10) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.10 planted, the named test went red, then green unplanted) |
| R.12 | The operator route and verb refuse a present source, a missing successor and a non-FAILED row by name, the same way, and the preview says the same | `apps/common/webhost:TestTheRetireRouteRetiresAndAnswersWithTheRecord_1528`, `core/internal/app:TestOperatorRetireRefusesWhileTheSourceIsPresent_1528` | Let the operator path skip the source check (RV.11) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.11 planted, the named test went red, then green unplanted) |
| R.13 | An agent cannot send either acknowledgement | `apps/common/webhost:TestAnAgentCannotSendARetireAcknowledgement_1528` | Drop the catalogue's `AgentMayNotSet` check in the proposer (RV.12) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.12 planted, the named test went red, then green unplanted) |
| R.14 | `GET /backups` filters by state and always carries `next_cursor`, so the MCP list tools can page instead of refusing | `apps/common/webhost:TestTheBackupsListAlwaysCarriesACursor_1528`, `apps/common/webhost:TestTheBackupsListRefusesAStateItDoesNotKnow_1528`, `apps/common/webhost:TestTheBackupsListFiltersByStateAndPagesWithACursor_1528` | Omit `next_cursor` when it is null (RV.13) | PASS (2026-11-20, `scripts/retire/selftest.sh` on MARS: RV.13 planted, the named test went red, then green unplanted) |
| R.15 | `retnd retire` is on the dispatch table, in usage, and in `docs/site/reference.html` | `core/cmd/retnd:TestRetireIsDiscoverableFromTheCommandLine_1528`, `distribution/packaging/site_reference_test.go` | Drop the reference row | PARTIAL (2026-11-19, red first: `TestRetireIsDiscoverableFromTheCommandLine_1528` failed with no `retire` command; the reference-row half rests on `distribution/packaging/site_reference_test.go` and was not planted here) |

## The planted-violation ledger (`RV.n`)

Each is a one-line change `scripts/retire/selftest.sh` makes in a scratch copy; the negative control is
the same run on the unplanted copy, which must be green.

| ID | Planted violation | Must turn red |
|---|---|---|
| RV.1 | the cycle never calls the automatic retirement pass | R.1 |
| RV.2 | the predicate ignores both absence probes | R.2 |
| RV.3 | any `Stat` error counts as absence | R.3 |
| RV.4 | the successor search drops both "newer" comparisons | R.4 |
| RV.5 | the final-name check always passes | R.5 |
| RV.6 | discovery never takes `RETIRED -> DISCOVERED` | R.6 |
| RV.7 | the `.partial` is never removed | R.7 |
| RV.8 | `COMMITTED -> RETIRED` appears in the table | R.8 |
| RV.9 | a failed retirement-record write is ignored, so the transition commits without its record | R.10 |
| RV.10 | health counts `RETIRED` as a failure | R.11 |
| RV.11 | the operator path does not probe the source | R.12 |
| RV.12 | the proposer ignores `AgentMayNotSet` | R.13 |
| RV.13 | `next_cursor` is omitted when empty | R.14 |

## The limits of this matrix

The crash cells are in-process (a journal that fails on command between the two writes), not the
SIGKILL harness under `core/tests/crashmatrix`, which drives only the pipeline. The web UI is not in
this pull request and has no row here; its contract is the preview route's shape.
