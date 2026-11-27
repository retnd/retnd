#!/usr/bin/env python3
"""The sharded half of scripts/release/record-local-gate.sh (#1455, item D).

    gate_shards.py manifest <file>            shard names, one per line
    gate_shards.py place --manifest <file>    where each shard would run, one per line
    gate_shards.py run ...                    run, sign and resume shards
    gate_shards.py aggregate ...              write the schema-2 record and its log

The recorder (bash) does the guards, the worktree and the signing of the
aggregate; this does the part that needs a scheduler and JSON.

THE MANIFEST, scripts/release/gate-shards.txt: one shard name per line, in the
order the log is written. Only the first whitespace-separated column counts,
`#` starts a comment, blank lines are skipped, a name may not repeat. A name is
lowercase letters, digits and `-`.

THE HOST TABLE, scripts/release/gate-hosts.txt beside the manifest (#1497):
which hosts each shard may run on, in the order it prefers them, whether it is
`exclusive` (one of those per host at a time) and its usual minutes. Its
header has the format and the refusals; docs/reference/gate-scheduler.md has
the rest. --host, --place, --hosts and --drain on the command line win over
it. A tree with no table (older than #1497) runs as before: every shard on
--host, or wherever the runner sends it when there is none.

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
from typing import Any, NamedTuple

SHARD_SCHEMA = "retnd.local-gate.shard/1"
REHEARSAL_SCHEMA = "retnd.local-gate.rehearsal/1"
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


class Refused(GateError):
    """A refusal before anything ran: exit 2, like a usage error."""


REFUSED_HOSTS = {"mercury": "gate work stays off it (benchmarks own it, through bench-queue.sh)"}
CLASSES = ("light", "exclusive")


class Host(NamedTuple):
    name: str
    target: str
    slots: int = 1
    max_load: float | None = None
    min_free_gib: float | None = None
    wait_min: int = 30
    poll_s: int = 30
    scratch: str = ""


HOST_KEYS = ("slots", "max_load", "min_free_gib", "wait_min", "poll_s", "scratch")


class Row(NamedTuple):
    pattern: str
    klass: str
    hosts: tuple[str, ...]
    est_min: int


class Table(NamedTuple):
    hosts: dict[str, Host]
    rows: dict[str, Row]


def parse_hosts(text: str, names: list[str]) -> Table:
    """scripts/release/gate-hosts.txt, checked against the manifest's names."""
    hosts: dict[str, Host] = {}
    rows: dict[str, Row] = {}
    shard_lines: list[tuple[int, list[str]]] = []
    for lineno, raw in enumerate(text.splitlines(), 1):
        fields = raw.split("#", 1)[0].split()
        if not fields:
            continue
        where = f"gate-hosts.txt line {lineno}"
        if fields[0] == "host":
            if len(fields) < 3:
                raise Refused(f"{where}: a host line is `host <name> <ssh target or -> [key=value ...]`")
            name = fields[1]
            if name in REFUSED_HOSTS:
                raise Refused(f"{where}: {name} is refused: {REFUSED_HOSTS[name]}")
            if not NAME_RE.match(name):
                raise Refused(f"{where}: `{name}` is not a host name")
            if name in hosts:
                raise Refused(f"{where}: host {name} is declared twice")
            hosts[name] = parse_host(where, name, fields[2], fields[3:])
        elif fields[0] == "shard":
            shard_lines.append((lineno, fields))
        else:
            raise Refused(f"{where}: `{fields[0]}` is neither `host` nor `shard`")
    for lineno, fields in shard_lines:
        where = f"gate-hosts.txt line {lineno}"
        if len(fields) != 5 or not fields[4].startswith("est_min="):
            raise Refused(f"{where}: a shard line is "
                          "`shard <name or prefix*> <light|exclusive> <host,...> est_min=<n>`")
        pattern, klass, listed, est = fields[1:]
        if not NAME_RE.match(pattern.removesuffix("*")):
            raise Refused(f"{where}: `{pattern}` is not a shard name or a prefix ending in *")
        if pattern in rows:
            raise Refused(f"{where}: shard {pattern} has two lines")
        if klass not in CLASSES:
            raise Refused(f"{where}: class `{klass}` is not one of {', '.join(CLASSES)}")
        seen: list[str] = []
        for h in listed.split(","):
            if h in REFUSED_HOSTS:
                raise Refused(f"{where}: {pattern} names {h}, which is refused: {REFUSED_HOSTS[h]}")
            if h not in hosts:
                raise Refused(f"{where}: {pattern} names host `{h}`, which no host line declares")
            if h in seen:
                raise Refused(f"{where}: {pattern} names {h} twice")
            seen.append(h)
        if not est.removeprefix("est_min=").isdigit():
            raise Refused(f"{where}: est_min must be a whole number of minutes, not `{est}`")
        rows[pattern] = Row(pattern, klass, tuple(seen), int(est.removeprefix("est_min=")))
    table = Table(hosts, rows)
    for pattern in rows:
        if not any(matches(pattern, n) for n in names):
            raise Refused(f"gate-hosts.txt has a line for {pattern}, and gate-shards.txt has no such shard")
    missing = [n for n in names if row_for(table, n) is None]
    if missing:
        raise Refused("gate-hosts.txt has no line for " + ", ".join(missing) + " (every shard needs one)")
    return table


