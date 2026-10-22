#!/usr/bin/env python3
"""Positive controls for artifact recovery's guards (EPIC Q, #976).

Recovery is a read path that hands out retained backup data, so its
guards are exactly the kind that look fine in a diff and can fail
silently: a cost check that never refuses, a digest compare that always
matches, a grant that can be used twice, a target name that escapes the
staging root. Their tests pass on the tree they ship with, which proves
nothing about whether they could fail.

So every guard is mutation-tested here against the real tree: a copy of
the working tree gets one deliberate violation planted in a real product
file, a narrowed slice of the suite runs, and it must fail AND name the
promise the violation broke. Naming it, not merely failing, is what stops
a mutant that broke the build for an unrelated reason from reading as a
pass. `scripts/bdtools/retention/selftest.py` is the template, and they share
`selftest_swap`'s anchor machinery.

Every mutation is anchored to a verbatim copy of product source, tabs and
all. An anchor that no longer matches is STALE ANCHOR: the control is
skipped rather than run against a tree with nothing planted in it, and the
run carries on so one run names every stale control (#458).
`--check-anchors` checks every anchor against the real tree, building
nothing.

A few minutes: each mutant rebuilds and runs a narrowed slice of one of
three packages. No Docker, no network.
"""

from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness, selftest_swap

PROGRAM = "recovery selftest"

EXPORT = "core/internal/recovery/export"
RESOLVE_GO = f"{EXPORT}/resolve.go"
ENGINE_GO = f"{EXPORT}/engine.go"
GRANT_GO = f"{EXPORT}/grant.go"
SCHEDULER_GO = "core/service/scheduler.go"
AUTHENTICATOR_GO = "apps/common/auth/local/authenticator.go"
SESSION_GO = "apps/common/auth/local/session.go"
SESSIONEND_GO = "apps/generic/cmd/retnd-web/sessionend.go"
WEB_MAIN_GO = "apps/generic/cmd/retnd-web/main.go"
STAGE_GO = f"{EXPORT}/stage.go"
UI_GO = "apps/common/webhost/serve/ui.go"
REFUSAL_GO = "apps/common/webhost/refusal.go"
RECOVER_GO = "core/service/recover.go"
RECOVER_RUN_GO = "core/service/recover_run.go"
ROUTER_GO = "apps/common/webhost/router.go"
HANDLER_GO = "apps/common/webhost/handlers_recovery.go"
CLI_GO = "core/cmd/retnd/recover.go"
BUDGET_GO = "core/internal/recovery/budget/budget.go"
APP_SCRUB_GO = "core/internal/app/scrub.go"
REVALIDATE_GO = "core/internal/revalidate/revalidate.go"
PLACEMENTS_GO = "core/internal/state/placements.go"
FLIGHT_GO = f"{EXPORT}/flight.go"
CAPS_GO = "apps/common/platform/capabilities/hostlimits.go"
WEBMAIN_GO = "apps/generic/cmd/retnd-web/main.go"
APP_RT_GO = "core/internal/app/recoverytest.go"
APP_SCRUB_GO = "core/internal/app/scrub.go"
APP_ALERTS_GO = "core/internal/app/alerts.go"
SVC_RT_GO = "core/service/recoverytest.go"
RT_GO = "core/internal/recoverytest/recoverytest.go"
LOCALREC_GO = "core/internal/recoverytest/localrecoverer.go"
STATE_RT_GO = "core/internal/state/recoverytest.go"
FSOPEN_GO = "core/internal/fsopen/fsopen.go"
CONFIG_ASSURANCE_GO = "core/internal/config/assurance.go"
ALERT_GO = "core/internal/alert/alert.go"
OBS_ASSURANCE_GO = "core/internal/obs/assurance_events.go"
REVALIDATE_SELECT_GO = "core/internal/revalidate/select.go"
REVALIDATE_BOUNDS_GO = "core/internal/revalidate/bounds.go"
RT_OUTPUT_GO = "core/internal/recoverytest/output.go"
RECONCILE_GO = "core/internal/reconcile/reconcile.go"
ASSURANCE_OPS_GO = "core/service/assuranceoperations.go"

# (module directory under the tree root, package patterns)
EXPORT_PKG = ("core", ("./internal/recovery/export/",))
SERVICE_PKG = ("core", ("./service/",))
DEPLOY_PKG = ("core", ("./tests/deploymentdiff/",))
BUDGET_PKG = ("core", ("./internal/recovery/budget/",))
CAPS_PKG = ("apps/common", ("./platform/capabilities/",))
WEBMAIN_PKG = ("apps/generic", ("./cmd/retnd-web/",))
WEB_PKG = ("apps/common", ("./webhost/",))
UI_PKG = ("apps/common", ("./webhost/serve/",))
CLI_PKG = ("core", ("./cmd/retnd/",))
AUTH_PKG = ("apps/common", ("./auth/local/",))
SERVE_PKG = ("apps/generic", ("./cmd/retnd-web/",))
APP_PKG = ("core", ("./internal/app/",))
STATE_PKG = ("core", ("./internal/state/",))
FSOPEN_PKG = ("core", ("./internal/fsopen/",))
RT_PKG = ("core", ("./internal/recoverytest/",))
REVALIDATE_PKG = ("core", ("./internal/revalidate/",))
RECONCILE_PKG = ("core", ("./internal/reconcile/",))
ALERT_PKG = ("core", ("./internal/alert/",))
OBS_PKG = ("core", ("./internal/obs/",))

RECOVER_STREAM_GO = "core/service/recover_stream.go"
PLANT = "// PLANTED VIOLATION (scripts/bdtools/recovery/selftest.py): "


def gate(root: Path, where: tuple[str, tuple[str, ...]], pattern: str) -> tuple[int, str]:
    """Run a narrowed slice in `root`, never raising on its own non-zero
    status: an expected failure has to become a value this file inspects,
    not an exception that unwinds through body() after the first control."""
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


def expect_fails(tracker, tally, label, directory, needle, where, pattern) -> None:
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


def expect_passes(tracker, tally, label, directory, where, pattern) -> None:
    if tracker.stale_verdict(label):
        return
    if tracker.anchors_only(label):
        return
    code, out = gate(directory, where, pattern)
    if code == 0:
        tally.ok(f"(clean): {label}")
    else:
        tally.bad(f"{label}. The suite FAILED against an unmutated tree, so its failures mean nothing.", out)


