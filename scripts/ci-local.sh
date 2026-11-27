#!/usr/bin/env sh
# The gate. The only one (#1260).
#
# This script, run entirely on this machine, is what stands in front of
# `main` and in front of a release. GitHub Actions gates nothing here any
# more: ci.yml, which used to mirror this file job for job and run on a
# pull request into `release`, is gone, and nothing on that side produces
# a check that could be mistaken for one. The pre-commit hook
# (.husky/pre-commit, installed by scripts/install-hooks.sh) runs this on
# every commit, and a release is refused unless a full green run of it is
# recorded on the tree being published (scripts/release/record-local-gate.sh
# writes that record, require-release-gate in release.yml reads it).
#
# Comprehensive on purpose, which means it is NOT
# fast: the full core/ test suite (including the Docker-backed crash matrix,
# SFTP integration and MinIO integration tests), two cross-compiles, three separate frontend
# installs/builds, and the dependency-rules worktree-deletion proofs. Needs
# a running Docker daemon for the full run, and now says so instead of
# quietly reporting on suites that skipped themselves.
#
# Set CI_LOCAL_FAST=1 for a quick iteration loop. It skips core/'s
# ./tests/... (the crash matrix, the SFTP integration tests and the MinIO
# integration tests), both
# cross-compiles, the ui/shared and upk-proof production builds, the
# apps/common/tests conformance suite, the structure proofs and this gate's
# own self-test. It does NOT skip apps/generic, whose own tests bring a
# compose stack up, so a FAST run is not a Docker-free run. A FAST run
# always ends INCOMPLETE; never rely on it before a merge.
#
# The JS workspaces (ui/shared, apps/common/tests, and
# apps/ugos/frontend/upk-proof where it exists) need their dependencies
# installed before this script can check them. node_modules/ is gitignored,
# so a fresh clone or a new `git worktree` has none, and until issue #160 a
# full run quietly skipped every check that needed them and still finished
# with "ci-local: ok". It no longer does. A full run now refuses to start
# until every JS workspace that is in the tree is installed, and names the
# command that fixes each one. Set CI_LOCAL_SKIP_JS=1 to leave them out on
# purpose: that run, like a FAST one, ends with INCOMPLETE instead of ok,
# because it is not merge evidence.
#
# The Docker daemon is the same story with a bigger blast radius: with it
# down, the crash matrix, the SFTP and MinIO integration suites and the whole
# apps/generic/tests/dockercli package call t.Skip, go test still exits 0,
# and nothing would reach the ledger. A full run refuses to start without
# the daemon; CI_LOCAL_SKIP_DOCKER=1 is the out-loud opt-out and ends
# INCOMPLETE.
#
# The daemon being up at the START is not the same claim as the daemon
# being up for the RUN, and on a Mac they come apart on their own. Docker
# Desktop's Resource Saver stops the hypervisor after five idle minutes,
# and this script has several Docker-free stretches longer than that (the
# compat self-test, the api, perf and architecture self-tests), so every
# run used to cold-start the VM somewhere in the middle. Two runs died of
# that on 2026-09-04 and neither looked like a failure, because a daemon
# that goes away at minute 18 is invisible to a probe that ran at minute 0
# and turns every Docker-backed suite into a t.Skip (#457). So: a sentinel
# container sleeps for the life of the run, which means the daemon is
# never idle and Resource Saver never fires, and every Docker-dependent
# step re-probes the daemon before it runs, which costs about 100ms and
# turns a VM that died mid-run into a named failure at the step that
# needed it. CI_LOCAL_SENTINEL=0 turns the container off;
# CI_LOCAL_SENTINEL_IMAGE picks a different one.
#
# A missing Playwright Chromium is the third instance of the same shape,
# and gets the same answer: the browser e2e step refuses and names the
# install command, and CI_LOCAL_SKIP_E2E=1 is the out-loud opt-out that
# ledgers. See scripts/e2e/run-tests-repo-gate.sh and the gate it execs,
# scripts/bdtools/e2e/run_tests_repo_gate.py, which is where that
# suite now runs from (#158 moved it to retnd/retnd-tests, #197
# is why it runs at all).
#
# The two-machine end-to-end backup proof (#356) is the fourth, with one
# extra state. Docker being absent is the same shape as everything above.
# A Docker daemon that is present and refuses a PRIVILEGED container is a
# capability question too: the manager machine is docker-in-docker, because
# mounting the host socket instead would install onto the developer's own
# host rather than onto a fresh machine. The script says CANNOT RUN and
# exits 3 for both, this gate ledgers that, and CI_LOCAL_SKIP_TWO_MACHINE=1
# is the out-loud opt-out.
#
# Every `go test` in this script carries -race (#417), with one named
# exception that says why on its own line (distribution/packaging, below).
# It is a flag on the steps that already exist rather than a step of its
# own, for two reasons.
# A separate step would run the same suites twice, and -race replaces
# nothing: everything the plain run asserted, the instrumented run asserts
# too, plus the detector. And a separate step is one more thing that can be
# commented out, skipped or quietly reordered, which is the failure this
# gate is built around (#160); a flag on the step that has to run anyway
# cannot be left out without deleting the step. It also settles what a
# detected race does: the step it is on is the step this gate already had
# to run, so a race is a red suite and a red suite is a FAILED verdict
# naming it. There is no opt-out variable, and no ledger entry, because a
# ledgered race would be this gate reporting on a defect it decided not to
# act on.
#
# Measured on this machine rather than estimated, warm cache, with five
# other worktrees running their own suites at the time, so the pairs are
# the reading and not the absolutes:
#
#   core/ minus the four Docker-backed suites   135s, 128s -> 174s, 177s
#   those four, under gotestwatch                     143s -> 147s
#   distribution minus packaging                       69s ->  64s
#   apps/generic                                       43s ->  51s
#   apps/synology                                       9s ->  17s
#   apps/common                                         6s ->  31s
#   distribution/packaging (left out)                  44s -> 521s
#
# About ninety seconds added on a gate that runs for twenty-five. The four
# Docker-backed suites were the ones worth measuring before committing
# them, and they turned out to be the cheapest: they wait on containers and
# on a real rclone, so the instrumentation is nearly free.
#
# Three outcomes, three exit statuses, so a wrapper does not have to parse
# prose: 0 for "ci-local: ok", 3 for "ci-local: INCOMPLETE", and whatever
# failed for "ci-local: FAILED". Nothing this script runs may leave as 3
# for any other reason: golangci-lint does (see gate_lint), so its
# invocations are wrapped.
#
# Parallel lanes (#1260): about thirty worktrees share this machine and one
# Docker daemon. The Docker-backed steps run under a lock in the shared
# git dir (gate_lock_acquire), so concurrent full runs queue rather than
# collide, and golangci-lint is told to allow parallel runners.
#
# A workspace that is not in the tree at all is a different thing from an
# uninstalled one, and is never a failure. apps/ugos/backend and
# apps/ugos/frontend/upk-proof are optional components that are not in this
# tree today; when a component is absent its checks are inapplicable, not
# skipped, and the run can still legitimately be ok.

set -e

# Shards (#1457). With no argument this runs the whole gate, as it always has.
#   --list-shards    print the shard names in scripts/release/gate-shards.txt
#   --shard <name>   run only the steps that belong to that shard
# Both are read before anything else happens, so asking for the list costs
# nothing and a name that is not in the manifest is refused before a single
# step runs. GATE_SHARD is set here, ahead of the library, so a value in the
# caller's environment never selects a shard by accident.
GATE_SHARD=""
gate_manifest="$(cd "$(dirname "$0")" && pwd)/release/gate-shards.txt"
gate_shard_names() { grep -vE '^[[:space:]]*(#|$)' "$gate_manifest"; }
case "${1:-}" in
  "") ;;
  --list-shards)
    gate_shard_names
    exit 0
    ;;
  --shard)
    GATE_SHARD="${2:-}"
    [ "$#" -eq 2 ] && [ -n "$GATE_SHARD" ] || { echo "ci-local.sh: --shard takes exactly one shard name" >&2; exit 2; }
    ;;
  *)
    echo "ci-local.sh: unknown argument [$1]. Usage: ci-local.sh [--shard <name> | --list-shards]" >&2
    exit 2
    ;;
esac
if [ -n "$GATE_SHARD" ] && [ "${CI_LOCAL_FAST:-0}" = 1 ]; then
  echo "ci-local.sh: --shard and CI_LOCAL_FAST=1 do not combine (the fast tier has no package split)" >&2
  exit 2
fi
if [ -n "$GATE_SHARD" ] && ! gate_shard_names | grep -qxF -- "$GATE_SHARD"; then
  echo "ci-local.sh: [$GATE_SHARD] is not a shard. The shards in scripts/release/gate-shards.txt are:" >&2
  gate_shard_names | sed 's/^/    /' >&2
  exit 2
fi

# Homebrew's tools have to be findable on a machine whose shell profile did
# not run (a hook started from a GUI client), so /opt/homebrew/bin is APPENDED
# when the caller's PATH doesn't have it. Never prepended, and a PATH that has
# it is left alone: that is an order somebody chose. Putting Homebrew first
# put brew's docker ahead of the gate self-test's stub daemon, so every docker
# case in it measured the host, and brew's python3 ahead of the pinned venv's
# (#1442, D5b and D5c there). It also used to need a second step to keep a
# hand-built golangci-lint (the one scripts/ci/pins.env pins, see
# gate_golangci_pin_note) ahead of brew's; appending makes that moot.
case ":$PATH:" in
  *:/opt/homebrew/bin:*) ;;
  *) PATH="$PATH:/opt/homebrew/bin" ;;
esac
export PATH
# GOLANGCI_LINT_DIR is how to say "this directory first" without touching
# the caller's PATH.
if [ -n "${GOLANGCI_LINT_DIR:-}" ]; then
  export PATH="$GOLANGCI_LINT_DIR:$PATH"
fi

# The marker every process this gate starts can read: "you are running inside
# the local gate", as opposed to a developer running one suite by hand. The
# Docker fixtures key on it to tell "this laptop has no Docker", an honest
# skip when somebody runs one suite by hand, from "the daemon this gate
# already used has gone away", which is a failure because a full run refuses
# to start without one.
#
# CI_LOCAL_SKIP_DOCKER=1 is the exception at both ends. It is how a run says
# out loud that it is proceeding without a daemon, this script ledgers it so
# the run cannot end ok, and it is already in the environment of everything
# below. A fixture that turns a skip into a failure under CI_LOCAL=1 has to
# honour it, or that opt-out stops working.
#
# The name is load-bearing in core/tests; do not rename it here alone.
export CI_LOCAL=1

# And CI, which this gate had never set (#1259, #1260). vitest reads it to
# pick its non-interactive reporter, Playwright reads it to stop retrying
# (and to refuse a `.only` where a config sets forbidOnly from it, which
# ui/shared/e2e's does; the Chromium lane passes --forbid-only as well), and
# core/tests/machines keys a refusal
# on it, so its comment about "a failure under CI" described a run nobody
# ever made. The recorder sets it too; both, so
# neither depends on the other being the version in the worktree.
export CI=1

# Which tier this is, for the one test that must not run in the other. The
# full gate is the release run; scripts/ci-fast.sh sets "fast". core/tests/compat
# reads it to stand down the check that asks GitHub whether every BLOCKED
# matrix row still cites an open issue, so closing an issue by hand cannot
# turn a commit red when no commit changed (#1361).
export CI_LOCAL_TIER=full

# Running as a git hook (pre-commit) means git has GIT_INDEX_FILE (a path
# relative to the repo root, e.g. ".git/index"), GIT_DIR, GIT_WORK_TREE, etc.
# set in the environment for the in-progress commit. The dependency-rules
# scripts below do `git worktree add` from inside a subshell that `cd`s
# elsewhere first, and a relative GIT_INDEX_FILE resolved against the wrong
# cwd is exactly how you get "fatal: .git/index: index file open failed:
# Not a directory" — a real failure this script hit and had to trace back to
# here. Unset them so every git command in this script (and anything it
# shells out to) resolves the repository fresh, the same as it would run
# standalone outside a hook.
unset GIT_INDEX_FILE GIT_DIR GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR GIT_PREFIX

FAST="${CI_LOCAL_FAST:-0}"

# Resolved from this script's own location, not the working directory. Every
# other path here is cwd-relative because .husky/pre-commit always invokes
# this from the repo root, but the one file that decides whether a run may
# call itself ok should not depend on that holding.
. "$(cd "$(dirname "$0")" && pwd)/lib/ci-local-gate.sh"

