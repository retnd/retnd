#!/usr/bin/env python3
"""Flip rows of docs/conformance/epic-u-matrix.md from BLOCKED to PASS, from
the record files scripts/bdtools/agents/selftest.py --record writes (#1353).

    flip-epic-u-rows.py MATRIX --record FILE [--record FILE ...] \
        --row ID [--also LABEL ...] [--row ID ...]

A record line is: stamp, host, commit, rows, label, verdict, command, RED, GREEN.
A row flips only when every record line naming it (and every --also label,
which borrows a control another row owns) says `caught`; the Falsification
cell gains one Run sentence quoting every label, with the hosts, the dates
and the commit. A row with no record line is refused, so nothing flips on
trust. Waits-on is cleared to "-".
"""
import argparse
import sys

ap = argparse.ArgumentParser()
ap.add_argument("matrix")
ap.add_argument("--record", action="append", required=True)
ap.add_argument("--row", action="append", required=True)
ap.add_argument("--also", action="append", default=[], help="LABEL borrowed by the row named just before it")
ap.add_argument("--image", default="retnd-ci:43a1a9aa18d6-u1000")
args = ap.parse_args()

recs = []
for f in args.record:
    for line in open(f):
        p = line.rstrip("\n").split("\t")
        if len(p) >= 9:
            recs.append(dict(stamp=p[0], host=p[1], commit=p[2], rows=p[3].split(), label=p[4], verdict=p[5]))

# --also LABEL@ROW borrows the control labelled LABEL for ROW
borrow = {}
for a in args.also:
    label, _, row = a.rpartition("@")
    borrow.setdefault(row, []).append(label)

lines = open(args.matrix).read().split("\n")
for rid in args.row:
    mine = [r for r in recs if rid in r["rows"]]
    for label in borrow.get(rid, []):
        mine += [r for r in recs if r["label"] == label and r not in mine]
    if not mine:
        sys.exit(f"{rid}: no record line, so nothing was watched")
    bad = [r["label"] for r in mine if r["verdict"] != "caught"]
    if bad:
        sys.exit(f"{rid}: not every control was caught: {bad}")
    seen, labels = set(), []
    for r in mine:
        if r["label"] not in seen:
            seen.add(r["label"])
            labels.append(r)
    def where(h):
        if h.upper() == "MARS":
            return "mars"
        if h.upper() == "HIGARA":
            return "the NAS (HIGARA, in a throwaway container)"
        return "the MacBook (Mac.lan, through the workspace lock)"
    hosts = sorted({where(r["host"]) for r in labels})
    date = min(r["stamp"] for r in labels)[:10]
    commits = sorted({r["commit"].split("+")[0] for r in labels})
    sentence = (
        f'Run {date} on {" and ".join(hosts)}, image {args.image} '
        "(Playwright rows: the pinned Playwright 1.62.1 image), "
        f'commit {", ".join(commits)}, by `scripts/bdtools/agents/selftest.py`: '
        + "; ".join(f'"{r["label"]}"' for r in labels)
        + " each turned its named test red and went green again with the file restored."
    )
    hit = False
    for i, line in enumerate(lines):
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) >= 6 and cells[0] == rid:
            if not cells[4].startswith("BLOCKED"):
                sys.exit(f"{rid} is {cells[4]}, not BLOCKED")
            cells[3] = f"{cells[3]} {sentence}"
            cells[4] = "PASS"
            cells[5] = "-"
            lines[i] = "| " + " | ".join(cells) + " |"
            hit = True
    if not hit:
        sys.exit(f"no row {rid}")
    print(f"{rid}: BLOCKED -> PASS ({len(labels)} controls)")
open(args.matrix, "w").write("\n".join(lines))
