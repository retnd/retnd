#!/usr/bin/env python3
"""Which agents and encryption self-test controls would not run on a Linux gate host (#1543).

The gate's hosts are Linux machines (MARS, the NAS) running scripts/ci-local.sh
inside scripts/ci-docker.sh. A control whose command names a path on the
maintainer's Mac, goes through the Mac's Playwright lock, uses an image that was
built by hand there, or starts docker itself instead of going through the
Chromium lane (scripts/e2e/ui-browser.sh) cannot run on one of them, so the gate
used to leave it out with RETND_SELFTEST_SKIP_BROWSER=1 and its matrix row kept
resting on one Mac run.

This reads every control of scripts/bdtools/agents/selftest.py and
scripts/bdtools/encryption/selftest.py and says, for each, where it runs on a
Linux gate host:

    here     wherever the driver runs (go test, a shell checker, python)
    lane     through the Chromium lane, in the ui-browser shard
    skips    not at all, and why

A skip is allowed only for a reason named in ALLOWED below, declared on the
control as `needs=`. Anything else is a control the gate silently does not
check, and fails this.

It also checks that the self-tests' own split (bdtools.selftest_lane.plan)
agrees: with RETND_SELFTEST_LANE=skip a control is left out only because it
runs in the lane or for an allowed reason, with =only exactly the lane
controls run, and nothing falls between the two.

    selftest_linux_skips.py               the table; exit 1 while any skip has no allowed reason
    selftest_linux_skips.py --self-check  the rules against made-up controls, so they cannot go quiet
"""

from __future__ import annotations

import importlib
import sys
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

# The only reasons a control may be left out on a Linux gate host. Adding one
# is a decision a reviewer has to see, which is why the list lives in this
# test and not beside the controls.
ALLOWED = {
    "retnd-tests": "builds the planted copy and runs a case of the pinned retnd-tests checkout, "
    "which no gate shard hands the self-test yet (RETND_TESTS_DIR, RETND_COMMIT; #1548)",
}

# The two ways into the Chromium lane. A control whose command starts with
# `bash <one of these>` runs in the lane.
LANE_ENTRIES = ("scripts/e2e/ui-browser.sh", "scripts/e2e/ui-capture.sh")

# Scripts that start docker themselves, with mounts the gate host has to
# resolve: from a control they belong in the lane.
STARTS_DOCKER = ("scripts/contrast/run-browser-check.sh",)

SELFTESTS = ("bdtools.agents.selftest", "bdtools.encryption.selftest")


def verdict(c: object) -> tuple[str, str, bool]:
    """(where, why, allowed) for one control on a Linux gate host."""
    cmd: list[str] = list(getattr(c, "cmd", []) or [])
    needs: str = getattr(c, "needs", "") or ""
    if not cmd:
        if needs:
            return "skips", f"needs {needs}, which a go test control has no use for", False
        return "here", "", True
    line = " ".join(cmd)
    problems = []
    if "/Users/" in line:
        problems.append("hard-codes a path on the maintainer's Mac")
    if "xdg-pw-lock" in line:
        problems.append("goes through the Mac's Playwright lock")
    if "retnd-capture:" in line:
        problems.append("uses retnd-capture, an image built by hand with no Dockerfile in the tree")
    if cmd[0] == "docker" or "docker " in line or any(s in line for s in STARTS_DOCKER):
        problems.append("starts docker itself instead of going through the Chromium lane")
    if "retnd_tests_case.sh" in line and needs != "retnd-tests":
        problems.append("runs a retnd-tests case without saying so (needs=\"retnd-tests\")")
    if problems:
        return "skips", "; ".join(problems), False
    if needs:
        if needs in ALLOWED:
            return "skips", f"needs {needs}: {ALLOWED[needs]}", True
        return "skips", f"needs {needs}, which is not an allowed reason", False
    if len(cmd) >= 2 and cmd[0] == "bash" and cmd[1] in LANE_ENTRIES:
        return "lane", "", True
    return "here", "", True