# Every exit from here on carries a verdict line, and drops anything this run
# started. Without the marker, a run that died under `set -e` printed no
# "==> ci-local: ..." line at all, so the one outcome a reader most needs to
# grep for was the one with no marker. Installed through one function rather
# than as `trap ... EXIT` here, because a trap is set and not appended: a
# second EXIT trap later in this file would silently replace this one. See
# gate_install_traps.
gate_install_traps

# The watchdog reads output as progress, and the self-test drivers are Python,
# which writes stdout in 8 KiB blocks when it is not a terminal. The gate's
# output is always a pipe or a file, so a driver's per-control lines reached the
# log in bursts, or only when it was killed: a selftest-recovery rehearsal on
# MARS sat 901s on thirteen finished controls and was ledgered STALLED (#1672).
# Every Python this gate starts writes line by line.
export PYTHONUNBUFFERED=1

# Every step is watched for a stall from the first one on (#1540): no output
# for RETND_GATE_STALL_MIN minutes, or past its hard cap, and the step is
# stopped with its containers and the run ends FAILED (STALLED: ...). This
# only checks the knobs; see the stall watchdog in lib/ci-local-gate.sh.
gate_watchdog_arm

# The hook cannot report its own absence, so the gate does, first thing
# and every run (#1260). A warning rather than a refusal: a run by hand in
# a clone without the hook is still a run. The recorder refuses.
gate_warn_hook_missing

# The pinned golangci-lint, from scripts/ci/pins.env (#1260). That file is
# the one source: this gate, scripts/ci-docker.sh, the recorder and the
# release check all read it. It used to be read out of ci.yml with a
# silenced sed, which went on returning nothing after that file was
# deleted, so the note below could never fire and the hint printed a
# placeholder.
GATE_LINT_PIN="$(sed -n 's/^GOLANGCI_LINT_VERSION=//p' scripts/ci/pins.env 2>/dev/null | head -n 1)"
if [ -z "$GATE_LINT_PIN" ]; then
  echo "==> scripts/ci/pins.env carries no GOLANGCI_LINT_VERSION, so this gate does not know which linter it means." >&2
  exit 1
fi
_go_floor="$(sed -n 's/^go[[:space:]]*\([0-9][0-9.]*\)[[:space:]]*$/\1/p' core/go.mod 2>/dev/null | head -n 1)"

if ! command -v golangci-lint >/dev/null 2>&1; then
  {
    echo "==> golangci-lint not found. Build the pinned one (scripts/ci/pins.env), with the Go core/go.mod names, and re-run:"
    echo "      GOTOOLCHAIN=go${_go_floor:-1.27.0} GOBIN=<dir> go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${GATE_LINT_PIN}"
    echo "      GOLANGCI_LINT_DIR=<dir> sh scripts/ci-local.sh"
  } >&2
  exit 1
fi

# The golangci-lint this gate means is the pinned one, run with the Go
# toolchain it was built with. A different one is not wrong to run, but
# its findings are not the pinned one's: a newer errcheck reports things
# an older one did not (#1260 found eleven that way), and a linter whose
# bundled x/tools predates the Go running it cannot read the export data
# that Go writes (`export data version 5 is greater than maximum supported
# version 4`), so every package "fails to typecheck". So a mismatch is
# LEDGERED: the run ends INCOMPLETE naming both versions, which is the
# same answer every other "this run performed less than it was asked to"
# gets, and the recorder refuses an INCOMPLETE run, so a release record
# is always a run of the pinned linter. A binary that says nothing
# parseable (a test stub) changes nothing.
gate_golangci_pin_note() {
  _have=$(golangci-lint --version 2>/dev/null | sed -n 's/.*has version v\{0,1\}\([0-9][0-9.]*\).*/\1/p' | head -n 1)
  [ -n "$_have" ] || return 0
  if [ "$_have" != "$GATE_LINT_PIN" ]; then
    _built=$(golangci-lint --version 2>/dev/null | sed -n 's/.*built with go\([0-9][0-9.]*\).*/\1/p' | head -n 1)
    {
      echo "==> note: golangci-lint on PATH is ${_have} built with go${_built:-?}; scripts/ci/pins.env pins ${GATE_LINT_PIN}."
      echo "    Its findings are not the pinned linter's, so this run is ledgered and ends INCOMPLETE."
      echo "    To run the pinned one, build it with the Go core/go.mod names and have this gate put it first on PATH:"
      echo "      GOTOOLCHAIN=go${_go_floor:-1.27.0} GOBIN=<dir> go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${GATE_LINT_PIN}"
      echo "      GOLANGCI_LINT_DIR=<dir> sh scripts/ci-local.sh"
    } >&2
    gate_note_skip "the lint steps ran golangci-lint ${_have}, not the pinned ${GATE_LINT_PIN} (scripts/ci/pins.env); its findings are not the pinned linter's"
  fi
}
gate_golangci_pin_note

# Then act on it, so the gate does not depend on the caller having exported
# the right toolchain: a golangci-lint built with a given Go is only able
# to read export data that Go (or an older one) wrote, so when this
# machine's default Go is newer, every package "fails to typecheck". If the
# binary on PATH says which Go built it and the caller has not chosen a
# toolchain, run the whole gate with that one. An explicit GOTOOLCHAIN
# always wins, and a binary that says nothing parseable (a test stub)
# changes nothing.
if [ -z "${GOTOOLCHAIN:-}" ]; then
  _built=$(golangci-lint --version 2>/dev/null | sed -n 's/.*built with go\([0-9][0-9.]*\).*/\1/p' | head -n 1)
  if [ -n "$_built" ]; then
    GOTOOLCHAIN="go$_built"
    export GOTOOLCHAIN
    echo "==> note: GOTOOLCHAIN=$GOTOOLCHAIN for this run, the Go the golangci-lint on PATH was built with." >&2
  fi
fi

# What this run is made of, as four lines the recorder reads into the
# release record (#1260): the Go that ran (under the GOTOOLCHAIN above, so
# `go version` here is the run's and not the shell's), the linter, node
# and the Docker daemon. Printed rather than looked up again afterwards,
# because the record is about the run.
echo "==> toolchain: go $(go version 2>/dev/null | sed 's/^go version //' || echo unknown)"
echo "==> toolchain: golangci-lint $(golangci-lint --version 2>/dev/null | sed -n 's/.*has version v\{0,1\}\([0-9][0-9.]*\).*built with \(go[0-9][0-9.]*\).*/\1 built with \2/p' | head -n 1)"
echo "==> toolchain: node $(node --version 2>/dev/null || echo unknown)"
echo "==> toolchain: docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || docker --version 2>/dev/null | sed -n 's/^Docker version \([^,]*\),.*/\1/p' || echo unknown)"
if [ -n "$GATE_SHARD" ]; then
  echo "==> shard: $GATE_SHARD"
fi

# Captured once, from repo root (where this script is always invoked from,
# per .husky/pre-commit), so every module below can reference the one
# project-wide .golangci.yml with an absolute path instead of working out
# its own relative depth back to the repo root.
REPO_ROOT="$(pwd)"

# Before anything slow: a capability this gate reports on but does not have
# is a hole in the gate, not a footnote. Refuse here rather than after twenty
# minutes of Docker-backed Go tests.
if gate_shard_wants js; then
  gate_require_js_deps ui/shared apps/common/tests apps/ugos/frontend/upk-proof
fi

# A shard that has no Docker-backed step has no use for the daemon, so it
# neither refuses without one nor keeps one awake (the sentinel below).
gate_needs_docker=0
if gate_shard_wants core-rest core-docker apps e2e ui-browser two-machine selftest-conformance structure; then
  gate_needs_docker=1
fi

if [ "$FAST" != "1" ] && [ "$gate_needs_docker" = 1 ]; then
  gate_require_docker
fi

# Not a refusal, and deliberately not one: Resource Saver being on is normal,
# the sentinel below makes it harmless, and the per-step probes turn what it
# still breaks into a named failure. It is printed because it is the first
# thing to check when the daemon dies at minute 18 (#457).
if [ "$gate_needs_docker" = 1 ]; then
  gate_warn_resource_saver
fi

# The daemon is now known good (or the run said out loud that it is not merge
# evidence). Keep it that way: Docker Desktop's Resource Saver measures IDLE,
# and this gate has several Docker-free stretches longer than its five-minute
# timer, so every run used to cold-start the VM somewhere in the middle. One
# container that sleeps for the life of the run means the daemon is never
# idle, on any machine, without depending on a GUI setting (#457).
if [ "$gate_needs_docker" = 1 ]; then
  gate_start_docker_sentinel
fi

if [ "$FAST" = "1" ]; then
  gate_note_skip "core/ ./tests/... (the Docker-backed crash matrix, the SFTP integration tests, the MinIO integration tests and the composed conformance scenario), the cross-compiles, the upk-proof and ui/shared production builds, the apps/common/tests cross-provider conformance suite, the browser e2e suite and CLI smoke slice from retnd-tests, the ui/shared browser specs and the assurance UI self-test, the repository-structure dependency rules and this gate's own self-test (CI_LOCAL_FAST=1)"
fi

