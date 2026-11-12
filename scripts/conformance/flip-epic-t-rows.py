#!/usr/bin/env python3
"""Flip EPIC T matrix rows from BLOCKED (#1358) to PASS from a falsification log (#1358).

The falsifications are not in this file. They are the controls in
scripts/bdtools/encryption/selftest.py (the tree on cut/0.8.0), which plants
each violation in the real product file, needs the narrowed test red naming
itself, and needs it green again with the file restored. Run it once, on the
scratch branch that holds every PR's code, and keep the log:

    bash scripts/encryption/selftest.sh --record /path/to/falsification.log

Then this script reads that log and does the edit to the matrix and to the
spec's exit-gate boxes, so the stage after the run is mechanical:

    python3 scripts/conformance/flip-epic-t-rows.py --log /path/to/falsification.log \\
        --image retnd-ci:<tag> [--dry-run]

What it flips, and what it refuses to:

  * A row flips only if it is BLOCKED (#1358) today and EVERY control
    scripts/bdtools/encryption declares for it is in the log, `caught`, with a
    green run after. A partial run (`--only`) therefore flips nothing whose
    other controls were not tried. A row with no control (T2.5) or whose
    controls were not run (T2.3 and T2.4 need a browser or a second
    repository) stays BLOCKED.
  * Every quoted control label must occur verbatim in a file that
    core/tests/encryptiongate/matrix_test.go lists in `selfTests`, because the
    guard refuses a PASS whose label it cannot find. If the list does not name
    selftest.py yet, the row is left alone and the script says why.
  * Every backticked path in the row's proof cell must exist, because
    TestTheMatrixDoesNotCiteSuitesThatDoNotExist resolves them for PASS rows.
  * TC-05 ("one guard per downgrade path") flips when TV.1 to TV.19 all did,
    since its proof is the ledger.

A flipped row gets the run appended to its falsification cell (date, host,
image, commit, label, test, what the red said), its status becomes
`PASS (<date>)`, and the matching `- [ ]` under the spec's phase exit gate is
ticked, since TestTheSpecsExitGateBoxesAgreeWithTheMatrix wants the two to
agree. Nothing else is touched. Rows stay BLOCKED (#1358) until this runs.
"""

from __future__ import annotations

import argparse
import importlib.util
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
SELFTEST = "scripts/bdtools/encryption/selftest.py"
BLOCKED = ("BLOCKED (#1358)", "BLOCKED (#1376)")
PHASE_HEADINGS = {"T1": "### Phase 1 exit gate", "T2": "### Phase 2 exit gate"}
LOG_FIELDS = 9  # stamp host commit rows label verdict cmd RED: GREEN:


def parse_log(path: Path) -> list[dict[str, str]]:
    out = []
    for n, line in enumerate(path.read_text().splitlines(), 1):
        if not line.strip():
            continue
        f = line.split("\t")
        if len(f) < LOG_FIELDS:
            sys.exit(f"{path}:{n}: expected {LOG_FIELDS} tab-separated fields, got {len(f)}")
        out.append(dict(zip(
            ("stamp", "host", "commit", "rows", "label", "verdict", "cmd", "red", "green"), f[:LOG_FIELDS])))
    return out


def declared_controls() -> dict[str, list[str]]:
    """Every control scripts/bdtools/encryption declares, by row id, so a row
    is judged against all of its controls and not only the ones a log holds."""
    sys.path.insert(0, str(REPO / "scripts"))
    spec = importlib.util.spec_from_file_location("encryption_selftest", REPO / SELFTEST)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    out: dict[str, list[str]] = {}
    for c in mod.CONTROLS:
        for rid in c.rows:
            out.setdefault(rid, []).append(c.label)
    return out