def plan_problems(mod_name: str, controls: list, lane: set[str]) -> list[str]:
    """Where bdtools.selftest_lane.plan disagrees with verdict()."""
    try:
        selftest_lane = importlib.import_module("bdtools.selftest_lane")
    except ImportError as e:
        return [f"{mod_name}: there is no bdtools.selftest_lane to split the run with ({e})"]
    out = []
    env: dict[str, str] = {}
    run_skip, left_skip = selftest_lane.plan(controls, "skip", env)
    run_only, left_only = selftest_lane.plan(controls, "only", env)
    skipped_run = {c.label for c in run_skip}
    only_run = {c.label for c in run_only}
    for label, why in left_skip:
        c = next(x for x in controls if x.label == label)
        where, _, allowed = verdict(c)
        if label in lane:
            continue
        if where == "skips" and allowed:
            continue
        out.append(
            f"{mod_name}: RETND_SELFTEST_LANE=skip leaves out {label!r} ({why}), "
            "which is neither a lane control nor an allowed skip"
        )
    for c in controls:
        where, _, _ = verdict(c)
        if where == "lane" and c.label in skipped_run:
            out.append(f"{mod_name}: RETND_SELFTEST_LANE=skip runs the lane control {c.label!r} outside the lane")
        if where == "here" and c.label not in skipped_run:
            out.append(f"{mod_name}: RETND_SELFTEST_LANE=skip does not run {c.label!r}, which needs nothing")
    if only_run != lane:
        extra = sorted(only_run - lane)
        missing = sorted(lane - only_run)
        if extra:
            out.append(f"{mod_name}: RETND_SELFTEST_LANE=only runs controls that are not lane controls: {extra}")
        if missing:
            out.append(f"{mod_name}: RETND_SELFTEST_LANE=only does not run the lane controls {missing}")
    if left_only:
        out.append(f"{mod_name}: RETND_SELFTEST_LANE=only leaves lane controls out: {left_only}")
    return out


def report() -> int:
    bad: list[str] = []
    for mod_name in SELFTESTS:
        mod = importlib.import_module(mod_name)
        controls = list(mod.CONTROLS)
        counts = {"here": 0, "lane": 0, "skips": 0}
        lane: set[str] = set()
        print(f"==> {mod_name}: {len(controls)} controls")
        for c in controls:
            where, why, allowed = verdict(c)
            counts[where] += 1
            if where == "lane":
                lane.add(c.label)
            if where == "skips":
                mark = "allowed" if allowed else "NOT ALLOWED"
                print(f"    skips  {c.label}  [{mark}: {why}]")
                if not allowed:
                    bad.append(f"{mod_name}: {c.label!r} would skip on a Linux gate host: {why}")
        print(
            f"    {counts['here']} run where the driver runs, {counts['lane']} in the Chromium lane, "
            f"{counts['skips']} skip"
        )
        bad += plan_problems(mod_name, controls, lane)
    if bad:
        print()
        print(
            f"FAIL: {len(bad)} control(s) would be left out on a Linux gate host without an allowed reason, "
            "or the run's split disagrees:"
        )
        for b in bad:
            print(f"    {b}")
        return 1
    print(
        "OK: every control runs on a Linux gate host, in the Chromium lane or where the driver runs, "
        "or skips for an allowed reason."
    )
    return 0


@dataclass
class Fake:
    label: str
    cmd: list[str] = field(default_factory=list)
    needs: str = ""


def self_check() -> int:
    browser = "scripts/e2e/ui-browser.sh"
    case_sh = "scripts/bdtools/encryption/retnd_tests_case.sh"
    cases = [
        (Fake("go", []), "here", True),
        (Fake("checker", ["python3", "scripts/contrast/check-ui-pairs.py"]), "here", True),
        (Fake("spec", ["bash", browser, "specs", "ui/shared/e2e/playwright.config.ts", "e2e/x.spec.ts"]), "lane", True),
        (Fake("capture", ["bash", "scripts/e2e/ui-capture.sh", "docs/site/tools/capture-agents.mjs"]), "lane", True),
        (Fake("mac path", ["sh", "-c", "docker run -v /Users/rom/workspace/x:/x img"]), "skips", False),
        (Fake("lock", ["/opt/xdg-pw-lock.sh", "bash", browser, "run", "true"]), "skips", False),
        (Fake("hand-built", ["bash", browser, "run", "retnd-capture:1.62.1"]), "skips", False),
        (Fake("bare docker", ["sh", "-c", "docker run --rm node:22 npx vitest run"]), "skips", False),
        (Fake("contrast", ["bash", "scripts/contrast/run-browser-check.sh", "--only", "x.html"]), "skips", False),
        (Fake("rt undeclared", ["bash", case_sh, "a", "x"]), "skips", False),
        (Fake("rt declared", ["bash", case_sh, "a", "x"], "retnd-tests"), "skips", True),
        (Fake("made-up need", ["bash", "scripts/tests/x.test.sh"], "a-quiet-afternoon"), "skips", False),
        (Fake("need on a go control", [], "retnd-tests"), "skips", False),
    ]
    failures = 0
    for c, want_where, want_allowed in cases:
        where, why, allowed = verdict(c)
        ok = where == want_where and allowed == want_allowed
        failures += not ok
        print(f"    {'ok  ' if ok else 'FAIL'} {c.label}: {where}{' (' + why + ')' if why else ''}, allowed={allowed}")
    return 1 if failures else 0


if __name__ == "__main__":
    if sys.argv[1:] == ["--self-check"]:
        sys.exit(self_check())
    if sys.argv[1:]:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    sys.exit(report())
