#!/usr/bin/env bash
# One docs/site/tools capture script in the Chromium lane, with ffmpeg (#1543).
#
#   bash scripts/e2e/ui-capture.sh docs/site/tools/capture-encryption.mjs --check --only rotated
#
# The capture controls of the agents and encryption self-tests run through
# this from the planted copy. It builds the image scripts/e2e/capture.Dockerfile
# describes (the lane's pinned Playwright image plus ffmpeg) unless the daemon
# already has it, tagged with that file's hash, and runs the lane
# (scripts/e2e/ui-browser.sh run) with RETND_UI_BROWSER_IMAGE pointing at it.
# The lane brings the rest: the tree mounted read-only at its own path, a linux
# install of ui/shared (whose playwright-core the harness borrows), the cap and
# the stall check.
#
# Inside the lane the command is this script again, with --inside: the lane
# links docs/ to the read-only tree, and the capture scripts write their
# pictures under docs/site/screens and resolve ui/shared from their own path,
# so docs/site is copied into the lane's writable tree first.
#
# Exit statuses are the lane's (0 passed, 1 failed, 3 this machine could not
# run it), and a 3 here too when the image could not be built.
#
# Knobs: the lane's (RETND_UI_BROWSER_*), with RETND_UI_BROWSER_MEMORY at 5g
# unless set, because a GIF pass holds every frame; RETND_UI_BROWSER_PLATFORM
# goes on the build as well as the run.
set -euo pipefail

if [ "${1:-}" = "--inside" ]; then
  shift
  # The lane's working copy: ui/ and scripts/ are real, everything else is a
  # link into the read-only tree.
  if [ -L docs ]; then
    src="$(readlink docs)"
    rm docs
    mkdir docs
    for entry in "$src"/* "$src"/.[!.]*; do
      [ -e "$entry" ] || continue
      [ "${entry##*/}" = site ] && continue
      ln -s "$entry" "docs/${entry##*/}"
    done
    cp -R "$src/site" docs/site
  fi
  exec node "$@"
fi

[ "$#" -ge 1 ] || { echo "usage: ui-capture.sh <docs/site/tools/capture-*.mjs> [args...]" >&2; exit 2; }
root="$(cd "$(dirname "$0")/../.." && pwd)"
dockerfile="$root/scripts/e2e/capture.Dockerfile"

from="$(sed -n 's/^FROM //p' "$dockerfile" | head -n 1)"
version="$(printf '%s' "$from" | sed -n 's/.*:v\([0-9][0-9.]*\)-.*/\1/p')"
[ -n "$version" ] || { echo "ui-capture: cannot read a Playwright version out of $dockerfile's FROM line [$from]" >&2; exit 2; }
sum="$( (sha256sum 2>/dev/null || shasum -a 256) <"$dockerfile" | cut -c1-12)"
tag="retnd-capture:v${version}-noble-${sum}"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "ui-capture: no docker daemon here, so the capture rig cannot run" >&2
  exit 3
fi
platform="${RETND_UI_BROWSER_PLATFORM:-}"
if [ -z "$platform" ]; then
  case "$(docker info --format '{{.Architecture}}' 2>/dev/null)" in
    x86_64 | amd64) platform=linux/amd64 ;;
    aarch64 | arm64) platform=linux/arm64 ;;
  esac
fi
plat=()
[ -z "$platform" ] || plat=(--platform "$platform")

if ! docker image inspect "$tag" >/dev/null 2>&1; then
  echo "==> ui-capture: building $tag from scripts/e2e/capture.Dockerfile"
  # No build context: the Dockerfile copies nothing in.
  if ! docker build ${plat[@]+"${plat[@]}"} -t "$tag" - <"$dockerfile"; then
    echo "ui-capture: could not build $tag, so this machine cannot run the capture rig" >&2
    exit 3
  fi
fi

export RETND_UI_BROWSER_IMAGE="$tag"
export RETND_UI_BROWSER_MEMORY="${RETND_UI_BROWSER_MEMORY:-5g}"
[ -z "$platform" ] || export RETND_UI_BROWSER_PLATFORM="$platform"
exec bash "$root/scripts/e2e/ui-browser.sh" run bash scripts/e2e/ui-capture.sh --inside "$@"
