# The local gate, by knob

Since #1260 the pre-commit gate is the only gate: nothing on GitHub runs the
tests any more, and the release workflow publishes only what a run of this
gate has vouched for. This page is the one place that names every variable,
trailer, file and exit status that machinery reads, so a refusal can be
looked up rather than reverse-engineered from a script.

The sources are `scripts/ci-local.sh` (the steps), `scripts/lib/ci-local-gate.sh`
(the verdict, the lock and the Docker helpers), `scripts/release/record-local-gate.sh`
(writes the record a release needs) and `scripts/release/verify-local-gate.sh`
(what the release workflow runs against it). `scripts/tests/local-gate-reference.test.sh`
reads the variable names out of those sources and fails the gate when one is
missing from this page.

## Running it

```
scripts/ci-local.sh                       # the full gate, as pre-commit runs it
scripts/ci-docker.sh                      # the same gate inside scripts/ci/Dockerfile
scripts/release/record-local-gate.sh      # a full run, then provenance/local-gate.json
```

The gate takes a while (the recorder prints the wall time). Start it and go
and do something else.

## Verdicts and exit statuses

The last line of a run is one of three verdicts, and the exit status matches:

| verdict | exit | meaning |
| --- | --- | --- |
| `==> ci-local: ok` | 0 | every step ran and passed; the only verdict the recorder accepts and the only one a commit should carry |
| `==> ci-local: INCOMPLETE` | 3 | nothing failed, but something did not run: a skip variable was set, Docker was missing, a toolchain did not match its pin, a lane could not be judged. The ledger above the verdict lists what. Not merge evidence |
| `==> ci-local: FAILED` | 1 (or whatever the failing command returned) | a step failed |

Only a step that declares itself INCOMPLETE through the ledger may end a run
at 3. A stray exit 3 from a command the gate did not mark is read as FAILED.

## Variables the gate reads

