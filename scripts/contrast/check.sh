#!/usr/bin/env bash
# The token half of the docs-site contrast rule (#1054): the positive
# controls first, so a checker that cannot fail is reported before it is
# trusted, then the check itself. No browser, no container, about a second.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
python3 "$here/check-tokens.py" --selftest
python3 "$here/check-tokens.py"
