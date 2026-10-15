#!/usr/bin/env bash
# The release workflow moves ghcr.io/retnd/retnd:latest (issue #1168).
#
# Until this existed the image had no `latest` tag at all, while the GitHub
# Release page already called the newest release "Latest". Two things have
# to hold for the tag to be worth having:
#
#   * it only ever moves FORWARD. A 0.5.x backport published after 0.6.x
#     must not take it, and neither may a prerelease;
#   * it is the SAME index digest as the version tag, so the cosign
#     signature and SBOM attestation (keyed by digest) verify for both.
#
# scripts/release/latest.sh is where those rules live, so this drives that
# script with stand-in `gh` and `docker` binaries, once per outcome worth
# having, instead of grepping YAML. The workflow is then checked for the
# wiring: which jobs exist, what they may touch, and that the post-publish
# check runs after the move.
#
# Planted-fault proof: every control below was turned red once by breaking
# the thing it guards (step removed, comparison inverted, prerelease allowed,
# an unpublished version accepted) and put back; the PR body lists them.
#
# Run directly (`bash scripts/tests/release-moves-latest.test.sh`) or let the
# gate run it: scripts/ci-local.sh invokes it in every run.
set -uo pipefail

SCRIPTS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$SCRIPTS_DIR/.." && pwd)"
WORKFLOW="$REPO_ROOT/.github/workflows/release.yml"
LATEST="$REPO_ROOT/scripts/release/latest.sh"

echo "==> release latest tag: latest follows the highest published release and nothing else"

