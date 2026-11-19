"""Reading Playwright's JSON report for the first-run specs (retnd#1049).

The first-run cases in retnd-tests (`*.1049.spec.ts`) are about a deployment
nobody has configured, so each opens with `needs(isFresh(facts), ...)`. On
the default, seeded rig that guard skips the case, and a skip is the same
colour as a pass: Playwright exits 0 for a run in which every case skipped.
That is how these cases went unrun. The gate therefore runs them against a
`--fresh` rig and reads the report instead of the exit status.

This module is only the judging. It takes the parsed report and returns the
reasons the run does not count, so it can be tested without Docker, a
browser or a rig. `run_tests_repo_gate.fresh_first_run_half` is what drives
the rig and calls it.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

# The file-name marker the gate selects on, and the Playwright path filter
# that matches it (a regular expression, so the dots are not escaped).
SPEC_SUFFIX = ".1049.spec.ts"
SPEC_FILTER = "1049.spec.ts"


def load(path: Path) -> object:
    """The parsed report. A missing or unparsable file is the caller's to
    report, so this lets the exception travel."""
    return json.loads(path.read_text(encoding="utf-8"))


def _walk(suite: dict[str, Any], out: list[tuple[str, str, str]]) -> None:
    for spec in suite.get("specs", []):
        for test in spec.get("tests", []):
            out.append((str(spec.get("file", "?")), str(spec.get("title", "?")), str(test.get("status", "?"))))
    for child in suite.get("suites", []):
        _walk(child, out)


def cases(report: object) -> list[tuple[str, str, str]] | None:
    """Every (file, title, status) in the report, or None when it is not a
    Playwright JSON report at all."""
    if not isinstance(report, dict) or not isinstance(report.get("suites"), list):
        return None
    out: list[tuple[str, str, str]] = []
    for suite in report["suites"]:
        if isinstance(suite, dict):
            _walk(suite, out)
    return out


def judge(report: object) -> list[str]:
    """Why this run does not count, as one line each. Empty means every case
    ran and none was skipped or failed."""
    found = cases(report)
    if found is None:
        return ["the report is not a Playwright JSON report, so nothing can be said about the cases"]
    if not found:
        return ["the report lists no cases at all, so the first-run specs did not run"]

    problems: list[str] = []
    for file, title, status in found:
        if status == "skipped":
            problems.append(f"SKIPPED: {file}: {title}")
        elif status == "unexpected":
            problems.append(f"FAILED: {file}: {title}")
        elif status not in ("expected", "flaky"):
            problems.append(f"{status.upper()}: {file}: {title}")

    # The summary is a second witness: a case skipped in a hook never
    # reaches a spec's `tests` list as a skip.
    stats = report.get("stats") if isinstance(report, dict) else None
    if isinstance(stats, dict):
        skipped = stats.get("skipped", 0)
        if isinstance(skipped, int) and skipped > 0 and not any(p.startswith("SKIPPED") for p in problems):
            problems.append(f"the report's own summary counts {skipped} skipped case(s)")
    if not any(status == "expected" or status == "flaky" for _, _, status in found):
        problems.append("no case passed")
    return problems