def tests_of(cmd: str) -> list[str]:
    m = re.search(r"-run <([^>]*)>", cmd)
    if m:
        return [t for t in m.group(1).split(",") if t]
    # a browser spec or a checker script, not a Go test (T2.3)
    f = re.search(r"(e2e/[\w.-]+\.spec\.ts|scripts/[\w./-]+\.(?:py|sh))", cmd)
    return [f.group(1)] if f else []


def excerpt(red: str) -> str:
    # "RED: --- FAIL: TestX; --- FAIL: TestY | foo_test.go:12: detail"
    detail = red.split(" | ", 1)[1] if " | " in red else ""
    detail = re.sub(r"^\S+_test\.go:\d+:\s*", "", detail).strip()
    # Single quotes and no backticks: the guard reads double-quoted runs as mutation
    # labels and backticked ones as paths, so an excerpt must not make either.
    return detail.replace("|", "/").replace('"', "'").replace("`", "")[:200]


def self_test_files() -> list[str]:
    src = (REPO / "core/tests/encryptiongate/matrix_test.go").read_text()
    block = re.search(r"var selfTests = \[\]string\{(.*?)\n\}", src, re.S)
    return re.findall(r'"([^"]+)"', block.group(1)) if block else []


def label_listed(label: str, files: list[str]) -> bool:
    return any((REPO / f).exists() and label in (REPO / f).read_text() for f in files)