| variable | default | what it does |
| --- | --- | --- |
| `CI_LOCAL` | set to `1` by the gate | tells fixtures they run under the gate: a skip that would be tolerated on a developer's machine is a failure here. Load-bearing in `core/tests`; never rename it |
| `CI` | set to `1` by the gate | the conventional "non-interactive" flag. vitest picks its non-interactive reporter from it, Playwright refuses a `.only` and stops retrying, and `core/tests/machines` keys a refusal on it |
| `CI_LOCAL_FAST` | `0` | `1` skips the Docker suites, the cross-compiles, the production builds, the browser suite and the gate's own self-test. Always INCOMPLETE; for an iteration loop, never for evidence |
| `CI_LOCAL_SKIP_DOCKER` | `0` | `1` runs without a Docker daemon. The Docker-backed steps are ledgered and the run ends INCOMPLETE |
| `CI_LOCAL_SKIP_JS` | `0` | `1` skips the JS workspaces (lint, typecheck, unit tests, builds). INCOMPLETE |
| `CI_LOCAL_SKIP_E2E` | `0` | `1` skips the browser suite and the CLI smoke slice from retnd-tests. INCOMPLETE |
| `CI_LOCAL_SKIP_TWO_MACHINE` | `0` | `1` skips the two-machine end-to-end backup proof (#356). INCOMPLETE |
| `CI_LOCAL_SENTINEL` | `1` | `0` does not start the sentinel container the Docker steps use to prove the daemon is really serving |
| `CI_LOCAL_SENTINEL_IMAGE` | `alpine:3.20` | the image the sentinel runs |
| `CI_LOCAL_SELFTEST` | unset | set to `1` by the gate when it runs its own self-test, so a nested gate does not recurse into another self-test |
| `CI_LOCAL_RED` | unset | a reason of at least 12 characters. Lets `.husky/pre-commit` commit red tests without running the gate, provided every staged path is a test path and the branch is neither `main` nor `release`. The commit is stamped `Local-Gate: red <reason>` |
| `CI_LOCAL_LOCK` | `1` | `0` runs the Docker-backed steps without the gate lock (below) |
| `CI_LOCAL_LOCK_DIR` | `<git common dir>/ci-local.lock` | where the lock lives. Shared by every worktree of a checkout, which is the point |
| `GATE_LOCK_POLL` | `5` | seconds between attempts while waiting for the lock |
| `GATE_LOCK_TIMEOUT` | `7200` | seconds a run waits for the lock before refusing (FAILED, with the holder's pid, host and worktree and the `rm -r` that breaks it) |
| `GATE_LOCK_STALE_AFTER` | `600` | a lock whose holder cannot be judged (another host's pid, a lock from before a reboot, no pid file) is reclaimed once it is this old. A lock whose holder is a dead pid on this host and boot is reclaimed at once |
| `GOLANGCI_LINT_DIR` | unset | a directory to put ahead of `PATH` so a specific golangci-lint wins over Homebrew's |
| `GOLANGCI_LINT_VERSION` | from `scripts/ci/pins.env` | the linter the gate is pinned to. A run with another version is ledgered INCOMPLETE and the release check refuses its record. `scripts/ci-docker.sh` builds the image with it |
| `GOTOOLCHAIN` | derived | the gate exports the Go its golangci-lint was built with, so the linter can read the export data. Set it yourself only to run the whole gate on one toolchain |
| `GOFLAGS` | unset | the recorder refuses a value that narrows tests (`-run`, `-skip`, `-short`, `-count=0`, `-failfast`, `-bench`, `-fuzz`); the gate itself does not read it |
| `RETND_TEST_OWNER` | the worktree's path | the owner stamped on every test container and network (`retnd-test=<hash>`). `dockerlease` sweeps only this owner's leftovers, so one lane never removes another's fixture. Set it to drive two owners from one worktree |
| `PUBLISH` | `false` in the release workflow | `true` makes `verify-local-gate.sh` refuse (exit 1) rather than report. `false` prints a dry run: what would have passed or been refused |

The recorder refuses to start with any `CI_LOCAL_*` set, because every one of
them weakens the run it is about to vouch for.

## The gate lock

The Docker-backed steps of every lane in a checkout run one at a time, under
a lock at `CI_LOCAL_LOCK_DIR` (default `ci-local.lock` in the shared git
dir). The lock is a symlink pointing at a directory beside it that belongs to
the holder alone, `ci-local.lock.<pid>.<checksum>`, holding `pid`, `host`,
`boot` and `where`, so a waiter can say who holds it and judge whether that
holder is still alive. The holder writes its directory first and makes the
symlink with `ln -s`, which is atomic and fails if anything is there, so the
lock is never visible without its holder's identity. Reclaiming a dead
holder means winning the rename of that holder's own directory, which exactly
one waiter can; only the winner removes the dead symlink. A plain directory
at the lock path is a lock an older gate left and is reclaimed by the same
rules. A run releases the lock on every exit path, including a signal.

## Commit trailers

| trailer | written by | meaning |
| --- | --- | --- |
| `Local-Gate: unverified` | `.husky/prepare-commit-msg` | the gate never saw this tree: `--no-verify`, a missing hook, or a working tree that differed from the index when the hook ran |
| `Local-Gate: red <why>` | `.husky/prepare-commit-msg` | a `CI_LOCAL_RED` commit of test files only, with the reason given |
| `Local-Gate: incomplete <what>` | `.husky/prepare-commit-msg` | the gate ran and ended INCOMPLETE; `<what>` is the first ledger entry |
| (no trailer) | | the gate ran on this exact tree and said ok |
| `Release-Gate-Override: <why>` | a person, on a release commit | lets `verify-local-gate.sh` pass a commit whose record does not. At least 12 characters; permanent, because `release` is append-only |

## The record

`scripts/release/record-local-gate.sh` runs the full gate in a clean worktree
of HEAD and, only on `==> ci-local: ok`, writes:

- `provenance/local-gate.json`, schema `retnd.local-gate/1`: `gate_tree` (the
  tree of the commit minus these two files, from `scripts/release/gate-tree.sh`),
  `head`, `result`, the ledger, `started_at`, `duration_s`, the `tools` the
  run printed (`go`, `node`, `golangci_lint`, `docker`), `log_sha256`,
  `web_ui_lane` and the run's `env`;
- `provenance/local-gate.log`, the gate's full output, with home directories
  scrubbed. The recorder refuses to write a log that carries a token.

`scripts/release/verify-local-gate.sh <commit>` is what `release.yml` runs:
the record's `gate_tree` must equal the commit's gate tree, `result` must be
`ok`, the log's hash must match, Go must agree with `core/go.mod` at
major.minor and golangci-lint with `scripts/ci/pins.env`. The schema id is
versioned so a future record shape can be refused by name rather than
misread.

## Installing the hook

`scripts/install-hooks.sh` sets `core.hooksPath=.husky` in the checkout's
shared config, so every worktree runs `.husky/pre-commit` and
`.husky/prepare-commit-msg`. The gate warns at the top of every run when
the hook is not installed, since a hook cannot report its own absence.
