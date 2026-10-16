#!/usr/bin/env python3
"""Positive controls for EPIC Q's assurance guards (#988).

The three claims (Stored, Content verified, Recovery tested) stay three
because a handful of small guards hold them apart, and each of those is the
kind that passes on the tree it shipped with and could not fail: a class
comparison that never reads the class, a digest compare that always matches,
a due check that treats a missing schedule as fresh. Their tests going green
is no evidence they can go red.

So every guard is mutation-tested here against the real tree, the way
`scripts/bdtools/recovery/selftest.py` does it for the recovery route: a copy
of the working tree gets one deliberate violation planted in a real product
file, a narrowed slice of the suite runs, and it must fail AND name the test
that holds the promise. Naming the test, not merely failing, is what stops a
mutant that broke the build for an unrelated reason from reading as a catch.

Every mutation is anchored to a verbatim copy of product source, tabs and
all. An anchor that no longer matches is STALE ANCHOR: the control is skipped
rather than run against a tree with nothing planted in it, and the run
carries on so one run names every stale control (#458).
`--check-anchors` checks every anchor against the real tree, building
nothing.

A few minutes: each mutant rebuilds and runs a narrowed slice of one package.
No Docker, no network.
"""

from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness, selftest_swap

PROGRAM = "assurance selftest"

SCRUB_GO = "core/internal/state/scrub.go"
RTSTATE_GO = "core/internal/state/recoverytest.go"
RTRUN_GO = "core/internal/recoverytest/recoverytest.go"
OUTPUT_GO = "core/internal/recoverytest/output.go"
CONFIG_GO = "core/internal/config/assurance.go"
SERVICE_GO = "core/service/assurance.go"
APP_SCRUB_GO = "core/internal/app/scrub.go"
TICK_GO = "core/internal/app/recoverytest.go"
SELECT_GO = "core/internal/revalidate/select.go"
CLAIM_GO = "core/internal/recoverytest/claim.go"
RECOVERY_MD = "docs/recovery.md"
REFERENCE_HTML = "docs/site/reference.html"
CHECKS_GO = "core/internal/revalidate/checks.go"
CONFIG_LOAD_GO = "core/internal/config/config.go"
CLI_ASSURANCE_GO = "core/cmd/retnd/assurance.go"
MIGRATION_SQL = "core/migrations/0016_copy_assurance.sql"

STATE_PKG = ("core", ("./internal/state/",))
RTRUN_PKG = ("core", ("./internal/recoverytest/",))
CONFIG_PKG = ("core", ("./internal/config/",))
SERVICE_PKG = ("core", ("./service/",))
APP_PKG = ("core", ("./internal/app/",))
REVAL_PKG = ("core", ("./internal/revalidate/",))
GATE_PKG = ("core", ("./tests/assurancegate/",))
CLI_PKG = ("core", ("./cmd/retnd/",))

PLANT = "// PLANTED VIOLATION (scripts/bdtools/assurance/selftest.py): "


def gate(root: Path, where: tuple[str, tuple[str, ...]], pattern: str) -> tuple[int, str]:
    """Run a narrowed slice in `root`, never raising on its own non-zero
    status: an expected failure has to become a value this file inspects."""
    module, packages = where
    env = dict(os.environ)
    env["GOWORK"] = "off"
    proc = harness.sh(
        ["go", "test", "-count=1", "-timeout", "10m", "-run", pattern, *packages],
        check=False,
        capture=True,
        cwd=root / module,
        env=env,
    )
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def expect_fails(tracker, tally, label, directory, needle, where, pattern) -> None:
    if tracker.stale_verdict(label):
        return
    if tracker.anchors_only(label):
        return
    code, out = gate(directory, where, pattern)
    if code == 0:
        tally.bad(f"{label}. The suite PASSED against a planted violation.", out)
        return
    if needle not in out:
        tally.bad(
            f"{label}. The suite failed, but never named the promise that was broken.",
            f"expected its output to mention: {needle}\n{out}",
        )
        return
    matched = next((line.strip()[:300] for line in out.splitlines() if needle in line), "")
    tally.ok(f"(caught): {label}", matched)


def expect_passes(tracker, tally, label, directory, where, pattern) -> None:
    if tracker.stale_verdict(label):
        return
    if tracker.anchors_only(label):
        return
    code, out = gate(directory, where, pattern)
    if code == 0:
        tally.ok(f"(clean): {label}")
    else:
        tally.bad(f"{label}. The suite FAILED against an unmutated tree, so its failures mean nothing.", out)


