#!/usr/bin/env bash
# scripts/api/gen-bindings.go gives the same bytes every time it is run on the
# same contract (#1066).
#
# Its own header says sorting makes the output byte-stable across runs, and
# check-contract-drift.sh depends on that: it regenerates and compares, so a
# generator that can answer two ways makes the drift gate fail a clean tree
# about half the time. One place did not sort: an operation's responses are a
# Go map, and the success status (and the response schema that goes with it)
# was whichever success entry the range visited last. No operation in the
# contract declared two success statuses until recovery's mint did (201 for a
# minted grant, 202 for an accepted archive restore), so it stayed latent.
#
# So this plants the case rather than waiting for it: a copy of the contract in
# which one operation declares a second success status, run through the
# generator many times. Twelve runs is not luck: a generator that picks at
# random agrees twelve times with probability 1 in 2048.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/root/api/v1"
python3 - "$repo_root/api/v1/openapi.json" "$work/root/api/v1/openapi.json" <<'PY'
import json, sys

doc = json.load(open(sys.argv[1]))
for path, methods in sorted(doc["paths"].items()):
    for method, op in sorted(methods.items()):
        responses = op.get("responses", {})
        success = [s for s in responses if s.isdigit() and int(s) < 400]
        if len(success) == 1 and "202" not in responses and "content" in responses[success[0]]:
            responses["202"] = json.loads(json.dumps(responses[success[0]]))
            responses["202"]["description"] = "Accepted, and nothing is minted yet."
            json.dump(doc, open(sys.argv[2], "w"), indent=2)
            print(f"{method.upper()} {path} now declares {success[0]} and 202", file=sys.stderr)
            sys.exit(0)
sys.exit("no operation to plant a second success status on: the contract has changed shape")
PY

(cd "$repo_root" && go build -o "$work/gen" scripts/api/gen-bindings.go)

first=""
runs=12
for i in $(seq 1 "$runs"); do
  mkdir -p "$work/out$i"
  "$work/gen" "$work/root" "$work/out$i/contract.gen.go" "$work/out$i/contract.ts" >/dev/null
  sum="$(cat "$work/out$i/contract.gen.go" "$work/out$i/contract.ts" | shasum -a 256 | cut -d' ' -f1)"
  if [ -z "$first" ]; then
    first="$sum"
  elif [ "$sum" != "$first" ]; then
    echo "FAIL: run $i of $runs produced different bindings from run 1 on the same contract." >&2
    echo "      An operation that declares two success statuses is bound to whichever the generator visits last." >&2
    exit 1
  fi
done
echo "OK: $runs runs over a contract with a two-success operation produced identical bindings."
