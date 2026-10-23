"""Judging the full web-ui suite's Playwright JSON report against the
known-red ratchet (#1260).

The browser suite from retnd-tests runs in every full gate, and when
#1260 made that gate the only gate it had 37 failures that were already
on main, mostly UI drift in the specs, over a 27-minute run. A four-expert
panel chose this shape over refreshing all 37 first or releasing over the
gate: a pinned blocking subset (the safety-invariants and
workflow-recovery specs, whose failures block however they are listed),
plus the full suite as a ratcheted lane judged against
scripts/e2e/known-red.txt, one line per spec that is allowed to fail,
with the issue, the owner and an expiry, keyed to the retnd-tests pin.

The judge reads the report, never the exit status. It refuses:

  * a failure that is not listed, or any failure in a blocking spec file;
  * a listed spec that now passes (the list only shrinks, and a pass is
    the moment to shrink it), unless the entry is marked `flaky`: a spec
    seen to pass and fail on the same tree in two baselines is allowed
    either answer until its expiry, because refusing it both ways would
    make the lane red on a coin toss;
  * an entry whose expiry has passed, or whose pin is not the pin the
    gate ran at (a pin bump has to touch the list in the same diff);
  * more skips than the list's own skip budget (a skip is the same colour
    as a pass in an exit status, which is #1049 again);
  * more entries than the list's cap, which only goes down;
  * a missing report, a report with no cases, or a file that is not a
    Playwright JSON report at all. A run that produced no report is a run
    that did not happen, and the caller turns that into INCOMPLETE rather
    than a pass or a failure.

This module is only the judging, so it can be tested without Docker, a
browser or a rig (scripts/bdtools/tests/web_ui_ratchet.py).
run_tests_repo_gate.browser_half drives the rig, writes the lane's result
for the gate record, and calls this.
"""

from __future__ import annotations

import datetime as _dt
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from bdtools.e2e import first_run_report

# Failures in these files block whatever the list says. They are the
# panel's "pinned known-green subset": the invariants a release stands on
# and the recovery of an interrupted workflow run, which is the one test
# anywhere that a crashed run is held and can be let go.
BLOCKING_FILES = ("safety-invariants.spec.ts", "workflow-recovery.spec.ts")

# Where the list lives, relative to the repository root, and the headers
# it has to carry. `pin` is the retnd-tests sha the entries were judged
# at, `cap` the most entries the list may hold, `skips` the most skipped
# cases a run may report.
KNOWN_RED = Path("scripts/e2e/known-red.txt")
HEADERS = ("pin", "cap", "skips")


@dataclass(frozen=True)
class Entry:
    spec: str
    issue: str
    owner: str
    expiry: _dt.date
    line: int
    flaky: bool = False


@dataclass(frozen=True)
class KnownRed:
    pin: str
    cap: int
    skips: int
    entries: tuple[Entry, ...]


def spec_id(file: str, titles: list[str]) -> str:
    """The id a spec is listed by: its file as the report names it (relative
    to the suite's test directory, so `wizard.spec.ts`, not `e2e/...`) and
    its full title path, describe blocks included, joined the way Playwright
    prints them. No line numbers, so an edit above a spec does not move its
    id."""
    return file + " :: " + " \u203a ".join(titles)


def matches(entry_spec: str, spec: str) -> bool:
    """Whether a listed id names a report id. An id ending in ` *` is a
    prefix, for a title that cannot be written out in full here: six
    retnd-tests titles end in a parenthesis naming that repository's old
    issue tracker, and this repository's former-name guard allows that
    word in no tracked file. The prefix has to reach the parenthesis, so it
    still names one spec."""
    if entry_spec.endswith(" *"):
        return spec.startswith(entry_spec[:-2])
    return entry_spec == spec


def parse_known_red(text: str) -> KnownRed:
    """The list, or a ValueError naming the line that is wrong. Lines are
    `key=value` headers, `#` comments, blanks, or entries of the form

        <file> :: <title path> | <issue> | <owner> | <YYYY-MM-DD>[ | flaky]
    """
    headers: dict[str, str] = {}
    entries: list[Entry] = []
    for number, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "|" not in line:
            key, sep, value = line.partition("=")
            if sep and key.strip() in HEADERS:
                headers[key.strip()] = value.strip()
                continue
            raise ValueError(f"line {number} is neither a header ({', '.join(HEADERS)}) nor an entry: {raw}")
        parts = [p.strip() for p in line.split("|")]
        flaky = False
        if len(parts) == 5 and parts[4] == "flaky":
            flaky = True
            parts = parts[:4]
        if len(parts) != 4 or not all(parts):
            raise ValueError(
                f"line {number} needs `<file> :: <title> | <issue> | <owner> | <YYYY-MM-DD>[ | flaky]`: {raw}"
            )
        spec, issue, owner, expiry_text = parts
        if " :: " not in spec:
            raise ValueError(f"line {number} names no spec (`<file> :: <title>`): {raw}")
        try:
            expiry = _dt.date.fromisoformat(expiry_text)
        except ValueError as exc:
            raise ValueError(f"line {number} has an expiry that is not a date: {expiry_text}") from exc
        entries.append(Entry(spec, issue, owner, expiry, number, flaky))
    for key in HEADERS:
        if key not in headers:
            raise ValueError(f"the list has no `{key}=` header")
    try:
        cap = int(headers["cap"])
        skips = int(headers["skips"])
    except ValueError as exc:
        raise ValueError("cap= and skips= have to be whole numbers") from exc
    return KnownRed(headers["pin"], cap, skips, tuple(entries))


