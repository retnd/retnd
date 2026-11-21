#!/usr/bin/env bash
# Runs promtool over the example bundle's alerting rules (#1595): the rules
# file is checked, then the unit tests in
# container/observability/prometheus/testdata/ are evaluated against it. Needs a
# Docker daemon; it fails rather than skips without one. The image is the one
# the bundle's compose file pins, so the rules are tested by the Prometheus
# they ship with.
#
#     bash scripts/observability/rule-tests.sh
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
image="$(sed -n 's/^[[:space:]]*image:[[:space:]]*\(prom\/prometheus:[^[:space:]]*\).*/\1/p' "$root/container/observability/compose.observability.yaml" | head -n 1)"
[ -n "$image" ] || { echo "rule-tests: no prom/prometheus image in compose.observability.yaml" >&2; exit 1; }
exec docker run --rm --entrypoint /bin/sh \
  -v "$root/container/observability/prometheus:/p:ro" -w /p/testdata "$image" \
  -c 'promtool check rules /p/rules/retnd.rules.yml && promtool test rules retnd.rules.test.yml' "$@"
