#!/usr/bin/env python3
"""Positive controls for artifact recovery's guards (EPIC Q, #976).

Recovery is a read path that hands out retained backup data, so its
guards are exactly the kind that look fine in a diff and can fail
silently: a cost check that never refuses, a digest compare that always
matches, a grant that can be used twice, a target name that escapes the
staging root. Their tests pass on the tree they ship with, which proves
nothing about whether they could fail.

So every guard is mutation-tested here against the real tree: a copy of
the working tree gets one deliberate violation planted in a real product
file, a narrowed slice of the suite runs, and it must fail AND name the
promise the violation broke. Naming it, not merely failing, is what stops
a mutant that broke the build for an unrelated reason from reading as a
pass. `scripts/bdtools/retention/selftest.py` is the model, and they share
`selftest_swap`'s anchor machinery.

Every mutation is anchored to a verbatim copy of product source, tabs and
all. An anchor that no longer matches is STALE ANCHOR: the control is
skipped rather than run against a tree with nothing planted in it, and the
run carries on so one run names every stale control (#458).
`--check-anchors` checks every anchor against the real tree, building
nothing.

A few minutes: each mutant rebuilds and runs a narrowed slice of one of
three packages. No Docker, no network.
"""

from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness, selftest_swap

PROGRAM = "recovery selftest"

EXPORT = "core/internal/recovery/export"
RESOLVE_GO = f"{EXPORT}/resolve.go"
ENGINE_GO = f"{EXPORT}/engine.go"
GRANT_GO = f"{EXPORT}/grant.go"
STAGE_GO = f"{EXPORT}/stage.go"
RECOVER_GO = "core/service/recover.go"
RECOVER_RUN_GO = "core/service/recover_run.go"
ROUTER_GO = "apps/common/webhost/router.go"
HANDLER_GO = "apps/common/webhost/handlers_recovery.go"
CLI_GO = "core/cmd/retnd/recover.go"

# (module directory under the tree root, package patterns)
EXPORT_PKG = ("core", ("./internal/recovery/export/",))
SERVICE_PKG = ("core", ("./service/",))
WEB_PKG = ("apps/common", ("./webhost/",))
CLI_PKG = ("core", ("./cmd/retnd/",))

PLANT = "// PLANTED VIOLATION (scripts/bdtools/recovery/selftest.py): "


