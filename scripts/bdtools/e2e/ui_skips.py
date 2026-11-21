#!/usr/bin/env python3
"""The Chromium lane's skip guard (#1588).

A spec run that quietly runs less must not read as green. The lane has
Playwright write a JSON report beside the list reporter, and this reads it:
the only skip the in-tree specs may carry is the recording one, a walkthrough's
`test.skip(!process.env.E2E_VIDEO, "recording only, run with E2E_VIDEO=1")`,
which reports as a `skip` annotation with exactly that reason. Any other skip (a
bare `test.skip`, a `test.fixme`, another reason, no reason) is refused, and so is a report that is
missing or does not parse, which would otherwise read as "no skips".

    ui_skips.py <playwright json report>     exit 0 clean, 1 refused
"""

from __future__ import annotations

import json
import sys
from collections.abc import Iterator
from typing import Any

GATE = "E2E_VIDEO"
# The one form the walkthroughs write: test.skip(!process.env.E2E_VIDEO, "recording only, run with E2E_VIDEO=1")
# reports as a "skip" annotation with exactly this reason (1252 and 1253).
GATED = {"type": "skip", "description": "recording only, run with E2E_VIDEO=1"}


def skipped(node: dict[str, Any], path: str = "") -> Iterator[tuple[str, str, list[Any]]]:
    """Yield (file, title, annotations) for every skipped test under a suite."""
    for spec in node.get("specs", []):
        for test in spec.get("tests", []):
            if test.get("status") == "skipped":
                yield spec.get("file") or node.get("file") or path, spec.get("title", ""), test.get("annotations") or []
    for child in node.get("suites", []):
        yield from skipped(child, node.get("file") or path)


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: ui_skips.py <playwright json report>", file=sys.stderr)
        return 2
    try:
        with open(argv[1]) as fh:
            report = json.load(fh)
    except (OSError, ValueError) as err:
        print(f"==> ui browser lane: skip guard cannot read {argv[1]}: {err}", file=sys.stderr)
        return 1
    try:
        found = [hit for suite in report.get("suites") or [] for hit in skipped(suite)]
        bad = [h for h in found if not any(isinstance(a, dict) and a == GATED for a in h[2])]
    except (AttributeError, TypeError) as err:
        print(f"==> ui browser lane: skip guard cannot read the shape of {argv[1]}: {err}", file=sys.stderr)
        return 1
    for file, title, ann in bad:
        kinds = ",".join(a.get("type", "?") for a in ann) or "no annotation"
        print(
            f"==> ui browser lane: refused skip in {file}: {title} ({kinds}). Only {GATE}-gated recordings may skip.",
            file=sys.stderr,
        )
    print(f"==> ui browser lane: skip guard, {len(found) - len(bad)} {GATE}-gated skips, {len(bad)} refused")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
