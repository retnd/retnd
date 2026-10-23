#!/usr/bin/env bash
# Self-test for the honesty guarantees scripts/ci-local.sh makes about its
# own final line and its own exit status (issue #160).
#
# The defect this pins down: the gate used to treat "this JS workspace has no
# node_modules/" as "print a note and carry on", and then still ended with
# "==> ci-local: ok". node_modules/ is gitignored and every agent on this
# project works in a fresh `git worktree`, so the DEFAULT state of a new
# checkout was "the frontend lint/typecheck/vitest suites and the
# cross-provider conformance suite never ran, and the gate said ok anyway".
# The final line is what this project reads as merge evidence, so a skip that
# is indistinguishable from a pass in both the exit code and the last line is
# the same class of bug as a test that asserts nothing.
#
# Four layers, because none of them alone is enough:
#
#   Group A drives scripts/lib/ci-local-gate.sh directly, which is where the
#   skip ledger, the exit status and the final line live.
#   Group B runs the REAL scripts/ci-local.sh against synthetic checkouts and
#   proves the preflight refusals.
#   Group C pins the invariant that ci-local.sh has no success line of its
#   own to print.
#   Group D runs the REAL scripts/ci-local.sh all the way to its last line
#   against a complete synthetic checkout, so the wiring between the skip
#   sites and gate_summary is measured rather than assumed. Group B alone
#   never observed that line: every Group B tree dies in the preflight.
#   Group E mutates that wiring on purpose and proves Group D goes red. A
#   review of this branch neutered the four gate_note_skip call sites, and
#   separately rewrote the three `case "$(gate_workspace_state ...)"`
#   subjects to a constant, and the suite stayed green at 37/37 both times.
#   E1 and E2 are those two exact mutations, kept here so they cannot come
#   back unnoticed.
#
# Every negative assertion here is paired with a positive control, because
# "the gate did not print ok" is also true when the gate fell over for a
# reason that has nothing to do with this fix. Group B's cases assert WHY the
# run failed (the workspace is named, the fix command is printed, and no Go
# step ran), and D1 is the control for the whole of Group D: the same fixture,
# fully installed, must reach "ci-local: ok" with status 0, or every
# INCOMPLETE assertion below it would pass against a gate that can never say
# ok at all.
#
# Run directly (`bash scripts/tests/ci-local-gate.test.sh`) or let the gate
# run it: scripts/ci-local.sh invokes it in a non-FAST run.
set -uo pipefail

SCRIPTS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$SCRIPTS_DIR/.." && pwd)"
GATE_LIB="$SCRIPTS_DIR/lib/ci-local-gate.sh"

