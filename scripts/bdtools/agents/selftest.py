#!/usr/bin/env python3
"""Positive controls for EPIC U's guards (#1343, #1353).

docs/conformance/epic-u-matrix.md ties every claim the epic makes to a test,
and a row only says `PASS` once the mutation it names has been run and watched
to fail. This file is where those mutations live, so "run and watched to fail"
is a command anybody can repeat rather than a sentence in a table. It has the
shape of scripts/bdtools/encryption/selftest.py (#1366), which it copies.

Each control plants one violation in a real product file, runs the narrowed
test the matrix row cites, and must see it fail AND name that test: a mutant
that broke the build for an unrelated reason does not count as a catch. The
file is then put back and the same narrowed test has to pass, so a test that
was red before anything was planted cannot read as a catch either.

core/tests/agentgate/matrix_test.go reads this file: a `PASS` row cites a
control here by its label, verbatim, in double quotes, beside the date it was
run, and a label that is not in this file is refused.

Every mutation is anchored to a verbatim copy of product source. An anchor
that no longer matches is STALE ANCHOR: the control is skipped rather than run
against a tree with nothing planted in it. `--check-anchors` checks every
anchor against the real tree, building nothing.

  --check-anchors   check every anchor, build nothing
  --only TEXT       run only controls whose label or row id contains TEXT
  --race            run each narrowed test with -race
  --record FILE     append one dated line per control to FILE: the label, its
                    rows, the verdict, the red excerpt, the green verdict, and
                    the commit (RETND_SELFTEST_COMMIT names it when the copy
                    has no .git)
"""

from __future__ import annotations

import datetime
import os
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness, selftest_swap

PROGRAM = "agents selftest"

PLANT = "// PLANTED VIOLATION (scripts/bdtools/agents/selftest.py): "


@dataclass
class Control:
    """One planted violation. `label` is what a matrix row quotes; `rows`
    are the row ids it proves; `plants` are (file, old, new) swaps; `pkg` is
    the Go package under core/; `names` are the tests that must fail (a
    `Parent/sub` name matches that subtest). `build` is a needle instead:
    the plant must stop the package building, naming it."""

    label: str
    rows: tuple[str, ...]
    plants: list[tuple[str, str, str]]
    pkg: str
    names: list[str] = field(default_factory=list)
    build: str = ""
    timeout: str = "15m"
    module: str = "core"
    # A guard that is not a Go test (a shell checker, the contrast script, a
    # Playwright spec): `cmd` runs from the copy's root (or `module`, when it
    # is not core), and every string in `needles` must appear in its output
    # for the red run to count. `links` are paths symlinked from the real tree
    # into the copy first (node_modules, which git does not list).
    cmd: list[str] = field(default_factory=list)
    needles: list[str] = field(default_factory=list)
    links: list[str] = field(default_factory=list)


# The controls live in controls_*.py beside this file, one file per area,
# each defining CONTROLS with Control and PLANT in scope.
CONTROLS: list[Control] = []


# Each file defines CONTROLS and is folded in here. core/tests/agentgate reads
# them for labels. RETND_SELFTEST_FILES (comma separated stems, e.g.
# "controls_tokens") loads only those, so a half-written file beside them
# cannot stop a run of the others.
_only_files = [x for x in os.environ.get("RETND_SELFTEST_FILES", "").split(",") if x]
for _extra in sorted(Path(__file__).resolve().parent.glob("controls_*.py")):
    if _only_files and _extra.stem not in _only_files:
        continue
    _ns: dict[str, object] = {"Control": Control, "PLANT": PLANT}
    exec(compile(_extra.read_text(), str(_extra), "exec"), _ns)
    CONTROLS.extend(_ns.get("CONTROLS", []))  # type: ignore[arg-type]


# ---------------------------------------------------------------- the run


EXPLORE = False


