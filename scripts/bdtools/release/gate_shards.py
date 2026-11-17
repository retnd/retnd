#!/usr/bin/env python3
"""The sharded half of scripts/release/record-local-gate.sh (#1455, item D).

    gate_shards.py manifest <file>            shard names, one per line
    gate_shards.py run ...                    run, sign and resume shards
    gate_shards.py aggregate ...              write the schema-2 record and its log

The recorder (bash) does the guards, the worktree and the signing of the
aggregate; this does the part that needs a scheduler and JSON.

THE MANIFEST, scripts/release/gate-shards.txt: one shard name per line, in the
order the log is written. Only the first whitespace-separated column counts,
`#` starts a comment, blank lines are skipped, a name may not repeat. A name is
lowercase letters, digits and `-`.

THE RUNNER (RETND_GATE_SHARD_RUNNER, default scripts/release/run-gate-shard.sh
of the recorded tree) is called from the clean worktree as

    <runner> <shard> [--host <host>]

and everything it prints is the shard's log. A shard passed when the runner
exits 0 AND the last `==> ci-local:` line of the log is exactly
`==> ci-local: ok (shard <name>)`. Exit 3 with an INCOMPLETE verdict is
INCOMPLETE, any other exit is FAILED. Optional lines the log may carry:
`==> toolchain: <tool> <version...>` (go, golangci-lint, node, docker) and
`==> web-ui-lane: <one line of JSON>` (the browser lane's result).

THE SIGNING. The Mac signs, nothing else does. Each passing shard gets
`<shard>.json` + `<shard>.json.sig` (ssh-keygen -Y sign, namespace
retnd-local-gate, the namespace allowed_signers already trusts), and the
aggregate carries each shard's exact signed bytes and signature, so the release
check can verify them without any file outside the commit.

Exit status of `run`: 0 every shard is signed, 4 --only left other shards
unsigned (nothing failed), 3 a shard was INCOMPLETE, 1 a shard failed.
"""

from __future__ import annotations

import argparse
import calendar
import concurrent.futures
import hashlib
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path

SHARD_SCHEMA = "retnd.local-gate.shard/1"
AGG_SCHEMA = "retnd.local-gate/2"
NAMESPACE = "retnd-local-gate"
PRINCIPAL = "retnd-release"
NAME_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")

SECRET_SHAPES = [
    (rb"ghp_[A-Za-z0-9]{20,}", "a GitHub personal access token"),
    (rb"github_pat_[A-Za-z0-9_]{20,}", "a GitHub fine-grained token"),
    (rb"gho_[A-Za-z0-9]{20,}", "a GitHub OAuth token"),
    (rb"AKIA[0-9A-Z]{16}", "an AWS access key id"),
    (rb"-----BEGIN [A-Z ]*PRIVATE KEY-----", "a private key block"),
    (rb"(?i)authorization:\s*(bearer|basic)\s+\S+", "an Authorization header"),
    (rb"(?i)x-api-key:\s*\S+", "an API key header"),
]


class GateError(Exception):
    pass


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def now() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def parse_manifest(text: str) -> list[str]:
    names: list[str] = []
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        name = line.split()[0]
        if not NAME_RE.match(name):
            raise GateError(f"gate-shards.txt: `{name}` is not a shard name (lowercase letters, digits and `-`)")
        if name in names:
            raise GateError(f"gate-shards.txt names `{name}` twice")
        names.append(name)
    if not names:
        raise GateError("gate-shards.txt names no shards")
    return names


def scrub(data: bytes) -> bytes:
    """Home paths become `~`; a log that still looks like it holds a secret is
    refused rather than scrubbed, the same rule as the serial recorder."""
    home = os.environ.get("HOME", "").rstrip("/")
    if home:
        data = re.sub(re.escape(home.encode()) + rb"(?=/|[\s\"']|$)", b"~", data)
    data = re.sub(rb"/Users/[^/\s\"']+", b"~", data)
    data = re.sub(rb"/home/[^/\s\"']+", b"~", data)
    found = [what for shape, what in SECRET_SHAPES if re.search(shape, data)]
    if found:
        raise GateError("the log still matches a secret shape after scrubbing: " + ", ".join(found))
    return data


def toolchain(log: bytes) -> dict[str, str]:
    text = log.decode("utf-8", errors="replace")

    def line(tool: str) -> str:
        m = re.search(rf"^==> toolchain: {re.escape(tool)} (.*)$", text, re.MULTILINE)
        return m.group(1).strip() if m else ""

    return {
        "go": line("go").split(" ")[0] if line("go") else "",
        "node": line("node"),
        "golangci_lint": line("golangci-lint").split(" ")[0] if line("golangci-lint") else "",
        "docker": line("docker"),
    }