def parse_host(where: str, name: str, target: str, pairs: list[str]) -> Host:
    values: dict[str, str] = {}
    for pair in pairs:
        key, sep, value = pair.partition("=")
        if not sep or key not in HOST_KEYS or key in values:
            raise Refused(f"{where}: `{pair}` is not one of {', '.join(k + '=' for k in HOST_KEYS)} (each once)")
        values[key] = value

    def number(key: str, floor: float) -> float | None:
        if key not in values:
            return None
        try:
            n = float(values[key])
        except ValueError:
            n = floor - 1
        if n < floor:
            raise Refused(f"{where}: {key}={values[key]} is not a number of at least {floor:g}")
        return n

    def whole(key: str, floor: int, default: int) -> int:
        n = number(key, floor)
        if n is not None and n != int(n):
            raise Refused(f"{where}: {key}={values[key]} is not a whole number")
        return default if n is None else int(n)

    return Host(name, target, slots=whole("slots", 1, 1), max_load=number("max_load", 0),
                min_free_gib=number("min_free_gib", 0), wait_min=whole("wait_min", 0, 30),
                poll_s=whole("poll_s", 0, 30), scratch=values.get("scratch", ""))


def probe(host: Host) -> tuple[str, float | None, float | None]:
    """Ask a host whether it can take a shard now: ssh (for a remote), docker,
    its load and the free space under its scratch parent. Returns why not ('' when
    it answered), the 1-minute load and the free GiB. The same commands run
    locally for the Mac (target -), so stand-ins on PATH can play every host."""
    scratch = host.scratch or ('"${TMPDIR:-/tmp}"' if host.target == "-" else "~")
    cmd = f"docker info --format '{{{{.ServerVersion}}}}' >/dev/null && uptime && df -Pk {scratch}"
    if host.target == "-":
        argv, env = ["bash", "-c", cmd], dict(os.environ, GATE_PROBE_HOST=host.name)
    else:
        argv, env = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host.target, cmd], None
    try:
        proc = subprocess.run(argv, capture_output=True, text=True, timeout=90, env=env)
    except subprocess.TimeoutExpired:
        return "the probe got no answer in 90s", None, None
    last = (proc.stderr.strip().splitlines() or [""])[-1]
    if proc.returncode == 255 and host.target != "-":
        return f"ssh {host.target} failed: {last}", None, None
    if proc.returncode != 0:
        return f"docker info or the probe failed (exit {proc.returncode}): {last}", None, None
    m = re.search(r"load averages?:\s*([0-9]+(?:[.,][0-9]+)?)", proc.stdout)
    load = float(m.group(1).replace(",", ".")) if m else None
    free = None
    df_lines = [ln.split() for ln in proc.stdout.splitlines() if ln.strip()]
    if df_lines and len(df_lines[-1]) >= 4 and df_lines[-1][3].isdigit():
        free = int(df_lines[-1][3]) / (1024 * 1024)
    return "", load, free