checks=0
failures=0
pass() { checks=$((checks + 1)); printf '    ok   %s\n' "$1"; }
fail() {
  checks=$((checks + 1))
  failures=$((failures + 1))
  printf '    FAIL %s\n' "$1" >&2
  if [ $# -gt 1 ] && [ -n "$2" ]; then
    printf '%s\n' "$2" | sed 's/^/         | /' >&2
  fi
}

SANDBOX="$REPO_ROOT/.release-latest-test"
case "$SANDBOX" in
  /*/.release-latest-test) ;;
  *) echo "refusing to use sandbox path [$SANDBOX]" >&2; exit 1 ;;
esac
rm -rf "$SANDBOX"
mkdir -p "$SANDBOX"
trap 'rm -rf "$SANDBOX"' EXIT

if [ ! -f "$LATEST" ]; then
  fail "scripts/release/latest.sh does not exist, so nothing moves latest"
fi

# --- stand-in gh and docker ----------------------------------------------
#
# gh answers `release list` from $STATE/releases.json, or fails when
# $STATE/gh-down exists. docker keeps one file per image reference under
# $STATE/registry: `inspect` prints it or fails (no such image), `create -t
# DST SRC@sha256:...` writes DST only when SRC is a digest reference that
# exists, which is what the real command does with a single source.
BIN="$SANDBOX/bin"
mkdir -p "$BIN"
cat > "$BIN/gh" <<'STUB'
#!/usr/bin/env bash
printf 'gh %s\n' "$*" >> "$STATE/calls"
[ -f "$STATE/gh-down" ] && { echo "stub gh: HTTP 502" >&2; exit 1; }
if [ "${1:-}" = release ] && [ "${2:-}" = list ]; then
  cat "$STATE/releases.json"
  exit 0
fi
echo "stub gh: unexpected: $*" >&2
exit 64
STUB
cat > "$BIN/docker" <<'STUB'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$STATE/calls"
key() { printf '%s' "$1" | tr -c 'A-Za-z0-9.' '_'; }
[ "${1:-}" = buildx ] && [ "${2:-}" = imagetools ] || { echo "stub docker: unexpected: $*" >&2; exit 64; }
case "${3:-}" in
  inspect)
    f="$STATE/registry/$(key "$4")"
    [ -f "$f" ] || { echo "ERROR: $4: not found" >&2; exit 1; }
    cat "$f"
    ;;
  create)
    [ "${4:-}" = -t ] || { echo "stub docker: create without -t" >&2; exit 64; }
    dst="$5"; src="$6"
    case "$src" in *@sha256:*) digest="sha256:${src##*@sha256:}" ;; *) echo "stub docker: source is not a digest: $src" >&2; exit 65 ;; esac
    printf '%s\n' "$digest" > "$STATE/registry/$(key "$dst")"
    ;;
  *) echo "stub docker: unexpected: $*" >&2; exit 64 ;;
esac
STUB
chmod +x "$BIN/gh" "$BIN/docker"

REPO_IMG="ghcr.io/retnd/retnd"
D63="sha256:$(printf 'a%.0s' $(seq 64))"
D64="sha256:$(printf 'b%.0s' $(seq 64))"
D59="sha256:$(printf 'c%.0s' $(seq 64))"
DOLD="sha256:$(printf 'd%.0s' $(seq 64))"

STATE=""
OUT=""
RC=0

# new_case <name> <releases-json>
new_case() {
  STATE="$SANDBOX/state-$1"
  rm -rf "$STATE"
  mkdir -p "$STATE/registry"
  : > "$STATE/calls"
  printf '%s\n' "$2" > "$STATE/releases.json"
  printf '{"version":"0.6.4","index_digest":"%s"}\n' "$D64" > "$STATE/manifest.json"
}
publish_image() { printf '%s\n' "$2" > "$STATE/registry/$(printf '%s' "$REPO_IMG:$1" | tr -c 'A-Za-z0-9.' '_')"; }
latest_digest() { cat "$STATE/registry/$(printf '%s' "$REPO_IMG:latest" | tr -c 'A-Za-z0-9.' '_')" 2>/dev/null || true; }
run() {
  OUT="$(STATE="$STATE" PATH="$BIN:$PATH" LATEST_MANIFEST="$STATE/manifest.json" GH_REPO=retnd/retnd \
    bash "$LATEST" "$@" 2>&1)"
  RC=$?
}
expect_rc() { # <want> <label>
  if [ "$RC" -eq "$1" ]; then pass "$2"; else fail "$2 (exit $RC, wanted $1)" "$OUT"; fi
}
expect_out() { # <substring> <label>
  case "$OUT" in *"$1"*) pass "$2" ;; *) fail "$2 (output lacks: $1)" "$OUT" ;; esac
}

REL_63='[{"tagName":"v0.6.3","isPrerelease":false,"isDraft":false},{"tagName":"v0.6.2","isPrerelease":false,"isDraft":false},{"tagName":"v0.5.9","isPrerelease":false,"isDraft":false}]'
REL_64='[{"tagName":"v0.6.4","isPrerelease":false,"isDraft":false},{"tagName":"v0.6.3","isPrerelease":false,"isDraft":false},{"tagName":"v0.5.9","isPrerelease":false,"isDraft":false}]'

# --- 1. the highest-semver rule -------------------------------------------
new_case rule-new "$REL_63"
run should-be-latest 0.6.4
expect_rc 0 "0.6.4 over a newest release of 0.6.3 asks for latest"
expect_out "true" "the rule prints true for 0.6.4 over 0.6.3"

new_case rule-either-order "$REL_64"
run should-be-latest 0.6.4
expect_rc 0 "the version being published may already have its release (a re-run)"
expect_out "true" "0.6.4 is still the highest when its own release exists"

new_case rule-backport "$REL_64"
run should-be-latest 0.5.10
expect_rc 0 "a 0.5.x backport is a valid answer, not an error"
expect_out "false" "0.5.10 does not take latest from 0.6.4"

new_case rule-numeric "$REL_63"
run should-be-latest 0.6.10
expect_out "true" "0.6.10 is higher than 0.6.3 (numeric order, not text order)"
new_case rule-numeric2 '[{"tagName":"v0.6.10","isPrerelease":false,"isDraft":false}]'
run should-be-latest 0.6.9
expect_out "false" "0.6.9 is lower than 0.6.10 (numeric order, not text order)"

new_case rule-same "$REL_63"
run should-be-latest 0.6.3
expect_out "true" "republishing the highest version keeps latest"

new_case rule-pre "$REL_63"
run should-be-latest 0.7.0-rc.1
expect_rc 0 "a prerelease is a valid answer, not an error"
expect_out "false" "a prerelease never takes latest, even above every release"

new_case rule-pre-published '[{"tagName":"v0.7.0-rc.1","isPrerelease":true,"isDraft":false},{"tagName":"v0.6.3","isPrerelease":false,"isDraft":false}]'
run should-be-latest 0.6.4
expect_out "true" "a published prerelease does not block 0.6.4 from latest"

new_case rule-draft '[{"tagName":"v0.9.0","isPrerelease":false,"isDraft":true},{"tagName":"v0.6.3","isPrerelease":false,"isDraft":false}]'
run should-be-latest 0.6.4
expect_out "true" "an unpublished draft release does not block 0.6.4 from latest"

new_case rule-first '[]'
run should-be-latest 0.1.0
expect_out "true" "the first release ever is the highest"

new_case rule-gh-down "$REL_63"
touch "$STATE/gh-down"
run should-be-latest 0.6.4
expect_rc 2 "a release list that cannot be read refuses rather than guessing"
case "$OUT" in true|false) fail "an unreadable release list still printed a verdict" "$OUT" ;; *) pass "no verdict is printed when the list is unreadable" ;; esac

new_case rule-garbage "$REL_63"
run should-be-latest not-a-version
expect_rc 2 "something that is not a version refuses"

# --- 2. moving latest, on the publish path --------------------------------
new_case move "$REL_63"
publish_image 0.6.4 "$D64"
publish_image 0.6.3 "$D63"
publish_image latest "$D63"
run move 0.6.4
expect_rc 0 "move succeeds when 0.6.4 is published and highest"
[ "$(latest_digest)" = "$D64" ] && pass "latest now resolves to 0.6.4's index digest" || fail "latest resolves to '$(latest_digest)', not $D64"
grep -q "docker buildx imagetools create -t $REPO_IMG:latest $REPO_IMG@$D64" "$STATE/calls" \
  && pass "it re-points by digest with imagetools create, never by rebuilding" \
  || fail "no imagetools create of the version's digest was issued" "$(cat "$STATE/calls")"

new_case move-backport "$REL_64"
publish_image 0.5.10 "$D59"
publish_image 0.6.4 "$D64"
publish_image latest "$D64"
run move 0.5.10
expect_rc 0 "move for a backport is a clean no-op"
[ "$(latest_digest)" = "$D64" ] && pass "a 0.5.10 backport leaves latest on 0.6.4" || fail "a backport moved latest to '$(latest_digest)'"
grep -q "imagetools create" "$STATE/calls" && fail "a backport issued an imagetools create" || pass "a backport issues no create at all"

new_case move-pre "$REL_63"
publish_image 0.7.0-rc.1 "$D64"
publish_image latest "$D63"
run move 0.7.0-rc.1
[ "$(latest_digest)" = "$D63" ] && pass "a prerelease leaves latest where it was" || fail "a prerelease moved latest to '$(latest_digest)'"

new_case move-nothing "$REL_63"
run move 0.6.4
expect_rc 1 "move refuses a version that has no published image"
[ -z "$(latest_digest)" ] && pass "and creates no latest tag for it" || fail "latest was created for an image that does not exist"

# --- 3. the post-publish check --------------------------------------------
new_case verify-ok "$REL_63"
publish_image 0.6.4 "$D64"
publish_image latest "$D64"
run verify 0.6.4
expect_rc 0 "verify passes when latest and the version tag are one digest"

new_case verify-stale "$REL_63"
publish_image 0.6.4 "$D64"
publish_image latest "$D63"
run verify 0.6.4
expect_rc 1 "verify fails when latest still points at the old release"
expect_out "$D63" "and says which digest latest resolves to"

new_case verify-missing "$REL_63"
publish_image 0.6.4 "$D64"
run verify 0.6.4
expect_rc 1 "verify fails when latest does not exist at all"

new_case verify-no-version "$REL_63"
publish_image latest "$D64"
run verify 0.6.4
expect_rc 1 "verify fails when the version has no image to compare against"

# --- 4. the dispatch backfill ---------------------------------------------
new_case dispatch "$REL_64"
publish_image 0.6.4 "$D64"
publish_image 0.6.3 "$D63"
publish_image latest "$D63"
run repoint 0.6.4
expect_rc 0 "repoint moves latest to a published version whose digest the manifest records"
[ "$(latest_digest)" = "$D64" ] && pass "latest follows 0.6.4 after the backfill" || fail "latest is '$(latest_digest)' after the backfill"

new_case dispatch-unpublished "$REL_64"
publish_image latest "$D63"
run repoint 0.6.4
expect_rc 1 "repoint refuses a version with no published image"
expect_out "no published image" "and says that is why"
[ "$(latest_digest)" = "$D63" ] && pass "latest is untouched by the refusal" || fail "a refused repoint still changed latest"

new_case dispatch-drift "$REL_64"
publish_image 0.6.4 "$DOLD"
publish_image latest "$D63"
run repoint 0.6.4
expect_rc 1 "repoint refuses when the registry digest differs from container/release-manifest.json"
expect_out "release-manifest.json" "and names the manifest"
[ "$(latest_digest)" = "$D63" ] && pass "latest is untouched by the digest mismatch" || fail "a mismatched digest still moved latest"

new_case dispatch-wrong-version "$REL_64"
publish_image 0.6.3 "$D63"
publish_image latest "$D63"
run repoint 0.6.3
expect_rc 1 "repoint refuses a version the manifest does not describe (it is 0.6.4)"

new_case dispatch-backport "$REL_64"
printf '{"version":"0.5.10","index_digest":"%s"}\n' "$D59" > "$STATE/manifest.json"
publish_image 0.5.10 "$D59"
publish_image latest "$D64"
run repoint 0.5.10
expect_rc 1 "repoint refuses to move latest backwards onto a backport"
[ "$(latest_digest)" = "$D64" ] && pass "latest stays on 0.6.4" || fail "a backport repoint moved latest"

# --- 5. the workflow wiring -----------------------------------------------
python3 - "$WORKFLOW" <<'PY'
import re
import sys

wf = open(sys.argv[1], encoding="utf-8").read()
lines = wf.splitlines()


def jobs():
    out, cur, in_jobs = {}, None, False
    for line in lines:
        if re.match(r"^jobs:\s*$", line):
            in_jobs = True
            continue
        if in_jobs and line and not line.startswith((" ", "#")):
            in_jobs = False
        if not in_jobs:
            continue
        m = re.match(r"^  ([A-Za-z0-9_.-]+):\s*$", line)
        if m:
            cur = m.group(1)
            out[cur] = []
            continue
        if cur:
            out[cur].append(line)
    return {k: "\n".join(v) for k, v in out.items()}


J = jobs()
bad = 0


def check(cond, msg):
    global bad
    print(("    ok   " if cond else "    FAIL ") + msg, file=sys.stdout if cond else sys.stderr)
    if not cond:
        bad += 1


def body(name):
    """The job without its comment lines, so prose cannot satisfy a check."""
    return "\n".join(l for l in J.get(name, "").splitlines() if not l.strip().startswith("#"))


def perms(name):
    m = re.search(r"^    permissions:\n((?:      .*\n|\s*\n)*)", body(name) + "\n", re.M)
    return m.group(1) if m else ""


check("tag_latest_for" in re.search(r"workflow_dispatch:.*?\nconcurrency:", wf, re.S).group(0)
      if re.search(r"workflow_dispatch:.*?\nconcurrency:", wf, re.S) else False,
      "workflow_dispatch has a tag_latest_for input")

for job in ("tag-latest", "repoint-latest"):
    b = body(job)
    check(job in J, f"{job} job exists")
    check("scripts/release/latest.sh" in b, f"{job} runs scripts/release/latest.sh")
    p = perms(job)
    check("packages: write" in p and "contents: read" in p, f"{job} holds packages: write and contents: read")
    check("id-token" not in p and "contents: write" not in p,
          f"{job} holds no id-token and no contents: write (it signs nothing and releases nothing)")
    check(re.search(r"docker/login-action", b) is not None, f"{job} logs in to ghcr.io")
    check("latest.sh verify" in b, f"{job} runs the post-publish check")
    mi, vi = b.find("latest.sh move") if job == "tag-latest" else b.find("latest.sh repoint"), b.find("latest.sh verify")
    check(0 <= mi < vi, f"{job} verifies only after it moves")

tb = body("tag-latest")
check(re.search(r"needs:\s*\[[^\]]*\bpublish\b", tb) is not None and "decide" in tb.split("steps:")[0],
      "tag-latest waits for publish (so after sign and attest) and decide")
check("needs.decide.outputs.publish == 'true'" in tb, "tag-latest only runs on a run that publishes")

rb = body("repoint-latest")
check("tag_latest_for" in rb or "needs.decide.outputs.repoint" in rb, "repoint-latest is driven by the tag_latest_for input")
check("workflow_dispatch" in rb or "repoint" in rb, "repoint-latest is dispatch-only")

# A repoint run must not rebuild or republish anything.
for job in ("require-release-gate", "compliance-artifacts"):
    check("repoint" in body(job).split("steps:")[0], f"{job} is skipped on a repoint-only dispatch, so nothing is rebuilt")
check("repoint" in body("decide"), "decide reports whether this run is a repoint")

# Least privilege: packages: write and id-token: write stay where they were needed.
holders = sorted(j for j in J if "packages: write" in perms(j))
check(holders == ["publish", "repoint-latest", "tag-latest"], f"packages: write is held by exactly publish, tag-latest, repoint-latest (found {holders})")
signers = sorted(j for j in J if "id-token" in perms(j))
check(signers == ["publish"], f"id-token: write stays with publish alone (found {signers})")

# The GitHub Release follows the same rule.
gb = body("github-release")
check("scripts/release/latest.sh should-be-latest" in gb, "github-release asks latest.sh whether this version is the latest")
check("--latest" in gb and "--latest=false" in gb, "github-release passes --latest and --latest=false")
check(gb.count("--latest") >= 4, "both the create and the refresh path state their --latest flag")
check("actions/checkout" in gb, "github-release checks the repo out to reach the script")

# No input is expanded into shell source anywhere in the new jobs.
for job in ("tag-latest", "repoint-latest", "github-release"):
    in_run, indent, hit = False, -1, []
    for line in J.get(job, "").splitlines():
        if re.match(r"^\s*#", line):
            continue
        m = re.search(r"[^ ]", line)
        ind = m.start() if m else -1
        if in_run and m and ind <= indent:
            in_run = False
        if in_run and "${{" in line:
            hit.append(line.strip())
        if not in_run and re.match(r"^\s*run:\s*[|>]", line):
            in_run, indent = True, ind
    check(not hit, f"{job} expands no ${{{{ }}}} expression into shell source" + (f": {hit}" if hit else ""))

sys.exit(1 if bad else 0)
PY
wf_rc=$?
if [ "$wf_rc" -eq 0 ]; then
  pass "the workflow wiring checks all held"
else
  fail "the workflow wiring checks failed (see FAIL lines above)"
fi

echo "==> release latest tag: $checks checks, $failures failing"
[ "$failures" -eq 0 ]
