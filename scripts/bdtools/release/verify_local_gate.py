#!/usr/bin/env python3
"""Does the commit about to be published carry a record of a full green
run of the local gate on exactly its tree? (#1260)

    python3 scripts/bdtools/release/verify_local_gate.py [<commit>]
    python3 scripts/bdtools/release/verify_local_gate.py --uncommitted
    python3 scripts/bdtools/release/verify_local_gate.py --log-sha256 <file>

This is the whole of the `require-release-gate` job in
.github/workflows/release.yml, as a script rather than a `run:` block, so
that three callers can run the same check: the job itself (PUBLISH=true,
refuse or allow), the hosted dry run (PUBLISH=false, report only, exit
0 whatever it finds), and scripts/release/record-local-gate.sh, which
runs it over the record it has just written before it says "recorded"
(--uncommitted: the record and the log are read from the working tree,
the gate tree from HEAD). The runbook's pre-merge line runs it too. One
script, so the writer and the reader cannot drift.

WHAT IT COMPARES. The record names `gate_tree`: the tree hash of the
commit the gate ran on with the record and its log left out, from
scripts/release/gate-tree.sh with git alone. This runs the SAME script
over the commit being published and refuses unless the hashes agree.
Trees, not commits, because the release pull request lands as GitHub's
own merge commit, which no hook ever ran on; its tree is main's tree
whenever release is an ancestor of main, which merge-back-to-main keeps
true. If anything landed on main after the record was written, the
hashes differ and the refusal names both.

WHAT ELSE IT REFUSES. A record of an INCOMPLETE run (a ledger that is
not empty, or a result that is not ok). A schema it does not read, by
name. A log whose hash is not the record's. A recorded Go whose
major.minor is not the `go` line of core/go.mod, the toolchain that
builds the binaries: a patch release of the same Go is the same language
and the same standard library, a different minor is a different
compiler. And an override trailer with no reason.

BYTES, NOT TEXT. The log is hashed as the bytes git holds, read through
`git cat-file blob`, because a bare carriage return from progress output
or a byte that is not UTF-8 is ordinary in twenty-five minutes of tool
output, and a reader that decoded the log first would hash something
else and die on the stray byte. The record is JSON this repository
wrote and is decoded as UTF-8; the commit message is decoded with
replacement, since only its trailers are read.

THE ESCAPE HATCH is a commit trailer, `Release-Gate-Override: <why>`,
with a reason of at least twelve characters, read off the commit being
published and nothing else, and read as a TRAILER: the last paragraph of
the message, as `git interpret-trailers --parse` reads it, so a line
quoted in a body (GitHub's squash message carries the pull request's
description and every commit's body) is not an override. The override
is written to the job summary and the run's annotations, because
`release` is append-only and the trailer is permanent there.

Exit status: 0 allowed (or a dry run, or an override), 1 refused, 2 a
command line or checkout this script cannot work from.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import subprocess
import sys
from pathlib import Path

RECORD = "provenance/local-gate.json"
LOG = "provenance/local-gate.log"
GATE_TREE = "scripts/release/gate-tree.sh"
GO_MOD = "core/go.mod"
PINS = "scripts/ci/pins.env"
SCHEMA = "retnd.local-gate/1"
TRAILER = "Release-Gate-Override"
MIN_REASON = 12
RECORD_HOWTO = "bash scripts/release/record-local-gate.sh"


def log_sha256(data: bytes) -> str:
    """The one hash both ends compute: SHA-256 over the log's bytes."""
    return hashlib.sha256(data).hexdigest()


def git_bytes(*args: str) -> tuple[int, bytes, str]:
    proc = subprocess.run(["git", *args], capture_output=True)
    return proc.returncode, proc.stdout, proc.stderr.decode("utf-8", errors="replace")


def blob(sha: str, path: str) -> bytes | None:
    """The file at `path` in commit `sha`, as bytes, or None when the commit
    has no such file. Read with git rather than from the working tree, so a
    record dropped into the checkout after the push is not the record."""
    code, out, _ = git_bytes("cat-file", "blob", f"{sha}:{path}")
    return out if code == 0 else None


def working_tree_file(path: str) -> bytes | None:
    p = Path(path)
    return p.read_bytes() if p.is_file() else None


