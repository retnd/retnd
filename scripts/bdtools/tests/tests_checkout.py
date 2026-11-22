#!/usr/bin/env python3
"""The pinned retnd-tests checkout a gate shard hands the self-tests (#1548).

Three encryption self-test controls run a case of retnd-tests against a retnd
built from a planted copy, so they need RETND_TESTS_DIR and RETND_COMMIT. The
selftest-encryption step gets them from scripts/e2e/tests-checkout.sh, which
reuses the checkout cache and the pin of the e2e gate. Everything here runs
against fixture repositories read over file://, so no network and no real
retnd-tests.

  A  the helper prints a checkout whose HEAD is the pin, and a second run
     reuses it (same path, no fetch).
  B  a pin that is not on main is refused as a failure (exit 1).
  C  a remote that cannot be reached is INCOMPLETE (exit 3), never a failure
     and never a pass.
  D  a pin whose URL carries credentials is refused before any network, so a
     token cannot be put on disk through the pin.
  E  the selftest-encryption step in ci-local.sh calls the helper and exports
     RETND_TESTS_DIR and RETND_COMMIT, and the other self-test steps do not.
"""

from __future__ import annotations

import os
import re
import subprocess
import sys
import tempfile
import time
from pathlib import Path

HERE = Path(__file__).resolve()
SCRIPTS = HERE.parents[2]
sys.path.insert(0, str(SCRIPTS))

from bdtools.tests import pin_on_main as fixtures  # noqa: E402
from bdtools.tests import testkit  # noqa: E402

HELPER = "scripts/bdtools/e2e/tests_checkout.py"


def run(root: Path, cache: Path, **extra: str) -> tuple[str, int]:
    # file:// is only for the fixtures: the helper refuses anything but https unless a test names the scheme.
    env = dict(os.environ, XDG_CACHE_HOME=str(cache), GIT_CONFIG_GLOBAL="/dev/null",
               RETND_TESTS_CHECKOUT_TEST_SCHEMES="file", **extra)
    return testkit.run([sys.executable, str(root / HELPER)], cwd=root, env=env, timeout=300)


def fake_git(base: Path, name: str, on_filtered_fetch: str) -> str:
    """A git on PATH that behaves, except the `--filter=tree:0` fetch require_on_main makes does `on_filtered_fetch`."""
    real = subprocess.run(["sh", "-c", "command -v git"], capture_output=True, text=True, check=True).stdout.strip()
    d = base / name
    d.mkdir()
    g = d / "git"
    g.write_text(
        "#!/bin/sh\n"
        'for a in "$@"; do [ "$a" = "--filter=tree:0" ] && { ' + on_filtered_fetch + " ; }; done\n"
        f'exec {real} "$@"\n'
    )
    g.chmod(0o755)
    return f"{d}{os.pathsep}{os.environ.get('PATH', '')}"


