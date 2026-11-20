#!/usr/bin/env python3
"""How much scratch the architecture self-test holds at once (#1580).

The self-test copies the whole tree (about 340 MB) once per mutation case.
It used to keep every copy until the very end, so by the dependency-direction
section nine copies were on disk at once, and all of them together came to
about 7.8 GB. On a Linux docker host the gate container shares the host's
/tmp, MARS's /tmp is tmpfs, and tmpfs pages count against the container's
memory fence and cannot be swapped out, so the run was SIGKILLed (137).

These cases hold the scratch bound without copying anything. The copy step is
`Selftest._copy_tree`, and a stand-in for it makes a small skeleton (only the
files the planted mutations edit) and records the directory. Whether a copy is
still alive is read from the filesystem, never from a counter the self-test
keeps about itself. Every check the self-test would run, and every git or go
command it would shell out to, is stubbed, so nothing here needs go, git or
the linters.

  A  the bound: at most MAX_LIVE copies are on disk at once, and each copy is
     gone before the next one is made, so a copy is removed right after its
     own case. The bound is two rather than one because the deletion-proof
     cases build a git worktree of their copy as well, which is a second tree
     on disk for the length of that case.
  B  nothing is left behind once the body returns: the last copy goes with
     its case, not with the run's final cleanup.
  C  a case that raises, is interrupted (KeyboardInterrupt) or is told to stop
     by the harness (harness.Interrupted, which is what SIGTERM becomes) still
     removes its copy and every earlier one before the exception leaves the
     body.
  D  SIGTERM through main(): main installs the harness's signal handlers, so
     a TERM in the middle of a case unwinds, removes the scratch and exits 143
     instead of dying with the copies still on disk. A second TERM while main
     is removing the scratch (a double Ctrl-C) does not cut that short.
  I  a check that is interrupted takes its whole process group with it, so a
     go or golangci-lint it started is not left writing into a copy that is
     being removed. And a group that is already gone by the time it is killed
     (macOS says EPERM, not ESRCH, when the last process in it has exited
     but is not reaped yet, #1602) still ends the run with 143, not 1.
  E  every `st.mutant(...)` in the body is the context expression of a `with`,
     so a case added later cannot quietly go back to keeping its copy, and no
     `d` is read outside such a block.
  F  the run made a real number of copies, so A to C cannot pass by the body
     having stopped making any.

The other self-tests that plant mutations in whole-tree copies one case after
another (api, compat, conformance, race, retention, discovery) had the same
pile-up, up to about thirty copies each. They hand their copies out through
`selftest_swap.one_at_a_time`, and:

  G  one_at_a_time removes the previous copy when the next one is asked for,
     so one is on disk at a time, and never removes the real tree (which is
     what a --check-anchors run gets instead of a copy), even when the path it
     is handed is spelled differently or is a directory holding the real tree.
  H  in each of those self-tests every whole-tree copy is made through
     one_at_a_time: no call to the copy function sits anywhere but in the
     function handed to it, the copy function is never imported under a bare
     name, and there is exactly one one_at_a_time per self-test.
"""

from __future__ import annotations

import ast
import contextlib
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Callable, Iterator
from unittest import mock

HERE = Path(__file__).resolve()
SCRIPTS = HERE.parents[2]
REPO = SCRIPTS.parent
sys.path.insert(0, str(SCRIPTS))

from bdtools import architecture as arch  # noqa: E402
from bdtools import harness, selftest_swap  # noqa: E402
from bdtools.architecture import selftest as arch_selftest  # noqa: E402
from bdtools.tests import testkit  # noqa: E402

MAX_LIVE = 2
MIN_COPIES = 20

# The files the planted mutations edit in place (append, substitute, rename),
# copied from the real tree so a substitution sees the real text. Every other
# planted file is written under a directory the case makes itself.
SKELETON_FILES = (
    "scripts/architecture/layers.conf",
    "apps/truenas/frontend/platform.ts",
    "ui/shared/src/types/platform.ts",
)
SKELETON_DIRS = ("apps/generic/platform",)

# The case whose check is made to raise or be interrupted. Far enough in that
# several copies have been made before it, and it plants into core/.
TRIP_CASE = "dep-core-to-provider-sdk"


class Recorder:
    """What the stand-in copy step saw."""

    def __init__(self) -> None:
        self.copies: list[Path] = []
        self.peak = 0
        # (the copy being made, the earlier copies still on disk at that moment)
        self.leftovers: list[tuple[str, list[str]]] = []

    def alive(self) -> list[Path]:
        return [p for p in self.copies if p.exists()]