# The sandbox lives inside the repository (gitignored) rather than under
# $TMPDIR so that everything this test creates and deletes stays within the
# checkout it belongs to.
SANDBOX="$REPO_ROOT/.ci-local-gate-test"
case "$SANDBOX" in
  /*/.ci-local-gate-test) ;;
  *) echo "refusing to use sandbox path [$SANDBOX]" >&2; exit 1 ;;
esac
rm -rf "$SANDBOX"
mkdir -p "$SANDBOX"
trap 'rm -rf "$SANDBOX"' EXIT

# The exit status ci-local.sh uses for "ran, but skipped something".
INCOMPLETE=3

checks=0
failures=0

# Every assertion goes through pass or fail and both count, so the run
# ends with a number rather than an impression. That count is what caught
# this suite's own worst failure: a review neutered the gate's skip
# ledger twice and it stayed green at 37 checks both times, which is why
# Groups D and E exist and why the total is worth reading.
pass() { checks=$((checks + 1)); printf '    ok   %s\n' "$1"; }
fail() {
  checks=$((checks + 1))
  failures=$((failures + 1))
  printf '    FAIL %s\n' "$1" >&2
  if [ $# -gt 1 ]; then
    printf '%s\n' "$2" | sed 's/^/         | /' >&2
  fi
}

assert_contains() { # <label> <needle> <haystack>
  case "$3" in
    *"$2"*) pass "$1" ;;
    *) fail "$1 -- expected output to contain: $2" "$3" ;;
  esac
}

assert_not_contains() { # <label> <needle> <haystack>
  case "$3" in
    *"$2"*) fail "$1 -- expected output NOT to contain: $2" "$3" ;;
    *) pass "$1" ;;
  esac
}

assert_eq() { # <label> <want> <got>
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 -- want [$2], got [$3]"; fi
}

assert_nonzero() { # <label> <status>
  if [ "$2" -ne 0 ]; then pass "$1"; else fail "$1 -- expected a non-zero exit, got 0"; fi
}

# ---------------------------------------------------------------- fixtures

# Printed by the stub self-test the synthetic trees carry. A nested run of
# ci-local.sh inside this suite must execute that stub and not the real
# suite, and D1/D7 assert on this marker in both directions.
SELFTEST_STUB='SELFTEST-STUB-RAN'

# Printed by the stub browser-e2e step the synthetic trees carry. Group G
# asserts on this marker in both directions, the same way D1/D7 do for the
# self-test stub.
E2E_STUB='E2E-GATE-STUB-RAN'

# The same, for the two-machine end-to-end backup proof (#356). Group I
# asserts on it in both directions. The real script builds a container
# image and stands up docker-in-docker; this fixture measures which steps
# the gate chooses to run, so the stub prints a marker and succeeds.
TWO_MACHINE_STUB='TWO-MACHINE-STUB-RAN'

# And for the repository-wide gofmt sweep (#417), which Group L watches in
# both directions.
GOFMT_STUB='GOFMT-SWEEP-STUB-RAN'

# The same, for the check that vets and lints the Go files no module owns
# (#417). It lives in scripts/architecture/ but runs near the top of the
# gate with the sweep rather than with the other architecture checks, and
# Group L is where that placement is pinned.
UNOWNED_STUB='UNOWNED-GO-STUB-RAN'

make_tree() { # -> path of a synthetic checkout carrying only the gate scripts
  # mktemp, not a counter: this runs inside $( ), so a counter would increment
  # in the subshell and every caller would get the same directory back. That
  # bug made three cases share one tree and each inherit the last one's
  # workspaces, which is exactly the confound the controls below exist to
  # catch, and it did catch it.
  tree="$(mktemp -d "$SANDBOX/tree.XXXXXX")"
  mkdir -p "$tree/scripts/lib" "$tree/bin"
  # Every synthetic checkout is a git repository of its own (#1260). The
  # sandbox sits INSIDE the real checkout, so before this a hook driven
  # from a fixture resolved `git write-tree`, `git diff --cached` and
  # `git rev-parse --git-path` against the real repository's index and
  # git directory: the red-commit rule would have read the developer's
  # own staged files, and the gate marker would have been written into
  # the real worktree's git dir by a self-test. A nested repository is
  # invisible to the outer one (it is an untracked directory, and the
  # sandbox is gitignored anyway), and every git question a fixture asks
  # now has the fixture's own answer.
  (cd "$tree" && git init -q -b feature && git config user.email t@example.invalid && git config user.name t) >/dev/null 2>&1
  cp "$SCRIPTS_DIR/ci-local.sh" "$tree/scripts/ci-local.sh"
  if [ -f "$GATE_LIB" ]; then cp "$GATE_LIB" "$tree/scripts/lib/"; fi
  # The Docker probe is a property of the machine, not of the tree, so every
  # synthetic run gets a stub daemon it controls. run_gate puts $tree/bin
  # first on PATH. If a real docker ever shadowed this stub the docker cases
  # below go red rather than quietly measuring the host.
  set_docker "$tree" available
  printf '%s\n' "$tree"
}

# A stub `docker` on the tree's own PATH, in whichever state the case
# needs. The Docker probe is a property of the machine and every case here
# is about what the gate DOES with the answer, so the answer has to be the
# fixture's to choose: measuring the host would make the docker cases pass
# or fail depending on who ran them.
set_docker() { # <tree> <available|unavailable>
  if [ "$2" = available ]; then
    printf '#!/bin/sh\nexit 0\n' >"$1/bin/docker"
  else
    printf '#!/bin/sh\nexit 1\n' >"$1/bin/docker"
  fi
  chmod +x "$1/bin/docker"
}

add_workspace() { # <tree> <relpath> <installed|uninstalled> [nolock]
  mkdir -p "$1/$2"
  # Every script the gate invokes on a JS workspace, mapped to true. The
  # fixture is measuring which steps the gate chooses to run, not what they
  # do, so the cheapest script that exists and succeeds is the right one.
  cat >"$1/$2/package.json" <<'PKG'
{
  "name": "fixture",
  "private": true,
  "scripts": {
    "lint": "true",
    "typecheck:providers": "true",
    "eslint": "true",
    "test": "true",
    "build": "true"
  }
}
PKG
  if [ "${4:-lock}" != nolock ]; then
    printf '{"lockfileVersion":3}\n' >"$1/$2/package-lock.json"
  fi
  if [ "$3" = installed ]; then mkdir -p "$1/$2/node_modules/.fixture"; fi
}

# A real module, not a marker directory: the gate runs `go build` and
# `go vet` per module, so a fixture module has to compile. One stub file
# is the cheapest thing that does, and keeping it minimal is what lets a
# full synthetic run finish in seconds.
add_go_module() { # <tree> <relpath> <module name>
  mkdir -p "$1/$2"
  printf 'module %s\n\ngo 1.21\n' "$3" >"$1/$2/go.mod"
  printf 'package stub\n\n// Stub exists so the module has something to build.\nfunc Stub() int { return 1 }\n' >"$1/$2/stub.go"
}

# make_full_tree -> a synthetic checkout a non-FAST run can complete in full
#
# Everything scripts/ci-local.sh reaches for on the way to gate_summary, in
# its cheapest honest form: two real (tiny) Go modules, the project's own
# golangci config, installed JS workspaces whose scripts are `true`, and
# stubs for the self-test and the four structure proofs. Group B's trees
# deliberately have none of this, which is why no Group B case has ever seen
# the final line.
make_full_tree() {
  tree="$(make_tree)"

  cp "$REPO_ROOT/.golangci.yml" "$tree/.golangci.yml"

  add_go_module "$tree" core stubcore
  add_go_module "$tree" core/internal/stub stubcoreinternal
  rm -f "$tree/core/internal/stub/go.mod"
  # issue #256: the Docker-backed suites step now runs
  # `go run ./cmd/gotestwatch ...` instead of a plain `go test`, so this
  # fixture needs that package to actually exist and resolve, the same
  # reason every other stub in this function exists. The stub does nothing
  # with its arguments (a bare `func main() {}` ignores them entirely),
  # which is enough: this fixture measures which steps the gate chooses to
  # run, not what gotestwatch itself does with real packages.
  mkdir -p "$tree/core/cmd/gotestwatch"
  printf 'package main\n\nfunc main() {}\n' >"$tree/core/cmd/gotestwatch/main.go"
  # And a test file, because the gate now COMPILES that package's own test
  # binary and runs it, to read the three-outcome status `go test` would
  # flatten (#533). `go test -c` on a package with no test files exits 0
  # and writes no binary at all, so without this the step would run a path
  # that is not there, exit 127 under set -e, and take every full-tree case
  # with it. A passing stub is enough: this fixture measures which steps
  # the gate runs, not what the real watchdog's own suite proves.
  printf 'package main\n\nimport "testing"\n\nfunc TestStub(t *testing.T) {}\n' >"$tree/core/cmd/gotestwatch/main_test.go"
  add_go_module "$tree" apps/common stubcommon
  # The distribution layer became its own Go module in #165, and ci-local.sh
  # builds, vets, tests and lints it like every other module. Without it here
  # the gate dies on `cd distribution` in every full-tree case, which is the
  # same shape of miss the two comments further down record.
  add_go_module "$tree" distribution stubdistribution
  # And distribution/packaging as a package of that module, because #417
  # gave it a step of its own: it is the one Go suite the gate runs without
  # -race, so `go test ./packaging/` is now a real path the gate walks and
  # a tree without it dies on it, which is the same miss again.
  add_go_module "$tree" distribution/packaging stubpackaging
  rm -f "$tree/distribution/packaging/go.mod"

  add_workspace "$tree" ui/shared installed
  add_workspace "$tree" apps/common/tests installed

  # A stub self-test, so a nested run terminates on purpose rather than on
  # whatever the fixture happens to lack. ci-local.sh's CI_LOCAL_SELFTEST
  # guard is the second, independent terminator; D7 measures it.
  mkdir -p "$tree/scripts/tests"
  printf '#!/usr/bin/env bash\necho "%s"\nexit 0\n' "$SELFTEST_STUB" \
    >"$tree/scripts/tests/ci-local-gate.test.sh"

  # The other guard suite ci-local.sh runs before the self-test. It arrived
  # with #182 without a stub here, and `bash` on a path that does not exist
  # exits 127 under set -e, so every full-tree case below that step died for
  # a reason unrelated to what it measured, Group D's own control included.
  printf '#!/usr/bin/env bash\nexit 0\n' \
    >"$tree/scripts/tests/record-release-hashes-guards.test.sh"

  # .husky/pre-commit is the one caller that has to act on the exit status
  # rather than read the prose, so Group F drives the real hook in this tree.
  # prepare-commit-msg is the one that stamps the message afterwards
  # (#1260), and Group P drives it the same way. The hook is INSTALLED in
  # the fixture's own config, because a complete checkout has it installed
  # and the gate warns about one that does not (Group N measures the
  # warning both ways).
  mkdir -p "$tree/.husky"
  cp "$REPO_ROOT/.husky/pre-commit" "$tree/.husky/pre-commit"
  if [ -f "$REPO_ROOT/.husky/prepare-commit-msg" ]; then
    cp "$REPO_ROOT/.husky/prepare-commit-msg" "$tree/.husky/prepare-commit-msg"
  fi
  chmod +x "$tree/.husky/"*
  (cd "$tree" && git config core.hooksPath .husky)

  # The three-layer checks and their mutation self-test (#165) join the four
  # structure proofs here: static ones run even under CI_LOCAL_FAST, so every
  # tree that gets as far as them needs all of them present.
  mkdir -p "$tree/scripts/architecture"
  for arch in check-layer-manifest check-core-dependency-rule \
              check-layer-ownership check-ui-shared-provider-imports \
              selftest verify-core-without-apps verify-core-without-distribution \
              verify-ui-shared-without-provider-sdks verify-ugos-removable \
              check-unowned-go; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/architecture/$arch.sh"
  done
  # check-unowned-go prints a marker on top of that, because Group L
  # watches for it in both directions the way Group G watches the browser
  # e2e stub. The real one vets and lints every Go file outside every
  # module, through a throwaway module per directory.
  printf '#!/usr/bin/env bash\necho "%s"\nexit 0\n' "$UNOWNED_STUB" \
    >"$tree/scripts/architecture/check-unowned-go.sh"

  # The performance baseline gate and its own self-test (#165), stubbed for
  # the same reason: this fixture measures which steps the gate chooses to
  # run, and the real ones read a baseline record captured on one host.
  mkdir -p "$tree/scripts/perf"
  for perf in check-baseline selftest; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/perf/$perf.sh"
  done

  # The brand-drift guard and its own mutation self-test (#794), stubbed for
  # the same reason as everything above, and with the same failure mode
  # without it: both run unconditionally, FAST included, so a gate step
  # pointed at a path this fixture does not have exits 127 under `set -e`
  # and every case below it dies for a reason that has nothing to do with
  # what it measures. The real guard greps every tracked file in a real
  # repository against an allowlist, and the real self-test builds a dozen
  # throwaway git repositories; a synthetic tree has neither and no reason
  # to have either.
  #
  # The former-name guard and its self-test (#1109) share that step, and
  # they landed in the gate without landing here, which is the lesson the
  # paragraph above records and the thing it says will happen: every
  # full-tree case below, D1's control included, went red at once on a 127
  # that had nothing to do with what any of them measured, and the gate's
  # own self-test step had been red on main since e7f9f9a7 before #1260
  # found it.
  mkdir -p "$tree/scripts/rename"
  for rename in check-brand-drift selftest check-no-former-name selftest-former-name; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/rename/$rename.sh"
  done

  # FR-44's two brand checks and their shared mutation self-test (#893),
  # which share the brand-drift guard's gate step and therefore its reason
  # for being stubbed: they run unconditionally, FAST included. The real
  # ones read every SVG in a real repository and parse a real manifest
  # against the real filesystem, and the real self-test builds seventeen
  # throwaway git repositories.
  mkdir -p "$tree/scripts/brand"
  for brand in check-svg-text check-brand-assets selftest; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/brand/$brand.sh"
  done

  # The docs site's contrast checks (#1054). The token half runs
  # unconditionally, FAST included, and the rendered half is a Docker-backed
  # browser run; a gate step pointed at a path this fixture does not have
  # exits 127 under `set -e`, so both are stubbed, for the same reason as
  # everything above.
  mkdir -p "$tree/scripts/contrast"
  for contrast in check run-browser-check; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/contrast/$contrast.sh"
  done

  # The /api/v1 contract drift check, the client-path check (#211) and
  # their shared mutation self-test (#166). Same reason again, and the same
  # failure mode if any of them is missing: they run unconditionally, FAST
  # included, so a gate step pointed at a path this fixture does not have
  # exits 127 under `set -e` and every case below it fails for a reason
  # that has nothing to do with what it was measuring.
  mkdir -p "$tree/scripts/api"
  for api in check-contract-drift check-client-paths selftest; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/api/$api.sh"
  done

  # The generator's determinism test (#1066), a static gate step next to the
  # drift check it protects, stubbed for the same reason.
  mkdir -p "$tree/scripts/tests"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/tests/api-generator-is-deterministic.test.sh"

  # The rest of the scripts/tests/*.test.sh suites the gate runs by name
  # (first-run report #1049, pin-on-main #1074, the release and publish
  # guards, ...), and the published-provenance check (#895), stubbed for the
  # same reason as everything above: each one drives its own fixture git
  # repositories, rig or Docker, none of which a synthetic tree has.
  for t in first-run-report pin-on-main every-module-builds-on-its-own \
    release-gate-covers-every-job release-refuses-an-ungated-publish \
    release-moves-latest two-machine-ci-verdict record-published-release \
    publish-image-guards published-provenance-guards; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/tests/$t.test.sh"
  done
  mkdir -p "$tree/scripts/release"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/release/check-published-provenance.sh"

  # EPIC E's FR-35 compatibility mutation self-test (#242), stubbed for the
  # third time for the third identical reason. The real one copies the tree
  # sixteen times and builds core/ in each copy; this fixture measures which
  # steps the gate chooses to run, not what they do, and a missing path here
  # exits 127 under `set -e` and takes every case below it with it.
  mkdir -p "$tree/scripts/compat"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/compat/selftest.sh"

  # And EPIC E's composed conformance mutation self-test (#242), for the
  # same reason: the real one copies the tree nine times, builds core/ in
  # each copy and stands up MinIO containers.
  mkdir -p "$tree/scripts/conformance"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/conformance/selftest.sh"

  # FR-20's retention-apply mutation self-test (#602), stubbed for the same
  # reason as the two above and found the same way: the step landed in the
  # gate, this fixture did not grow the path, and every Group D and Group E
  # case went red at once on a 127 that had nothing to do with what any of
  # them was measuring. The paragraph above says that will happen; this is
  # what it looks like when somebody adds a step without reading it. The
  # real one copies the tree eight times and builds core/ in each copy.
  mkdir -p "$tree/scripts/retention"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/retention/selftest.sh"

  # EPIC Q's recovery mutation self-test (#976), stubbed for the reason
  # above: the real one copies the tree a couple of dozen times and builds
  # core/ and apps/common in each copy.
  mkdir -p "$tree/scripts/recovery"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/recovery/selftest.sh"

  # EPIC Q's assurance mutation self-test (#988), stubbed for the same
  # reason: the real one copies the tree a dozen times and builds core/ in
  # each copy.
  mkdir -p "$tree/scripts/assurance"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/assurance/selftest.sh"

  # The race detector's own mutation self-test (#417), stubbed for the
  # seventh time for the seventh identical reason: the real one copies the
  # tree twice, plants a data race in core/service and runs the detector
  # over it, and `bash` on a path that does not exist exits 127 under
  # `set -e` and takes every case below it down with it.
  mkdir -p "$tree/scripts/race"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/race/selftest.sh"

  # The formatting sweep and its own mutation self-test (#417), for the
  # eighth and ninth time for the same reason. The sweep prints a marker
  # because Group L watches for it in both directions; the real one reads
  # every tracked .go file in the repository.
  mkdir -p "$tree/scripts/format"
  printf '#!/usr/bin/env bash\necho "%s"\nexit 0\n' "$GOFMT_STUB" \
    >"$tree/scripts/format/check-gofmt.sh"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/format/selftest.sh"

  # The package-documentation baseline check and its own mutation
  # self-test (#526), for the tenth and eleventh time for the same reason.
  # The real check runs core/cmd/docguard over every package in the
  # repository and compares against scripts/docs/package-doc.baseline, and
  # the real self-test plants a promoted comment in core/service and puts
  # it back; this fixture has neither, and a gate step pointed at a path it
  # does not have exits 127 under `set -e` and takes Group D's control down
  # with it.
  mkdir -p "$tree/scripts/docs"
  for docs in check-package-doc selftest; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/docs/$docs.sh"
  done

  # Stubs for the release-script guard suites the gate runs, for the same
  # reason the four structure proofs above are stubbed: this fixture
  # measures which steps the gate chooses to run, not what those steps do,
  # and the real suites drive Docker-shaped scripts in throwaway git
  # repositories.
  #
  # Not optional. `bash` on a path that does not exist exits 127, the gate
  # runs under `set -e`, and the run dies at that step, so every full-tree
  # case below it (Group D's control included) fails for a reason that has
  # nothing to do with what it is measuring. That is what happened when
  # #174's guard suite was added to ci-local.sh without being added here:
  # 18 of this suite's 94 checks were failing on main before this line
  # existed, and the gate could not reach its own summary.
  for guard in record-release-hashes-guards publish-image-guards; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/tests/$guard.test.sh"
  done

  # The e2e drivers' --help pin (#514), stubbed for the same reason and for
  # the seventh time. It is one of the few steps up in the static section
  # rather than behind FAST, so without this stub every tree here would die
  # at it, before anything below had a chance to measure anything. The real
  # one renders the two drivers' help and diffs it against a golden; this
  # fixture carries stub drivers with no help block at all.
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/tests/e2e-help.test.sh"

  # The two-machine proof's exit-status pin (#551), stubbed for the eighth
  # time and for the same reason: it is another static step, so a tree
  # without it dies before Group D can measure anything. The real one
  # drives the real two-machine script with a stand-in `docker` and `git`
  # on PATH and proves the CLI's own exit 3 can never be mistaken for this
  # gate's INCOMPLETE.
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/tests/two-machine-exit-status.test.sh"

  # The release gate's own guards (#575, #1260), stubbed for the ninth,
  # tenth and eleventh times and for the same reason as everything above.
  # They are static steps, so a tree without them dies before Group D can
  # measure anything, and the failure reads as this suite being broken
  # rather than as a missing fixture. The real ones prove that a proof CI
  # could not perform comes out red under the word INCOMPLETE, that
  # release.yml refuses to publish a commit the local gate never recorded,
  # that the recorder refuses to write a record for anything short of a
  # full green run on a clean tree, that the latest tag only moves
  # forward, and that every module builds on its own. None of them has
  # anything to say about a synthetic tree with no .github in it.
  for guard in two-machine-ci-verdict release-refuses-an-ungated-publish local-gate-record release-moves-latest every-module-builds-on-its-own; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/tests/$guard.test.sh"
  done

  # Five more steps that landed in the gate without landing here, found
  # together by #1260: the first-run report (#1049), the tests pin (#1074),
  # the post-publish recorder, the published-provenance guard and its
  # mutation self-test (#895). Each is a `bash` on a path this fixture did
  # not have, each exits 127 under `set -e`, and between them every
  # full-tree case in this suite had been red on main for weeks while the
  # gate's own self-test step reported that. Same stubs, same reason as
  # every block above: this fixture measures which steps the gate runs,
  # not what they do.
  for guard in first-run-report pin-on-main published-provenance-guards record-published-release web-ui-ratchet; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/tests/$guard.test.sh"
  done
  mkdir -p "$tree/scripts/release"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tree/scripts/release/check-published-provenance.sh"

  # The installer's unit tests (#262), which the gate runs by `cd`-ing into
  # scripts/install. Same reason as every stub above, and the same failure
  # mode without it: a `cd` into a directory this fixture does not have
  # fails, the gate runs under `set -e`, and every full-tree case below the
  # step dies for a reason that has nothing to do with what it measures.
  # This is the third time that has happened here, and it is why the step
  # gets a stub rather than a conditional in ci-local.sh: a step that
  # quietly skips itself when its own file is missing is #160's silent skip
  # wearing a different hat.
  mkdir -p "$tree/scripts/install"
  # `-> None` on the stub, because scripts/install is inside the tree the
  # lint step now walks and mypy --strict rejects an unannotated def.
  printf 'import unittest\n\n\nclass Stub(unittest.TestCase):\n    def test_stub(self) -> None:\n        pass\n' \
    >"$tree/scripts/install/test_install_docker_host.py"

  # scripts/deploy's unit tests (#82/B4.1), wired into the gate by #672.
  # Stubbed for the identical reason as the block above, and it is the same
  # `cd` into a directory this fixture does not have.
  mkdir -p "$tree/scripts/deploy"
  printf 'import unittest\n\n\nclass Stub(unittest.TestCase):\n    def test_stub(self) -> None:\n        pass\n' \
    >"$tree/scripts/deploy/test_deploy_generic.py"

  # The browser e2e step (#158, #197). Same reason as every stub above, and
  # the same failure mode without it, which this suite has now been bitten
  # by twice: `bash` on a path that does not exist exits 127, the gate runs
  # under `set -e`, and every full-tree case below the step dies for a
  # reason that has nothing to do with what it measures. That is what
  # #174's guard suite did to 18 of these checks before its stub existed.
  #
  # The real script clones a pinned repository, builds a binary, and drives
  # a browser. This fixture measures which steps the gate chooses to run,
  # so the stub prints a marker and succeeds; Group G replaces it with a
  # failing one where it needs the other direction.
  mkdir -p "$tree/scripts/e2e"
  printf '#!/usr/bin/env bash\necho "%s"\nexit 0\n' "$E2E_STUB" \
    >"$tree/scripts/e2e/run-tests-repo-gate.sh"

  # The two-machine end-to-end backup proof (#356), stubbed for exactly the
  # reason the four comments above give, and this is the sixth time that
  # lesson has had to be written down here: `bash` on a path that does not
  # exist exits 127, the gate runs under `set -e`, and every full-tree case
  # below the step would die for a reason that has nothing to do with what
  # it measures. The real script builds an image from the working tree and
  # stands up two containers on a temporary network.
  printf '#!/usr/bin/env bash\necho "%s"\nexit 0\n' "$TWO_MACHINE_STUB" \
    >"$tree/scripts/e2e/two-machine-backup.sh"

  # The Python lint step (EPIC I, I1.6 / #672), and this is the SEVENTH time
  # the lesson above has had to be written down here. The step runs
  # `ruff check --config scripts/bdtools/pyproject.toml scripts` and
  # `mypy --strict --config-file ... scripts/bdtools scripts/deploy
  # scripts/install/embed_compose.py`; a synthetic tree has none of that and
  # no reason to have it, so a tool exits 2 on a path that is not there, the
  # gate runs under `set -e`, and every full-tree case below dies for a
  # reason that has nothing to do with what it measures. That is precisely
  # what happened when the step landed.
  #
  # THREE halves now, and the third is here because the second turned out
  # not to work. The TOOLS are stubbed on the tree's own PATH the same way
  # `docker` is -- whether a developer has ruff and mypy installed is a
  # property of the machine, and in the real tree the step ledgers when a
  # tool is missing, which is exactly what D1 asserts the absence of. But
  # ci-local.sh prepends /opt/homebrew/bin to PATH ahead of everything, so a
  # Homebrew-installed ruff SHADOWS the stub and runs for real. It ran for
  # real the whole time this step was `ruff check scripts/bdtools`, and
  # passed only because that path happened to hold one clean file with no
  # configuration to find.
  #
  # So the tree is made honest instead of made quiet: the real configuration
  # is copied in, the stub package and the two stub suites are written to be
  # clean under it, and the paths the step names all exist. Whichever ruff
  # wins the PATH race now measures the same thing.
  mkdir -p "$tree/scripts/bdtools"
  printf '"""A stub package, so the lint step has a directory to point at."""\n' \
    >"$tree/scripts/bdtools/__init__.py"
  cp "$SCRIPTS_DIR/bdtools/pyproject.toml" "$tree/scripts/bdtools/pyproject.toml"
  printf '"""A stub module, so the mypy half of the step has a file to point at."""\n' \
    >"$tree/scripts/install/embed_compose.py"
  printf '#!/bin/sh\nexit 0\n' >"$tree/bin/ruff"
  printf '#!/bin/sh\nexit 0\n' >"$tree/bin/mypy"
  chmod +x "$tree/bin/ruff" "$tree/bin/mypy"

  printf '%s\n' "$tree"
}

out=""
status=0

# run_hook drives .husky/pre-commit; run_gate drives scripts/ci-local.sh.
# Both share $out/$status, and both scrub every CI_LOCAL_* variable so the
# caller's own environment cannot decide what a case measures. Both also
# give the run a lock directory of its own (#1260): the real gate takes a
# lock under the shared git dir around its Docker-backed steps so parallel
# lanes queue, and a fixture that took the REAL lock would wait on whatever
# lane happens to be in its crash matrix.
scrub_env() {
  env -u CI_LOCAL_FAST -u CI_LOCAL_SKIP_JS -u CI_LOCAL_SKIP_DOCKER \
    -u CI_LOCAL_SELFTEST -u CI_LOCAL_SKIP_E2E -u CI_LOCAL_SKIP_TWO_MACHINE \
    -u CI_LOCAL_RED -u CI -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE \
    -u GIT_OBJECT_DIRECTORY -u GIT_COMMON_DIR -u GIT_PREFIX "$@"
}

run_hook() { # <tree> [VAR=VAL ...]
  hook_tree="$1"
  shift
  out="$(cd "$hook_tree" && scrub_env CI_LOCAL_LOCK_DIR="$hook_tree/.gate-lock" \
    PATH="$hook_tree/bin:$PATH" "$@" sh .husky/pre-commit 2>&1)"
  status=$?
}

run_gate() { # <tree> [VAR=VAL ...]
  gate_tree="$1"
  shift
  # A developer running this suite under CI_LOCAL_FAST=1 would otherwise
  # silently turn every fail-closed case into a pass, and the gate exports
  # CI_LOCAL_SELFTEST=1 around this very script, which would otherwise reach
  # every synthetic run as an inherited skip.
  out="$(cd "$gate_tree" && scrub_env CI_LOCAL_LOCK_DIR="$gate_tree/.gate-lock" \
    PATH="$gate_tree/bin:$PATH" "$@" bash scripts/ci-local.sh 2>&1)"
  status=$?
}

# run_git_commit drives a REAL `git commit` in a fixture, hooks and all,
# which is the only way to measure what prepare-commit-msg does under
# --no-verify: git skips pre-commit and commit-msg for that flag and runs
# prepare-commit-msg anyway, and no amount of invoking the hook by hand
# proves that git calls it.
run_git_commit() { # <tree> [VAR=VAL ...] -- <git commit args...>
  commit_tree="$1"
  shift
  # An array, not a string: a CI_LOCAL_RED reason has spaces in it, and a
  # string would split it into a command that is not there.
  commit_vars=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do
    commit_vars+=("$1")
    shift
  done
  [ "${1:-}" = "--" ] && shift
  out="$(cd "$commit_tree" && scrub_env CI_LOCAL_LOCK_DIR="$commit_tree/.gate-lock" \
    PATH="$commit_tree/bin:$PATH" "${commit_vars[@]}" git commit "$@" 2>&1)"
  status=$?
}

FAIL_BANNER='present in this tree but have no installed dependencies'
DOCKER_BANNER='The Docker daemon is not reachable'

# ------------------------------------- Group A: the ledger and the summary

echo "==> A. scripts/lib/ci-local-gate.sh"

if [ ! -f "$GATE_LIB" ]; then
  fail "A0 scripts/lib/ci-local-gate.sh exists"
else
  pass "A0 scripts/lib/ci-local-gate.sh exists"

  ws="$SANDBOX/states"
  mkdir -p "$ws"
  add_workspace "$ws" installed installed
  add_workspace "$ws" uninstalled uninstalled
  add_workspace "$ws" nolock uninstalled nolock
  mkdir -p "$ws/absent-dir"

  probe() { ( . "$GATE_LIB" && cd "$ws" && "$@" ); }

  assert_eq "A1 a workspace that is not in the tree reads as absent" \
    absent "$(probe gate_workspace_state not-here)"
  assert_eq "A2 a directory with no package.json reads as absent" \
    absent "$(probe gate_workspace_state absent-dir)"
  assert_eq "A3 package.json with no node_modules reads as uninstalled" \
    uninstalled "$(probe gate_workspace_state uninstalled)"
  assert_eq "A4 package.json with node_modules reads as installed" \
    installed "$(probe gate_workspace_state installed)"
  assert_eq "A5 the fix command for a locked workspace is npm ci" \
    "cd uninstalled && npm ci" "$(probe gate_install_hint uninstalled)"
  assert_eq "A6 the fix command without a lockfile is npm install" \
    "cd nolock && npm install" "$(probe gate_install_hint nolock)"

  # A7 is the positive control for A8: it proves the summary CAN print the
  # success line, so A8's absence assertion is not vacuous.
  clean_summary="$( . "$GATE_LIB" && gate_summary 2>&1 )"
  assert_contains "A7 an empty ledger still prints the success line" \
    'ci-local: ok' "$clean_summary"

  dirty_summary="$( . "$GATE_LIB" && gate_note_skip 'the conformance suite' && gate_summary 2>&1 )"
  assert_not_contains "A8 a non-empty ledger cannot print the success line" \
    'ci-local: ok' "$dirty_summary"
  assert_contains "A9 the summary names what did not run" \
    'the conformance suite' "$dirty_summary"
  assert_contains "A10 the summary says the run is not merge evidence" \
    'not merge evidence' "$dirty_summary"

  # A11/A12: the exit status has to say what the last line says. A wrapper
  # (.husky/pre-commit, any scripted merge) reads the status, never the
  # prose, and before this pair both outcomes returned 0.
  ( . "$GATE_LIB" && gate_summary >/dev/null 2>&1 )
  assert_eq "A11 an empty ledger exits 0" 0 "$?"

  ( . "$GATE_LIB" && gate_note_skip 'the conformance suite' && gate_summary >/dev/null 2>&1 )
  assert_eq "A12 a non-empty ledger exits $INCOMPLETE, not 0" "$INCOMPLETE" "$?"

  # A13-A17: the Docker probe. `docker info`, not `command -v docker`: the
  # binary being installed says nothing about the daemon being up, and it is
  # the daemon that decides whether the crash matrix runs or t.Skips.
  dockerbin="$SANDBOX/dockerbin"
  mkdir -p "$dockerbin"
  set_docker_probe() { # <available|unavailable>
    if [ "$1" = available ]; then printf '#!/bin/sh\nexit 0\n' >"$dockerbin/docker"
    else printf '#!/bin/sh\nexit 1\n' >"$dockerbin/docker"; fi
    chmod +x "$dockerbin/docker"
  }

  set_docker_probe available
  assert_eq "A13 a reachable daemon reads as available" \
    available "$( PATH="$dockerbin:$PATH" sh -c ". '$GATE_LIB' && gate_docker_state" )"
  set_docker_probe unavailable
  assert_eq "A14 an unreachable daemon reads as unavailable" \
    unavailable "$( PATH="$dockerbin:$PATH" sh -c ". '$GATE_LIB' && gate_docker_state" )"

  docker_refusal="$( PATH="$dockerbin:$PATH" sh -c ". '$GATE_LIB' && gate_require_docker" 2>&1 )"
  docker_refusal_status=$?
  assert_nonzero "A15 an unreachable daemon refuses the run" "$docker_refusal_status"
  assert_contains "A16 the refusal names the suites that would t.Skip" \
    'crash matrix' "$docker_refusal"

  docker_optout="$( PATH="$dockerbin:$PATH" CI_LOCAL_SKIP_DOCKER=1 \
    sh -c ". '$GATE_LIB' && gate_require_docker && gate_summary" 2>&1 )"
  assert_contains "A17 CI_LOCAL_SKIP_DOCKER=1 ledgers instead of refusing" \
    'ci-local: INCOMPLETE' "$docker_optout"
  assert_contains "A18 the ledgered Docker entry names the suites" \
    'crash matrix' "$docker_optout"

  # A19 is the positive control for A15-A18: with the daemon up, the same
  # call must be a silent success and leave the ledger empty, or A15-A18
  # would pass against a probe that always refuses.
  set_docker_probe available
  docker_ok="$( PATH="$dockerbin:$PATH" sh -c ". '$GATE_LIB' && gate_require_docker && gate_summary" 2>&1 )"
  assert_contains "A19 a reachable daemon leaves the ledger empty" \
    'ci-local: ok' "$docker_ok"
fi

# ------------------------------- Group B: the real script, real checkouts

echo "==> B. scripts/ci-local.sh preflight against synthetic checkouts"

# B1-B3: each skip site fails closed, independently. apps/common/tests gets
# its own tree with no other workspace in it, so it is proved on its own and
# not assumed to share a code path with ui/shared -- it is the site nobody
# noticed for three phases.
for site in ui/shared apps/common/tests apps/ugos/frontend/upk-proof; do
  tree="$(make_tree)"
  add_workspace "$tree" "$site" uninstalled
  run_gate "$tree"

  assert_nonzero "B[$site] the run fails" "$status"
  assert_not_contains "B[$site] the run does not report success" 'ci-local: ok' "$out"
  assert_contains "B[$site] the failure names the workspace" "$site" "$out"
  assert_contains "B[$site] the failure prints the exact fix command" \
    "cd $site && npm ci" "$out"
  assert_contains "B[$site] the failure explains itself" "$FAIL_BANNER" "$out"
  # The gate must refuse BEFORE it spends twenty minutes on the Go suites,
  # and this is also what tells a real preflight failure apart from the
  # synthetic tree simply having no core/ module to build.
  assert_not_contains "B[$site] nothing ran before the refusal" \
    'core/ go build' "$out"
done

# B4: the control for "absent is not uninstalled". A tree with no JS
# workspace at all must sail through the preflight; apps/ugos/frontend/upk-proof
# genuinely does not exist on main, and an optional component that is not in
# the tree is nothing to check, not a hole.
tree="$(make_tree)"
run_gate "$tree"
assert_not_contains "B4 an absent workspace is not a failure" "$FAIL_BANNER" "$out"
assert_contains "B4 an absent workspace is reported as absent" \
  'ui/shared not present in this tree' "$out"

# B5: the control for "the check keys on node_modules". Same tree shape as
# B1, one directory different, opposite verdict.
tree="$(make_tree)"
add_workspace "$tree" ui/shared installed
run_gate "$tree"
assert_not_contains "B5 an installed workspace is not a failure" "$FAIL_BANNER" "$out"
assert_contains "B5 an installed workspace is reported as installed" \
  'ui/shared installed' "$out"

# B6: skipping stays possible, but only when it is chosen out loud.
tree="$(make_tree)"
add_workspace "$tree" ui/shared uninstalled
run_gate "$tree" CI_LOCAL_SKIP_JS=1
assert_not_contains "B6 CI_LOCAL_SKIP_JS=1 opts out of the refusal" "$FAIL_BANNER" "$out"

# B7: CI_LOCAL_FAST=1 keeps behaving as the documented fast-iteration mode.
tree="$(make_tree)"
add_workspace "$tree" ui/shared uninstalled
run_gate "$tree" CI_LOCAL_FAST=1
assert_not_contains "B7 CI_LOCAL_FAST=1 still runs" "$FAIL_BANNER" "$out"

# --------------------------------- Group C: no second way to claim success

echo "==> C. the success line has exactly one source"

# Comments are exempt: the header explains the rule and has to be able to
# quote the string. Only a line that can actually print counts.
emits_success_line() { grep -v '^[[:space:]]*#' "$1" | grep -q 'ci-local: ok'; }

if emits_success_line "$SCRIPTS_DIR/ci-local.sh"; then
  fail "C1 ci-local.sh prints no success line of its own (it must go through gate_summary)"
else
  pass "C1 ci-local.sh prints no success line of its own"
fi

# C2 is C1's positive control: the same scan, run against a copy that has
# been given exactly the bad habit C1 forbids, must flag it.
mutant="$SANDBOX/mutant.sh"
cp "$SCRIPTS_DIR/ci-local.sh" "$mutant"
printf 'echo "==> ci-local: ok"\n' >>"$mutant"
if emits_success_line "$mutant"; then
  pass "C2 the C1 scan flags a script that does print one"
else
  fail "C2 the C1 scan flags a script that does print one -- the scan cannot fail, so C1 proves nothing"
fi

# ------------------ Group D: the real script, all the way to its last line

echo "==> D. scripts/ci-local.sh end to end, through gate_summary"

# D1 is the control for everything below it. Same fixture, nothing missing,
# daemon up: the run must reach the success line with status 0. Without this
# case, every INCOMPLETE assertion in D2-D8 would also pass against a gate
# that had lost the ability to say ok at all.
tree="$(make_full_tree)"
run_gate "$tree"
assert_contains "D1 a complete run reports success" 'ci-local: ok' "$out"
assert_eq "D1 a complete run exits 0" 0 "$status"
assert_not_contains "D1 a complete run is not INCOMPLETE" 'ci-local: INCOMPLETE' "$out"
assert_not_contains "D1 a complete run prints no failure marker" 'ci-local: FAILED' "$out"
# Proof the fixture really performed the run rather than short-circuiting
# somewhere convenient: the last three steps before the summary all show up.
assert_contains "D1 the conformance step ran" \
  'cross-provider conformance suite' "$out"
assert_contains "D1 the ui/shared vitest step ran" 'ui/shared tests' "$out"
assert_contains "D1 the nested self-test ran (and was the stub)" "$SELFTEST_STUB" "$out"

# D2: the ui/shared skip site, end to end. Mutation E1 and E2 both make this
# case say ok.
tree="$(make_full_tree)"
rm -rf "$tree/ui/shared/node_modules"
run_gate "$tree" CI_LOCAL_SKIP_JS=1
assert_not_contains "D2 a skipped ui/shared cannot report success" 'ci-local: ok' "$out"
assert_contains "D2 a skipped ui/shared ends INCOMPLETE" 'ci-local: INCOMPLETE' "$out"
assert_contains "D2 the summary names the ui/shared checks" \
  'ui/shared lint, typecheck:providers' "$out"
assert_contains "D2 the summary prints the fix command" 'cd ui/shared && npm ci' "$out"
assert_eq "D2 a skipped ui/shared exits $INCOMPLETE" "$INCOMPLETE" "$status"

# D3: the apps/common/tests skip site, end to end, on its own tree. This is
# the site that was silently skipped on every Phase 3 gate run.
tree="$(make_full_tree)"
rm -rf "$tree/apps/common/tests/node_modules"
run_gate "$tree" CI_LOCAL_SKIP_JS=1
assert_not_contains "D3 a skipped conformance suite cannot report success" \
  'ci-local: ok' "$out"
assert_contains "D3 the summary names the conformance suite" \
  'cross-provider conformance suite' "$out"
assert_eq "D3 a skipped conformance suite exits $INCOMPLETE" "$INCOMPLETE" "$status"

# D4: FAST is a documented, deliberate skip, and it still may not claim
# success. Everything in this tree is installed, so the only thing standing
# between this run and D1's ok is the FAST ledger entry.
tree="$(make_full_tree)"
run_gate "$tree" CI_LOCAL_FAST=1
assert_not_contains "D4 CI_LOCAL_FAST=1 cannot report success" 'ci-local: ok' "$out"
assert_contains "D4 CI_LOCAL_FAST=1 ends INCOMPLETE" 'ci-local: INCOMPLETE' "$out"
assert_contains "D4 the summary names FAST as the reason" 'CI_LOCAL_FAST=1' "$out"
assert_eq "D4 CI_LOCAL_FAST=1 exits $INCOMPLETE" "$INCOMPLETE" "$status"

# D5: a stopped Docker daemon is refused in the preflight. Without this the
# crash matrix, the SFTP integration suite and apps/generic/tests/dockercli
# all t.Skip, go test exits 0, and the run ends ok having tested none of it.
tree="$(make_full_tree)"
set_docker "$tree" unavailable
run_gate "$tree"
assert_nonzero "D5 a stopped daemon fails the run" "$status"
assert_not_contains "D5 a stopped daemon cannot report success" 'ci-local: ok' "$out"
assert_contains "D5 the refusal explains itself" "$DOCKER_BANNER" "$out"
assert_contains "D5 the refusal names the crash matrix" 'crash matrix' "$out"
assert_not_contains "D5 nothing ran before the refusal" 'core/ go build' "$out"

# D6: the out-loud opt-out proceeds and ledgers. The 'core/ go build'
# assertion is the positive control against D5: same tree, same stopped
# daemon, and this time the run really does continue.
tree="$(make_full_tree)"
set_docker "$tree" unavailable
run_gate "$tree" CI_LOCAL_SKIP_DOCKER=1
assert_contains "D6 CI_LOCAL_SKIP_DOCKER=1 proceeds past the preflight" \
  'core/ go build' "$out"
assert_not_contains "D6 CI_LOCAL_SKIP_DOCKER=1 cannot report success" \
  'ci-local: ok' "$out"
assert_contains "D6 the summary names the Docker suites" 'crash matrix' "$out"
assert_eq "D6 CI_LOCAL_SKIP_DOCKER=1 exits $INCOMPLETE" "$INCOMPLETE" "$status"

# D7: the recursion guard. ci-local.sh runs this suite, and this suite runs
# ci-local.sh, so a nested run must refuse to descend again. D1 is the
# positive control: it proves the stub self-test does run when the marker is
# not set, so this absence assertion is about the guard and not about a
# fixture that never had a self-test to run.
tree="$(make_full_tree)"
run_gate "$tree" CI_LOCAL_SELFTEST=1
assert_not_contains "D7 a nested run does not run the self-test again" \
  "$SELFTEST_STUB" "$out"
assert_contains "D7 a nested run says why it stopped" 'not recursing' "$out"
assert_contains "D7 a nested run ledgers the self-test it skipped" \
  'nested inside one' "$out"
assert_eq "D7 a nested run exits $INCOMPLETE" "$INCOMPLETE" "$status"

# D8: the third outcome. A run that actually fails used to print no
# "==> ci-local:" marker at all, so of three outcomes only two were
# greppable and the missing one was the failure. D1 is the control for the
# absence of this marker on a healthy run.
tree="$(make_full_tree)"
printf 'package stub\n\nthis is not go\n' >"$tree/core/stub.go"
run_gate "$tree"
assert_nonzero "D8 a broken step fails the run" "$status"
assert_contains "D8 a failed run prints a verdict line naming the step" \
  'ci-local: FAILED (core/ go build)' "$out"
assert_not_contains "D8 a failed run does not report success" 'ci-local: ok' "$out"

# D9: a vitest suite that is not there at all (#1260, item 7). The ui/shared
# and apps/common/tests dispatches had arms for installed and uninstalled
# and none for absent, so a tree without either workspace ran no vitest and
# still said ok. "Absent is not a hole" is true of an OPTIONAL component
# (apps/ugos/frontend/upk-proof, which is not in this tree today), and it is
# false of the product's own UI and its cross-provider conformance suite:
# the only thing that catches a stale CLI-twin fixture is the ui/shared
# vitest run (#1259), so a run that never started it is not merge
# evidence. D1 is the control: with both present the same run says ok.
tree="$(make_full_tree)"
rm -rf "$tree/ui/shared"
run_gate "$tree"
assert_not_contains "D9 a tree with no ui/shared cannot report success" 'ci-local: ok' "$out"
assert_contains "D9 a tree with no ui/shared ends INCOMPLETE" 'ci-local: INCOMPLETE' "$out"
assert_contains "D9 the summary names the vitest suite that did not run" 'ui/shared' "$out"
assert_eq "D9 a tree with no ui/shared exits $INCOMPLETE" "$INCOMPLETE" "$status"

tree="$(make_full_tree)"
rm -rf "$tree/apps/common/tests"
run_gate "$tree"
assert_not_contains "D10 a tree with no apps/common/tests cannot report success" 'ci-local: ok' "$out"
assert_contains "D10 the summary names the conformance suite that did not run" \
  'cross-provider conformance suite' "$out"
assert_eq "D10 a tree with no apps/common/tests exits $INCOMPLETE" "$INCOMPLETE" "$status"

# D11 is D9's control in the other direction: an optional component that
# is not here is still nothing to check, and the run still says ok.
tree="$(make_full_tree)"
rm -rf "$tree/apps/ugos"
run_gate "$tree"
assert_contains "D11 an absent optional component still leaves the run ok" 'ci-local: ok' "$out"

# ------------- Group E: the mutations that stayed green during code review

echo "==> E. Group D goes red when the ledger wiring is removed"

# count_matches <file> <extended regex>
count_matches() { grep -cE "$2" "$1" | tr -d '[:space:]'; }

# E1: the review's first mutation. Prefix every gate_note_skip call site in
# ci-local.sh with `:` so the ledger is never written. Nothing else changes:
# the arms are still there, still reached, still "handling" the skip.
tree="$(make_full_tree)"
script="$tree/scripts/ci-local.sh"
before="$(count_matches "$script" '^[[:space:]]*gate_note_skip ')"
sed -i.bak 's/^\([[:space:]]*\)gate_note_skip /\1: gate_note_skip /' "$script"
rm -f "$script.bak"
after="$(count_matches "$script" '^[[:space:]]*: gate_note_skip ')"
# Without this the mutation could match nothing and E1 would "pass" by
# testing the unmutated script.
assert_eq "E1 the mutation neutered every gate_note_skip call site" "$before" "$after"
rm -rf "$tree/ui/shared/node_modules"
run_gate "$tree" CI_LOCAL_SKIP_JS=1
assert_contains "E1 without the ledger the same run reports success, so D2 has teeth" \
  'ci-local: ok' "$out"

# E2: the review's second mutation. Leave every arm and every
# gate_note_skip intact and change only the case subject, so each frontend
# and conformance block vanishes with no ledger entry. A static scan for
# "every uninstalled) arm calls gate_note_skip" does not catch this one,
# which is why Group D exists in the end-to-end form. The subject is a
# value NO arm matches: the review's original mutation used "absent", and
# since #1260 the ui/shared and apps/common/tests dispatches ledger that
# one (D9, D10), so it no longer makes the blocks vanish.
tree="$(make_full_tree)"
script="$tree/scripts/ci-local.sh"
before="$(count_matches "$script" 'case "\$\(gate_workspace_state ')"
sed -i.bak 's/case "\$(gate_workspace_state [^)]*)" in/case "nothing-matches-this" in/' "$script"
rm -f "$script.bak"
after="$(count_matches "$script" '^[[:space:]]*case "nothing-matches-this" in')"
assert_eq "E2 the mutation rewrote every workspace-state dispatch" "$before" "$after"
rm -rf "$tree/ui/shared/node_modules"
run_gate "$tree" CI_LOCAL_SKIP_JS=1
assert_contains "E2 without the dispatch the same run reports success, so D2 has teeth" \
  'ci-local: ok' "$out"

# E3: the summary call itself. Delete it and the run ends with no verdict
# line at all, which is what D1's success assertion is really guarding.
tree="$(make_full_tree)"
script="$tree/scripts/ci-local.sh"
before="$(count_matches "$script" '^gate_summary$')"
assert_eq "E3 ci-local.sh calls gate_summary exactly once, unindented" 1 "$before"
sed -i.bak 's/^gate_summary$/: no summary/' "$script"
rm -f "$script.bak"
run_gate "$tree"
assert_not_contains "E3 without gate_summary the run says nothing, so D1 has teeth" \
  'ci-local: ok' "$out"

# --------------------------- Group F: the one caller that reads the status

echo "==> F. .husky/pre-commit acts on the exit status, not the prose"

# The hook is the reason the exit status matters at all: before this it was
# `set -e; bash scripts/ci-local.sh`, and since an INCOMPLETE run returned 0
# the hook could not tell a full run from one that skipped everything. Three
# outcomes, three behaviours, and F1 is the control for the other two.
tree="$(make_full_tree)"
run_hook "$tree"
assert_eq "F1 the hook passes a complete run" 0 "$status"
assert_contains "F1 the hook ran the gate" 'ci-local: ok' "$out"
assert_not_contains "F1 a complete run gets no INCOMPLETE warning" \
  'allowing this commit on an INCOMPLETE gate run' "$out"

tree="$(make_full_tree)"
run_hook "$tree" CI_LOCAL_FAST=1
assert_eq "F2 the hook still allows an INCOMPLETE run" 0 "$status"
assert_contains "F2 the hook says out loud that it allowed one" \
  'allowing this commit on an INCOMPLETE gate run' "$out"

tree="$(make_full_tree)"
printf 'package stub\n\nthis is not go\n' >"$tree/core/stub.go"
run_hook "$tree"
assert_nonzero "F3 the hook blocks a run that actually failed" "$status"
assert_not_contains "F3 a real failure is not treated as INCOMPLETE" \
  'allowing this commit on an INCOMPLETE gate run' "$out"

# ------------------------- Group G: the browser e2e step is a real signal

echo "==> G. the browser e2e step (#158, #197)"

# G1 is the control for everything below it. The step is not conditional on
# anything but FAST, so a complete run must actually invoke it. Without this
# case, G3's absence assertion would also pass against a gate that had lost
# the step entirely.
tree="$(make_full_tree)"
run_gate "$tree"
assert_contains "G1 a complete run invokes the browser e2e step" \
  'browser e2e + CLI smoke' "$out"
assert_contains "G1 the step really ran (and was the stub)" "$E2E_STUB" "$out"
assert_contains "G1 a complete run still reports success" 'ci-local: ok' "$out"

# G2: the out-loud opt-out ledgers rather than printing a note and carrying
# on. This is #197's first open question answered: a missing browser is
# openable, but only the way a stopped Docker daemon is, and the run that
# opens it says so and cannot be merge evidence.
tree="$(make_full_tree)"
run_gate "$tree" CI_LOCAL_SKIP_E2E=1
assert_not_contains "G2 CI_LOCAL_SKIP_E2E=1 does not run the step" "$E2E_STUB" "$out"
assert_not_contains "G2 CI_LOCAL_SKIP_E2E=1 cannot report success" 'ci-local: ok' "$out"
assert_contains "G2 CI_LOCAL_SKIP_E2E=1 ends INCOMPLETE" 'ci-local: INCOMPLETE' "$out"
assert_contains "G2 the summary names the browser suite" \
  'browser e2e suite and the CLI smoke slice' "$out"
assert_eq "G2 CI_LOCAL_SKIP_E2E=1 exits $INCOMPLETE" "$INCOMPLETE" "$status"

# G3: FAST leaves it out, which the never-merge-on-FAST rule already covers.
# D4 owns the INCOMPLETE half; this owns the "it did not run" half.
tree="$(make_full_tree)"
run_gate "$tree" CI_LOCAL_FAST=1
assert_not_contains "G3 CI_LOCAL_FAST=1 does not run the browser e2e step" "$E2E_STUB" "$out"

# G4: the whole point. A red suite has to refuse the commit, not annotate
# it. The stub is replaced by a failing one, which is the closest a
# synthetic tree can get to a red spec without cloning a repository and
# driving a browser; the real thing is demonstrated in the pull request by
# breaking a spec in a worktree and watching this step go red.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\necho "a spec failed"\nexit 1\n' \
  >"$tree/scripts/e2e/run-tests-repo-gate.sh"
run_gate "$tree"
assert_nonzero "G4 a red browser suite fails the run" "$status"
assert_not_contains "G4 a red browser suite cannot report success" 'ci-local: ok' "$out"
assert_contains "G4 the verdict line names the step that failed" \
  'ci-local: FAILED (browser e2e + CLI smoke' "$out"

# G7: the third outcome (#1260). The browser suite is a lane judged by its
# report, and a lane that produced no report (the rig could not run, or
# the runner crashed) exits 3. That has to be LEDGERED, the way the
# two-machine proof's 3 is: neither ok nor FAILED, and the summary names
# the lane. G4 is the control for the failure half, G1 for the ok half.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\necho "==> e2e gate: CANNOT RUN. the browser lane left no report" >&2\nexit 3\n' \
  >"$tree/scripts/e2e/run-tests-repo-gate.sh"
run_gate "$tree"
assert_not_contains "G7 a lane that could not be judged cannot report success" 'ci-local: ok' "$out"
assert_contains "G7 it ends INCOMPLETE rather than FAILED" 'ci-local: INCOMPLETE' "$out"
assert_contains "G7 the summary names the lane" 'browser suite lane' "$out"
assert_eq "G7 it exits $INCOMPLETE" "$INCOMPLETE" "$status"

# G8: G7's control, the same shape as I6: exit 3 and exit 1 from the
# e2e gate are told apart, or a real red suite would be ledgered and
# committed.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\nexit 1\n' >"$tree/scripts/e2e/run-tests-repo-gate.sh"
run_gate "$tree"
assert_nonzero "G8 a failed lane still fails the run" "$status"
assert_not_contains "G8 a failed lane is not ledgered as a skip" 'browser suite lane' "$out"

# G5: the pin is a full sha, checked statically. A branch name or a short
# sha there would let the tests under this gate change without the pin
# changing, which is the one thing the pin exists to stop. Checked against
# the REAL pin file, not a fixture, because the fixture would only prove the
# fixture.
pin="$REPO_ROOT/scripts/e2e/tests-repo.pin"
if [ ! -f "$pin" ]; then
  fail "G5 scripts/e2e/tests-repo.pin exists"
elif grep -qE '^TESTS_REPO_SHA=[0-9a-f]{40}$' "$pin"; then
  pass "G5 the tests-repo pin is a full 40-character sha"
else
  fail "G5 the tests-repo pin is a full 40-character sha"
fi

# G6 is G5's positive control: the same scan against a copy carrying exactly
# the bad habit G5 forbids must flag it.
mutant_pin="$SANDBOX/tests-repo.pin"
printf 'TESTS_REPO_URL=https://example.invalid/x.git\nTESTS_REPO_SHA=main\n' >"$mutant_pin"
if grep -qE '^TESTS_REPO_SHA=[0-9a-f]{40}$' "$mutant_pin"; then
  fail "G6 the G5 scan flags a branch-name pin -- the scan cannot fail, so G5 proves nothing"
else
  pass "G6 the G5 scan flags a branch-name pin"
fi

# H1: the installer prerequisite-refusal step (#262) piped its `unittest`
# run through `tail -3` for output tidiness, and this file has no
# `set -o pipefail` (nor could it portably rely on one, being
# #!/usr/bin/env sh): a pipeline's exit status under plain `set -e` is its
# LAST command's, which is `tail` here, and `tail` always succeeds. That
# let the installer's own 55-case test suite fail silently forever -- the
# exact "a refusal nobody has watched work is not a refusal" failure this
# step exists to close, one line away from closing it, caught by review
# before it shipped. This proves the fix: a red installer suite has to
# refuse the commit, not get truncated and ignored, the same shape as G4's
# proof for the browser e2e step.
tree="$(make_full_tree)"
printf 'import unittest\n\n\nclass Stub(unittest.TestCase):\n    def test_stub(self):\n        self.fail("a real installer regression")\n' \
  >"$tree/scripts/install/test_install_docker_host.py"
run_gate "$tree"
assert_nonzero "H1 a red installer suite fails the run" "$status"
assert_not_contains "H1 a red installer suite cannot report success" 'ci-local: ok' "$out"
assert_contains "H1 the verdict line names the step that failed" \
  'ci-local: FAILED (installer prerequisite refusals' "$out"

# ------------- Group I: the two-machine backup proof is a real signal

echo "==> I. the two-machine end-to-end backup proof (#356)"

# I1 is the control for everything below it, and the same control G1 is:
# without it, I3's absence assertion would also pass against a gate that
# had lost the step entirely.
tree="$(make_full_tree)"
run_gate "$tree"
assert_contains "I1 a complete run invokes the two-machine step" \
  'two throwaway machines' "$out"
assert_contains "I1 the step really ran (and was the stub)" "$TWO_MACHINE_STUB" "$out"
assert_contains "I1 a complete run still reports success" 'ci-local: ok' "$out"

# I2: the out-loud opt-out ledgers rather than printing a note and carrying
# on, the same shape as CI_LOCAL_SKIP_DOCKER and CI_LOCAL_SKIP_E2E.
tree="$(make_full_tree)"
run_gate "$tree" CI_LOCAL_SKIP_TWO_MACHINE=1
assert_not_contains "I2 CI_LOCAL_SKIP_TWO_MACHINE=1 does not run the step" "$TWO_MACHINE_STUB" "$out"
assert_not_contains "I2 CI_LOCAL_SKIP_TWO_MACHINE=1 cannot report success" 'ci-local: ok' "$out"
assert_contains "I2 CI_LOCAL_SKIP_TWO_MACHINE=1 ends INCOMPLETE" 'ci-local: INCOMPLETE' "$out"
assert_contains "I2 the summary names the proof that did not run" \
  'two-machine end-to-end backup proof' "$out"
assert_eq "I2 CI_LOCAL_SKIP_TWO_MACHINE=1 exits $INCOMPLETE" "$INCOMPLETE" "$status"

# I3: FAST leaves it out, under the never-merge-on-FAST rule D4 owns.
tree="$(make_full_tree)"
run_gate "$tree" CI_LOCAL_FAST=1
assert_not_contains "I3 CI_LOCAL_FAST=1 does not run the two-machine step" "$TWO_MACHINE_STUB" "$out"

# I4: a failed proof refuses the commit rather than annotating it.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\necho "the digests did not match"\nexit 1\n' \
  >"$tree/scripts/e2e/two-machine-backup.sh"
run_gate "$tree"
assert_nonzero "I4 a failed backup proof fails the run" "$status"
assert_not_contains "I4 a failed backup proof cannot report success" 'ci-local: ok' "$out"
assert_contains "I4 the verdict line names the step that failed" \
  'ci-local: FAILED (two throwaway machines' "$out"

# I5 is the case this step has that no other step here has, and the one
# worth the most. "This machine cannot perform the proof" is neither a pass
# nor a failure: no Docker, or a daemon that refuses a privileged container
# so docker-in-docker cannot start. The script says so and exits 3. The
# gate has to LEDGER that, which means neither reporting ok nor dying on
# it, and the distinction is invisible in the exit status alone, which is
# why it is asserted in all three directions.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\necho "==> two-machine: CANNOT RUN. no privileged containers here" >&2\nexit 3\n' \
  >"$tree/scripts/e2e/two-machine-backup.sh"
run_gate "$tree"
assert_not_contains "I5 a proof this machine cannot perform cannot report success" 'ci-local: ok' "$out"
assert_contains "I5 it ends INCOMPLETE rather than FAILED" 'ci-local: INCOMPLETE' "$out"
assert_contains "I5 the summary names what could not be performed" \
  'this machine could not perform it' "$out"
assert_eq "I5 it exits $INCOMPLETE" "$INCOMPLETE" "$status"

# I6 is I5's positive control. Without it, I5 would also pass against a
# gate that treated EVERY non-zero status as a ledgered skip, which would
# turn a real digest mismatch into an INCOMPLETE nobody reads. I4 asserts
# the failure half; this asserts that the two statuses are actually told
# apart rather than collapsed.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\nexit 3\n' >"$tree/scripts/e2e/two-machine-backup.sh"
run_gate "$tree"
three_status="$status"
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\nexit 1\n' >"$tree/scripts/e2e/two-machine-backup.sh"
run_gate "$tree"
if [ "$three_status" = "$INCOMPLETE" ] && [ "$status" != "$INCOMPLETE" ]; then
  pass "I6 exit 3 and exit 1 from the proof are told apart"
else
  fail "I6 exit 3 and exit 1 from the proof are told apart, got $three_status and $status"
fi

# I7: every container the proof creates is removed WITH its volumes.
#
# Not a stub test, because the property is in the real script rather than
# in the gate's handling of it. docker:28-dind declares
# VOLUME /var/lib/docker, so each manager machine gets an anonymous host
# volume carrying that machine's whole inner Docker state, and
# `docker rm -f` leaves it behind. Nothing references a leftover, so
# nothing complains, and the shared Docker disk fills silently until an
# install refuses on free space. That is not hypothetical: it is how a
# full run failed, with the installer's own preflight reporting 912 MiB
# free against its 2048 MiB floor on a Docker VM at 98%.
#
# Asserted on the source rather than by running a case, because running
# one costs a container image build and this has to hold for a removal
# site somebody adds later, not only for the two that exist today.
#
# PORTED-CHECK HAZARD NOTE. EPIC I / I1.6 (#672) moved the proof to
# scripts/bdtools/e2e/two_machine_backup.py and left an exec shim at the old
# path, and this check went to the shim. Both halves broke, in the two
# opposite ways a port breaks a check:
#
#   "the proof says why -v is load-bearing" went RED, loudly, because the
#   shim carries none of the explanation. Visible, and fixed by repointing.
#
#   "every docker rm removes the container's volumes too" went VACUOUSLY
#   GREEN, silently, because a shim contains no `docker rm` at all and a
#   grep that matches nothing has nothing to complain about. It would have
#   gone on ticking forever over a file that cannot violate it.
#
# So the scan is repointed AND given a floor: the proof must contain at
# least two `docker rm` sites (teardown and release_case), which is what
# stops the absence of a finding from being mistaken for a clean result.
proof_script="$(dirname "$0")/../bdtools/e2e/two_machine_backup.py"
if [ ! -f "$proof_script" ]; then
  fail "I7 the two-machine proof is where this expects it" "no file at $proof_script"
else
  # Comment lines are excluded, and the prose above the teardown quotes
  # `docker rm -f` on purpose to explain why it is wrong; a check that
  # cannot tell a command from its own explanation is not a check.
  code_only="$(grep -vE '^[[:space:]]*#' "$proof_script")"
  removal_sites="$(printf '%s\n' "$code_only" | grep -cE '"docker", "rm"' || true)"
  if [ "$removal_sites" -ge 2 ]; then
    pass "I7 the proof still has removal sites to check ($removal_sites), so the scan below is not vacuous"
  else
    fail "I7 the proof still has removal sites to check, found $removal_sites" \
      "A scan that matches nothing cannot fail. Either the removals moved, or this check has stopped watching."
  fi
  bare_removals="$(printf '%s\n' "$code_only" | grep -nE '"docker", "rm"' | grep -v -- '"-fv"' || true)"
  if [ -z "$bare_removals" ]; then
    pass "I7 every docker rm in the proof removes the container's volumes too"
  else
    fail "I7 every docker rm in the proof removes the container's volumes too, but these do not:
$bare_removals"
  fi
  assert_contains "I7 the proof says why -v is load-bearing" \
    'VOLUME /var/lib/docker' "$(cat "$proof_script")"
fi

# ------------- Group J: a daemon that dies in the middle of a run (#457)

echo "==> J. the Docker daemon during the run, not only at the preflight"

# Docker Desktop's Resource Saver stops the hypervisor after five idle
# minutes and cold-starts it on the next API call. This gate has several
# Docker-free stretches longer than that, so every run was restarting the VM
# somewhere in the middle, and two runs on 2026-09-04 died of it: one VM came
# up and died 86ms later racing the previous hypervisor over Docker.raw, and
# one `images/create` returned HTTP 500 for 2m31s.
#
# Neither showed up as a failure. The gate probed the daemon once, at the
# top, so a VM that died at minute 18 turned every Docker-backed suite into a
# t.Skip, `go test` exited 0, and the run reported on tests that never ran.
# That is #160's defect again, arriving through the machine rather than
# through the checkout.
#
# This stub is that shape exactly: a daemon that answers the preflight probe
# and then stops answering. It also records every call, which is how the two
# sentinel cases below watch a container they cannot create for real.
set_docker_recording() { # <tree>
  cat >"$1/bin/docker" <<'STUB'
#!/bin/sh
# Records every invocation, and stops answering `docker info` after
# DOCKER_STUB_MAX_INFO of them. Everything else always succeeds, so the
# number of non-info calls the gate makes cannot change what a case measures.
if [ -n "${DOCKER_STUB_LOG:-}" ]; then printf '%s\n' "$*" >>"$DOCKER_STUB_LOG"; fi
if [ "${1:-}" = info ]; then
  count_file="${DOCKER_STUB_LOG:-/dev/null}.info"
  n=$(( $(cat "$count_file" 2>/dev/null || echo 0) + 1 ))
  echo "$n" >"$count_file" 2>/dev/null || true
  if [ "$n" -gt "${DOCKER_STUB_MAX_INFO:-99}" ]; then exit 1; fi
fi
exit 0
STUB
  chmod +x "$1/bin/docker"
}

# J1: the headline. One `docker info` answered, every one after it refused,
# and the run must FAIL by name at the step that needed the daemon.
tree="$(make_full_tree)"
set_docker_recording "$tree"
run_gate "$tree" DOCKER_STUB_LOG="$tree/docker.log" DOCKER_STUB_MAX_INFO=1
assert_nonzero "J1 a daemon that dies mid-run fails the run" "$status"
assert_not_contains "J1 a daemon that dies mid-run cannot report success" \
  'ci-local: ok' "$out"
assert_not_contains "J1 a daemon that dies mid-run is not a ledgered skip" \
  'ci-local: INCOMPLETE' "$out"
assert_contains "J1 the failure names the step that needed the daemon" \
  'ci-local: FAILED (core/ go test -race ./...' "$out"
assert_contains "J1 the failure says the daemon died during the run" \
  'it was at the start of this run' "$out"
assert_contains "J1 the failure points at Resource Saver" 'Resource Saver' "$out"
# The positive control for "this is a mid-run failure and not the preflight
# refusal wearing a different hat": the run really did get past the preflight
# and do work first.
assert_contains "J1 the run got past the preflight before it failed" \
  'core/ go build' "$out"

# J2: J1's control. Same tree, same stub, same everything except that the
# daemon keeps answering. Without this, J1 would also pass against a gate
# that had simply lost the ability to finish at all.
tree="$(make_full_tree)"
set_docker_recording "$tree"
run_gate "$tree" DOCKER_STUB_LOG="$tree/docker.log" DOCKER_STUB_MAX_INFO=99
assert_contains "J2 a daemon that stays up reports success" 'ci-local: ok' "$out"
assert_eq "J2 a daemon that stays up exits 0" 0 "$status"

# J3: the sentinel. Resource Saver measures IDLE, so the fix that does not
# depend on anyone's GUI settings is to never be idle. J2's run is reused
# through its recorded call log: one `docker run` of a sleeping container,
# and one removal of that same container on the way out.
docker_log="$(cat "$tree/docker.log" 2>/dev/null || true)"
assert_contains "J3 the gate starts a sentinel container" \
  'run -d --rm --name ci-local-sentinel-' "$docker_log"
assert_contains "J3 the sentinel just sleeps" 'sleep infinity' "$docker_log"
assert_contains "J3 the sentinel is labelled" \
  '--label retnd-ci-local-sentinel=1' "$docker_log"
assert_contains "J3 the gate removes the sentinel on the way out" \
  'rm -f ci-local-sentinel-' "$docker_log"
# Same container, not just some container of each shape: a start and a
# removal that name different containers is the leak this case is about.
sentinel_started="$(printf '%s\n' "$docker_log" | sed -n 's/.*--name \(ci-local-sentinel-[0-9]*\).*/\1/p' | head -1)"
sentinel_removed="$(printf '%s\n' "$docker_log" | sed -n 's/^rm -f \(ci-local-sentinel-[0-9]*\)$/\1/p' | head -1)"
if [ -n "$sentinel_started" ] && [ "$sentinel_started" = "$sentinel_removed" ]; then
  pass "J3 the container that was started is the container that was removed"
