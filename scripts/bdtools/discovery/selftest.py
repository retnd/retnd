#!/usr/bin/env python3
"""Positive controls for discovery's readability probe (issue #1541).

`core/internal/discovery/unreadable_1541_test.go` is the evidence that a
source file which is complete under its strategy but cannot be opened yet
waits as Pending, is not journaled, and is admitted on the first pass that
can read it. The cheapest way to pass "nothing is failed" is to never look,
so the probe's own classification is mutation-tested here against the real
tree: a copy of the working tree gets one deliberate violation planted in
`core/internal/discovery/discovery.go`, the narrowed suite runs, and it must
fail AND name the promise the violation broke. Naming it, not merely
failing, is what stops a plant that broke the build for an unrelated reason
from reading as a pass. `scripts/bdtools/retention/selftest.py` is the
template, and this shares `selftest_swap`'s anchor machinery.

Every mutation is anchored to a verbatim copy of product source, tabs and
all. An anchor that no longer matches is STALE ANCHOR: the control is
skipped rather than run against a tree with nothing planted in it.
`--check-anchors` checks every anchor against the real tree, building
nothing, and `scripts/bdtools/selftest/check_anchors.py` runs it alongside
the others.

Seconds rather than minutes: each mutant rebuilds one package and runs a
narrowed slice of it. No Docker, no network.
"""

from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness, selftest_swap

PROGRAM = "discovery selftest"

PACKAGES = ("./internal/discovery/",)

DISCOVERY_GO = "core/internal/discovery/discovery.go"

PASS_PATTERN = "_1541"

DENIED_READ_AS_COMPLETE_OLD = (
    "\tcase category == transport.PermissionDenied || errors.Is(err, os.ErrPermission):\n"
    '\t\treturn "unreadable at the source (permission denied): the producer has not published it yet", nil'
)
DENIED_READ_AS_COMPLETE_NEW = (
    "\tcase category == transport.PermissionDenied || errors.Is(err, os.ErrPermission):\n"
    "\t\t// PLANTED VIOLATION (scripts/bdtools/discovery/selftest.py): the\n"
    "\t\t// readability probe treats permission denied as complete, so a file\n"
    "\t\t// the producer has not published is journaled and later FAILED.\n"
    '\t\treturn "", nil'
)

PROBE_SKIPPED_OLD = '\topener, ok := deps.Transport.(sourceOpener)\n\tif !ok {\n\t\treturn "", nil\n\t}'
PROBE_SKIPPED_NEW = (
    "\t// PLANTED VIOLATION (scripts/bdtools/discovery/selftest.py): no\n"
    "\t// transport is ever probed, which is discovery before #1541.\n"
    "\topener, ok := deps.Transport.(sourceOpener)\n"
    "\tif !ok || opener != nil {\n"
    '\t\treturn "", nil\n'
    "\t}"
)


def discovery_gate(root: Path, pattern: str) -> tuple[int, str]:
    """Run the narrowed suite in `root`, never raising on its own non-zero
    status: an expected failure has to become a value this file inspects,
    for the reason retention/selftest.py's hazard note gives."""
    env = dict(os.environ)
    env["GOWORK"] = "off"
    proc = harness.sh(
        ["go", "test", "-count=1", "-timeout", "5m", "-run", pattern, *PACKAGES],
        check=False,
        capture=True,
        cwd=root / "core",
        env=env,
    )
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def expect_gate_fails(
    tracker: selftest_swap.AnchorTracker,
    tally: selftest_swap.Tally,
    label: str,
    directory: Path,
    needle: str,
    pattern: str,
) -> None:
    if tracker.stale_verdict(label):
        return
    if tracker.anchors_only(label):
        return
    code, out = discovery_gate(directory, pattern)
    if code == 0:
        tally.bad(f"{label}. The evidence PASSED against a planted violation.", out)
        return
    if needle not in out:
        tally.bad(
            f"{label}. The suite failed, but never named the promise that was broken.",
            f"expected its output to mention: {needle}\n{out}",
        )
        return
    matched = next((line.strip()[:400] for line in out.splitlines() if needle in line), "")
    tally.ok(f"(caught): {label}", matched)


def expect_gate_passes(
    tracker: selftest_swap.AnchorTracker,
    tally: selftest_swap.Tally,
    label: str,
    directory: Path,
    pattern: str,
) -> None:
    """The negative control: a plant that turns a suite red proves nothing
    if the suite was red already."""
    if tracker.stale_verdict(label):
        return
    if tracker.anchors_only(label):
        return
    code, out = discovery_gate(directory, pattern)
    if code == 0:
        tally.ok(f"(clean): {label}")
    else:
        tally.bad(f"{label}. The evidence FAILED against an unmutated tree, so its failures mean nothing.", out)


def body(root: Path, dry_run: bool) -> int:
    tally = selftest_swap.Tally()

    with tempfile.TemporaryDirectory(prefix="retnd-discovery-selftest.") as tmp_name:
        tmp = Path(tmp_name)
        tracker = selftest_swap.AnchorTracker(dry_run=dry_run, root=root, tmp=tmp)
        # One copy on disk at a time (#1580): the next case's copy replaces this one.
        mutant = selftest_swap.one_at_a_time(
            lambda name: selftest_swap.mutant(root, tmp, name, dry_run=dry_run), root
        )

        print("==> negative control: the #1541 discovery evidence is clean on the real tree")
        expect_gate_passes(tracker, tally, "the #1541 discovery evidence on an unmutated tree", root, PASS_PATTERN)

        print()
        print("==> a complete file the source will not open waits, and is never journaled")
        d = mutant("denied-read-as-complete")
        tracker.swap(d / DISCOVERY_GO, DENIED_READ_AS_COMPLETE_OLD, DENIED_READ_AS_COMPLETE_NEW)
        expect_gate_fails(
            tracker,
            tally,
            "the readability probe treats permission denied as complete",
            d,
            "a complete but unreadable file was admitted",
            "TestDiscover_ACompleteFileTheSourceWillNotOpenWaitsAsPending_1541",
        )

        print()
        print("==> a new candidate is probed at all")
        d = mutant("probe-skipped")
        tracker.swap(d / DISCOVERY_GO, PROBE_SKIPPED_OLD, PROBE_SKIPPED_NEW)
        expect_gate_fails(
            tracker,
            tally,
            "discovery never opens a new candidate before admitting it",
            d,
            "want the new candidate probed exactly once",
            "TestDiscover_ACandidateAlreadyJournaledIsNotProbedAgain_1541",
        )

        tracker.stale_summary()
        stale_count = tracker.stale_count
        anchors_checked = tracker.anchors_checked

    total = tally.passed + tally.failed + stale_count
    if tally.failed or stale_count:
        print(file=sys.stderr)
        print(
            f"FAIL: {tally.failed + stale_count} of {total} discovery controls did not behave as required "
            f"({tally.failed} reached the wrong verdict, {stale_count} could not plant their violation at all).",
            file=sys.stderr,
        )
        return harness.EXIT_FAILED

    print()
    if dry_run:
        print(f"OK: all {anchors_checked} discovery mutation anchors still name code that is in this tree.")
    else:
        print(
            f"OK: all {tally.passed} discovery controls behaved as required (every claim was shown to go "
            "red against a real planted violation, and shown not to on the real tree)."
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