def fake_selftest_class(rec: Recorder) -> type:
    class FakeSelftest(arch_selftest.Selftest):
        def _copy_tree(self, directory: Path) -> None:
            earlier = rec.alive()
            if earlier:
                rec.leftovers.append((directory.name, [p.name for p in earlier]))
            directory.mkdir(parents=True, exist_ok=True)
            for rel in SKELETON_FILES:
                (directory / rel).parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(REPO / rel, directory / rel)
            for rel in SKELETON_DIRS:
                (directory / rel).mkdir(parents=True, exist_ok=True)
            rec.copies.append(directory)
            rec.peak = max(rec.peak, len(earlier) + 1)

    return FakeSelftest


def quiet_run(trip: Callable[[], None] | None = None) -> Callable[..., tuple[int, str]]:
    """A stand-in for the module's `_run`: every check exits 1 saying nothing,
    and the first one run inside TRIP_CASE's copy calls `trip`."""
    state = {"tripped": False}

    def run(cwd: Path, *argv: str) -> tuple[int, str]:
        del argv
        if trip is not None and not state["tripped"] and Path(cwd).name == TRIP_CASE:
            state["tripped"] = True
            trip()
        return 1, ""

    return run


@contextlib.contextmanager
def stubbed(run: Callable[..., tuple[int, str]]) -> Iterator[None]:
    """No subprocess at all: the checks, git and go are all stand-ins, and the
    self-test's own output goes nowhere (it reports a FAIL per control here,
    because every stubbed check fails, and that is not what is measured)."""
    with contextlib.ExitStack() as stack:
        stack.enter_context(mock.patch.object(arch_selftest, "_run", run))
        stack.enter_context(mock.patch.object(harness, "sh", lambda *a, **k: None))
        stack.enter_context(mock.patch.object(harness, "sh_ok", lambda *a, **k: False))
        devnull = stack.enter_context(open(os.devnull, "w", encoding="utf-8"))
        stack.enter_context(contextlib.redirect_stdout(devnull))
        stack.enter_context(contextlib.redirect_stderr(devnull))
        yield


def run_body(rec: Recorder, tmp: Path, trip: Callable[[], None] | None = None) -> BaseException | None:
    st = fake_selftest_class(rec)(REPO, tmp)
    with stubbed(quiet_run(trip)):
        try:
            arch_selftest.body(st)
        except BaseException as exc:  # the cases below are about exactly this
            return exc
    return None


def names(paths: list[Path]) -> str:
    return ", ".join(p.name for p in paths) or "(none)"


