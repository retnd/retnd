#!/usr/bin/env python3
"""What a self-test control needs from its host (#1530, #1531, the #1538 review).

The agents and encryption mutation self-tests have controls that drive a
browser through the Playwright lock, start docker, run node, or run a case from
the retnd-tests checkout. A gate host may have none of that. bdtools/selftest_lanes.py
decides which controls a host can run, and these cases hold it:

  A  what a command visibly needs: the Playwright lock, docker, node (a vitest
     or tsc run that falls back from docker to npx), the retnd-tests checkout,
     and nothing for a Go test whose package path happens to say docker.
  B  the pinned files (scripts/bdtools/<x>/lanes.txt) name exactly the controls
     that need a lane, with the lanes each needs, in both self-tests: a control
     that starts needing one, or stops, is a reviewed change to that file.
  C  the plan: a host with every lane runs everything; a host without one
     skips exactly the controls that need it and names what is missing; a
     --check-anchors run and a --record run skip nothing.
  D  the skip ledger: a skipped control is one line in the file the gate
     reads into its skip ledger, so the shard ends INCOMPLETE, not ok.
  E  the controls: pin_problems reports a control added to and dropped from the
     pinned set, so B is able to fail.
"""

from __future__ import annotations

import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

HERE = Path(__file__).resolve()
SCRIPTS = HERE.parents[2]
sys.path.insert(0, str(SCRIPTS))

from bdtools import selftest_lanes as lanes  # noqa: E402
from bdtools.tests import testkit  # noqa: E402

LOCK = "/Users/rom/workspace/xdg-pw-lock.sh"
NODE_FALLBACK = (
    "w=$(pwd); if docker info >/dev/null 2>&1; then docker run img npx vitest run a; else npx vitest run a; fi"
)


@dataclass
class Fake:
    label: str
    cmd: list[str]


def main() -> int:
    s = testkit.Suite("selftest-lanes")

    # A
    vis = lanes.visible_needs
    s.assert_eq("A a Playwright spec needs the lock and docker", "docker,playwright-lock",
                ",".join(sorted(vis([LOCK, "sh", "-c", "w=$(pwd); docker run img npx playwright test x"]))))
    s.assert_eq("A a vitest run with the node fallback needs node only", "node",
                ",".join(sorted(vis(["sh", "-c", NODE_FALLBACK]))))
    s.assert_eq("A a bare docker run needs docker", "docker",
                ",".join(sorted(vis(["sh", "-c", "w=$(pwd); docker run --rm img true"]))))
    s.assert_eq("A a retnd-tests case needs the checkout", "retnd-tests",
                ",".join(sorted(vis(["bash", "scripts/bdtools/encryption/retnd_tests_case.sh", "x"]))))
    s.assert_eq("A a Go test under ./internal/docker needs nothing", "",
                ",".join(sorted(vis(["go", "test", "./internal/docker", "-run", "TestX"]))))
    s.assert_eq("A a Go control (no command) needs nothing", "", ",".join(sorted(vis([]))))

    # B
    for name in ("agents", "encryption"):
        mod = __import__(f"bdtools.{name}.selftest", fromlist=["CONTROLS", "LANES_FILE"])
        pinned = lanes.load_pinned(mod.LANES_FILE)
        problems = lanes.pin_problems({c.label: c.cmd for c in mod.CONTROLS}, pinned)
        s.check(not problems, f"B {name}: lanes.txt names exactly the controls that need a lane",
                f"B {name}: lanes.txt and the controls disagree", "\n".join(problems))
        s.check(len(pinned) > 0, f"B {name}: the pinned set is not empty ({len(pinned)})",
                f"B {name}: the pinned set is empty, so B proves nothing")

    # C
    pinned = {"browser one": frozenset({"playwright-lock", "docker"}), "node one": frozenset({"node"}),
              "tests one": frozenset({"retnd-tests"})}
    controls = [Fake("go one", []), Fake("browser one", [LOCK]), Fake("node one", ["sh"]), Fake("tests one", ["bash"])]
    every = {lane: True for lane in lanes.LANES}
    run, skipped = lanes.plan(controls, pinned, every, dry_run=False, recording=False)
    s.check(len(run) == 4 and not skipped, "C a host with every lane runs every control",
            "C a host with every lane skipped something", repr(skipped))
    bare = {lane: False for lane in lanes.LANES}
    bare["node"] = True
    run, skipped = lanes.plan(controls, pinned, bare, dry_run=False, recording=False)
    s.assert_eq("C a host with only node runs the Go and node controls", "go one,node one",
                ",".join(c.label for c in run))
    s.assert_eq("C and skips exactly the browser and retnd-tests ones, naming what is missing",
                "browser one: docker, playwright-lock|tests one: retnd-tests",
                "|".join(f"{c.label}: {', '.join(m)}" for c, m in skipped))
    modes = (
        ("--check-anchors", {"dry_run": True, "recording": False}),
        ("--record", {"dry_run": False, "recording": True}),
    )
    for mode, kw in modes:
        run, skipped = lanes.plan(controls, pinned, bare, **kw)
        s.check(len(run) == 4 and not skipped, f"C a {mode} run skips nothing",
                f"C a {mode} run skipped something", repr(skipped))

    # D
    with tempfile.TemporaryDirectory() as tmp:
        ledger = Path(tmp) / "skips"
        run, skipped = lanes.plan(controls, pinned, bare, dry_run=False, recording=False)
        code = lanes.report(skipped, "encryption", str(ledger))
        lines = ledger.read_text().splitlines() if ledger.exists() else []
        s.check(code == 0 and len(lines) == 2 and all("encryption self-test" in x for x in lines),
                "D two skipped controls are two lines in the gate's ledger file", "D the ledger file is wrong",
                f"code={code} lines={lines}")
        s.check(lanes.report(skipped, "encryption", "") == 3,
                "D without a ledger file a run that skipped something is INCOMPLETE (3)",
                "D a run that skipped controls with nobody to tell exited as if whole")
        s.check(lanes.report([], "encryption", "") == 0, "D a run that skipped nothing is 0",
                "D an empty skip was not 0")

    # E
    probs = lanes.pin_problems({"a": [LOCK], "b": []}, {"b": frozenset({"docker"})})
    s.check(len(probs) == 2, "E an unpinned control that needs a lane, and a pinned one that needs none, are reported",
            "E pin_problems missed a disagreement", "\n".join(probs))
    return s.finish()


if __name__ == "__main__":
    sys.exit(main())
