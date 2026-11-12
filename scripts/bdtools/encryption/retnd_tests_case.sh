#!/usr/bin/env bash
# Runs a slice of the retnd-tests suites against retnd built from the tree
# this script sits in (#1331). scripts/bdtools/encryption/selftest.py calls it
# for the T2.4 and N2.5 controls: the planted copy is built, the pinned tests
# run against it, and a red run has to name the case.
#
#     retnd_tests_case.sh a <case-selector>    Suite A: TestCLIContract/<selector>
#     retnd_tests_case.sh c <spec> [grep]      Suite C: one equivalence spec
#
#   RETND_TESTS_DIR  a checkout of retnd/retnd-tests at the sha
#                    scripts/e2e/tests-repo.pin names (refused otherwise),
#                    with suites/equivalence's npm dependencies and Playwright
#                    browser installed for Suite C
#   RETND_COMMIT     the commit the build says it is (the copy has no .git, so
#                    the caller passes it; both sides of the identity
#                    handshake read this one value)
#
# Suite C also needs ui/shared's dependencies in this tree (the copy links
# them in) because it builds the production bundle from it.
set -euo pipefail

root="$(cd "$(dirname "$0")/../../.." && pwd)"
suite="${1:?usage: retnd_tests_case.sh a|c <selector> [grep]}"
sel="${2:?usage: retnd_tests_case.sh a|c <selector> [grep]}"
tests="${RETND_TESTS_DIR:?set RETND_TESTS_DIR to the pinned retnd-tests checkout}"
commit="${RETND_COMMIT:?set RETND_COMMIT to the commit this build says it is}"

want="$(sed -n 's/^TESTS_REPO_SHA=//p' "$root/scripts/e2e/tests-repo.pin")"
have="$(git -C "$tests" rev-parse HEAD)"
if [ "$want" != "$have" ]; then
  echo "retnd_tests_case: $tests is at $have and the pin says $want" >&2
  exit 2
fi

bin="$root/.e2e-rt"
mkdir -p "$bin"
ld="-X main.version=${commit:0:7} -X main.commit=$commit"
(cd "$root/core" && GOWORK=off GOFLAGS= go build -ldflags "$ld" -o "$bin/retnd" ./cmd/retnd)
(cd "$root/apps/generic" && GOWORK=off GOFLAGS= go build -ldflags "$ld" -o "$bin/retnd-web" ./cmd/retnd-web)

export RETND_MODE=local RETND_BINARY="$bin/retnd" RETND_WEB_BINARY="$bin/retnd-web" RETND_COMMIT="$commit"
case "$suite" in
  a)
    cd "$tests" && go test ./suites/cli/ -count=1 -v -run "TestCLIContract/$sel"
    ;;
  c)
    export RETND_UI_DIR="$root/ui/shared"
    cd "$tests/suites/equivalence"
    if [ -n "${3:-}" ]; then
      npx playwright test "$sel" --reporter=list -g "$3"
    else
      npx playwright test "$sel" --reporter=list
    fi
    ;;
  *)
    echo "retnd_tests_case: unknown suite $suite (a or c)" >&2
    exit 2
    ;;
esac