gate_seg_static_a() {
# The mutation-anchor check (#458), added under separate work. An anchor here
# is a verbatim copy of product source that a mutation selftest plants a
# violation into, not a link in a document. scripts/compat/selftest.sh and
# scripts/conformance/selftest.sh plant deliberate violations to prove each
# cell of their gate can go red, and every plant is anchored to a verbatim
# copy of product source that lives in a script the author of the product
# change never opens. A refactor drifts the anchor, the mutation then plants
# nothing, and that control is dead while still reporting ok. This dry-runs
# every anchor in both selftests against the real tree in about a second,
# which is why it runs here rather than at minute 20 where the selftests
# themselves would have found the same drift one anchor at a time.
#
# Guarded on the file rather than assumed, because this branch and the branch
# that writes it are in flight at the same time; once that one has landed the
# guard can go and the step becomes unconditional, which is the shape every
# other step here has for #160's reason (a step that quietly skips itself
# when its own file is missing is a silent skip wearing a different hat).
if [ -f scripts/selftest/check-anchors.sh ]; then
  gate_step "mutation anchors in the compat, conformance and race selftests still match the tree (#458)"
  bash scripts/selftest/check-anchors.sh
else
  echo "==> mutation anchors: scripts/selftest/check-anchors.sh is not in this tree yet, nothing to run (#458)"
fi

# Formatting, over every tracked Go file in the repository (#417). Half a
# second, so it belongs up here with the anchors check rather than behind
# twenty minutes of Go suites.
#
# It is not the same check as the gofmt formatter .golangci.yml now
# enables, and neither one makes the other redundant. golangci-lint is
# invoked per module below (`cd core && ...`, `cd apps/common && ...`), and
# two Go files in this repository live outside every module and outside
# go.work: scripts/api/gen-bindings.go and scripts/architecture/ownership.go.
# No per-module lint run has ever been able to see either, and the
# unformatted one of the pair was gen-bindings.go, which is how a file
# stayed unformatted for its whole life with every gate step green.
gate_step "every tracked Go file is gofmt-clean, including the ones outside every module (#417)"
bash scripts/format/check-gofmt.sh

# No new old-brand identifier, over every tracked source file (#794, #887).
# This project is on its third name, and both previous ones left runtime
# identifiers behind: the first product name's RM_ and RCLONE_MANAGER_ environment
# variables and retnd's bm_ cookies and BACKUP_MANAGER_ variables.
# #794 renamed some of them, and EPIC R (#885) is
# renaming the whole `retnd` family to `retnd`; #887 pointed this guard at
# it. The rename is a one-off edit; this step is what makes it stay done. The
# way the old prefixes spread in the first place was somebody copying the
# line above the one they were writing, and nothing anywhere looked.
#
# Two `git grep` sweeps and three fixed lists, so it costs about two seconds
# and belongs up here with the anchors and gofmt sweeps rather than behind
# twenty minutes of Go suites. It runs in FAST mode too, for the same
# reason those two do: it is seconds, and a mid-refactor edit is exactly
# what introduces the thing it looks for.
#
# The guard is RED until EPIC R finishes, by design, and green here only
# because every surviving occurrence is on its `pending` list (FR-40): the
# list going empty is the epic's completion signal, and each sub-issue
# deletes its own entries.
#
# The self-test next to it plants each of the eleven patterns in a throwaway
# repository and requires the guard to go red, and plants the lookalikes it
# must NOT catch -- CONFIRM_DELETE, rclone's own ibm_signer.go, and the nine
# look-alike identifiers (BackupDetailPage and friends) whose 50 occurrences a case-insensitive
# `retnd` pattern would flag -- and requires it to stay green. A guard
# whose only evidence is that it passes on the one tree anybody runs it
# against has proven nothing (#160's shape again), and it is under five
# seconds, so it runs here rather than nowhere.
#
# FR-44's two brand checks (#893) run in THIS step rather than one of their
# own, because they are the same claim from the side `git grep` cannot
# reach. The guard above greps tracked source and passes -I, so it never
# opens a raster and it cannot read a letter that has become an SVG
# `<path>`; a favicon saying the old name is invisible to it by
# construction. So `check-svg-text.sh` refuses a text node in any source
# SVG -- `<text>`, `<tspan>`, `<title>`, `<desc>`, the four places a name
# can hide inside a picture -- with one path-pinned exception, the store
# icon whose `<title>` distribution/packaging's CheckStoreIcon requires,
# where the element is allowed and its TEXT is checked instead. And
# `check-brand-assets.sh` holds
# `docs/design/brand-assets.md` to the filesystem in both directions, so an
# asset cannot be missed by being forgotten. Both are `git grep`-shaped and
# take under a second between them.
#
# Neither is a claim that the art is right, and the manifest says so: the
# rest of FR-44 is a human looking at the pictures, recorded as outstanding
# in that file and as PARTIAL on row R2.14 of
# docs/conformance/epic-r-matrix.md. scripts/brand/selftest.sh is the proof
# these two can still fail, and it plants exactly the two violations FR-44
# names.
gate_step "no new RM_/BM_/bm_/rbm_/retnd/retndproject/rclone[-_ ]manager/retnd identifier, no brand asset unaccounted for or carrying a text node, and all three of those guards can still fail (#794, #887, #893)"
bash scripts/rename/check-brand-drift.sh
bash scripts/rename/selftest.sh
bash scripts/rename/check-no-former-name.sh
bash scripts/rename/selftest-former-name.sh
bash scripts/brand/check-svg-text.sh
bash scripts/brand/check-brand-assets.sh
bash scripts/brand/selftest.sh

# What `go doc` prints for every package, against a recorded baseline
# (#526). A comment adjacent to `package` IS the package doc, and go/doc
# concatenates every one of them across a package in sorted file order. Six
# documentation lanes put per-file openers there, so `go doc ./core/service`
# opened with "This file is the operator's activity feed" and the real
# overview was several topics down the list.
#
# Every step in this gate was green for the whole of it. Nothing here had
# ever assembled a package overview and looked at it: go build and go vet do
# not care where a comment sits, and none of the linters .golangci.yml
# enables reads package documentation. So this assembles it, through
# go/doc's own Package.Doc rather than by shaping text, and records the
# files each overview is built from alongside a digest of it, because a
# promoted opener shows up as a new carrier in a line a reviewer can read.
#
# A second, like the sweep above, and it belongs here for the same reason.
gate_step "every package's go doc overview still matches its baseline, carriers included (#526)"
bash scripts/docs/check-package-doc.sh

# The docs site's contrast rule (#1054): WCAG 2.x AA, 4.5:1 for normal text
# and 3:1 for large text and control boundaries, against the background the
# text actually sits on, in both themes. The tokens are checked here with no
# browser (and with the checker's own positive controls first); the rendered
# pages, which is where an inherited colour or an opacity shows up, are
# checked in the Docker-backed step further down.
gate_step "docs site colour tokens meet WCAG AA in both themes, and the checker can still fail (#1054)"
bash scripts/contrast/check.sh

# The docs-site media capture tooling (#926). Two unrelated breaks (an unbound
# FFMPEG in Clip.write, a placeholder SSH port the wizard refuses since #864)
# meant nothing could be re-recorded and no gate said so. This runs the harness
# far enough to encode a clip, checks EXAMPLE.port against the wizard's own
# isPort, and checks every label a capture script drives still exists in
# ui/shared/src (the stale step names #788 left behind). No browser, no
# server, about a second. Exit 3 is "no ffmpeg or no node here", a ledgered
# skip like the other host-dependent steps.
gate_step "the docs-site capture tooling still encodes a clip and drives labels the UI has (#926)"
if command -v node >/dev/null 2>&1; then
  _cap_rc=0
  # Probe ffmpeg BEFORE node --test: its first case encodes a clip and fails
  # (exit 1, "broken") on a host with no ffmpeg, which said "broken" about a
  # capability the host simply lacks (#1455). Missing is the named skip below.
  if ! command -v "${FFMPEG:-ffmpeg}" >/dev/null 2>&1 && [ ! -x /opt/homebrew/bin/ffmpeg ] && [ ! -x /usr/local/bin/ffmpeg ] && [ ! -x /usr/bin/ffmpeg ]; then
    _cap_rc=3
  fi
  [ "$_cap_rc" -eq 0 ] && { node --test docs/site/tools/check-tools.test.mjs || _cap_rc=$?; }
  [ "$_cap_rc" -eq 0 ] && { node docs/site/tools/check-tools.mjs || _cap_rc=$?; }
  if [ "$_cap_rc" -eq 3 ]; then
    gate_note_skip "the docs-site capture tooling check (#926): no ffmpeg on PATH. Install it and re-run."
  elif [ "$_cap_rc" -ne 0 ]; then
    exit "$_cap_rc"
  fi
else
  gate_note_skip "the docs-site capture tooling check (#926): node is not on PATH."
fi

# The same blind spot, for the checks that actually find bugs (#417). Being
# outside every module does not only cost those two files their formatting:
# this gate vets and lints per module too, so nothing has ever vetted or
# linted them either. `go vet` needs no module when it is handed file
# paths, and golangci-lint gets a throwaway module per unowned directory,
# which resolves offline because every unowned file here is standard-library
# only. A few seconds, and it belongs up here with the sweep for the same
# reason.
#
# Deliberately NOT with the other scripts/architecture checks further down:
# those run after the Go suites, and a Go file nobody checks is worth
# hearing about before twenty minutes of Docker-backed tests, not after.
gate_step "every Go file no module owns still passes go vet and golangci-lint (#417)"
bash scripts/architecture/check-unowned-go.sh

# The two end-to-end drivers' --help, pinned against a golden (#514). Both
# used to render it by reading their own header BY LINE NUMBER, so the help
# an operator reads was a set of coordinates: a comment inserted above the
# boundary rewrote it and deleting one truncated it, with nothing anywhere
# rendering either script's help. Both had already drifted by the time
# anybody looked, one of them to a sentence that stops mid-word.
#
# FR-35 clause 4 says nothing may reword a line an operator already reads,
# and core/tests/compat enforces that for the CLI byte for byte. These two
# surfaces are the same kind of text with none of that protection, so they
# get a golden of their own and a reword has to update it on purpose.
#
# Up here with the other static text checks rather than behind FAST: it
# renders two help texts and diffs them, so it builds nothing, installs
# nothing and needs no Docker, and it costs about a second.
gate_step "the e2e drivers' --help is still the text it was (#514)"
bash scripts/tests/e2e-help.test.sh

# The first-run specs skip on the seeded rig and a skip looks like a pass, so
# the gate runs them against a fresh rig and refuses a skip (retnd#1049).
# This pins the judging and that the gate calls it; no Docker, about a second.
gate_step "a skipped first-run spec fails the gate (#1049)"
bash scripts/tests/first-run-report.test.sh

# A tests pin must name a commit on retnd-tests main (#1074). The pin once
# pointed at the head of an open retnd-tests PR, which the gate ran
# happily, and the bump tool had been written not to check. Both now
# refuse, and this runs fixture git repositories over file:// (no network,
# no Docker) to prove it, including a planted check that always passes,
# which the suite has to notice.
gate_step "a tests pin must be on retnd-tests main (#1074)"
bash scripts/tests/pin-on-main.test.sh

# The pinned retnd-tests checkout the encryption self-test shard hands its
# three retnd-tests controls (#1548): the pin, the cache and the refusals, over
# fixture repositories (no network).
gate_step "the pinned retnd-tests checkout for the self-test shards (#1548)"
bash scripts/tests/tests-checkout.test.sh

# The web-ui ratchet's judge (#1260). The browser lane is judged by its
# Playwright report against scripts/e2e/known-red.txt rather than by its
# exit status, so the judge is the whole gate for the specs on that list.
# This drives it over synthetic reports: a failure not listed, a listed
# spec that passes, an expired or other-pin entry, a grown skip count, a
# list over its cap, and the two blocking files. It also holds the real
# list to the real pin and to its ceiling. No Docker, under a second.
gate_step "the web-ui ratchet refuses a new failure, a listed pass, an expired or other-pin entry and a grown skip count (#1260)"
bash scripts/tests/web-ui-ratchet.test.sh

# The Chromium lane's own cases (#1532, #1537), against a stand-in docker:
# the read-only mount, the fence, the cap and the stall limit it hands the
# daemon, the container removed on every way out, and its three answers kept
# apart (a red, stalled or capped run is 1, a machine that cannot run it is
# 3, and a 3 from the command inside is a 1). No Docker, a second or two.
gate_step "the Chromium lane hands the daemon a read-only tree and a fence, and tells a red run from a machine that cannot run it (#1532, #1537)"
bash scripts/tests/ui-browser-lane.test.sh

# docs/reference/local-gate.md names every variable, trailer and exit
# status this gate reads or writes; this reads the names out of the gate's
# sources and refuses when the page is missing one. No Docker, instant.
gate_step "docs/reference/local-gate.md documents every knob the gate reads (#1260)"
bash scripts/tests/local-gate-reference.test.sh

# The fast tier and the pre-merge check (#1361): which paths pick which
# checks, that a Go-only commit in a fresh worktree is not blocked by missing
# JS deps, the hooks-path refusal, the Local-Gate: fast stamp, and that
# scripts/merge-gate.sh posts the right state. Stubs only, no Docker.
gate_step "the fast tier, the hook's hooks-path refusal and the pre-merge check behave as documented (#1361)"
bash scripts/tests/ci-fast.test.sh

# ci-docker.sh --host (#1267) ships the tree to another machine's Docker
# daemon and runs the gate there. This drives it with stand-ins for docker,
# rsync, tar and ssh: the daemon each call goes to, what is mounted, the exit
# status, the cleanup, the free-space refusal and that no secret travels.
# No Docker, no network, a few seconds.
gate_step "ci-docker.sh --host runs the gate on a remote daemon from a copy, and cleans up (#1267)"
bash scripts/tests/ci-docker-remote.test.sh

# Every Python file under scripts/, held to a linter, and the subset that is
# clean under a type checker held to that too (EPIC I / I1.6, #672).
#
# Nothing in this repository linted Python before #672: the gate ran gofmt,
# go vet, golangci-lint and eslint, and for Python it ran one unittest suite
# and stopped. The step landed scoped to scripts/bdtools alone so that a new
# rule would not turn up first as somebody else's untouched file going red.
# It is wider now, and the two halves widened by different amounts because
# the trial runs said different things.
#
# ruff: 49 findings across scripts/install and scripts/deploy, all fixed at
# the cause, so it now runs over all of scripts/. Two files carry an E501
# exemption named in the config, both because they embed text nobody may
# reflow: a byte-exact copy of container/compose.yaml, and pasted `iptables
# -L` output.
#
# mypy --strict: 965 errors over the same six files, 944 of them in
# scripts/install. So it names its paths instead. scripts/deploy is included
# because it is clean (deploy_generic.py needed nothing; its two suites
# needed twenty annotations). scripts/install/install_docker_host.py and its
# test suite are not, and the count is the reason: a gate that arrives with
# 965 suppressions is not a gate. Typing them is a change of its own.
#
# --config / --config-file explicitly, for both tools. ruff would otherwise
# discover the configuration by walking up from each file, which finds it for
# scripts/bdtools and finds nothing for scripts/install -- and silently
# linting half the tree at ruff's 88-column defaults is the failure this
# change exists to avoid.
#
# Neither tool is a dependency of this repository, and neither is going to be
# installed on a NAS. A machine without them therefore LEDGERS rather than
# passes: that is the same "the gate performed less than it was asked to"
# verdict #160 exists for, and the difference between it and a silent skip is
# that the run ends INCOMPLETE and says which tool was missing. It is not a
# hard refusal because these tools arrived with this change and failing every
# developer's gate until they install two things is not how a new check earns
# its place.
gate_step "scripts: ruff over every Python file, mypy --strict over the typed subset (#672)"
if command -v ruff >/dev/null 2>&1; then
  ruff check --config scripts/bdtools/pyproject.toml scripts
else
  gate_note_skip "ruff over scripts (#672): ruff is not on PATH. Install it (pipx install ruff) and re-run."
fi
# `mypy` on PATH rather than `python3 -m mypy`, and that is a fixture
# decision as much as an ergonomic one: scripts/tests/ci-local-gate.test.sh
# drives this script against synthetic checkouts with $tree/bin first on
# PATH, which is how it controls `docker`. A module invocation cannot be
# stubbed that way, so every full-tree case in that suite would measure
# whichever python3 the developer happened to have. `pipx install mypy` and
# an activated virtualenv both put the binary on PATH.
if command -v mypy >/dev/null 2>&1; then
  mypy --strict --config-file scripts/bdtools/pyproject.toml \
    scripts/bdtools scripts/deploy scripts/install/embed_compose.py
else
  gate_note_skip "mypy --strict over scripts/bdtools, scripts/deploy and scripts/install/embed_compose.py (#672): mypy is not on PATH. Install it (pipx install mypy) and re-run."
fi

# The two-machine proof's exit statuses, which are this gate's own ledger
# seen from the other end (#551). That script says "this machine cannot
# perform the proof" as exit 3, which the step below reads as a skip and
# ledgers, and the CLI it drives now returns 3 for a refusal of its own.
# One unguarded `bm` meeting that refusal would report a failed backup
# proof as a machine that never tried, and the hook would allow the
# commit. The script keeps its verdict on a number nothing else produces
# and translates once at its own exit; this is what holds it there.
#
# Up here with the other static checks: it stands up no containers and
# needs no daemon, it drives the real script with a stand-in `docker` and
# `git` on PATH, and it costs about a second.
gate_step "the two-machine proof's verdict is still its own number (#551)"
bash scripts/tests/two-machine-exit-status.test.sh

# The same verdict, one layer out (#575). scripts/e2e/two-machine-ci.sh
# wraps the proof for a workflow step, which has two outcomes where the
# proof has three: a runner that cannot start docker-in-docker has to come
# out red under the word INCOMPLETE, not green under no word at all. The
# workflow that called it is gone (#1260), and the wrapper stays proven for
# whoever runs the proof under a hosted runner of their own.
#
# Same cost and same reasoning as the step above: stand-in proofs, no
# containers, about a second.
gate_step "a proof CI could not perform still reads as incomplete, not as a pass (#575)"
bash scripts/tests/two-machine-ci-verdict.test.sh

# Every Go module resolves its own dependencies without go.work (#1064). The
# workspace builds the whole tree on the highest version of every requirement,
# so a dependency bumped in core/ alone is green everywhere the workspace is
# used and red in the product image and under GOWORK=off. #1003, #797 and #838
# were all that.
gate_step "every Go module resolves its own dependencies without go.work (#1064)"
bash scripts/tests/every-module-builds-on-its-own.test.sh

# The release's own check that this gate ran (#575, #1260). A commit on
# `release` publishes a signed image, and the only gate in front of it is
# this script, run on a machine GitHub cannot see. So release prep records
# a full green run (provenance/local-gate.json, written by
# scripts/release/record-local-gate.sh) and require-release-gate in
# release.yml refuses to publish a tree that record does not describe.
# This pulls that job's script out of the workflow and runs it in
# throwaway repositories, once per outcome: a matching record, no record,
# a different tree, an INCOMPLETE ledger, the wrong Go, an unknown schema,
# not JSON, a log that does not match, overridden, and overridden with no
# reason given.
#
# Same cost and same reasoning as the step above: no containers, no
# network, a few seconds.
gate_step "a publish the local gate never recorded is still refused (#575, #1260)"
bash scripts/tests/release-refuses-an-ungated-publish.test.sh

# The image's `latest` tag (#1168). scripts/release/latest.sh decides whether
# a version is the highest non-prerelease release, moves the tag by digest
# and checks it landed; this drives it against stand-in gh and docker, and
# then checks release.yml's wiring and permissions. No containers, no
# network.
gate_step "the release still moves the latest tag, only forwards (#1168)"
bash scripts/tests/release-moves-latest.test.sh

# EPIC N's closing check (#998): the canonical container contract is an empty
# diff, the epic adds exactly one migration (0015), and, in the release job, no
# matrix row is BLOCKED and every listed capture exists. This drives the real
# script against throwaway repositories, one planted violation per guard with
# controls that must pass. The matrix and capture halves are the Go tests in
# core/tests/encryptiongate, which run with the core suite below and carry their
# own lie tables. No containers, no browser, no network.
gate_step "the EPIC N closing check refuses a moved container contract or a second migration, and can still fail (#998)"
bash scripts/tests/epic-n-closure.test.sh

# EPIC T's closing check (#1320), the same shape for suite agility: the
# canonical deployment is an empty diff against 477aec5a (a suite changes the
# bytes of a copy, never how a container runs), the epic adds exactly its two
# migrations (0020, the key's suite; 0021, the default it last ran under), and, in the release job, no row of
# docs/conformance/epic-t-matrix.md is BLOCKED. This drives the real script
# against throwaway repositories, one planted violation per guard and two
# controls. The suite vectors (core/internal/copycrypt/suitetest) run with the
# core suite below, the matrix half is core/tests/encryptiongate, and the
# two-suite e2e rows run in the retnd-tests lane at the pin. No containers, no
# browser, no network.
gate_step "the EPIC T closing check refuses a moved canonical deployment or a migration other than 0020 and 0021, and can still fail (#1320)"
bash scripts/tests/epic-t-closure.test.sh

gate_step "the closure checks and their self-tests never pipe into grep -q under pipefail (#1482)"
bash scripts/tests/closure-no-pipe-grep-q.test.sh

# Issue #1528's planted violations, anchors only: each mutation the
# self-test plants still matches the product source it names, so a refactor
# cannot turn a control into one that plants nothing. Builds nothing; the
# planted runs themselves are the selftest-recovery shard's.
gate_step "the retirement self-test's planted violations still anchor in the product source (#1528)"
bash scripts/retire/selftest.sh --check-anchors

# A self-test or guard script that is in the tree and in no step here runs
# nowhere, and its PASS rows rest on one run (#1530, #1531, #1533). This names
# the ones the gate does not run, and takes an exemption only with an issue.
gate_step "every self-test and guard script is a step of this gate, or an exemption that names an issue (#1530, #1531, #1533)"
bash scripts/tests/every-guard-is-a-gate-step.test.sh

# Which self-test controls a host can run, and that the rest reach the skip
# ledger (#1530, #1531): the lane plan and the pinned lanes.txt of each.
gate_step "the self-tests run every control the host has a lane for and ledger the rest (#1530, #1531)"
bash scripts/tests/selftest-lanes.test.sh

# The long, quiet steps (the mutation self-tests that print once per control,
# the browser lane) carry their own stall and cap limits, above the bounds
# their commands set for themselves, so they run on a host no caller
# environment reaches (#1672). Static, a second or two.
gate_step "the long, quiet steps carry stall and cap limits above their own bounds (#1672)"
bash scripts/tests/gate-step-limits.test.sh

# The agents and encryption self-tests' controls used to hard-code paths on
# the maintainer's Mac and a hand-built capture image, so the gate left about
# twenty of them out wholesale (#1543). This lists every control that would
# not run on a Linux gate host and refuses any without an allowed reason (only
# the retnd-tests checkout, #1548). Static, a second or two: it imports the two
# self-tests and reads their lanes.txt.
gate_step "every agents and encryption self-test control runs on a Linux gate host, in the Chromium lane or where the driver runs, or skips for an allowed reason (#1543)"
bash scripts/tests/selftest-linux-skips.test.sh

# EPIC U's closing check (#1353), the same shape for the MCP server: the
# canonical deployment is an empty diff against 9cd53e1f (an agent is a client of
# /api/v1 and the engine runs exactly as before), the epic adds exactly one
# migration (0022, the three agent tables), and no row of
# docs/conformance/epic-u-matrix.md is BLOCKED. This drives the real script
# against throwaway repositories, one planted violation per guard (a moved
# compose file, a second migration, a BLOCKED row) and two controls. The matrix
# half in closing mode is core/tests/agentgate, which runs with the core suite
# below in open mode and carries its own lie table, and the equivalence suite
# runs in the retnd-tests lane at the pin. No containers, no browser, no network.
gate_step "the EPIC U closing check refuses a moved canonical deployment, a migration other than 0022 or a BLOCKED row, and can still fail (#1353)"
bash scripts/tests/epic-u-closure.test.sh

# EPIC O's dashboards and rules name guard (#902, FR-102). A Grafana panel
# querying a series retnd never emitted draws an empty line that reads like a
# healthy one, and a rule over such a name never fires. This renders the real
# exposition from a fully loaded health report and workflow counter set and
# fails on any retnd_* name, label or closed-vocabulary value in
# container/observability's dashboards and rules that the renderers do not
# produce, on any Loki label or field the shipped pipeline and log schema do
# not carry, and on any panel title that does not say what healthy looks like.
# Its planted violations (PV4 and the rules-file variant) and positive controls
# run in the same go test. No containers, no browser, no network.
gate_step "every retnd_* name, label and Loki field a shipped dashboard or rule names is one retnd emits, and the guard can still fail (#902)"
bash scripts/observability/check-dashboard-metrics.sh

# EPIC O's closing check (#903), the same shape as N, T and U's: the canonical
# deployment is an empty diff against 89af3d93 (/metrics is on the listener the
# engine already had, and the bundle is an example), core/migrations has not
# moved at all (EPIC O stores nothing in the journal), no row of
# docs/conformance/epic-o-matrix.md is BLOCKED and every exit-gate box in the
# spec is ticked. This drives the real script against throwaway repositories,
# one planted violation per guard and four controls. The matrix half in closing
# mode is core/tests/observabilitygate, which runs with the core suite below in
# open mode and carries its own lie table. No containers, no browser, no network.
gate_step "the EPIC O closing check refuses a moved canonical deployment, any migration, a BLOCKED row or an unticked exit-gate box, and can still fail (#903)"
bash scripts/tests/epic-o-closure.test.sh
}