else
  fail "J3 the container that was started is the container that was removed" \
    "started [$sentinel_started], removed [$sentinel_removed]"
fi
# Nothing after the removal, so the sentinel cannot outlive the run.
assert_eq "J3 the removal is the last thing the gate asks docker for" \
  "rm -f $sentinel_started" "$(printf '%s\n' "$docker_log" | tail -1)"

# J4: the removal is not conditional on the run going well. A sentinel that
# survives a failed run is #150's leak wearing a different label, and a
# failed run is the common case for a gate that runs on every commit.
tree="$(make_full_tree)"
set_docker_recording "$tree"
printf 'package stub\n\nthis is not go\n' >"$tree/core/stub.go"
run_gate "$tree" DOCKER_STUB_LOG="$tree/docker.log" DOCKER_STUB_MAX_INFO=99
assert_nonzero "J4 the broken tree fails the run" "$status"
docker_log="$(cat "$tree/docker.log" 2>/dev/null || true)"
assert_contains "J4 a failed run still starts its sentinel" \
  'run -d --rm --name ci-local-sentinel-' "$docker_log"
assert_contains "J4 a failed run still removes its sentinel" \
  'rm -f ci-local-sentinel-' "$docker_log"

# J5: the sentinel's label must NOT be the one core/tests/dockerlease
# sweeps. That sweep removes labelled containers older than fifteen minutes,
# and a full gate run is twenty-five, so sharing the label would delete the
# sentinel out from under the run it exists to protect, at almost exactly the
# halfway point. Read out of the Go source rather than copied here, because
# the failure this guards against is the two constants drifting together.
lease_source="$REPO_ROOT/core/tests/dockerlease/dockerlease.go"
if [ ! -f "$lease_source" ]; then
  fail "J5 core/tests/dockerlease/dockerlease.go is where this expects it"