def proofs_resolve(cell: str) -> list[str]:
    bad = []
    for tok in re.findall(r"`([^`]+)`", cell):
        if not re.fullmatch(r"[A-Za-z0-9_./-]+(:Test[A-Za-z0-9_]+)?", tok) or not ("/" in tok or ":" in tok):
            continue
        if not (REPO / tok.split(":")[0]).exists():
            bad.append(tok)
    return bad


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--log", required=True, type=Path, help="the --record file from selftest.sh")
    ap.add_argument("--image", required=True, help="the pinned image the run used, e.g. retnd-ci:<tag>")
    ap.add_argument("--host", help="the machine's name, e.g. mars; the log only holds the container's hostname")
    ap.add_argument("--matrix", type=Path, default=REPO / "docs/conformance/epic-t-matrix.md")
    ap.add_argument("--spec", type=Path, default=REPO / "docs/EPIC-T-encryption-suites.md")
    ap.add_argument("--dry-run", action="store_true", help="say what would change, write nothing")
    args = ap.parse_args()

    # Latest line per label wins, so a re-run replaces an earlier red.
    latest: dict[str, dict[str, str]] = {}
    for rec in parse_log(args.log):
        latest[rec["label"]] = rec
    by_row: dict[str, list[dict[str, str]]] = {}
    for rec in latest.values():
        for rid in rec["rows"].split():
            by_row.setdefault(rid, []).append(rec)

    files = self_test_files()
    declared = declared_controls()
    lines = args.matrix.read_text().split("\n")
    flipped: list[str] = []
    skipped: list[str] = []

    def eligible(rid: str, cell_proof: str) -> str:
        recs = by_row.get(rid, [])
        want = declared.get(rid, [])
        if not want:
            return "no control declared for it"
        untried = [lab for lab in want if lab not in latest]
        if untried:
            return f'{len(untried)} of its {len(want)} controls are not in the log, first "{untried[0]}"'
        for r in recs:
            green = r["green"].removeprefix("GREEN:").strip()
            if r["verdict"] != "caught" or green == "" or green == "-" or green.startswith("red"):
                return f'control "{r["label"]}" is {r["verdict"]}, green: {green or "-"}'
            if not label_listed(r["label"], files):
                return f'label "{r["label"]}" is in none of selfTests ({", ".join(files)}); matrix_test.go must list {SELFTEST}'
        if (miss := proofs_resolve(cell_proof)):
            return "proof cell cites paths that do not exist: " + ", ".join(miss)
        return ""

    rows: dict[str, int] = {}
    for i, line in enumerate(lines):
        cells = [c.strip() for c in line.strip().strip("|").split("|")] if line.startswith("| ") else []
        if len(cells) >= 6 and re.fullmatch(r"T[12]\.\d+|TC-\d\d|TV\.\d+", cells[0]):
            rows[cells[0]] = i

    def flip(rid: str, evidence: str, date: str) -> None:
        i = rows[rid]
        parts = lines[i].split(" | ")
        # ID | Claim | Proof | Falsification | Status | Waits on  (cells may not contain " | ")
        if len(parts) != 6:
            sys.exit(f"{rid}: row does not split into six cells, refusing to edit it")
        cell = parts[3].rstrip()
        parts[3] = cell + ("" if cell.endswith((".", "!", "?", ":", ";")) else ".") + " " + evidence
        parts[4] = f"PASS ({date})"
        lines[i] = " | ".join(parts)

    for rid in rows:
        if rid == "TC-05":
            continue
        line = lines[rows[rid]]
        if not any(b in line for b in BLOCKED):
            continue
        cells = line.split(" | ")
        why = eligible(rid, cells[2] if len(cells) > 2 else "")
        if why:
            skipped.append(f"{rid}: {why}")
            continue
        recs = by_row[rid]
        date = max(r["stamp"] for r in recs)[:10]
        host = ", ".join(sorted({r["host"] for r in recs}))
        if args.host:
            host = f"{args.host} (container {host})"
        commit = ", ".join(sorted({r["commit"][:12] for r in recs}))
        said = "; ".join(
            f'"{r["label"]}" turned `{"`, `".join(tests_of(r["cmd"])) or "its build"}` red'
            + (f" ({excerpt(r['red'])})" if excerpt(r["red"]) else "")
            for r in sorted(recs, key=lambda r: r["label"]))
        flip(rid, f"Run {date} on {host} in the pinned image {args.image}, commit {commit}, by "
                  f"`{SELFTEST}`: {said}. Each went green again with the file restored.", date)
        flipped.append(rid)

    ledger = [r for r in rows if r.startswith("TV.")]
    if "TC-05" in rows and any(b in lines[rows["TC-05"]] for b in BLOCKED):
        if ledger and all(r in flipped or "PASS" in lines[rows[r]].split(" | ")[4] for r in ledger):
            ids = ", ".join(sorted(ledger, key=lambda r: int(r.split(".")[1])))
            eg = [latest[lab]["label"] for r in sorted(ledger, key=lambda r: int(r.split(".")[1])) for lab in [x["label"] for x in by_row.get(r, [])][:1]][:2]
            quoted = " and ".join('"' + lab + '"' for lab in eg)
            last = max(r["stamp"] for rr in by_row.values() for r in rr)[:10]
            flip("TC-05", f"Every `TV.n` guard was run and watched to fail ({ids}), for example {quoted}; the last of those runs was {last}.", last)
            flipped.append("TC-05")
        else:
            skipped.append("TC-05: some TV.n row is still not PASS")

    # Tick the spec's exit-gate box for each flipped gate row.
    spec = args.spec.read_text().split("\n")
    for rid in flipped:
        if rid.startswith("TC-") or rid.startswith("TV."):
            continue
        phase, n = rid.split(".")
        inside, seen = False, 0
        for j, line in enumerate(spec):
            t = line.strip()
            if t == PHASE_HEADINGS[phase]:
                inside = True
                continue
            if inside and t.startswith("#"):
                break
            if inside and (t.startswith("- [ ]") or t.startswith("- [x]")):
                seen += 1
                if seen == int(n):
                    spec[j] = line.replace("- [ ]", "- [x]", 1)
                    break

    print(f"flip {len(flipped)}: {' '.join(flipped) or '-'}")
    for s in skipped:
        print(f"stay BLOCKED (#1358) - {s}")
    if args.dry_run:
        print("dry run, nothing written")
        return 0
    args.matrix.write_text("\n".join(lines))
    args.spec.write_text("\n".join(spec))
    return 0


if __name__ == "__main__":
    sys.exit(main())