gate_seg_core_build() {
gate_step "core/ go build"
(cd core && GOWORK=off go build ./...)

gate_step "core/ go vet"
(cd core && GOWORK=off go vet ./...)

gate_step "core/ golangci-lint"
gate_lint core
}

gate_seg_core_test() {
if [ "$FAST" = "1" ]; then
  # go test prints a package only when it finishes, in order, and kopia alone
  # took 715s on the 0.8.0 run, so this step is quiet for longer than the
  # stall watchdog's ten minutes on a healthy run. Its own -timeout 45m is the
  # bound that names the test; the watchdog waits past it (#1540).
  gate_docker_step "core/ go test -race ./internal/... (CI_LOCAL_FAST=1: skipping ./tests/... Docker suites)" --stall-min 50
  (cd core && GOWORK=off go test -race -timeout 45m ./internal/...)
else
  # tests/crashmatrix, tests/sftpintegration, tests/sshexecintegration,
  # tests/miniointegration and tests/containerhooks
  # run separately, under cmd/gotestwatch instead of `go test`'s own
  # default -timeout (10m per package). All of them drive real Docker work
  # through a real subprocess (tests/crashmatrix's own harness, a real
  # rclone transfer against the SFTP fixture container, a real S3
  # round trip against the MinIO one, or -- for tests/containerhooks --
  # an ephemeral container per workflow hook), so their wall-clock time
  # tracks real machine load rather than a fixed budget; issue #256 is a
  # real gate run hitting go test's fixed 10m default under load. On a
  # machine that has to PULL a fixture image first, the pull alone can eat
  # most of that budget. gotestwatch bounds them with a no-progress window
  # derived from this run's own measured pace instead (issue #247's
  # reasoning, one layer out; see core/cmd/gotestwatch/doc.go), so there
  # is no fixed number to outgrow.
  # core/tests/compat asks GitHub whether every BLOCKED conformance row
  # still cites an open issue, through gh and gh's own login. The pinned
  # image in scripts/ci/Dockerfile has no gh and no credential, on purpose,
  # so under scripts/ci-docker.sh that check cannot run at all (#1331). It
  # used to FAIL there, which said "broken" about a capability the host
  # simply lacks. Now the gate probes gh first and, when it is missing or
  # not logged in, hands the test a file to write the rows it could not
  # resolve into. Each one lands in the skip ledger, so the run ends
  # INCOMPLETE naming them, and a matrix with no BLOCKED row costs nothing.
  gate_github_ledger_open
  # --stall-min 50 for the reason given on the CI_LOCAL_FAST step above (#1540).
  gate_docker_step "core/ go test -race ./... (excluding tests/crashmatrix + tests/sftpintegration + tests/sshexecintegration + tests/miniointegration + tests/conformance + tests/machinegate + tests/containerhooks and cmd/gotestwatch, all run next)" --stall-min 50
  # shellcheck disable=SC2046  # the package list from `go list` is meant to split into one word each
  (cd core && GOWORK=off go test -race -timeout 45m $(GOWORK=off go list ./... | grep -vE '/(tests/(crashmatrix|sftpintegration|sshexecintegration|miniointegration|conformance|machinegate|containerhooks)|cmd/gotestwatch)$' | gate_core_shard_filter))
  gate_github_ledger_close
fi
}