def gate(work: Path, c: Control, race: bool) -> tuple[int, str]:
    """The narrowed run, never raising on its own non-zero status: an
    expected failure has to become a value this file inspects."""
    env = dict(os.environ)
    env["GOWORK"] = "off"
    if c.cmd:
        cwd = work if c.module == "core" else work / c.module
        proc = harness.sh(c.cmd, check=False, capture=True, cwd=cwd, env=env)
        return proc.returncode, (proc.stdout or "") + (proc.stderr or "")
    tops = sorted({n.split("/", 1)[0] for n in c.names})
    pattern = "^(" + "|".join(re.escape(t) for t in tops) + ")$" if tops else "^$"
    if EXPLORE:
        pattern = "."
    argv = ["go", "test", "-count=1", "-timeout", c.timeout]
    if race:
        argv.append("-race")
    argv += ["-run", pattern, c.pkg]
    proc = harness.sh(argv, check=False, capture=True, cwd=work / c.module, env=env)
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def caught(c: Control, code: int, out: str) -> tuple[bool, str]:
    """Whether a red run is a catch: the build failure the control names, or
    every named test reported as failing."""
    if code == 0:
        return False, "the narrowed test PASSED against a planted violation"
    if c.cmd:
        missing = [n for n in c.needles if n not in out]
        if missing:
            return False, "it failed, but never said " + ", ".join(missing)
        line = next((x.strip() for x in out.splitlines() if c.needles and c.needles[0] in x), "")
        return True, line[:400]
    if c.build:
        if c.build in out and ("build failed" in out or "setup failed" in out or "FAIL" in out):
            line = next((x.strip() for x in out.splitlines() if c.build in x), "")
            return True, line[:300]
        return False, f"the build did not fail naming {c.build}"
    if "[build failed]" in out or "[setup failed]" in out:
        return False, "the package did not build, so nothing was caught"
    missing = [n for n in c.names if f"--- FAIL: {n}" not in out]
    if missing:
        return False, "it failed, but never named " + ", ".join(missing)
    lines = [x.strip() for x in out.splitlines() if x.strip().startswith("--- FAIL:")]
    detail = next((x.strip() for x in out.splitlines() if "_test.go:" in x), "")
    return True, "; ".join(lines)[:400] + (f" | {detail[:300]}" if detail else "")


def selected(c: Control, only: list[str]) -> bool:
    return not only or any(o in c.label or o in c.rows for o in only)


def commit_of(root: Path) -> str:
    proc = harness.sh(["git", "rev-parse", "--short=12", "HEAD"], check=False, capture=True, cwd=root)
    dirty = harness.sh(["git", "status", "--porcelain", "--untracked-files=no"], check=False, capture=True, cwd=root)
    head = os.environ.get("RETND_SELFTEST_COMMIT") or (proc.stdout or "").strip() or "unknown"
    return head + ("+dirty" if (dirty.stdout or "").strip() else "")


def mutant(root: Path, tmp: Path, name: str, *, dry_run: bool) -> Path:
    """selftest_swap.mutant, except that a file git lists and the tree does not
    have is skipped when it is a .env* template: scripts/ci-docker.sh --host
    leaves those out of the copy on purpose (credentials), and git still lists
    container/.env.example."""
    if dry_run:
        return root
    dest = tmp / name
    dest.mkdir(parents=True, exist_ok=True)
    listed = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        cwd=root, stdout=subprocess.PIPE, check=True,
    ).stdout
    for raw in listed.split(b"\0"):
        if not raw:
            continue
        rel = raw.decode("utf-8", "surrogateescape")
        src = root / rel
        if not os.path.lexists(src) and Path(rel).name.startswith(".env"):
            continue
        dst = dest / rel
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(src, dst, follow_symlinks=False)
    return dest


