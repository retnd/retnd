#!/usr/bin/env bash
# The dashboards and rules name guard (EPIC O, FR-102, #902, matrix row O-14).
#
# Fails on any retnd_* series, label or closed-vocabulary value that a shipped
# Grafana dashboard or Prometheus rules file in container/observability/ names
# and the two renderers do not emit, on any Loki label or field the shipped
# Promtail pipeline and the pinned log schema do not carry, and on any panel
# whose title does not say what it looks like when nothing is wrong. The
# emitted set is computed by rendering a fully loaded health report and
# workflow counter set, not typed out. Its planted violations (PV4, a panel
# querying retnd_backup_set_stale, and the rules-file variant) and their
# positive controls run in the same test, every time.
#
#     bash scripts/observability/check-dashboard-metrics.sh
#     RETND_OBSERVABILITY_DIR=/path/to/a/copy bash scripts/observability/check-dashboard-metrics.sh
#
# The second form checks another copy of the bundle, which is how a planted
# violation is run against the guard without touching the tree. No network,
# no containers.
set -euo pipefail
cd "$(dirname "$0")/../../core"
if [ -n "${RETND_OBSERVABILITY_DIR:-}" ]; then
  RETND_OBSERVABILITY_DIR="$(cd "$RETND_OBSERVABILITY_DIR" && pwd)"
  export RETND_OBSERVABILITY_DIR
fi
exec go test -count=1 -run '_(902|1595)$' ./internal/metrics/ "$@"
