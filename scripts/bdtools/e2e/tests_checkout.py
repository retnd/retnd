#!/usr/bin/env python3
"""The pinned retnd-tests checkout for a gate shard's self-tests (#1548).

Three encryption self-test controls run a case of retnd-tests against a retnd
built from a planted copy (scripts/bdtools/encryption/retnd_tests_case.sh), so
they need RETND_TESTS_DIR, a checkout at the sha scripts/e2e/tests-repo.pin
names, and RETND_COMMIT. scripts/ci-local.sh's selftest-encryption step runs
this and exports both.

    tests_checkout.py     prints the checkout's path on its last line

Where it comes from. The same pin and URL the e2e gate uses, so there is one
place that says which tests a revision is gated against. The pin has to be on
retnd-tests main (the e2e gate's own #1074 check, reused), the fetch is one
shallow `git fetch` of that sha, and the result is cached per sha under
$XDG_CACHE_HOME/retnd-tests-gate/<sha>.src, which is the shard container's own
cache volume. It is a different directory from the e2e gate's <sha> one on
purpose: that one is only trusted once its npm install finished, and this one
has no install.

Credentials. None. retnd-tests is public and is fetched anonymously over
https, with git's prompt off, so a host with no login fails the fetch instead
of waiting on a password. A pin URL that carries a user or token is refused,
so a credential cannot be put on disk by way of the pin, and nothing here reads
or writes a token.

Exit statuses: 0 the path is printed; 1 the pin is wrong (not a full sha, a
URL with credentials, not on main, a sha the remote does not have); 3 the
remote cannot be reached or a git command ran out of time (the gate ledgers
that, the three controls then skip by name and the shard ends INCOMPLETE).
"""

from __future__ import annotations

import os
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness
from bdtools.e2e import tests_pin

PROGRAM = "tests checkout"
# Every git call is bounded, the on-main check included. A hung remote is INCOMPLETE, not a hung shard.
# RETND_TESTS_CHECKOUT_GIT_TIMEOUT (seconds) is for the tests, which use a short one.
GIT_TIMEOUT = float(os.environ.get("RETND_TESTS_CHECKOUT_GIT_TIMEOUT", "600"))
# No prompt, no askpass, and no user, global or system git config, so an insteadOf rewrite to ssh or a
# credential helper cannot apply: the fetch is anonymous https or it fails.
GIT_ENV = {
    "GIT_TERMINAL_PROMPT": "0",
    "GIT_ASKPASS": "true",
    "GIT_CONFIG_GLOBAL": "/dev/null",
    "GIT_CONFIG_SYSTEM": "/dev/null",
    "GIT_CONFIG_NOSYSTEM": "1",
}


def read_the_pin(root: Path) -> tuple[str, str]:
    pin_file = root / tests_pin.PIN_RELATIVE
    if not pin_file.is_file():
        harness.die(f"there is no {tests_pin.PIN_RELATIVE}, so I do not know which tests to check out.")
    values = tests_pin.read_pin(pin_file)
    url, sha = values.get("TESTS_REPO_URL", ""), values.get("TESTS_REPO_SHA", "")
    if not tests_pin.SHA.match(sha):
        harness.die(f"{tests_pin.PIN_RELATIVE} does not carry a full 40-character commit sha.")
    parts = urlsplit(url)
    # https only. file:// is a seam for the unit cases, named by the test and nothing else.
    schemes = {"https", *filter(None, os.environ.get("RETND_TESTS_CHECKOUT_TEST_SCHEMES", "").split(","))}
    if not url or parts.scheme not in schemes or parts.username or parts.password or "@" in parts.netloc:
        harness.die(
            f"{tests_pin.PIN_RELATIVE} has a TESTS_REPO_URL that is empty, is not https, or carries credentials.",
            "retnd-tests is public and is fetched anonymously over https; an ssh or scp URL would use a key on the "
            "host, and a token in the pin would land on disk and in git.",
        )
    return url, sha


def git(args: list[str], cwd: Path | None = None, check: bool = True) -> subprocess.CompletedProcess[str]:
    env = dict(os.environ, **GIT_ENV)
    return harness.sh(["git", *args], cwd=cwd, env=env, timeout=GIT_TIMEOUT, check=check)


def verified(checkout: Path, sha: str) -> bool:
    """A cache hit is only a hit when the directory is a git checkout whose HEAD is the pinned sha."""
    if not (checkout / ".complete").is_file():
        return False
    head = git(["rev-parse", "HEAD"], cwd=checkout, check=False)
    return head.returncode == 0 and head.stdout.strip() == sha


def body(root: Path) -> int:
    url, sha = read_the_pin(root)
    harness.require_tools("git")
    try:
        reachable = git(["ls-remote", "--exit-code", url, "HEAD"], check=False).returncode == 0
    except harness.Failure as failure:
        harness.cannot_run(failure.message, *failure.details)
    if not reachable:
        harness.cannot_run(f"{url} cannot be reached, so the retnd-tests checkout cannot be made.")
    try:
        tests_pin.require_on_main(url, sha, timeout=GIT_TIMEOUT, env=dict(os.environ, **GIT_ENV))
    except tests_pin.Unreachable as unreachable:
        harness.cannot_run(unreachable.message, *unreachable.details)

    cache_home = os.environ.get("XDG_CACHE_HOME") or str(Path.home() / ".cache")
    cache_root = Path(cache_home) / "retnd-tests-gate"
    checkout = cache_root / f"{sha}.src"
    if not verified(checkout, sha):
        print(f"==> {PROGRAM}: fetching retnd-tests at {sha[:12]}", file=sys.stderr, flush=True)
        cache_root.mkdir(parents=True, exist_ok=True)
        # Built beside the final place and renamed in once whole, so an
        # interrupted fetch is never mistaken for a checkout. A losing or
        # failed scratch directory is left where it is: this path is outside
        # any workspace directory, and leaking one is cheaper than a delete.
        if checkout.exists():
            # Something sits where the checkout goes and is not the pinned tree. Move it aside inside the cache
            # (nothing is deleted) and fetch into a fresh directory.
            os.rename(str(checkout), str(cache_root / f"{sha}.src.rejected.{os.getpid()}.{int(time.time())}"))
        scratch = cache_root / f"scratch-src.{os.getpid()}.{int(time.time())}"
        scratch.mkdir()
        try:
            git(["init", "-q", "."], cwd=scratch)
            git(["remote", "add", "origin", url], cwd=scratch)
            git(["fetch", "-q", "--depth", "1", "origin", sha], cwd=scratch)
            git(["checkout", "-q", "--detach", "FETCH_HEAD"], cwd=scratch)
        except (harness.CommandFailed, harness.Failure) as failed:
            harness.cannot_run(
                f"the fetch of retnd-tests at {sha[:12]} did not finish: {failed}",
                f"Scratch directory left at {scratch}.",
            )
        (scratch / ".complete").write_text("", encoding="utf-8")
        try:
            os.rename(str(scratch), str(checkout))
        except OSError:
            pass
        if not verified(checkout, sha):
            harness.die(f"could not publish the checkout to {checkout} (scratch left at {scratch}).")
    print(checkout)
    return harness.EXIT_OK


def main() -> int:
    harness.set_program(PROGRAM)
    harness.install_signal_handlers()
    root = harness.repo_root(Path(__file__))
    return harness.finish(lambda: body(root))


if __name__ == "__main__":
    sys.exit(main())