def gate(root: Path, where: tuple[str, tuple[str, ...]], pattern: str) -> tuple[int, str]:
    """Run a narrowed slice in `root`, never raising on its own non-zero
    status: an expected failure has to become a value this file inspects,
    not an exception that unwinds through body() after the first control."""
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
        "a chargeable copy chosen without the cost acknowledgement",
        RESOLVE_GO,
        "\t\tif chargeable(c) && !req.AcknowledgeCost {\n\t\t\tif declined == nil {",
        "\t\t" + PLANT + "a billed read needs no yes.\n\t\tif false {\n\t\t\tif declined == nil {",
        "want ErrCostNotAcknowledged",
        EXPORT_PKG,
        "TestResolveRefusesAChargeableReadWithoutAcknowledgement|TestResolveAFreeCopyIsChosenOver",
    ),
    (
        "an archive copy thawed without the cost acknowledgement",
        RESOLVE_GO,
        "\t\tif c.Access == archive.RequiresRestore {\n\t\t\tif !req.AcknowledgeCost {",
        "\t\tif c.Access == archive.RequiresRestore {\n\t\t\t" + PLANT + "a provider restore needs no yes.\n\t\t\tif false {",
        "want a cost refusal",
        EXPORT_PKG,
        "TestResolveArchiveOnlyIsRefusedForCostAndSaysSo",
    ),
    (
        "a copy whose bytes differ at the same size delivered as good",
        ENGINE_GO,
        "\tif !strings.EqualFold(sum, exp.Hash) {",
        "\t" + PLANT + "the digest is never compared.\n\tif false {",
        "want ErrIntegrity",
        EXPORT_PKG,
        "TestStageBytesFlippedMidFile|TestStageLocalAndRemoteCorruptionAreDistinguishable",
    ),
    (
        "a copy of the wrong length delivered as good",
        ENGINE_GO,
        "\tif exp.Size != nil && n != *exp.Size {",
        "\t" + PLANT + "the length is never compared.\n\tif false {",
        "short copy",
        EXPORT_PKG,
        "TestStageShortAndLongCopiesFail",
    ),
    (
        "a failed recovery that leaves its partial file behind",
        ENGINE_GO,
        "\t\tif rmErr := e.Staging.remove(f.Name()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {",
        "\t\t" + PLANT + "the partial is never removed.\n\t\tif rmErr := error(nil); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {",
        "left staging entries",
        EXPORT_PKG,
        "TestStageBytesFlippedMidFile",
    ),
    (
        "a grant that can be used again after it was spent",
        GRANT_GO,
        "\tif e.closed() {\n\t\treturn Redemption{}, ErrGrantUsed\n\t}",
        "\t" + PLANT + "a spent grant is honoured.\n\tif false {\n\t\treturn Redemption{}, ErrGrantUsed\n\t}",
        "second use after close",
        EXPORT_PKG,
        "TestGrantRedeemsOnceThenIsUsed",
    ),
    (
        "a grant redeemable from another session",
        GRANT_GO,
        "\t\tsubtle.ConstantTimeCompare([]byte(e.session.key()), []byte(caller.key())) != 1 {",
        "\t\t// " + PLANT + "the caller's session is never compared.\n\t\tsubtle.ConstantTimeCompare([]byte(e.session.key()), []byte(e.session.key())) != 1 {",
        "a different session of the same actor",
        EXPORT_PKG,
        "TestGrantRefusesAnotherSessionAndAnotherActor",
    ),
    (
        "a grant that never expires",
        GRANT_GO,
        "\tif !now.Before(e.grant.ExpiresAt) {\n\t\treturn Redemption{}, ErrGrantExpired\n\t}",
        "\t" + PLANT + "an expired grant is honoured.\n\tif false {\n\t\treturn Redemption{}, ErrGrantExpired\n\t}",
        "want ErrGrantExpired",
        EXPORT_PKG,
        "TestGrantExpires",
    ),
    (
        "a grant that outlives the session that minted it",
        GRANT_GO,
        "\tif !s.Expires.IsZero() && s.Expires.Before(exp) {",
        "\t" + PLANT + "the session end is not a bound.\n\tif false {",
        "grant outlives its session",
        EXPORT_PKG,
        "TestGrantNeverExpiresPastItsSession",
    ),
    (
        "a target name that climbs out of the staging root",
        STAGE_GO,
        '\tif clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {',
        "\t" + PLANT + "dot-dot is allowed.\n\tif clean == \".\" {",
        "want ErrTargetOutsideRoot",
        EXPORT_PKG,
        "TestStagingPublishRefusesEscapes",
    ),
    (
        "a target that passes through a symlink",
        STAGE_GO,
        "\t\tif fi.Mode()&fs.ModeSymlink != 0 {\n\t\t\treturn ErrTargetOutsideRoot\n\t\t}",
        "\t\t" + PLANT + "a symlink is followed.\n\t\tif false {\n\t\t\treturn ErrTargetOutsideRoot\n\t\t}",
        "want ErrTargetOutsideRoot",
        EXPORT_PKG,
        "TestStagingPublishRefusesEscapes",
    ),
    (
        "staging that ignores its free-space reserve",
        STAGE_GO,
        "\tif free-need < s.Reserve {",
        "\t" + PLANT + "the reserve is not enforced.\n\tif false {",
        "want ErrBelowReserve",
        EXPORT_PKG,
        "TestStagingRefusesBelowTheReserve|TestStageBelowReserveRefusesBeforeOpeningTheCopy",
    ),
    (
        "a publish of a file the staging area never created",
        STAGE_GO,
        "\tif filepath.Dir(from) != filepath.Clean(s.Root) || !isPartial(filepath.Base(from)) {",
        "\t" + PLANT + "any path may be moved into place.\n\tif false {",
        "Publish must only move files this staging area created",
        EXPORT_PKG,
        "TestStagingPublishRefusesToPublishAForeignFile",
    ),
    (
        "a kopia backup set recovered as if it held files",
        RECOVER_GO,
        "ok && set.Engine == model.EngineKopia {",
        "ok && set.Engine == model.EngineKopia && false { " + PLANT.rstrip(),
        "snapshot restore",
        SERVICE_PKG,
        "TestKopiaSetRefusesAndNamesSnapshotRestore",
    ),
    (
        "a mint that writes no action-log row",
        RECOVER_GO,
        "\tdefer func() { b.auditRecovery(ctx, &ev, err) }()",
        "\t" + PLANT + "the mint is not audited.\n\tdefer func() { _ = ev }()",
        "one mint must write exactly one row",
        SERVICE_PKG,
        "TestEveryMintAndEveryServedRange",
    ),
    (
        "a served range that writes no action-log row",
        RECOVER_RUN_GO,
        "\td.b.auditRecovery(context.Background(), &ev, err)\n",
        "\t" + PLANT + "a served range is not audited.\n\t_ = &ev\n",
        "should write 51 rows",
        SERVICE_PKG,
        "TestEveryMintAndEveryServedRange",
    ),
    (
        "a download whose grant is not spent when the client goes away",
        RECOVER_RUN_GO,
        "\tif err != nil || (d.size == 0 || end >= d.size-1) {",
        "\t" + PLANT + "only a complete download spends the grant.\n\tif d.size == 0 || end >= d.size-1 {",
        "left",
        SERVICE_PKG,
        "TestAFailedRangeWritesARowSayingSo",
    ),
    (
        "a recovery mint behind the destructive gate",
        ROUTER_GO,
        'r.With(requireCSRF).Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        'r.With(requireCSRF, requireDestructiveGate(gate)).Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        "mint = ",
        WEB_PKG,
        "TestARecoveryCompletesWhileTheDestructiveGateIsShut",
    ),
    (
        "a recovery mint with no CSRF check",
        ROUTER_GO,
        'r.With(requireCSRF).Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        'r.Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        "mint without CSRF",
        WEB_PKG,
        "TestMintCarriesCSRFAndRedeemNeedsOnlyTheSession",
    ),
    (
        "several ranges in one request served instead of refused",
        HANDLER_GO,
        '\tif strings.Contains(r.Header.Get("Range"), ",") {',
        "\t" + PLANT + "multipart ranges are served.\n\tif false {",
        "= 200",
        WEB_PKG,
        "TestMultipleRangesInOneRequestAreRefused",
    ),
    (
        "a download cut by the server's whole-response write timeout",
        HANDLER_GO,
        "func (c *recoveryResponseWriter) extendDeadline() {\n",
        "func (c *recoveryResponseWriter) extendDeadline() {\n\treturn // " + PLANT.rstrip() + "\n",
        "download cut after",
        WEB_PKG,
        "TestADownloadSlowerThanTheServersWriteTimeoutStillCompletes",
    ),
    (
        "a mint that is recorded twice",
        HANDLER_GO,
        "\tmarkSelfAudited(r.Context())\n",
        "\t// " + PLANT.rstrip() + "\n",
        "extra row",
        WEB_PKG,
        "TestAMintIsAuditedOnceByItsOwnRow",
    ),
    (
        "a recover verb that takes no explicit target",
        CLI_GO,
        '\tif *to == "" {',
        "\t" + PLANT + "no --to is fine.\n\tif false {",
        "--to is required",
        CLI_PKG,
        "TestRecoverIsRefusedBeforeAnythingIsOpened",
    ),
]

