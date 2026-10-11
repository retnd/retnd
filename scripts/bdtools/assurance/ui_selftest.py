#!/usr/bin/env python3
"""Positive controls for EPIC Q's browser-side guards (#988).

The three claims are rendered by a few small pieces of TypeScript, and the
guards that keep them honest are exactly the kind that pass on the tree they
ship with and could not fail: a glyph table whose `unknown` entry could be the
pass glyph, an `inert` that could pass a control byte through, a merge that
could keep a replayed line. Their tests going green is no evidence they can go
red.

So each is mutation-tested here against the real tree, the way the Go
self-tests next to this one do it: a copy of `ui/shared` gets one deliberate
violation planted in a real source file, the narrowed unit suite and the
narrowed Playwright specs run against the dev server, and they must fail AND
name the test that holds the promise.

It needs node, `ui/shared`'s dependencies and `@playwright/test` with a
browser (the `recovery-captures` job's arrangement). A few minutes.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness

PROGRAM = "assurance ui selftest"

PLANT = "PLANTED VIOLATION (scripts/bdtools/assurance/ui_selftest.py)"

# (label, file under ui/shared, old, new, [unit files], [spec names], needle)
CONTROLS = [
    (
        "an unknown claim rendered with the pass glyph",
        "src/assurance/claims.ts",
        'unknown: { id: "unknown", text: "?" },',
        'unknown: { id: "pass", icon: "success" }, // ' + PLANT,
        ["src/test/assurance.test.tsx", "src/test/assurance-glyphs.test.ts"],
        ["985-claims"],
        "no claim state borrows another state's glyph",
    ),
    (
        "an existence check rendered as content verified",
        "src/assurance/claims.ts",
        'if (cls === "existence") return make("existence_only", true);',
        'if (cls === "existence") return make("pass", true); // ' + PLANT,
        ["src/test/assurance.test.tsx"],
        ["985-claims"],
        "an existence or attested result is never content verification",
    ),
    (
        "a host-scoped readout given a tooltip entry the code cannot state",
        "src/tooltips/tooltips.json",
        '"assurance.col.claims": {',
        '"assurance.host.stagingRoot": {"label": "x", "html": "x"},\n  "assurance.col.claims": {',
        ["src/test/assurance.test.tsx"],
        [],
        "the host-scoped readouts and the strategy select are refused an entry",
    ),
    (
        "validator output displayed with its control bytes live",
        "src/assurance/inert.ts",
        "const control = (c < 0x20 && c !== 0x0a && c !== 0x09) || c === 0x7f || (c >= 0x80 && c <= 0x9f);",
        "const control = false; // " + PLANT,
        ["src/test/assurance.test.tsx"],
        ["985-reconnect-and-log"],
        "replaces every control and window sequence with its visible escape",
    ),
    (
        "a failing scrub that leaves the old Content verified chip on screen",
        "src/assurance/mockAssurance.ts",
        'status: v.state === "failed" ? "failed" : "pass"',
        'status: "pass" /* ' + PLANT + " */",
        [],
        ["985-scrub-and-settings"],
        "a scrub that fails replaces the Content verified chip",
    ),
    (
        "a terminal command echoed that the CLI does not have",
        "src/assurance/assuranceCommands.ts",
        'return "retnd scrub " + setId;',
        'return "retnd scrub-now " + setId; // ' + PLANT,
        ["src/test/assurance-wiring.test.ts"],
        [],
        "assurance-wiring",
    ),
    (
        "a replayed feed merged without de-duplicating by sequence",
        "src/assurance/useOperation.ts",
        "const fresh = incoming.filter((e) => !have.has(e.seq));",
        "const fresh = incoming; // " + PLANT,
        ["src/test/assurance.test.tsx"],
        ["985-reconnect-and-log"],
        "a replay from the start adds nothing the reader already holds",
    ),
    (
        "age text below 4.5:1 against its chip in the light theme",
        "src/assurance/assurance.css",
        ".achip__age { font-weight: 500; color: var(--text-2); }",
        ".achip__age { font-weight: 500; color: #c8cdc6; } /* " + PLANT + " */",
        [],
        ["985-claims"],
        "claim chips meet 4.5:1 text contrast in the light theme",
    ),
]


def run(cmd: list[str], cwd: Path) -> tuple[int, str]:
    proc = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, check=False)
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def suite(copy: Path, units: list[str], specs: list[str]) -> tuple[int, str]:
    out, code = "", 0
    if units:
        c, o = run(["npx", "vitest", "run", *units], copy)
        code, out = code or c, out + o
    if specs:
        c, o = run(["npx", "playwright", "test", "-c", "e2e/playwright.config.ts", *specs], copy)
        code, out = code or c, out + o
    return code, out


def body(root: Path) -> int:
    ui = root / "ui" / "shared"
    if not (ui / "node_modules").is_dir():
        harness.die(f"{ui}/node_modules is missing: run `cd ui/shared && npm ci && npm i --no-save @playwright/test`.")

    tally_ok, tally_bad = 0, 0
    with tempfile.TemporaryDirectory(prefix="retnd-ui-selftest.") as tmp_name:
        tmp = Path(tmp_name)
        # The same shape as the repository, because the bundler resolves
        # @platform-entry to ../../apps/<platform>/frontend/bootstrap.tsx
        # relative to ui/shared.
        copy = tmp / "ui" / "shared"
        copy.parent.mkdir(parents=True)
        shutil.copytree(ui, copy, symlinks=True, ignore=shutil.ignore_patterns("node_modules", "dist", "test-results"))
        os.symlink(ui / "node_modules", copy / "node_modules")
        # The unit tests read Go source (the CLI's usage text, the stage list)
        # at ../../core, so every top-level directory but ui/ is linked in.
        for entry in sorted(root.iterdir()):
            if entry.is_dir() and entry.name not in ("ui", ".git", "node_modules"):
                os.symlink(entry, tmp / entry.name)

        print("==> negative control: the narrowed suites are clean on the real tree")
        units = sorted({u for c in CONTROLS for u in c[4]})
        specs = sorted({s for c in CONTROLS for s in c[5]})
        code, out = suite(copy, units, specs)
        if code != 0:
            print(out[-3000:], file=sys.stderr)
            print(f"exit status {code}; the first failing line of the run is:", file=sys.stderr)
            print(next((l for l in out.splitlines() if "failed" in l.lower() or "error" in l.lower()), "(none)"), file=sys.stderr)
            harness.die("the narrowed suites FAILED against an unmutated tree, so their failures would mean nothing.")
        print("  ok:   (clean): the narrowed unit tests and specs on an unmutated tree")

        for label, rel, old, new, unit_files, spec_names, needle in CONTROLS:
            print()
            print(f"==> {label}")
            target = copy / rel
            original = (ui / rel).read_text()
            if old not in original:
                print(f"  STALE ANCHOR: {rel} no longer contains the text this control plants over", file=sys.stderr)
                tally_bad += 1
                continue
            target.write_text(original.replace(old, new, 1))
            code, out = suite(copy, unit_files, spec_names)
            target.write_text(original)
            if code == 0:
                print(f"  FAIL: the suite PASSED against a planted violation: {label}", file=sys.stderr)
                tally_bad += 1
            elif needle not in out:
                print(f"  FAIL: the suite failed but never named the promise ({needle!r}): {label}", file=sys.stderr)
                print(out[-1500:], file=sys.stderr)
                tally_bad += 1
            else:
                print(f"  ok:   (caught): {label}")
                tally_ok += 1

    if tally_bad:
        print(f"FAIL: {tally_bad} of {len(CONTROLS)} UI controls did not behave as required.", file=sys.stderr)
        return harness.EXIT_FAILED
    print()
    print(f"OK: all {tally_ok} UI controls behaved as required (each guard went red against a real planted violation).")
    return harness.EXIT_OK


def main(argv: list[str]) -> int:
    harness.set_program(PROGRAM)
    harness.install_signal_handlers()
    root = harness.repo_root(Path(__file__))
    return harness.finish(lambda: body(root))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