def behaviour(s: testkit.Suite) -> None:
    """A to D, against a stand-in for the copy step."""
    work = Path(tempfile.mkdtemp(prefix="arch-selftest-scratch."))
    try:
        # A, B, F: a whole run with nothing going wrong.
        rec = Recorder()
        tmp = work / "clean"
        tmp.mkdir()
        exc = run_body(rec, tmp)
        s.check(exc is None, "A the body runs to its end against the stand-ins",
                f"A the body raised against the stand-ins: {exc!r}")
        s.check(len(rec.copies) >= MIN_COPIES, f"F the run made {len(rec.copies)} copies (at least {MIN_COPIES})",
                f"F the run made only {len(rec.copies)} copies, so the bound below proves little")
        s.check(rec.peak <= MAX_LIVE, f"A at most {MAX_LIVE} tree copies alive at once (peak {rec.peak})",
                f"A {rec.peak} tree copies were alive at once, the bound is {MAX_LIVE} (about 340 MB each)")
        s.check(not rec.leftovers, "A each copy is gone before the next case makes its own",
                "A earlier copies were still on disk when a later case made its copy",
                "\n".join(f"{made}: {', '.join(left)}" for made, left in rec.leftovers[:5]))
        s.check(not rec.alive(), "B no copy is left once the body returns",
                f"B {len(rec.alive())} copies were still on disk after the body returned", names(rec.alive()))

        # C: the case raises, is interrupted, or is told to stop.
        def raise_error() -> None:
            raise RuntimeError("a check blew up mid-case")

        def keyboard() -> None:
            raise KeyboardInterrupt

        def terminated() -> None:
            raise harness.Interrupted(harness.EXIT_TERMINATED, "SIGTERM")

        for label, trip, want in (
            ("raises", raise_error, RuntimeError),
            ("is interrupted (KeyboardInterrupt)", keyboard, KeyboardInterrupt),
            ("is stopped by the harness (SIGTERM)", terminated, harness.Interrupted),
        ):
            rec = Recorder()
            tmp = work / f"trip-{want.__name__}"
            tmp.mkdir()
            exc = run_body(rec, tmp, trip)
            tripped = isinstance(exc, want) and any(p.name == TRIP_CASE for p in rec.copies)
            s.check(tripped, f"C a case that {label} reached {TRIP_CASE} and the exception left the body",
                    f"C a case that {label}: the exception did not come out of {TRIP_CASE} ({exc!r})")
            s.check(tripped and not rec.alive(),
                    f"C a case that {label} leaves no copy behind ({len(rec.copies)} made)",
                    f"C a case that {label} left {len(rec.alive())} copies on disk", names(rec.alive()))

        # D: a real SIGTERM, through main().
        rec = Recorder()
        tmpdir = work / "sigterm"
        tmpdir.mkdir()
        unhandled: list[int] = []
        before = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
        cwd = os.getcwd()
        signal.signal(signal.SIGTERM, lambda signum, _frame: unhandled.append(signum))
        status: int | None = None
        try:
            with stubbed(quiet_run(lambda: os.kill(os.getpid(), signal.SIGTERM))), \
                    mock.patch.object(arch_selftest, "Selftest", fake_selftest_class(rec)), \
                    mock.patch.object(arch, "resolve_toplevel", lambda: REPO), \
                    mock.patch.dict(os.environ, {"TMPDIR": str(tmpdir)}):
                status = arch_selftest.main([])
        finally:
            for sig, handler in before.items():
                signal.signal(sig, handler)
            os.chdir(cwd)
        s.check(not unhandled and status == harness.EXIT_TERMINATED,
                "D a SIGTERM mid-case is handled by main and the run exits 143",
                f"D main did not handle a SIGTERM mid-case (exit {status}, it reached the test's own handler "
                f"{len(unhandled)} time(s)), so a TERM from a timeout kills the run with the copies on disk")
        s.check(not rec.alive() and not any(tmpdir.iterdir()),
                "D nothing is left under TMPDIR after a SIGTERM",
                "D scratch was left under TMPDIR after a SIGTERM", names(list(tmpdir.iterdir())))

        # D, the second signal: TERM mid-case, then TERM again while main is
        # removing its scratch directory.
        rec = Recorder()
        tmpdir = work / "sigterm-twice"
        tmpdir.mkdir()
        real_rmtree = shutil.rmtree
        second: list[Path] = []

        def rmtree_with_a_second_term(path: str | os.PathLike[str], *args: object, **kwargs: object) -> None:
            if Path(path).parent == tmpdir and not second:
                second.append(Path(path))
                os.kill(os.getpid(), signal.SIGTERM)
            real_rmtree(path, ignore_errors=True)

        escaped: BaseException | None = None
        status = None
        before = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
        try:
            with stubbed(quiet_run(lambda: os.kill(os.getpid(), signal.SIGTERM))), \
                    mock.patch.object(arch_selftest, "Selftest", fake_selftest_class(rec)), \
                    mock.patch.object(arch, "resolve_toplevel", lambda: REPO), \
                    mock.patch.object(shutil, "rmtree", rmtree_with_a_second_term), \
                    mock.patch.dict(os.environ, {"TMPDIR": str(tmpdir)}):
                status = arch_selftest.main([])
        except BaseException as exc:  # the case is about exactly this
            escaped = exc
        finally:
            for sig, handler in before.items():
                signal.signal(sig, handler)
            os.chdir(cwd)
        s.check(bool(second) and escaped is None and status == harness.EXIT_TERMINATED
                and not any(tmpdir.iterdir()),
                "D a second SIGTERM during main's final cleanup is ignored: exit 143, nothing left",
                f"D a second SIGTERM during main's final cleanup (sent: {bool(second)}) ended the run with "
                f"{escaped!r} and exit {status}, leaving {names(list(tmpdir.iterdir()))}")

        # I: an interrupted check takes its process group with it.
        interrupted_check(s, work)
        killpg_refused(s, work, cwd)
    finally:
        shutil.rmtree(work, ignore_errors=True)


