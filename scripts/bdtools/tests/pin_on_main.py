#!/usr/bin/env python3
"""A tests pin must name a commit that is on retnd-tests main (issue #1074).

What it guards against. scripts/e2e/tests-repo.pin once pointed at c75e4dd,
the head of retnd-tests#87. That commit was never on retnd-tests main (the
squash merge is 9584fa3, same tree). bump_tests_pin.py took the sha without
looking at it and the gate happily ran a branch head, so nothing stopped
the pin landing. Both tools now refuse a commit that is not an ancestor of
the tests repository's main.

Everything here runs against fixture git repositories in a temp directory,
read over file://, so there is no network and no real retnd-tests.

Cases:

  A  bump_tests_pin refuses a full sha that is a branch head (not on main).
  B  bump_tests_pin accepts a sha that is on main, and a ref that is main.
  C  bump_tests_pin refuses a ref (a branch name) that resolves to a commit
     that is not on main.
  D  run_tests_repo_gate refuses a hand-edited pin that is not on main, and
     lets one that is on main through the check.
  E  the controls: a bump whose check always passes (planted) MUST accept the
     branch head, so case A is able to fail, and a sha the remote does not
     have is refused rather than waved through.

# PORTED-CHECK HAZARD NOTE

Not a port, but the same rule applies: a check that cannot fail is worse
than none.

the refusal is real
  hazard:      a require_on_main that returns without looking would leave
               cases A, C and D green on any implementation that merely
               prints a message.
  held by:     case E plants exactly that (a no-op override appended to a
               copy of tests_pin.py) and asserts the branch head is then
               ACCEPTED. If the planted copy is also refused, the probe in
               A is not measuring the helper and E fails.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve()
SCRIPTS = HERE.parents[2]
sys.path.insert(0, str(SCRIPTS))

from bdtools.tests import testkit  # noqa: E402

BUMP = "scripts/bdtools/e2e/bump_tests_pin.py"
GATE = "scripts/bdtools/e2e/run_tests_repo_gate.py"
PIN = "scripts/e2e/tests-repo.pin"
# The text both tools put in a refusal for this. Matched case-insensitively
# on a fragment so a rewording of the rest does not break the suite.
REFUSAL = "not on main"


def git(cwd: Path, *args: str) -> str:
    env = dict(
        os.environ,
        GIT_AUTHOR_NAME="t",
        GIT_AUTHOR_EMAIL="t@example.invalid",
        GIT_COMMITTER_NAME="t",
        GIT_COMMITTER_EMAIL="t@example.invalid",
        GIT_CONFIG_GLOBAL="/dev/null",
        GIT_CONFIG_SYSTEM="/dev/null",
    )
    proc = subprocess.run(
        ["git", *args], cwd=str(cwd), env=env, text=True, capture_output=True, check=False
    )
    if proc.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {proc.stderr}")
    return proc.stdout.strip()


def commit(repo: Path, name: str) -> str:
    (repo / name).write_text(name, encoding="utf-8")
    git(repo, "add", name)
    git(repo, "commit", "-q", "-m", name)
    return git(repo, "rev-parse", "HEAD")


class Fixture:
    """A fake retnd-tests: main = A, B; branch `feature` = A, C."""

    def __init__(self, base: Path) -> None:
        self.repo = base / "retnd-tests"
        self.repo.mkdir()
        git(self.repo, "init", "-q", "-b", "main")
        git(self.repo, "config", "uploadpack.allowFilter", "true")
        git(self.repo, "config", "uploadpack.allowAnySHA1InWant", "true")
        self.a = commit(self.repo, "a")
        git(self.repo, "checkout", "-q", "-b", "feature")
        self.c = commit(self.repo, "c")
        git(self.repo, "checkout", "-q", "main")
        self.b = commit(self.repo, "b")
        self.url = "file://" + str(self.repo)
        self.missing = "0" * 39 + "1"


def sandbox_tree(base: Path, name: str, fx: Fixture, pinned: str) -> Path:
    """A copy of scripts/bdtools with its own pin, so the real pin is never touched."""
    root = base / name
    shutil.copytree(
        SCRIPTS / "bdtools",
        root / "scripts" / "bdtools",
        ignore=shutil.ignore_patterns("__pycache__", "*.pyc"),
    )
    pin = root / PIN
    pin.parent.mkdir(parents=True)
    pin.write_text(f"# header kept\nTESTS_REPO_URL={fx.url}\nTESTS_REPO_SHA={pinned}\n", encoding="utf-8")
    return root


def pinned_sha(root: Path) -> str:
    for line in (root / PIN).read_text(encoding="utf-8").splitlines():
        if line.startswith("TESTS_REPO_SHA="):
            return line.split("=", 1)[1]
    return ""


def run_script(root: Path, script: str, *args: str, cache: Path) -> tuple[str, int]:
    env = dict(os.environ, XDG_CACHE_HOME=str(cache), GIT_CONFIG_GLOBAL="/dev/null")
    return testkit.run([sys.executable, str(root / script), *args], cwd=root, env=env, timeout=120)


def plant_always_passes(root: Path) -> None:
    """Append an override that makes the helper a no-op: the check that always passes."""
    target = root / "scripts" / "bdtools" / "e2e" / "tests_pin.py"
    with target.open("a", encoding="utf-8") as handle:
        handle.write("\n\ndef require_on_main(*args, **kwargs):\n    return None\n")


def main() -> int:
    suite = testkit.Suite("pin-on-main (#1074)")
    base = Path(tempfile.mkdtemp(prefix="pin-on-main."))
    try:
        fx = Fixture(base)
        cache = base / "cache"
        cache.mkdir()

        # ------------------------------------------------------- case A
        root = sandbox_tree(base, "a", fx, fx.a)
        out, status = run_script(root, BUMP, fx.c, cache=cache)
        suite.check(
            status != 0 and REFUSAL in out.lower(),
            "A: the bump refuses a full sha that is a branch head and says it is not on main",
            f"A: the bump did not refuse a branch-head sha (exit {status}, needed non-zero and '{REFUSAL}')",
            out,
        )
        suite.assert_eq("A: and the pin file was not rewritten", fx.a, pinned_sha(root))

        # ------------------------------------------------------- case B
        root = sandbox_tree(base, "b1", fx, fx.a)
        out, status = run_script(root, BUMP, fx.b, cache=cache)
        suite.check(
            status == 0,
            "B: the bump accepts a sha that is on main",
            f"B: refused a main sha (exit {status})",
            out,
        )
        suite.assert_eq("B: and the pin now names it", fx.b, pinned_sha(root))

        root = sandbox_tree(base, "b2", fx, fx.c)
        out, status = run_script(root, BUMP, "main", cache=cache)
        suite.check(status == 0, "B: the bump accepts the ref `main`", f"B: refused `main` (exit {status})", out)
        suite.assert_eq("B: and the pin now names main's tip", fx.b, pinned_sha(root))

        root = sandbox_tree(base, "b3", fx, fx.b)
        out, status = run_script(root, BUMP, fx.a, cache=cache)
        suite.check(
            status == 0 and pinned_sha(root) == fx.a,
            "B: an older commit that is an ancestor of main is accepted too",
            f"B: refused an ancestor of main (exit {status})",
            out,
        )

        # ------------------------------------------------------- case C
        root = sandbox_tree(base, "c", fx, fx.a)
        out, status = run_script(root, BUMP, "feature", cache=cache)
        suite.check(
            status != 0 and REFUSAL in out.lower(),
            "C: a ref that resolves to a commit not on main is refused",
            f"C: the bump pinned a branch via its name (exit {status}, needed non-zero and '{REFUSAL}')",
            out,
        )
        suite.assert_eq("C: and the pin file was not rewritten", fx.a, pinned_sha(root))

        # ------------------------------------------------------- case D
        root = sandbox_tree(base, "d1", fx, fx.c)  # hand-edited to a branch head
        out, status = run_script(root, GATE, cache=cache)
        suite.check(
            status != 0 and REFUSAL in out.lower(),
            "D: the gate refuses a hand-edited pin that is not on main, before it fetches or builds anything",
            f"D: the gate did not refuse a non-main pin with the right message (exit {status})",
            out,
        )
        root = sandbox_tree(base, "d2", fx, fx.b)  # on main
        out, status = run_script(root, GATE, cache=cache)
        suite.check(
            REFUSAL not in out.lower(),
            "D: a pin that is on main is not refused for being off main",
            "D: the gate said a main sha is not on main",
            out,
        )

        # ------------------------------------------------------- case E
        root = sandbox_tree(base, "e1", fx, fx.a)
        plant_always_passes(root)
        out, status = run_script(root, BUMP, fx.c, cache=cache)
        suite.check(
            status == 0 and pinned_sha(root) == fx.c,
            "E: control: with the check planted to always pass, the branch head IS accepted, so case A can fail",
            f"E: control: the planted always-pass check still refused the branch head (exit {status}), "
            "so case A is not measuring the helper",
            out,
        )
        root = sandbox_tree(base, "e2", fx, fx.a)
        plant_always_passes(root)
        out, status = run_script(root, GATE, cache=cache)
        suite.check(
            REFUSAL not in out.lower(),
            "E: control: the planted check makes the gate stop refusing too, so case D can fail",
            "E: control: the gate refused even with the check planted to pass",
            out,
        )
        root = sandbox_tree(base, "e3", fx, fx.a)
        out, status = run_script(root, BUMP, fx.missing, cache=cache)
        suite.check(
            status != 0 and pinned_sha(root) == fx.a,
            "E: a sha the remote does not have is refused, not waved through",
            f"E: a sha the remote does not have was accepted (exit {status})",
            out,
        )
    finally:
        shutil.rmtree(base, ignore_errors=True)
    return suite.finish()


if __name__ == "__main__":
    sys.exit(main())