gate_seg_core_watch() {
if [ "$FAST" != "1" ]; then
  # cmd/gotestwatch is compiled and run rather than handed to `go test`,
  # and the reason is the same three-outcome honesty this gate is built on
  # (#160), one level down. Its TestMain keeps a ledger of controls that
  # could not be measured and exits 3 when that ledger is not empty. The
  # one it loses most often is #533's own live proof, which skips itself
  # when the host stalls it harder than the 800ms its fixture plants: a
  # loaded gate machine, which is precisely the machine that issue is
  # about. `go test` flattens any non-zero status from a test binary into
  # its own 1 and prints FAIL, so a run that merely lost that control
  # reached this gate as a failure, indistinguishable from a watchdog that
  # is actually broken. Run directly, the 3 survives and lands in the
  # ledger, where it says which coverage this run does not have instead of
  # claiming coverage it never got.
  gate_step "core/ cmd/gotestwatch -race, reading its own INCOMPLETE (#533)"
  gotestwatch_bin="$REPO_ROOT/.gotestwatch-gate.test"
  (cd core && GOWORK=off go test -race -c -o "$gotestwatch_bin" ./cmd/gotestwatch)
  # Not under `set -e`: 3 is a verdict this script has to READ, exactly as
  # it reads the two-machine proof's below, and `set -e` would end the run
  # on it before the ledger ever saw it.
  set +e
  (cd core/cmd/gotestwatch && "$gotestwatch_bin" -test.timeout=15m)
  gotestwatch_status=$?
  set -e
  rm -f "$gotestwatch_bin"
  case "$gotestwatch_status" in
    0) ;;
    3)
      gate_note_skip "at least one of gotestwatch's own controls (the live bursty-host proof from #533, or the negative control from #401): this host could not be measured, so the watchdog's behaviour under load is guarded only by the synthetic-clock tests on this run. The run above names which."
      ;;
    *)
      exit "$gotestwatch_status"
      ;;
  esac
fi
}

gate_seg_core_docker() {
if [ "$FAST" != "1" ]; then
  # The 4m default of -overall-floor is the cap on a run that keeps reporting
  # progress and never finishes, and it is a fixed number: these seven
  # packages take about 150s on an idle machine, and sftpintegration alone
  # took 223s with the Docker VM shared with other worktrees' gates, which
  # killed a healthy run at 4m0s three times running (#1260). The no-progress
  # window (45s here) still catches a stall; only the overall backstop moves.
  gate_docker_step "core/ tests/crashmatrix + tests/sftpintegration + tests/sshexecintegration + tests/miniointegration + tests/conformance + tests/machinegate + tests/containerhooks under gotestwatch, -race (issue #256: no fixed go test -timeout)"
  (cd core && GOWORK=off go run ./cmd/gotestwatch -overall-floor 12m -- -race -count=1 ./tests/crashmatrix/... ./tests/sftpintegration/... ./tests/sshexecintegration/... ./tests/miniointegration/... ./tests/conformance/... ./tests/machinegate/... ./tests/containerhooks/...)

  # EPIC O's shipped log pipelines, executed rather than read (#900, #903,
  # matrix row O-08): the real Promtail 3.2.1 and Alloy 1.5.1 images run over a
  # log file the real logger wrote, and the label set they push has to be
  # exactly job, instance, level and schema. Its control (a config promoting
  # action_id) runs in the same go test. It needs this daemon, which is why it
  # sits here with the other Docker-backed suites; it fails rather than skips
  # without one.
  gate_docker_step "the shipped Promtail and Alloy pipelines label exactly job, instance, level and schema (#903)"
  bash scripts/observability/log-pipeline.sh

  # The bundle's alerting rules, evaluated rather than read (#1595): promtool in the
  # pinned Prometheus image checks the rules file and runs the unit tests beside it
  # (a token rotation stays quiet, a probe still fires, the report-age alert holds).
  gate_docker_step "the shipped alerting rules pass promtool and its unit tests (#1595)"
  bash scripts/observability/rule-tests.sh
fi
}

gate_seg_apps_a() {
# A Docker-backed step since #1260: apps/common/auth/local's recovery test
# starts a mail-sink container, so it belongs under the lock with the
# other steps that load the daemon.
gate_docker_step "apps/common go build, vet, test -race (Docker-backed: the local-auth recovery test starts a container)"
(cd apps/common && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -race ./...)

gate_step "apps/common golangci-lint"
gate_lint apps/common

# The rendered half of the same rule (#1054): every page, light and dark,
# every visible text node against its composited background, in the Playwright
# image pinned for the e2e machines. Non-FAST only, like the other
# Docker-backed browser step, so a FAST run cannot merge on it. It sits after
# core/'s first Docker-backed step on purpose: ci-local-gate.test.sh's J1 lets
# exactly one `docker info` answer and expects the run to fail at that step.
if [ "$FAST" != "1" ]; then
  gate_docker_step "docs site rendered contrast: every visible text node, both themes (#1054)"
  bash scripts/contrast/run-browser-check.sh
fi

gate_docker_step "distribution go build, vet, test -race (every package but packaging, which runs next)"
# shellcheck disable=SC2046  # the package list from `go list` is meant to split into one word each
(cd distribution && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -race $(GOWORK=off go list ./... | grep -v '/packaging$'))

# The one Go suite in this gate that does NOT run under the detector, and
# the only one, which is why it gets a paragraph rather than a flag.
#
# distribution/packaging is a static-analysis suite: it reads this
# repository's own manifests, matrices, READMEs and release records and
# asserts they agree with each other. It starts no goroutine of its own:
# no `go` statement in product code or in tests, no t.Parallel anywhere,
# and one sync.Once memoising a fixture. The only concurrency in the whole
# package is os/exec's internal pipe plumbing, which is the standard
# library's and is not what a race in this repository would look like. So
# -race there has nothing of ours to report on.
#
# What it can do is cost. This is also the most CPU-bound package in the
# repository (four of its epic-matrix cases alone are 14.5s, 7.4s, 7.1s and
# 6.9s of pure graph and text analysis), which is exactly the shape race
# instrumentation multiplies: 44s becomes 521s, and that one package was
# the whole of this module's -race cost.
#
# So it is left out on purpose, marked on the command line so the gate's
# own self-test can see it, and Group K of that self-test asserts this is
# the ONLY line carrying that marker. A second one appearing without
# somebody deciding to add it is the thing that would make this exclusion
# rot.
gate_step "distribution/packaging (the static-analysis suite, no -race: see above)"
(cd distribution && GOWORK=off go test ./packaging/) # no -race: no goroutine of its own, so nothing here to detect, and 44s becomes 521s

gate_step "distribution golangci-lint"
gate_lint distribution

if [ -f apps/generic/go.mod ]; then
  gate_docker_step "apps/generic go build, vet, test -race"
  (cd apps/generic && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -race ./...)

  gate_step "apps/generic golangci-lint"
  gate_lint apps/generic
fi

if [ -f apps/synology/go.mod ]; then
  gate_step "apps/synology go build, vet, test -race"
  (cd apps/synology && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -race ./...)

  gate_step "apps/synology golangci-lint"
  gate_lint apps/synology
fi

if [ -f apps/ugos/backend/go.mod ]; then
  gate_step "apps/ugos/backend go build, vet, test -race"
  (cd apps/ugos/backend && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -race ./...)

  gate_step "apps/ugos/backend golangci-lint"
  gate_lint apps/ugos/backend

  if [ "$FAST" != "1" ]; then
    gate_step "apps/ugos/backend cross-compile linux/amd64"
    (cd apps/ugos/backend && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./...)
  fi
fi
}

gate_seg_js_upk() {
if [ "$FAST" != "1" ]; then
  case "$(gate_workspace_state apps/ugos/frontend/upk-proof)" in
    installed)
      gate_step "apps/ugos/frontend/upk-proof typecheck, build"
      (cd apps/ugos/frontend/upk-proof && npm run --silent build)
      ;;
    uninstalled)
      # Only reachable under CI_LOCAL_SKIP_JS=1; the preflight refuses the
      # run otherwise. Ledgered either way, so the final line cannot say ok.
      gate_note_skip "apps/ugos/frontend/upk-proof typecheck and build ($(gate_install_hint apps/ugos/frontend/upk-proof))"
      ;;
  esac
fi
}

gate_seg_apps_b() {
if [ "$FAST" != "1" ]; then
  gate_step "UGREEN cross-compile (amd64)"
  (cd core && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -o /dev/null ./...)

  gate_step "UGREEN cross-compile (arm64)"
  (cd core && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go build -o /dev/null ./...)
fi
}

gate_seg_js_a() {
case "$(gate_workspace_state ui/shared)" in
  installed)
    gate_step "ui/shared typecheck"
    (cd ui/shared && npm run --silent lint)

    gate_step "ui/shared typecheck every provider"
    (cd ui/shared && npm run --silent typecheck:providers)

    gate_step "ui/shared eslint"
    (cd ui/shared && npm run --silent eslint)

    gate_step "ui/shared tests"
    (cd ui/shared && npm test --silent)

    if [ "$FAST" != "1" ]; then
      gate_step "ui/shared build"
      (cd ui/shared && npm run --silent build)
    fi
    ;;
  uninstalled)
    # Reachable under CI_LOCAL_FAST=1 or CI_LOCAL_SKIP_JS=1 only.
    gate_note_skip "ui/shared lint, typecheck:providers, eslint, the vitest suite and the production build ($(gate_install_hint ui/shared))"
    ;;
  absent)
    # Not an optional component (#1260, item 7). ui/shared is the product's
    # UI, and its vitest run is the only thing that catches a stale
    # CLI-twin fixture (#1259); a tree without it has skipped that suite,
    # whatever else it has, and the run may not say ok.
    gate_note_skip "ui/shared lint, typecheck:providers, eslint, the vitest suite and the production build: ui/shared is not in this tree at all"
    ;;
esac

if [ "$FAST" != "1" ]; then
  case "$(gate_workspace_state apps/common/tests)" in
    installed)
      gate_step "apps/common/tests typecheck"
      (cd apps/common/tests && npm run --silent lint)

      gate_step "apps/common/tests eslint"
      (cd apps/common/tests && npm run --silent eslint)

      gate_step "cross-provider conformance suite (apps/common/tests)"
      (cd apps/common/tests && npm test --silent)
      ;;
    uninstalled)
      # The site nobody noticed: this is the cross-provider conformance
      # suite, and it was skipped on every Phase 3 gate run.
      gate_note_skip "apps/common/tests lint, eslint and the cross-provider conformance suite ($(gate_install_hint apps/common/tests))"
      ;;
    absent)
      # Same rule as ui/shared above: the conformance suite is not
      # optional, so a tree without it did not run it and may not say ok.
      gate_note_skip "apps/common/tests lint, eslint and the cross-provider conformance suite: apps/common/tests is not in this tree at all"
      ;;
  esac
fi
}

# A browser run, through the workspace Playwright lock on the maintainer's Mac
# (#1373): one run at a time and a watchdog on the headless Chromes. Anywhere
# else (the lanes, Linux hosts) it runs bare. RETND_PW_LOCK names another lock
# script. On a Mac with no lock at that path the run goes ahead bare, and says
# so. The command's own status is returned, untouched.
gate_pw_locked() {
  pw_lock="${RETND_PW_LOCK:-/Users/rom/workspace/xdg-pw-lock.sh}"
  if [ "$(uname -s)" = "Darwin" ] && [ -x "$pw_lock" ]; then
    # Every Playwright run in the workspace queues on that lock, and the
    # wait is not this step's: the stall watchdog holds the step's clocks
    # until the command under the lock writes the time it got it (#1540).
    gate_watchdog_hold
    # shellcheck disable=SC2016,SC2154  # $1 is the inner shell's; gate_wd_resume comes from lib/ci-local-gate.sh
    "$pw_lock" sh -c '[ -z "$1" ] || date +%s >"$1"; shift; exec "$@"' sh "$gate_wd_resume" "$@"
    return
  fi
  if [ "$(uname -s)" = "Darwin" ]; then
    echo "==> ci-local: browser gate running without the Playwright lock ($pw_lock is not an executable file; RETND_PW_LOCK names another)"
  fi
  "$@"
}

