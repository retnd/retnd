#!/usr/bin/env python3
"""Planted violations for issue #1528's retirement (docs/conformance/issue-1528-matrix.md).

Every `RV.n` row of that matrix names a one-line violation and the test that
must catch it. This file plants each one in a scratch copy of the tree, runs
only that test, and needs it red naming the test; then it restores the file
and needs the same test green, so a test that was already red before anything
was planted cannot pass for a catch. The real tree is never written.

Every mutation is anchored to a verbatim piece of product source. An anchor
that no longer matches is reported as STALE ANCHOR and fails the run, so a
refactor cannot quietly turn a control into one that plants nothing.

  --check-anchors   check every anchor against the real tree and build nothing
                    (this is what the fast tier runs)
  --only TEXT       run only the controls whose id contains TEXT (repeatable)
"""

from __future__ import annotations

import argparse
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]


@dataclass(frozen=True)
class Control:
    id: str
    path: str
    anchor: str
    planted: str
    module: str  # the directory go test runs in
    package: str
    tests: str  # the -run expression; the red output must name one of them


CONTROLS = [
    Control("RV.1", "core/internal/app/cycle.go",
            "if n := s.autoRetire(ctx, src, bs, after); n > 0",
            "if n := len(after) * 0; n > 0",
            "core", "./internal/app/", "TestTheForgeSetGoesFromFailingToHealthyWhenTheSourceIsGone_1528"),
    Control("RV.2", "core/internal/app/retire.go",
            "func sourceCheckFor(remotePath string, listing []transport.RemoteArtifact, listErr error, stat func() error) SourceCheck {\n",
            "func sourceCheckFor(remotePath string, listing []transport.RemoteArtifact, listErr error, stat func() error) SourceCheck {\n"
            "\tif remotePath != \"\" {\n\t\treturn SourceCheck{Result: state.SourceAbsent}\n\t}\n",
            "core", "./internal/app/", "TestNothingIsRetiredWhileTheSourceIsStillThere_1528"),
    Control("RV.3", "core/internal/app/retire.go",
            "\tif c, ok := transport.CategoryOf(err); ok && c == transport.NotFound {\n",
            "\tif err != nil {\n",
            "core", "./internal/app/", "TestAPermissionFailureOnAPresentFileIsNeverAutoRetired_1528|TestATransientStatIsNotAbsence_1528"),
    Control("RV.4", "core/internal/app/retire.go",
            "func newerGood(failed, candidate state.Record) bool {\n",
            "func newerGood(failed, candidate state.Record) bool {\n"
            "\tif candidate.State != \"\" {\n"
            "\t\treturn candidate.Artifact != failed.Artifact && lifecycle.IsDurableRestorePoint(lifecycle.State(candidate.State))\n\t}\n",
            "core", "./internal/app/", "TestAnOlderGoodBackupIsNotASuccessor_1528"),
    Control("RV.5", "core/internal/app/retire.go",
            "err == nil && rec.LocalPath == final {\n",
            "err == nil && rec.LocalPath == final && final == \"\" {\n",
            "core", "./internal/app/", "TestTheOwnCopyAtTheFinalNameIsNeverRetired_1528"),
    Control("RV.6", "core/internal/app/cycle.go",
            "\ts.bringBackReappeared(ctx, discRes)\n",
            "\t_ = discRes\n",
            "core", "./internal/app/", "TestARetiredObjectThatReappearsReentersThePipeline_1528"),
    Control("RV.7", "core/internal/app/retire.go",
            "\t\tremoved = s.removePartial(ctx, p.partial)\n",
            "\t\tremoved = false\n",
            "core", "./internal/app/", "TestTheLeftoverPartialIsRemovedWithTheRetirement_1528"),
    Control("RV.8", "core/internal/lifecycle/machine.go",
            "\t{From: Failed, To: Retired},\n",
            "\t{From: Failed, To: Retired},\n\t{From: Committed, To: Retired},\n",
            "core", "./internal/lifecycle/", "TestRetiredIsReachableOnlyFromFailed_1528"),
    Control("RV.9", "core/internal/state/journal.go",
            "\t\tif err := insertRetirement(ctx, tx, artifactRowID, t, redact); err != nil {\n\t\t\treturn Outcome{}, err\n\t\t}\n",
            "\t\t_ = insertRetirement(ctx, tx, artifactRowID, t, redact)\n",
            "core", "./internal/state/", "TestARetirementAndItsRecordCommitOrRollBackTogether_1528"),
    Control("RV.10", "core/internal/health/compute.go",
            "\t\t\tagg.retiredCount++\n",
            "\t\t\tagg.retiredCount++\n\t\t\tagg.failures++\n",
            "core", "./internal/health/", "TestRetiredIsCountedApartAndNeverFails_1528"),
    Control("RV.11", "core/internal/app/retire.go",
            "\tp.check = s.probe(ctx, src, bs, rec.RemotePath, listing)\n",
            "\tp.check = SourceCheck{Result: state.SourceAbsent}\n",
            "core", "./internal/app/", "TestOperatorRetireRefusesWhileTheSourceIsPresent_1528"),
    Control("RV.12", "apps/common/webhost/agentconfirmations.go",
            "found && len(row.AgentMayNotSet) > 0 {\n",
            "found && len(row.AgentMayNotSet) > 99 {\n",
            "apps/common", "./webhost/", "TestAnAgentCannotSendARetireAcknowledgement_1528"),
    Control("RV.13", "apps/common/webhost/handlers_artifacts.go",
            "\tNextCursor *string `json:\"next_cursor\"`\n",
            "\tNextCursor *string `json:\"next_cursor,omitempty\"`\n",
            "apps/common", "./webhost/", "TestTheBackupsListAlwaysCarriesACursor_1528"),
    Control("RV.14", "core/internal/app/retire.go",
            "\t\t\tRetry:  &state.RetryUpdate{Count: rec.RetryCount + 1, LastError: r.FailureReason},\n",
            "",
            "core", "./internal/app/", "TestARetiredObjectThatReappearsReentersThePipeline_1528"),
    Control("RV.15", "core/internal/app/retire.go",
            "if a.Path == remotePath || path.Base(a.Path) == name {",
            "if a.Path == remotePath || name == \"\" {",
            "core", "./internal/app/", "TestAnObjectMovedIntoASubdirectoryIsNotRetiredAsGone_1528"),
]


