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
  * an entry whose expiry has passed (inclusive: an entry is good through
    its date, judged in UTC), or whose pin is not the pin the gate ran at
    (a pin bump has to touch the list in the same diff; bump_tests_pin.py
    rewrites the header);
  * an entry that names no spec in the report, a duplicate entry or
    header, and a ` *` prefix that does not reach into a test's own title;
  * a report with errors outside its tests, a summary that counts
    failures its cases do not show, fewer cases than the list's floor,
    or a blocking file with no case in the report at all;
  * against main's copy of the list (against_baseline): a cap or skip
    budget that grew, an entry added, an expiry moved later;
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
HEADERS = ("pin", "cap", "skips", "floor")

# The owner and issue every entry's refusal message suggests, and the
# line an operator pastes. The issue is the suite's own open one in
# retnd-tests, not the pull request that wrote the list (which closes).
SUGGESTED_ISSUE = "https://github.com/retnd/retnd-tests/issues/43"
SUGGESTED_OWNER = "spdrman"


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
    floor: int
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


def suggested_line(spec: str, expiry: _dt.date) -> str:
    """The line a refusal tells the operator to add, so a NEW FAILURE says
    what to do and not only what it saw."""
    return f"{spec} | {SUGGESTED_ISSUE} | {SUGGESTED_OWNER} | {expiry.isoformat()}"


def parse_known_red(text: str) -> KnownRed:
    """The list, or a ValueError naming the line that is wrong. Lines are
    `key=value` headers, `#` comments, blanks, or entries of the form

        <file> :: <title path> | <issue> | <owner> | <YYYY-MM-DD>[ | flaky]
    """
    headers: dict[str, str] = {}
    entries: list[Entry] = []
    seen: dict[str, int] = {}
    for number, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "|" not in line:
            key, sep, value = line.partition("=")
            if sep and key.strip() in HEADERS:
                if key.strip() in headers:
                    raise ValueError(
                        f"line {number} repeats the `{key.strip()}=` header; the last one would win silently"
                    )
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
        title_part = spec.partition(" :: ")[2]
        if title_part.endswith(" *") or title_part == "*":
            # A prefix has to reach into the test's own title: past the
            # describe block's separator, with something of the title after
            # it. `file :: *` would silence a whole file in one cap slot.
            head = title_part[:-1].rstrip()
            parts = head.split(" \u203a ")
            if len(parts) < 2 or len(parts[-1].strip()) < 8:
                raise ValueError(
                    f"line {number}: a ` *` prefix has to reach at least eight characters into the test's own title, "
                    f"past the describe block: {raw}"
                )
        if spec in seen:
            raise ValueError(f"line {number} repeats the entry on line {seen[spec]}: {spec}")
        seen[spec] = number
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
        floor = int(headers["floor"])
    except ValueError as exc:
        raise ValueError("cap=, skips= and floor= have to be whole numbers") from exc
    return KnownRed(headers["pin"], cap, skips, floor, tuple(entries))


