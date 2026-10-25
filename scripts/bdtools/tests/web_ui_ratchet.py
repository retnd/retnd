#!/usr/bin/env python3
"""The web-ui ratchet refuses what it has to, and only that (#1260).

The full browser suite runs as a lane judged against
scripts/known-red.txt rather than by its exit status, so the judge is
the whole gate for 33 specs that were red on main the day the local gate
became the only gate. These cases pin its answers without Docker:

  A  a clean report against an empty list is accepted, and the counts it
     reports are the report's
  B  a failure not on the list is refused, and named
  C  a listed failure is accepted, and counted as a listed failure
  D  a listed spec that passes is refused: the list only shrinks
  E  a failure in a blocking file is refused even when listed, and a
     blocking file on the list is refused on its own; a `flaky` entry is
     accepted whether it passes or fails, and still expires
  F  an expired entry is refused; one that expires tomorrow is not
  G  a list judged at another pin is refused
  H  more skips than the budget is refused; the summary's own count is a
     second witness
  I  more entries than the cap is refused
  J  a missing report, a report with no cases and a file that is not a
     report are refused
  K  the parser: a header missing, an entry short a field, an expiry that
     is not a date, a blocking spec id
  L  the controls: the same builder produces both the accepted and the
     refused report; the real list parses, is at the real pin, and holds
     no blocking spec; the gate calls the judge and writes the lane
     result the recorder reads

# PORTED-CHECK HAZARD NOTE

There is no bash original: this is new with #1260.

a failure reads as a pass
  hazard:   the lane's exit status is ignored on purpose, so the only
            thing that can fail a run is the judge seeing the failure.
  held by:  B, and E for the blocking files.

the list grows under the gate
  hazard:   a spec added to the list is a failure nobody sees again.
  held by:  I (the cap), D (a pass has to come off), F (an expiry), G
            (a pin bump revisits the list).

the judge is never called
  hazard:   a correct judge the gate stopped calling checks nothing.
  held by:  L, which reads run_tests_repo_gate.py for the call site, the
            JSON reporter it asks the rig for, and the lane file it writes.
"""

# ruff: noqa: RUF001, E501
# The id separator is the one Playwright prints (U+203A), and the ids here
# have to be what the judge composes from a real report. E501 because the
# long lines are the two messages of each check side by side, which is
# how every testkit suite reads; splitting them would hide which pair is
# which.
from __future__ import annotations

import datetime as dt
import sys
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

from bdtools.e2e import tests_pin, web_ui_ratchet  # noqa: E402
from bdtools.tests import testkit  # noqa: E402

GATE = SCRIPTS / "bdtools" / "e2e" / "run_tests_repo_gate.py"
RECORDER = SCRIPTS / "release" / "record-local-gate.sh"
REAL_LIST = SCRIPTS / "e2e" / "known-red.txt"
REAL_PIN = SCRIPTS / "e2e" / "tests-repo.pin"

PIN = "9699ec5118a0d7748e988346192be37c7bbb5ec7"
TODAY = dt.date(2026, 10, 23)


BLOCKING_OK = [("safety-invariants.spec.ts", "s", "expected"), ("workflow-recovery.spec.ts", "w", "expected")]


def report(cases: list[tuple[str, str, str]], *, skipped_stat: int | None = None, blocking: bool = True) -> dict[str, object]:
    """A Playwright JSON report shaped as the real one is: a file suite per
    file, a describe suite inside it, and the specs under that. Each case
    is (file, title, status); the describe title is "d". The two blocking
    files are present and passing unless a case says otherwise, because a
    report without them is refused for that alone (M6)."""
    files: dict[str, list[dict[str, object]]] = {}
    present = {file for file, _, _ in cases}
    if blocking:
        cases = cases + [c for c in BLOCKING_OK if c[0] not in present]
    for file, title, status in cases:
        files.setdefault(file, []).append(
            {"title": title, "file": file, "tests": [{"projectName": "generic", "status": status}]}
        )
    return {
        "stats": {"skipped": sum(1 for _, _, s in cases if s == "skipped") if skipped_stat is None else skipped_stat},
        "suites": [
            {"title": file.split("/")[-1], "file": file, "suites": [{"title": "d", "specs": specs}]}
            for file, specs in files.items()
        ],
    }


def listing(entries: list[str], *, pin: str = PIN, cap: int = 5, skips: int = 2, expiry: str = "2027-01-31",
            floor: int = 0) -> str:
    lines = [f"pin={pin}", f"cap={cap}", f"skips={skips}", f"floor={floor}", "# a comment"]
    lines += [f"{spec} | https://github.com/retnd/retnd/issues/1260 | spdrman | {expiry}" for spec in entries]
    return "\n".join(lines) + "\n"


