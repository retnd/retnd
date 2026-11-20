#!/usr/bin/env python3
"""Which guard scripts does scripts/ci-local.sh actually run? (#1530, #1531, #1533)

A guard is wired when a line of the gate RUNS it as a command: the line starts
with an executor (bash, sh, python3, `npx playwright test -c <config>`, or
ci-local.sh's own gate_selftest_with_ledger, which runs `bash <script>`),
optionally behind env assignments, a quoted lock wrapper such as "$pw_lock",
ci-local.sh's gate_pw_locked or gate_ui_browser "<what>" wrappers, or a
`(cd <dir> &&`; through the Chromium lane (scripts/e2e/ui-browser.sh) the guard
is what the lane runs (`specs <config>`, `run <command>`), and the script it
runs is the guard, without --check-anchors or --dry-run (an anchors-only run is
not a run of the controls). A step title, an
echo, a skip note, a `[ -f ]` or `[[ -f ]]` test, an assignment, shellcheck over
the file and `false && bash ...` all name the path without running it. The line
has to sit inside a gate_seg_* function that a `gate_run_segment` or `gate_run_shard_only`
line puts in a shard that is in scripts/release/gate-shards.txt. A loop
(`for t in a b; do bash scripts/tests/$t.test.sh`) is expanded.

    gate_wiring.py <root>            print "<guard> <TAB> <shards>" for each wired guard
    A path the release job (.github/workflows/release.yml) runs counts as wired in "release.yml".

    gate_wiring.py <root> --guards   print the guards the tree has, one per line
"""

from __future__ import annotations

import os
import re
import shlex
import sys
from pathlib import Path

GUARD_GLOBS = (
    "scripts/*/selftest*.sh",
    "scripts/*/ui-selftest.sh",
    "scripts/*/check-*.sh",
    "scripts/*/verify-*.sh",
    "scripts/*/run-tests-repo-gate.sh",
    "scripts/tests/*.test.sh",
    "ui/shared/e2e/playwright.config.ts",
)

EXECUTOR = re.compile(
    r"^\(?\s*(?:cd\s+(?P<dir>\S+)\s+&&\s+)?"
    r"(?:gate_pw_locked\s+|gate_ui_browser\s+\"[^\"]*\"\s+|env\s+)?"
    r"(?:[A-Za-z_]\w*=\S*\s+)*"
    r"(?:\"?\$\{?\w+\}?\"?\s+)?"
    r"(?:exec\s+)?(?P<prog>bash|sh|python3|npx|gate_selftest_with_ledger)\s+(?P<args>.*)$"
)
NOT_A_RUN = ("--check-anchors", "--dry-run")
LANE = "scripts/e2e/ui-browser.sh"
SEGMENT = re.compile(r"^(gate_seg_\w+)\(\) \{")
RUN_SEGMENT = re.compile(r'^gate_run_segment\s+"([^"]*)"\s+(gate_seg_\w+)')
RUN_ONLY = re.compile(r"^gate_run_shard_only\s+(\S+)\s+(gate_seg_\w+)")
RELEASE_RUN = re.compile(r"^\s*run:\s*(bash|python3)\s+(\S+)")
FOR_LOOP = re.compile(r"^\s*for\s+(\w+)\s+in\s+([^;]+);\s*do\b")


def guards(root: Path) -> list[str]:
    found: set[str] = set()
    for pattern in GUARD_GLOBS:
        found.update(str(p.relative_to(root)) for p in root.glob(pattern))
    return sorted(found)


def runs(command: str) -> str:
    """The repository path a command line runs, or "" when it runs none."""
    m = EXECUTOR.match(command)
    if not m:
        return ""
    try:
        args = shlex.split(m.group("args").rstrip(")").strip())
    except ValueError:
        return ""
    if any(a in NOT_A_RUN for a in args):
        return ""
    script = ""
    if m.group("prog") == "npx":
        if args[:2] == ["playwright", "test"] and "-c" in args[:-1]:
            script = args[args.index("-c") + 1]
    else:
        script = next((a for a in args if not a.startswith("-")), "")
    if not script:
        return ""
    if script == LANE and len(args) >= 3:
        # Through the Chromium lane (#1537, #1543): `specs <config>` runs that
        # config's specs, `run <command...>` runs the command.
        if args[1] == "specs":
            return args[2]
        if args[1] == "run":
            return runs(shlex.join(args[2:]))
    if script.startswith("$") and "/scripts/" in script:
        script = script[script.index("/scripts/") + 1:]
    if m.group("dir"):
        script = os.path.normpath(os.path.join(m.group("dir"), script))
    return script


def manifest(root: Path) -> set[str]:
    text = (root / "scripts/release/gate-shards.txt").read_text()
    return {ln.strip() for ln in text.splitlines() if ln.strip() and not ln.lstrip().startswith("#")}


def wired(root: Path) -> dict[str, set[str]]:
    shards = manifest(root)
    lines = (root / "scripts/ci-local.sh").read_text().splitlines()
    by_function: dict[str, set[str]] = {}
    for line in lines:
        if m := RUN_SEGMENT.match(line):
            by_function.setdefault(m.group(2), set()).update(m.group(1).split())
        elif m := RUN_ONLY.match(line):
            by_function.setdefault(m.group(2), set()).add(m.group(1))

    commands: list[tuple[str, str]] = []  # (function, command line)
    function = ""
    loops: list[tuple[str, list[str]]] = []
    for raw in lines:
        if m := SEGMENT.match(raw):
            function = m.group(1)
            continue
        if raw.startswith("}"):
            function = ""
            continue
        line = raw.split(" #", 1)[0].strip() if not raw.lstrip().startswith("#") else ""
        if not line:
            continue
        if m := FOR_LOOP.match(line):
            loops.append((m.group(1), m.group(2).split()))
            continue
        if line == "done" and loops:
            loops.pop()
            continue
        variants = [line]
        for name, values in loops:
            plain = [val for val in values if "$" not in val]
            variants = [v.replace("${" + name + "}", val).replace("$" + name, val) for v in variants for val in plain]
        commands.extend((function, v) for v in variants)

    out: dict[str, set[str]] = {}
    release = root / ".github/workflows/release.yml"
    if release.is_file():
        # The release job runs its closing checks itself; the gate covers them
        # through the tests that drive them against planted repositories.
        for line in release.read_text().splitlines():
            if m := RELEASE_RUN.match(line):
                out.setdefault(m.group(2), set()).add("release.yml")
    for guard in guards(root):
        for fn, command in commands:
            if fn and runs(command) == guard:
                hit = by_function.get(fn, set()) & shards
                if hit:
                    out.setdefault(guard, set()).update(hit)
    return out


def main(argv: list[str]) -> int:
    root = Path(argv[1])
    if "--guards" in argv[2:]:
        print("\n".join(guards(root)))
        return 0
    for guard, in_shards in sorted(wired(root).items()):
        print(f"{guard}\t{' '.join(sorted(in_shards))}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
