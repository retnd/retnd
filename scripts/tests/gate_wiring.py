#!/usr/bin/env python3
"""Which guard scripts does scripts/ci-local.sh actually run? (#1530, #1531, #1533)

A guard is wired when a line of the gate that runs a command (not a comment, not
an echo, a skip note, a step title or an `if [ -f ... ]` test) names its path,
inside a gate_seg_* function that a `gate_run_segment` or `gate_run_shard_only`
line puts in a shard that is in scripts/release/gate-shards.txt. A loop
(`for t in a b; do bash scripts/tests/$t.test.sh`) is expanded.

    gate_wiring.py <root>            print "<guard> <TAB> <shards>" for each wired guard
    A path the release job (.github/workflows/release.yml) runs counts as wired in "release.yml".

    gate_wiring.py <root> --guards   print the guards the tree has, one per line
"""

from __future__ import annotations

import re
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

NOT_A_COMMAND = ("echo", "printf", "gate_note_skip", "gate_step", "[", "if", "case", "elif", "while")
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
        if line.split()[0] in NOT_A_COMMAND:
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
            if guard in command and fn:
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
