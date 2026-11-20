#!/usr/bin/env python3
"""Planted violations for EPIC O's observability guards (#899, FR-99).

The scrape endpoint's guards are the kind that pass on the tree they ship
with and could not fail: a handler that answers 401 because nothing reached
it, a verifier that refuses a rat_ token because no test ever offered one, a
cache that looks cached because the exposition's clock has one-second
resolution. Their tests going green is no evidence they can go red.

So each guard is mutation-tested here against the real tree, the way
`scripts/bdtools/assurance/selftest.py` does it: a copy of the working tree
gets one deliberate violation planted in a real product file, a narrowed
slice of the suite runs, and it must fail AND name the promise. Naming it, not
merely failing, is what stops a mutant that broke the build for an unrelated
reason from reading as a catch.

This file is O1.1's half of `scripts/observability/selftest.sh`: PV1 (a
handler that never consults the verifier) and PV2 (a plaintext token in
config accepted), the positive controls the spec pairs with them (a valid
bearer and the same token through basic_auth both scrape, which is the clean
run of the PV1 test), one plant per falsification matrix rows O-01, O-03,
O-04, O-05, O-06 and O-25 name, and the two the #899 review found (the limit
keyed on a forwarded header, and the /metrics method fallback stripping Allow
from every API 405). PV3 to PV6 belong to O1.2, O1.3 and O1.4 and
join this list when those land.

Every mutation is anchored to a verbatim copy of product source, tabs and
all. An anchor that no longer matches is STALE ANCHOR: the control is skipped
rather than run against a tree with nothing planted in it (#458).
`--check-anchors` checks every anchor against the real tree, building
nothing.

A few minutes: each mutant rebuilds and runs a narrowed slice of one package.
No Docker, no network.
"""

from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness, selftest_swap

PROGRAM = "observability selftest"

HANDLER_GO = "apps/common/webhost/handlers_metrics.go"
ROUTER_GO = "apps/common/webhost/router.go"
AUTH_GO = "apps/common/webhost/auth.go"
ACTIONLOG_GO = "apps/common/webhost/actionlog.go"
AGENTACCESS_GO = "apps/common/webhost/agentaccess.go"
SCRAPE_GO = "core/service/scrape.go"
CONFIG_GO = "core/internal/config/observability.go"
COUNTER_GO = "core/internal/metrics/scrape.go"
MOUNTS_GO = "apps/common/webhost/serve/mounts.go"

WEB_PKG = ("apps/common", ("./webhost/",))
SERVICE_PKG = ("core", ("./service/",))
CONFIG_PKG = ("core", ("./internal/config/",))
METRICS_PKG = ("core", ("./internal/metrics/",))
SERVE_PKG = ("apps/common", ("./webhost/serve/",))

PLANT = "// PLANTED VIOLATION (scripts/bdtools/observability/selftest.py): "

VERIFY = "\tif !ok || !scrape.VerifyScrapeToken(presented) {\n"
METRICS_ROUTE = '\tr.Get("/metrics", h.metrics)\n'


def gate(root: Path, where: tuple[str, tuple[str, ...]], pattern: str) -> tuple[int, str]:
    """Run a narrowed slice in `root`, never raising on its own non-zero
    status: an expected failure has to become a value this file inspects."""
    module, packages = where
    env = dict(os.environ)
    env["GOWORK"] = "off"
    proc = harness.sh(
        ["go", "test", "-count=1", "-timeout", "10m", "-run", pattern, *packages],
        check=False,
        capture=True,
        cwd=root / module,
        env=env,
    )
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def expect_fails(
    tracker: Any, tally: Any, label: str, directory: Path, needle: str, where: tuple[str, tuple[str, ...]], pattern: str
) -> None:
    if tracker.stale_verdict(label):
        return
    if tracker.anchors_only(label):
        return
    code, out = gate(directory, where, pattern)
    if code == 0:
        tally.bad(f"{label}. The suite PASSED against a planted violation.", out)
        return
    if needle not in out:
        tally.bad(
            f"{label}. The suite failed, but never named the promise that was broken.",
            f"expected its output to mention: {needle}\n{out}",
        )
        return
    matched = next((line.strip()[:300] for line in out.splitlines() if needle in line), "")
    tally.ok(f"(caught): {label}", matched)