# Each entry: label, file, old, new, needle, where, pattern
MUTATIONS = [
    (
        "an existence check recorded as content verification",
        SCRUB_GO,
        "\t\tif c.Class == VerificationContent {\n\t\t\tcontentAt = at\n\t\t}",
        "\t\t" + PLANT + "any achieved class sets the content age.\n\t\tif c.Class != \"\" {\n\t\t\tcontentAt = at\n\t\t}",
        "--- FAIL: TestRecordCopyScrub_AnExistencePassCannotMoveTheContentAge",
        STATE_PKG,
        "TestRecordCopyScrub_AnExistencePass",
    ),
    (
        "a failed scrub that leaves the content claim standing",
        SCRUB_GO,
        "\t\t\t\tconsecutive_failures = consecutive_failures + 1,\n\t\t\t\tcontent_verified_at = NULL,\n\t\t\t\tlast_bytes_read = ?, last_duration_ms = ?,",
        "\t\t\t\tconsecutive_failures = consecutive_failures + 1,\n\t\t\t\tlast_bytes_read = ?, last_duration_ms = ?,",
        "--- FAIL: TestRecordCopyScrub_AFailureClearsTheClaimItContradicts",
        STATE_PKG,
        "TestRecordCopyScrub_AFailureClearsTheClaimItContradicts",
    ),
    (
        "a copy nobody has scheduled read as not due",
        SCRUB_GO,
        "\tif a.NextDueAt == nil {\n\t\treturn time.Time{}, true\n\t}\n\tat := *a.NextDueAt",
        "\tif a.NextDueAt == nil {\n\t\t" + PLANT + "no schedule means nothing is owed.\n\t\treturn time.Time{}, false\n\t}\n\tat := *a.NextDueAt",
        "--- FAIL: TestDueness_ACopyNobodyScheduledIsDueAndSortsFirst",
        REVAL_PKG,
        "TestDueness_ACopyNobodyScheduledIsDueAndSortsFirst",
    ),
    (
        "a recovery test stored as a pass without the digest having matched",
        RTSTATE_GO,
        "\t\tcase r.DigestMatched == nil || !*r.DigestMatched:\n\t\t\treturn errors.New(\"state: a recovery test passes only",
        "\t\t" + PLANT + "a pass needs no digest match.\n\t\tcase false:\n\t\t\treturn errors.New(\"state: a recovery test passes only",
        "--- FAIL: TestRecordRecoveryTest_APassWithoutADigestMatchIsRefused",
        STATE_PKG,
        "TestRecordRecoveryTest_APassWithoutADigestMatchIsRefused",
    ),
    (
        "a validator that never ended on its own counted as a pass",
        RTSTATE_GO,
        "\t\tcase r.Termination != RecoveryTerminationExited:\n\t\t\treturn fmt.Errorf(\"state: a validator that did not end on its own",
        "\t\t" + PLANT + "any termination is a verdict.\n\t\tcase false:\n\t\t\treturn fmt.Errorf(\"state: a validator that did not end on its own",
        "--- FAIL: TestRecordRecoveryTest_APassWithoutADigestMatchIsRefused/validator_was_killed",
        STATE_PKG,
        "TestRecordRecoveryTest_APassWithoutADigestMatchIsRefused",
    ),
    (
        "recovered bytes that match nothing handed to the validator",
        RTRUN_GO,
        "\tmatched := digestMatches(sum, expected)",
        "\t" + PLANT + "the digest gate always opens.\n\tmatched := true\n\t_ = digestMatches",
        "--- FAIL: TestADigestMismatchIsAFailureAndTheValidatorNeverRuns",
        RTRUN_PKG,
        "TestADigestMismatchIsAFailureAndTheValidatorNeverRuns|TestAValidatorThatExitsZeroWithoutReadingDoesNotProduceAPassOverWrongBytes|TestBytesThatDecryptToTheWrongDigestAreNotAPass",
    ),
    (
        "validator output reaching a record with its control bytes intact",
        OUTPUT_GO,
        "out = append(out, OutputRecord{Kind: OutputText, Text: sanitizeText(line)})",
        "out = append(out, OutputRecord{Kind: OutputText, Text: line}) " + PLANT + "output is not sanitised.",
        "--- FAIL: TestHostileOutputLeavesAsSanitisedRecordsWithTheTruncationFlagged",
        RTRUN_PKG,
        "TestHostileOutputLeavesAsSanitisedRecordsWithTheTruncationFlagged",
    ),
    (
        "a per-set override that forgets the deployment's policy",
        CONFIG_GO,
        "\tout := global\n\tif o == nil {\n\t\treturn out\n\t}",
        "\t" + PLANT + "an override starts from nothing.\n\tout := global\n\tif o == nil {\n\t\treturn out\n\t}\n\tout = Assurance{}",
        "--- FAIL: TestAssurance_AScrubOverrideInheritsEveryFieldItDoesNotName",
        CONFIG_PKG,
        "TestAssurance_AScrubOverrideInheritsEveryFieldItDoesNotName",
    ),
    (
        "a global assurance change written nowhere",
        SERVICE_GO,
        "\tif err := b.persistConfig(cfg); err != nil {\n\t\treturn AssuranceSettings{}, err\n\t}\n\treturn b.withRecoveryCeilings(assuranceSettingsOf(b.state.Load().inner.Config)), nil\n}",
        "\t" + PLANT + "the patch is never persisted or reloaded.\n\tif err := error(nil); err != nil {\n\t\treturn AssuranceSettings{}, err\n\t}\n\treturn b.withRecoveryCeilings(assuranceSettingsOf(b.state.Load().inner.Config)), nil\n}",
        "--- FAIL: TestUpdateAssuranceSettings_MovesAnInheritingSetAndNotAPinnedOne",
        SERVICE_PKG,
        "TestUpdateAssuranceSettings_MovesAnInheritingSetAndNotAPinnedOne",
    ),
    (
        "a scrub pass that writes a pass row for every set",
        APP_SCRUB_GO,
        "\t\ts.tally(ctx, &sum, bs.ID, report)\n\t\tif runErr != nil && ctx.Err() != nil {",
        "\t\ts.tally(ctx, &sum, bs.ID, report)\n\t\t" + PLANT + "a pass row per set, not one per pass.\n\t\t_ = s.finishScrub(ctx, journal, sum, started)\n\t\tif runErr != nil && ctx.Err() != nil {",
        "--- FAIL: TestScrub_AThousandDueCopiesAcrossFiftySetsIsOnePassRowAndNoFeedRows",
        APP_PKG,
        "TestScrub_AThousandDueCopiesAcrossFiftySetsIsOnePassRowAndNoFeedRows",
    ),
    (
        "staging a crashed recovery test left behind never swept",
        TICK_GO,
        "\t\t\ts.sweepRecoveryStaging(ctx, bs)\n",
        "\t\t\t" + PLANT + "the sweep never runs.\n\t\t\t_ = bs\n",
        "--- FAIL: TestRecoveryTestTick_SweepsAStagingDirectoryACrashLeftBehind",
        APP_PKG,
        "TestRecoveryTestTick_SweepsAStagingDirectoryACrashLeftBehind",
    ),
    (
        "a scrub work list that leaves out every copy on a storage medium",
        SELECT_GO,
        "\treturn rec.ActiveMediumPlacements()\n}",
        "\t" + PLANT + "the journal's medium copies are never a work list.\n\treturn nil\n}",
        "--- FAIL: TestAMissingObjectOnAMediumQuarantines",
        REVAL_PKG,
        "TestAMissingObjectOnAMediumQuarantines|TestEveryCopyGoneIsAQuarantine",
    ),
    (
        "a kopia set whose weaker verification level is read as a recovery test",
        CLAIM_GO,
        "r.VerificationLevelAchieved != string(model.LevelRestoreDrill) ||",
        "r.VerificationLevelAchieved != string(model.LevelRestoreDrill) && false || " + PLANT + "any achieved level is a drill.\n\t\t\t",
        "--- FAIL: TestAKopiaSetsClaimIsReadFromItsAchievedLevelAndRunsNothing",
        RTRUN_PKG,
        "TestAKopiaSetsClaimIsReadFromItsAchievedLevelAndRunsNothing",
    ),
    (
        "the no-download promise made again in the recovery document",
        RECOVERY_MD,
        "**This product recovers and exports the retained artifact; restoring it into an\napplication remains the operator's responsibility.**",
        "**Restoring an artifact is not an automated command; restoring it into an\napplication remains the operator's responsibility.**",
        "--- FAIL: TestTheBoundarySentenceIsEverywhereTheOldPromiseWasMadeAndThePromiseIsGone",
        GATE_PKG,
        "TestTheBoundarySentenceIsEverywhereTheOldPromiseWasMadeAndThePromiseIsGone",
    ),
    (
        "a site anchor renamed under the links that point at it",
        REFERENCE_HTML,
        '<h3 id="web-backup-detail">Backup detail</h3>',
        '<h3 id="web-backup-detail-renamed">Backup detail</h3>',
        "--- FAIL: TestEveryLinkInTheSiteAndTheDocsResolves",
        GATE_PKG,
        "TestEveryLinkInTheSiteAndTheDocsResolves",
    ),
    (
        "a scrub lease that a second scrubber walks straight through",
        SCRUB_GO,
        "\t\tif until.After(now) && owner != holder {",
        "\t\t" + PLANT + "a held lease is ignored.\n\t\tif until.After(now) && owner != holder && false {",
        "--- FAIL: TestClaimCopyScrub_OnlyOneHolderAtATime",
        STATE_PKG,
        "TestClaimCopyScrub_OnlyOneHolderAtATime",
    ),
    (
        "due-ness read from the artifact row's clock again",
        SELECT_GO,
        "\t\tat, ok := p.DueUnder(interval, now)\n\t\tif !ok {\n\t\t\tcontinue\n\t\t}",
        "\t\t" + PLANT + "the artifact row's clock decides.\n\t\t_ = p\n\t\tat, ok := rec.UpdatedAt.Add(interval), now.Sub(rec.UpdatedAt) >= interval\n\t\tif !ok {\n\t\t\tcontinue\n\t\t}",
        "--- FAIL: TestDueness_RewritingTheArtifactRowsClockChangesNothingAboutWhatIsDue",
        REVAL_PKG,
        "TestDueness_RewritingTheArtifactRowsClockChangesNothingAboutWhatIsDue",
    ),
    (
        "a failed validator that clears the content claim",
        RTSTATE_GO,
        "recovery_cleanup_state = ?, recovery_bytes_read = ?, recovery_duration_ms = ?\n\t\t\t WHERE placement_id = ?`,\n\t\t\tr.State, at, next, reason,",
        "recovery_cleanup_state = ?, recovery_bytes_read = ?, recovery_duration_ms = ?,\n\t\t\t\tcontent_verified_at = NULL\n\t\t\t WHERE placement_id = ?`,\n\t\t\tr.State, at, next, reason,",
        "--- FAIL: TestRecordRecoveryTest_AFailedValidatorLeavesTheContentClaimStanding",
        STATE_PKG,
        "TestRecordRecoveryTest_AFailedValidatorLeavesTheContentClaimStanding",
    ),
    (
        "a cleanup failure that overwrites the recovery verdict",
        RTSTATE_GO,
        "`UPDATE placement_assurance SET recovery_cleanup_state = ? WHERE placement_id = ?`",
        "`UPDATE placement_assurance SET recovery_cleanup_state = ?, recovery_state = 'failed' WHERE placement_id = ?`",
        "--- FAIL: TestRecordRecoveryCleanup_ALaterSweepCanSettleCleanupWithoutTouchingTheVerdict",
        STATE_PKG,
        "TestRecordRecoveryCleanup_ALaterSweepCanSettleCleanupWithoutTouchingTheVerdict",
    ),
    (
        "a config that takes an unknown assurance key",
        CONFIG_LOAD_GO,
        "\tdec.KnownFields(true)",
        "\tdec.KnownFields(false) " + PLANT + "an unknown key is accepted.",
        "--- FAIL: TestAssurance_UnknownKeyIsRefusedByLoad",
        CONFIG_PKG,
        "TestAssurance_UnknownKeyIsRefusedByLoad",
    ),
    (
        "an archive-tier copy recorded as a plain pass",
        CHECKS_GO,
        "\t\tcase archived:\n\t\t\t// The object is there, at the right size",
        "\t\tcase archived && false:\n\t\t\t// The object is there, at the right size",
        "--- FAIL: TestAnArchiveClassCopyIsRecordedRequiresRestoreAndNothingIsRead",
        REVAL_PKG,
        "TestAnArchiveClassCopyIsRecordedRequiresRestoreAndNothingIsRead",
    ),
    (
        "a host-scoped knob accepted under a backup set",
        CLI_ASSURANCE_GO,
        '"config", "json", "reset")...); len(named) > 0 {',
        '"config", "json", "reset")...); len(named) < 0 { ' + "// PLANTED VIOLATION: the global-only refusal never fires.",
        "--- FAIL: TestBackupSetAssurance_PatchResetAndTheGlobalOnlyRefusal",
        CLI_PKG,
        "TestBackupSetAssurance_PatchResetAndTheGlobalOnlyRefusal",
    ),
    (
        "a fresh copy that defaults to a passed scrub",
        MIGRATION_SQL,
        "    assurance_state TEXT NOT NULL DEFAULT 'unknown'\n",
        "    assurance_state TEXT NOT NULL DEFAULT 'passed'\n",
        "--- FAIL: TestMigration0016_TheZeroValueCannotLookGood",
        STATE_PKG,
        "TestMigration0016_TheZeroValueCannotLookGood",
    ),
    (
        "two of the five not-a-pass reasons collapsed into one",
        CLAIM_GO,
        "\tcase c.State == \"\" || c.State == state.RecoveryUnknown || c.State == state.RecoveryNotConfigured:",
        "\tcase c.State == state.RecoveryRefusedForCost: // " + "PLANTED VIOLATION (scripts/bdtools/assurance/selftest.py): a refusal for cost reads as could not complete.\n\t\tc.State = state.RecoveryCouldNotComplete\n\tcase c.State == \"\" || c.State == state.RecoveryUnknown || c.State == state.RecoveryNotConfigured:",
        "--- FAIL: TestTheFiveNotAPassReasonsAreFiveValues",
        RTRUN_PKG,
        "TestTheFiveNotAPassReasonsAreFiveValues",
    ),
]

