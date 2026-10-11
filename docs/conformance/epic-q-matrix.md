# EPIC Q conformance matrix: artifact recovery, scheduled scrubbing and recoverability assurance

What this file is for, and what it is not. It is not a summary of the work: it is the list of claims
EPIC Q (#953) makes, each one tied to the test that would go red if the claim stopped being true, plus
the **mutation that was actually run and watched to fail**. A row says `PASS` only because a named
mutation, planted as a real violation in a real product file by a self-test that lives in this
repository, turned the named test red. A row says `BY HAND` when no self-test plants that violation,
and then it says who did it, what they did and on which day, in the row, rather than borrowing a
neighbouring green. There is no `BLOCKED` row: the epic does not close with one, and
`core/tests/assurancegate` fails the build if one appears.

`docs/epic-checklist.md` section 1 calls this the ungated item that matters most. It is gated here
anyway. `core/tests/assurancegate/matrix_test.go` reads this file and fails when one of the nineteen
adversarial-review findings has no row, when a row names a test that does not exist in the tree, when
a `PASS` row cites a mutation its self-test does not contain, when a `BY HAND` row does not name a
person and a date, and when any row is `BLOCKED`. That guard has its own mutation test,
`TestTheMatrixCheckerRefusesEveryWayAMatrixCanLie`, because a guard whose only evidence is that it
passes on the tree it shipped with has proven nothing.

**How to read a proof reference.** `<package dir>:<TestName>` is a Go test function that has to exist
in that directory; a bare path is a file that has to exist (the Playwright specs under
`ui/shared/e2e/` are cited that way, since they are TypeScript). **How to read a falsification.**
`<self-test file>` ("<label>") is the verbatim label of a mutation in that file; the guard checks the
label is really there. The two self-tests are `scripts/bdtools/recovery/selftest.py` (#976, run by
`scripts/recovery/selftest.sh`: the route, the grant, the cost refusal, the budgets) and
`scripts/bdtools/assurance/selftest.py` (#988, run by `scripts/assurance/selftest.sh`: the claims
themselves).

---

## What these rows do not say

Stated here because a gate that did not say what it is defending would invite the wrong conclusions
from the rows below.

- **A reachable copy is not a recovered one.** The Stored claim says a copy is recorded and, at the
  class named beside it, that something looked. An existence check is a HEAD: it says an object of the
  right size is at that key. Nothing in the Stored column says the bytes can be read, and nothing
  short of a recovery test says they can be put back to use. That is the whole reason there are three
  claims.
- **A recovery test says the bytes came back and the operator's validator accepted them.** It does not
  say the application will start. Restoring into an application stays the operator's responsibility
  (QC-15).
- **The captures are not a store screenshot.** `docs/site/screens/recovery-*.png` are documentation
  pictures of the real component tree rendering the mock API. `docs/submission/screenshots.md` needs
  real hardware, a real installation and a real SFTP source, and a mock must never be substituted
  there; two of its four rows are back to outstanding because this epic changed those screens.
- **The web client does not yet offer Scrub now or Run recovery test.** There is no `api/v1` route
  that starts either, so a real deployment's browser shows the equivalent `retnd scrub` command and
  nothing to click, and the pictures of those controls (`recovery-04` .. `recovery-10`) show the
  design the mock offers.
- **Suite C proves equivalence in both directions except one, the ungated direction.** A recovery
  completes on a deployment whose destructive gate is shut (QC-01), and the CLI has no gate to be
  shut, so the two cannot be put side by side on that axis. The browser half is asserted in Suite C
  (`assurance-recovery.986.spec.ts`: the engine that just completed a browser recovery still answers
  `403 DESTRUCTIVE_OPERATIONS_DISABLED` to `POST /api/v1/operations`) and in
  `apps/common/webhost:TestARecoveryCompletesWhileTheDestructiveGateIsShutAndOperationsStillRefuses`.
  The matrix does not claim the CLI side of that axis, because there is nothing there to claim.

---

## The findings

| ID | Claim | Proof | Falsification | Status |
|---|---|---|---|---|
| QC-01 | Recovery is its own route pair, registered outside the **destructive gate**, with CSRF, and a recovery completes on a deployment whose gate reports false while `POST /operations` still refuses | `apps/common/webhost:TestARecoveryCompletesWhileTheDestructiveGateIsShutAndOperationsStillRefuses` `apps/common/webhost:TestMintCarriesCSRFAndRedeemNeedsOnlyTheSession` | `scripts/bdtools/recovery/selftest.py` ("a recovery mint behind the destructive gate") and `scripts/bdtools/recovery/selftest.py` ("a recovery mint with no CSRF check") | PASS |
| QC-02 | There is no role, so the answer is **audit**: every grant and every served range writes an action-log row, and the grant is single-use, short-lived and bound to the minting session | `apps/common/webhost:TestAMintIsAuditedOnceByItsOwnRowAndNotAgainByTheGenericRecorder` `core/service:TestRefusedRecoveriesAreAuditedToo` `core/internal/recovery/export:TestGrantRefusesAnotherSessionAndAnotherActor` `core/internal/recovery/export:TestGrantDiesWithItsSession` | `scripts/bdtools/recovery/selftest.py` ("a mint that writes no action-log row") and `scripts/bdtools/recovery/selftest.py` ("a served range that writes no action-log row") and `scripts/bdtools/recovery/selftest.py` ("a grant that can be used again after it was spent") and `scripts/bdtools/recovery/selftest.py` ("a grant redeemable from another session") | PASS |
| QC-03 | An **existence** check is never recorded or rendered as content verification, and the three claims have three homes | `core/internal/state:TestRecordCopyScrub_AnExistencePassCannotMoveTheContentAge` `core/internal/revalidate:TestAHeadIsNeverRecordedAsContentVerification` `core/internal/revalidate:TestRevalidationOfAMediumPlacementIsExistenceAndSaysSo` `ui/shared/e2e/985-claims.spec.ts` | `scripts/bdtools/assurance/selftest.py` ("an existence check recorded as content verification") and `scripts/bdtools/assurance/selftest.py` ("a failed scrub that leaves the content claim standing") | PASS |
| QC-04 | A scrub reads its work list from the **journal** (the placement rows) and writes its verdict there; a missing object the journal expects is a failure, never a silent skip | `core/internal/revalidate:TestAMissingObjectOnAMediumQuarantines` `core/internal/revalidate:TestEveryCopyGoneIsAQuarantine` `core/internal/revalidate:TestDueness_ARecordWithNoCopyRowIsDueAndGetsOneAdopted` | `scripts/bdtools/assurance/selftest.py` ("a scrub work list that leaves out every copy on a storage medium") | PASS |
| QC-05 | **Due**-ness is a property of the copy, not of the artifact row's clock, and a rebuilt or upgraded catalog reads unknown, which is due | `core/internal/revalidate:TestDueness_RewritingTheArtifactRowsClockChangesNothingAboutWhatIsDue` `core/internal/revalidate:TestDueness_ACopyNobodyScheduledIsDueAndSortsFirst` `core/internal/state:TestMigration0016_AnUpgradedCopyIsUnknownAndDueNotFresh` | `scripts/bdtools/assurance/selftest.py` ("a copy nobody has scheduled read as not due") | PASS |
| QC-06 | Nothing is green because nobody looked: `unknown` is the zero value, and never configured, never run, overdue, refused for cost and could-not-complete are five values rendered as themselves | `core/internal/state:TestMigration0016_TheZeroValueCannotLookGood` `core/internal/recoverytest:TestTheFiveNotAPassReasonsAreFiveValues` `ui/shared/e2e/985-claims.spec.ts` | by the author of this change on 2026-10-11: planted the break in `ui/shared/src/assurance/claims.ts` (see the log at the end of this file) and watched `985-claims.spec.ts` go red | BY HAND |
| QC-07 | **Reachability** is never a recovery-test pass: the bytes are recovered, re-read, digest-matched, and only then does the validator run, and a validator that exits 0 having read nothing produces no pass | `core/internal/recoverytest:TestADigestMismatchIsAFailureAndTheValidatorNeverRuns` `core/internal/recoverytest:TestAValidatorThatExitsZeroWithoutReadingDoesNotProduceAPassOverWrongBytes` `core/internal/state:TestRecordRecoveryTest_APassWithoutADigestMatchIsRefused` `core/internal/recoverytest:TestAnUnconfirmedTerminationIsNeverAPass` | `scripts/bdtools/assurance/selftest.py` ("recovered bytes that match nothing handed to the validator") and `scripts/bdtools/assurance/selftest.py` ("a recovery test stored as a pass without the digest having matched") and `scripts/bdtools/assurance/selftest.py` ("a validator that never ended on its own counted as a pass") | PASS |
| QC-08 | Money and bandwidth are refusals at the moment of spend, not prose: a **budget** per concurrent read, per destination, per pass, per validator output and per download, and no unattended pass reaches a content read | `core/internal/recovery/budget:TestAGateRefusesPastItsLineNamingTheBudgetAndFreesOnRelease` `core/internal/recovery/budget:TestOneThousandDueCopiesCompleteInsideThePerPassBoundWithTheCeilingsNeverExceeded` `core/internal/recoverytest:TestAScheduledTestOfAMediumCopyIsRefusedForCostAndNothingIsRead` | `scripts/bdtools/recovery/selftest.py` ("a read budget that never refuses") and `scripts/bdtools/recovery/selftest.py` ("a per-destination budget that is not applied") and `scripts/bdtools/recovery/selftest.py` ("a scheduler pass with no bound on the copies it considers") and `scripts/bdtools/recovery/selftest.py` ("an unattended pass allowed to reach a content read") and `scripts/bdtools/recovery/selftest.py` ("a download ceiling that is not enforced") | PASS |
| QC-09 | The **per-set** answer is written per knob, including where it is no: scrub cadence, strategy, recovery-test mode and validator are per set; staging root, TTL, concurrency and the download memory budget are global only | `core/internal/config:TestAssurance_PerSetModesEachResolveThroughValidate` `core/cmd/retnd:TestBackupSetAssurance_PatchResetAndTheGlobalOnlyRefusal` `core/service:TestAssuranceSettings_ReportTheResolvedDefaultsAndTheWrittenNoPerHostKnob` | `scripts/bdtools/assurance/selftest.py` ("a per-set override that forgets the deployment's policy") | PASS |
| QC-10 | A global change moves a set that **inherits**, through the route and not in a unit test of the resolver, and leaves a pinned set alone | `apps/common/webhost:TestAssuranceRoute_AGlobalPatchMovesAnInheritingSetAndLeavesAPinnedOneAlone` `core/service:TestUpdateAssuranceSettings_MovesAnInheritingSetAndNotAPinnedOne` | `scripts/bdtools/assurance/selftest.py` ("a global assurance change written nowhere") | PASS |
| QC-11 | One assurance vocabulary for both engines: a **kopia** set gets no second drill, its achieved level is read as the recovery-test claim, and a recover on it points at `snapshot restore` | `core/internal/recoverytest:TestAKopiaSetsClaimIsReadFromItsAchievedLevelAndRunsNothing` `core/service:TestAnIncrementalEngineSetRefusesAndNamesSnapshotRestore` | `scripts/bdtools/assurance/selftest.py` ("a kopia set whose weaker verification level is read as a recovery test") and `scripts/bdtools/recovery/selftest.py` ("a kopia backup set recovered as if it held files") | PASS |
| QC-12 | Validator output is **untrusted** terminal input: bounded per run to EPIC L's bound, no control byte reaches a structural field or the docked terminal, and a forged truncation marker is told from the genuine one by kind, on EPIC L's own CR-08 corpus | `core/internal/recoverytest:TestNoEntryOfEpicLsHostileCorpusSurvivesAsAControlByteInAValidatorRecord` `core/internal/recoverytest:TestAForgedTruncationMarkerIsDistinguishableFromTheGenuineOneOnlyByKind` `core/internal/recoverytest:TestHostileOutputLeavesAsSanitisedRecordsWithTheTruncationFlagged` `ui/shared/e2e/985-reconnect-and-log.spec.ts` | `scripts/bdtools/assurance/selftest.py` ("validator output reaching a record with its control bytes intact") and `scripts/bdtools/recovery/selftest.py` ("a validator output bound that is no longer EPIC L's") | PASS |
| QC-13 | A crash with a **staging** directory present is swept and reconciled at start, and a cleanup failure is visible separately from a recovery failure | `core/internal/app:TestRecoveryTestTick_SweepsAStagingDirectoryACrashLeftBehind` `core/service:TestStartupReconcileRemovesAKilledProcessesPartialsAndReportsThem` `core/internal/recoverytest:TestACleanupFailureIsItsOwnStateAndChangesNeitherVerdict` | `scripts/bdtools/assurance/selftest.py` ("staging a crashed recovery test left behind never swept") and `scripts/bdtools/recovery/selftest.py` ("a failed recovery that leaves its partial file behind") | PASS |
| QC-14 | A scrub is one durable event per pass with counts, plus one per failure up to a cap, so the feed **volume** over a thousand due copies is two rows, stated with the number and asserted against a 1000-copy fixture | `core/service:TestScrubFeed_AThousandHealthyCopiesCostTwoRows` `core/service:TestScrubFeed_AThousandFailingCopiesAreCappedAndSayTheyWere` `core/internal/app:TestScrub_AThousandDueCopiesAcrossFiftySetsIsOnePassRowAndNoFeedRows` | `scripts/bdtools/assurance/selftest.py` ("a scrub pass that writes a feed row for every failing copy") | PASS |
| QC-15 | The no-download promise is reversed as one **boundary** sentence in every place it was made, found by what the claim was split into, and the old words are gone | `core/tests/assurancegate:TestTheBoundarySentenceIsEverywhereTheOldPromiseWasMadeAndThePromiseIsGone` `core/tests/assurancegate:TestTheBoundaryCheckCanFail` `docs/adr/0029-the-recovery-boundary.md` | `scripts/bdtools/assurance/selftest.py` ("the no-download promise made again in the recovery document") | PASS |
| QC-16 | Q works with M and N absent and takes no verdict from either: bytes that decrypt to the wrong **encryption** digest are not a pass, and a lock or ciphertext existing is not evidence | `core/internal/recoverytest:TestBytesThatDecryptToTheWrongDigestAreNotAPass` `core/internal/recoverytest:TestAScheduledTestOfAMediumCopyIsRefusedForCostAndNothingIsRead` | `scripts/bdtools/assurance/selftest.py` ("recovered bytes that match nothing handed to the validator") | PASS |
| QC-17 | An unreadable copy never looks assured: an archived object is its own **requires_restore** state, nothing is read, no thaw is started without the acknowledgement, and it is never counted towards a satisfied policy | `core/internal/revalidate:TestAnArchiveClassCopyIsRecordedRequiresRestoreAndNothingIsRead` `core/internal/state:TestRecordCopyScrub_RequiresRestoreIsItsOwnStateAndKeepsTheClassItAchieved` `core/internal/recovery/export:TestResolveArchiveOnlyIsRefusedForCostAndSaysSo` | `scripts/bdtools/recovery/selftest.py` ("an archive copy thawed without the cost acknowledgement") and `scripts/bdtools/recovery/selftest.py` ("an archive copy the provider already restored still read as needing a restore") | PASS |
| QC-18 | The new **verb** vocabulary is `recover` and `scrub`, never `restore`; every route names its CLI equivalent or the gap, no gap names a verb the binary does not ship, and the refusal is the same on both routes | `apps/common/webhost:TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap` `core/cmd/retnd:TestNoGapClaimsAVerbThisBinaryShips` `core/cmd/retnd:TestARefusalIsTheSameOnBothRoutes` `distribution/packaging:TestTheSiteReferenceDocumentsExactlyTheRegisteredCommands` | by the author of this change on 2026-10-11: planted a route with no CLI equivalent and a gap that names a verb the binary does not ship, and watched both gates go red (see the log at the end of this file) | BY HAND |
| QC-19 | A recovery a **browser** cannot complete is no recovery on an appliance: mint, stream, resume-safe progress, cancel and a visible failure all work through the UI proxy, over plain http, with a download slower than the server's write timeout | `apps/common/webhost:TestADownloadSlowerThanTheServersWriteTimeoutStillCompletes` `apps/common/webhost/serve:TestUI_ARecoveryIsNotCutByTheUIHostsWholeResponseWriteTimeout` `ui/shared/e2e/985-recover.spec.ts` | `scripts/bdtools/recovery/selftest.py` ("a recovery download cut by the UI proxy's header timeout") and `scripts/bdtools/recovery/selftest.py` ("a recovery download cut by the UI host's whole-response write timeout") | PASS |

---

## The release gate, line by line

Every line of #988's release gate, and the script or test that proves it. Nothing here is asserted by
this table: each line is a command that runs.

| Gate line | What proves it |
|---|---|
| Deterministic recapture: the same script twice gives byte-identical output | `node docs/site/tools/capture-recovery-assurance.mjs --check`, wired into CI as the `recovery-captures` job |
| Site-wide dead-fragment check and the docs link checks | `core/tests/assurancegate:TestEveryLinkInTheSiteAndTheDocsResolves` (every `href`, `src` and Markdown link under `docs/` and the README, ids checked in the HTML pages), with `scripts/bdtools/assurance/selftest.py` ("a site anchor renamed under the links that point at it") |
| Suites A, B and C green at the new pin | `scripts/e2e/run-tests-repo-gate.sh`; the pin moves with #986, see that pull request for what is blocked on the tests repository |
| Parity gates | `apps/common/webhost:TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap`, `core/cmd/retnd:TestNoGapClaimsAVerbThisBinaryShips`, `distribution/packaging/site_reference_test.go`, `core/cmd/retnd/routeparity_test.go`, `scripts/api/check-contract-drift.sh` |
| Large-download memory budget proven with a number | `apps/generic/tests/recoveryperf` run with `RECOVERY_PERF_BYTES=4294967296`; the numbers and the host are in `docs/perf/epic-q-recovery.md` |
| No validator output can drive a browser terminal action, on EPIC L's CR-08 corpus | QC-12 above: the corpus is read out of `core/internal/workflowrun/terminal_test.go` itself, so there is one corpus |
| The container security contract unchanged, and `container/compose.yaml` plus the packaging manifests byte-identical | `core/tests/deploymentdiff` (the digests are in `core/tests/deploymentdiff/testdata/digests.txt`) and `scripts/bdtools/recovery/selftest.py` ("the shipped container quietly made privileged") |
| No regression for a deployment with assurance at its default and no validator | `core/tests/compat` (the FR-35 cells) and `core/internal/recoverytest:TestASetWithNoValidatorIsNotConfiguredNotAPassAndNotAFailure` |
| Any new CI job is in the release gate's `needs` list | `scripts/tests/release-gate-covers-every-job.test.sh` |
| `scripts/ci-local.sh` green | run, with the result in the pull request |

---

## Log of the hand checks

Written so a reader can tell who did what and when, and so a later hand check adds a line rather than
rewriting this one. Both were run on 2026-10-11 by the author of this change, in throw-away
containers, against a copy of the tree.

- **QC-06.** In `ui/shared/src/assurance/claims.ts`, `if (!record) return make("unknown", false);`
  was changed to return `"pass"`, so a backup with no assurance record at all rendered as a pass.
  `ui/shared/e2e/985-claims.spec.ts` ran green before the change (11 passed) and red after it (1
  failed, 10 passed: "an assurance API that is absent renders unknown, never a tick", on
  `toHaveCount`). No self-test plants this one because the claim lives in TypeScript, and the
  self-tests plant Go.
- **QC-18.** In `core/cliecho/routes.go`, first the `GET /recoveries/{grant}` entry was deleted, so
  a route existed with neither a CLI equivalent nor a declared gap, and
  `apps/common/webhost:TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap` went red. Then the entry was
  restored and `namesShippedVerbs: []string{"recover"}` was removed from it, so a gap sentence named a
  verb the binary ships without declaring it, and `core/cmd/retnd:TestNoGapClaimsAVerbThisBinaryShips`
  went red. Both were green before.
