#!/usr/bin/env bash
# Every Go module in this repository resolves its own dependencies without the
# workspace (#1064).
#
# go.work makes the whole tree build with the highest version of every
# requirement any member asks for, which is convenient and hides one failure
# completely: a dependency bumped in core/ and not in the module that depends on
# core/ builds and tests green here, and the product image (container/Dockerfile
# builds each module without the workspace) and anything run with GOWORK=off
# fail with "go: updates to go.mod needed". That is #1003, #797 and #838.
#
# `go list -mod=readonly -deps ./...` is the cheapest command that makes Go
# resolve the module's whole graph from its own go.mod and go.sum and refuse to
# edit either. It compiles nothing, needs no Docker, and with a warm module
# cache takes a few seconds per module.
#
# The module list is read off the tree rather than written down, so a module
# added later is covered, and the fixtures under testdata (deliberately broken
# or hanging packages for cmd/gotestwatch's own tests) are not modules of this
# repository's and are skipped.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root"

checked=0
failed=()
while IFS= read -r mod; do
  dir="$(dirname "$mod")"
  checked=$((checked + 1))
  if out="$(cd "$dir" && GOWORK=off go list -mod=readonly -deps ./... 2>&1 >/dev/null)"; then
    echo "  ok: $dir resolves on its own"
  else
    echo "  FAIL: $dir does not build without the workspace:" >&2
    printf '%s\n' "$out" | head -6 | sed 's/^/        /' >&2
    failed+=("$dir")
  fi
done < <(find . -name go.mod -not -path '*/node_modules/*' -not -path '*/testdata/*' | sed 's#^\./##' | sort)

# A positive control on the input: a listing that found no module would pass by
# checking nothing, and this repository has five.
if [ "$checked" -lt 5 ]; then
  echo "FAIL: found only $checked go.mod file(s); the listing has stopped seeing the modules." >&2
  exit 1
fi

if [ "${#failed[@]}" -gt 0 ]; then
  echo "FAIL: ${#failed[@]} of $checked module(s) need \`GOWORK=off go mod tidy\`: ${failed[*]}" >&2
  exit 1
fi
echo "OK: all $checked modules resolve their own dependencies without go.work."