def go_pin() -> str | None:
    """The `go` line of core/go.mod, which is the toolchain that builds the
    release's binaries (release.yml's own jobs use go-version-file on it)."""
    try:
        text = Path(GO_MOD).read_text(encoding="utf-8")
    except OSError:
        return None
    m = re.search(r"^go\s+(\d+\.\d+(?:\.\d+)?)\s*$", text, re.MULTILINE)
    return m.group(1) if m else None


def lint_pin() -> str | None:
    """GOLANGCI_LINT_VERSION from scripts/ci/pins.env, the one source the
    gate, the Docker image and this check share."""
    try:
        text = Path(PINS).read_text(encoding="utf-8")
    except OSError:
        return None
    m = re.search(r"^GOLANGCI_LINT_VERSION=v?(\S+)\s*$", text, re.MULTILINE)
    return m.group(1) if m else None


def override_from(message: str) -> tuple[str, bool]:
    """The override reason, and whether a trailer was present with too little
    behind it. Only real trailers count: `git interpret-trailers --parse`
    reads the last paragraph, which is what makes a quoted line in a body a
    body line and not an override."""
    proc = subprocess.run(
        ["git", "interpret-trailers", "--parse"], input=message.encode("utf-8", errors="replace"), capture_output=True
    )
    reason = ""
    short = False
    for raw in proc.stdout.decode("utf-8", errors="replace").splitlines():
        key, sep, value = raw.partition(":")
        if not sep or key.strip().lower() != TRAILER.lower():
            continue
        value = value.strip()
        if len(value) >= MIN_REASON:
            reason = value
        else:
            short = True
    return reason, short


class Report:
    def __init__(self, summary_path: str) -> None:
        self.summary_path = summary_path

    def emit(self, lines: list[str]) -> None:
        text = "\n".join(lines)
        print(text)
        if self.summary_path:
            with open(self.summary_path, "a", encoding="utf-8") as fh:
                fh.write(text + "\n")


