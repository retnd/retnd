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
scripts/ci-docker.sh --host ssh://user@host   # the same, on another machine's Docker daemon (below)
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
| `CI_LOCAL_GITHUB_LEDGER` | set by the gate | a file the gate creates when `gh` is missing or not logged in (`gh auth status` fails), as it is in the pinned container image. `core/tests/compat`'s BLOCKED-row citation check writes each row it could not ask GitHub about there and skips; the gate ledgers every line, so the run ends INCOMPLETE naming the rows. With no BLOCKED row nothing is written and nothing is ledgered. Outside the gate it is unset and a missing `gh` still fails that check |
| `CI_LOCAL_LOCK` | `1` | `0` runs the Docker-backed steps without the gate lock (below) |
| `CI_LOCAL_LOCK_DIR` | `<git common dir>/ci-local.lock` | where the lock lives. Shared by every worktree of a checkout, which is the point |
| `GATE_LOCK_POLL` | `5` | seconds between attempts while waiting for the lock |
| `GATE_LOCK_TIMEOUT` | `7200` | seconds a run waits for the lock before refusing (FAILED, with the holder's pid, host and worktree and the `rm -r` that breaks it) |
| `GATE_LOCK_STALE_AFTER` | `600` | a lock whose holder cannot be judged (another host's pid, a lock from before a reboot, no pid file) is reclaimed once it is this old. A lock whose holder is a dead pid on this host and boot is reclaimed at once |
| `GOLANGCI_LINT_DIR` | unset | a directory to put ahead of `PATH` so a specific golangci-lint wins over Homebrew's |
| `GOLANGCI_LINT_VERSION` | from `scripts/ci/pins.env` | the linter the gate is pinned to. A run with another version is ledgered INCOMPLETE and the release check refuses its record. `scripts/ci-docker.sh` builds the image with it |
| `RUFF_VERSION`, `MYPY_VERSION` | from `scripts/ci/pins.env` | the ruff and mypy `scripts/ci-docker.sh` builds into the gate image (with `python3-yaml`), so the Python lint step runs there instead of being ledgered. The gate itself uses whichever ruff and mypy are on `PATH` |
| `CI_DOCKER_HOST` | unset | `user@host` or `ssh://user@host[:port]`: run `scripts/ci-docker.sh` on that machine's Docker daemon. `--host` on the command line wins. A remote `DOCKER_HOST` without it is refused, because the gate's bind mounts would be empty on the other machine |
| `CI_DOCKER_REMOTE_DIR` | `/work/nas-work` for `devshell@` hosts, else `$HOME/workspace/nas-work` there | the parent of the one scratch directory a remote run uses |
| `CI_DOCKER_MIN_FREE_MB` | `20480` | a remote run refuses, before sending anything, when the scratch parent has less free space than this |
| `CI_DOCKER_KEEP_CACHE` | `0` | `1` keeps the toolchain image and the cache volumes on the remote after the run, for a faster next one. By default a remote run removes what it created |
| `CI_DOCKER_SYNC` | `rsync` | `tar` copies the tree with tar over ssh, for a host that has no rsync or whose rsync cannot write the scratch path (the UGREEN NAS's rsync resolves paths inside the home directory and refuses the copy with `cannot stat destination ... (in home)`, so use `tar` there) |
| `CI_DOCKER_SHARE_HOST` | `auto` | `1` runs the `scripts/ci-docker.sh` container with `--network host` and the daemon host's `/tmp` mounted at `/tmp`; `0` runs it on the default bridge with a private `/tmp`. `auto` picks `0` when `docker info` says Docker Desktop and `1` for any other daemon (below) |
| `GOTOOLCHAIN` | derived | the gate exports the Go its golangci-lint was built with, so the linter can read the export data. Set it yourself only to run the whole gate on one toolchain |
| `GOFLAGS` | unset | the recorder refuses a value that narrows tests (`-run`, `-skip`, `-short`, `-count=0`, `-failfast`, `-bench`, `-fuzz`); the gate itself does not read it |
| `RETND_TEST_OWNER` | the worktree's path | the owner stamped on every test container and network (`retnd-test=<hash>`). `dockerlease` sweeps only this owner's leftovers, so one lane never removes another's fixture. Set it to drive two owners from one worktree |
| `PUBLISH` | `false` in the release workflow | `true` makes `verify-local-gate.sh` refuse (exit 1) rather than report. `false` prints a dry run: what would have passed or been refused |

The recorder refuses to start with any `CI_LOCAL_*` set, because every one of
them weakens the run it is about to vouch for.

## Running the gate on another machine

`scripts/ci-docker.sh --host ssh://user@host` (or `CI_DOCKER_HOST`) sends the
gate to another machine's Docker daemon, so several gates do not all load one
laptop. Pointing `DOCKER_HOST` there by hand does not work: the gate bind
mounts paths under the repo, the daemon reads them from its own disk, and
docker turns a missing path into an empty directory rather than an error. So
the script copies first. It checks the free space, copies the files git knows
about (tracked, plus untracked that are not ignored, minus `.env*`, `.npmrc`,
`.netrc` and `.git-credentials`) and the git dir (without its config and
hooks) into one scratch directory, runs the unchanged gate there as the remote
user, streams the output and returns the gate's exit status. Afterwards it
removes the scratch directory, the toolchain image if the run built it, and
the cache volumes it created (`CI_DOCKER_KEEP_CACHE=1` keeps them). When the
remote's non-interactive `PATH` has no `docker` but `/usr/local/bin`,
`/opt/homebrew/bin`, `/usr/bin` or `/snap/bin` does (MERCURY: Homebrew and
colima), a stand-in `ssh` on this machine's `PATH` calls it by that path for
the run; nothing changes on the remote. Before anything runs, locally or
remotely, the script checks from inside a container that the repository mount
is not empty and refuses with a named error if it is (colima's dockerd can
resolve the Mac's home on its own disk and mount an empty directory instead). No
environment variable is passed to the container and no token is written on the
remote. Only `ssh`, `docker` and writes into the scratch directory happen on
the host. The remote platform defaults to `linux/amd64` (`RCM_CI_PLATFORM`
overrides). The gate lock is per copy, so two remote runs on one host are not
serialized against each other; pick different hosts, or run them one after the
other. `scripts/tests/ci-docker-remote.test.sh` drives all of this with
stand-ins for docker, rsync, tar and ssh.

### Linux daemons

A few things differ between Docker Desktop and a Linux host such as MARS or
the NAS (#1331). `scripts/ci-docker.sh` handles the first four itself, locally
or remotely, and the fixtures handle the last:

- **The socket's group.** Docker Desktop hands the socket into a container
  as `root:root 0660`; a Linux host has it as `root:docker`, with whatever
  gid that machine gave the docker group. The script reads the gid from
  inside a container (`stat -c %g /var/run/docker.sock`) and adds it with
  `--group-add`, so the gate runs as the host user and can still reach the
  daemon. On Docker Desktop that is still group 0.
- **Ports on the host's loopback.** `core/tests/machines` and the SFTP
  fixtures publish ports on the daemon host's `127.0.0.1` and dial them
  there, which a container on the default bridge cannot reach. On a daemon
  that is not Docker Desktop the container gets `--network host`.
- **`/tmp`.** `core/tests/containerhooks` builds its runner layout under a
  short `/tmp` root and bind-mounts it into each hook container, so the
  daemon has to find the same path on its own disk. On a daemon that is not
  Docker Desktop the host's `/tmp` is mounted at `/tmp`, and every test's
  temporary directory lands there; Go removes them as tests finish, but a
  run killed halfway can leave some behind in the host's `/tmp`.

- **The scratch copy's modes.** A Linux account's umask is often 002, which
  left the remote scratch directories group-writable, and the SSH key checks
  refuse a key under any group-writable directory. Remote mode makes them
  under umask 022 and runs `chmod go-w` on them after the copy.
- **The open-file limit.** Debian's `docker.io` hands containers
  `LimitNOFILE=infinity` (1073741816 descriptors). The SFTP fixture's sshd
  closes every descriptor up to that limit before it starts the sftp
  subsystem, so each connection authenticated and then sat for minutes, and
  the SFTP suites looked hung in rclone's connection setup. The fixtures in
  `core/tests/machines` now start their sshd with `--ulimit
  nofile=1024:524288`, the default Docker Desktop and current Docker Engine
  already use.

One difference nothing here can paper over: the UGREEN NAS's home volume
creates every new file as 0777 whatever the umask or the mode asked for (a
later `chmod` does stick). `ssh-keygen` never chmods what it wrote, so the
SFTP fixtures' client keys come out 0777 and the key checks refuse them.
From a scratch copy under `~/workspace/nas-work` the SFTP suites cannot be
judged on the NAS; run them on MARS or MERCURY.

Docker Desktop keeps the bridge and its private `/tmp`: there `--network
host` is the VM's loopback rather than the one ports are published on, and a
`/tmp` mount would cross the file-sharing layer, where unix sockets cannot be
made. `CI_DOCKER_SHARE_HOST` overrides the choice.

The pinned image carries no `gh` and no credential, on purpose, so the check
that asks GitHub whether every BLOCKED conformance row still cites an open
issue cannot run in it. The gate ledgers those rows (`CI_LOCAL_GITHUB_LEDGER`
above) and the run ends INCOMPLETE, never ok, while any row is BLOCKED. Run
that check where `gh` is logged in: `cd core && go test ./tests/compat -run
TestEveryBlockedRowCitesAnIssueThatIsStillOpen`.

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
