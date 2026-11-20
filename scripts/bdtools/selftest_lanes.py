"""What a self-test control needs from its host, and what this host has (#1530, #1531).

The agents and encryption mutation self-tests carry controls that need more than
Go: a browser through the Playwright lock, a docker daemon, node with
ui/shared's dependencies, or the retnd-tests checkout. A gate host runs the
self-test inside a container that may have none of those.

Which controls need which lane is pinned per self-test in its lanes.txt, one
`lane,lane<TAB>label` line each, so a control that starts or stops needing one is
a reviewed change to that file. visible_needs() reads what a command asks for
and pin_problems() refuses any disagreement with the pinned file, which is a
cross-check and not the source of truth.

plan() then runs every control whose lanes this host has and skips the rest,
naming what each is missing, except in a --check-anchors or a --record run,
which skip nothing. report() writes the skips to the file the gate hands over
in RETND_SELFTEST_SKIP_LEDGER, so the gate's skip ledger holds them and the shard
ends INCOMPLETE; run by hand with no ledger, a run that skipped anything exits 3.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
from pathlib import Path
from typing import Protocol, Sequence, TypeVar

LANE_LOCK = "playwright-lock"
LANE_DOCKER = "docker"
LANE_NODE = "node"
LANE_TESTS = "retnd-tests"
LANES = (LANE_LOCK, LANE_DOCKER, LANE_NODE, LANE_TESTS)

PW_LOCK = Path("/Users/rom/workspace/xdg-pw-lock.sh")

# docker run in command position: at the start, or after ; & | ( or then/else.
_DOCKER_RUN = re.compile(r"(?:^|[;&|(]\s*|\b(?:then|else|do)\s+)docker\s+run\b")
# a vitest or tsc run that prefers the local node (see ui_node in
# encryption/controls_ui.py): it needs node or docker, and ui/shared's dependencies.
_NODE_RUN = re.compile(r"\bthen\s+(?:env\s+(?:\S+=\S+\s+)*)?npm\s+exec\s+--no\s+--\s+(?:vitest|tsc)\b")


class HasCommand(Protocol):
    label: str
    cmd: list[str]


C = TypeVar("C", bound=HasCommand)


def visible_needs(cmd: Sequence[str]) -> frozenset[str]:
    """The lanes a command visibly asks for."""
    if not cmd:
        return frozenset()
    line = " ".join(cmd)
    needs: set[str] = set()
    if cmd[0].endswith("xdg-pw-lock.sh"):
        needs |= {LANE_LOCK, LANE_DOCKER}
    if "retnd_tests_case.sh" in line:
        needs.add(LANE_TESTS)
    if LANE_LOCK not in needs:
        if _NODE_RUN.search(line):
            needs.add(LANE_NODE)
        elif _DOCKER_RUN.search(line):
            needs.add(LANE_DOCKER)
    return frozenset(needs)


def load_pinned(path: Path) -> dict[str, frozenset[str]]:
    out: dict[str, frozenset[str]] = {}
    for line in path.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        lanes, _, label = line.partition("\t")
        out[label] = frozenset(x for x in lanes.split(",") if x)
    return out


def pin_problems(commands: dict[str, list[str]], pinned: dict[str, frozenset[str]]) -> list[str]:
    problems: list[str] = []
    for label, cmd in commands.items():
        seen, want = visible_needs(cmd), pinned.get(label, frozenset())
        if seen != want:
            got, pinned_as = ", ".join(sorted(seen)), ", ".join(sorted(want))
            problems.append(f"{label}: its command needs [{got}] and lanes.txt says [{pinned_as}]")
    for label in pinned:
        if label not in commands:
            problems.append(f"{label}: lanes.txt names it and no control has that label")
    return problems


def node_lane(root: Path, *, has_node: bool, has_docker: bool) -> bool:
    """vitest and tsc need ui/shared's node_modules, and node or a docker daemon to run them."""
    return (root / "ui/shared/node_modules").is_dir() and (has_node or has_docker)


def host_lanes(root: Path) -> dict[str, bool]:
    """Probe this host for each lane."""
    docker = False
    if shutil.which("docker"):
        try:
            docker = subprocess.run(["docker", "info"], capture_output=True, timeout=20, check=False).returncode == 0
        except (OSError, subprocess.TimeoutExpired):
            docker = False
    node = node_lane(root, has_node=shutil.which("node") is not None, has_docker=docker)
    tests_dir = os.environ.get("RETND_TESTS_DIR", "")
    return {
        LANE_LOCK: docker and PW_LOCK.is_file(),
        LANE_DOCKER: docker,
        LANE_NODE: node,
        LANE_TESTS: bool(tests_dir) and Path(tests_dir).is_dir(),
    }


def plan(controls: Sequence[C], pinned: dict[str, frozenset[str]], have: dict[str, bool], *,
         dry_run: bool, recording: bool) -> tuple[list[C], list[tuple[C, list[str]]]]:
    """The controls to run and the ones skipped, each with the lanes it lacks."""
    if dry_run or recording:
        return list(controls), []
    run: list[C] = []
    skipped: list[tuple[C, list[str]]] = []
    for c in controls:
        missing = sorted(lane for lane in pinned.get(c.label, frozenset()) if not have.get(lane, False))
        if missing:
            skipped.append((c, missing))
        else:
            run.append(c)
    return run, skipped


def report(skipped: Sequence[tuple[HasCommand, list[str]]], selftest: str, ledger: str) -> int:
    """Say what was skipped. 0 when the gate took the list, 3 when nobody did."""
    if not skipped:
        return 0
    lines = [f"{selftest} self-test: {c.label} (needs {', '.join(m)})" for c, m in skipped]
    print(f"==> not running {len(skipped)} {selftest} controls this host has no lane for:")
    for line in lines:
        print(f"    {line}")
    if ledger:
        with open(ledger, "a") as fh:
            fh.write("".join(x + "\n" for x in lines))
        return 0
    return 3