else
  lease_key="$(sed -n 's/^[[:space:]]*LabelKey = "\(.*\)"$/\1/p' "$lease_source" | head -1)"
  sentinel_key="$(sh -c ". '$GATE_LIB' && printf '%s' \"\$GATE_SENTINEL_LABEL_KEY\"")"
  if [ -n "$lease_key" ] && [ -n "$sentinel_key" ] && [ "$lease_key" != "$sentinel_key" ]; then
    pass "J5 the sentinel does not carry the label dockerlease sweeps"
  else
    fail "J5 the sentinel does not carry the label dockerlease sweeps" \
      "dockerlease LabelKey [$lease_key], sentinel [$sentinel_key]"
  fi
fi

# J6: an interrupt is the other way a sentinel outlives its run, and it is
# not a rare path on a gate that takes twenty-five minutes from a pre-commit
# hook. Driven against the library directly, because run_gate has no way to
# send a signal into the middle of a run.
int_log="$SANDBOX/interrupt-docker.log"
cat >"$SANDBOX/interrupt-docker" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>"$DOCKER_STUB_LOG"
exit 0
STUB
chmod +x "$SANDBOX/interrupt-docker"
mkdir -p "$SANDBOX/intbin"
cp "$SANDBOX/interrupt-docker" "$SANDBOX/intbin/docker"
cat >"$SANDBOX/interrupted-run.sh" <<'RUN'
set -e
. "$1"
gate_install_traps
gate_start_docker_sentinel >/dev/null
gate_step "core/ tests/crashmatrix under gotestwatch" >/dev/null
kill -INT $$
echo "REACHED-THE-LINE-AFTER-THE-SIGNAL"
RUN
int_out="$(DOCKER_STUB_LOG="$int_log" PATH="$SANDBOX/intbin:$PATH" \
  sh "$SANDBOX/interrupted-run.sh" "$GATE_LIB" 2>&1)"