def busy_reasons(host: Host, load: float | None, free: float | None) -> list[str]:
    out = []
    if host.max_load is not None and load is not None and load > host.max_load:
        out.append(f"load {load:.2f} is over max_load {host.max_load:g}")
    if host.min_free_gib is not None and free is not None and free < host.min_free_gib:
        out.append(f"{free:.1f} GiB free under {host.scratch or 'its scratch parent'} is under min_free_gib "
                   f"{host.min_free_gib:g}")
    return out


def wait_for_host(host: Host, shard: str) -> str:
    """'' once the host is under its limits, else why it was not within wait_min."""
    if host.max_load is None and host.min_free_gib is None:
        return ""
    deadline = time.time() + host.wait_min * 60
    said = False
    while True:
        why, load, free = probe(host)
        if why:
            return f"{host.name} did not answer its probe: {why}"
        busy = busy_reasons(host, load, free)
        if not busy:
            return ""
        if time.time() >= deadline:
            return f"waited {host.wait_min} min for {host.name}, and still " + "; ".join(busy)
        if not said:
            print(f"local-gate: shard {shard} waits for {host.name} (up to {host.wait_min} min): " + "; ".join(busy),
                  flush=True)
            said = True
        time.sleep(host.poll_s)


def matches(pattern: str, name: str) -> bool:
    return name.startswith(pattern[:-1]) if pattern.endswith("*") else name == pattern


def row_for(table: Table, shard: str) -> Row | None:
    """The shard's exact line, else the longest prefix line that covers it."""
    if shard in table.rows:
        return table.rows[shard]
    prefixes = [r for p, r in table.rows.items() if p.endswith("*") and matches(p, shard)]
    return max(prefixes, key=lambda r: len(r.pattern)) if prefixes else None


def host_list(value: str, table: Table, what: str) -> list[str]:
    out = [h for h in value.split(",") if h]
    for h in out:
        if h in REFUSED_HOSTS:
            raise Refused(f"{what} {h}: {h} is refused, {REFUSED_HOSTS[h]}")
        if h not in table.hosts:
            raise Refused(f"{what} {h}: gate-hosts.txt has no host {h} ({', '.join(table.hosts)})")
    return out


def placement(table: Table, names: list[str], args: argparse.Namespace) -> dict[str, list[str]]:
    """Each shard's hosts in the order to try them, after the command line."""
    drain = [h for d in args.drain for h in host_list(d, table, "--drain")]
    only = host_list(args.hosts, table, "--hosts") if args.hosts else []
    forced = host_list(args.host, table, "--host") if args.host else []
    placed: dict[str, list[str]] = {}
    for entry in args.place:
        shard, sep, value = entry.partition("=")
        if not sep or not value:
            raise Refused(f"--place {entry}: write it as <shard>=<host>[,<host>...]")
        if shard not in names:
            raise Refused(f"--place {entry}: {shard} is not a shard in gate-shards.txt")
        placed[shard] = host_list(value, table, "--place")
    plan: dict[str, list[str]] = {}
    homeless: list[str] = []
    for name in names:
        row = row_for(table, name)
        assert row is not None  # parse_hosts refused a shard with no line
        if name in placed:
            hosts = placed[name]
        elif forced:
            hosts = forced
        else:
            hosts = [h for h in row.hosts if not only or h in only]
        hosts = [h for h in hosts if h not in drain]
        if not hosts:
            homeless.append(name)
        plan[name] = hosts
    if homeless:
        raise Refused("no host is left for " + ", ".join(homeless) + ". Give each one with "
                      "--place <shard>=<host>, or drain less.")
    return plan