def check(sha: str, *, uncommitted: bool) -> tuple[list[str], list[str]]:
    """Every reason the record on `sha` does not hold (empty means it does),
    and what did hold, for the report."""
    findings: list[str] = []
    report: list[str] = []
    read = working_tree_file if uncommitted else (lambda path: blob(sha, path))
    where = "the working tree" if uncommitted else f"the commit being published (`{sha[:12]}`)"

    raw = read(RECORD)
    record: dict[str, object] | None = None
    if raw is None:
        findings.append(f"`{RECORD}` is not in {where}: nothing recorded a gate run on this tree.")
    else:
        try:
            parsed = json.loads(raw.decode("utf-8"))
        except (ValueError, UnicodeDecodeError) as exc:
            findings.append(f"`{RECORD}` is not JSON ({exc}), so there is no record in it to read.")
            parsed = None
        if parsed is not None and not isinstance(parsed, dict):
            findings.append(f"`{RECORD}` is JSON but not an object, so there is no record in it to read.")
            parsed = None
        record = parsed

    if record is not None:
        schema = str(record.get("schema", ""))
        if schema != SCHEMA:
            findings.append(
                f"the record's schema is `{schema or '(missing)'}` and this check reads `{SCHEMA}`. "
                "A format this check does not know is a refusal, not a guess."
            )
        else:
            report.append(f"- schema `{SCHEMA}`")

        result = str(record.get("result", ""))
        ledger = record.get("ledger")
        if result != "ok":
            findings.append(
                f"the record's result is `{result or '(missing)'}`, not `ok`. The recorder never writes a record "
                "for a run that was not ok, so this one was written by hand."
            )
        if not isinstance(ledger, list):
            findings.append("the record carries no ledger list at all, so nothing says the run was complete.")
        elif ledger:
            findings.append(
                "the record's ledger is not empty, so the run it describes was INCOMPLETE and is not merge evidence. "
                "It did not run:\n" + "\n".join(f"    - {item}" for item in ledger)
            )
        else:
            report.append("- result `ok`, ledger empty")

        recorded_tree = str(record.get("gate_tree", ""))
        code, _, _ = git_bytes("rev-parse", "--verify", "--quiet", f"{sha}^{{tree}}")
        if code != 0:
            findings.append(
                f"`{sha}` does not name a commit in this checkout, so there is no tree to compare the record against."
            )
        else:
            proc = subprocess.run(["bash", GATE_TREE, sha], capture_output=True, text=True)
            published_tree = proc.stdout.strip()
            if proc.returncode != 0 or not re.fullmatch(r"[0-9a-f]{40}", published_tree):
                findings.append(
                    f"`{GATE_TREE}` could not compute the gate tree of `{sha[:12]}`:\n```\n"
                    f"{proc.stderr.strip() or proc.stdout.strip() or '(no output)'}\n```"
                )
            elif recorded_tree != published_tree:
                findings.append(
                    f"the record describes a different tree. Recorded `{recorded_tree or '(missing)'}`, and "
                    f"`{sha[:12]}` "
                    f"is `{published_tree}` with the record and its log left out. Something landed between the "
                    f"recorder's run and this cut, or the record was written somewhere else; re-run `{RECORD_HOWTO}` "
                    "on the tip that is being released."
                )
            else:
                report.append(f"- gate_tree `{published_tree}` matches `{sha[:12]}`")

        log_body = read(LOG)
        recorded_log = str(record.get("log_sha256", ""))
        if log_body is None:
            findings.append(
                f"`{LOG}` is not in {where}, so the record's log_sha256 points at nothing a reader can open."
            )
        else:
            actual = log_sha256(log_body)
            if actual != recorded_log:
                findings.append(
                    f"the committed log does not match the record: log_sha256 is `{recorded_log or '(missing)'}` and "
                    f"`{LOG}` hashes to `{actual}`. One of them was edited after the run."
                )
            else:
                report.append(f"- log `{LOG}` hashes to the recorded `{actual[:12]}...`")

        tools = record.get("tools")
        recorded_go = str(tools.get("go", "")) if isinstance(tools, dict) else ""
        pin = go_pin()
        if pin is None:
            findings.append(
                f"`{GO_MOD}` has no `go` line, so this check cannot tell which Go builds the release."
            )
        else:
            pin_minor = ".".join(pin.split(".")[:2])
            got_minor = ".".join(recorded_go.removeprefix("go").split(".")[:2])
            if not recorded_go.startswith("go") or got_minor != pin_minor:
                findings.append(
                    f"the record was made with Go `{recorded_go or '(missing)'}` and `{GO_MOD}` builds with go {pin}. "
                    "Same major.minor is required; a different compiler means the run that was green is not the run "
                    "that ships."
                )
            else:
                report.append(f"- Go `{recorded_go}` matches `{GO_MOD}`'s go {pin} at major.minor")

        recorded_lint = str(tools.get("golangci_lint", "")) if isinstance(tools, dict) else ""
        lint = lint_pin()
        if lint is None:
            findings.append(
                f"`{PINS}` names no GOLANGCI_LINT_VERSION, so this check cannot tell which linter the gate means."
            )
        elif recorded_lint.removeprefix("v") != lint:
            findings.append(
                f"the record was made with golangci-lint `{recorded_lint or '(missing)'}` and `{PINS}` pins {lint}. "
                "Another linter's findings are not the pinned linter's, so the lint that passed is not the lint "
                "that gates."
            )
        else:
            report.append(f"- golangci-lint `{recorded_lint}` is the pinned one")

    return findings, report