int_status=$?
assert_eq "J6 an interrupted run exits 130" 130 "$int_status"
assert_not_contains "J6 an interrupted run stops where it was interrupted" \
  'REACHED-THE-LINE-AFTER-THE-SIGNAL' "$int_out"
assert_contains "J6 an interrupted run still prints a verdict naming the step" \
  'ci-local: FAILED (core/ tests/crashmatrix under gotestwatch, interrupted by SIGINT)' "$int_out"
assert_contains "J6 an interrupted run removes its sentinel" \
  'rm -f ci-local-sentinel-' "$(cat "$int_log" 2>/dev/null || true)"

# J7: CI_LOCAL=1 reaches the processes the gate starts. The Docker fixtures
# key on it to tell "this laptop has no Docker", which is an honest skip
# outside the gate, from "the daemon this gate already used has gone away",
# which is a failure. Measured through a child process rather than in this
# shell, because being exported is the whole point.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\necho "CI-LOCAL-ENV=[${CI_LOCAL:-unset}]"\nexit 0\n' \
  >"$tree/scripts/perf/selftest.sh"
run_gate "$tree"
assert_contains "J7 the gate exports CI_LOCAL=1 to the steps it runs" \
  'CI-LOCAL-ENV=[1]' "$out"

# J8: the mutation-anchor step (#458). The file is written under separate
# work, so the gate guards on its existence; these two cases are what stops
# that guard from being a permanent silent skip. A tree that has the script
# must run it, and a tree whose script fails must fail the run. The label is
# asserted, not just the exit status: this step goes red when a mutation
# anchor in the compat or conformance selftest has drifted off the code it
# names, and the person reading the verdict line has to know that is what
# broke.
tree="$(make_full_tree)"
mkdir -p "$tree/scripts/selftest"
printf '#!/usr/bin/env bash\necho "ANCHORS-STUB-RAN"\nexit 0\n' \
  >"$tree/scripts/selftest/check-anchors.sh"
