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

# (module directory under the tree root, package patterns)
EXPORT_PKG = ("core", ("./internal/recovery/export/",))
SERVICE_PKG = ("core", ("./service/",))
WEB_PKG = ("apps/common", ("./webhost/",))
UI_PKG = ("apps/common", ("./webhost/serve/",))
CLI_PKG = ("core", ("./cmd/retnd/",))
AUTH_PKG = ("apps/common", ("./auth/local/",))
SERVE_PKG = ("apps/generic", ("./cmd/retnd-web/",))

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
        "\tif filepath.Dir(from) != filepath.Clean(s.Root) || !isPartial(filepath.Base(from)) {",
        "\t" + PLANT + "any path may be moved into place.\n\tif false {",
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
        "\td.b.auditRecovery(context.Background(), &ev, err)\n\tif err != nil {\n\t\td.b.finishRecoveryDownload(d.token)\n\t\treturn\n\t}",
        "\t" + PLANT + "a served range is not audited.\n\t_ = &ev\n\tif err != nil {\n\t\td.b.finishRecoveryDownload(d.token)\n\t\treturn\n\t}",
        "should write 52 rows",
        SERVICE_PKG,
        "TestEveryMintAndEveryServedRange",
    ),
    (
        "a download whose grant is not spent when the client goes away",
        RECOVER_RUN_GO,
        "\tif err != nil {\n\t\td.b.finishRecoveryDownload(d.token)\n\t\treturn\n\t}\n\tif d.b.recoveryHost().grants.Served(d.token, start, end, d.size) {",
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
        'r.With(requireCSRF).Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        'r.With(requireCSRF, requireDestructiveGate(gate)).Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        "mint = ",
        WEB_PKG,
        "TestARecoveryCompletesWhileTheDestructiveGateIsShut",
    ),
    (
        "a recovery mint with no CSRF check",
        ROUTER_GO,
        'r.With(requireCSRF).Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
        'r.Post("/backups/{source}/{set}/{name}/recovery", h.mintRecovery)',
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
        "\t\tif !fi.Mode().IsRegular() {\n\t\t\treturn nil, recoveryErr(RecoveryNotReadable,",
        "\t\t" + PLANT + "any file type is opened.\n\t\tif false && !fi.Mode().IsRegular() {\n\t\t\treturn nil, recoveryErr(RecoveryNotReadable,",
        "must be refused",
        SERVICE_PKG,
        "TestALocalCopyThatIsASymlink",
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
        "a recover verb that takes no explicit target",
        CLI_GO,
        '\tif *to == "" {',
        "\t" + PLANT + "no --to is fine.\n\tif false {",
        "--to is required",
        CLI_PKG,
        "TestRecoverIsRefusedBeforeAnythingIsOpened",
    ),
    (
        "a download that stages the artifact again before streaming it (Q1.24)",
        RECOVER_STREAM_GO,
        "\treader := export.NewVerifiedReader(ctx, src, ix, plan.expect.Origin, plan.expect.Medium)\n",
        "\t" + PLANT + "the download is staged as a whole file first.\n\t_, _ = b.stageGrant(ctx, g)\n"
        "\treader := export.NewVerifiedReader(ctx, src, ix, plan.expect.Origin, plan.expect.Medium)\n",
        "copied the artifact to the staging disk",
        SERVICE_PKG,
        "TestADownloadIsStreamedAndStagesNothing",
    ),
    (
        "a download that makes a whole-file temporary copy outside staging (Q1.24)",
        RECOVER_STREAM_GO,
        "\treader := export.NewVerifiedReader(ctx, src, ix, plan.expect.Origin, plan.expect.Medium)\n",
        "\t" + PLANT + "the copy is read in full again, into a temp file nobody checks.\n"
        "\tif tmp, terr := os.CreateTemp(\"\", \"retnd-planted-\"); terr == nil {\n"
        "\t\tif rc, oerr := src.OpenAt(ctx, 0); oerr == nil {\n\t\t\t_, _ = io.Copy(tmp, rc)\n\t\t\t_ = rc.Close()\n\t\t}\n"
        "\t\t_ = tmp.Close()\n\t\t_ = os.Remove(tmp.Name())\n\t}\n"
        "\treader := export.NewVerifiedReader(ctx, src, ix, plan.expect.Origin, plan.expect.Medium)\n",
        "read the copy again before sending a byte",
        SERVICE_PKG,
        "TestThePreparationReadIsPaidOncePerArtifact",
    ),
    (
        "a recovery path that starts a process",
        RECOVER_STREAM_GO,
        "\treader := export.NewVerifiedReader(ctx, src, ix, plan.expect.Origin, plan.expect.Medium)\n",
        "\t" + PLANT + "recovery shells out.\n\t_ = procseam.CommandContext(ctx, \"true\").Run()\n"
        "\treader := export.NewVerifiedReader(ctx, src, ix, plan.expect.Origin, plan.expect.Medium)\n",
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
]

CLEAN_CONTROLS = [
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

        print("==> negative controls: each slice is clean on the real tree")
        for label, where, pattern in CLEAN_CONTROLS:
            expect_passes(tracker, tally, f"{label} on an unmutated tree", root, where, pattern)

        for i, entry in enumerate(MUTATIONS):
            label, rel, old, new, needle, where, pattern = entry[:7]
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
