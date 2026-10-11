# EPIC Q: fault injection and budgets (#983)

`docs/conformance/epic-q-matrix.md` is not on `main` yet (it lands with the
EPIC Q specification, #954), so I have not added rows to it. This file is the
other half of the same promise and is written so its rows can be moved into
the matrix unchanged: one row per scenario, each naming its test and the
mutation that has to turn it red. The mutations are not prose. Every one that
lives in `scripts/bdtools/recovery/selftest.py` is planted as a real violation
in a real product file by `scripts/recovery/selftest.sh`, which runs in the
local gate, and the run has to go red naming the promise.

## The scenarios the issue lists

| Scenario | Test | What it asserts is left behind | Falsification (planted by the self-test unless marked) |
|---|---|---|---|
| Corrupt local copy | `TestCorruptLocalCopyFailsBeforeAByteReachesTheTarget` | no byte on standard output, no staging entry, its own code (`RECOVERY_LOCAL_COPY_CORRUPT`) | digest never compared |
| Corrupt remote copy | `TestCorruptRemoteCopyIsDistinguishableFromACorruptLocalOne` | no staging entry, a different code from the local one | digest never compared; local and remote origin conflated |
| Provider checksum mismatch | `TestAProviderChecksumThatDisagreesIsAFindingBeforeAnyByteIsRead` | no read, no billing, a positive refusal | the attestation never consulted |
| Archive copy requiring thaw | `TestArchiveOnlyIsRefusedForCostAndStartsNoProviderJob`, `TestArchiveAcknowledgedStartsExactlyOneProviderJobEvenWhenAskedTwice`, `TestARestoredArchiveCopyIsReadableAndNeedsNoSecondRestore` | zero provider jobs on a refusal, exactly one on yes, none for a copy already restored | archive thaw without the acknowledgement; restored copy still read as needing a restore |
| Cancellation during thaw | `TestCancellationDuringTheThawLeavesNothingAndStartsNoGrant` | no grant, no staging entry | (see below) |
| Cancellation during read | `TestCancellationDuringTheReadLeavesNothingAndNoTarget`, `TestStageCancelledHalfwayLeavesNoStagingEntry` | no staging entry, no target | partial never removed |
| Cancellation during write | `TestCancellationBetweenVerifyingAndPlacingLeavesNoOutputAndNoPartial`, `TestCancellationDuringTheUploadLeavesNoPartialAndNoObject` | no output file, no partial, no object | partial never removed |
| Cancellation during decrypt | not applicable yet | there is no decrypt stage until EPIC N | owned by N, which must use the same harness |
| Cancellation during the validator | not in this change | the runner is #980 | owned by #980 |
| Browser disconnect mid-download | `TestAFailedRangeWritesARowSayingSoAndRemovesStaging`, `TestAClientThatGoesAwayMidDownloadIsReportedAsAFailedRange` | no staging entry, grant spent, a row saying it failed | grant not spent when the client goes away |
| Process crash with staging present | `TestAProcessKilledMidWriteLeavesAPartialTheNextStartReconcilesAndReports`, `TestStartupReconcileRemovesAKilledProcessesPartialsAndReportsThem` | nothing after reconcile, and the reconcile says what it removed | (reconcile is not mutation-planted; see "Not planted" below) |
| Destination disappearing mid-read | `TestADestinationThatDisappearsMidReadFailsCleanlyAndSaysWhy` | no staging entry, the provider's text not in the message | (see below) |
| Validator hangs, double-forks, floods, or emits hostile control sequences | not in this change | the runner is #980; the output bound is a budget here, equal to EPIC L's | owned by #980 |
| Disk fills while staging | `TestStageDiskFullRefusesAndLeavesNothingThatLooksComplete` | no file at all | (engine-level fault double) |
| Two operators, same recovery | `TestTwoOperatorsRequestingTheSameRecoveryProduceOneReadAndOneReceiptEach` | one read of the copy, an output and a receipt per operator | sharing removed |
| Two operators, same recovery test | not in this change | the runner is #980; `export.Flight` is what it should key on | owned by #980 |
| Catalog rebuilt with unknown verification age | `TestResolveRanksAnUnknownVerificationBelowAnyRecordedClassButStillRecoversIt` | recovery still works and still verifies the bytes | (see below) |
| One very large artifact | `apps/generic/tests/recoveryperf` (4 GiB, `docs/perf/epic-q-recovery.md`) | RSS inside the stated ceiling, both processes | buffering the copy: the harness's ceilings |
| 1000+ copies due at once | `TestOneThousandDueCopiesCompleteWithTheReadCeilingsNeverExceeded`, `core/internal/recovery/budget` measurement in `docs/perf/epic-q-recovery.md` | the read concurrency ceilings never exceeded | read ceilings removed |

## The budgets

| Budget | Default | Refusal | Exit code | Test over the line |
|---|---|---|---|---|
| Concurrent full reads | 2 | `RECOVERY_READS_BUSY` | 32 | `TestTheConcurrentReadBudgetRefusesAtTheMomentOfSpendAndReadsNothing` |
| Reads per destination | 1 | `RECOVERY_DESTINATION_BUSY` | 33 | `TestThePerDestinationBudgetIsADifferentRefusalFromTheGlobalOne` |
| Staging disk reserve | 1 GiB | `RECOVERY_BELOW_RESERVE` | 18 | `TestAStagingReserveRefusalHasItsOwnCodeAndReadsNothing` |
| Concurrent downloads | 4 | `RECOVERY_DOWNLOADS_BUSY` | 36 | `TestTheDownloadCeilingRefusesTheNextStreamAndFreesWhenOneFinishes` |

Every number is a property of the host or the process, never of one backup
set. The settings surface for them is #982's; the defaults are
`core/internal/recovery/budget`'s, and the two read ceilings are the same
numbers as #982's operator-facing defaults (2 and 1).

Three budgets this document used to list are gone: work per scheduler pass,
captured validator output and egress per scheduled pass. Nothing in the
product read them (exit codes 34, 35 and 37, unused now), so they were a table
row and a unit test and not a limit. A scheduler pass, a validator run or a
scheduled verification that wants a bound brings its own, in the change that
makes it read one; `TestEveryBudgetIsReadByAProductCodePath` is what stops one
from being declared without a reader.

## Host facts go through the capability contract

The staging reserve and the read and download ceilings are properties of the
host that every provider shares, so a provider says them once through
`capabilities.HostLimits` and not in a provider-specific place. The question is
optional (`capabilities.HostLimitsProvider`) because the contract documents two
ways to implement a provider and a new method on `PlatformAdapter` would break
every one that does it by hand.

| Claim | Test | Falsification |
|---|---|---|
| a platform that says nothing leaves the engine's defaults | `TestAPlatformThatSaysNothingHasNoOpinionAboutHostLimits`, `TestZeroHostLimitsTakeTheEnginesDefaults` | the base adapter given an opinion |
| a platform's limits reach the engine before anything is served, on both start branches | `TestAPlatformsHostLimitsReachTheRecoveryEngineBeforeAnythingIsServed` | the call removed from the start sequence |
| limits that cannot mean anything stop the start and say why | `TestHostLimitsThatCannotMeanAnythingAreRefused`, `TestLimitsThatCannotMeanAnythingStopTheStartAndSayWhy` | validation removed |
| limits are fixed once recovery has started | `TestHostLimitsCannotChangeOnceRecoveryHasStarted` | the started check removed |

## The deployment does not move

`core/tests/deploymentdiff` holds the reference compose file, the seven
per-platform compose files, the two Unraid templates, the TrueNAS catalog and
the four packaging manifests to the digests they had before EPIC Q. A change to
any of them fails the test naming the file, and a change that is meant to alter
the deployment says so by updating `testdata/digests.txt` in the same commit.
Falsification: the self-test makes the shipped container privileged.