def expect_passes(
    tracker: Any, tally: Any, label: str, directory: Path, where: tuple[str, tuple[str, ...]], pattern: str
) -> None:
    if tracker.stale_verdict(label):
        return
    if tracker.anchors_only(label):
        return
    code, out = gate(directory, where, pattern)
    if code == 0:
        tally.ok(f"(clean): {label}")
    else:
        tally.bad(f"{label}. The suite FAILED against an unmutated tree, so its failures mean nothing.", out)


# Each entry: label, [(file, old, new), ...], needle, where, pattern. Most
# plant one swap; the recordActions one needs two files moved together.
MUTATIONS: list[tuple[Any, ...]] = [
    (
        "#1568: the engine's serve mux without /metrics, so the real engine answers 404 to every scrape",
        [(
            MOUNTS_GO,
            '\t\tmount{pattern: "/metrics", handler: apiRouter, kind: mountAPIRouter},\n',
            "\t\t" + PLANT + "the scrape endpoint is never mounted.\n",
        )],
        "the engine's mux answers /metrics with its own 404",
        SERVE_PKG,
        "TestEngineMounts_1568_|TestEngine_1568_",
    ),
    (
        "#1568, ADR 0035 decision 5: web-ui forwarding /metrics to the engine",
        [(
            MOUNTS_GO,
            '\t\t{pattern: "/metrics", handler: http.NotFoundHandler(), kind: mountNotForwarded},\n',
            '\t\t{pattern: "/metrics", handler: proxy, kind: mountEngineProxy}, ' + PLANT + "the published port carries the scrape endpoint.\n",
        )],
        "which only the engine serves (ADR 0035, decision 5)",
        SERVE_PKG,
        "TestEveryAPIMountIsARouterTheGuardWalks$|TestUI_1568_",
    ),
    (
        "PV1 (O-01): a handler that serves /metrics without consulting the verifier",
        [(HANDLER_GO, VERIFY, "\t" + PLANT + "the verifier is never asked.\n\tif _ = presented; ok && false {\n")],
        "a wrong bearer = 200",
        WEB_PKG,
        "TestMetrics_AnUnauthenticatedScrapeIsRefusedAndTheCredentialStillWorks",
    ),
    (
        "PV2 (O-03): a plaintext observability.metrics.token accepted at Load",
        [(CONFIG_GO, '\t\tif m.Token != "" {\n', "\t\t" + PLANT + "an inline token is accepted.\n\t\tif false {\n")],
        "--- FAIL: TestObservability_APlaintextScrapeTokenIsRefusedByNameAndNeverEchoed",
        CONFIG_PKG,
        "TestObservability_APlaintextScrapeTokenIsRefusedByNameAndNeverEchoed",
    ),
    (
        "O-01: no refusal limiter, so the eleventh refusal is another 401",
        [(
            HANDLER_GO,
            "\tif wait := h.scrapeLimiter.RetryAfter(address); wait > 0 {\n",
            "\t" + PLANT + "nothing is ever limited.\n\tif wait := h.scrapeLimiter.RetryAfter(address); wait > 0 && false {\n",
        )],
        "the eleventh refusal = 401",
        WEB_PKG,
        "TestMetrics_TheEleventhRefusalFromOneAddressIsRateLimitedAndACleanAddressIsNot",
    ),
    (
        "O-01: the limit keyed on a forwarded header a stranger wrote",
        [(
            HANDLER_GO,
            "func scrapeClientAddress(r *http.Request) string {\n",
            "func scrapeClientAddress(r *http.Request) string {\n\tif first := firstForwardedEntry(r.Header.Get(\"X-Forwarded-For\")); first != \"\" { "
            + PLANT
            + "the header picks the key.\n\t\treturn first\n\t}\n",
        )],
        "after a stranger sent ten refusals claiming to be it",
        WEB_PKG,
        "TestMetrics_TheRefusalLimitIsKeyedOnThePeerAndNeverOnAForwardedHeader",
    ),
    (
        "the /metrics method fallback set on the root mux, which chi copies into every subrouter",
        [(
            ROUTER_GO,
            METRICS_ROUTE,
            METRICS_ROUTE
            + "\tr.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) { "
            + PLANT
            + "every 405 loses its Allow.\n\t\tif req.URL.Path == \"/metrics\" {\n\t\t\th.metrics(w, req)\n\n\t\t\treturn\n\t\t}\n\t\tw.WriteHeader(http.StatusMethodNotAllowed)\n\t})\n",
        )],
        "answered 405 with Allow",
        WEB_PKG,
        "TestA405OutsideTheScrapeEndpointStillListsItsMethods",
    ),
    (
        "a wrong method on /metrics answered by chi, uncounted and before the credential",
        [(
            ROUTER_GO,
            '\t\t\tif req.URL.Path == "/metrics" && req.Method != http.MethodGet {\n',
            '\t\t\tif false && req.URL.Path == "/metrics" && req.Method != http.MethodGet { ' + PLANT + "chi answers the method.\n",
        )],
        "POST /metrics with the credential",
        WEB_PKG,
        "TestA405OutsideTheScrapeEndpointStillListsItsMethods",
    ),
    (
        "O-01 control: a limiter that counts every scrape, so a clean address is limited",
        [(
            HANDLER_GO,
            "\tpresented, ok := scrapeCredential(r)\n",
            "\th.scrapeLimiter.Allow(address) " + PLANT + "every scrape counts against the limit.\n\tpresented, ok := scrapeCredential(r)\n",
        )],
        "from a clean address = 429",
        WEB_PKG,
        "TestMetrics_TheEleventhRefusalFromOneAddressIsRateLimitedAndACleanAddressIsNot",
    ),
    (
        "O-03: the plaintext written into the credential store beside its digest",
        [(
            SCRAPE_GO,
            '\tencoded, err := json.Marshal(record)\n\tif err != nil {\n\t\treturn MintedScrapeToken{}, fmt.Errorf("service: encoding the scrape credential store: %w", err)\n\t}\n',
            '\tencoded, err := json.Marshal(record)\n\tif err != nil {\n\t\treturn MintedScrapeToken{}, fmt.Errorf("service: encoding the scrape credential store: %w", err)\n\t}\n\tencoded = append(encoded, token...) '
            + PLANT
            + "the plaintext is kept.\n",
        )],
        "--- FAIL: TestAScrapeCredentialIsStoredAsADigestInAPrivateFile",
        SERVICE_PKG,
        "TestAScrapeCredentialIsStoredAsADigestInAPrivateFile",
    ),
    (
        "O-03: the plaintext left in a second file under the state directory, which only the route-level byte scan reads",
        [(
            SCRAPE_GO,
            "\tb.scrape.loaded = false\n\n\treturn MintedScrapeToken{Token: token,",
            "\t_ = os.WriteFile(filepath.Join(filepath.Dir(dir), \"scrape-token.txt\"), []byte(token), 0o600) "
            + PLANT
            + "a plaintext copy beside config.yaml.\n\tb.scrape.loaded = false\n\n\treturn MintedScrapeToken{Token: token,",
        )],
        "--- FAIL: TestMetrics_NoPlaintextScrapeTokenIsPersistedAnywhere",
        WEB_PKG,
        "TestMetrics_NoPlaintextScrapeTokenIsPersistedAnywhere",
    ),
    (
        "O-03: the scrape-token mint over plain HTTP from another machine without the acknowledgement",
        [(
            ROUTER_GO,
            'r.With(requireCSRF, requireMintTransportAck(h.enforce)).Post("/settings/observability/scrape-token", h.mintScrapeToken)',
            'r.With(requireCSRF).Post("/settings/observability/scrape-token", h.mintScrapeToken) ' + PLANT + "no transport acknowledgement.",
        )],
        "--- FAIL: TestMetrics_AMintOverPlainHTTPFromAnotherMachineNeedsTheAcknowledgement",
        WEB_PKG,
        "TestMetrics_AMintOverPlainHTTPFromAnotherMachineNeedsTheAcknowledgement",
    ),
    (
        "O-04: /metrics behind recordActions, with the middleware's GET skip gone",
        [
            (ROUTER_GO, METRICS_ROUTE, '\tr.With(recordActions(cfg.Recorder)).Get("/metrics", h.metrics) ' + PLANT + "every scrape is an action.\n"),
            (ACTIONLOG_GO, "\t\t\tif recorder == nil || r.Method == http.MethodGet {\n", "\t\t\tif recorder == nil { " + PLANT + "GETs are recorded.\n"),
        ],
        "--- FAIL: TestMetrics_FiftyScrapesWriteNoActionLogRow",
        WEB_PKG,
        "TestMetrics_FiftyScrapesWriteNoActionLogRow",
    ),
    (
        "O-04: the refusal counter labelled with a value the request carried",
        [
            (COUNTER_GO, "\trejected [len(scrapeRejectedReasons)]uint64\n", "\trejected [len(scrapeRejectedReasons)]uint64\n\tlast     string " + PLANT + "the last reason seen, whatever it was.\n"),
            (COUNTER_GO, "\treturn false\n}\n", "\ts.mu.Lock()\n\ts.last = reason\n\ts.mu.Unlock()\n\n\treturn false\n}\n"),
            (COUNTER_GO, "\ts.mu.Lock()\n\tcounts := s.rejected\n\ts.mu.Unlock()\n", "\ts.mu.Lock()\n\tcounts := s.rejected\n\tlast := s.last\n\ts.mu.Unlock()\n"),
            (
                COUNTER_GO,
                '\t\tb.WriteString(name + "{reason=" + quoteLabel(r) + "} " + formatFloat(float64(counts[i])) + "\\n")\n\t}\n',
                '\t\tb.WriteString(name + "{reason=" + quoteLabel(r) + "} " + formatFloat(float64(counts[i])) + "\\n")\n\t}\n\tif last != "" {\n\t\tb.WriteString(name + "{reason=" + quoteLabel(last) + "} 1\\n")\n\t}\n',
            ),
        ],
        "--- FAIL: TestScrapeRejected_AValueOutsideTheVocabularyNeverBecomesALabel",
        METRICS_PKG,
        "TestScrapeRejected_AValueOutsideTheVocabularyNeverBecomesALabel",
    ),
    (
        "O-04: a refused scrape that is never counted",
        [(
            HANDLER_GO,
            "\t\th.scrapeLimiter.Allow(address)\n\t\tscrape.ObserveScrapeRejected(scrapeRejectedUnauthenticated)\n",
            "\t\th.scrapeLimiter.Allow(address)\n\t\t" + PLANT + "the refusal is not counted.\n",
        )],
        "--- FAIL: TestMetrics_RefusalsAreCountedOverAClosedVocabularyAndNeverByRequestValue",
        WEB_PKG,
        "TestMetrics_RefusalsAreCountedOverAClosedVocabularyAndNeverByRequestValue",
    ),
    (
        "O-05: the workflow half rendered at zero when no engine was built",
        [(
            HANDLER_GO,
            '\tif workflow := scrape.WorkflowMetrics(); workflow != "" {\n',
            '\tif workflow := scrape.WorkflowMetrics(); true {\n\t\tif workflow == "" {\n\t\t\tworkflow = "retnd_workflow_runs_total 0\\n" '
            + PLANT
            + "a fabricated zero.\n\t\t}\n",
        )],
        "--- FAIL: TestMetrics_TheWorkflowExpositionIsAbsentNotZeroWhenNoEngineWasBuiltAndPresentWhenOneWas",
        WEB_PKG,
        "TestMetrics_TheWorkflowExpositionIsAbsentNotZeroWhenNoEngineWasBuiltAndPresentWhenOneWas",
    ),
    (
        "O-05 control: the workflow half always omitted, even when an engine ran",
        [(
            HANDLER_GO,
            '\tif workflow := scrape.WorkflowMetrics(); workflow != "" {\n',
            '\tif workflow := scrape.WorkflowMetrics(); workflow != "" && false { ' + PLANT + "never served.\n",
        )],
        "--- FAIL: TestMetrics_TheWorkflowExpositionIsAbsentNotZeroWhenNoEngineWasBuiltAndPresentWhenOneWas",
        WEB_PKG,
        "TestMetrics_TheWorkflowExpositionIsAbsentNotZeroWhenNoEngineWasBuiltAndPresentWhenOneWas",
    ),
    (
        "O-06: the health report rebuilt on every scrape",
        [(
            SCRAPE_GO,
            "\tif !b.scrape.cachedAt.IsZero() && now().Sub(b.scrape.cachedAt) < interval {\n",
            "\tif false && !b.scrape.cachedAt.IsZero() && now().Sub(b.scrape.cachedAt) < interval { " + PLANT + "no cache.\n",
        )],
        "--- FAIL: TestTheHealthExpositionIsRebuiltOnlyWhenTheIntervalHasPassed",
        SERVICE_PKG,
        "TestTheHealthExpositionIsRebuiltOnlyWhenTheIntervalHasPassed",
    ),
    (
        "O-25, first direction: the scrape endpoint accepts an rat_ agent token",
        [(
            HANDLER_GO,
            VERIFY,
            '\t' + PLANT + 'an agent token opens the exposition.\n\tif !ok || (!scrape.VerifyScrapeToken(presented) && !strings.HasPrefix(presented, "rat_")) {\n',
        )],
        "an rat_ agent token as a bearer on /metrics = 200",
        WEB_PKG,
        "TestMetrics_TheScrapeCredentialAndTheAgentTokenNeverCross",
    ),
    (
        "O-25, second direction: authMiddleware lets an rsc_ scrape credential into /api/v1",
        [(
            AUTH_GO,
            "\t\t\tif presented, ok := bearerToken(r); ok {\n",
            '\t\t\tif a := r.Header.Get("Authorization"); len(a) > 11 && a[:11] == "Bearer rsc_" { '
            + PLANT
            + "a scrape credential is a session.\n\t\t\t\tnext.ServeHTTP(w, r)\n\t\t\t\treturn\n\t\t\t}\n\t\t\tif presented, ok := bearerToken(r); ok {\n",
        )],
        "the scrape credential on /api/v1/system/version = 200",
        WEB_PKG,
        "TestMetrics_TheScrapeCredentialAndTheAgentTokenNeverCross",
    ),
    (
        "O-25, the audit half: an rsc_ bearer taken for an attempted agent token",
        [(
            AGENTACCESS_GO,
            "\tif !strings.HasPrefix(token, service.AgentTokenPrefix) {\n",
            '\tif !strings.HasPrefix(token, service.AgentTokenPrefix) && !strings.HasPrefix(token, "rsc_") { ' + PLANT + "a scrape token is tried as an agent's.\n",
        )],
        "a crossed credential wrote an agent_audit row",
        WEB_PKG,
        "TestMetrics_TheScrapeCredentialAndTheAgentTokenNeverCross",
    ),
    (
        "O-25, third red: /metrics behind auditAgentCalls with the scrape taken for an agent's call",
        [(
            ROUTER_GO,
            METRICS_ROUTE,
            "\tr.With(func(next http.Handler) http.Handler {\n"
            "\t\treturn http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {\n"
            '\t\t\tnext.ServeHTTP(w, q.WithContext(withAgentPrincipal(q.Context(), service.AgentPrincipal{TokenID: "scrape", Scope: "read"})))\n'
            "\t\t})\n"
            '\t}, auditAgentCalls(cfg.Backend)).Get("/metrics", h.metrics) ' + PLANT + "the scrape is an audited agent call.\n",
        )],
        "while the agent audit trail is unavailable = 503",
        WEB_PKG,
        "TestMetrics_AScrapeStillAnswersWhileTheAgentAuditTrailIsUnavailable",
    ),
]