def killpg_refused(s: testkit.Suite, work: Path, cwd: str) -> None:
    """I, the window #1602 is about: SIGTERM arrives while a real check runs,
    and killing its group raises PermissionError, the way macOS answers for a
    group whose last process has exited and is not reaped yet."""
    rec = Recorder()
    tmpdir = work / "killpg-eperm"
    tmpdir.mkdir()
    real_run = arch_selftest._run
    real_killpg = os.killpg
    refused: list[int] = []

    def killpg_eperm(pgid: int, sig: int) -> None:
        refused.append(pgid)
        with contextlib.suppress(OSError):
            real_killpg(pgid, sig)
        raise PermissionError(1, "Operation not permitted")

    def term_soon() -> None:
        signal.setitimer(signal.ITIMER_REAL, 0.5)
        real_run(Path("/"), "bash", "-c", "sleep 30")

    before = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM)}
    signal.signal(signal.SIGALRM, lambda _signum, _frame: os.kill(os.getpid(), signal.SIGTERM))
    escaped: BaseException | None = None
    status: int | None = None
    try:
        with stubbed(quiet_run(term_soon)), \
                mock.patch.object(arch_selftest, "Selftest", fake_selftest_class(rec)), \
                mock.patch.object(arch, "resolve_toplevel", lambda: REPO), \
                mock.patch.object(os, "killpg", killpg_eperm), \
                mock.patch.dict(os.environ, {"TMPDIR": str(tmpdir)}):
            status = arch_selftest.main([])
    except BaseException as exc:  # the case is about exactly this
        escaped = exc
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        for sig, handler in before.items():
            signal.signal(sig, handler)
        os.chdir(cwd)
    s.check(bool(refused) and escaped is None and status == harness.EXIT_TERMINATED and not any(tmpdir.iterdir()),
            "I a SIGTERM whose killpg is refused with EPERM (macOS, #1602) still exits 143 with nothing left",
            f"I a SIGTERM whose killpg was refused (refused: {bool(refused)}) ended the run with {escaped!r} and "
            f"exit {status}, leaving {names(list(tmpdir.iterdir()))}")


def interrupted_check(s: testkit.Suite, work: Path) -> None:
    pidfile = work / "grandchild.pid"

    def alarm(_signum: int, _frame: object) -> None:
        raise harness.Interrupted(harness.EXIT_TERMINATED, "SIGTERM")

    before = signal.signal(signal.SIGALRM, alarm)
    raised = False
    try:
        signal.setitimer(signal.ITIMER_REAL, 1.0)
        # A check that starts a long-lived child of its own, the way go test
        # and golangci-lint do, and waits on it.
        arch_selftest._run(work, "bash", "-c", f"sleep 300 & echo $! > {pidfile}; wait")
    except harness.Interrupted:
        raised = True
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, before)

    def alive(pid: int) -> bool:
        state = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], stdout=subprocess.PIPE, text=True,
                               check=False).stdout.strip()
        return bool(state) and not state.startswith("Z")

    pid = int(pidfile.read_text().strip()) if pidfile.exists() else 0
    deadline = time.monotonic() + 5
    while pid and alive(pid) and time.monotonic() < deadline:
        time.sleep(0.1)
    left = bool(pid) and alive(pid)
    if left:
        os.kill(pid, signal.SIGKILL)
    s.check(raised and bool(pid) and not left,
            "I an interrupted check's own children go with it",
            f"I an interrupted check left its child running (interrupted: {raised}, child pid {pid or 'unknown'}), "
            "so it can keep writing into a copy that is being removed")


def main() -> int:
    s = testkit.Suite("architecture-selftest-scratch")

    # Without the seam the stand-in would never be called and every case below
    # would make real copies, so its absence is a failure of its own.
    seam = callable(getattr(arch_selftest.Selftest, "_copy_tree", None))
    s.check(seam, "the copy step is its own method (Selftest._copy_tree), so a stand-in can count copies",
            "Selftest has no _copy_tree, so copies cannot be counted without making them for real "
            "(about 340 MB each); A to D did not run")
    if seam:
        behaviour(s)

    # E: every copy the body makes is scoped by a `with`.
    tree = ast.parse(Path(arch_selftest.__file__).read_text(encoding="utf-8"))
    body_fn = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "body")
    scoped = {
        id(item.context_expr)
        for node in ast.walk(body_fn)
        if isinstance(node, ast.With)
        for item in node.items
    }
    calls = [
        node for node in ast.walk(body_fn)
        if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr == "mutant"
    ]
    loose = [node.lineno for node in calls if id(node) not in scoped]
    s.check(bool(calls) and not loose,
            f"E all {len(calls)} st.mutant(...) calls in body() are scoped by a with",
            f"E {len(loose)} of {len(calls)} st.mutant(...) calls in body() are not the subject of a with, "
            f"so their copies outlive their case (lines {', '.join(map(str, loose[:10]))})")
    inside: set[int] = set()
    for node in ast.walk(body_fn):
        if isinstance(node, ast.With) and any(id(item.context_expr) in {id(c) for c in calls} for item in node.items):
            for stmt in node.body:
                inside.update(id(sub) for sub in ast.walk(stmt))
    outside = [
        node.lineno for node in ast.walk(body_fn)
        if isinstance(node, ast.Name) and node.id == "d" and isinstance(node.ctx, ast.Load) and id(node) not in inside
    ]
    s.check(bool(calls) and not outside, "E no d is read outside the with block that made its copy",
            f"E d is read outside a with st.mutant(...) block, after its copy is gone (lines "
            f"{', '.join(map(str, outside[:10]))})")

    one_at_a_time(s)
    return s.finish()