def against_baseline(current: KnownRed, baseline: KnownRed) -> list[str]:
    """Why this list is not a ratchet of `baseline` (main's copy): the cap
    and the skip budget may only go down, an entry may only come off, and
    an expiry may only move earlier. A list that only shrank returns
    nothing. The pin is not compared here: a pin bump legitimately changes
    it, and the judge already holds it to the pin file."""
    problems: list[str] = []
    if current.cap > baseline.cap:
        problems.append(f"the cap grew from {baseline.cap} on main to {current.cap}; the cap only goes down")
    if current.skips > baseline.skips:
        problems.append(f"the skip budget grew from {baseline.skips} on main to {current.skips}; it only goes down")
    base = {e.spec: e for e in baseline.entries}
    for entry in current.entries:
        if entry.spec not in base:
            problems.append(f"entry added since main: {entry.spec} (line {entry.line}); the list only shrinks")
        elif entry.expiry > base[entry.spec].expiry:
            problems.append(
                f"expiry moved later than main's for {entry.spec} (line {entry.line}): "
                f"{base[entry.spec].expiry.isoformat()} to {entry.expiry.isoformat()}"
            )
    return problems


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
    gate record carries as evidence. Empty problems means the run held.
    `today` is a UTC date; an entry is good THROUGH its expiry."""
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
            "a pin bump has to revisit the list in the same change (bump_tests_pin.py rewrites `pin=`; then judge "
            "the suite at the new pin and take off what passes)"
        )
    if len(known.entries) > known.cap:
        problems.append(
            f"the known-red list holds {len(known.entries)} entries and its cap is {known.cap}; "
            "the cap only goes down"
        )
    renew_by = (today + _dt.timedelta(days=60)).isoformat()
    for entry in known.entries:
        if entry.expiry < today:
            problems.append(
                f"EXPIRED: {entry.spec} (line {entry.line}, {entry.issue}, {entry.owner}, {entry.expiry.isoformat()}); "
                f"refresh the spec in retnd-tests and take the line off, or renew it with a date no later than "
                f"{renew_by}"
            )
        if any(entry.spec.startswith(f + " :: ") for f in BLOCKING_FILES):
            problems.append(
                f"BLOCKING SPEC LISTED: {entry.spec} (line {entry.line}); "
                "the blocking files cannot be ratcheted, refresh the spec"
            )

    # The report's own channels first. A global setup or teardown error is
    # not a test and never reaches a spec's `tests`; a partial report can
    # look like a clean short one.
    errors = report.get("errors") if isinstance(report, dict) else None
    if isinstance(errors, list) and errors:
        first = errors[0] if isinstance(errors[0], dict) else {}
        problems.append(
            f"the report carries {len(errors)} error(s) outside any test (first: "
            f"{str(first.get('message', errors[0]))[:160]}); a run that errored outside its tests is not judged "
            "on them"
        )

    passed = failed = skipped = 0
    failed_specs: dict[str, list[str]] = {}
    seen_passing: set[str] = set()
    files_seen: set[str] = set()
    for file, spec, project, status in found:
        files_seen.add(file)
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

    for blocking in BLOCKING_FILES:
        if blocking not in files_seen:
            problems.append(
                f"BLOCKING FILE MISSING: {blocking} has no case in the report; the blocking set cannot be allowed to "
                "vanish by a rename or a move, so this run does not count until the file is back under that name "
                "(or BLOCKING_FILES in web_ui_ratchet.py follows it, on purpose)"
            )

    def listing_for(spec: str) -> Entry | None:
        for e in known.entries:
            if matches(e.spec, spec):
                return e
        return None

    listed_failures = 0
    for spec, projects in sorted(failed_specs.items()):
        where = ", ".join(p for p in projects if p) or "?"
        if any(spec.startswith(f + " :: ") for f in BLOCKING_FILES):
            problems.append(f"BLOCKING FAILURE: {spec} [{where}]")
        elif listing_for(spec) is not None:
            listed_failures += 1
        else:
            problems.append(
                f"NEW FAILURE: {spec} [{where}]; not in {KNOWN_RED}. Fix the spec or the product, or, if it is "
                f"known drift, add this line (and lower nothing): "
                f"{suggested_line(spec, today + _dt.timedelta(days=60))}"
            )
    all_specs = {spec for _, spec, _, _ in found}
    for entry in known.entries:
        matched = [sp for sp in all_specs if matches(entry.spec, sp)]
        if not matched:
            problems.append(
                f"UNMATCHED ENTRY: {entry.spec} (line {entry.line}) names no spec in this report; the spec was renamed "
                "or removed, so take the line off (the cap goes down with it)"
            )
            continue
        passing = [sp for sp in matched if sp in seen_passing and sp not in failed_specs]
        if passing and not entry.flaky:
            problems.append(f"LISTED SPEC PASSES: {passing[0]} (line {entry.line}); take it off the list")

    stats = report.get("stats") if isinstance(report, dict) else None
    if isinstance(stats, dict):
        counted_skipped = stats.get("skipped")
        if isinstance(counted_skipped, int) and counted_skipped > skipped:
            skipped = counted_skipped
        counted_failed = stats.get("unexpected")
        if isinstance(counted_failed, int) and counted_failed > failed:
            problems.append(
                f"the report's summary counts {counted_failed} unexpected while its cases show {failed}; a case the "
                "summary saw fail and the suites do not list is a partial report"
            )
    if skipped > known.skips:
        problems.append(
            f"the run skipped {skipped} cases and the list allows {known.skips}; a skip is not a pass (#1049). "
            "Un-skip the cases or, if the deployment really cannot run them, raise skips= with the reason in the diff"
        )
    ran = passed + failed
    if ran < known.floor:
        problems.append(
            f"the run ran {ran} case(s) and the list's floor is {known.floor}; a suite this short did not run, "
            "whatever it reports"
        )
    if passed == 0:
        problems.append("no case passed")
    return Verdict(tuple(problems), passed, failed, skipped, listed_failures)


def load_report(path: Path) -> object:
    return first_run_report.load(path)
