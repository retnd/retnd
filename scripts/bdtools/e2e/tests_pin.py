#!/usr/bin/env python3
"""Reading `scripts/e2e/tests-repo.pin`, for the two scripts that read it.

Both `run_tests_repo_gate` and `bump_tests_pin` used to get at the pin
with `. "$pin_file"`, which is to say by EXECUTING it as shell. That
worked because the file is two assignments and two hundred lines of
comment explaining them, and it is exactly the arrangement this parser
replaces: sourcing a file gives it the running script's privileges to say
what the pin is worth having, and the pin's whole job is to be one
reviewable line rather than a program.

Two real callers in the same change, which is the bar
`bdtools.harness`'s own docblock sets for a shared helper existing at
all. It is here rather than in the harness because the harness has no
business knowing what a pin is.

Ported from `scripts/e2e/run-tests-repo-gate.sh` and
`scripts/e2e/bump-tests-pin.sh` under #672 / #662.
"""

from __future__ import annotations

import re
import tempfile
from pathlib import Path

from bdtools import harness

# The path both scripts name, relative to the repository root, as a string
# because it appears in operator-visible refusals in exactly this form.
PIN_RELATIVE = "scripts/e2e/tests-repo.pin"

# A full 40-character commit sha, and nothing shorter. The gate refuses a
# pin that is not one of these and `bump_tests_pin` uses the same shape to
# tell an exact commit from a ref to resolve, so the two cannot disagree
# about what "a sha" is.
SHA = re.compile(r"^[0-9a-f]{40}$")

# `KEY=VALUE`, anchored, with everything else -- comments, blank lines, and
# any shell that a sourced file would have RUN -- ignored rather than
# interpreted.
_ASSIGNMENT = re.compile(r"^([A-Za-z_][A-Za-z0-9_]*)=(.*)$")


def read_pin(path: Path) -> dict[str, str]:
    """The pin's assignments, last one winning.

    Last one winning is `.`-sourcing's own behaviour, kept because the pin
    file's header has been rewritten in place a dozen times and a stray
    earlier assignment must not quietly outrank the line at the bottom.

    A quoted value is unquoted, once, on the outside only: the shell would
    have done that, and a pin whose sha arrived wrapped in the quotes would
    otherwise fail the 40-character check with a message about the sha
    rather than about the quotes.
    """
    values: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        match = _ASSIGNMENT.match(line)
        if match is None:
            continue
        value = match.group(2).strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
            value = value[1:-1]
        values[match.group(1)] = value
    return values


def require_on_main(url: str, sha: str, branch: str = "main") -> None:
    """Refuse unless `sha` is an ancestor of the tests repository's `branch` (#1074).

    A pin names a reviewed, merged suite state. A branch head is none of
    those: it can be force-pushed, deleted, or squashed into a different
    sha, and the gate runs it just as happily as a merged one, which is how
    `c75e4dd` (the head of retnd-tests#87, never on main) got pinned.

    The bump tool and the gate both call this, so the two cannot disagree
    about what "on main" means. It fetches into a throwaway git directory
    with `--filter=tree:0`, which brings commits and no file contents, so
    the cost is a few kilobytes rather than a clone, and asks git
    `merge-base --is-ancestor`. Status 1 from that is the answer "no"; any
    other non-zero status is git failing, which is reported as that and
    never read as either answer.
    """
    with tempfile.TemporaryDirectory(prefix="retnd-pin-check.") as tmp:
        git = ["git", "-C", tmp]
        harness.sh([*git, "init", "-q", "--bare"])
        fetched_main = harness.sh(
            [
                *git,
                "fetch",
                "-q",
                "--no-tags",
                "--filter=tree:0",
                url,
                f"+refs/heads/{branch}:refs/remotes/origin/{branch}",
            ],
            check=False,
        )
        if fetched_main.returncode != 0:
            harness.die(
                f"could not fetch {branch} from {url} to check that {sha} is on it.",
                *(fetched_main.stderr or "").strip().splitlines()[-5:],
            )
        fetched_sha = harness.sh(
            [*git, "fetch", "-q", "--no-tags", "--filter=tree:0", url, sha], check=False
        )
        if fetched_sha.returncode != 0:
            harness.die(
                f"{url} has no commit {sha}, so it cannot be on {branch}.",
                "Pin a commit that has been merged to main and is reachable from it.",
            )
        verdict = harness.sh(
            [*git, "merge-base", "--is-ancestor", sha, f"refs/remotes/origin/{branch}"], check=False
        )
        if verdict.returncode == 0:
            return
        if verdict.returncode == 1:
            harness.die(
                f"{sha} is not on {branch} in {url} (not on main means not an ancestor of {branch}).",
                "A branch head can be force-pushed, deleted or squashed into a different sha, and the gate",
                "would run it anyway. Pin the commit that merged to main instead (for a squash merge that is",
                "the squash commit, not the branch head it came from).",
            )
        harness.die(
            f"git could not decide whether {sha} is an ancestor of {branch} (status {verdict.returncode}).",
            *(verdict.stderr or "").strip().splitlines()[-5:],
        )