def load_table(args: argparse.Namespace, names: list[str]) -> Table | None:
    """The table beside the manifest (or --hosts-table), None for a tree without one."""
    path = Path(args.hosts_table) if args.hosts_table else Path(args.manifest).with_name("gate-hosts.txt")
    if not path.is_file():
        if args.hosts_table:
            raise Refused(f"--hosts-table {path} does not exist")
        if args.place or args.hosts or args.drain:
            raise Refused("--place, --hosts and --drain need scripts/release/gate-hosts.txt, and this tree has none")
        return None
    return parse_hosts(path.read_text(encoding="utf-8"), names)


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


PW_SUMMARY = re.compile(r"^\s+\d+ failed$")
PW_SPEC = re.compile(r"^\s{4}(\S+\.spec\.[cm]?[jt]sx?:\d+:\d+) \u203a ")
STALLED = re.compile(r"^==> stall watchdog: STALLED: (.*?)(?: \((?:no output|past|over) .*)?$")


def failed_tests(log: bytes) -> list[str]:
    """What a red shard names as its failures, so a retry can be told from a
    repeat (#1497, #1672 P1.7):
      Go tests        `--- FAIL: <name>` lines of a go test run
      self-tests      `SELFTEST FAIL: <control>. ...`, as `selftest: <control>`
      Playwright      the list reporter's summary under `N failed`, as `spec: <file:line:col>`
      a stalled step  the stall watchdog's STALLED line, as `stalled: <step>`
    A mutation self-test prints the `--- FAIL:` lines of each mutant it caught
    (after `->`), and quotes a mutant's Go output, indented, under a control
    that failed; the watchdog quotes the step's last lines, indented, under its
    STALLED line. None of those is a test of the tree that failed, so none is
    named. A failure with no name (a build, a lint, a guard) gets an empty
    list, and run_attempt does not retry it."""
    names: set[str] = set()
    quoting = summary = False
    for line in log.decode("utf-8", errors="replace").splitlines():
        indented = line[:1] in (" ", "\t")
        if quoting and indented:
            continue
        quoting = False
        if summary:
            m = PW_SPEC.match(line)
            if m:
                names.add(f"spec: {m.group(1)}")
                continue
            summary = False
        if line.startswith("SELFTEST FAIL: "):
            names.add("selftest: " + line[len("SELFTEST FAIL: "):].split(". ", 1)[0].rstrip("."))
            quoting = True
        elif m := STALLED.match(line):
            names.add(f"stalled: {m.group(1)}")
            quoting = True
        elif m := re.match(r"\s*--- FAIL: (\S+)", line):
            names.add(m.group(1))
        elif PW_SUMMARY.match(line):
            summary = True
    return sorted(names)


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


def runner_sha(args: argparse.Namespace) -> str:
    return sha256(Path(args.runner).read_bytes())


def valid_shard(d: Path, shard: str, args: argparse.Namespace, signers: Path) -> str | None:
    """None when `shard` has a record that may be reused, else why not."""
    rec_path, log_path = d / f"{shard}.json", d / f"{shard}.log"
    if not rec_path.is_file() or not log_path.is_file():
        return "no record"
    try:
        rec = json.loads(rec_path.read_text(encoding="utf-8"))
    except ValueError:
        return "its record is not JSON"
    if isinstance(rec, dict) and rec.get("schema") == REHEARSAL_SCHEMA:
        return "it is a rehearsal result, which is never a record"
    if not verify_sig(rec_path, signers):
        return "its signature does not verify"
    want = {"schema": SHARD_SCHEMA, "shard": shard, "result": "ok", "gate_tree": args.gate_tree,
            "manifest_sha256": args.manifest_sha}
    for key, value in want.items():
        if rec.get(key) != value:
            return f"its {key} is not {value}"
    if rec.get("runner_sha256") != runner_sha(args):
        return "it was signed under another runner"
    got = sha256(log_path.read_bytes())
    attempts = rec.get("attempts")
    if rec.get("log_sha256") != got or not attempts or attempts[-1].get("log_sha256") != got:
        return "its log was edited after it was signed"
    minor = go_minor(Path(args.worktree))
    recorded = str(rec.get("tools", {}).get("go", "")).removeprefix("go")
    if not recorded:
        return "its log names no Go toolchain"
    if minor and ".".join(recorded.split(".")[:2]) != minor:
        return f"it ran with Go {recorded} and core/go.mod says {minor}"
    return None