# Each entry: label, file, old, new, needle, where, pattern
MUTATIONS = [
    (
        "a chargeable copy chosen without the cost acknowledgement",
        RESOLVE_GO,
        "\t\tif chargeable(c) && !req.AcknowledgeCost {\n\t\t\tif declined == nil {",
        "\t\t" + PLANT + "a billed read needs no yes.\n\t\tif false {\n\t\t\tif declined == nil {",
        "want ErrCostNotAcknowledged",
        EXPORT_PKG,
        "TestResolveRefusesAChargeableReadWithoutAcknowledgement|TestResolveAFreeCopyIsChosenOver",
    ),
    (
        "an archive copy thawed without the cost acknowledgement",
        RESOLVE_GO,
        "\t\tif c.Access == archive.RequiresRestore {\n\t\t\tif !req.AcknowledgeCost {",
        "\t\tif c.Access == archive.RequiresRestore {\n\t\t\t" + PLANT + "a provider restore needs no yes.\n\t\t\tif false {",
        "want a cost refusal",
        EXPORT_PKG,
        "TestResolveArchiveOnlyIsRefusedForCostAndSaysSo",
    ),
    (
        "a copy whose bytes differ at the same size delivered as good",
        ENGINE_GO,
        "\tif !strings.EqualFold(sum, exp.Hash) {",
        "\t" + PLANT + "the digest is never compared.\n\tif false {",
        "want ErrIntegrity",
        EXPORT_PKG,
        "TestStageBytesFlippedMidFile|TestStageLocalAndRemoteCorruptionAreDistinguishable",
    ),
    (
        "a copy of the wrong length delivered as good",
        ENGINE_GO,
        "\tif exp.Size != nil && n != *exp.Size {",
        "\t" + PLANT + "the length is never compared.\n\tif false {",
        "short copy",
        EXPORT_PKG,
        "TestStageShortAndLongCopiesFail",
    ),
    (
        "a failed recovery that leaves its partial file behind",
        ENGINE_GO,
        "\t\tif rmErr := e.Staging.remove(f.Name()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {",
        "\t\t" + PLANT + "the partial is never removed.\n\t\tif rmErr := error(nil); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {",
        "left staging entries",
        EXPORT_PKG,
        "TestStageBytesFlippedMidFile",
    ),
    (
        "a grant that can be used again after it was spent",
        GRANT_GO,
        "\tif e.closed() {\n\t\treturn Redemption{}, ErrGrantUsed\n\t}",
        "\t" + PLANT + "a spent grant is honoured.\n\tif false {\n\t\treturn Redemption{}, ErrGrantUsed\n\t}",
        "second use after close",
        EXPORT_PKG,
        "TestGrantRedeemsOnceThenIsUsed",
    ),
    (
        "a grant redeemable from another session",
        GRANT_GO,
        "\t\tsubtle.ConstantTimeCompare([]byte(e.session.key()), []byte(caller.key())) != 1 {",
        "\t\t// " + PLANT + "the caller's session is never compared.\n\t\tsubtle.ConstantTimeCompare([]byte(e.session.key()), []byte(e.session.key())) != 1 {",
        "a different session of the same actor",
        EXPORT_PKG,
        "TestGrantRefusesAnotherSessionAndAnotherActor",
    ),
    (
        "a grant that never expires",
        GRANT_GO,
        "\tif e.open == 0 && !now.Before(e.grant.ExpiresAt) {\n\t\treturn Redemption{}, ErrGrantExpired\n\t}",
        "\t" + PLANT + "an expired grant is honoured.\n\tif false {\n\t\treturn Redemption{}, ErrGrantExpired\n\t}",
        "want ErrGrantExpired",
        EXPORT_PKG,
        "TestGrantExpires",
    ),
    (
        "a grant that outlives the session that minted it",
        GRANT_GO,
        "\tif !s.Expires.IsZero() && s.Expires.Before(exp) {",
        "\t" + PLANT + "the session end is not a bound.\n\tif false {",
        "grant outlives its session",
        EXPORT_PKG,
        "TestGrantNeverExpiresPastItsSession",
    ),
    (
        "a target name that climbs out of the staging root",
        STAGE_GO,
        '\tif clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {',
        "\t" + PLANT + "dot-dot is allowed.\n\tif clean == \".\" {",
        'ValidateName("',
        EXPORT_PKG,
        "TestAMintTimeNameCheckRefusesEveryEscapeToo",
        # cleanTarget has a second, independent check (the containment test
        # in noSymlinksBelowRoot), so one plant alone is absorbed by it,
        # which is defence in depth working. Both go for this control.
        [
            (
                STAGE_GO,
                '\tif err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {',
                "\t" + PLANT + "containment is not re-checked.\n\tif err != nil {",
            )
        ],
    ),
    (
        "a target that passes through a symlink",
        STAGE_GO,
        "\t\tif fi.Mode()&fs.ModeSymlink != 0 {\n\t\t\treturn ErrTargetOutsideRoot\n\t\t}",
        "\t\t" + PLANT + "a symlink is followed.\n\t\tif false && fi.Mode()&fs.ModeSymlink != 0 {\n\t\t\treturn ErrTargetOutsideRoot\n\t\t}",
        "want ErrTargetOutsideRoot",
        EXPORT_PKG,
        "TestStagingPublishRefusesEscapes",
    ),
    (
        "staging that ignores its free-space reserve",
        STAGE_GO,
        "\tif free-s.inflight-need < s.Reserve {",
        "\t" + PLANT + "the reserve is not enforced.\n\tif false && free-s.inflight-need < s.Reserve {",
        "want ErrBelowReserve",
        EXPORT_PKG,
        "TestStagingRefusesBelowTheReserve|TestStageBelowReserveRefusesBeforeOpeningTheCopy",
    ),
    (
        "a publish of a file the staging area never created",
        STAGE_GO,
        "func (s *Staging) Publish(from, rel string) (string, error) {\n\tif filepath.Dir(from) != filepath.Clean(s.Root) || !isPartial(filepath.Base(from)) {",
        "func (s *Staging) Publish(from, rel string) (string, error) {\n\t" + PLANT + "any path may be moved into place.\n\tif false {",
        "Publish must only move files this staging area created",
        EXPORT_PKG,
        "TestStagingPublishRefusesToPublishAForeignFile",
    ),
    (
        "a kopia backup set recovered as if it held files",
        RECOVER_GO,
        "ok && set.Engine == model.EngineKopia {",
        "ok && set.Engine == model.EngineKopia && false { " + PLANT.rstrip(),
        "snapshot restore",
        SERVICE_PKG,
        "TestAnIncrementalEngineSetRefusesAndNamesSnapshotRestore",
    ),
    (
        "a mint that writes no action-log row",
        RECOVER_GO,
        "\tdefer func() { b.auditRecovery(ctx, &ev, err) }()",
        "\t" + PLANT + "the mint is not audited.\n\tdefer func() { _ = ev }()",
        "one mint must write exactly one row",
        SERVICE_PKG,
        "TestEveryMintAndEveryServedRange",
    ),
    (
        "a served range that writes no action-log row",
        RECOVER_RUN_GO,
        "\td.b.auditRecovery(context.Background(), &ev, err)\n\tif err != nil {\n\t\td.endTrace(err)\n\t\td.b.finishRecoveryDownload(d.token)\n\t\treturn\n\t}",
        "\t" + PLANT + "a served range is not audited.\n\t_ = &ev\n\tif err != nil {\n\t\td.endTrace(err)\n\t\td.b.finishRecoveryDownload(d.token)\n\t\treturn\n\t}",
        "should write 52 rows",
        SERVICE_PKG,
        "TestEveryMintAndEveryServedRange",
    ),
    (
        "a download whose grant is not spent when the client goes away",
        RECOVER_RUN_GO,
        "\tif err != nil {\n\t\td.endTrace(err)\n\t\td.b.finishRecoveryDownload(d.token)\n\t\treturn\n\t}\n\tif d.b.recoveryHost().grants.Served(d.token, start, end, d.size) {",
        "\t" + PLANT + "only a complete download spends the grant.\n\tif d.b.recoveryHost().grants.Served(d.token, start, end, d.size) {",
        "still redeemable",
        SERVICE_PKG,
        "TestAFailedRangeWritesARowSayingSo|TestAFailedTailRangeStillSpendsTheGrant",
    ),
    (
        "a tail range that spends the grant",
        RECOVER_RUN_GO,
        "\tif d.b.recoveryHost().grants.Served(d.token, start, end, d.size) {",
        "\t" + PLANT + "reaching the last byte spends the grant.\n\tif d.size == 0 || end >= d.size-1 {",
        "after one tail range",
        SERVICE_PKG,
        "TestATailRangeDoesNotSpendTheGrant|TestRangesThatCoverTheArtifact",
    ),
    (
        "ranges that cover the artifact never spending the grant",
        GRANT_GO,
        "\treturn len(e.served) == 1 && e.served[0].from == 0 && e.served[0].to >= size",
        "\t" + PLANT + "coverage is never recognised.\n\treturn false",
        "still redeemable",
        SERVICE_PKG,
        "TestRangesThatCoverTheArtifact|TestAWholeFileRequestSpendsTheGrant",
    ),
    (
        "a gap in the served ranges counted as covered",
        GRANT_GO,
        "\treturn len(e.served) == 1 && e.served[0].from == 0 && e.served[0].to >= size",
        "\t" + PLANT + "one span that does not start at zero counts.\n\treturn len(e.served) == 1 && e.served[0].to >= size",
        "the grant is spent",
        SERVICE_PKG,
        "TestATailRangeDoesNotSpendTheGrant|TestRangesThatCoverTheArtifact",
    ),
    (
        "a chunk that does not match its digest, sent anyway",
        f"{EXPORT}/verifyreader.go",
        "\tif sum := sha256.Sum256(r.buf[:n]); sum != r.ix.Chunks[i] {",
        "\t" + PLANT + "a chunk is never compared with the index.\n\tif sum := sha256.Sum256(r.buf[:n]); false && sum != r.ix.Chunks[i] {",
        "went out; exactly the 3 chunks",
        SERVICE_PKG,
        "TestACopyThatRotsAfterPreparationStopsAtTheBadChunk",
    ),
    (
        "a whole-content digest that is never checked before the last chunk is released",
        f"{EXPORT}/verifyreader.go",
        "\t\t\tif !strings.EqualFold(hex.EncodeToString(r.whole.Sum(nil)), r.ix.SHA256) {",
        "\t\t\t" + PLANT + "the whole digest is never compared.\n\t\t\tif false && !strings.EqualFold(hex.EncodeToString(r.whole.Sum(nil)), r.ix.SHA256) {",
        "want an integrity failure",
        EXPORT_PKG,
        "TestTheWholeDigestIsCheckedBeforeTheLastChunkIsReleased",
    ),
    (
        "a download started on a copy of the wrong size",
        "core/service/recover_stream.go",
        "\tif plan.expect.Size != nil && size != *plan.expect.Size {",
        "\t" + PLANT + "the size is never compared in the pre-flight.\n\tif false {",
        "bandwidth was spent",
        SERVICE_PKG,
        "TestPreflightRefusesBeforeAnyByte",
    ),
    (
        "an ETag that does not change with the bytes it names",
        f"{EXPORT}/chunks.go",
        "\th.Write([]byte(strings.ToLower(sha256Hex)))",
        "\t" + PLANT + "the digest is not part of the validator.\n\t_ = sha256Hex",
        "does not change with the artifact or the digest",
        EXPORT_PKG,
        "TestTheETagIsAStrongValidator",
    ),
    (
        "a body cut short that the server finishes as if it were whole",
        HANDLER_GO,
        "\t\tpanic(http.ErrAbortHandler)",
        "\t\t" + PLANT + "a short body is left to end cleanly.\n\t\t_ = http.ErrAbortHandler",
        "must abort the response",
        WEB_PKG,
        "TestAClientThatGoesAwayMidDownload",
    ),
    (
        "a first chunk that is not verified before the status line",
        HANDLER_GO,
        "\tif p, ok := stream.(service.RecoveryPrimer); ok {",
        "\t" + PLANT + "the first chunk is not primed.\n\tif p, ok := stream.(service.RecoveryPrimer); ok && false {",
        "status 200",
        WEB_PKG,
        "TestAFirstChunkThatFailsVerificationIsAnErrorResponseNotA200",
    ),
    (
        "a finished download that records no verification",
        RECOVER_RUN_GO,
        "\t\td.recordVerified()\n",
        "\t\t" + PLANT + "the end of a download is not recorded.\n",
        "left no verification row",
        SERVICE_PKG,
        "TestACompleteDownloadRecordsWhetherTheWholeThingVerified",
    ),
    (
        "a partial download that claims it verified",
        RECOVER_RUN_GO,
        "\tif d.b.recoveryHost().grants.Served(d.token, start, end, d.size) {\n\t\td.recordVerified()",
        "\t" + PLANT + "any served range claims the whole download verified.\n\td.b.recoveryHost().grants.Served(d.token, start, end, d.size)\n\tif true {\n\t\td.recordVerified()",
        "the trail says the download verified",
        SERVICE_PKG,
        "TestAPartialDownloadClaimsNoVerification",
    ),
    (
        "a recovery reaper that sweeps nothing",
        RECOVER_RUN_GO,
        "\t}\n\tb.sweepRecoveryIfDue(ctx)\n}\n",
        "\t}\n\t" + PLANT + "the timer fires and does nothing.\n\t_ = ctx\n}\n",
        "no request to reap it",
        SERVICE_PKG,
        "TestAbandonedDownloadIsReapedWithoutANewRequest",
    ),
    (
        "a recovery reaper nobody starts",
        SCHEDULER_GO,
        "\t\tb.runRecoveryReaper(ctx)\n",
        "\t\t" + PLANT + "the reaper is never started.\n\t\t<-ctx.Done()\n",
        "never reaped the abandoned download",
        SERVICE_PKG,
        "TestRunOnScheduleStartsTheRecoveryReaper",
    ),
    (
        "a local session whose expiry the authenticator never reports",
        AUTHENTICATOR_GO,
        "\t\tSessionExpiresAt: rec.expiresAt.UTC().Format(time.RFC3339Nano),\n",
        "\t\tSessionExpiresAt: rec.expiresAt.UTC().Format(time.RFC3339Nano)[:0], // " + PLANT.rstrip() + "\n",
        "SessionExpiresAt is empty",
        AUTH_PKG,
        "TestAuthenticatorReportsSessionExpiry",
    ),
    (
        "a logout that tells nobody",
        SESSION_GO,
        "\tif ok && !m.now().After(rec.expiresAt) {\n\t\tm.ended(fn, rec)\n\t}",
        "\t" + PLANT + "the session end is not reported.\n\t_, _, _ = ok, fn, rec",
        "logout never told",
        AUTH_PKG,
        "TestLogoutTellsTheSessionEndHook",
    ),
    (
        "a password rotation that leaves the dropped sessions' grants alive",
        SESSION_GO,
        "\tfn := m.onEnd\n\tm.mu.Unlock()\n\tm.ended(fn, dropped...)\n\treturn token, expiresAt, nil",
        "\tfn := m.onEnd\n\tm.mu.Unlock()\n\t" + PLANT + "dropped sessions are not reported.\n\t_, _ = fn, dropped\n\treturn token, expiresAt, nil",
        "told the hook about 0",
        AUTH_PKG,
        "TestRotatingThePassword",
    ),
    (
        "a logout adapter that never reaches the backup service",
        SESSIONEND_GO,
        "\t\tbackend.EndRecoverySession(service.RecoverySession{Actor: username, Expires: expiresAt})\n",
        "\t\t" + PLANT + "the backup service is not told.\n\t\t_, _, _ = backend, username, expiresAt\n",
        "logout ended 0 recovery sessions",
        SERVE_PKG,
        "TestLogoutRevokesTheRecoveryGrantsOfThatSession",
    ),
    (
        "a first-run backend that never hears about a logout",
        WEB_MAIN_GO,
        "\t\t\tendRecoverySessionsOnLogout(authSvc, opened)\n",
        "\t\t\t" + PLANT + "first-run activation is not wired.\n",
        "calls endRecoverySessionsOnLogout 1 time",
        SERVE_PKG,
        "TestServeHandsEveryBackendItBuildsToTheLogoutAdapter",
    ),
    (
        "a recovery mint behind the destructive gate",
        ROUTER_GO,
        'r.With(requireCSRF, '
        'requireSourceRecoveryGate(h.enforce, h.backend, maxRecoveryMintBodyBytes, mintSourceTarget))'
        '.Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        'r.With(requireCSRF, requireDestructiveGate(gate), '
        'requireSourceRecoveryGate(h.enforce, h.backend, maxRecoveryMintBodyBytes, mintSourceTarget))'
        '.Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        "mint = ",
        WEB_PKG,
        "TestARecoveryCompletesWhileTheDestructiveGateIsShut",
    ),
    (
        "a recovery mint with no CSRF check",
        ROUTER_GO,
        'r.With(requireCSRF, '
        'requireSourceRecoveryGate(h.enforce, h.backend, maxRecoveryMintBodyBytes, mintSourceTarget))'
        '.Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        'r.With(requireSourceRecoveryGate(h.enforce, h.backend, maxRecoveryMintBodyBytes, mintSourceTarget))'
        '.Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        "mint without CSRF",
        WEB_PKG,
        "TestMintCarriesCSRFAndRedeemNeedsOnlyTheSession",
    ),
    (
        "several ranges in one request served instead of refused",
        HANDLER_GO,
        '\tif strings.Contains(r.Header.Get("Range"), ",") {',
        "\t" + PLANT + "multipart ranges are served.\n\tif false {",
        "= 206",
        WEB_PKG,
        "TestMultipleRangesInOneRequestAreRefused",
    ),
    (
        "a download cut by the server's whole-response write timeout",
        HANDLER_GO,
        "func (c *recoveryResponseWriter) extendDeadline() {\n",
        "func (c *recoveryResponseWriter) extendDeadline() {\n\treturn // " + PLANT.rstrip() + "\n",
        "download cut after",
        WEB_PKG,
        "TestADownloadSlowerThanTheServersWriteTimeoutStillCompletes",
    ),
    (
        "a mint that is recorded twice",
        HANDLER_GO,
        "\tmarkSelfAudited(r.Context())\n",
        "\t// " + PLANT.rstrip() + "\n",
        "extra row",
        WEB_PKG,
        "TestAMintIsAuditedOnceByItsOwnRow",
    ),
    (
        "two concurrent recoveries each checking free space alone",
        STAGE_GO,
        "\tif free-s.inflight-need < s.Reserve {",
        "\t" + PLANT + "what other recoveries already claimed is ignored.\n\tif free-need < s.Reserve {",
        "was admitted",
        EXPORT_PKG,
        "TestStagingCountsWhatIsAlreadySpokenFor",
    ),
    (
        "a recovery that never gives back its claim on the staging space",
        ENGINE_GO,
        "\tdefer e.Staging.Release(need)\n",
        "\t// " + PLANT.rstrip() + "\n",
        "kept its claim",
        EXPORT_PKG,
        "TestStageGivesBackItsClaim",
    ),
    (
        "a target name with no length bound",
        STAGE_GO,
        "\tif len(rel) > maxTargetNameLen {",
        "\t" + PLANT + "any length will do.\n\tif false {",
        "want ErrTargetTooLong",
        EXPORT_PKG,
        "TestATargetNameHasALengthBound",
    ),
    (
        "an archive copy the provider already restored still read as needing a restore",
        RECOVER_GO,
        "access, aerr := archive.Access(p.Medium, class, b.observeRestore(ctx, st, m, class, p), nowT)",
        "access, aerr := archive.Access(p.Medium, class, archive.Observation{}, nowT) // " + PLANT.rstrip(),
        "the provider says the restore finished",
        SERVICE_PKG,
        "TestARestoredArchiveCopyIsReadable",
    ),
    (
        "a recovery download cut by the UI proxy's header timeout",
        UI_GO,
        '\tmux.Handle("/api/v1/recoveries/", rollingWriteDeadline(&downloads))\n',
        "\t// " + PLANT.rstrip() + "\n",
        "cut by the proxy",
        UI_PKG,
        "TestUI_ARecoveryMayTakeLonger",
    ),
    (
        "a recovery download cut by the UI host's whole-response write timeout",
        UI_GO,
        "\t_ = w.rc.SetWriteDeadline(now.Add(downloadWriteIdle))",
        "\t_ = now // " + PLANT.rstrip(),
        "cut a download that was still being written",
        UI_PKG,
        "TestUI_ARecoveryIsNotCutByTheUIHost",
    ),
    (
        "a bearer grant written to the proxy's log",
        UI_GO,
        "\tif strings.HasPrefix(p, prefix) {\n\t\treturn prefix + \"{grant}\"\n\t}",
        "\t" + PLANT + "the path is logged whole.\n\tif false {\n\t\treturn prefix + \"{grant}\"\n\t}",
        "a bearer grant reached the log",
        UI_PKG,
        "TestUI_ADownloadsGrantIsNeverLogged",
    ),
    (
        "a bearer grant written to the engine's refusal log",
        REFUSAL_GO,
        "\tif strings.HasPrefix(r.URL.Path, recoveryGrantPathPrefix) {",
        "\t" + PLANT + "the path is logged whole.\n\tif false && strings.HasPrefix(r.URL.Path, recoveryGrantPathPrefix) {",
        "a bearer grant reached the log",
        WEB_PKG,
        "TestAGrantIsNeverWrittenToTheLog",
    ),
    (
        "a grant reaped from under a download that is still being served",
        GRANT_GO,
        "\t\tif e.redeemed && (e.session.endedAt(now) || (e.open == 0 && !now.Before(e.grant.ExpiresAt))) {",
        "\t\t// " + PLANT.rstrip() + "\n\t\tif e.redeemed && (e.session.endedAt(now) || !now.Before(e.grant.ExpiresAt)) {",
        "from under a request that is still streaming it",
        EXPORT_PKG,
        "TestAnOpenRequestKeepsItsGrantAlive",
    ),
    (
        "a local copy that is a symlink or a fifo, opened anyway",
        RECOVER_GO,
        "\t\tf, err := fsopen.OpenRegular(s.local)\n\t\tif errors.Is(err, fsopen.ErrNotRegular) {",
        "\t\tf, err := fsopen.OpenRegular(s.local)\n\t\t" + PLANT + "a refusal is not recognised.\n\t\tif false && errors.Is(err, fsopen.ErrNotRegular) {",
        "must be refused as not readable",
        SERVICE_PKG,
        "TestALocalCopyThatIsASymlink",
    ),
    (
        "an opener that follows a fifo or a link to whatever is behind it",
        FSOPEN_GO,
        "\tif !fi.Mode().IsRegular() {",
        "\t" + PLANT + "any file type is opened.\n\tif false {",
        "OpenRegular opened a fifo",
        FSOPEN_PKG,
        "TestOpenRegularRefusesAFifoWithoutBlocking_C11|TestOpenRegularRefusesWhatIsNotARegularFile_C11",
    ),
    (
        "a client's bad range offset that spends the grant",
        HANDLER_GO,
        "\tcase cw.status == http.StatusRequestedRangeNotSatisfiable:",
        "\t" + PLANT + "a typo in a range burns the download.\n\tcase false:",
        "must not read as the download failing",
        WEB_PKG,
        "TestAnUnsatisfiableRangeIsReportedAsARefusal",
    ),
    (
        "a read budget that never refuses",
        BUDGET_GO,
        "\tdefault:\n\t\treturn nil, &Refusal{Budget: g.name, Limit: int64(g.limit), Destination: g.dest}\n",
        "\tdefault:\n\t\t" + PLANT + "the line is not enforced.\n\t\treturn func() {}, nil\n",
        "over the line",
        BUDGET_PKG,
        "TestAGateRefusesPastItsLine",
    ),
    (
        "a per-destination budget that is not applied",
        BUDGET_GO,
        "\treleaseDest, err := dg.Acquire()",
        "\t" + PLANT + "one destination may take every slot.\n\treleaseDest, err := func() (func(), error) { _ = dg; return func() {}, nil }()",
        "a third read of one destination",
        BUDGET_PKG,
        "TestReadsRefusesPerDestination",
    ),
    (
        "a budget the product never reads",
        BUDGET_GO,
        '\tDownloadStreams Name = "download_streams"\n',
        '\tDownloadStreams Name = "download_streams"\n\n\t' + PLANT + 'a budget with a name, a default and an exit code and no reader.\n\tUnreadBudget Name = "unread_budget"\n',
        "UnreadBudget is declared",
        BUDGET_PKG,
        "TestEveryBudgetIsReadByAProductCodePath",
    ),
    (
        "read ceilings that disagree with the configuration's documented defaults",
        CONFIG_ASSURANCE_GO,
        "\tDefaultMaxConcurrentReads = 2",
        "\tDefaultMaxConcurrentReads = 4 // " + PLANT.rstrip(),
        "documented defaults are 2 and 1",
        BUDGET_PKG,
        "TestTheReadCeilingDefaultsAreTheOnesTheOperatorIsTold",
    ),
    (
        "an abandoned download whose stream slot the reaper keeps",
        RECOVER_RUN_GO,
        "	if st.release != nil {\n\t\tst.release()\n\t}",
        "	" + PLANT + "the slot is not given back.\n\t_ = st.release",
        "kept its slot",
        SERVICE_PKG,
        "TestTheReaperGivesBackAnAbandonedDownloadsStreamSlot",
    ),
    (
        "a full read that never takes a read slot",
        ENGINE_GO,
        "\tif e.Reads != nil {",
        "\t" + PLANT + "reads are unbounded.\n\tif false && e.Reads != nil {",
        "want a per-destination refusal",
        EXPORT_PKG,
        "TestStageOverTheReadBudgetIsRefusedBeforeTheCopyIsOpened",
    ),
    (
        "a full read that never gives its slot back",
        ENGINE_GO,
        "\t	defer release()\n",
        "\t	// " + PLANT.rstrip() + "\n\t\t_ = release\n",
        "is still held",
        EXPORT_PKG,
        "TestAStageGivesBackItsReadSlotWhateverHappens",
    ),
    (
        "two operators asking for the same copy each reading it",
        FLIGHT_GO,
        "\tif c, ok := f.calls[key]; ok {",
        "\t" + PLANT + "nothing is shared.\n\tif c, ok := f.calls[key]; ok && false {",
        "read 2 times",
        EXPORT_PKG,
        "TestTwoOperatorsAskingForTheSameCopyShareOneRead",
    ),
    (
        "a shared copy removed by the first operator to finish",
        FLIGHT_GO,
        "\t	staged.addRefs(c.waiters)\n",
        "\t	// " + PLANT.rstrip() + "\n",
        "removed the copy the second is still using",
        EXPORT_PKG,
        "TestTwoOperatorsAskingForTheSameCopyShareOneRead",
    ),
    (
        "a download ceiling that is not enforced",
        "core/service/recover_stream.go",
        "\t\trel, aerr := h.streams.Acquire()",
        "\t\t" + PLANT + "any number of streams.\n\t\trel, aerr := func() (func(), error) { return func() {}, nil }()",
        "a second stream over a ceiling of one",
        SERVICE_PKG,
        "TestTheDownloadCeilingRefusesTheNextStream",
    ),
    (
        "a provider checksum that disagrees and is read anyway",
        RECOVER_RUN_GO,
        "\tif !res.Passed {",
        "\t" + PLANT + "a disagreement is ignored.\n\tif false && !res.Passed {",
        "positive finding of corruption",
        SERVICE_PKG,
        "TestAProviderChecksumThatDisagrees",
    ),
    (
        "a cancelled recovery that places its output anyway",
        RECOVER_RUN_GO,
        "\tif serr == nil && ctx.Err() != nil {",
        "\t" + PLANT + "cancellation after verifying is ignored.\n\tif false {",
        "was ignored",
        SERVICE_PKG,
        "TestCancellationBetweenVerifyingAndPlacing",
    ),
    (
        "a destination that loses an object mid-read blamed on nobody",
        RECOVER_GO,
        "\tcase isNotFoundCategory(err):",
        "\t" + PLANT + "a vanished object is a generic failure.\n\tcase false && isNotFoundCategory(err):",
        "mid-read",
        SERVICE_PKG,
        "TestADestinationThatDisappearsMidRead",
    ),
    (
        "a budget refusal that maps to the wrong code",
        RECOVER_GO,
        "\tcase errors.As(err, &ref):",
        "\t" + PLANT + "budget refusals are not recognised.\n\tcase false && errors.As(err, &ref):",
        "refusal maps to",
        SERVICE_PKG,
        "TestEveryBudgetRefusalMapsToACode",
    ),
    (
        "host limits changed under live grants",
        RECOVER_GO,
        "\tif h.started {",
        "\t" + PLANT + "limits may change at any time.\n\tif false && h.started {",
        "limits changed under live grants",
        SERVICE_PKG,
        "TestHostLimitsCannotChangeOnceRecoveryHasStarted",
    ),
    (
        "host limits that cannot mean anything accepted from a platform",
        CAPS_GO,
        "\tif err := l.Validate(); err != nil {",
        "\t" + PLANT + "nothing is validated.\n\tif err := l.Validate(); err != nil && false {",
        "was accepted",
        CAPS_PKG,
        "TestHostLimitsThatCannotMeanAnythingAreRefused",
    ),
    (
        "a platform's host limits never handed to the engine",
        WEBMAIN_GO,
        "\tif err := applyHostLimits(backend, platformAdapter); err != nil {",
        "\t" + PLANT + "the limits are never applied.\n\tif err := error(nil); err != nil {",
        "the engine runs with",
        WEBMAIN_PKG,
        "TestAPlatformsHostLimitsReach",
    ),
    (
        "the shipped container quietly made privileged",
        "container/compose.yaml",
        "x-security: &security\n  privileged: false\n",
        "x-security: &security\n  privileged: true # " + PLANT.rstrip() + "\n",
        "container/compose.yaml has changed",
        DEPLOY_PKG,
        "TestTheCanonicalDeploymentIsExactlyWhatItWasBeforeEpicQ",
    ),
    (
        # #1191 made no --to mean the set's SFTP source host, so the guard is
        # now the refusal for a set that has no such default.
        "a recover verb that takes no explicit target",
        CLI_GO,
        '\tif *to == "" {',
        "\t" + PLANT + "no --to is fine.\n\tif false {",
        "--- FAIL: TestRecover1193_ANonSFTPSetHasNoDefaultAndSaysWhy",
        CLI_PKG,
        "TestRecover1193_ANonSFTPSetHasNoDefaultAndSaysWhy",
    ),
    # --- the panel's findings on the recovery-test side (C2, C3, C13, C14) ---
    (
        "a scheduled recovery test that ignores the policy's mode",
        APP_RT_GO,
        "\t\t\tif !bs.Assurance.RecoveryTestScheduled()"
        " || (!builtinMode && s.Stages == nil) {",
        "\t\t\t" + PLANT + "the mode is not consulted.\n\t\t\tif !builtinMode && s.Stages == nil {",
        "under mode off",
        APP_PKG,
        "TestRecoveryTest_ModeOffRunsNothingEvenWithAScript",
    ),
    (
        "a recovery test cadence that is a constant and not the setting",
        APP_RT_GO,
        "recoverytest.Config{Cadence: bs.Assurance.RecoveryTestInterval(), BuiltinCheck:",
        "recoverytest.Config{Cadence: 7 * 24 * time.Hour /* " + PLANT.rstrip() + " */, BuiltinCheck:",
        "the cadence the operator wrote",
        APP_PKG,
        "TestRecoveryTest_UsesTheResolvedCadence",
    ),
    (
        "a validator_id that selects no script",
        APP_RT_GO,
        "\t\treq.Only = v.scriptFile\n",
        "\t\t_ = v.scriptFile // " + PLANT.rstrip() + "\n",
        "asking for exactly pg-check.local.sh",
        APP_PKG,
        "TestRecoveryTest_ValidatorIDSelectsTheScript",
    ),
    (
        "a named validator that is missing, run as another script",
        APP_RT_GO,
        "\tif c.req.Only != \"\" && (!res.Configured || len(res.Scripts) != 1 || res.Scripts[0].Name != c.req.Only) {",
        "\t" + PLANT + "an absent named script reads as not configured.\n\tif false {",
        "no directory but a declared validator",
        APP_PKG,
        "TestRecoveryTest_ANamedValidatorThatIsNotThereFailsClosed",
    ),
    (
        "a pass that sets no recovery requirement on a set's health",
        "core/internal/app/health.go",
        "\t\t\t\tRecoveryTestRequired: bs.Assurance.RecoveryTestEnabled(),",
        "\t\t\t\t// " + PLANT.rstrip(),
        "want DEGRADED",
        APP_PKG,
        "TestHealth_RequiredModeDegradesASetWithNoPass",
    ),
    (
        "a scheduled assurance tick that scrubs and never tests",
        APP_SCRUB_GO,
        "\ts.ScrubTick(ctx)\n\ts.RecoveryTestTick(ctx)\n}",
        "\ts.ScrubTick(ctx)\n\t" + PLANT + "the recovery test is not run.\n}",
        "the tick that scrubs must also test",
        APP_PKG,
        "TestAssuranceTick_ScrubsAndRecoveryTestsInOneCall",
    ),
    (
        "a serving scheduler that scrubs and never tests",
        SCHEDULER_GO,
        "b.state.Load().inner.AssuranceTick(app.WithBackupSetHolds(ctx, b.holds))",
        "b.state.Load().inner.ScrubTick(app.WithBackupSetHolds(ctx, b.holds)) // " + PLANT.rstrip(),
        "RecoveryTestTick had no caller under serve",
        SERVICE_PKG,
        "TestServeSchedulerRunsADueRecoveryTest",
    ),
    (
        "a failing recovery test or scrub that reaches no notifier",
        APP_ALERTS_GO,
        "\tconditions = append(conditions, alert.AssuranceConditions(s.assuranceSignals(healthReport, alertable))...)",
        "\t_ = s.assuranceSignals // " + PLANT.rstrip(),
        "RECOVERY_TEST_FAILING alerts",
        APP_PKG,
        "TestARecoveryTestFailureRaisesRecoveryTestFailingThroughTheDispatcher|TestAScrubFailureRaisesTheScrubFailingAlertThroughTheDispatcher",
    ),
    (
        "a recovery test verdict stamped on bytes the copy no longer has",
        STATE_RT_GO,
        "\tif expect != nil && *expect != cur {",
        "\t" + PLANT + "the bytes are not compared.\n\tif false {",
        "want ErrCopyChanged",
        STATE_PKG,
        "TestRecordRecoveryTestRefusesAVerdictForDifferentBytes",
    ),
    (
        "a recovery test that records its verdict without the identity it read",
        RT_GO,
        "\t\trt.Expect = &identity\n",
        "\t\t// " + PLANT.rstrip() + "\n",
        "guarded by the identity of the copy that was read",
        RT_PKG,
        "TestEveryRecordedVerdictCarriesTheIdentityOfTheCopyItRead",
    ),
    (
        "a recoverer's integrity refusal recorded as an attempt that could not complete",
        RT_GO,
        "\t\tif errors.Is(err, ErrCorruptCopy) {",
        "\t\t" + PLANT + "corrupt bytes are not a digest failure.\n\t\tif false && errors.Is(err, ErrCorruptCopy) {",
        "want failed with the digest not matched",
        RT_PKG,
        "TestARecoverersIntegrityRefusalIsADigestFailure",
    ),
    (
        "a recovery test that stages a local copy through a plain open",
        LOCALREC_GO,
        "\tsrc, err := fsopen.OpenRegular(req.Copy.Location)",
        "\tsrc, err := os.Open(req.Copy.Location) // " + PLANT.rstrip(),
        "Recover blocked on a fifo",
        RT_PKG,
        "TestLocalRecovererRefusesAFifoWithoutBlocking",
    ),
    (
        "a free local volume priced as a download",
        RT_GO,
        "\t\t\tif m.Type == transport.MediumTypeLocalDir {",
        "\t\t\t" + PLANT + "a directory costs money to read.\n\t\t\tif false && m.Type == transport.MediumTypeLocalDir {",
        "nothing is charged for reading it",
        SERVICE_PKG,
        "TestAScheduledRecoveryTestOfAFreeLocalDirMediumRuns",
    ),
    (
        "a recovery test that cannot reach a copy on a storage medium",
        SVC_RT_GO,
        "\tcase RecoveryLocalCopyCorrupt, RecoveryRemoteCopyCorrupt:",
        "\t" + PLANT + "corruption is not recognised.\n\tcase \"\":",
        "want a failure at the digest gate",
        SERVICE_PKG,
        "TestARecoveryTestOfAMediumCopyWithWrongBytesFailsAtTheDigestGate",
    ),
    (
        "an unattended recovery test that pays for a download",
        RT_GO,
        "\tack := req.AcknowledgeCost && !req.Scheduled",
        "\t" + PLANT + "a scheduled test carries the yes.\n\tack := req.AcknowledgeCost",
        "a scheduled test never pays",
        RT_PKG,
        "TestAScheduledTestOfAMediumCopyIsRefusedForCostAndNothingIsRead",
    ),
    (
        "a real recovery that says nothing about its stages",
        RECOVER_RUN_GO,
        "\t\ttrace.stage(ctx, \"reading\")\n",
        "\t\t// " + PLANT.rstrip() + "\n",
        "stages =",
        SERVICE_PKG,
        "TestARealRecoveryEmitsTheStageEventsTheUIRenders",
    ),
    (
        "a failed recovery that still says Complete",
        RECOVER_RUN_GO,
        "\t\tt.b.logger.RecoveryEnd(ctx, t.opID, t.set, t.artifact, 0, now().Sub(t.started), result, reason)",
        "\t\tt.b.logger.RecoveryStage(ctx, t.opID, t.set, \"complete\") // " + PLANT.rstrip() + "\n\t\tt.b.logger.RecoveryEnd(ctx, t.opID, t.set, t.artifact, 0, now().Sub(t.started), result, reason)",
        "reported the Complete stage",
        SERVICE_PKG,
        "TestAFailedRecoveryEndsWithAnErrorAndNeverSaysComplete",
    ),
    (
        "a configured staging root nothing reads",
        RECOVER_GO,
        "\t\troot := cfg.Assurance.StagingRoot\n",
        "\t\troot := \"\" // " + PLANT.rstrip() + "\n",
        "staging root =",
        SERVICE_PKG,
        "TestAssuranceHostKnobsReachTheEngine",
    ),
    (
        "a configured read ceiling nothing reads",
        RECOVER_GO,
        "\tlim.MaxConcurrentReads = smallerPositive(lim.MaxConcurrentReads, explicit(a.MaxConcurrentFullReads, a.MaxConcurrentFullReadsWritten, config.DefaultMaxConcurrentReads))",
        "\t_ = explicit // " + PLANT.rstrip(),
        "effective read ceilings",
        SERVICE_PKG,
        "TestAssuranceHostKnobsReachTheEngine",
    ),
    (
        "a staging root that fails custody and is written to anyway",
        RECOVER_GO,
        "\t\tif err := ValidateStagingRoot(root); err != nil {",
        "\t\t" + PLANT + "custody is not checked.\n\t\tif err := ValidateStagingRoot(root); err != nil && false {",
        "a world-writable staging root",
        SERVICE_PKG,
        "TestAStagingRootThatFailsCustody",
    ),
    (
        "a scrub that runs under its own read ceilings and not the settings'",
        APP_SCRUB_GO,
        "\t\t\tb.GlobalConcurrency = s.Config.Assurance.MaxConcurrentFullReads\n",
        "\t\t\t// " + PLANT.rstrip() + "\n",
        "admitted under assurance.max_concurrent_full_reads = 1",
        APP_PKG,
        "TestAssuranceHostKnobsReachTheScrubLimits",
    ),
    (
        "a route that starts a recovery test and is not registered",
        ROUTER_GO,
        '\t\tr.With(requireCSRF).Post("/backup-sets/{source}/{set}/recovery-test", h.startRecoveryTest)',
        "\t\t_ = h.startRecoveryTest // " + PLANT.rstrip(),
        "= 405",
        WEB_PKG,
        "TestAssuranceRoute_StartRecoveryTest",
    ),
    (
        "a scheduled pass that neither scheduler can reach",
        APP_SCRUB_GO,
        "// AssuranceTick is one scheduled assurance pass:",
        "// " + PLANT.rstrip() + "\n// NewPassTick is a pass nobody schedules.\nfunc (s *Service) NewPassTick(ctx context.Context) {}\n\n// AssuranceTick is one scheduled assurance pass:",
        "is a scheduled pass that AssuranceTick does not call",
        APP_PKG,
        "TestEveryEntryPointInAssuranceTickIsCalledByBothSchedulers",
    ),
    (
        "an alert kind nothing ever raises",
        ALERT_GO,
        '\tRecoveryTestFailing Kind = "RECOVERY_TEST_FAILING"\n',
        '\tRecoveryTestFailing Kind = "RECOVERY_TEST_FAILING"\n\n\t' + PLANT + 'a kind with a name, a title and no producer.\n\tUnraised Kind = "UNRAISED"\n',
        "the kind Unraised is produced by no function",
        ALERT_PKG,
        "TestEveryAlertKindHasAProducerAndEveryProducerHasACaller",
    ),
    (
        "an assurance event nothing emits",
        OBS_ASSURANCE_GO,
        "// validatorRef renders a validator reference for the feed.",
        "// " + PLANT.rstrip() + "\n// Dormant is an event with an emitter and no caller.\nfunc (l *Logger) Dormant() {}\n\n// validatorRef renders a validator reference for the feed.",
        "events nothing emits",
        OBS_PKG,
        "TestEveryAssuranceEventHasAProductionEmitter",
    ),
    (
        "a second recovery test of one set started while the first runs",
        SVC_RT_GO,
        "\tif cur := b.ops.running(opKindRecoveryTest, set.String()); cur != nil {",
        "\t" + PLANT + "any number at once.\n\tif cur := b.ops.running(opKindRecoveryTest, set.String()); cur != nil && false {",
        "already running",
        SERVICE_PKG,
        "TestStartRecoveryTestRunsOneAtATimePerSet",
    ),
    # Panel finding C10 (#988): the matrix said a default deployment scrubs and
    # recovery-tests on the strength of tests that call one tick by hand or only
    # import the package. These plant the break one hop out, in the process the
    # product actually runs, and the suites that go red are the ones that start
    # RunOnSchedule (what `retnd serve` runs) and read the `serve` command.
    (
        "a serving scheduler that never starts the scrub timer",
        SCHEDULER_GO,
        "\t\tb.runScrubTicks(ctx, b.PollInterval())\n",
        "\t\t" + PLANT + "the serving scheduler never starts the scrub timer.\n\t\t<-ctx.Done()\n",
        "nothing the serving process runs scrubs it",
        SERVICE_PKG,
        "TestRunOnScheduleScrubsADefaultInstall",
    ),
    (
        "a scheduled pass that drops the recovery tests",
        APP_SCRUB_GO,
        "\ts.ScrubTick(ctx)\n\ts.RecoveryTestTick(ctx)\n",
        "\ts.ScrubTick(ctx)\n\t" + PLANT + "the scheduled pass skips the recovery tests.\n",
        "nothing the serving process runs starts it",
        SERVICE_PKG,
        "TestRunOnScheduleRunsTheDueRecoveryTest",
    ),
    (
        "a scrub tick that gives up when an operation is running",
        SCHEDULER_GO,
        "\t\tb.tickScrubWhenFree(ctx)\n",
        "\t\t" + PLANT + "a busy tick is simply lost until the next interval.\n\t\t_ = b.tickScrub(ctx, true)\n",
        "scrub ticks that each arrive while a cycle holds the lock",
        SERVICE_PKG,
        "TestAScrubTickThatFindsTheLockHeldRunsAsSoonAsItIsFree",
    ),
    (
        "a serve command that hands the engine no scheduler",
        WEB_MAIN_GO,
        "\t\tscheduler = backend\n",
        "\t\t" + PLANT + "the process serves and never runs a scrub.\n\t\tscheduler = nil\n",
        "assigns a nil scheduler",
        SERVE_PKG,
        "TestServeHandsTheEngineAScheduler",
    ),
    (
        "a forced scrub that obeys an empty schedule",
        REVALIDATE_GO,
        "\tif cfg.MaxPerCycle <= 0 && !opts.Force {\n",
        "\t" + PLANT + "Force is ignored.\n\tif cfg.MaxPerCycle <= 0 {\n",
        "a forced pass selected 0 artifacts",
        REVALIDATE_PKG,
        "TestRunWith_AForcedPass",
    ),
    (
        "a placement upsert that lets any class move the content age",
        PLACEMENTS_GO,
        "\tif p.VerificationClass == VerificationContent && p.VerifiedAt != nil && status == PlacementActive {\n",
        "\t" + PLANT + "an existence write moves the content age.\n\tif p.VerificationClass != \"\" && p.VerifiedAt != nil && status == PlacementActive {\n",
        "an existence write moved the content age",
        STATE_PKG,
        "TestUpsertOfAnExistenceClassCannotMoveTheContentAge",
    ),
    # The activity feed is durable (migration 0018). Seven controls, one per
    # promise: it survives a restart, it does not store samples or per-range
    # lines, it coalesces stages, it prunes, it never blocks the cycle, and a
    # backwards page stays behind its cursor.
    (
        "an activity feed that forgets everything at a restart",
        "core/service/service.go",
        "\tactivity.hydrate(ctx, journal, configuredSetIDs(cfg))\n",
        "\t" + PLANT + "the rings are not refilled from the journal.\n\t_ = configuredSetIDs\n",
        "after a restart the feed holds",
        SERVICE_PKG,
        "TestTheFeedSurvivesARestartAndTheNumberingContinues",
    ),
    (
        "a periodic sample written to the journal",
        "core/service/liveactivity_durable.go",
        "\tif d == nil || activityNotStored[e.Event] {",
        "\t" + PLANT + "samples are stored like everything else.\n\tif d == nil || (false && activityNotStored[e.Event]) {",
        "rows stored for 150 samples",
        SERVICE_PKG,
        "TestPeriodicSamplesAndPerRangeDownloadLinesAreNotStored",
    ),
    (
        "a per-range download line written for every range",
        "core/service/liveactivity_durable.go",
        '\t\tif f.Key == "operation" && f.Value == "recovery range" {',
        "\t\t" + PLANT + "every range is stored.\n\t\tif false && f.Key == \"operation\" && f.Value == \"recovery range\" {",
        "rows stored for 150 samples",
        SERVICE_PKG,
        "TestPeriodicSamplesAndPerRangeDownloadLinesAreNotStored",
    ),
    (
        "a recovery whose every stage costs a row",
        "core/service/liveactivity_durable.go",
        "\t	Coalesce: it.e.Event == obs.EventRecoveryStage,",
        "\t\t" + PLANT + "stages are never replaced.\n\t\tCoalesce: false && it.e.Event == obs.EventRecoveryStage,",
        "stored 4 stage rows",
        SERVICE_PKG,
        "TestRecoveryStagesAreCoalescedToTheLastOneReached|TestACoalescedStageReplaces",
        [
            (
                "core/service/liveactivity_durable.go",
                '\t\tif it.e.Event == obs.EventRecoveryStage && it.e.ActionID != "" && lastStage[it.e.ActionID] != i {',
                "\t\t" + PLANT + "every stage in a batch is written.\n\t\tif false && it.e.Event == obs.EventRecoveryStage && it.e.ActionID != \"\" && lastStage[it.e.ActionID] != i {",
            )
        ],
    ),
    (
        "a download that stages the artifact again before streaming it (Q1.24)",
        RECOVER_STREAM_GO,
        "\treader := export.NewVerifiedReader(ctx, pf.src, pf.index, pf.plan.expect.Origin, pf.plan.expect.Medium)\n",
        "\t" + PLANT + "the download is staged as a whole file first.\n\t_, _ = b.stageGrant(ctx, g, trace)\n"
        "\treader := export.NewVerifiedReader(ctx, pf.src, pf.index, pf.plan.expect.Origin, pf.plan.expect.Medium)\n",
        "copied the artifact to the staging disk",
        SERVICE_PKG,
        "TestADownloadIsStreamedAndStagesNothing",
    ),
    (
        "a download that makes a whole-file temporary copy outside staging (Q1.24)",
        RECOVER_STREAM_GO,
        "\treader := export.NewVerifiedReader(ctx, pf.src, pf.index, pf.plan.expect.Origin, pf.plan.expect.Medium)\n",
        "\t" + PLANT + "the copy is read in full again, into a temp file nobody checks.\n"
        "\tif tmp, terr := os.CreateTemp(\"\", \"retnd-planted-\"); terr == nil {\n"
        "\t\tif rc, oerr := pf.src.OpenAt(ctx, 0); oerr == nil {\n\t\t\t_, _ = io.Copy(tmp, rc)\n\t\t\t_ = rc.Close()\n\t\t}\n"
        "\t\t_ = tmp.Close()\n\t\t_ = os.Remove(tmp.Name())\n\t}\n"
        "\treader := export.NewVerifiedReader(ctx, pf.src, pf.index, pf.plan.expect.Origin, pf.plan.expect.Medium)\n",
        "read the copy again before sending a byte",
        SERVICE_PKG,
        "TestThePreparationReadIsPaidOncePerArtifact",
    ),
    (
        "a recovery path that starts a process",
        RECOVER_STREAM_GO,
        "\treader := export.NewVerifiedReader(ctx, pf.src, pf.index, pf.plan.expect.Origin, pf.plan.expect.Medium)\n",
        "\t" + PLANT + "recovery shells out.\n\t_ = procseam.CommandContext(ctx, \"true\").Run()\n"
        "\treader := export.NewVerifiedReader(ctx, pf.src, pf.index, pf.plan.expect.Origin, pf.plan.expect.Medium)\n",
        "a process was spawned where none is allowed",
        SERVICE_PKG,
        "TestRecoverySpawnsNoProcessFromAnyPathItRuns",
        [
            (
                RECOVER_STREAM_GO,
                '\t"github.com/retnd/retnd/core/internal/obs"\n',
                '\t"github.com/retnd/retnd/core/internal/obs"\n\t"github.com/retnd/retnd/core/internal/procseam"\n',
            )
        ],
    ),
    (
        "activity retention that never prunes by age",
        "core/internal/state/activity_events.go",
        "(SELECT seq FROM activity_events WHERE at < ? ORDER BY at LIMIT ?)",
        "(SELECT seq FROM activity_events WHERE 0 AND at < ? ORDER BY at LIMIT ?)",
        "want 20 by age",
        SERVICE_PKG,
        "TestRetentionDropsWhatIsOldAndWhatIsOverTheRowBound",
    ),
    (
        "a durable writer that blocks the feed when it falls behind",
        "core/service/liveactivity_durable.go",
        "\tdefault:\n\t\td.dropped.Add(1)\n\t}",
        "\tdefault:\n\t\t" + PLANT + "the feed waits for the writer.\n\t\td.queue <- durableItem{e: e, setID: setID}\n\t}",
        "blocked on a full durable queue",
        SERVICE_PKG,
        "TestTheWriterFallingBehindDropsFromTheRecordAndNeverBlocksTheFeed",
    ),
    (
        "a backwards page that includes the cursor it was asked to stay behind",
        "core/internal/state/activity_events.go",
        "AND seq < ? ORDER BY seq DESC LIMIT ?`,",
        "AND seq <= ? ORDER BY seq DESC LIMIT ?`,",
        "at or after it",
        SERVICE_PKG,
        "TestBeforePagesBackwardsPastWhatTheRingHolds",
    ),
    (
        "a conditional GET answered 304 that reports the whole file served",
        HANDLER_GO,
        "\tif noBodyAsked(r, cw) {",
        "\t" + PLANT + "a 304 is accounted as a range.\n\tif false && noBodyAsked(r, cw) {",
        "a 304 sent no byte but told the service",
        WEB_PKG,
        "TestAConditionalGetAnswered304SpendsNothing",
    ),
    (
        "a request that wrote nothing counted as a range served",
        RECOVER_RUN_GO,
        "\tif err == nil && written == 0 && d.size > 0 {",
        "\t" + PLANT + "an empty success covers the artifact.\n\tif false {",
        "after a request that sent no byte",
        SERVICE_PKG,
        "TestARequestThatSentNoBytesSpendsNothingAndVerifiesNothing",
    ),
    (
        "a download that stops short with no interrupted row",
        RECOVER_RUN_GO,
        "\t\td.recordEnd(start, written, err)\n",
        "\t\t" + PLANT + "the end of a failed download is not recorded.\n",
        "left no interrupted row",
        SERVICE_PKG,
        "TestADownloadTheClientWalkedAway",
    ),
    (
        "a failed verification recorded as an interruption",
        RECOVER_STREAM_GO,
        "&& (re.Code == RecoveryLocalCopyCorrupt || re.Code == RecoveryRemoteCopyCorrupt) {",
        "&& false && (re.Code == RecoveryLocalCopyCorrupt || re.Code == RecoveryRemoteCopyCorrupt) { " + PLANT.rstrip(),
        "left no failed-verification row",
        SERVICE_PKG,
        "TestACopyThatFailedVerification",
    ),
    (
        "a UI proxy that keeps the engine streaming after the browser aborts",
        UI_GO,
        "\t\t\tpr.SetXForwarded()\n",
        "\t\t\t" + PLANT + "the browser's cancel stops at the proxy.\n"
        "\t\t\tpr.Out = pr.Out.WithContext(context.WithoutCancel(pr.In.Context()))\n"
        "\t\t\tpr.SetXForwarded()\n",
        "kept the engine streaming after the browser went away",
        UI_PKG,
        "TestUI_ABrowserThatAbortsMidDownloadStopsTheUpstreamStream",
        # The proxy also stops the engine another way: the failed write to
        # the gone browser closes the upstream body. That is defence in depth
        # working, so both are planted for this control.
        [
            (UI_GO, '\t"errors"\n', '\t"context"\n\t"errors"\n'),
            (
                UI_GO,
                "\t\tModifyResponse: func(res *http.Response) error {\n",
                "\t\tModifyResponse: func(res *http.Response) error {\n"
                "\t\t\t" + PLANT + "the upstream body is never closed.\n"
                "\t\t\tres.Body = struct {\n\t\t\t\tio.Reader\n\t\t\t\tio.Closer\n\t\t\t}{res.Body, io.NopCloser(nil)}\n",
            ),
        ],
    ),
    (
        "a scheduled pass that takes every due copy instead of deferring past the per-pass bound",
        REVALIDATE_SELECT_GO,
        "\tif len(due) > cfg.MaxPerCycle {",
        "\t" + PLANT + "the per-pass bound is never applied.\n\tif false && len(due) > cfg.MaxPerCycle {",
        "the per-pass bound is 50",
        REVALIDATE_PKG,
        "TestAThousandDueCopiesDefer",
    ),
    (
        "a scrub driver that runs more reads at once than the operator-facing ceiling",
        REVALIDATE_BOUNDS_GO,
        "\tDefaultGlobalConcurrency = config.DefaultMaxConcurrentReads\n",
        "\t" + PLANT + "eight reads at once.\n\tDefaultGlobalConcurrency = 8\n",
        "a default limiter admits 8",
        REVALIDATE_PKG,
        "TestAThousandDueCopiesDefer",
    ),
    (
        "validator output kept without its bound, so no runner-only truncation record",
        RT_OUTPUT_GO,
        "\t\tif len(out) >= maxRecords || total+len(text) > maxBytes {",
        "\t\t" + PLANT + "output is never cut.\n\t\tif false && (len(out) >= maxRecords || total+len(text) > maxBytes) {",
        "records, the bound is",
        RT_PKG,
        "TestBoundOutputFitsWhatTheJournalWillStore",
    ),
    (
        "a provider checksum mismatch that is refused and then forgotten",
        RECOVER_RUN_GO,
        "\t\tb.recordProviderFinding(ctx, plan, p)\n",
        "\t\t" + PLANT + "the finding is not written on the copy.\n",
        "after a positive finding of corruption",
        SERVICE_PKG,
        "TestAProviderChecksumMismatchIsRecordedOnTheCopy",
    ),
    (
        "a medium copy recorded as failing that reconcile reports as left alone",
        RECONCILE_GO,
        "\t\tf.NeedsInvestigation = true\n\t\treason := p.LastFailureReason",
        "\t\t" + PLANT + "the failure is not flagged.\n\t\treason := p.LastFailureReason",
        "does not flag a copy recorded as failing",
        RECONCILE_PKG,
        "TestReconcile_AMediumCopyRecordedAsFailing",
    ),
    (
        "two concurrent first downloads that each make the whole-copy pre-flight read",
        RECOVER_STREAM_GO,
        "\t\tif c, ok := d.building[key]; ok {",
        "\t\t" + PLANT + "an index in the making is never joined.\n\t\tif c, ok := d.building[key]; ok && false {",
        "which is the most one destination is asked for",
        SERVICE_PKG,
        "TestTwoConcurrentFirstDownloads",
    ),
    (
        "a second operator asking for a running recovery test refused instead of joined",
        SVC_RT_GO,
        "\t\tif cur.artifact != opts.ArtifactID || cur.ack != opts.AcknowledgeCost {",
        "\t\t" + PLANT + "an identical request is not joined.\n\t\tif true {",
        "a recovery test of this backup set is already running",
        SERVICE_PKG,
        "TestTwoOperatorsOnTheSameRecoveryTest",
    ),
    (
        "a cancel that tells the operator it is cancelling and stops nothing",
        ASSURANCE_OPS_GO,
        "\top.cancel(app.ErrOperationCancelled)\n\treturn AssuranceOperationCancelled",
        "\t" + PLANT + "the operation is never told.\n\treturn AssuranceOperationCancelled",
        "background work did not finish",
        SERVICE_PKG,
        "TestCancellingARecoveryTestAtEachStage/the_read",
    ),
    (
        "a default install whose download ceiling is not four",
        BUDGET_GO,
        "\tDefaultMaxDownloadStreams     = 4\n",
        "\t" + PLANT + "the ceiling is raised.\n\tDefaultMaxDownloadStreams     = 40\n",
        "the default ceiling is 40",
        SERVICE_PKG,
        "TestADefaultDeploymentRefusesTheFifth",
    ),
]

CLEAN_CONTROLS = [
    ("the budget proofs on the real drivers (#983)", REVALIDATE_PKG, "TestAThousandDueCopiesDefer|TestTheBacklogHarness"),
    ("the budget proofs on the service (#983)", SERVICE_PKG, "TestAProviderChecksumMismatch|TestTwoConcurrentFirstDownloads|TestADefaultDeployment|TestCancellingARecoveryTest|TestTwoOperatorsOnTheSameRecoveryTest"),
    ("the failing-copy reconcile finding (#983)", RECONCILE_PKG, "TestReconcile_AMediumCopy"),
    ("the recovery domain's own suite", EXPORT_PKG, "."),
    (
        "the recovery service suite",
        SERVICE_PKG,
        "TestAnIncrementalEngineSet|TestEveryMint|TestAFailedRange|TestGrantRefusals|TestRecoverALocalCopy",
    ),
    (
        "the recovery route suite",
        WEB_PKG,
        "TestARecoveryCompletes|TestMintCarries|TestMultipleRanges|TestADownloadSlower|TestAMintIsAudited|TestAGrantIsNever",
    ),
    ("the UI proxy's recovery path", UI_PKG, "TestUI_ARecovery|TestUI_ADownloadsGrant|TestUI_ABrowserThatAborts"),
    ("the recover verb suite", CLI_PKG, "TestRecoverIsRefusedBeforeAnythingIsOpened"),
    ("the budget package", BUDGET_PKG, "."),
    ("the host-limits contract", CAPS_PKG, "."),
    ("the deployment tripwire", DEPLOY_PKG, "."),
    (
        "the grant lifetime suite",
        SERVICE_PKG,
        "TestATailRange|TestRangesThat|TestAWholeFile|TestAFailedTail|TestAbandoned|TestRunOnScheduleStartsTheRecovery|TestEndingTheSession",
    ),
    (
        "the local session suite",
        AUTH_PKG,
        "TestAuthenticatorReportsSessionExpiry|TestLogoutTells|TestRotatingThePassword",
    ),
    ("the logout wiring suite", SERVE_PKG, "TestLogoutRevokes|TestServeHandsEveryBackend"),
    (
        "the recovery-test policy suite",
        APP_PKG,
        "TestRecoveryTest_|TestHealth_RequiredMode|TestAssuranceTick_|TestEveryEntryPoint|TestTheAssuranceTickGuard|TestRecoveryTestTick_|TestARecoveryTest|TestAScrubFailure|TestALapsed|TestAFailedRecoveryTest|TestAssuranceHostKnobsReachTheScrub|TestMediumFor_",
    ),
    ("the recovery-test runner suite", RT_PKG, "."),
    ("the recovery-test journal suite", STATE_PKG, "TestRecordRecovery|TestMarkRecoveryOverdue"),
    ("the copy opener", FSOPEN_PKG, "."),
    ("the alert vocabulary guard", ALERT_PKG, "TestEveryAlertKind|TestTheAlertProducerGuard"),
    ("the assurance event guard", OBS_PKG, "TestEveryAssuranceEvent|TestTheAssuranceEmitterGuard|TestTheScrubAndRecoveryTestEvents"),
    ("the default-deployment wiring suite", SERVICE_PKG, "TestRunOnSchedule"),
    ("the serve command's scheduler guard", SERVE_PKG, "TestServeHandsTheEngineAScheduler|TestTheServeSchedulerGuardCanFail"),
    ("the forced-pass contract", REVALIDATE_PKG, "TestRunWith_"),
    (
        "the recovery-test service suite",
        SERVICE_PKG,
        "TestAssuranceHostKnobs|TestADefaultDeployment|TestAStagingRoot|TestRecoveryTestOf|TestAScheduledRecovery|TestARecoveryTestOf|TestARealRecovery|TestAFailedRecovery|TestServeScheduler|TestAServeTick|TestStartRecoveryTest",
    ),
    ("the assurance route suite", WEB_PKG, "TestAssuranceRoute_StartRecoveryTest"),
    (
        "the durable activity feed suite",
        SERVICE_PKG,
        "TestTheFeedSurvivesARestart|TestRecoveryAndRecoveryTestEvents|TestPeriodicSamples|TestRecoveryStagesAreCoalesced|TestACoalescedStage|TestRetentionDrops|TestTheWriterFallingBehind|TestBeforePagesBackwards|TestAnOverlongMessage",
    ),
    (
        "the streamed download suite",
        SERVICE_PKG,
        "TestADownloadIsStreamed|TestADownloadReadsTheSource|TestTheETagIsTheSame|TestThePreparationReadIsPaid|TestPreflightRefuses|TestACopyThatRots|TestAnInterruptedDownloadResumes|TestACompleteDownloadRecords|TestAPartialDownloadClaims|TestADownloadTheClientWalked|TestACopyThatFailedVerification|TestOnlyAFinishedDownload|TestARequestThatSentNoBytes|TestRecoverySpawnsNoProcess",
    ),
    (
        "the streamed download route suite",
        WEB_PKG,
        "TestADownloadCarriesAStrongETag|TestIfRangeWithTheETag|TestAFirstChunkThatFails|TestAStreamThatFailsMidBody|TestAClientThatGoesAwayMidDownload|TestAConditionalGet|TestAHeadRequest",
    ),
]


def body(root: Path, dry_run: bool) -> int:
    tally = selftest_swap.Tally()

    with tempfile.TemporaryDirectory(prefix="retnd-recovery-selftest.") as tmp_name:
        tmp = Path(tmp_name)
        tracker = selftest_swap.AnchorTracker(dry_run=dry_run, root=root, tmp=tmp)

        # RECOVERY_SELFTEST_ONLY=<substring> runs only the controls whose label
        # contains it (and no negative controls), for re-running the control
        # you just wrote. Unset, everything runs, and that is the gate.
        only = os.environ.get("RECOVERY_SELFTEST_ONLY", "")

        print("==> negative controls: each slice is clean on the real tree")
        for label, where, pattern in ([] if only else CLEAN_CONTROLS):
            expect_passes(tracker, tally, f"{label} on an unmutated tree", root, where, pattern)

        for i, entry in enumerate(MUTATIONS):
            label, rel, old, new, needle, where, pattern = entry[:7]
            if only and only not in label:
                continue
            extras = entry[7] if len(entry) > 7 else []
            print()
            print(f"==> {label}")
            d = selftest_swap.mutant(root, tmp, f"recovery-{i:02d}", dry_run=dry_run)
            tracker.swap(d / rel, old, new)
            for extra_rel, extra_old, extra_new in extras:
                tracker.swap(d / extra_rel, extra_old, extra_new)
            expect_fails(tracker, tally, label, d, needle, where, pattern)

        tracker.stale_summary()
        stale_count = tracker.stale_count
        anchors_checked = tracker.anchors_checked

    total = tally.passed + tally.failed + stale_count
    if tally.failed or stale_count:
        print(file=sys.stderr)
        print(
            f"FAIL: {tally.failed + stale_count} of {total} recovery controls did not behave as required "
            f"({tally.failed} reached the wrong verdict, {stale_count} could not plant their violation at all).",
            file=sys.stderr,
        )
        return harness.EXIT_FAILED

    print()
    if dry_run:
        print(f"OK: all {anchors_checked} recovery mutation anchors still name code that is in this tree.")
    else:
        print(
            f"OK: all {tally.passed} recovery controls behaved as required (every guard was shown to go red "
            "against a real planted violation, and shown not to on the real tree)."
        )
    return harness.EXIT_OK


def main(argv: list[str]) -> int:
    harness.set_program(PROGRAM)
    harness.install_signal_handlers()
    dry_run = selftest_swap.parse_selftest_args(argv, "selftest.py")
    root = harness.repo_root(Path(__file__))
    return harness.finish(lambda: body(root, dry_run))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