def body(root: Path, dry_run: bool, only: list[str], race: bool, record: Path | None) -> int:
    tally = selftest_swap.Tally()
    controls = [c for c in CONTROLS if selected(c, only)]
    labels = [c.label for c in CONTROLS]
    dupes = sorted({x for x in labels if labels.count(x) > 1})
    if dupes:
        harness.die("two controls share a label, so a matrix row citing it proves neither", *dupes)
    host = os.uname().nodename
    commit = commit_of(root)

    with tempfile.TemporaryDirectory(prefix="retnd-agents-selftest.") as tmp_name:
        tmp = Path(tmp_name)
        tracker = selftest_swap.AnchorTracker(dry_run=dry_run, root=root, tmp=tmp)
        work = mutant(root, tmp, "work", dry_run=dry_run)
        if not dry_run:
            for rel in sorted({x for c in controls for x in c.links}):
                if (root / rel).exists() and not (work / rel).exists():
                    (work / rel).parent.mkdir(parents=True, exist_ok=True)
                    (work / rel).symlink_to(root / rel)

        for c in controls:
            print()
            print(f"==> {c.label} ({', '.join(c.rows)})")
            saved = {rel: (work / rel).read_bytes() for rel, _, _ in c.plants if (work / rel).exists()}
            try:
                for rel, old, new in c.plants:
                    tracker.swap(work / rel, old, new)
                if tracker.stale_verdict(c.label) or tracker.anchors_only(c.label):
                    continue
                code, out = gate(work, c, race)
                if EXPLORE:
                    fails = [x.strip() for x in out.splitlines() if "--- FAIL" in x or "build failed" in x or x.startswith("FAIL")]
                    print("EXPLORE " + c.label + ":\n    " + "\n    ".join(fails[:40]))
                    if "build failed" in out:
                        print("\n".join(out.splitlines()[:15]))
                    continue
                ok, why = caught(c, code, out)
            finally:
                if not dry_run:
                    for rel, data in saved.items():
                        (work / rel).write_bytes(data)
            if not ok:
                tally.bad(f"{c.label}. {why}.", out[-6000:])
                verdict, green = "NOT CAUGHT", "-"
            else:
                gcode, gout = gate(work, c, race)
                if gcode != 0:
                    tally.bad(f"{c.label}. The narrowed test FAILED on the unplanted tree, so its red means nothing.", gout[-6000:])
                    verdict, green = "RED ON A CLEAN TREE", "red"
                else:
                    tally.ok(f"(caught): {c.label}", why)
                    verdict = "caught"
                    green = next((x.strip() for x in gout.splitlines() if x.startswith("ok")), "ok")
            if record is not None:
                stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
                with record.open("a") as fh:
                    fh.write(
                        f"{stamp}\t{host}\t{commit}\t{' '.join(c.rows)}\t{c.label}\t{verdict}\t"
                        + (" ".join(c.cmd) if c.cmd else f"go test -count=1{' -race' if race else ''} -run <{','.join(c.names) or c.build}> {c.pkg}") + "\t"
                        f"RED: {why}\tGREEN: {green}\n"
                    )

        tracker.stale_summary()
        stale = tracker.stale_count
        checked = tracker.anchors_checked
        shutil.rmtree(tmp / "work", ignore_errors=True)

    total = tally.passed + tally.failed + stale
    if tally.failed or stale:
        print(file=sys.stderr)
        print(
            f"FAIL: {tally.failed + stale} of {total} agents controls did not behave as required "
            f"({tally.failed} reached the wrong verdict, {stale} could not plant their violation at all).",
            file=sys.stderr,
        )
        return harness.EXIT_FAILED
    print()
    if dry_run:
        print(f"OK: all {checked} agents mutation anchors still name code that is in this tree.")
    else:
        print(f"OK: all {tally.passed} agents controls went red naming their test and green again unplanted.")
    return harness.EXIT_OK


def main(argv: list[str]) -> int:
    harness.set_program(PROGRAM)
    harness.install_signal_handlers()
    rest: list[str] = []
    only: list[str] = []
    race = False
    record: Path | None = None
    it = iter(argv)
    for arg in it:
        if arg == "--only":
            only.append(next(it, ""))
        elif arg == "--explore":
            global EXPLORE
            EXPLORE = True
        elif arg == "--race":
            race = True
        elif arg == "--record":
            record = Path(next(it, "")).resolve()
        else:
            rest.append(arg)
    dry_run = selftest_swap.parse_selftest_args(rest, "selftest.py")
    root = harness.repo_root(Path(__file__))
    return harness.finish(lambda: body(root, dry_run, only, race, record))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
