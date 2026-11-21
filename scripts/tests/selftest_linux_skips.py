#!/usr/bin/env python3
"""Which agents and encryption self-test controls would not run on a Linux gate host (#1543).

The gate's hosts are Linux machines (MARS, the NAS) running scripts/ci-local.sh
inside scripts/ci-docker.sh, a container with the host's docker socket, node
and ui/shared's dependencies, and nothing else: no Playwright lock (that is the
maintainer's Mac) and no retnd-tests checkout. bdtools/selftest_lanes.py says
which lanes each control needs (pinned in scripts/bdtools/<x>/lanes.txt), and a
control whose lane the host lacks is skipped into the gate's skip ledger.

Until #1543 about twenty browser, vitest and capture controls needed the
Playwright lock, hard-coded worktrees on the Mac and a capture image built by
hand there, so on every gate host they were skipped. This lists, for each
control, where it runs on a Linux gate host:

    here     wherever the driver runs (go test, a shell checker, python, node)
    lane     through the Chromium lane (scripts/e2e/ui-browser.sh)
    skips    not at all, and why

A skip is allowed only for a lane named in ALLOWED below. Anything else is a
control the gate never checks, and fails this. Whatever the lanes say, a
command that names a path on the Mac or the hand-built image fails too.

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

from bdtools import selftest_lanes as lanes  # noqa: E402

# What a Linux gate host has. The selftest-encryption step fetches the pinned
# retnd-tests checkout itself (scripts/e2e/tests-checkout.sh, #1548).
LINUX_GATE = {lanes.LANE_DOCKER, lanes.LANE_NODE, lanes.LANE_CHROMIUM, lanes.LANE_TESTS}

# The only lanes a control may lack there. Adding one is a decision a reviewer
# has to see, which is why the list lives in this test and not beside the controls.
ALLOWED = {
    lanes.LANE_TESTS_BROWSER: "Suite C needs its npm install and a Chromium next to the retnd-tests checkout, "
    "and a shard container has neither (#1548 follow-up: run it in the Playwright image)",
}

SELFTESTS = ("bdtools.agents.selftest", "bdtools.encryption.selftest")


def verdict(cmd: list[str]) -> tuple[str, str, bool]:
    """(where, why, allowed) for one control's command on a Linux gate host."""
    line = " ".join(cmd)
    problems = []
    if "/Users/" in line:
        problems.append("hard-codes a path on the maintainer's Mac")
    if "retnd-capture:" in line:
        problems.append("uses retnd-capture, an image built by hand with no Dockerfile in the tree")
    needs = lanes.visible_needs(cmd)
    missing = sorted(needs - LINUX_GATE)
    if problems:
        return "skips", "; ".join(problems), False
    if missing:
        bad = [m for m in missing if m not in ALLOWED]
        why = "; ".join(
            f"needs {m}" + (f": {ALLOWED[m]}" if m in ALLOWED else ", which a Linux gate host does not have")
            for m in missing
        )
        return "skips", why, not bad
    if lanes.LANE_CHROMIUM in needs:
        return "lane", "", True
    return "here", "", True


def report() -> int:
    bad: list[str] = []
    for mod_name in SELFTESTS:
        mod = importlib.import_module(mod_name)
        controls = list(mod.CONTROLS)
        counts = {"here": 0, "lane": 0, "skips": 0}
        print(f"==> {mod_name}: {len(controls)} controls")
        for c in controls:
            where, why, allowed = verdict(list(c.cmd))
            counts[where] += 1
            if where == "skips":
                print(f"    skips  {c.label}  [{'allowed' if allowed else 'NOT ALLOWED'}: {why}]")
                if not allowed:
                    bad.append(f"{mod_name}: {c.label!r} would skip on a Linux gate host: {why}")
        print(
            f"    {counts['here']} run where the driver runs, {counts['lane']} in the Chromium lane, "
            f"{counts['skips']} skip"
        )
        problems = lanes.pin_problems({c.label: c.cmd for c in controls}, lanes.load_pinned(mod.LANES_FILE))
        bad += [f"{mod_name}: {p}" for p in problems]
    if bad:
        print()
        print(f"FAIL: {len(bad)} control(s) would be left out on a Linux gate host without an allowed reason:")
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


def self_check() -> int:
    lock = "/Users/rom/workspace/xdg-pw-lock.sh"
    case_sh = "scripts/bdtools/encryption/retnd_tests_case.sh"
    cases = [
        (Fake("go", []), "here", True),
        (Fake("checker", ["python3", "scripts/contrast/check-ui-pairs.py"]), "here", True),
        (Fake("spec", lanes.spec("e2e/x.spec.ts", "a test")), "lane", True),
        (Fake("capture", lanes.capture("docs/site/tools/capture-agents.mjs", "--check")), "lane", True),
        (Fake("contrast", lanes.chromium("bash scripts/contrast/in-container.sh")), "lane", True),
        (Fake("mac lock", [lock, "sh", "-c", "docker run img npx playwright test x"]), "skips", False),
        (Fake("mac path", ["sh", "-c", "w=$(pwd); docker run -v /Users/rom/workspace/x:/x img true"]), "skips", False),
        (Fake("hand-built", lanes.chromium("docker run retnd-capture:1.62.1 node x")), "skips", False),
        (Fake("rt suite a", ["bash", case_sh, "a", "x"]), "here", True),
        (Fake("rt suite c", ["bash", case_sh, "c", "x"]), "skips", True),
    ]
    failures = 0
    for c, want_where, want_allowed in cases:
        where, why, allowed = verdict(c.cmd)
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