def lane(log: bytes) -> object:
    m = re.search(rb"^==> web-ui-lane: (\{.*\})\s*$", log, re.MULTILINE)
    if not m:
        return None
    try:
        return json.loads(m.group(1))
    except ValueError:
        return None


def failed_tests(log: bytes) -> list[str]:
    return sorted(set(m.decode() for m in re.findall(rb"^\s*--- FAIL: (\S+)", log, re.MULTILINE)))


def verdict_of(log: bytes) -> str:
    lines = [ln for ln in log.decode("utf-8", errors="replace").splitlines() if ln.startswith("==> ci-local: ")]
    return lines[-1] if lines else ""


def has_ledger(log: bytes) -> bool:
    text = log.decode("utf-8", errors="replace")
    i = text.find("==> ci-local: INCOMPLETE")
    return i >= 0 and "\n        - " in text[i:]


def go_minor(worktree: Path) -> str:
    try:
        m = re.search(r"^go\s+(\d+\.\d+)", (worktree / "core/go.mod").read_text(encoding="utf-8"), re.MULTILINE)
    except OSError:
        return ""
    return m.group(1) if m else ""


def verify_sig(path: Path, signers: Path) -> bool:
    sig = Path(str(path) + ".sig")
    if not sig.is_file():
        return False
    proc = subprocess.run(
        ["ssh-keygen", "-Y", "verify", "-f", str(signers), "-I", PRINCIPAL, "-n", NAMESPACE, "-s", str(sig)],
        input=path.read_bytes(), capture_output=True,
    )
    return proc.returncode == 0


def sign(path: Path, key: str) -> None:
    sig = Path(str(path) + ".sig")
    if sig.exists():
        sig.unlink()
    proc = subprocess.run(["ssh-keygen", "-Y", "sign", "-n", NAMESPACE, "-f", key, str(path)], capture_output=True)
    if proc.returncode != 0 or not sig.is_file():
        raise GateError(f"ssh-keygen could not sign {path.name} with {key}")


def valid_shard(d: Path, shard: str, args: argparse.Namespace, signers: Path) -> str | None:
    """None when `shard` has a record that may be reused, else why not."""
    rec_path, log_path = d / f"{shard}.json", d / f"{shard}.log"
    if not rec_path.is_file() or not log_path.is_file():
        return "no record"
    if not verify_sig(rec_path, signers):
        return "its signature does not verify"
    try:
        rec = json.loads(rec_path.read_text(encoding="utf-8"))
    except ValueError:
        return "its record is not JSON"
    want = {"schema": SHARD_SCHEMA, "shard": shard, "result": "ok", "gate_tree": args.gate_tree,
            "manifest_sha256": args.manifest_sha}
    for key, value in want.items():
        if rec.get(key) != value:
            return f"its {key} is not {value}"
    got = sha256(log_path.read_bytes())
    attempts = rec.get("attempts")
    if rec.get("log_sha256") != got or not attempts or attempts[-1].get("log_sha256") != got:
        return "its log was edited after it was signed"
    minor = go_minor(Path(args.worktree))
    recorded = str(rec.get("tools", {}).get("go", "")).removeprefix("go")
    if recorded and minor and ".".join(recorded.split(".")[:2]) != minor:
        return f"it ran with Go {recorded} and core/go.mod says {minor}"
    return None