# One step of the Chromium lane (#1532, #1537): the lane's 3 (this machine
# could not run it) is ledgered with what it left out, anything else
# non-zero ends the run on this step.
#
# Not on a Mac unless asked. Heavy retnd steps stay off the maintainer's Mac,
# so there the steps are ledgered with the command that runs them on MARS
# (scripts/release/run-gate-shard.sh, which sends this shard there when no
# host is named). RETND_UI_BROWSER_ALLOW_LOCAL=1 runs them on the Mac anyway,
# and then only through the Playwright lock: with no lock to take they are
# ledgered too, never run bare.
gate_ui_browser() { # <what it runs, for the ledger> <command...>
  ui_what="$1"
  shift
  # scripts/ci-docker.sh says which OS the run started on (#1588): inside its
  # container uname is always Linux, so a by-hand run on a Mac would pass for
  # a Linux box. A gate run directly has no such variable and asks uname.
  # Either one saying Darwin makes it a Mac: a stale RETND_GATE_HOST_OS=Linux
  # exported in a Mac shell must not hide a native Mac run.
  if [ "$(uname -s)" = "Darwin" ] || [ "${RETND_GATE_HOST_OS:-}" = "Darwin" ]; then
    if [ "${RETND_UI_BROWSER_ALLOW_LOCAL:-0}" != "1" ]; then
      gate_note_skip "$ui_what (#1532, #1537): not run on this Mac, where heavy steps do not run. Run the shard on MARS: bash scripts/release/run-gate-shard.sh ui-browser --host mars (RETND_UI_BROWSER_ALLOW_LOCAL=1 runs it here, through the Playwright lock)"
      return 0
    fi
    ui_lock="${RETND_PW_LOCK:-/Users/rom/workspace/xdg-pw-lock.sh}"
    if [ ! -x "$ui_lock" ]; then
      gate_note_skip "$ui_what (#1532, #1537): RETND_UI_BROWSER_ALLOW_LOCAL=1 runs it on a Mac only through the Playwright lock, and $ui_lock is not an executable file"
      return 0
    fi
  fi
  set +e
  gate_pw_locked "$@"
  ui_status=$?
  set -e
  case "$ui_status" in
    0) ;;
    3) gate_note_skip "$ui_what (#1532, #1537): the Chromium lane could not run on this machine, and said why above" ;;
    *) exit "$ui_status" ;;
  esac
}

gate_seg_e2e() {
# The browser e2e signal (#158, #197). Until this step existed, the
# Playwright suite had no automated execution anywhere: nightly-e2e.yml's
# schedule is commented out, and this script never invoked it. So it ran when somebody remembered to,
# which is how a deterministically red spec sat on main through four merges
# and was dismissed twice as an ordering flake.
#
# The suite itself no longer lives in this repository; it is Suite B of
# retnd/retnd-tests, pinned by scripts/e2e/tests-repo.pin. What
# it runs against is not the pin's own build, it is THIS working tree's
# ui/shared, on a port the harness picks and proves free. The same step
# also runs that repository's CLI smoke slice against a retnd
# built from this tree, which is a black-box signal this repository has
# never had at all.
#
# Non-FAST only, so CI_LOCAL_FAST=1 skips it under the existing
# never-merge-on-FAST rule. CI_LOCAL_SKIP_E2E=1 is the separate out-loud
# opt-out for a machine with no browser, and it ledgers, so that run ends
# INCOMPLETE and says which check it left out.
#
# Three outcomes since #1260, like the two-machine proof below. The browser
# suite runs as a lane judged against scripts/e2e/known-red.txt (a failure
# in a blocking spec, a failure not on the list, a listed spec that now
# passes, an expired or other-pin entry, a grown skip count: all FAILED),
# and a lane that produced no report to judge, because the rig could not
# run or the runner crashed, exits 3. That is ledgered here, so the run
# ends INCOMPLETE and the release recorder writes nothing for it: a lane
# nobody could judge is not a lane that passed. The judge's own cases are
# scripts/tests/web-ui-ratchet.test.sh, up with the static checks.
if [ "$FAST" != "1" ]; then
  if [ "${CI_LOCAL_SKIP_E2E:-0}" = "1" ]; then
    gate_note_skip "the browser e2e suite and the CLI smoke slice from retnd-tests, which are the only automated execution either of them gets (CI_LOCAL_SKIP_E2E=1)"
  else
    # Under the lock (#1261 review): this step builds the product images
    # and runs the three-machine rig, the most Docker-heavy stretch of
    # the whole gate, and it used to be the one step that dropped the
    # lock the crash matrix had just held.
    # --stall-min 30: the rig builds the client image (the pinned Playwright
    # image, over a gigabyte) with `docker build -q`, which prints nothing
    # until it is done, and on a cold cache that takes longer than the stall
    # watchdog's ten minutes (#1540).
    gate_docker_step "browser e2e + CLI smoke, from retnd-tests at the pinned sha (#197), the browser suite judged against the known-red ratchet (#1260)" --stall-min 30
    # Not under `set -e`: exit 3 is a verdict this script has to READ, and
    # `set -e` would end the run on it before the ledger ever saw it.
    set +e
    gate_pw_locked bash scripts/e2e/run-tests-repo-gate.sh
    e2e_status=$?
    set -e
    case "$e2e_status" in
      0) ;;
      3)
        gate_note_skip "the browser suite lane (#1260): it produced no report to judge, because the rig could not run on this machine or the runner crashed, and said why above"
        ;;
      *)
        exit "$e2e_status"
        ;;
    esac
  fi
fi
}

gate_seg_ui_browser() {
# The Chromium lane (#1532, #1537). The spec files in ui/shared/e2e (131 when
# this landed) are
# the proof the Dashboard, Settings, Agents and suite UI work landed with, and
# EPIC Q's assurance UI self-test is what says the glyph and wording guards can
# still go red. Neither ran anywhere before this but on somebody's Mac, so a
# spec could go red on main and nothing would notice (#197's shape again).
#
# Both run in scripts/e2e/ui-browser.sh, which starts the Playwright image the
# other browser steps pin, with this tree mounted read-only at its own path
# (so it works from inside scripts/ci-docker.sh on a remote daemon too), makes
# a linux install of ui/shared inside it, and runs the command there. Every
# run carries a hard cap and a stall check (no output for ten minutes is a
# stalled run, and it is killed), and the specs a per-test timeout; any of
# those is a FAILED run, not a skip. Its 3 means the machine could not run the
# lane at all, and is ledgered. CI_LOCAL_SKIP_UI_BROWSER=1 is the out-loud
# opt-out, and ledgers too. Non-FAST only, about 56 minutes for the specs
# at workers: 1, so it is a shard of its own, and one that does not run on a
# Mac unless RETND_UI_BROWSER_ALLOW_LOCAL=1 says so (see gate_ui_browser).
if [ "$FAST" != "1" ]; then
  if [ "${CI_LOCAL_SKIP_UI_BROWSER:-0}" = "1" ]; then
    gate_note_skip "the ui/shared browser specs and the assurance UI mutation self-test, which are the only automated execution either of them gets (CI_LOCAL_SKIP_UI_BROWSER=1)"
  else
    # The lane has its own stall check (600s) and cap (14400s for the specs,
    # 2700s for a run, plus 900s for the image and the install), and its
    # verdict says which spec or control hung. The step's limits sit above
    # those, so the lane always answers first (#1672): the record-based cap
    # once fired 13s before the lane's own on MARS.
    # scripts/tests/gate-step-limits.test.sh reads both sides.
    gate_docker_step "ui/shared browser specs (ui/shared/e2e), every file, in the pinned Playwright image (#1537)" --stall-min 15 --cap-min 270
    gate_ui_browser "the ui/shared browser specs" bash scripts/e2e/ui-browser.sh specs ui/shared/e2e/playwright.config.ts

    # Plants each of the assurance UI violations in a copy of ui/shared and
    # needs the narrowed unit tests and specs to go red naming the promise.
    # Its anchors are also checked statically by scripts/selftest/check-anchors.sh.
    gate_docker_step "the assurance UI guards can actually fail (browser mutation self-test, #1532)" --stall-min 15 --cap-min 65
    gate_ui_browser "the assurance UI mutation self-test" bash scripts/e2e/ui-browser.sh run bash scripts/assurance/ui-selftest.sh
  fi
fi
}

gate_seg_two_machine() {
# The two-machine end-to-end backup proof (#356). Everything between
# "nothing installed" and "an artifact is on disk" was proven in pieces and
# nowhere joined up, so the one claim a user actually makes (a fresh
# install can be pointed at a machine and pull a backup off it) had no
# test anywhere. This is that test: two throwaway containers on a temporary
# network, the real installer, a backup set created through the CLI, and
# the artifact compared to the source by digest.
#
# Non-FAST only, like the browser suite, and for the same reason: it costs
# a container image build plus about a minute per case. CI_LOCAL_SKIP_TWO_MACHINE=1
# is the separate out-loud opt-out, and it ledgers.
#
# Three outcomes, not two, which is the part that matters. The script exits
# 3 when this machine CANNOT perform the proof (no Docker, or a daemon that
# refuses a privileged container so docker-in-docker cannot start), and
# that is neither a pass nor a failure: it is ledgered, exactly as a
# missing browser or a stopped daemon is, so the run ends INCOMPLETE and
# says which proof it could not perform. Reporting ok for a backup nobody
# proved would be the single worst version of #160.
#
# It exits 3 for that and for nothing else, which is a guarantee that had
# to be built rather than assumed once the CLI it drives grew its own 3
# (#551). That script keeps its verdict on a status nothing else produces
# and translates once at its own exit, so a `bm` call meeting the CLI's
# refusal arrives here as a failure rather than as a proof this machine
# could not perform; scripts/tests/two-machine-exit-status.test.sh, up
# with the static checks, is what holds it there.
if [ "$FAST" != "1" ]; then
  if [ "${CI_LOCAL_SKIP_TWO_MACHINE:-0}" = "1" ]; then
    gate_note_skip "the two-machine end-to-end backup proof (#356), which is the only test anywhere that a fresh install can pull a real backup off a real machine (CI_LOCAL_SKIP_TWO_MACHINE=1)"
  else
    gate_docker_step "two throwaway machines, one temporary network, one real backup (#356)"
    # Not under `set -e`: exit 3 is a verdict this script has to READ, and
    # `set -e` would end the run on it before the ledger ever saw it.
    set +e
    bash scripts/e2e/two-machine-backup.sh
    two_machine_status=$?
    set -e
    case "$two_machine_status" in
      0) ;;
      3)
        gate_note_skip "the two-machine end-to-end backup proof (#356): this machine could not perform it, and said why above"
        ;;
      *)
        exit "$two_machine_status"
        ;;
    esac
  fi
fi
}