def check_anchors(root: Path, controls: list[Control]) -> int:
    stale = 0
    for c in controls:
        text = (root / c.path).read_text()
        n = text.count(c.anchor)
        if n != 1:
            stale += 1
            print(f"retire-selftest: STALE ANCHOR {c.id}: {c.path} carries the anchor {n} times, want exactly 1")
        else:
            print(f"retire-selftest: anchor ok {c.id} ({c.path})")
    return stale


def go_test(tree: Path, c: Control) -> tuple[int, str]:
    p = subprocess.run(["go", "test", "-count=1", "-run", f"^({c.tests})$", c.package],
                       cwd=tree / c.module, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--check-anchors", action="store_true")
    ap.add_argument("--only", action="append", default=[])
    args = ap.parse_args()
    controls = [c for c in CONTROLS if not args.only or any(o in c.id for o in args.only)]
    if not controls:
        print("retire-selftest: no control matches --only", file=sys.stderr)
        return 2
    stale = check_anchors(REPO, controls)
    if stale:
        return 1
    if args.check_anchors:
        print(f"retire-selftest: {len(controls)} anchors resolve")
        return 0

    failures = 0
    with tempfile.TemporaryDirectory(prefix="retire-selftest-") as tmp:
        tree = Path(tmp) / "tree"
        ignore = shutil.ignore_patterns(".git", "node_modules", "dist")
        shutil.copytree(REPO, tree, ignore=ignore, symlinks=True)
        for c in controls:
            f = tree / c.path
            original = f.read_text()
            try:
                f.write_text(original.replace(c.anchor, c.planted, 1))
                code, out = go_test(tree, c)
                names = c.tests.split("|")
                caught = code != 0 and any(f"--- FAIL: {n}" in out for n in names)
            finally:
                f.write_text(original)
            code_clean, out_clean = go_test(tree, c)
            if caught and code_clean == 0:
                print(f"retire-selftest: ok   {c.id}: planted in {c.path}, {c.tests} went red and is green again")
            else:
                failures += 1
                why = "the planted violation did not turn the test red naming it" if not caught else "the unplanted test is not green"
                tail = (out if not caught else out_clean)[-1500:]
                print(f"retire-selftest: FAIL {c.id}: {why}\n{tail}")
    print(f"retire-selftest: {len(controls)} controls, {failures} failures")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
