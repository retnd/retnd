"""Which self-test controls run in the Chromium lane, and which are left out (#1543).

The agents and encryption self-tests (scripts/bdtools/agents/selftest.py,
scripts/bdtools/encryption/selftest.py) have controls whose proof is a browser
spec, a vitest test, tsc, the browser contrast check or the capture rig. Those
go through the Chromium lane (scripts/e2e/ui-browser.sh, or
scripts/e2e/ui-capture.sh for the capture rig, which hands the lane the image
built from scripts/e2e/capture.Dockerfile), run from the planted copy, so the
lane mounts the copy read-only and makes its own linux install of ui/shared.
Nothing in them names a machine.

The gate runs each self-test twice over: its own shard leaves the lane half
out by name (RETND_SELFTEST_LANE=skip) because that shard has no docker, and
the ui-browser shard runs only that half (RETND_SELFTEST_LANE=only). Unset,
or `all`, everything runs.

A control may also declare `needs`: something outside the tree it cannot run
without. It is left out, counted and named, unless the variable that provides
it is set. scripts/tests/selftest_linux_skips.py holds the list of needs a
control is allowed to declare, and fails on any other.
"""

from __future__ import annotations

import os
import sys
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any

LANE = "scripts/e2e/ui-browser.sh"
CAPTURE = "scripts/e2e/ui-capture.sh"

# need -> (the variables that provide it, what it is)
NEEDS: dict[str, tuple[tuple[str, ...], str]] = {
    "retnd-tests": (
        ("RETND_TESTS_DIR", "RETND_COMMIT"),
        "the pinned retnd-tests checkout, which no gate shard hands the self-test yet (#1548)",
    ),
}

MODES = ("all", "skip", "only")

IN_LANE_ELSEWHERE = "runs in the Chromium lane, in the ui-browser shard (RETND_SELFTEST_LANE=only)"


def in_lane(cmd: Sequence[str]) -> bool:
    return len(cmd) >= 2 and cmd[0] == "bash" and cmd[1] in (LANE, CAPTURE)


def spec(spec_file: str, grep: str) -> list[str]:
    """One ui/shared Playwright spec, narrowed to the tests matching `grep`."""
    return ["bash", LANE, "specs", "ui/shared/e2e/playwright.config.ts", spec_file, "-g", grep]


def in_ui_shared(command: str) -> list[str]:
    """A command in the lane's linux install of ui/shared (vitest, tsc)."""
    return ["bash", LANE, "run", "sh", "-c", f"cd ui/shared && NO_COLOR=1 FORCE_COLOR=0 {command}"]


def capture(script: str, args: str) -> list[str]:
    """One docs/site/tools capture script in the lane, with ffmpeg."""
    return ["bash", CAPTURE, script, *args.split()]


def mode_from_env(env: Mapping[str, str]) -> str:
    mode = env.get("RETND_SELFTEST_LANE", "") or "all"
    if mode not in MODES:
        raise ValueError(f"RETND_SELFTEST_LANE={mode!r}: it is all, skip or only")
    return mode


def plan(controls: Sequence[Any], mode: str, env: Mapping[str, str]) -> tuple[list[Any], list[tuple[str, str]]]:
    """(the controls to run, [(label, why)] for the ones left out).

    In `only` the controls that are not lane controls are simply the other
    half, run by the self-test's own shard, and are not listed as left out.
    """
    run: list[Any] = []
    left: list[tuple[str, str]] = []
    for c in controls:
        lane_control = bool(c.cmd) and in_lane(c.cmd)
        if mode == "only" and not lane_control:
            continue
        need = getattr(c, "needs", "")
        if need:
            names, what = NEEDS[need]
            missing = [n for n in names if not env.get(n)]
            if missing:
                left.append((c.label, f"needs {what} ({', '.join(missing)} unset)"))
                continue
        if mode == "skip" and lane_control:
            left.append((c.label, IN_LANE_ELSEWHERE))
            continue
        run.append(c)
    return run, left


def announce(left: Sequence[tuple[str, str]]) -> None:
    """The left-out controls, counted and named, grouped by reason."""
    if not left:
        return
    by_why: dict[str, list[str]] = {}
    for label, why in left:
        by_why.setdefault(why, []).append(label)
    print(f"==> not running {len(left)} controls here:")
    for why, labels in by_why.items():
        print(f"    {len(labels)} {why}:")
        for label in labels:
            print(f"        {label}")


def lock_prefix() -> list[str] | None:
    """What a lane command runs under on this machine: nothing on Linux; on a
    Mac the workspace Playwright lock (RETND_PW_LOCK names another), the same
    one scripts/ci-local.sh takes. None when a Mac has no lock to take: a
    browser run never goes ahead bare there."""
    if sys.platform != "darwin":
        return []
    pw_lock = os.environ.get("RETND_PW_LOCK", "/Users/rom/workspace/xdg-pw-lock.sh")
    if Path(pw_lock).is_file() and os.access(pw_lock, os.X_OK):
        return [pw_lock]
    return None