run_gate "$tree"
assert_contains "J8 a tree with an anchors script runs it" 'ANCHORS-STUB-RAN' "$out"
assert_contains "J8 the anchors step is announced, and says mutation anchors" \
  'mutation anchors in the compat, conformance and race selftests' "$out"
assert_eq "J8 a passing anchors script leaves the run green" 0 "$status"

tree="$(make_full_tree)"
mkdir -p "$tree/scripts/selftest"
printf '#!/usr/bin/env bash\necho "ANCHORS-STUB-FAILED"\nexit 1\n' \
  >"$tree/scripts/selftest/check-anchors.sh"
run_gate "$tree"
assert_nonzero "J8 a failing anchors script fails the run" "$status"
assert_contains "J8 the failure names the anchors step" \
  'ci-local: FAILED (mutation anchors in the compat, conformance and race selftests' "$out"
# And it fails EARLY: the point of putting a one-second check near the top is
# that nobody waits twenty-five minutes to hear about a broken link.
assert_not_contains "J8 a failing anchors script fails before the Go suites" \
  'core/ go test' "$out"

# J9: the Resource Saver reading, both answers and both ways of having no
# answer. It is a warning and never a refusal, so every one of these paths
# has to return 0: a gate that refused over a Docker Desktop preference
# would be unrunnable on the machine that has it on, which is this one.
rs_dir="$SANDBOX/resource-saver"
mkdir -p "$rs_dir"
printf '{"UseResourceSaver": true, "Other": 1}\n' >"$rs_dir/on.json"
printf '{"UseResourceSaver":false}\n' >"$rs_dir/off.json"
printf '{"SomethingElse":true}\n' >"$rs_dir/nokey.json"
for rs_case in on off nokey missing; do
  case "$rs_case" in
    on) want=on ;;
    off) want=off ;;
    *) want=unknown ;;
  esac
  got="$(GATE_DOCKER_SETTINGS_FILE="$rs_dir/$rs_case.json" \
    sh -c ". '$GATE_LIB' && gate_resource_saver_state")"
  assert_eq "J9 Resource Saver reads $rs_case as $want" "$want" "$got"
  warn_out="$(GATE_DOCKER_SETTINGS_FILE="$rs_dir/$rs_case.json" \
    sh -c ". '$GATE_LIB' && gate_warn_resource_saver" 2>&1)"
  assert_eq "J9 the $rs_case warning never fails the run" 0 $?
  if [ "$rs_case" = on ]; then
    assert_contains "J9 the warning names the setting" 'Resource Saver' "$warn_out"
    assert_contains "J9 the warning says where it lives" \
      'Docker Desktop > Settings > Resources > Advanced' "$warn_out"
  else
    assert_eq "J9 $rs_case prints no warning" "" "$warn_out"
  fi
done

# J10: CI=1 reaches the processes the gate starts (#1260, item 7). vitest
# reads CI to pick its non-interactive reporter, Playwright reads it to
# refuse a `.only` and to stop retrying, and more than one Go test in this
# tree keys a refusal on it; the gate set none of it, so a comment claiming
# "a failure under CI" described a run nobody ever made. Measured through
# a child process rather than in this shell, like J7, because being
# exported is the whole point, and scrubbed from the caller's environment
# by run_gate so a developer's own CI=1 cannot make this pass.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\necho "CI-ENV=[${CI:-unset}]"\nexit 0\n' \
  >"$tree/scripts/perf/selftest.sh"
run_gate "$tree"
assert_contains "J10 the gate exports CI=1 to the steps it runs" \
  'CI-ENV=[1]' "$out"

# ------------- Group K: every Go suite in the gate runs under -race (#417)

echo "==> K. the race detector is on, and on everything"

# The gate ran no -race anywhere until #417. That is the same shape as every
# other hole this suite exists for: `go test` exits 0 whether the detector
# looked or not, so "this tree has no data race" and "nobody asked" were the
# same output. Turning it on found two real things in one package on the
# first run, one of them a genuine data race in a dependency.
#
# It is a flag on the steps that already exist rather than a step of its
# own, which is the decision these cases pin down. A separate step can be
# commented out and the suites still run and still report ok; a flag cannot
# be removed without the step going with it. So the assertion is not "there
# is a race step" but "no `go test` in this gate runs without the detector",
# which is a rule a new module cannot be added around by accident.

# race_flag_problems <path to a ci-local.sh> -> one line per invocation
# that runs a Go suite without the detector and without saying why.
#
# It reads command lines only: `GOWORK=off go test` is how every suite in
# this gate is actually invoked, and cmd/gotestwatch appears in two other
# shapes of its own. Step headings and comments are prose about those
# commands and are skipped, which matters because at least one heading says
# "go test -timeout" while describing the flag it does NOT pass.
#
# The gotestwatch rule matches any `go run` or `go test` that names that
# package, which is both shapes: `go run ./cmd/gotestwatch` is the
# progress-bounded wrapper the Docker-backed suites run under, and
# `go test -race -c ./cmd/gotestwatch` is how the gate builds that
# package's OWN test binary, since it reads a three-outcome status
# `go test` would flatten (#533). The line that runs the built binary
# carries no flag and cannot: the detector is decided when it is compiled,
# which is the line this catches.
#
# A trailing `# no -race: <reason>` on the command line itself is the one
# way out, and it is not a loophole because it is counted: K4 below asserts
# how many lines carry it and which. An exclusion nobody can enumerate is
# how a gate ends up not running the thing it says it runs.
race_flag_problems() {
  awk '
    /^[[:space:]]*#/ { next }
    /GOWORK=off go test/ && !/GOWORK=off go test -race/ {
      if ($0 ~ /# no -race: [^ ]/) { next }
      printf "  a Go suite runs without -race and without a reason, line %d: %s\n", NR, $0
    }
    /(go run|go test)[^#]*cmd\/gotestwatch/ && !/-race/ {
      printf "  the gotestwatch suites run without -race, line %d: %s\n", NR, $0
    }
  ' "$1"
}

# race_flag_exceptions <path> -> one line per deliberate exclusion.
race_flag_exceptions() {
  awk '
    /^[[:space:]]*#/ { next }
    /GOWORK=off go test/ && !/GOWORK=off go test -race/ && /# no -race: [^ ]/ {
      printf "%d: %s\n", NR, $0
    }
  ' "$1"
}

# race_flag_invocations <path> -> how many Go suites the script runs at all.
# The count is the control for the scan: a script this scan finds nothing
# wrong with because it found nothing at all would otherwise read as a pass.
race_flag_invocations() {
  grep -cE '^[^#]*(GOWORK=off go test|cmd/gotestwatch)' "$1" | tr -d '[:space:]'
}

real_gate="$SCRIPTS_DIR/ci-local.sh"

# K1: the real script, as it stands. Every Go suite, detector on.
k1_problems="$(race_flag_problems "$real_gate")"
if [ -z "$k1_problems" ]; then
  pass "K1 every Go suite in scripts/ci-local.sh runs under -race"
else
  fail "K1 every Go suite in scripts/ci-local.sh runs under -race" "$k1_problems"
fi

# K1's control: the scan has something to find. Nine Go suites today (the
# FAST core step, the full core step, gotestwatch, apps/common,
# distribution, distribution/packaging, apps/generic, apps/synology and
# apps/ugos/backend), and the floor is deliberately low so adding or
# removing a module does not fail this, while an empty scan does.
k1_count="$(race_flag_invocations "$real_gate")"
if [ "$k1_count" -ge 5 ]; then
  pass "K1 the scan actually found the gate's Go suites ($k1_count of them)"
else
  fail "K1 the scan actually found the gate's Go suites" \
    "found $k1_count invocations of \`go test\` or gotestwatch in $real_gate, want at least 5; the scan is looking for the wrong shape"
fi

# K2: the mutation. Strip the flag out of a copy and the scan has to say so,
# by name, or K1 is a check that cannot fail.
tree="$(make_full_tree)"
script="$tree/scripts/ci-local.sh"
sed -i.bak 's/go test -race/go test/g; s/gotestwatch -race/gotestwatch/g' "$script"
rm -f "$script.bak"
k2_problems="$(race_flag_problems "$script")"
assert_contains "K2 a gate with the flag stripped out is caught" \
  'a Go suite runs without -race' "$k2_problems"
assert_contains "K2 the gotestwatch suites are caught too" \
  'the gotestwatch suites run without -race' "$k2_problems"

# K3: the other direction, end to end. A static scan of the file says the
# flag is written down; this says a run actually announces it, which is what
# stops the whole thing from passing against a step nobody reaches.
tree="$(make_full_tree)"
run_gate "$tree"
assert_eq "K3 a full run with -race everywhere still reaches ok" 0 "$status"
assert_contains "K3 the core step announces the detector" \
  'core/ go test -race ./...' "$out"
assert_contains "K3 the gotestwatch step announces the detector" \
  'under gotestwatch, -race' "$out"
assert_contains "K3 the other Go modules announce it too" \
  'apps/common go build, vet, test -race' "$out"