def main(argv: list[str]) -> int:
    if len(argv) >= 2 and argv[0] == "--log-sha256":
        try:
            print(log_sha256(Path(argv[1]).read_bytes()))
        except OSError as exc:
            print(f"verify-local-gate: cannot read {argv[1]}: {exc}", file=sys.stderr)
            return 2
        return 0

    uncommitted = False
    sha_arg = "HEAD"
    for arg in argv:
        if arg == "--uncommitted":
            uncommitted = True
        elif arg.startswith("-"):
            print(f"verify-local-gate: unknown option {arg}", file=sys.stderr)
            return 2
        else:
            sha_arg = arg

    publish = os.environ.get("PUBLISH", "false")
    out = Report(os.environ.get("GITHUB_STEP_SUMMARY", ""))

    def refuse(title: str, lines: list[str]) -> int:
        out.emit(["## release gate: REFUSED", "", *lines])
        print(f"::error title={title}::{lines[0]}")
        return 1

    # The inputs first, and an unrecognised one refuses. Reading anything
    # that is not "true" as "this run does not publish" would turn a
    # renamed output into a gate that waves everything through while still
    # reporting green, which is the failure this job exists to stop wearing
    # this job's own uniform.
    if publish not in ("true", "false"):
        return refuse(
            "release gate cannot tell whether this run publishes",
            [
                f"`decide` handed this job publish={publish!r}, which is neither `true` nor `false`.",
                "",
                "Every publishing step reads that output. If it has been renamed or dropped, this job cannot tell a "
                "dry run from a release, so it refuses rather than guess the answer that happens to look safe.",
            ],
        )

    code, sha_bytes, err = git_bytes("rev-parse", "--verify", "--quiet", f"{sha_arg}^{{commit}}")
    if code != 0:
        return refuse(
            "release gate does not know what it is looking at",
            [
                f"`{sha_arg}` does not name a commit in this checkout ({err.strip() or 'no output'}), "
                "so there is no commit to read the record off."
            ],
        )
    sha = sha_bytes.decode("ascii").strip()

    code, message_bytes, err = git_bytes("log", "-1", "--format=%B", sha)
    if code != 0:
        return refuse(
            "release gate could not read the commit",
            [f"`git log -1 {sha}` failed:", "", "```", err.strip() or "(no output)", "```"],
        )
    override, override_short = override_from(message_bytes.decode("utf-8", errors="replace"))

    findings, report = check(sha, uncommitted=uncommitted)
    trouble = [f"I checked the local-gate record on `{sha[:12]}` and found:", ""] + [f"- {f}" for f in findings]
    if report:
        trouble += ["", "What did hold:", "", *report]

    if publish == "false":
        # Report only. The runbook's dry run and the recorder's own round
        # trip both come through here, and neither may be refused by it:
        # the point is to see the answer before the merge that cannot be
        # taken back.
        if not findings:
            out.emit([f"## release gate: DRY RUN, the record on `{sha[:12]}` would have passed", "", *report])
        else:
            out.emit([f"## release gate: DRY RUN, a publishing run would have REFUSED `{sha[:12]}`", "", *trouble])
        return 0

    if not findings:
        out.emit([
            "## release gate: PASSED",
            "",
            f"`{RECORD}` on `{sha[:12]}` records a full green run of scripts/ci-local.sh on exactly this tree.",
            "",
            *report,
        ])
        if override:
            out.emit([
                "",
                f"(A `{TRAILER}:` trailer is on this commit too; it is noise, the record is what allowed this "
                "release.)",
            ])
        return 0

    if override_short and not override:
        return refuse(
            "release gate override carries no reason",
            [
                f"The commit being published has a `{TRAILER}:` trailer with nothing usable after it, and the "
                "local-gate record does not pass.",
                "",
                f"An override needs a reason of at least {MIN_REASON} characters, because the reason is the whole "
                "audit trail. `release` is append-only, so what is written there is what somebody reads a year from "
                "now when they ask why this release skipped its own gate.",
                "",
                *trouble,
            ],
        )

    if override:
        print(f"::warning title=release gate overridden::{override}")
        out.emit([
            "## release gate: OVERRIDDEN",
            "",
            f"The local-gate record does not pass on this commit, and the commit carries a `{TRAILER}:` trailer, so "
            "this run publishes anyway.",
            "",
            f"> {override}",
            "",
            "This is the deliberate escape hatch, and it is not a quiet one. The trailer stays in the history of "
            "`release` for good, so this paragraph is permanent. If the reason for it has since gone away, the next "
            "cut should not carry it.",
            "",
            *trouble,
        ])
        return 0

    return refuse(
        "nothing checked the commit this run would publish",
        [
            "This run publishes a signed image to a public registry, and no full green run of the local gate is "
            "recorded on what it would publish.",
            "",
            *trouble,
            "",
            f"The way through is release prep: on the `main` tip being released, with a clean tree and the hook "
            f"installed, run `{RECORD_HOWTO}`, commit `{RECORD}` and `{LOG}` with the prep, and cut the release from "
            f"that. If this is a real emergency, put `{TRAILER}: <why>` as a trailer of the merge commit and it "
            "publishes, loudly and permanently on the record. docs/release-workflow.md has the rest.",
        ],
    )


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