def run_attempt(d: Path, shard: str, args: argparse.Namespace, attempts: list[dict]) -> tuple[str, dict]:
    """One run of one shard. Returns (outcome, attempt): ok, failed (a retry may
    help), incomplete, or broken (a retry cannot help)."""
    number = max(a["attempt"] for a in attempts) + 1 if attempts else 1
    cmd = ["bash", args.runner, shard] + (["--host", args.host] if args.host else [])
    started = now()
    t0 = time.time()
    env = dict(os.environ, CI="1")
    proc = subprocess.run(cmd, cwd=args.worktree, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    duration = int(time.time() - t0)
    raw = proc.stdout
    attempt = {"attempt": number, "started_at": started, "host": args.host or "auto", "exit": proc.returncode,
               "duration_s": duration, "log_sha256": "", "failed_tests": failed_tests(raw)}
    try:
        data = scrub(raw)
    except GateError as exc:
        (d / f"{shard}.attempt-{number}.log").write_bytes(b"(withheld: " + str(exc).encode() + b")\n")
        attempt["failed_tests"] = []
        attempt["log_sha256"] = sha256((d / f"{shard}.attempt-{number}.log").read_bytes())
        attempt["exit"] = 1
        print(f"local-gate: shard {shard}: {exc}", file=sys.stderr)
        return "broken", attempt
    attempt["log_sha256"] = sha256(data)
    verdict = verdict_of(data)
    if proc.returncode == 0 and verdict == f"==> ci-local: ok (shard {shard})" and not has_ledger(data):
        (d / f"{shard}.log").write_bytes(data)
        return "ok", attempt
    (d / f"{shard}.attempt-{number}.log").write_bytes(data)
    if proc.returncode == 3 and "INCOMPLETE" in verdict:
        return "incomplete", attempt
    if proc.returncode == 0:
        print(f"local-gate: shard {shard}: the runner exited 0 but the last verdict is [{verdict or 'none'}], not "
              f"[==> ci-local: ok (shard {shard})]", file=sys.stderr)
        attempt["exit"] = 1
        return "broken", attempt
    return ("failed" if attempt["failed_tests"] else "broken"), attempt


def work(shard: str, args: argparse.Namespace, signers: Path) -> tuple[str, str]:
    d = Path(args.dir)
    attempts_path = d / f"{shard}.attempts.json"
    attempts: list[dict] = json.loads(attempts_path.read_text()) if attempts_path.is_file() else []
    # A shard being rerun over a finished record (--only, --force, a record that
    # no longer verifies) starts a new attempts list; the old logs stay.
    for stale in (f"{shard}.json", f"{shard}.json.sig", f"{shard}.log"):
        if (d / stale).exists():
            (d / stale).unlink()
            attempts = []
    outcome = "failed"
    for _ in range(2 if args.retry_once else 1):
        outcome, attempt = run_attempt(d, shard, args, attempts)
        attempts.append(attempt)
        attempts_path.write_text(json.dumps(attempts, indent=2) + "\n")
        print(f"local-gate: shard {shard} attempt {attempt['attempt']}: {outcome} "
              f"(exit {attempt['exit']}) in {attempt['duration_s']}s")
        if outcome != "failed":
            break
    if outcome != "ok":
        return shard, outcome
    log = (d / f"{shard}.log").read_bytes()
    first = attempts[0]["started_at"]
    record = {
        "schema": SHARD_SCHEMA, "shard": shard, "gate_tree": args.gate_tree, "head": args.head,
        "manifest_sha256": args.manifest_sha, "result": "ok", "ledger": [],
        "host": attempts[-1]["host"], "command": f"{os.path.basename(args.runner)} {shard}",
        "started_at": first, "duration_s": sum(a["duration_s"] for a in attempts),
        "tools": toolchain(log), "web_ui_lane": lane(log), "attempts": attempts,
        "log_sha256": sha256(log),
    }
    path = d / f"{shard}.json"
    path.write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    sign(path, args.key)
    return shard, "ok"


def cmd_run(args: argparse.Namespace) -> int:
    names = parse_manifest(Path(args.manifest).read_text(encoding="utf-8"))
    if args.only and args.only not in names:
        print(f"local-gate: REFUSED. --only {args.only} is not a shard in scripts/release/gate-shards.txt "
              f"({', '.join(names)}).", file=sys.stderr)
        return 2
    d = Path(args.dir)
    d.mkdir(parents=True, exist_ok=True)
    signers = Path(args.signers)
    todo: list[str] = []
    for name in names:
        if args.only and name != args.only:
            continue
        why = "forced" if (args.force or args.only) else valid_shard(d, name, args, signers)
        if why is None:
            print(f"local-gate: shard {name}: signed and ok for this tree, reused")
        else:
            todo.append(name)
    results: dict[str, str] = {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, args.jobs)) as pool:
        for shard, outcome in pool.map(lambda s: work(s, args, signers), todo):
            results[shard] = outcome
    bad = {s: o for s, o in results.items() if o != "ok"}
    if bad:
        for shard, outcome in bad.items():
            what = "was INCOMPLETE" if outcome == "incomplete" else "FAILED"
            print(f"local-gate: NOT RECORDED. Shard {shard} {what}; its log is {d}/{shard}.attempt-*.log. "
                  "Rerun the same command to resume: the signed shards are reused.", file=sys.stderr)
        return 1 if any(o != "incomplete" for o in bad.values()) else 3
    unsigned = [n for n in names if valid_shard(d, n, args, signers) is not None]
    if unsigned:
        print("local-gate: no aggregate yet, these shards are not signed for this tree: " + ", ".join(unsigned))
        return 4
    return 0