# K4: the exclusions, enumerated. One suite is deliberately out
# (distribution/packaging: no goroutine anywhere in it, so nothing to
# detect, and it is the most CPU-bound package here). That is a decision
# somebody made with a number in hand, and this is what keeps the next one
# from being made by accident: the count is pinned, so a second exclusion
# fails here until it is added on purpose.
k4_exceptions="$(race_flag_exceptions "$real_gate")"
k4_count="$(printf '%s' "$k4_exceptions" | grep -c . | tr -d '[:space:]')"
assert_eq "K4 exactly one Go suite in the gate is excluded from -race" 1 "$k4_count"
assert_contains "K4 the exclusion is distribution/packaging" './packaging/' "$k4_exceptions"
assert_contains "K4 the exclusion says why on the line itself" \
  'no goroutine of its own' "$k4_exceptions"

# K4's control: the enumeration notices a new one. Without this, K4's count
# assertion would also pass against a scan that can no longer see any
# exclusion at all.
tree="$(make_full_tree)"
script="$tree/scripts/ci-local.sh"
sed -i.bak 's|^(cd apps/common && \(.*\)go test -race \./\.\.\.)$|(cd apps/common \&\& \1go test ./...) # no -race: a second exclusion nobody decided on|' "$script"
rm -f "$script.bak"
k4_mutant="$(race_flag_exceptions "$script")"
k4_mutant_count="$(printf '%s' "$k4_mutant" | grep -c . | tr -d '[:space:]')"
if [ "$k4_mutant_count" -eq 2 ]; then
  pass "K4 a second exclusion is counted, so the count assertion has teeth"
else
  fail "K4 a second exclusion is counted, so the count assertion has teeth" \
    "the mutation should have produced 2 exclusions, the scan found $k4_mutant_count:
$k4_mutant"
fi

# ------------- Group L: formatting is checked at all (#417)

echo "==> L. the formatting gate, in both halves"

# Formatting was not checked anywhere until #417, and the way that surfaced
# is the reason this group exists. Two Go files in this repository were not
# gofmt-clean, one of them since it was written, and every gate step stayed
# green: `go build`, `go vet` and every linter .golangci.yml enabled are all
# indifferent to layout. A check that was never there and a check that is
# there and looking produce the same output, which is #160's defect arriving
# through a third door.
#
# It is closed in two places, and neither makes the other redundant.
# .golangci.yml enables the gofmt formatter, which covers the five Go
# modules, and scripts/format/check-gofmt.sh sweeps every tracked .go file,
# which is the only thing that reaches the two Go files living outside every
# module and outside go.work. L1 and L2 are the sweep as a gate step; L3 is
# the config, with the mutation that proves the assertion can fail.

# L1: the sweep runs, and a run that has it stays green. The marker is the
# control against L2 passing because the step was never reached at all.
tree="$(make_full_tree)"
run_gate "$tree"
assert_contains "L1 the gate runs the formatting sweep" "$GOFMT_STUB" "$out"
assert_contains "L1 the step says what it covers" \
  'every tracked Go file is gofmt-clean' "$out"
assert_eq "L1 a passing sweep leaves the run green" 0 "$status"

# L2: a tree with unformatted Go in it fails the run, by name, and early.
# Early matters for the same reason it did for the anchors check: this is a
# half-second sweep, and nobody should wait out the Docker-backed suites to
# be told about whitespace.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\necho "not gofmt-clean" >&2\nexit 1\n' \
  >"$tree/scripts/format/check-gofmt.sh"
run_gate "$tree"
assert_nonzero "L2 an unformatted tree fails the run" "$status"
assert_not_contains "L2 an unformatted tree cannot report success" 'ci-local: ok' "$out"
assert_contains "L2 the verdict names the formatting step" \
  'ci-local: FAILED (every tracked Go file is gofmt-clean' "$out"
assert_not_contains "L2 the formatting sweep fails before the Go suites" \
  'core/ go test' "$out"

# L4: the other half of the same blind spot. Being outside every module
# does not only cost those two files their formatting: this gate vets and
# lints per module too, so nothing had ever vetted or linted them either.
# The check that does now lives in scripts/architecture/ but runs HERE,
# near the top, and that placement is the thing this cell pins: the other
# architecture checks run after the Go suites, and a Go file nobody checks
# is worth hearing about before twenty minutes of Docker-backed tests.
tree="$(make_full_tree)"
run_gate "$tree"
assert_contains "L4 the gate checks the Go files no module owns" "$UNOWNED_STUB" "$out"
assert_contains "L4 the step says what it covers" \
  'every Go file no module owns still passes go vet and golangci-lint' "$out"
assert_eq "L4 a passing check leaves the run green" 0 "$status"

tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\necho "go vet found something" >&2\nexit 1\n' \
  >"$tree/scripts/architecture/check-unowned-go.sh"
run_gate "$tree"
assert_nonzero "L4 an unvetted Go file fails the run" "$status"
assert_contains "L4 the verdict names the step" \
  'ci-local: FAILED (every Go file no module owns' "$out"
assert_not_contains "L4 it fails before the Go suites" 'core/ go test' "$out"

# L3: the other half. golangci-lint checks formatting only if the config
# asks it to, and the config is a file anybody can trim.
#
# The reading is structural rather than a grep for the word, because
# .golangci.yml mentions gofmt several times in the prose explaining why it
# is enabled, and a scan that matched those would pass against a config that
# had lost the setting and kept the paragraph.
gofmt_formatter_enabled() { # <path to a .golangci.yml>
  python3 "$SCRIPTS_DIR/tests/gofmt-formatter-enabled.py" "$1"
}

if ! python3 -c 'import yaml' 2>/dev/null; then
  fail "L3 .golangci.yml enables the gofmt formatter, and python3 has no yaml module to read it structurally"
else
  if gofmt_formatter_enabled "$REPO_ROOT/.golangci.yml"; then
    pass "L3 .golangci.yml enables the gofmt formatter"
  else
    fail "L3 .golangci.yml enables the gofmt formatter" \
      "formatters.enable does not list gofmt, so golangci-lint checks no formatting in any of the five Go modules"
  fi

  # L3's mutation: drop the setting, keep every word of the prose around it,
  # and the scan has to notice. Without this, L3 would also pass against a
  # scan that can no longer tell the difference.
  cp "$REPO_ROOT/.golangci.yml" "$SANDBOX/golangci-no-formatters.yml"
  perl -0pi -e 's/formatters:\n  enable:\n    - gofmt\n//' "$SANDBOX/golangci-no-formatters.yml"
  if gofmt_formatter_enabled "$SANDBOX/golangci-no-formatters.yml"; then
    fail "L3 the scan notices a config with the formatter removed" \
      "the mutated config still reads as enabling gofmt, so L3 cannot fail"
  else
    pass "L3 the scan notices a config with the formatter removed, so L3 has teeth"
  fi
fi

# ------------- Group M: the watchdog's own INCOMPLETE reaches the ledger

echo "==> M. gotestwatch's own three-outcome status (#533)"

# core/cmd/gotestwatch keeps a ledger of its own, for this suite's reason
# one level down: its live proof that a bursty host is killed without being
# told why skips itself when the host stalls it harder than the 800ms its
# fixture plants, which is exactly the loaded machine the issue is about.
# Its TestMain exits 3 when that ledger is not empty.
#
# `go test` flattens any non-zero status from a test binary into its own 1
# and prints FAIL, so that run used to arrive here as a failure and read
# as a broken watchdog. The gate compiles that package's test binary and
# runs it instead, which keeps the 3, and these two cases are the wiring:
# a lost control is ledgered, and a real failure still fails.
tree="$(make_full_tree)"
cat >"$tree/core/cmd/gotestwatch/main_test.go" <<'GOSTUB'
package main

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	fmt.Fprintln(os.Stderr, "==> gotestwatch self-test: INCOMPLETE (exit 3). a control could not be measured")
	if code != 0 {
		os.Exit(code)
	}
	os.Exit(3)
}

func TestStub(t *testing.T) {}
GOSTUB
run_gate "$tree"
assert_eq "M1 a lost watchdog control ends the run INCOMPLETE" "$INCOMPLETE" "$status"
assert_contains "M1 the summary names the controls that did not run"   "gotestwatch's own controls" "$out"
assert_not_contains "M1 a lost control cannot report success" 'ci-local: ok' "$out"

# M2: the control for M1. If that route read every non-zero status as a
# skip, a genuinely broken watchdog would be ledgered and committed.
tree="$(make_full_tree)"
cat >"$tree/core/cmd/gotestwatch/main_test.go" <<'GOSTUB'
package main

import "testing"

func TestStub(t *testing.T) { t.Fatal("a real failure in the watchdog's own suite") }
GOSTUB
run_gate "$tree"
assert_eq "M2 a real failure in that package still fails the run" 1 "$status"
assert_not_contains "M2 a real failure is not ledgered as a skip"   "gotestwatch's own controls" "$out"
assert_contains "M2 the verdict names the step that failed" 'cmd/gotestwatch' "$out"

# ------------- Group N: the hook has to be installed, and says so (#1260)

echo "==> N. the hook is installed, or the run says it is not"

# The hook was not installed anywhere when #1260 was opened: core.hooksPath
# was unset in the main checkout and in every worktree, nothing in the
# tree set it, and the gate the repository says it relies on had not been
# running on commits at all. A hook cannot detect its own absence, so the
# two things that can say so are the gate, when somebody runs it by hand,
# and the release recorder, which refuses outright. This group is the
# gate's half: a warning (never a refusal, so a run by hand still works),
# naming the cause and then the fix.
HOOK_WARNING='pre-commit hook is not installed'

# N1: the control. A checkout with the hook installed gets no warning.
tree="$(make_full_tree)"
run_gate "$tree"
assert_not_contains "N1 an installed hook draws no warning" "$HOOK_WARNING" "$out"
assert_eq "N1 and the run is ok" 0 "$status"

# N2: the same checkout with core.hooksPath unset.
tree="$(make_full_tree)"
(cd "$tree" && git config --unset core.hooksPath)
run_gate "$tree"
assert_contains "N2 a missing hook is warned about" "$HOOK_WARNING" "$out"
assert_contains "N2 the warning names the setting" 'core.hooksPath' "$out"
assert_contains "N2 the warning names the fix" 'scripts/install-hooks.sh' "$out"
assert_contains "N2 the warning says why it matters" 'not gated' "$out"
assert_eq "N2 a missing hook is a warning, not a refusal: the run still ends ok" 0 "$status"
assert_contains "N2 and the run really did finish" 'ci-local: ok' "$out"

# N3: a hooksPath that names somewhere else is a missing hook too.
tree="$(make_full_tree)"
(cd "$tree" && git config core.hooksPath /somewhere/else)
run_gate "$tree"
assert_contains "N3 a hooksPath pointing elsewhere is warned about" "$HOOK_WARNING" "$out"

# N4: the installer. One command, the shared config, every worktree.
installer="$SCRIPTS_DIR/install-hooks.sh"
if [ ! -f "$installer" ]; then
  fail "N4 scripts/install-hooks.sh exists"
else
  pass "N4 scripts/install-hooks.sh exists"
  tree="$(make_full_tree)"
  (cd "$tree" && git config --unset core.hooksPath && git add -A >/dev/null 2>&1 && git -c core.hooksPath=/dev/null commit -qm base)
  install_out="$(cd "$tree" && scrub_env bash "$installer" 2>&1)"
  install_status=$?
  assert_eq "N4 the installer exits 0" 0 "$install_status"
  assert_eq "N4 the installer sets core.hooksPath to .husky" \
    '.husky' "$(cd "$tree" && git config --get core.hooksPath)"
  assert_contains "N4 the installer says what it set" 'core.hooksPath' "$install_out"
  assert_contains "N4 and that it covers every worktree" 'worktree' "$install_out"
  install_again="$(cd "$tree" && scrub_env bash "$installer" 2>&1)"
  assert_eq "N4 the installer is idempotent" 0 $?
  # The relative path is read from the SHARED config and resolves against
  # each worktree's own root, which is what makes one install cover every
  # lane: a worktree added afterwards sees it with no install of its own.
  (cd "$tree" && git worktree add -q "$tree/.lane" -b lane >/dev/null 2>&1)
  assert_eq "N4 a worktree added afterwards sees the same setting" \
    '.husky' "$(cd "$tree/.lane" && git config --get core.hooksPath)"
  lane_out="$(cd "$tree/.lane" && scrub_env CI_LOCAL_LOCK_DIR="$tree/.gate-lock" \
    PATH="$tree/bin:$PATH" bash scripts/ci-local.sh 2>&1)"
  assert_not_contains "N4 and the gate in that worktree draws no warning" "$HOOK_WARNING" "$lane_out"
  (cd "$tree" && git worktree remove --force "$tree/.lane" >/dev/null 2>&1)

  # N5: the installer refuses a tree whose hook is not there to install.
  tree="$(make_full_tree)"
  rm -f "$tree/.husky/pre-commit"
  refuse_out="$(cd "$tree" && scrub_env bash "$installer" 2>&1)"
  assert_nonzero "N5 the installer refuses a tree with no .husky/pre-commit" $?
  assert_contains "N5 and names what is missing" '.husky/pre-commit' "$refuse_out"
fi

# ------------- Group O: red-first commits, out loud (#1260, item 5)

echo "==> O. CI_LOCAL_RED commits the red tests and nothing else"

# Tests go in red, ahead of their fix, and the hook blocks a failing
# commit. The documented way through used to be --no-verify, which leaves
# nothing in history and skips every check, not only the one that was
# meant to be red. CI_LOCAL_RED="<reason>" is the replacement: the hook
# skips the gate, and only when every staged file is a test file, never on
# main or release, and only with a reason long enough to mean something
# (twelve characters, the same rule as Release-Gate-Override). The reason
# is stamped on the commit by prepare-commit-msg (Group P).
RED_REASON='#1260: red tests for the recorder'

# red_tree -> a complete checkout, as a repository with one base commit and
# a staged test file, which is the honest red-first shape.
red_tree() {
  tree="$(make_full_tree)"
  (cd "$tree" && git add -A >/dev/null 2>&1 && git -c core.hooksPath=/dev/null commit -qm base) >/dev/null 2>&1
  printf 'package stub\n\nimport "testing"\n\nfunc TestRed(t *testing.T) { t.Fatal("red first") }\n' \
    >"$tree/core/red_test.go"
  (cd "$tree" && git add core/red_test.go)
  printf '%s\n' "$tree"
}

