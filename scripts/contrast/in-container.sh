#!/usr/bin/env bash
# The half of the browser check that runs where the browsers are: a
# Playwright image, or the CI job that uses one as its container.
#
# Expects the site at /site unless CONTRAST_SITE says otherwise. It installs
# playwright-core at the version the image's browsers were built for into a
# scratch directory (never into the checkout), then runs the check.
set -euo pipefail

site="${CONTRAST_SITE:-/site}"
version="${PLAYWRIGHT_VERSION:-1.62.1}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

(cd "$work" && npm init -y >/dev/null && \
  PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install --no-audit --no-fund --save-exact "playwright-core@${version}" >/dev/null)

PLAYWRIGHT_CORE_DIR="$work" PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/ms-playwright}" \
  node "$site/tools/check-contrast.mjs" --site "$site" "$@"