# The self-tests whose cases each make a whole-tree copy in turn. All of them
# copy with selftest_swap.mutant, except api, which has its own _mutant.
SWAP_USERS = ("api", "compat", "conformance", "race", "retention", "discovery")


def one_at_a_time(s: testkit.Suite) -> None:
    helper = getattr(selftest_swap, "one_at_a_time", None)
    if not callable(helper):
        s.bad("G selftest_swap has no one_at_a_time, so the swap self-tests keep every copy until they end")
    else:
        work = Path(tempfile.mkdtemp(prefix="arch-selftest-one-at-a-time."))
        try:
            root = work / "root"
            root.mkdir()

            def make(name: str) -> Path:
                d = work / name
                d.mkdir()
                return d

            next_copy = helper(make, root)
            made = [next_copy(f"case-{i}") for i in range(5)]
            alive = [p.name for p in made if p.exists()]
            s.check(alive == ["case-4"], "G one_at_a_time keeps only the latest copy on disk",
                    f"G one_at_a_time left {alive} on disk after five cases, want only case-4")
            dry = helper(lambda name: root, root)
            dry("a")
            dry("b")
            s.check(root.is_dir(), "G one_at_a_time never removes the real tree a dry run hands back",
                    "G one_at_a_time removed the real tree")
            alias = root / ".." / "root"
            spelled = helper(lambda name: alias, root)
            spelled("a")
            spelled("b")
            s.check(root.is_dir(), "G nor the real tree spelled another way (root/../root)",
                    "G one_at_a_time removed the real tree when it was handed root/../root")
            selftest_swap.discard(work, root)
            s.check(root.is_dir(), "G discard never removes a directory that holds the real tree",
                    "G discard removed a directory holding the real tree")
        finally:
            shutil.rmtree(work, ignore_errors=True)

    for domain in SWAP_USERS:
        path = SCRIPTS / "bdtools" / domain / "selftest.py"
        tree = ast.parse(path.read_text(encoding="utf-8"))
        handed: set[int] = set()
        for node in ast.walk(tree):
            if (isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute)
                    and node.func.attr == "one_at_a_time"):
                for arg in node.args:
                    handed.update(id(sub) for sub in ast.walk(arg))
        copies = [
            node for node in ast.walk(tree)
            if isinstance(node, ast.Call) and (
                (isinstance(node.func, ast.Attribute) and node.func.attr == "mutant"
                 and isinstance(node.func.value, ast.Name) and node.func.value.id == "selftest_swap")
                or (isinstance(node.func, ast.Name) and node.func.id == "_mutant")
            )
        ]
        loose = [node.lineno for node in copies if id(node) not in handed]
        helpers = [
            node for node in ast.walk(tree)
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr == "one_at_a_time"
        ]
        bare = [
            node.lineno for node in ast.walk(tree)
            if isinstance(node, ast.ImportFrom) and (node.module or "").endswith("selftest_swap")
            and any(alias.name in ("mutant", "one_at_a_time") for alias in node.names)
        ]
        s.check(len(helpers) == 1 and not bare,
                f"H {domain}: one one_at_a_time, and no bare import of the copy function",
                f"H {domain}: {len(helpers)} one_at_a_time calls (want 1), bare imports of selftest_swap's "
                f"mutant or one_at_a_time at lines {bare}")
        s.check(bool(copies) and not loose,
                f"H {domain}: every whole-tree copy goes through one_at_a_time",
                f"H {domain}: {len(loose)} of {len(copies)} whole-tree copies are made outside one_at_a_time "
                f"and kept until the run ends (lines {', '.join(map(str, loose[:8]))})")


if __name__ == "__main__":
    sys.exit(main())