OK_SHELL = ("shell.spec.ts", "renders", "expected")
RED_WIZARD = ("wizard.spec.ts", "groups the flow into six steps", "unexpected")
OK_WIZARD = ("wizard.spec.ts", "groups the flow into six steps", "expected")


def judged(
    cases: list[tuple[str, str, str]],
    known: web_ui_ratchet.KnownRed,
    *,
    pin: str = PIN,
    skipped_stat: int | None = None,
) -> web_ui_ratchet.Verdict:
    return web_ui_ratchet.judge(report(cases, skipped_stat=skipped_stat), known, pin, TODAY)


def main() -> int:
    suite = testkit.Suite("web-ui ratchet")
    empty = web_ui_ratchet.parse_known_red(listing([]))
    wizard = "wizard.spec.ts :: d › groups the flow into six steps"

    # A
    clean = judged([OK_SHELL, OK_WIZARD], empty)
    suite.check(clean.ok, "A a clean report against an empty list is accepted", "A a clean report was refused", str(clean.problems))
    suite.check((clean.passed, clean.failed, clean.skipped, clean.listed_failures) == (4, 0, 0, 0),
                "A the verdict carries the report's counts", "A the counts are wrong", str(clean))

    # B
    new = judged([OK_SHELL, RED_WIZARD], empty)
    suite.check(any(p.startswith("NEW FAILURE") and wizard in p for p in new.problems),
                "B a failure not on the list is refused and named", "B an unlisted failure was accepted", str(new.problems))

    # C
    known = web_ui_ratchet.parse_known_red(listing([wizard]))
    listed = judged([OK_SHELL, RED_WIZARD], known)
    suite.check(listed.ok and listed.listed_failures == 1 and listed.failed == 1,
                "C a listed failure is accepted and counted", "C a listed failure was refused or miscounted", str(listed))

    # D
    passing = judged([OK_SHELL, OK_WIZARD], known)
    suite.check(any(p.startswith("LISTED SPEC PASSES") and wizard in p for p in passing.problems),
                "D a listed spec that passes is refused", "D a listed spec that passes was accepted", str(passing.problems))

    # E
    blocking_spec = "safety-invariants.spec.ts :: d › post-commit"
    blocked = judged(
        [OK_SHELL, ("safety-invariants.spec.ts", "post-commit", "unexpected")],
        web_ui_ratchet.parse_known_red(listing([blocking_spec])),
    )
    suite.check(any(p.startswith("BLOCKING FAILURE") for p in blocked.problems)
                and any(p.startswith("BLOCKING SPEC LISTED") for p in blocked.problems),
                "E a blocking spec's failure is refused even when listed, and the listing is refused too",
                "E a blocking failure got through the list", str(blocked.problems))
    recovery = judged([OK_SHELL, ("workflow-recovery.spec.ts", "held", "unexpected")], empty)
    suite.check(any(p.startswith("BLOCKING FAILURE") for p in recovery.problems),
                "E workflow-recovery is blocking too", "E a workflow-recovery failure was not blocking", str(recovery.problems))

    flaky_list = web_ui_ratchet.parse_known_red(listing([]) + f"{wizard} | https://github.com/retnd/retnd/issues/1260 | spdrman | 2027-01-31 | flaky\n")
    suite.check(judged([OK_SHELL, OK_WIZARD], flaky_list).ok and judged([OK_SHELL, RED_WIZARD], flaky_list).ok,
                "E a flaky entry is accepted whether it passes or fails", "E a flaky entry was refused on one answer",
                str(judged([OK_SHELL, OK_WIZARD], flaky_list).problems) + str(judged([OK_SHELL, RED_WIZARD], flaky_list).problems))
    stale_flaky = web_ui_ratchet.parse_known_red(listing([]) + f"{wizard} | https://github.com/retnd/retnd/issues/1260 | spdrman | 2026-10-22 | flaky\n")
    suite.check(any(p.startswith("EXPIRED") for p in judged([OK_SHELL, OK_WIZARD], stale_flaky).problems),
                "E a flaky entry still expires", "E an expired flaky entry was accepted")

    prefixed = web_ui_ratchet.parse_known_red(listing(["wizard.spec.ts :: d › groups the flow *"]))
    suite.check(judged([OK_SHELL, RED_WIZARD], prefixed).ok and not judged([OK_SHELL, OK_WIZARD], prefixed).ok,
                "E an entry ending in ` *` lists every spec it is a prefix of, and a pass under it is still refused",
                "E a prefix entry did not match, or let a pass through",
                str(judged([OK_SHELL, RED_WIZARD], prefixed).problems))

    # F
    expired = judged([OK_SHELL, RED_WIZARD], web_ui_ratchet.parse_known_red(listing([wizard], expiry="2026-10-22")))
    suite.check(any(p.startswith("EXPIRED") for p in expired.problems),
                "F an entry whose expiry was yesterday is refused", "F an expired entry was accepted", str(expired.problems))
    tomorrow = judged([OK_SHELL, RED_WIZARD], web_ui_ratchet.parse_known_red(listing([wizard], expiry="2026-10-24")))
    suite.check(tomorrow.ok, "F an entry that expires tomorrow is still accepted", "F an unexpired entry was refused", str(tomorrow.problems))

    # G
    other = judged([OK_SHELL], web_ui_ratchet.parse_known_red(listing([], pin="0" * 40)))
    suite.check(any("pin bump" in p for p in other.problems),
                "G a list judged at another pin is refused", "G an other-pin list was accepted", str(other.problems))

    # H
    skipped = judged([OK_SHELL] + [("x.spec.ts", f"s{i}", "skipped") for i in range(3)], empty)
    suite.check(any("skipped 3 cases" in p for p in skipped.problems),
                "H more skips than the budget is refused", "H a grown skip count was accepted", str(skipped.problems))
    hidden = judged([OK_SHELL], empty, skipped_stat=9)
    suite.check(any("skipped 9 cases" in p for p in hidden.problems),
                "H the summary's own skip count is a second witness", "H a summary-only skip count was ignored", str(hidden.problems))

    # I
    over = judged([OK_SHELL], web_ui_ratchet.parse_known_red(listing([f"a.spec.ts :: d › t{i}" for i in range(3)], cap=2)))
    suite.check(any("cap only goes down" in p for p in over.problems),
                "I more entries than the cap is refused", "I an over-cap list was accepted", str(over.problems))

    # J
    suite.check(not web_ui_ratchet.judge(report([], blocking=False), empty, PIN, TODAY).ok,
                "J a report with no cases is refused", "J an empty report was accepted")
    suite.check(not web_ui_ratchet.judge({"hello": 1}, empty, PIN, TODAY).ok and not web_ui_ratchet.judge(None, empty, PIN, TODAY).ok,
                "J a file that is not a report is refused", "J a non-report was accepted")

    # K
    def refuses(text: str) -> bool:
        try:
            web_ui_ratchet.parse_known_red(text)
        except ValueError:
            return True
        return False
    suite.check(refuses("cap=1\nskips=1\n"), "K a list with no pin header is refused", "K a list with no pin parsed")
    suite.check(refuses(listing([]) + "a.spec.ts :: d › t | #1260 | spdrman\n"), "K an entry short a field is refused", "K a short entry parsed")
    suite.check(refuses(listing([]) + "a.spec.ts :: d › t | #1260 | spdrman | soon\n"),
                "K an expiry that is not a date is refused", "K a non-date expiry parsed")
    suite.check(refuses(listing([]) + "a.spec.ts d t | #1260 | spdrman | 2027-01-01\n"),
                "K an entry that names no spec is refused", "K a spec-less entry parsed")
    suite.check(refuses(listing([]) + "a.spec.ts :: d › t | #1260 | spdrman | 2027-01-01 | sometimes\n"),
                "K a fifth field that is not `flaky` is refused", "K an unknown fifth field parsed")

    # M: the hygiene the review of #1261 asked for (item 5).
    # M1 duplicates and duplicate headers are parse errors, with the line.
    suite.check(refuses(listing([wizard, wizard])), "M1 a duplicate entry is refused", "M1 a duplicate entry parsed")
    suite.check(refuses(listing([]) + "cap=1\n"), "M1 a duplicate header is refused", "M1 a duplicate header parsed")
    # M2 an entry that matches no spec in the report is refused: a renamed
    # or deleted spec would otherwise hold its cap slot forever.
    ghost = judged([OK_SHELL], web_ui_ratchet.parse_known_red(listing(["ghost.spec.ts :: d › gone"])))
    suite.check(any(p.startswith("UNMATCHED ENTRY") and "ghost.spec.ts" in p for p in ghost.problems),
                "M2 an entry that matches no spec in the report is refused and named", "M2 an unmatched entry was accepted", str(ghost.problems))
    # M3 the prefix form is a TITLE prefix: `file :: *` and a prefix shorter
    # than the describe block are refused by the parser.
    suite.check(refuses(listing(["wizard.spec.ts :: *"])), "M3 `file :: *` is refused", "M3 a whole-file prefix parsed")
    suite.check(refuses(listing(["wizard.spec.ts :: d *"])), "M3 a prefix that ends inside the describe title is refused", "M3 a describe-only prefix parsed")
    # M4 the report's own channels: a non-empty `errors` list refuses, and
    # stats that disagree with the walked cases refuse.
    errored = report([OK_SHELL])
    errored["errors"] = [{"message": "globalSetup boom"}]
    suite.check(not web_ui_ratchet.judge(errored, empty, PIN, TODAY).ok, "M4 a report with errors is refused", "M4 a report with errors was accepted")
    skewed = report([OK_SHELL])
    skewed["stats"] = {"expected": 1, "unexpected": 3, "skipped": 0, "flaky": 0}
    suite.check(any("summary counts 3 unexpected" in p for p in web_ui_ratchet.judge(skewed, empty, PIN, TODAY).problems),
                "M4 a summary that counts failures the cases do not show is refused", "M4 a skewed summary was accepted")
    # M5 a floor on what ran: fewer cases than the list's `floor=` refuses.
    few = judged([OK_SHELL], web_ui_ratchet.parse_known_red(listing([], floor=5)))
    suite.check(any("ran 3 case(s)" in p and "floor" in p for p in few.problems),
                "M5 fewer cases than the floor is refused", "M5 a run under the floor was accepted", str(few.problems))
    # M6 a blocking file that is absent from the report fails rather than
    # vanishing from the blocking set.
    no_blocking = web_ui_ratchet.judge(report([OK_SHELL], blocking=False), empty, PIN, TODAY)
    suite.check(any(p.startswith("BLOCKING FILE MISSING") for p in no_blocking.problems),
                "M6 a blocking file with no case in the report is refused", "M6 a missing blocking file vanished silently", str(no_blocking.problems))
    both_blocking = [OK_SHELL, *BLOCKING_OK]
    suite.check(judged(both_blocking, empty).ok, "M6 with both blocking files present the run is accepted", "M6 a run with both blocking files was refused", str(judged(both_blocking, empty).problems))
    # M7 expiry: inclusive of the date (an entry is good THROUGH its expiry),
    # judged in UTC.
    on_the_day = judged([OK_SHELL, RED_WIZARD, *both_blocking[1:]], web_ui_ratchet.parse_known_red(listing([wizard], expiry="2026-10-23")))
    suite.check(on_the_day.ok, "M7 an entry is good through its expiry date", "M7 an entry expired on its own date", str(on_the_day.problems))
    day_after = judged([OK_SHELL, RED_WIZARD, *both_blocking[1:]], web_ui_ratchet.parse_known_red(listing([wizard], expiry="2026-10-22")))
    suite.check(any(p.startswith("EXPIRED") for p in day_after.problems), "M7 the day after, it is expired", "M7 a past expiry was accepted")
    suite.check("timezone.utc" in (SCRIPTS / "bdtools" / "e2e" / "run_tests_repo_gate.py").read_text(encoding="utf-8"),
                "M7 the gate judges with today's date in UTC", "M7 the gate judges with a local date")
    # M8 the baseline: against main's copy, cap and skip budget cannot grow,
    # an entry cannot be added, and an expiry cannot move later.
    base = web_ui_ratchet.parse_known_red(listing([wizard], cap=1, skips=2))
    grown_cap = web_ui_ratchet.parse_known_red(listing([wizard], cap=2, skips=2))
    suite.check(any("cap grew" in p for p in web_ui_ratchet.against_baseline(grown_cap, base)), "M8 a cap larger than main's is refused", "M8 a grown cap passed the baseline")
    grown_skips = web_ui_ratchet.parse_known_red(listing([wizard], cap=1, skips=3))
    suite.check(any("skip budget grew" in p for p in web_ui_ratchet.against_baseline(grown_skips, base)), "M8 a skip budget larger than main's is refused", "M8 a grown skip budget passed")
    added = web_ui_ratchet.parse_known_red(listing([wizard, "a.spec.ts :: d › t"], cap=2, skips=2))
    suite.check(any("entry added" in p for p in web_ui_ratchet.against_baseline(added, base)), "M8 an entry main does not have is refused", "M8 an added entry passed the baseline")
    later = web_ui_ratchet.parse_known_red(listing([wizard], cap=1, skips=2, expiry="2027-06-30"))
    suite.check(any("expiry moved later" in p for p in web_ui_ratchet.against_baseline(later, base)), "M8 an expiry later than main's is refused", "M8 a later expiry passed")
    suite.check(web_ui_ratchet.against_baseline(base, base) == [], "M8 the same list as main passes the baseline", "M8 an unchanged list failed its own baseline")
    shrunk = web_ui_ratchet.parse_known_red(listing([], cap=0, skips=1))
    suite.check(web_ui_ratchet.against_baseline(shrunk, base) == [], "M8 a list that only shrank passes the baseline", "M8 a shrunk list failed the baseline")
    # M9 refusal messages say what to do next.
    suite.check(any("known-red.txt" in p and "| spdrman |" in p for p in new.problems), "M9 a new failure's refusal shows the line to add", "M9 a new failure's refusal does not say what to add", str(new.problems))
    suite.check(any("renew" in p.lower() or "refresh" in p.lower() for p in day_after.problems), "M9 an expiry's refusal says to renew or refresh", "M9 an expiry's refusal says nothing about next steps", str(day_after.problems))

    # L
    suite.check(clean.problems != new.problems,
                "L control: the judge answers differently for a clean and a failing report", "L control: the judge cannot tell them apart")
    real = web_ui_ratchet.parse_known_red(REAL_LIST.read_text(encoding="utf-8")) if REAL_LIST.is_file() else None
    suite.check(real is not None, "L the real known-red list exists and parses", f"L {REAL_LIST} is missing or does not parse")
    if real is not None:
        real_pin = tests_pin.read_pin(REAL_PIN)["TESTS_REPO_SHA"]
        suite.check(real.pin == real_pin, "L the real list is judged at the real pin", f"L the list is at {real.pin[:12]} and the pin is {real_pin[:12]}")
        suite.check(len(real.entries) <= real.cap <= 39,
                    f"L the real list holds {len(real.entries)} entries under a cap of {real.cap}, at most the 39 it started with",
                    f"L the real list breaks its cap ({len(real.entries)} entries, cap {real.cap}, ceiling 39)")
        suite.check(not any(any(e.spec.startswith(f + " :: ") for f in web_ui_ratchet.BLOCKING_FILES) for e in real.entries),
                    "L the real list holds no blocking spec", "L a blocking spec is on the real list")
        suite.check(all(e.owner and e.issue.startswith("https://github.com/") for e in real.entries),
                    "L every real entry has an owner and an issue link", "L a real entry lacks an owner or an issue link")
        suite.check(all("/issues/1260" not in e.issue for e in real.entries),
                    "L no real entry cites #1260, which the PR that wrote the list closes", "L a real entry cites #1260")
        suite.check(len({e.expiry for e in real.entries}) >= 3 and max(e.expiry for e in real.entries) < dt.date(2027, 1, 31),
                    "L the real expiries are staggered and all before 2027-01-31", "L the real expiries are one date, or reach 2027-01-31")
        header = REAL_LIST.read_text(encoding="utf-8")
        suite.check("scripts/bdtools/e2e/web_ui_ratchet.py" in header and "scripts/e2e/tests-repo.pin" in header
                    and "scripts/bdtools/web_ui_ratchet.py" not in header and "scripts/tests-repo.pin" not in header,
                    "L the header names the judge and the pin by their real paths", "L the header names a path that does not exist")
    gate = GATE.read_text(encoding="utf-8")
    suite.check("web_ui_ratchet.judge(" in gate and '"--reporter=list,json"' in gate,
                "L control: the gate judges the lane's report, and asks the rig for the JSON reporter",
                "L control: the gate does not judge the lane's report with web_ui_ratchet.judge, or runs the rig without the JSON reporter")
    suite.check("web-ui-lane.json" in gate and "web-ui-lane.json" in RECORDER.read_text(encoding="utf-8"),
                "L control: the gate writes .e2e-gate/web-ui-lane.json and the recorder reads it into the record",
                "L control: the lane result does not travel from the gate to the record")
    half = gate.split("def browser_half")[1].split("\ndef ")[0] if "def browser_half" in gate else ""
    # read_the_pin hands the gate {"url", "sha"}, not the file's own keys. A
    # KeyError here is found after the suite's 27 minutes, which is how it
    # was found once.
    suite.check('pin["sha"]' in half and "TESTS_REPO_SHA" not in half,
                "L control: the lane reads the pin by the key read_the_pin hands it",
                "L control: the lane reads the pin by a key read_the_pin does not hand it (KeyError after the suite ran)")
    suite.check("harness.cannot_run(" in half,
                "L control: a lane that produced no report is INCOMPLETE, never ok",
                "L control: browser_half never says cannot_run, so a crashed lane cannot end INCOMPLETE")
    return suite.finish()


if __name__ == "__main__":
    sys.exit(main())