# O1: the headline. Only test files staged, a feature branch, a real
# reason: the commit is allowed and the gate did not run.
tree="$(red_tree)"
run_hook "$tree" CI_LOCAL_RED="$RED_REASON"
assert_eq "O1 a red commit of test files on a feature branch is allowed" 0 "$status"
assert_contains "O1 the hook says it is a red commit" 'red' "$out"
assert_contains "O1 and repeats the reason" "$RED_REASON" "$out"
assert_not_contains "O1 the gate did not run" 'core/ go build' "$out"
assert_not_contains "O1 and nothing claimed ok" 'ci-local: ok' "$out"
marker="$(cd "$tree" && git rev-parse --path-format=absolute --git-path local-gate-marker)"
if [ -f "$marker" ]; then
  pass "O1 the hook left a marker for prepare-commit-msg"
  assert_contains "O1 the marker says red" 'red' "$(cat "$marker")"
  assert_contains "O1 the marker names the tree being committed" \
    "$(cd "$tree" && git write-tree)" "$(cat "$marker")"
else
  fail "O1 the hook left a marker for prepare-commit-msg" "no file at $marker"
fi

# O2: a product file in the staging area. That is a fix riding in with the
# red tests, and the gate exists to run on fixes.
tree="$(red_tree)"
printf 'package stub\n\nfunc Fixed() int { return 2 }\n' >"$tree/core/fix.go"
(cd "$tree" && git add core/fix.go)
run_hook "$tree" CI_LOCAL_RED="$RED_REASON"
assert_nonzero "O2 a red commit with a non-test file staged is refused" "$status"
assert_contains "O2 the refusal names the file" 'core/fix.go' "$out"
assert_not_contains "O2 and does not name the test file as the problem" 'core/red_test.go' "$out"
assert_not_contains "O2 the gate did not run either" 'core/ go build' "$out"

# O3: main and release. A red commit is a branch-only thing.
for protected in main release; do
  tree="$(red_tree)"
  (cd "$tree" && git checkout -q -b "$protected")
  run_hook "$tree" CI_LOCAL_RED="$RED_REASON"
  assert_nonzero "O3 a red commit on $protected is refused" "$status"
  assert_contains "O3 the refusal names the branch" "$protected" "$out"
done

# O4: the reason is the audit trail, so a reason that says nothing is no
# reason. Same floor as Release-Gate-Override.
tree="$(red_tree)"
run_hook "$tree" CI_LOCAL_RED="wip"
assert_nonzero "O4 a red commit with a short reason is refused" "$status"
assert_contains "O4 the refusal names the floor" '12' "$out"

# O5: nothing staged is not a red commit of anything.
tree="$(red_tree)"
(cd "$tree" && git reset -q core/red_test.go)
run_hook "$tree" CI_LOCAL_RED="$RED_REASON"
assert_nonzero "O5 a red commit with nothing staged is refused" "$status"

# O6: every test-file shape the rule accepts, each on its own, because the
# rule is a list and a list is what rots. Shell, Go, TypeScript, Python
# and a fixture directory.
for red_path in scripts/tests/new-guard.test.sh core/thing_test.go ui/shared/src/x/y.test.ts \
                ui/shared/src/x/y.spec.tsx scripts/bdtools/tests/new_guard.py scripts/deploy/test_thing.py \
                core/internal/x/testdata/fixture.json; do
  tree="$(red_tree)"
  (cd "$tree" && git reset -q core/red_test.go && mkdir -p "$(dirname "$red_path")" \
    && printf 'red\n' >"$red_path" && git add "$red_path")
  run_hook "$tree" CI_LOCAL_RED="$RED_REASON"
  assert_eq "O6 $red_path counts as a test file" 0 "$status"
done

# O7: the control for O6, from the other side: shapes that look like
# tests and are not.
for not_red in core/testing.go core/test_helpers.go scripts/tests.sh ui/shared/src/test.ts; do
  tree="$(red_tree)"
  (cd "$tree" && git reset -q core/red_test.go && mkdir -p "$(dirname "$not_red")" \
    && printf 'not a test\n' >"$not_red" && git add "$not_red")
  run_hook "$tree" CI_LOCAL_RED="$RED_REASON"
  assert_nonzero "O7 $not_red does not count as a test file" "$status"
done

# O8: without the variable the hook is exactly what it was (F1 to F3 own
# the three outcomes); a staged test file changes nothing about that.
tree="$(red_tree)"
run_hook "$tree"
assert_contains "O8 without CI_LOCAL_RED the gate runs as before" 'core/ go build' "$out"

# ------------- Group P: what the commit message says afterwards (#1260, item 4)

echo "==> P. prepare-commit-msg stamps what the hook did not check"

# git runs prepare-commit-msg under --no-verify (it skips pre-commit and
# commit-msg only), so it is the one hook a bypass cannot bypass. It reads
# the marker pre-commit left for this tree and stamps the message when the
# marker is not there: `Local-Gate: unverified` for a bypass,
# `Local-Gate: red <reason>` for a red commit. A commit the gate checked
# gets no trailer at all, on purpose: a rebase runs no hooks and keeps
# every trailer, so an `ok` would outlive the tree it described.
#
# Driven through a REAL `git commit` in each fixture, because the claim is
# about what git does with the hook, not about what the hook does when
# somebody runs it by hand.
stamped_tree() {
  tree="$(make_full_tree)"
  (cd "$tree" && git add -A >/dev/null 2>&1 && git -c core.hooksPath=/dev/null commit -qm base) >/dev/null 2>&1
  printf 'package stub\n\nimport "testing"\n\nfunc TestRed(t *testing.T) { t.Fatal("red first") }\n' \
    >"$tree/core/red_test.go"
  (cd "$tree" && git add core/red_test.go)
  printf '%s\n' "$tree"
}
last_message() { (cd "$1" && git log -1 --format=%B); }

# P1: the bypass shows up in history.
tree="$(stamped_tree)"
run_git_commit "$tree" -- --no-verify -qm "Bypass the gate"
assert_eq "P1 a --no-verify commit still commits" 0 "$status"
assert_contains "P1 a --no-verify commit is stamped unverified" 'Local-Gate: unverified' "$(last_message "$tree")"
assert_not_contains "P1 the gate did not run under --no-verify" 'core/ go build' "$out"

# P2: the red commit carries its reason.
tree="$(stamped_tree)"
run_git_commit "$tree" CI_LOCAL_RED="$RED_REASON" -- -qm "Add red tests for the recorder"
assert_eq "P2 a red commit commits" 0 "$status"
assert_contains "P2 a red commit is stamped red, with the reason" "Local-Gate: red $RED_REASON" "$(last_message "$tree")"
assert_not_contains "P2 and not unverified" 'Local-Gate: unverified' "$(last_message "$tree")"

# P3: a commit the gate checked carries nothing.
tree="$(stamped_tree)"
printf 'package stub\n\nimport "testing"\n\nfunc TestGreen(t *testing.T) {}\n' >"$tree/core/red_test.go"
(cd "$tree" && git add core/red_test.go)
run_git_commit "$tree" -- -qm "A commit the gate ran on"
assert_eq "P3 a gated commit commits" 0 "$status"
assert_contains "P3 the gate ran" 'ci-local: ok' "$out"
assert_not_contains "P3 a gated commit carries no Local-Gate trailer" 'Local-Gate:' "$(last_message "$tree")"

# P4: --no-verify with the red variable set is still a bypass, because the
# hook that checks the red rule never ran.
tree="$(stamped_tree)"
printf 'package stub\n\nfunc Fixed() int { return 2 }\n' >"$tree/core/fix.go"
(cd "$tree" && git add core/fix.go)
run_git_commit "$tree" CI_LOCAL_RED="$RED_REASON" -- --no-verify -qm "Bypass with the variable set"
assert_contains "P4 --no-verify with CI_LOCAL_RED is stamped unverified" 'Local-Gate: unverified' "$(last_message "$tree")"
assert_not_contains "P4 and not red" 'Local-Gate: red' "$(last_message "$tree")"

# P5: the stamp is a trailer, after the body, where git tooling reads it.
tree="$(stamped_tree)"
run_git_commit "$tree" -- --no-verify -qm "Subject line" -m "A body paragraph."
trailers="$(cd "$tree" && git log -1 --format=%B | git interpret-trailers --parse)"
assert_contains "P5 the stamp parses as a trailer" 'Local-Gate: unverified' "$trailers"
assert_contains "P5 the body survives above it" 'A body paragraph.' "$(last_message "$tree")"

# P6: an amend re-stamps rather than stacking. The second message must
# carry one Local-Gate line, not two.
tree="$(stamped_tree)"
run_git_commit "$tree" -- --no-verify -qm "First"
run_git_commit "$tree" -- --no-verify -q --amend --no-edit
stamp_count="$(last_message "$tree" | grep -c '^Local-Gate:' | tr -d ' ')"
assert_eq "P6 an amended bypass carries exactly one stamp" 1 "$stamp_count"

# ------------- Group Q: parallel lanes queue on the Docker-backed steps (#1260, item 6)

echo "==> Q. the gate lock"

# About thirty worktrees share one Docker daemon on this machine, and the
# full gate runs -race suites plus the crash matrix in every one of them.
# Several at once is the load #256 and #533 exist for, and it produces red
# runs that mean nothing. So the Docker-backed steps run under a lock in
# the shared git dir: a second run waits, says who it is waiting for, and
# goes when the first lets go. A lock left by a run that was killed is
# reclaimed, by the same kill -0 rule the sentinel sweep uses.
lock_dir="$SANDBOX/lock"

# Q1: acquire and release, driven against the library.
lock_out="$(CI_LOCAL_LOCK_DIR="$lock_dir" sh -c ". '$GATE_LIB' && gate_lock_acquire && test -d '$lock_dir' && test -s '$lock_dir/pid' && echo HELD && gate_lock_release && test ! -e '$lock_dir' && echo RELEASED" 2>&1)"
assert_contains "Q1 acquire creates the lock with the holder's pid in it" 'HELD' "$lock_out"
assert_contains "Q1 release removes it" 'RELEASED' "$lock_out"

# Q2: a second run waits, and goes when the first is done.
rm -rf "$lock_dir"
(CI_LOCAL_LOCK_DIR="$lock_dir" sh -c ". '$GATE_LIB' && gate_lock_acquire && sleep 3 && gate_lock_release" >/dev/null 2>&1) &
first_pid=$!
sleep 1
q2_start="$(date +%s)"
q2_out="$(CI_LOCAL_LOCK_DIR="$lock_dir" GATE_LOCK_POLL=1 sh -c ". '$GATE_LIB' && gate_lock_acquire && echo SECOND-GOT-LOCK && gate_lock_release" 2>&1)"
q2_elapsed=$(( $(date +%s) - q2_start ))
wait "$first_pid" 2>/dev/null
assert_contains "Q2 the second run gets the lock in the end" 'SECOND-GOT-LOCK' "$q2_out"
assert_contains "Q2 and says it waited" 'waiting' "$q2_out"
if [ "$q2_elapsed" -ge 1 ]; then
  pass "Q2 and it really did wait (${q2_elapsed}s)"
else
  fail "Q2 and it really did wait" "the second acquire returned in ${q2_elapsed}s while the first still held the lock"
fi

# Q3: a lock left by a dead run is reclaimed, not waited on forever.
rm -rf "$lock_dir"
mkdir -p "$lock_dir"
# A pid no process on this machine has: pid_max is far below this on both
# macOS and Linux.
printf '2147483646\n' >"$lock_dir/pid"
printf '%s\n' "$SANDBOX/gone" >"$lock_dir/where"
q3_out="$(CI_LOCAL_LOCK_DIR="$lock_dir" GATE_LOCK_POLL=1 sh -c ". '$GATE_LIB' && gate_lock_acquire && echo RECLAIMED-AND-HELD && gate_lock_release" 2>&1)"
assert_contains "Q3 a lock whose holder is gone is reclaimed" 'RECLAIMED-AND-HELD' "$q3_out"
assert_contains "Q3 and the run says so" 'no longer exists' "$q3_out"

# Q4: end to end. A full run takes the lock around its Docker-backed
# steps, releases it between them, and leaves nothing behind, whether it
# ends ok or fails.
tree="$(make_full_tree)"
run_gate "$tree"
assert_contains "Q4 a full run takes the lock" 'gate lock' "$out"
assert_eq "Q4 a full run still ends ok" 0 "$status"
if [ -e "$tree/.gate-lock" ]; then
  fail "Q4 a finished run leaves no lock behind" "$(ls "$tree/.gate-lock")"
else
  pass "Q4 a finished run leaves no lock behind"
fi
tree="$(make_full_tree)"
printf 'package stub\n\nthis is not go\n' >"$tree/core/stub.go"
run_gate "$tree"
assert_nonzero "Q4 the broken tree fails" "$status"
if [ -e "$tree/.gate-lock" ]; then
  fail "Q4 a failed run leaves no lock behind" "$(ls "$tree/.gate-lock")"
else
  pass "Q4 a failed run leaves no lock behind"
fi

# Q5: the lock lives under the shared git dir by default, so every
# worktree of one repository shares it, and a fixture only avoids that
# because run_gate overrides the path.
tree="$(make_full_tree)"
want_lock="$(cd "$tree" && git rev-parse --path-format=absolute --git-common-dir)/ci-local.lock"
got_lock="$(cd "$tree" && scrub_env sh -c ". '$GATE_LIB' && gate_lock_path")"
assert_eq "Q5 the default lock path is under the shared git dir" "$want_lock" "$got_lock"

# Q6: the lock is held while a Docker-backed step runs and not while a
# plain one does, measured through the steps themselves: a stub that
# reports whether the lock directory exists at the moment it runs.
tree="$(make_full_tree)"
printf '#!/usr/bin/env bash\nif [ -d "%s" ]; then echo "CONTRAST-STEP: lock held"; else echo "CONTRAST-STEP: lock free"; fi\nexit 0\n' "$tree/.gate-lock" \
  >"$tree/scripts/contrast/run-browser-check.sh"
printf '#!/usr/bin/env bash\nif [ -d "%s" ]; then echo "PERF-STEP: lock held"; else echo "PERF-STEP: lock free"; fi\nexit 0\n' "$tree/.gate-lock" \
  >"$tree/scripts/perf/selftest.sh"
run_gate "$tree"
assert_contains "Q6 a Docker-backed step runs with the lock held" 'CONTRAST-STEP: lock held' "$out"
assert_contains "Q6 a plain step runs with the lock free" 'PERF-STEP: lock free' "$out"

# ------------------------------------------------------------------ result

echo
if [ "$failures" -eq 0 ]; then
  echo "==> ci-local gate self-test: ok ($checks checks)"
  exit 0
fi
echo "==> ci-local gate self-test: $failures of $checks checks FAILED" >&2
exit 1