CLEAN_CONTROLS = [
    ("the per-copy record guards", STATE_PKG, "TestRecordCopyScrub_|TestRecordRecoveryTest_"),
    ("the due-ness and archive guards", REVAL_PKG, "TestDueness_|TestAnArchiveClassCopyIsRecordedRequiresRestoreAndNothingIsRead"),
    ("the lease, migration and recovery-record guards", STATE_PKG, "TestClaimCopyScrub_OnlyOneHolderAtATime|TestMigration0016_TheZeroValueCannotLookGood|TestRecordRecoveryCleanup_|TestRecordRecoveryTest_AFailedValidator"),
    ("the five-reasons guard", RTRUN_PKG, "TestTheFiveNotAPassReasonsAreFiveValues"),
    ("the unknown-key guard", CONFIG_PKG, "TestAssurance_UnknownKeyIsRefusedByLoad"),
    ("the global-only refusal guard", CLI_PKG, "TestBackupSetAssurance_PatchResetAndTheGlobalOnlyRefusal"),
    ("the recovery-test runner guards", RTRUN_PKG, "TestADigestMismatch|TestAValidatorThatExits|TestBytesThatDecrypt|TestHostileOutput"),
    ("the per-set resolution guard", CONFIG_PKG, "TestAssurance_AScrubOverrideInheritsEveryFieldItDoesNotName"),
    ("the assurance settings and feed guards", SERVICE_PKG, "TestUpdateAssuranceSettings_|TestScrubFeed_"),
    ("the boundary and link guards", GATE_PKG, "TestTheBoundary|TestEveryLink"),
    ("the staging sweep guard", APP_PKG, "TestRecoveryTestTick_SweepsAStagingDirectoryACrashLeftBehind"),
]