def cmd_aggregate(args: argparse.Namespace) -> int:
    names = parse_manifest(Path(args.manifest).read_text(encoding="utf-8"))
    d = Path(args.dir)
    signers = Path(args.signers)
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    header = [f"==> local-gate shards (schema {AGG_SCHEMA}), gate_tree {args.gate_tree}", "==> attempts"]
    entries, bodies = [], []
    for name in names:
        why = valid_shard(d, name, args, signers)
        if why:
            raise GateError(f"shard {name} cannot be aggregated: {why}")
        rec_bytes = (d / f"{name}.json").read_bytes()
        rec = json.loads(rec_bytes)
        for a in rec["attempts"]:
            tests = ", ".join(a["failed_tests"])
            header.append(f"    {name} attempt {a['attempt']} on {a['host']}: exit {a['exit']}"
                          + (f", failed: {tests}" if tests else "") + f" ({a['started_at']})")
        entries.append((name, rec, rec_bytes))
        bodies.append((d / f"{name}.log").read_bytes())
    log = ("\n".join(header) + "\n").encode()
    shards = []
    for (name, rec, rec_bytes), body in zip(entries, bodies):
        log += f"==> shard {name} on {rec['host']} ({rec['started_at']})\n".encode()
        offset = len(log)
        log += body
        if not body.endswith(b"\n"):
            log += b"\n"
        shards.append({
            "name": name, "host": rec["host"], "command": rec["command"], "started_at": rec["started_at"],
            "duration_s": rec["duration_s"], "tools": rec["tools"], "gate_tree": rec["gate_tree"],
            "attempts": rec["attempts"], "log_sha256": rec["log_sha256"], "log_offset": offset,
            "log_length": len(body), "record": rec_bytes.decode("utf-8"),
            "signature": (d / f"{name}.json.sig").read_text(encoding="utf-8"),
        })
    (out / "local-gate.log").write_bytes(log)

    def first(key: str) -> str:
        return next((e["tools"][key] for e in shards if e["tools"].get(key)), "")

    lane_value = next((rec["web_ui_lane"] for _, rec, _ in entries if rec.get("web_ui_lane")), None)
    started = min(e["started_at"] for e in shards)
    wall = int(time.time() - calendar.timegm(time.strptime(started, "%Y-%m-%dT%H:%M:%SZ")))
    record = {
        "schema": AGG_SCHEMA, "gate_tree": args.gate_tree, "head": args.head, "result": "ok", "ledger": [],
        "ledger_source": "shard logs", "started_at": started, "duration_s": max(wall, 0),
        "tools": {k: first(k) for k in ("go", "node", "golangci_lint", "docker")},
        "log_sha256": sha256(log), "manifest_sha256": args.manifest_sha, "shards": shards,
        "web_ui_lane": lane_value, "env": json.loads(args.env_json),
        "note": "Written by scripts/release/record-local-gate.sh --shards. Each shard's record was signed on the "
                "owner's machine and travels here with its signature. gate_tree is scripts/release/gate-tree.sh over "
                "the recorded commit: its tree with this file and local-gate.log left out.",
    }
    (out / "local-gate.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    return 0


def main(argv: list[str]) -> int:
    if len(argv) == 2 and argv[0] == "manifest":
        try:
            print("\n".join(parse_manifest(Path(argv[1]).read_text(encoding="utf-8"))))
        except (GateError, OSError) as exc:
            print(f"local-gate: REFUSED. {exc}", file=sys.stderr)
            return 2
        return 0
    ap = argparse.ArgumentParser()
    ap.add_argument("command", choices=["run", "aggregate"])
    for opt in ("manifest", "manifest-sha", "dir", "gate-tree", "head", "worktree", "signers"):
        ap.add_argument(f"--{opt}", required=True)
    ap.add_argument("--runner")
    ap.add_argument("--key")
    ap.add_argument("--host", default="")
    ap.add_argument("--only", default="")
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--retry-once", action="store_true")
    ap.add_argument("--jobs", type=int, default=3)
    ap.add_argument("--out", default="provenance")
    ap.add_argument("--env-json", default="{}")
    args = ap.parse_args(argv)
    try:
        return cmd_run(args) if args.command == "run" else cmd_aggregate(args)
    except GateError as exc:
        print(f"local-gate: NOT RECORDED. {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