def _walk(suite: dict[str, Any], titles: list[str], out: list[tuple[str, str, str, str]]) -> None:
    for spec in suite.get("specs", []):
        file = str(spec.get("file", "?"))
        path = [*titles, str(spec.get("title", "?"))]
        for test in spec.get("tests", []):
            out.append((file, spec_id(file, path), str(test.get("projectName", "")), str(test.get("status", "?"))))
    for child in suite.get("suites", []):
        if isinstance(child, dict):
            _walk(child, [*titles, str(child.get("title", ""))], out)


def cases(report: object) -> list[tuple[str, str, str, str]] | None:
    """Every (file, spec id, project, status) in the report, or None when
    it is not a Playwright JSON report. The top-level suites are the file
    suites, whose title is the file name, so their titles are not part of
    an id; every describe block below them is."""
    if not isinstance(report, dict) or not isinstance(report.get("suites"), list):
        return None
    out: list[tuple[str, str, str, str]] = []
    for suite in report["suites"]:
        if isinstance(suite, dict):
            _walk(suite, [], out)
    return out


@dataclass(frozen=True)
class Verdict:
    problems: tuple[str, ...]
    passed: int
    failed: int
    skipped: int
    listed_failures: int

    @property
    def ok(self) -> bool:
        return not self.problems


def judge(report: object, known: KnownRed, pin: str, today: _dt.date) -> Verdict:
    """Why this run does not count, one line each, plus the counts the
    gate record carries as evidence. Empty problems means the run held."""
    found = cases(report)
    if found is None:
        return Verdict(("the report is not a Playwright JSON report, so nothing can be said about the cases",),
                       0, 0, 0, 0)
    if not found:
        return Verdict(("the report lists no cases at all, so the browser suite did not run",), 0, 0, 0, 0)

    problems: list[str] = []
    if known.pin != pin:
        problems.append(
            f"the known-red list was judged at retnd-tests {known.pin[:12]} and this run is at {pin[:12]}: "
            "a pin bump has to revisit the list in the same change"
        )
    if len(known.entries) > known.cap:
        problems.append(
            f"the known-red list holds {len(known.entries)} entries and its cap is {known.cap}; the cap only goes down"
        )
    listed = {e.spec: e for e in known.entries}

    def listing_for(spec: str) -> Entry | None:
        for e in known.entries:
            if matches(e.spec, spec):
                return e
        return None
    for entry in known.entries:
        if entry.expiry <= today:
            problems.append(
                f"EXPIRED: {entry.spec} (line {entry.line}, {entry.issue}, {entry.owner}, {entry.expiry.isoformat()})"
            )
        if any(entry.spec.startswith(f + " :: ") for f in BLOCKING_FILES):
            problems.append(
                f"BLOCKING SPEC LISTED: {entry.spec} (line {entry.line}); "
                "the blocking files cannot be ratcheted, refresh the spec"
            )

    passed = failed = skipped = 0
    failed_specs: dict[str, list[str]] = {}
    seen_passing: set[str] = set()
    for _file, spec, project, status in found:
        if status == "skipped":
            skipped += 1
        elif status == "unexpected":
            failed += 1
            failed_specs.setdefault(spec, []).append(project)
        elif status in ("expected", "flaky"):
            passed += 1
            seen_passing.add(spec)
        else:
            failed += 1
            failed_specs.setdefault(spec, []).append(f"{project}:{status}")

    listed_failures = 0
    for spec, projects in sorted(failed_specs.items()):
        where = ", ".join(p for p in projects if p) or "?"
        if any(spec.startswith(f + " :: ") for f in BLOCKING_FILES):
            problems.append(f"BLOCKING FAILURE: {spec} [{where}]")
        elif listing_for(spec) is not None:
            listed_failures += 1
        else:
            problems.append(f"NEW FAILURE: {spec} [{where}]; not in {KNOWN_RED}")
    for entry in listed.values():
        passing = [s for s in seen_passing if matches(entry.spec, s) and s not in failed_specs]
        if passing and not entry.flaky:
            problems.append(f"LISTED SPEC PASSES: {passing[0]} (line {entry.line}); take it off the list")

    stats = report.get("stats") if isinstance(report, dict) else None
    counted = stats.get("skipped") if isinstance(stats, dict) else None
    if isinstance(counted, int) and counted > skipped:
        skipped = counted
    if skipped > known.skips:
        problems.append(
            f"the run skipped {skipped} cases and the list allows {known.skips}; a skip is not a pass (#1049)"
        )
    if passed == 0:
        problems.append("no case passed")
    return Verdict(tuple(problems), passed, failed, skipped, listed_failures)


def load_report(path: Path) -> object:
    return first_run_report.load(path)