def body(root: Path, dry_run: bool) -> int:
    tally = selftest_swap.Tally()

    with tempfile.TemporaryDirectory(prefix="retnd-assurance-selftest.") as tmp_name:
        tmp = Path(tmp_name)
        tracker = selftest_swap.AnchorTracker(dry_run=dry_run, root=root, tmp=tmp)

        print("==> negative controls: each slice is clean on the real tree")
        for label, where, pattern in CLEAN_CONTROLS:
            expect_passes(tracker, tally, f"{label} on an unmutated tree", root, where, pattern)

        for i, entry in enumerate(MUTATIONS):
            label, rel, old, new, needle, where, pattern = entry[:7]
            print()
            print(f"==> {label}")
            d = selftest_swap.mutant(root, tmp, f"assurance-{i:02d}", dry_run=dry_run)
            tracker.swap(d / rel, old, new)
            expect_fails(tracker, tally, label, d, needle, where, pattern)

        tracker.stale_summary()
        stale_count = tracker.stale_count
        anchors_checked = tracker.anchors_checked

    total = tally.passed + tally.failed + stale_count
    if tally.failed or stale_count:
        print(file=sys.stderr)
        print(
            f"FAIL: {tally.failed + stale_count} of {total} assurance controls did not behave as required "
            f"({tally.failed} reached the wrong verdict, {stale_count} could not plant their violation at all).",
            file=sys.stderr,
        )
        return harness.EXIT_FAILED

    print()
    if dry_run:
        print(f"OK: all {anchors_checked} assurance mutation anchors still name code that is in this tree.")
    else:
        print(
            f"OK: all {tally.passed} assurance controls behaved as required (every guard was shown to go red "
            "against a real planted violation, and shown not to on the real tree)."
        )
    return harness.EXIT_OK


def main(argv: list[str]) -> int:
    harness.set_program(PROGRAM)
    harness.install_signal_handlers()
    dry_run = selftest_swap.parse_selftest_args(argv, "selftest.py")
    root = harness.repo_root(Path(__file__))
    return harness.finish(lambda: body(root, dry_run))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
