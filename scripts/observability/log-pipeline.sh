#!/usr/bin/env bash
# Runs the shipped Promtail and Alloy pipelines over a captured log file and
# asserts the Loki label set (EPIC O, matrix row O-08). Needs a Docker daemon
# and the docker CLI on PATH; the test fails, it does not skip, without them.
set -euo pipefail
cd "$(dirname "$0")/../../core"
exec go test -count=1 -tags observability_pipeline -timeout 10m ./tests/logpipeline/ "$@"