gate_seg_static_b() {
# The static layer checks (issue #165) run even in FAST mode: none of them
# builds, installs or deletes anything, so together they cost seconds, and
# they are the ones a mid-refactor edit is most likely to break.
gate_step "three-layer boundaries, static checks (§7.1, #165)"
bash scripts/architecture/check-layer-manifest.sh
bash scripts/architecture/check-core-dependency-rule.sh
bash scripts/architecture/check-layer-ownership.sh
bash scripts/architecture/check-ui-shared-provider-imports.sh

# The installer's refusals (#262). Standard library only and no Docker, so
# this is a couple of seconds and runs in FAST mode too. It is here rather
# than nowhere for the reason #160 exists: a refusal nobody has watched
# work is not a refusal. Every assertion in it is about the installer
# saying no, which is exactly the behaviour nobody exercises until the day
# it matters on a NAS they cannot debug.
#
# TestTheInstallerStillTravelsAlone is in there too, and it is the reason
# this step matters to #672 rather than only to #262. The installer is
# copied to a NAS on its own and may import only the standard library, so
# it is the one script domain that can never become an bdtools module.
# That claim had no test behind it until this change; now a repo-relative
# import in install_docker_host.py fails here.
gate_step "installer prerequisite refusals (#262)"
# Not piped through `tail`: this file has no `set -o pipefail` (and could
# not portably rely on one, being #!/usr/bin/env sh), so a pipeline's exit
# status under plain `set -e` is its LAST command's -- `tail`, which always
# succeeds. Piping this would let the installer's own test suite fail
# silently forever, the exact "a refusal nobody has watched work is not a
# refusal" failure this step exists to close, one line away from closing it.
(cd scripts/install && python3 -m unittest test_install_docker_host)

# scripts/deploy's own Python tests, which the comment above this step used
# to name as the example of a suite that has never run on a commit. It was
# accurate: 12 assertions about deploy_generic.py refusing an unreadable, a
# world-readable or a missing SSH key, and about the rendered config.yaml
# never containing key material, none of which anything ran. The integration
# suite is deliberately not here -- it stands up a real SFTP container and
# skips itself without one, and this step is the seconds-long half.
#
# Not piped, for the same reason as the line above.
gate_step "deploy_generic's key refusals and rendering (#82/B4.1)"
(cd scripts/deploy && python3 -m unittest test_deploy_generic)

# Presence and the mutation self-test only, and the label says so on
# purpose (#635). These two lines assert that a complete baseline exists
# for the designated host and that the gate is still capable of failing.
# They compare nothing to it: --compare needs a freshly captured candidate,
# which is both harnesses plus an image build, and the timing half of that
# is measured badly on a machine simultaneously running the rest of this
# script.
#
# Six of the seven metrics are therefore still enforced only by somebody
# choosing to capture. The seventh, image_size_bytes, is enforced on every
# full run by apps/generic/tests/dockercli's
# TestTheBuiltImageIsInsideTheRecordedSizeBudget, up in the "apps/generic
# go build, vet, test -race" step, because that package already builds the
# image and the metric has no noise to need a quiet machine for. Naming it
# here rather than leaving the ledger to say "baseline present" is the
# whole difference between a check that exists and a check that runs: this
# step's own label was accurate while it was the only perf line in the
# gate, and would have gone on reading as if nothing had changed after the
# assertion landed somewhere else.
gate_step "performance baseline present, its gate can fail, and the image size is enforced in apps/generic (#165, #635)"
bash scripts/perf/check-baseline.sh
bash scripts/perf/selftest.sh

# The /api/v1 contract gate (#166). Until it was wired in here, nothing
# bound for main ran it: without these two
# lines the byte-for-byte binding comparison, the implementation-type leak
# scan and all 15 of their mutation controls had never executed on a commit
# at all (#166, PR #194 review M1). Unconditional, FAST included: together
# they take under 20 seconds, and check-contract-drift.sh is the only thing
# that catches a hand edit to a generated binding that keeps its digest
# intact.
gate_step "/api/v1 bindings match the contract, and no implementation type leaks (#166)"
bash scripts/api/check-contract-drift.sh

# The contract generator answers the same way every time it is run (#1066).
# check-contract-drift.sh regenerates and compares, so a generator that can
# answer two ways fails a clean tree about half the time.
gate_step "the contract generator is deterministic (#1066)"
bash scripts/tests/api-generator-is-deterministic.test.sh

# The other half of #166's "generated from it or mechanically validated
# against it": ui/shared/src/api/client.ts is hand-written on top of the
# generated module, so its request paths are string literals that the
# binding comparison above cannot see. Fourteen of them named operations
# neither the contract nor the router had, and four of the six shipped
# pages failed against a real backend while every suite stayed green
# (#211). Static, so it costs about a second and needs no npm install.
gate_step "every /api/v1 path the shared client builds is a declared operation (#211)"
bash scripts/api/check-client-paths.sh

# --stall-min 15: its Go controls run go test with no -timeout, so go's own
# 10 minutes plus the build can pass in silence (#1672; the rule and its
# reasons are in scripts/tests/gate-step-limits.test.sh).
gate_step "the /api/v1 contract gates can actually fail (mutation self-test)" --stall-min 15
bash scripts/api/selftest.sh

# No already-published provenance record is rewritten (#895, R2.8, FR-43).
#
# `provenance/**` records artifacts that have been PUSHED, and the epic's
# own words for it are "regenerated forward, never rewritten". Nothing
# checked that. distribution/packaging's TestComplianceArtifactsMatchThisTree
# asks the opposite question -- whether the checked-in bytes are what this
# tree generates -- and is green against a bundle that quietly restates the
# registry digest of a release that shipped six months ago.
#
# Four fields for a version whose record says `published`: the flag itself,
# the recorded build version, the architectures, and the per-architecture
# registry digests. Deliberately NOT the derived digests of NOTICE, the
# licence inventory, the SBOM and the checksum manifest, which track the
# tree and moved in most of the commits that have ever touched that file;
# and deliberately not `imageReference` or `signing.identity`, because
# FR-41 moves the registry path and re-issues the identity at the cutover
# and a guard that froze them would refuse the change it was written for.
# The self-test in the non-FAST block below holds both of those exclusions
# as controls, so neither can be tightened by accident into a guard nobody
# can land the cutover past.
#
# One `git show` and one JSON compare, so it belongs up here with the
# other seconds-long sweeps rather than behind the Go suites.
gate_step "no published release's provenance record has been restated (#895, R2.8)"
bash scripts/release/check-published-provenance.sh
}

gate_seg_static_c() {
if [ "$FAST" != "1" ]; then
  gate_step "release-manifest generator guards (#174)"
  bash scripts/tests/record-release-hashes-guards.test.sh

  gate_step "post-publish registry metadata recorder"
  bash scripts/tests/record-published-release.test.sh

  # The local-gate recorder's refusals, and the record it writes (#1260).
  # It is the one step of release prep that turns "the gate was green"
  # into something require-release-gate can check, so a recorder that
  # could be talked into writing a record for a run that was not full, or
  # not on this tree, would be the chain's weakest link. Driven against
  # the real script in throwaway repositories with a stub gate committed
  # into each, then the real require-release-gate script is run over what
  # it wrote. No Docker, a few seconds.
  gate_step "the local-gate recorder refuses everything short of a full green run on a clean tree, and the release accepts what it writes (#1260)"
  bash scripts/tests/local-gate-record.test.sh

  # The publish script is the one step in this repository that does
  # something irreversible, and it runs once, on the day it matters. Its
  # six refusals get the same treatment #174's five got: driven against
  # the real script in throwaway repositories, asserting the distinct
  # message rather than only the exit code, through a seam that stops
  # before the first Docker command (#88).
  gate_step "image-publish guards (#88)"
  bash scripts/tests/publish-image-guards.test.sh

  # And the proof the guard above can still go red (#895, R2.8). Row R2.8
  # of docs/conformance/epic-r-matrix.md will not take a check that has
  # never been watched to fire, and this one is an assertion that
  # something never happens, which is the shape that rots silently. Nine
  # cases in throwaway repositories: four refusals, each asserting its own
  # message rather than only the exit code, and five controls -- an
  # untouched record, the four derived digests moving, FR-41's image
  # reference and signing identity moving, a release being cut, and a
  # record that was never published freezing nothing.
  gate_step "the published-provenance guard can actually fail (mutation self-test, #895)"
  bash scripts/tests/published-provenance-guards.test.sh
fi
}

gate_seg_selftest_gate() {
if [ "$FAST" != "1" ]; then
  # The self-test runs this very script against synthetic checkouts, so
  # without a marker the recursion terminates only by whatever the fixture
  # happens to lack. CI_LOCAL_SELFTEST makes that an enforced invariant
  # instead, and a nested run says out loud that it stopped.
  if [ "${CI_LOCAL_SELFTEST:-0}" = "1" ]; then
    echo "==> gate self-test: already inside one, not recursing (CI_LOCAL_SELFTEST=1)"
    gate_note_skip "this gate's own self-test, because this run is itself nested inside one (CI_LOCAL_SELFTEST=1)"
  else
    gate_step "gate self-test (scripts/tests/ci-local-gate.test.sh)"
    CI_LOCAL_SELFTEST=1 bash scripts/tests/ci-local-gate.test.sh
  fi

  gate_step "architecture rules can actually fail (mutation self-test)"
  bash scripts/architecture/selftest.sh

  # Its scratch bound (#1580): at most two tree copies alive at once, each
  # removed right after its case, also when the case raises or is stopped.
  # Stand-ins for the copy and every check, so it takes a second.
  gate_step "the architecture self-test holds at most two tree copies and removes each after its case (#1580)"
  bash scripts/tests/architecture-selftest-scratch.test.sh

  # The three self-tests of the gate's own machinery that sat in the tree and
  # in no step (#1533). Each runs against stand-ins for docker, ssh and rsync
  # and a throwaway repository, so there is no daemon and no network: the
  # shard runner (about a second), the --shard i/n split of the
  # recovery and assurance self-tests (a few seconds), and the release e2e hooks
  # and script (under half a minute, it makes a throwaway signing key).
  gate_step "the shard runner refuses what it should and speaks the recorder's verdicts (#1460, #1533)"
  bash scripts/tests/run-gate-shard.test.sh

  gate_step "the --shard i/n split of the self-tests runs every control in exactly one slice (#1461, #1533)"
  bash scripts/tests/selftest-shard.test.sh

  gate_step "the release e2e hooks, the stamp guard and the release script behave as documented (#1375, #1533)"
  bash scripts/tests/release-e2e.test.sh
fi
}

gate_seg_static_d() {
if [ "$FAST" != "1" ]; then
  # The detector itself, shown to fire (#417). Every `go test` above now
  # carries -race, and -race is the kind of check this repository has
  # learnt to distrust on sight: it exits 0 whether it looked or not, so a
  # tree with no race and a detector that was never asked produce the same
  # output. This plants a real data race in real product source, in a copy
  # of the tree, and requires the detector to catch it AND to name the
  # write that planted it AND to be the reason it was caught, by showing
  # the same mutant going green with the flag off. One package, one test,
  # three runs: about ten seconds on a warm cache, because the mutant is
  # one file deep in a leaf package and Go's build cache is
  # content-addressed, so each copy rebuilds core/service and nothing
  # under it.
  gate_step "the race detector can actually catch a race (mutation self-test, #417)"
  bash scripts/race/selftest.sh

  # And the formatting gate, shown to fire (#417). Same argument as the
  # block above, applied to the check that turned out to be missing
  # altogether: two Go files in this tree were not gofmt-clean and nothing
  # noticed, because `go build`, `go vet` and every linter that was enabled
  # are all indifferent to layout. Both halves get a planted violation
  # here, the sweep and .golangci.yml's own formatter, including one
  # planted outside every module, which is the case the per-module linter
  # cannot make. A second on a warm cache.
  gate_step "the formatting gate can actually fail (mutation self-test, #417)"
  bash scripts/format/selftest.sh

  # And the package-documentation gate, shown to fire, in a pair (#526). One
  # control promotes a comment to sit adjacent to `package` in the real
  # core/service/activity.go and requires check-package-doc.sh to go red and
  # name the file. Its complement runs the SAME mutation past
  # check-comments-only.sh and requires that one to stay silent, because a
  # promotion changes what `go doc` prints and changes no token: without the
  # pair, "the check went red" is also what a checker that fires on any edit
  # would produce, and the two halves of #526's fix would not be measuring
  # different things. A third control renames a function so the silent half
  # is shown able to speak. The plant goes into the real file and is put
  # back, and the last control checks the putting back. A second or two.
  gate_step "the package-documentation gate can actually fail, and its independent half stays quiet (mutation self-test, #526)"
  bash scripts/docs/selftest.sh
fi
}

gate_seg_selftest_compat() {
if [ "$FAST" != "1" ]; then
  # EPIC E's FR-35 compatibility gate, shown to fire (#242). core/tests/compat
  # is a wall of "nothing about a medium-free deployment moved" assertions,
  # and every one of them is the shape this repository keeps finding passing
  # for the wrong reason. This runs each cell against a real planted
  # violation in a copy of the tree, including the two the EPIC E spec's own
  # section 4 table names by hand. It costs a few minutes because every
  # mutant builds core/ and retnd and runs a real capture; that is
  # the price of the corpus meaning anything.
  gate_step "the FR-35 compatibility cells can actually fail (mutation self-test, #242)"
  bash scripts/compat/selftest.sh
fi
}

gate_seg_selftest_conformance() {
if [ "$FAST" != "1" ]; then
  # EPIC E's composed conformance scenario, shown to fire (#242). Same
  # argument as the block above, applied to the other half of that issue:
  # core/tests/conformance runs the three-tier chain against MinIO and
  # watches FR-30's standing invariant at every event that could falsify it,
  # and a watcher nobody has watched catch something is a watcher that
  # certifies nothing. This plants nine violations in real product files,
  # including the gate line's own, and each one has to turn the suite red
  # AND name the promise it broke.
  gate_docker_step "the composed conformance cells can actually fail (mutation self-test, #242)"
  bash scripts/conformance/selftest.sh
fi
}

gate_seg_selftest_retention() {
if [ "$FAST" != "1" ]; then
  # FR-20's retention apply, shown to fire (#602). The two blocks above are
  # this repository's answer to "an assertion nobody has watched fail",
  # applied to the two surfaces that already had one; this is the same
  # answer applied to the one thing in this product that removes a local
  # restore point. The cheapest way to pass "every previewed DELETE is
  # gone and every KEEP survives" is to delete nothing, or to plan
  # nothing, so each of those is planted here as a real violation in a
  # real product file and the evidence has to go red naming the promise.
  # The last control is planted in the test rather than in the product:
  # the exact-set comparison itself is made vacuous, and its own positive
  # control is the only thing that can see it. No Docker, a few minutes.
  gate_step "the retention-apply evidence can actually fail (mutation self-test, #602)"
  bash scripts/retention/selftest.sh

  # Discovery's readability probe (#1541), shown to fire the same way: a plant
  # in discovery.go, the narrowed suite red naming the promise. It had a
  # self-test and no step until the guard-is-a-gate-step check named it.
  gate_step "the discovery readability probe can actually fail (mutation self-test, #1541)"
  bash scripts/discovery/selftest.sh
fi
}

