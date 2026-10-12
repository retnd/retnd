#!/usr/bin/env bash
# Runs docs/site/tools/check-contrast.mjs in the pinned Playwright image.
#
# The browsers are already in the image (the same digest
# scripts/e2e/client-machine.Dockerfile pins), so the only thing fetched is
# the playwright-core package, into a throw-away directory inside the
# container. Nothing is installed on the machine that runs this, which is
# the point: the local toolchain is not the thing being tested.
#
#   bash scripts/contrast/run-browser-check.sh [check-contrast.mjs args...]
#
# DOCKER overrides the docker command (CI inside the Playwright container
# calls scripts/contrast/in-container.sh directly instead).
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
image="${CONTRAST_PLAYWRIGHT_IMAGE:-mcr.microsoft.com/playwright:v1.62.1-noble@sha256:dcc5531e97840b9b5e794f2814476b21571c5124a3fca2267d73041f56e7580e}"

exec "${DOCKER:-docker}" run --rm \
  -v "$repo_root/docs/site:/site:ro" \
  -v "$repo_root/scripts/contrast:/contrast:ro" \
  "$image" bash /contrast/in-container.sh "$@"
