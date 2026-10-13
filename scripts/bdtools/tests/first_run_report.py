#!/usr/bin/env python3
"""A skipped first-run spec fails the gate (retnd#1049).

The `*.1049.spec.ts` cases in retnd-tests guard themselves with
`needs(isFresh(facts), ...)`. On the seeded rig the guard skips the case,
Playwright exits 0, and the run looks green. The gate step that runs them
against `three-machine-web-ui.sh --fresh` therefore reads the JSON report
and refuses a skip. These cases pin that judgement without Docker:

  A  a report where every case ran and passed is accepted
  B  one skipped case is refused, and named
  C  the exit-0 shape (every case skipped) is refused
  D  a report with no cases, and a file that is not a report, are refused
  E  a failed case is refused, and so is a skip only the summary counts
  F  the controls: the same builder produces both the accepted and the
     refused report, so the judge is seen to answer differently; and the
     wiring is checked in text, because a judge nobody calls cannot fail a
     commit (the gate calls it from `body()`, and drives the rig with
     --fresh)

# PORTED-CHECK HAZARD NOTE

There is no bash original: this is new with #1049.

a skip reads as a pass
  hazard:   Playwright's exit status is 0 for a run of only skipped cases.
  held by:  the gate judging the report (cases B and C), not the status.

the judge is never called
  hazard:   a correct judge that the gate stopped calling checks nothing.
  held by:  case F, which reads run_tests_repo_gate.py for the call site and
            for the --fresh flag it hands the rig.

an empty report
  hazard:   zero cases is "no skips" to a naive count.
  held by:  case D, which requires a refusal.
"""

from __future__ import annotations

import sys
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

from bdtools.e2e import first_run_report  # noqa: E402
from bdtools.tests import testkit  # noqa: E402

GATE = SCRIPTS / "bdtools" / "e2e" / "run_tests_repo_gate.py"


def report(statuses: list[str], *, skipped_stat: int | None = None) -> dict[str, object]:
    """A Playwright JSON report shaped as the real one is: a file suite, a
    describe suite inside it, and the specs under that."""
    specs = [
        {"title": f"case {i}", "file": "e2e/first-run-failure-surfaces.1049.spec.ts", "tests": [{"status": s}]}
        for i, s in enumerate(statuses)
    ]
    return {
        "stats": {"skipped": statuses.count("skipped") if skipped_stat is None else skipped_stat},
        "suites": [{"title": "first-run-failure-surfaces.1049.spec.ts", "suites": [{"title": "d", "specs": specs}]}],
    }


def main() -> int:
    suite = testkit.Suite("first-run report")
    ok = report(["expected", "expected", "expected"])

    suite.check(first_run_report.judge(ok) == [], "A every case ran and passed: accepted",
                "A a clean report was refused", str(first_run_report.judge(ok)))

    one = first_run_report.judge(report(["expected", "skipped", "expected"]))
    suite.check(len(one) == 1 and one[0].startswith("SKIPPED") and "case 1" in one[0],
                "B one skipped case is refused and named", "B one skipped case was not refused by name", str(one))

    allskipped = first_run_report.judge(report(["skipped", "skipped"]))
    suite.check(any(p.startswith("SKIPPED") for p in allskipped) and "no case passed" in allskipped,
                "C a report of only skips (what exit 0 hides) is refused",
                "C a report of only skips was accepted", str(allskipped))

    suite.check(first_run_report.judge(report([])) != [], "D a report with no cases is refused",
                "D a report with no cases was accepted")
    suite.check(first_run_report.judge({"hello": 1}) != [] and first_run_report.judge(None) != [],
                "D a file that is not a report is refused", "D a non-report was accepted")

    failed = first_run_report.judge(report(["expected", "unexpected"]))
    suite.check(any(p.startswith("FAILED") for p in failed), "E a failed case is refused",
                "E a failed case was accepted", str(failed))
    hidden = first_run_report.judge(report(["expected"], skipped_stat=2))
    suite.check(any("summary counts 2 skipped" in p for p in hidden),
                "E a skip only the summary counts is refused", "E a summary-only skip was accepted", str(hidden))

    suite.check(first_run_report.judge(ok) != first_run_report.judge(report(["skipped"])),
                "F control: the judge answers differently for a clean and a skipped report",
                "F control: the judge cannot tell a clean report from a skipped one")
    gate = GATE.read_text(encoding="utf-8")
    suite.check("    fresh_first_run_half(root, checkout)\n    return harness.EXIT_OK" in gate,
                "F control: the gate's body() calls the first-run step",
                "F control: the gate no longer calls fresh_first_run_half from body()")
    suite.check('"--fresh"' in gate and "first_run_report.judge(" in gate,
                "F control: the step drives the rig with --fresh and judges the report",
                "F control: the step lost --fresh or the judging")

    return suite.finish()


if __name__ == "__main__":
    sys.exit(main())