# The clean runs. The first is the spec's two positive controls (a valid
# bearer and the same token through basic_auth both return 200 and parse as
# exposition), without which a handler that refuses everything passes PV1.
CLEAN_CONTROLS = [
    ("the positive controls: a valid bearer and basic_auth both scrape", WEB_PKG, "TestMetrics_AnUnauthenticatedScrapeIsRefusedAndTheCredentialStillWorks"),
    ("the scrape endpoint suite and the 405 guard", WEB_PKG, "TestMetrics_|TestA405OutsideTheScrapeEndpointStillListsItsMethods"),
    ("the shared root-route allowlist", WEB_PKG, "TestEveryRootRouteOutsideTheAPIIsInTheOneList|TestAPlantedRootRouteOutsideTheListIsCaught"),
    ("the credential store and the report cache", SERVICE_PKG, "TestAScrapeCredential|TestOnlyTheCurrentScrapeCredentialVerifies|TestTheHealthExpositionIsRebuilt|TestEveryRefusalReasonIsCounted"),
    ("the observability block", CONFIG_PKG, "TestObservability_"),
    ("the refusal counter's vocabulary", METRICS_PKG, "TestScrapeRejected_"),
]


def body(root: Path, dry_run: bool, shard: tuple[int, int] | None) -> int:
    tally = selftest_swap.Tally()

    with tempfile.TemporaryDirectory(prefix="retnd-observability-selftest.") as tmp_name:
        tmp = Path(tmp_name)
        tracker = selftest_swap.AnchorTracker(dry_run=dry_run, root=root, tmp=tmp)

        print("==> negative controls: each slice is clean on the real tree")
        for label, where, pattern in ([] if shard and shard[0] != 1 else CLEAN_CONTROLS):
            expect_passes(tracker, tally, f"{label} on an unmutated tree", root, where, pattern)

        for i, entry in enumerate(MUTATIONS):
            if not selftest_swap.in_shard(i, shard):
                continue
            label, swaps, needle, where, pattern = entry
            print()
            print(f"==> {label}")
            d = selftest_swap.mutant(root, tmp, f"observability-{i:02d}", dry_run=dry_run)
            for rel, old, new in swaps:
                tracker.swap(d / rel, old, new)
            expect_fails(tracker, tally, label, d, needle, where, pattern)
            selftest_swap.discard(d, root)

        tracker.stale_summary()
        stale_count = tracker.stale_count
        anchors_checked = tracker.anchors_checked

    total = tally.passed + tally.failed + stale_count
    if tally.failed or stale_count:
        print(file=sys.stderr)
        print(
            f"FAIL: {tally.failed + stale_count} of {total} observability controls did not behave as required "
            f"({tally.failed} reached the wrong verdict, {stale_count} could not plant their violation at all).",
            file=sys.stderr,
        )
        return harness.EXIT_FAILED

    print()
    if dry_run:
        print(f"OK: all {anchors_checked} observability mutation anchors still name code that is in this tree.")
    else:
        print(
            f"OK: all {tally.passed} observability controls behaved as required (every guard was shown to go red "
            "against a real planted violation, and shown not to on the real tree)."
        )
    return harness.EXIT_OK


def main(argv: list[str]) -> int:
    harness.set_program(PROGRAM)
    harness.install_signal_handlers()
    dry_run, shard = selftest_swap.parse_shard_args(argv, "selftest.py", len(MUTATIONS))
    root = harness.repo_root(Path(__file__))
    return harness.finish(lambda: body(root, dry_run, shard))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