def main() -> int:
    suite = testkit.Suite("tests-checkout (#1548)")
    base = Path(tempfile.mkdtemp(prefix="tests-checkout."))
    fx = fixtures.Fixture(base)
    cache = base / "cache"
    cache.mkdir()

    root = fixtures.sandbox_tree(base, "a", fx, fx.b)
    # the real helper is part of the tree under test
    out, status = run(root, cache)
    path = out.strip().splitlines()[-1] if out.strip() else ""
    suite.check(status == 0 and bool(path) and fixtures.git(Path(path), "rev-parse", "HEAD") == fx.b,
                "A the helper prints a checkout at the pinned sha",
                f"A the helper did not print a checkout at the pin (exit {status})", out)
    out2, status2 = run(root, cache)
    suite.check(status2 == 0 and out2.strip().splitlines()[-1:] == [path],
                "A a second run reuses the same checkout",
                f"A a second run printed another path or failed (exit {status2})", out2)

    root = fixtures.sandbox_tree(base, "b", fx, fx.c)
    out, status = run(root, cache)
    suite.check(status == 1 and "not on main" in out.lower(),
                "B a pin that is not on main is a failure",
                f"B a pin off main exited {status}, not 1", out)

    root = fixtures.sandbox_tree(base, "c", fx, fx.missing)
    pin = root / fixtures.PIN
    pin.write_text(pin.read_text().replace(fx.url, "file://" + str(base / "no-such-repo")), encoding="utf-8")
    out, status = run(root, cache)
    suite.check(status == 3, "C an unreachable remote is INCOMPLETE (3)",
                f"C an unreachable remote exited {status}, not 3", out)

    root = fixtures.sandbox_tree(base, "d", fx, fx.b)
    pin = root / fixtures.PIN
    with_token = "https://user:tok@example.invalid/retnd-tests.git"
    pin.write_text(pin.read_text().replace(fx.url, with_token), encoding="utf-8")
    out, status = run(root, cache)
    suite.check(status == 1 and "credentials" in out.lower(),
                "D a pin URL with credentials is refused before any network",
                f"D a pin URL with credentials exited {status}", out)

    # F (#1633 review): the remote answers ls-remote and then dies or stalls in require_on_main's own fetch.
    root = fixtures.sandbox_tree(base, "f1", fx, fx.b)
    out, status = run(root, base / "cache-f1", PATH=fake_git(base, "g1", "echo 'fatal: 503' >&2; exit 128"),
                      RETND_TESTS_CHECKOUT_GIT_TIMEOUT="20")
    suite.check(status == 3, "F a remote that errors after ls-remote is INCOMPLETE (3), not a failure",
                f"F a remote that errors after ls-remote exited {status}, not 3", out)
    root = fixtures.sandbox_tree(base, "f2", fx, fx.b)
    started = time.monotonic()
    out, status = run(root, base / "cache-f2", PATH=fake_git(base, "g2", "sleep 60"),
                      RETND_TESTS_CHECKOUT_GIT_TIMEOUT="3")
    suite.check(status == 3 and time.monotonic() - started < 40,
                "F a remote that stalls after ls-remote is INCOMPLETE (3) inside the bound",
                f"F a stalled remote exited {status} after {time.monotonic() - started:.0f}s, not 3 in the bound",
                out)

    # G: the pin URL has to be anonymous https (file:// only through the test seam), and an empty or odd sha is refused.
    for label, url in (("scp form", "git@github.com:retnd/retnd-tests.git"),
                       ("ssh scheme", "ssh://github.com/retnd/retnd-tests.git"),
                       ("git scheme", "git://github.com/retnd/retnd-tests.git"),
                       ("empty", "")):
        root = fixtures.sandbox_tree(base, "g-" + label.replace(" ", ""), fx, fx.b)
        pin = root / fixtures.PIN
        pin.write_text(pin.read_text().replace(fx.url, url), encoding="utf-8")
        out, status = run(root, cache)
        suite.check(status == 1, f"G a pin URL in the {label} is refused (1)",
                    f"G a pin URL in the {label} exited {status}, not 1", out)
    root = fixtures.sandbox_tree(base, "g-file", fx, fx.b)
    env = dict(os.environ, XDG_CACHE_HOME=str(cache), GIT_CONFIG_GLOBAL="/dev/null")
    env.pop("RETND_TESTS_CHECKOUT_TEST_SCHEMES", None)
    out, status = testkit.run([sys.executable, str(root / HELPER)], cwd=root, env=env, timeout=120)
    suite.check(status == 1, "G file:// is refused when no test names the scheme",
                f"G file:// was accepted outside the test seam (exit {status})", out)
    for label, sha in (("short", fx.b[:7]), ("non-hex", "z" * 40), ("empty", "")):
        root = fixtures.sandbox_tree(base, "h-" + label, fx, fx.b)
        pin = root / fixtures.PIN
        pin.write_text(pin.read_text().replace(fx.b, sha), encoding="utf-8")
        out, status = run(root, cache)
        suite.check(status == 1, f"G a {label} sha is refused (1)", f"G a {label} sha exited {status}, not 1", out)

    # H: a cache hit is verified. A directory that only looks complete is not the checkout.
    root = fixtures.sandbox_tree(base, "i", fx, fx.b)
    cache_i = base / "cache-i"
    planted = cache_i / "retnd-tests-gate" / f"{fx.b}.src"
    planted.mkdir(parents=True)
    (planted / ".complete").write_text("", encoding="utf-8")
    (planted / "stray").write_text("not the tests", encoding="utf-8")
    out, status = run(root, cache_i)
    path = out.strip().splitlines()[-1] if out.strip() else ""
    ok = status == 0 and bool(path) and fixtures.git(Path(path), "rev-parse", "HEAD") == fx.b
    suite.check(ok, "H a planted directory that only holds .complete is not trusted, the pin is fetched instead",
                f"H a planted .complete directory was trusted or the refetch failed (exit {status})", out)
    suite.check((planted / "stray").exists() is False or Path(path) != planted,
                "H and what was planted is not the path handed to the controls",
                "H the planted directory was handed out")

    text = (SCRIPTS / "ci-local.sh").read_text(encoding="utf-8")
    seg = {m.group(1): m.group(2) for m in re.finditer(r"^(gate_seg_\w+)\(\) \{\n(.*?)^\}\n", text, re.S | re.M)}
    enc = seg.get("gate_seg_selftest_encryption", "")
    suite.check("scripts/e2e/tests-checkout.sh" in enc and "RETND_TESTS_DIR" in enc and "RETND_COMMIT" in enc,
                "E the encryption self-test step gets the checkout and exports it",
                "E the encryption self-test step does not call tests-checkout.sh and export both variables")
    suite.check("tests-checkout.sh" not in seg.get("gate_seg_selftest_agents", ""),
                "E the agents step does not fetch it (no control there needs it)",
                "E the agents step fetches the checkout")
    return suite.finish()


if __name__ == "__main__":
    sys.exit(main())