def run_attempt(
    d: Path, shard: str, host: str, args: argparse.Namespace, attempts: list[dict[str, Any]], d_logs: int = 0
) -> tuple[str, dict[str, Any]]:
    """One run of one shard on `host` (empty: wherever the runner sends it).
    Returns (outcome, attempt): ok, failed (a retry may help), incomplete, or
    broken (a retry cannot help)."""
    number = max([a["attempt"] for a in attempts] + [d_logs]) + 1
    cmd = ["bash", args.runner, shard] + (["--host", host] if host else [])
    started = now()
    t0 = time.time()
    env = dict(os.environ, CI="1")
    proc = subprocess.run(cmd, cwd=args.worktree, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    duration = int(time.time() - t0)
    raw = proc.stdout
    attempt: dict[str, Any] = {"attempt": number, "started_at": started, "host": host or "auto",
               "gate_tree": args.gate_tree, "exit": proc.returncode,
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
        if not toolchain(data)["go"]:
            print(f"local-gate: shard {shard}: its log names no Go toolchain (no `==> toolchain: go` line), so the "
                  "Go minor cannot be checked against core/go.mod", file=sys.stderr)
            (d / f"{shard}.attempt-{number}.log").write_bytes(data)
            attempt["exit"] = 1
            return "broken", attempt
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


def waited_out(
    d: Path, shard: str, host: str, args: argparse.Namespace, attempts: list[dict[str, Any]], d_logs: int, why: str
) -> tuple[str, dict[str, Any]]:
    """The attempt for a shard whose host never got under its limits: INCOMPLETE,
    with the reason as its log, and the runner never called."""
    number = max([a["attempt"] for a in attempts] + [d_logs]) + 1
    data = f"==> ci-local: INCOMPLETE (shard {shard}). gate_shards: {why}\n".encode()
    (d / f"{shard}.attempt-{number}.log").write_bytes(data)
    print(f"local-gate: shard {shard}: {why}", file=sys.stderr)
    return "incomplete", {"attempt": number, "started_at": now(), "host": host, "gate_tree": args.gate_tree,
                          "exit": 3, "duration_s": 0, "log_sha256": sha256(data), "failed_tests": []}


def work(shard: str, host: str, info: Host | None, args: argparse.Namespace, signers: Path) -> tuple[str, str]:
    d = Path(args.dir)
    attempts_path = d / f"{shard}.attempts.json"
    attempts: list[dict[str, Any]] = json.loads(attempts_path.read_text()) if attempts_path.is_file() else []
    # Attempts belong to a tree: another tree starts over. Within a tree they
    # only ever grow, so a rerun (--only, --force, a record that no longer
    # verifies) cannot hide an earlier failure, and the numbering skips every
    # attempt log already on disk so none is overwritten.
    attempts = [a for a in attempts if a.get("gate_tree") == args.gate_tree]
    on_disk = [int(m.group(1)) for f in d.glob(f"{shard}.attempt-*.log")
               if (m := re.fullmatch(re.escape(shard) + r"\.attempt-(\d+)\.log", f.name))]
    for stale in (f"{shard}.json", f"{shard}.json.sig", f"{shard}.log"):
        if (d / stale).exists():
            (d / stale).unlink()
    outcome = "failed"
    for _ in range(2 if args.retry_once else 1):
        why = wait_for_host(info, shard) if info else ""
        if why:
            outcome, attempt = waited_out(d, shard, host, args, attempts, max(on_disk, default=0), why)
        else:
            outcome, attempt = run_attempt(d, shard, host, args, attempts, max(on_disk, default=0))
        attempts.append(attempt)
        attempts_path.write_text(json.dumps(attempts, indent=2) + "\n")
        print(f"local-gate: shard {shard} attempt {attempt['attempt']}: {outcome} "
              f"(exit {attempt['exit']}) in {attempt['duration_s']}s")
        if outcome != "failed":
            break
    if args.rehearse:
        log_path = d / f"{shard}.log"
        rehearsal = {
            "schema": REHEARSAL_SCHEMA, "shard": shard, "gate_tree": args.gate_tree, "head": args.head,
            "manifest_sha256": args.manifest_sha, "result": outcome, "host": attempts[-1]["host"],
            "duration_s": attempts[-1]["duration_s"], "attempts": attempts,
            "log_sha256": sha256(log_path.read_bytes()) if outcome == "ok" else "",
            "note": "A rehearsal (gate_shards.py run --rehearse): unsigned, and never reusable as a record.",
        }
        (d / f"{shard}.json").write_text(json.dumps(rehearsal, indent=2) + "\n", encoding="utf-8")
        return shard, outcome
    if outcome != "ok":
        return shard, outcome
    log = (d / f"{shard}.log").read_bytes()
    first = attempts[0]["started_at"]
    record = {
        "schema": SHARD_SCHEMA, "shard": shard, "gate_tree": args.gate_tree, "head": args.head,
        "manifest_sha256": args.manifest_sha, "result": "ok", "ledger": [],
        "host": attempts[-1]["host"], "command": f"{os.path.basename(args.runner)} {shard}",
        "runner_sha256": runner_sha(args),
        "started_at": first, "duration_s": sum(a["duration_s"] for a in attempts),
        "tools": toolchain(log), "web_ui_lane": lane(log), "attempts": attempts,
        "log_sha256": sha256(log),
    }
    path = d / f"{shard}.json"
    path.write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    sign(path, args.key)
    return shard, "ok"


def schedule(
    todo: list[str], plan: dict[str, list[str]], table: Table | None, args: argparse.Namespace, signers: Path
) -> dict[str, str]:
    """Run `todo`, longest first, each on the first host of its plan with a
    free slot. A host runs at most its slots at once and at most one exclusive
    shard. A shard that ends INCOMPLETE is moved (at most twice) only when its
    host then fails its probe, which drains that host for the rest of the run;
    a FAILED shard never moves. Without a table every shard goes to --host (or
    the runner's default) and --jobs (default 3) is the only limit."""
    rows = {n: row_for(table, n) for n in todo} if table else {}

    def est(n: str) -> int:
        r = rows.get(n)
        return r.est_min if r else 0

    def exclusive(n: str) -> bool:
        r = rows.get(n)
        return bool(r and r.klass == "exclusive")

    if table:
        slots = {h: table.hosts[h].slots for h in table.hosts}
        cap = args.jobs if args.jobs > 0 else sum(slots.values())
    else:
        cap = args.jobs if args.jobs > 0 else 3
        slots = {(args.host or ""): cap}
        plan = {n: [args.host or ""] for n in todo}
    pending = sorted(todo, key=lambda n: -est(n))
    count = dict.fromkeys(slots, 0)
    holds_exclusive: set[str] = set()
    drained: set[str] = set()
    moves: dict[str, int] = {}
    results: dict[str, str] = {}
    running: dict[concurrent.futures.Future[tuple[str, str]], tuple[str, str]] = {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, cap)) as pool:
        while pending or running:
            for shard in list(pending):
                if len(running) >= cap:
                    break
                options = [h for h in plan[shard] if h not in drained]
                if not options:
                    pending.remove(shard)
                    results[shard] = "incomplete"
                    print(f"local-gate: shard {shard}: every host it may run on is drained "
                          f"({', '.join(plan[shard])}); give it one with --place {shard}=<host>", file=sys.stderr)
                    continue
                for h in options:
                    if count[h] < slots[h] and not (exclusive(shard) and h in holds_exclusive):
                        info = table.hosts[h] if table else None
                        running[pool.submit(work, shard, h, info, args, signers)] = (shard, h)
                        count[h] += 1
                        if exclusive(shard):
                            holds_exclusive.add(h)
                        pending.remove(shard)
                        break
            if not running:
                continue
            done, _ = concurrent.futures.wait(running, return_when=concurrent.futures.FIRST_COMPLETED)
            for fut in done:
                shard, h = running.pop(fut)
                count[h] -= 1
                if exclusive(shard):
                    holds_exclusive.discard(h)
                outcome = fut.result()[1]
                results[shard] = outcome
                if outcome != "incomplete" or table is None or moves.get(shard, 0) >= 2:
                    continue
                why, _, _ = probe(table.hosts[h])
                if not why:
                    continue
                drained.add(h)
                print(f"local-gate: host {h} drained for the rest of this run: {why}", file=sys.stderr)
                rest = [x for x in plan[shard] if x not in drained]
                if rest:
                    moves[shard] = moves.get(shard, 0) + 1
                    del results[shard]
                    pending.append(shard)
                    pending.sort(key=lambda n: -est(n))
                    print(f"local-gate: shard {shard} moves off {h} (move {moves[shard]} of at most 2); "
                          f"its attempt there stays in the record", file=sys.stderr)
    return results


def rehearsal_summary(d: Path, names: list[str], results: dict[str, str], args: argparse.Namespace) -> int:
    print(f"local-gate: rehearsal of gate tree {args.gate_tree}, results in {d}")
    for name in (n for n in names if n in results):
        rec = json.loads((d / f"{name}.json").read_text(encoding="utf-8")) if (d / f"{name}.json").is_file() else {}
        print(f"    {name:<22} {results[name]:<11} on {rec.get('host', '?'):<6} {rec.get('duration_s', '?')}s")
    counts = {o: sum(1 for r in results.values() if r == o) for o in ("ok", "failed", "broken", "incomplete")}
    print(f"local-gate: REHEARSAL on {args.gate_tree[:12]}, not a record: {counts['ok']} ok, "
          f"{counts['failed'] + counts['broken']} failed, {counts['incomplete']} incomplete. Nothing was signed.")
    if counts["failed"] or counts["broken"]:
        return 1
    return 3 if counts["incomplete"] else 0


def cmd_run(args: argparse.Namespace) -> int:
    names = parse_manifest(Path(args.manifest).read_text(encoding="utf-8"))
    if args.only and args.only not in names:
        print(f"local-gate: REFUSED. --only {args.only} is not a shard in scripts/release/gate-shards.txt "
              f"({', '.join(names)}).", file=sys.stderr)
        return 2
    table = load_table(args, names)
    plan = placement(table, names, args) if table else {n: [args.host] if args.host else [] for n in names}
    d = Path(args.dir)
    if args.rehearse:
        # Beside the shards, never in them: <git dir>/local-gate/rehearsal/<tree>.
        if d.parent.name != "shards" or d.name != args.gate_tree:
            raise Refused(f"--rehearse needs --dir <git dir>/local-gate/shards/<tree> to find the rehearsal dir "
                          f"beside it, and {d} is not that")
        d = d.parent.parent / "rehearsal" / args.gate_tree
        args.dir = str(d)
    elif not args.key:
        raise Refused("--key is needed to sign the shards (only --rehearse runs without one)")
    d.mkdir(parents=True, exist_ok=True)
    signers = Path(args.signers)
    todo: list[str] = []
    for name in names:
        if args.only and name != args.only:
            continue
        why = "forced" if (args.force or args.only or args.rehearse) else valid_shard(d, name, args, signers)
        if why is None:
            print(f"local-gate: shard {name}: signed and ok for this tree, reused")
        else:
            todo.append(name)
    results = schedule(todo, plan, table, args, signers)
    if args.rehearse:
        return rehearsal_summary(d, names, results, args)
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


def add_placement_options(ap: argparse.ArgumentParser) -> None:
    ap.add_argument("--hosts-table", default="")
    ap.add_argument("--host", default="")
    ap.add_argument("--place", action="append", default=[])
    ap.add_argument("--hosts", default="")
    ap.add_argument("--drain", action="append", default=[])


def cmd_place(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(prog="gate_shards.py place")
    ap.add_argument("--manifest", required=True)
    add_placement_options(ap)
    args = ap.parse_args(argv)
    names = parse_manifest(Path(args.manifest).read_text(encoding="utf-8"))
    table = load_table(args, names)
    if table is None:
        for name in names:
            print(f"{name} - {args.host or 'auto'}")
        return 0
    for name, hosts in placement(table, names, args).items():
        row = row_for(table, name)
        assert row is not None
        print(f"{name} {row.klass} {','.join(hosts)} est_min={row.est_min}")
    return 0


def cmd_probe(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(prog="gate_shards.py probe")
    ap.add_argument("--manifest", required=True)
    add_placement_options(ap)
    args = ap.parse_args(argv)
    names = parse_manifest(Path(args.manifest).read_text(encoding="utf-8"))
    table = load_table(args, names)
    if table is None:
        print("no gate-hosts.txt beside the manifest: nothing to probe")
        return 0
    used = {h for hosts in placement(table, names, args).values() for h in hosts}
    refused = 0
    for name in (h for h in table.hosts if h in used):
        host = table.hosts[name]
        why, load, free = probe(host)
        if why:
            refused += 1
            print(f"{name} refuse {why}")
            continue
        low = host.min_free_gib is not None and free is not None and free < host.min_free_gib
        busy = busy_reasons(host, load, None)
        facts = f"load={load if load is not None else '?'} free_gib={f'{free:.1f}' if free is not None else '?'}"
        if low:
            refused += 1
            print(f"{name} refuse {free:.1f} GiB free under {host.scratch or 'its scratch parent'}, min_free_gib "
                  f"{host.min_free_gib:g} ({facts})")
        else:
            print(f"{name} ok {facts}" + (" (busy: " + "; ".join(busy) + ")" if busy else ""))
    return 1 if refused else 0


def main(argv: list[str]) -> int:
    if len(argv) == 2 and argv[0] == "manifest":
        try:
            print("\n".join(parse_manifest(Path(argv[1]).read_text(encoding="utf-8"))))
        except (GateError, OSError) as exc:
            print(f"local-gate: REFUSED. {exc}", file=sys.stderr)
            return 2
        return 0
    if argv[:1] in (["place"], ["probe"]):
        try:
            return (cmd_place if argv[0] == "place" else cmd_probe)(argv[1:])
        except (GateError, OSError) as exc:
            print(f"local-gate: REFUSED. {exc}", file=sys.stderr)
            return 2
    ap = argparse.ArgumentParser()
    ap.add_argument("command", choices=["run", "aggregate"])
    for opt in ("manifest", "manifest-sha", "dir", "gate-tree", "head", "worktree", "signers"):
        ap.add_argument(f"--{opt}", required=True)
    ap.add_argument("--runner")
    ap.add_argument("--key")
    add_placement_options(ap)
    ap.add_argument("--only", default="")
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--retry-once", action="store_true")
    ap.add_argument("--rehearse", action="store_true")
    ap.add_argument("--jobs", type=int, default=0)
    ap.add_argument("--out", default="provenance")
    ap.add_argument("--env-json", default="{}")
    args = ap.parse_args(argv)
    try:
        return cmd_run(args) if args.command == "run" else cmd_aggregate(args)
    except Refused as exc:
        print(f"local-gate: REFUSED. {exc}", file=sys.stderr)
        return 2
    except GateError as exc:
        print(f"local-gate: NOT RECORDED. {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