CLEAN_CONTROLS = [
    ("the recovery domain's own suite", EXPORT_PKG, "."),
    (
        "the recovery service suite",
        SERVICE_PKG,
        "TestKopiaSet|TestEveryMint|TestAFailedRange|TestGrantRefusals|TestRecoverALocalCopy",
    ),
    (
        "the recovery route suite",
        WEB_PKG,
        "TestARecoveryCompletes|TestMintCarries|TestMultipleRanges|TestADownloadSlower|TestAMintIsAudited",
    ),
    ("the recover verb suite", CLI_PKG, "TestRecoverIsRefusedBeforeAnythingIsOpened"),
]


def body(root: Path, dry_run: bool) -> int:
    tally = selftest_swap.Tally()

    with tempfile.TemporaryDirectory(prefix="retnd-recovery-selftest.") as tmp_name:
        tmp = Path(tmp_name)
        tracker = selftest_swap.AnchorTracker(dry_run=dry_run, root=root, tmp=tmp)

        print("==> negative controls: each slice is clean on the real tree")
        for label, where, pattern in CLEAN_CONTROLS:
            expect_passes(tracker, tally, f"{label} on an unmutated tree", root, where, pattern)

        for i, (label, rel, old, new, needle, where, pattern) in enumerate(MUTATIONS):
            print()
            print(f"==> {label}")
            d = selftest_swap.mutant(root, tmp, f"recovery-{i:02d}", dry_run=dry_run)
            tracker.swap(d / rel, old, new)
            expect_fails(tracker, tally, label, d, needle, where, pattern)

        tracker.stale_summary()
        stale_count = tracker.stale_count
        anchors_checked = tracker.anchors_checked

    total = tally.passed + tally.failed + stale_count
    if tally.failed or stale_count:
        print(file=sys.stderr)
        print(
            f"FAIL: {tally.failed + stale_count} of {total} recovery controls did not behave as required "
            f"({tally.failed} reached the wrong verdict, {stale_count} could not plant their violation at all).",
            file=sys.stderr,
        )
        return harness.EXIT_FAILED

    print()
    if dry_run:
        print(f"OK: all {anchors_checked} recovery mutation anchors still name code that is in this tree.")
    else:
        print(
            f"OK: all {tally.passed} recovery controls behaved as required (every guard was shown to go red "
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