gate_seg_selftest_recovery() {
if [ "$FAST" != "1" ]; then
  # EPIC Q (#976): recovery is a read path that hands out retained backup
  # data, and its guards (the cost refusal, the digest compare, a grant that
  # cannot be used twice or from another session, a target that cannot
  # leave the staging root, the audit rows, the route's tier) are exactly
  # the kind that pass on the tree they ship with and could not fail. Each
  # is planted as a real violation in a real product file and the narrowed
  # suite has to go red naming the promise. No Docker, a few minutes.
  # --stall-min 15: each control prints when it ends and runs go test
  # -timeout 10m, so 10 minutes plus the build can pass in silence (#1672,
  # scripts/tests/gate-step-limits.test.sh).
  gate_step "the recovery guards can actually fail (mutation self-test, #976)" --stall-min 15
  bash scripts/recovery/selftest.sh

  # Issue #1528: retiring a FAILED backup. Each row of
  # docs/conformance/issue-1528-matrix.md's RV.n ledger is planted in a
  # scratch copy of the tree (the automatic pass skipped, absence read from
  # any Stat error, the successor's "newer" dropped, the #662 shape, the way
  # back, the .partial, an edge into RETIRED from COMMITTED, a record that
  # can fail without its transition rolling back, RETIRED counted as a
  # failure, the operator path skipping the source check, the agent
  # acknowledgement, the list cursor), and the narrowed test has to go red
  # naming itself and green again on the unplanted copy. No Docker.
  gate_step "the retirement guards can actually fail (mutation self-test, #1528)"
  bash scripts/retire/selftest.sh
fi
}

gate_seg_selftest_assurance() {
if [ "$FAST" != "1" ]; then
  # EPIC Q (#988): the other half of the same promise. The recovery
  # self-test above plants violations in the route, the grant and the
  # budgets; this one plants them in what keeps Stored, Content verified
  # and Recovery tested three claims: an existence check relabelled as
  # content, a recovery test stored as a pass with no digest match, a copy
  # nobody scheduled read as fresh, a per-set override that forgets the
  # deployment, a feed row per copy, a crash that leaves staging behind, the
  # no-download promise made again. No Docker, a few minutes.
  gate_step "the assurance guards can actually fail (mutation self-test, #988)"
  bash scripts/assurance/selftest.sh
fi
}

# gate_memory_gib: the memory this run may use, in whole GiB: the cgroup limit
# when there is one (a container's fence), else the machine's. A test can set
# CI_LOCAL_MEMORY_LIMIT_BYTES to stand in for the measurement.
gate_memory_gib() {
  bytes="${CI_LOCAL_MEMORY_LIMIT_BYTES:-}"
  if [ -z "$bytes" ] && [ -r /sys/fs/cgroup/memory.max ]; then
    bytes="$(cat /sys/fs/cgroup/memory.max)"; [ "$bytes" = max ] && bytes=""
  fi
  if [ -z "$bytes" ] && [ -r /sys/fs/cgroup/memory/memory.limit_in_bytes ]; then
    bytes="$(cat /sys/fs/cgroup/memory/memory.limit_in_bytes)"
  fi
  meminfo=""
  if [ -r /proc/meminfo ]; then meminfo="$(awk '/^MemTotal:/ { print $2 * 1024 }' /proc/meminfo)"; fi
  [ -n "$meminfo" ] || meminfo="$(sysctl -n hw.memsize 2>/dev/null || echo 0)"
  if [ -z "$bytes" ] || [ "$bytes" -gt "$meminfo" ] 2>/dev/null; then bytes="$meminfo"; fi
  echo $(( ${bytes:-0} / 1073741824 ))
}

# gate_selftest_with_ledger <script>: run a mutation self-test and put every
# control it could not run on this host into the skip ledger, one line each.
gate_selftest_with_ledger() {
  skips="$(mktemp "${TMPDIR:-/tmp}/selftest-skips.XXXXXX")"
  RETND_SELFTEST_SKIP_LEDGER="$skips" bash "$1" || { rc=$?; rm -f "$skips"; return "$rc"; }
  while IFS= read -r line; do
    [ -n "$line" ] && gate_note_skip "$line"
  done <"$skips"
  rm -f "$skips"
}

gate_seg_selftest_encryption() {
if [ "$FAST" != "1" ]; then
  # EPIC N and EPIC T (#1531): the retained-copy encryption and suite guards
  # (the key never on a surface, no downgrade of a suite, a copy that opens
  # with the documented command, the copy and verify path, the UI chips) are
  # planted as real violations in real product files, and each has to turn
  # the narrowed suite red naming the test the matrix row cites. The matrix
  # rows' PASS dates are from the 0.8.0 run; this is what makes them a
  # standing claim. Only the anchors half ran before, from check_anchors.py.
  # No Docker, and the longest of the self-tests, so it is a shard of its own.
  #
  # Some controls need more than Go: the Playwright lock, a docker daemon, node
  # with ui/shared's dependencies, or the retnd-tests checkout. Which ones is
  # pinned in scripts/bdtools/<x>/lanes.txt, and the self-test probes this host
  # for each lane: a control whose lane is missing is not run, and lands in this
  # gate's skip ledger through RETND_SELFTEST_SKIP_LEDGER, so the shard ends
  # INCOMPLETE naming it instead of ok. The browser specs, the browser contrast
  # check and the capture rig go through the Chromium lane (scripts/e2e/ui-browser.sh,
  # #1537, #1543), which needs only the docker daemon a gate container has; the
  # three retnd-tests cases have no shard with the checkout yet (#1548).
  #
  # Two copycrypt controls allocate several GiB and are OOM-killed under about
  # 8 GiB, so the shard needs 9 GiB: scripts/release/run-gate-shard.sh asks for
  # a 9g fence and refuses a smaller one, and this step refuses to start the
  # self-test in less than that, out loud, which also ends INCOMPLETE.
  #
  # --stall-min 65: a control prints when it ends, and the longest go test
  # -timeout a control sets is 60 minutes. The step's window sits above that,
  # so a hung control is named by its own timeout first (#1672,
  # scripts/tests/gate-step-limits.test.sh). The cap stays the default one.
  gate_step "the encryption and suite guards can actually fail (mutation self-test, #1531)" --stall-min 65
  have_gib="$(gate_memory_gib)"
  if [ "$have_gib" -lt 9 ]; then
    gate_note_skip "the encryption self-test (#1531): this run has ${have_gib} GiB and its two copycrypt controls need 9 GiB, so none of it ran. Run the selftest-encryption shard with a 9g fence (scripts/release/run-gate-shard.sh asks for one)"
  else
    # Three controls run a case of retnd-tests against a build of the planted
    # copy and need RETND_TESTS_DIR and RETND_COMMIT (#1548). The pinned checkout
    # comes anonymously over https at the sha scripts/e2e/tests-repo.pin names
    # (no token, no login), cached per sha. A remote this host cannot reach
    # exits 3: the controls then skip by name into the ledger as before and the
    # shard ends INCOMPLETE. A wrong pin exits 1 and fails the shard.
    tests_dir="" tests_commit=""
    if tests_dir="$(bash scripts/e2e/tests-checkout.sh)"; then
      tests_commit="$(git rev-parse HEAD 2>/dev/null || true)"
    else
      rc=$?
      [ "$rc" = "3" ] || return "$rc"
      tests_dir=""
    fi
    if [ -n "$tests_dir" ] && [ -n "$tests_commit" ]; then
      RETND_TESTS_DIR="$(printf '%s\n' "$tests_dir" | tail -n 1)" RETND_COMMIT="$tests_commit" \
        gate_selftest_with_ledger scripts/encryption/selftest.sh
    else
      gate_selftest_with_ledger scripts/encryption/selftest.sh
    fi
  fi
fi
}

gate_seg_selftest_agents() {
if [ "$FAST" != "1" ]; then
  # EPIC U (#1530): the catalogue, the denied routes and the projection, the
  # transports, the audit trail, the confirmations and the Agents card, each
  # planted as a real violation in a real product file, and the narrowed suite
  # has to go red naming the test. core/tests/agentgate refuses a PASS row
  # whose control label is not in the file, and this is what runs them. Controls
  # this host has no lane for go to the skip ledger, as for the encryption one.
  # --stall-min 30: the longest go test -timeout a control sets is 25 minutes
  # (#1672, scripts/tests/gate-step-limits.test.sh).
  gate_step "the agent (MCP) guards can actually fail (mutation self-test, #1530)" --stall-min 30
  gate_selftest_with_ledger scripts/agents/selftest.sh
fi
}

gate_seg_selftest_observability() {
if [ "$FAST" != "1" ]; then
  # EPIC O (#899): the scrape endpoint's guards (the verifier, the inline
  # token refusal, the refusal limiter, the digest-only store, the action-log
  # silence, the absent-not-zero workflow half, the report cache, and the
  # scrape credential and the agent token never crossing) each planted as a
  # real violation in a real product file, and the narrowed suite has to go
  # red naming the promise. PV1 and PV2 of the spec live here; O1.2 to O1.4
  # add theirs to the same file. No Docker, a few minutes.
  gate_step "the observability guards can actually fail (mutation self-test, #899)"
  bash scripts/observability/selftest.sh
fi
}

gate_seg_structure() {
if [ "$FAST" != "1" ]; then
  gate_step "repository-structure dependency rules (§7.1), by actual deletion"
  bash scripts/architecture/check-core-dependency-rule.sh
  bash scripts/architecture/verify-core-without-apps.sh
  bash scripts/architecture/verify-core-without-distribution.sh
  bash scripts/architecture/verify-ui-shared-without-provider-sdks.sh
  bash scripts/architecture/verify-ugos-removable.sh
fi
}

gate_seg_static_e() {
# rclone-upgrade-gate.yml's own point was never to fully automate FR-2, only
# to enforce compilation/tests (already covered above) and print a reminder
# for the one step that's inherently manual: a human reading the upstream
# release notes before approving. Keep printing that reminder locally too.
if git diff --cached --name-only 2>/dev/null | grep -qE '^core/go\.(mod|sum)$'; then
  echo "==> core/go.mod or core/go.sum changed: docs/rclone-upgrade.md (FR-2) requires reading the rclone release notes between the old and new pinned version before this merges. This script cannot verify that happened."
fi
}

gate_seg_gh_rows() {
# The BLOCKED-row citation check, alone (#1457). A serial run covers it inside
# core/'s go test above; as a shard it is the one that needs gh, so it can run
# where gh is logged in while the container shards run without it (#1331).
gate_step "core/tests/compat: every BLOCKED conformance row cites an issue that is still open (#1331, TestEveryBlockedRowCitesAnIssueThatIsStillOpen)"
gate_github_ledger_open
(cd core && GOWORK=off go test -race -count=1 -run '^TestEveryBlockedRowCitesAnIssueThatIsStillOpen$' ./tests/compat)
gate_github_ledger_close
}

# The segments, in the order a serial run walks them (#1457). Each names the
# shard(s) it belongs to; --shard <name> runs the ones that list it, a run with
# no flag runs all of them, in this order, which is the order the steps had
# before they were split.
gate_run_segment "static" gate_seg_static_a
gate_run_segment "core-kopia" gate_seg_core_build
gate_run_segment "core-kopia core-rest" gate_seg_core_test
gate_run_segment "core-rest" gate_seg_core_watch
gate_run_segment "core-docker" gate_seg_core_docker
gate_run_segment "apps" gate_seg_apps_a
gate_run_segment "js" gate_seg_js_upk
gate_run_segment "apps" gate_seg_apps_b
gate_run_segment "js" gate_seg_js_a
gate_run_segment "e2e" gate_seg_e2e
gate_run_segment "ui-browser" gate_seg_ui_browser
gate_run_segment "two-machine" gate_seg_two_machine
gate_run_segment "static" gate_seg_static_b
gate_run_segment "static" gate_seg_static_c
gate_run_segment "selftest-gate" gate_seg_selftest_gate
gate_run_segment "static" gate_seg_static_d
gate_run_segment "selftest-compat" gate_seg_selftest_compat
gate_run_segment "selftest-conformance" gate_seg_selftest_conformance
gate_run_segment "selftest-assurance" gate_seg_selftest_retention
gate_run_segment "selftest-recovery" gate_seg_selftest_recovery
gate_run_segment "selftest-assurance" gate_seg_selftest_assurance
gate_run_segment "selftest-assurance" gate_seg_selftest_observability
gate_run_segment "selftest-encryption" gate_seg_selftest_encryption
gate_run_segment "selftest-agents" gate_seg_selftest_agents
gate_run_segment "structure" gate_seg_structure
gate_run_segment "static" gate_seg_static_e
gate_run_shard_only gh-rows gate_seg_gh_rows

# Never a bare echo: "ok" has to be earned by an empty skip ledger.
gate_summary
