/**
 * Issue #985 (and #1070's sibling work on the same page): the operator
 * controls the browser already drew, Scrub now, Cancel and Show validator
 * output, wired to the routes that exist now, plus the dedicated renderings
 * of the events those controls produce.
 *
 * Everything here runs against the PRODUCTION composition
 * (`withHttpAssurance` over a client), never the mock's assurance seam, since
 * the mock is where a refusal used to look like a working button.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { RetndError } from "@shared/api/contracts";
import type { RecoveryTestOutput, RetndApi } from "@shared/api/contracts";
import { AssuranceSection } from "@shared/assurance/AssuranceSection";
import { createHttpAssurance, withHttpAssurance } from "@shared/assurance/realAssurance";
import { ValidatorLog } from "@shared/assurance/ValidatorLog";
import { stageLabel } from "@shared/assurance/stages";
import { activityLine, logText, UnfinishedActionsNotice } from "@shared/pages/ActivityStrip";
import { DashboardActivity } from "@shared/pages/DashboardActivity";
import { SetActivityPanel } from "@shared/pages/SetActivityPanel";
import { resetGraphForTests } from "@shared/state/graph";
import { TOOLTIPS_KEY, setTooltipsEnabled } from "@shared/state/tooltipNodes";
import { TOOLTIPS } from "@shared/tooltips/tooltips";
import { TooltipOptOutDialog } from "@shared/components/TooltipOptOutDialog";
import type { SetActivity, SetActivityEvent } from "@shared/types/activity";
import recorded from "../../../../core/cmd/retnd/testdata/recovery-follow-feed.json";

const SET = "production/postgres-primary";
const T = "2026-10-09T03:00:00Z";

const refusal = (code: string, message: string, status: number) =>
  new RetndError({ code: code as never, message, status, correlationId: "cid" });

const emptyLive = () => ({ observedAt: T, epoch: "e", pollAfterMs: 1000, deployment: null, sets: [] });

const ev = (sequence: number, event: string, fields: Record<string, string>, result?: SetActivityEvent["result"], level: SetActivityEvent["level"] = "info"): SetActivityEvent => ({
  sequence, at: T, level, event, scope: "set", message: event, fields, ...(result ? { result } : {})
});
const top = (events: SetActivityEvent[]) => events.reduce((m, e) => Math.max(m, e.sequence), 0);
const live = (events: SetActivityEvent[], over: { open?: boolean; deployment?: SetActivityEvent[]; opId?: string } = {}) => ({
  observedAt: T, epoch: "e", pollAfterMs: 1000,
  deployment: over.deployment ? { events: over.deployment, truncated: false, dropped: false, oldestSequence: 1, latestSequence: top(over.deployment) } : null,
  sets: [{
    setId: SET, events, truncated: false, dropped: false, oldestSequence: 1, latestSequence: top(events),
    unfinishedActions: over.open ? [{ action: "recovery_test", actionId: over.opId ?? "op-1", startedAt: T, sequence: 1 }] : []
  } as unknown as SetActivity]
});

const output = (over: Partial<RecoveryTestOutput> = {}): RecoveryTestOutput => ({
  backupSetId: SET, recorded: true, operationId: "op-1", artifactId: SET + "/postgres-prod-20260828.dump.zst",
  medium: "local", state: "failed", recordedAt: T, truncated: false, records: [{ kind: "output", text: "validate: ok" }], ...over
});

const rootWith = (over: Partial<RetndApi> = {}) => ({ ...createMockApi("assurance-unavailable"), ...over }) as RetndApi;

describe("the production client offers the operator controls", () => {
  it("offers operations, because the three routes exist", () => {
    expect(createHttpAssurance(rootWith()).offers.operations).toBe(true);
  });

  it("Scrub now calls POST .../scrub and follows the operation id it returns", async () => {
    const startScrub = vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "scrub-op-1" });
    const a = createHttpAssurance(rootWith({ startScrub, getLiveActivity: vi.fn().mockResolvedValue(emptyLive()) } as never));
    const op = await a.startScrub(SET);
    expect(startScrub).toHaveBeenCalledWith("production", "postgres-primary");
    expect(op).toMatchObject({ id: "scrub-op-1", kind: "scrub", state: "running", setId: SET });
  });

  it("a scrub that is already running, a disabled set and an unknown set reach the caller as the service's own refusal", async () => {
    for (const [code, status] of [["OPERATION_ALREADY_RUNNING", 409], ["INVALID_REQUEST", 400], ["BACKUP_SET_NOT_FOUND", 404]] as const) {
      const startScrub = vi.fn().mockRejectedValue(refusal(code, "refused: " + code, status));
      const a = createHttpAssurance(rootWith({ startScrub, getLiveActivity: vi.fn().mockResolvedValue(emptyLive()) } as never));
      await expect(a.startScrub(SET)).rejects.toMatchObject({ api: { code, status } });
    }
  });

  it("Cancel calls POST /assurance/operations/{op}/cancel", async () => {
    const cancelAssuranceOperation = vi.fn().mockResolvedValue({ operationId: "scrub-op-1", kind: "scrub", backupSetId: SET, status: "cancelling" });
    const a = createHttpAssurance(rootWith({ cancelAssuranceOperation } as never));
    await a.cancelOperation("scrub-op-1");
    expect(cancelAssuranceOperation).toHaveBeenCalledWith("scrub-op-1");
  });

  it("a cancel of an operation that already ended is the service's 404, not a pretend success", async () => {
    const cancelAssuranceOperation = vi.fn().mockRejectedValue(refusal("OPERATION_NOT_FOUND", "no such running operation", 404));
    const a = createHttpAssurance(rootWith({ cancelAssuranceOperation } as never));
    await expect(a.cancelOperation("gone")).rejects.toMatchObject({ api: { code: "OPERATION_NOT_FOUND" } });
  });

  it("joining a test that was already running says so, and follows the running one", async () => {
    const startRecoveryTest = vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-running", coalesced: true });
    const a = createHttpAssurance(rootWith({ startRecoveryTest }));
    const op = await a.startRecoveryTest(SET);
    expect(op).toMatchObject({ id: "op-running", joined: true });
    const fresh = createHttpAssurance(rootWith({ startRecoveryTest: vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-2", coalesced: false }) }));
    expect((await fresh.startRecoveryTest(SET)).joined).toBeFalsy();
  });
});

describe("the validator output is read from GET .../recovery-test/output", () => {
  it("asks for the set's output and keeps the records in order, branching on kind", async () => {
    const getRecoveryTestOutput = vi.fn().mockResolvedValue(output({
      truncated: true,
      records: [{ kind: "output", text: "line one" }, { kind: "output", text: "[output truncated]" }, { kind: "truncation", text: "output truncated: 41873 bytes dropped" }]
    }));
    const a = createHttpAssurance(rootWith({ getRecoveryTestOutput } as never));
    const got = await a.getValidatorResult(SET);
    expect(getRecoveryTestOutput).toHaveBeenCalledWith("production", "postgres-primary");
    expect(got).toMatchObject({ artifactId: SET + "/postgres-prod-20260828.dump.zst", medium: "local", state: "failed", truncated: true });
    expect(got?.records.map((r) => r.kind)).toEqual(["output", "output", "truncation"]);
  });

  it("recorded=false is no result yet (null), never an empty log that looks like a silent pass", async () => {
    const getRecoveryTestOutput = vi.fn().mockResolvedValue(output({
      recorded: false, operationId: "", artifactId: "", medium: "", state: "" as never, recordedAt: "", records: []
    }));
    const a = createHttpAssurance(rootWith({ getRecoveryTestOutput } as never));
    expect(await a.getValidatorResult(SET)).toBeNull();
  });
});

describe("ValidatorLog", () => {
  afterEach(() => cleanup());
  const result = (records: Array<{ kind: "output" | "truncation"; text: string }>, truncated = false) => ({
    artifactId: SET + "/a.dump", medium: "local", operationId: "op-1", state: "failed" as const, recordedAt: T, truncated, records
  });

  it("draws control bytes as visible escapes and never as markup", () => {
    render(<ValidatorLog result={result([{ kind: "output", text: "a\u001b]0;pwned\u0007b <b>bold</b>" }])} />);
    const log = screen.getByTestId("validator-log");
    expect(log.textContent).toContain("a\\x1b]0;pwned\\x07b <b>bold</b>");
    expect(log.querySelector("b")).toBeNull();
  });

  it("a validator that prints the marker's own words is still output; only the truncation record is the marker", () => {
    render(<ValidatorLog result={result([
      { kind: "output", text: "[output truncated]" },
      { kind: "truncation", text: "output truncated: 41873 bytes were dropped before this point" }
    ], true)} />);
    expect(screen.getByTestId("validator-log").textContent).toContain("[output truncated]");
    const marker = screen.getByTestId("truncation-marker");
    expect(marker.textContent).toContain("41873");
    expect(screen.getByTestId("validator-log").contains(marker)).toBe(false);
    expect(screen.getAllByTestId("truncation-marker")).toHaveLength(1);
  });

  it("no truncation record means no marker, even if the output says it was truncated", () => {
    render(<ValidatorLog result={result([{ kind: "output", text: "output truncated: 5 bytes dropped" }])} />);
    expect(screen.queryByTestId("truncation-marker")).toBeNull();
  });

  it("an empty record list says the validator printed nothing, in words", () => {
    render(<ValidatorLog result={result([])} />);
    expect(screen.getByTestId("validator-empty").textContent).toMatch(/printed nothing/i);
  });

  it("names the recorded state in words, not only the log", () => {
    render(<ValidatorLog result={result([{ kind: "output", text: "x" }])} />);
    expect(screen.getByTestId("validator-facts").textContent).toMatch(/failed/i);
    expect(screen.getByTestId("validator-facts").textContent).toContain("local");
  });
});

describe("following a scrub, whose operation id is not on the feed", () => {
  const scrubPass = (sequence: number, fields: Record<string, string>, result: SetActivityEvent["result"] = "success") =>
    ev(sequence, "scrub_pass", { trigger: "manual", scope: "set " + SET, selected: "2", checked: "2", passed: "2", failed: "0", could_not_complete: "0", requires_restore: "0", skipped_held: "0", skipped_leased: "0", bytes_read: "1024", ...fields }, result, result === "warn" ? "warn" : "info");

  function scrubApi(getLiveActivity: ReturnType<typeof vi.fn>) {
    return createHttpAssurance(rootWith({
      startScrub: vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "scrub-op-1" }), getLiveActivity
    } as never));
  }

  it("is complete when this set's manual scrub_pass arrives after the start", async () => {
    const getLiveActivity = vi.fn()
      .mockResolvedValueOnce(live([ev(5, "cycle_start", {})]))
      .mockResolvedValueOnce(live([], { deployment: [scrubPass(6, {})] }));
    const a = scrubApi(getLiveActivity);
    await a.startScrub(SET);
    const read = await a.readOperation("scrub-op-1", 0);
    expect(read.operation).toMatchObject({ kind: "scrub", state: "complete" });
    expect(read.events.some((e) => /scrub pass/.test(e.text))).toBe(true);
  });

  it("is failed when the pass found a copy wrong, and the failing copy is one of the lines", async () => {
    const getLiveActivity = vi.fn()
      .mockResolvedValueOnce(live([]))
      .mockResolvedValueOnce(live([], {
        deployment: [
          ev(6, "scrub_failure", { artifact: SET + "/a.dump", medium: "local", class: "content", reason: "sha256 differs from the one recorded at ingest" }, "warn", "warn"),
          scrubPass(7, { failed: "1", passed: "1" }, "warn")
        ]
      }));
    const a = scrubApi(getLiveActivity);
    await a.startScrub(SET);
    const read = await a.readOperation("scrub-op-1", 0);
    expect(read.operation.state).toBe("failed");
    expect(read.events.map((e) => e.text).join("\n")).toContain("sha256 differs from the one recorded at ingest");
  });

  it("ignores another set's pass and any pass a schedule wrote", async () => {
    const getLiveActivity = vi.fn()
      .mockResolvedValueOnce(live([]))
      .mockResolvedValueOnce(live([], {
        deployment: [scrubPass(6, { scope: "set other/set" }), scrubPass(7, { trigger: "schedule" }), scrubPass(8, { trigger: "schedule", scope: "all due" })]
      }));
    const a = scrubApi(getLiveActivity);
    await a.startScrub(SET);
    expect((await a.readOperation("scrub-op-1", 0)).operation.state).toBe("running");
  });

  it("a pass from before the start is not this scrub's", async () => {
    const getLiveActivity = vi.fn()
      .mockResolvedValueOnce(live([], { deployment: [scrubPass(3, {})] }))
      .mockResolvedValueOnce(live([], { deployment: [scrubPass(3, {})] }));
    const a = scrubApi(getLiveActivity);
    await a.startScrub(SET);
    expect((await a.readOperation("scrub-op-1", 0)).operation.state).toBe("running");
  });

  it("says untracked, not complete, once nothing has been seen for longer than a pass with nothing due would take", async () => {
    vi.useFakeTimers();
    try {
      const getLiveActivity = vi.fn().mockResolvedValue(live([]));
      const a = scrubApi(getLiveActivity);
      await a.startScrub(SET);
      vi.setSystemTime(Date.now() + 10 * 60_000);
      expect((await a.readOperation("scrub-op-1", 0)).operation.state).toBe("untracked");
    } finally { vi.useRealTimers(); }
  });
});

describe("cancelling a recovery test", () => {
  const start = (extra: Partial<RetndApi> = {}) => rootWith({
    startRecoveryTest: vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-1", coalesced: false }),
    cancelAssuranceOperation: vi.fn().mockResolvedValue({ operationId: "op-1", kind: "recovery_test", backupSetId: SET, status: "cancelling" }),
    ...extra
  } as never);

  it("a warn end after a cancel is Cancelled, and it never reaches a pass", async () => {
    const getLiveActivity = vi.fn().mockResolvedValue(live([
      ev(1, "recovery_test_start", { op_id: "op-1" }),
      ev(2, "recovery_test_end", { op_id: "op-1", reason: "cancelled before it finished; no result was recorded" }, "warn", "warn")
    ]));
    const a = createHttpAssurance(start({ getLiveActivity }));
    await a.startRecoveryTest(SET);
    await a.cancelOperation("op-1");
    const read = await a.readOperation("op-1", 0);
    expect(read.operation.state).toBe("cancelled");
    expect(read.events[read.events.length - 1].text).toMatch(/cancelled/i);
  });

  it("between the accepted cancel and the end line the operation says it is cancelling", async () => {
    const getLiveActivity = vi.fn().mockResolvedValue(live([ev(1, "recovery_test_start", { op_id: "op-1" })], { open: true }));
    const a = createHttpAssurance(start({ getLiveActivity }));
    await a.startRecoveryTest(SET);
    await a.cancelOperation("op-1");
    const read = await a.readOperation("op-1", 0);
    expect(read.operation).toMatchObject({ state: "running", cancelling: true });
  });

  it("a warn end nobody asked for is not called cancelled", async () => {
    const getLiveActivity = vi.fn().mockResolvedValue(live([
      ev(1, "recovery_test_start", { op_id: "op-1" }),
      ev(2, "recovery_test_end", { op_id: "op-1", reason: "the endpoint did not answer" }, "warn", "warn")
    ]));
    const a = createHttpAssurance(start({ getLiveActivity }));
    await a.startRecoveryTest(SET);
    const read = await a.readOperation("op-1", 0);
    expect(read.operation.state).not.toBe("cancelled");
    expect(read.operation.state).not.toBe("complete");
    expect(read.events[read.events.length - 1].text).toContain("the endpoint did not answer");
  });
});

describe("an operation this page did not start (a reload, or a link to its detail)", () => {
  it("is found on the live feed by its op_id, with its set and kind", async () => {
    const getLiveActivity = vi.fn().mockResolvedValue(live([
      ev(1, "recovery_test_start", { op_id: "op-9" }),
      ev(2, "recovery_stage", { op_id: "op-9", stage: "reading", stage_label: "Reading" })
    ], { open: true, opId: "op-9" }));
    const a = createHttpAssurance(rootWith({ getLiveActivity }));
    const read = await a.readOperation("op-9", 0);
    expect(read.operation).toMatchObject({ id: "op-9", kind: "recovery_test", setId: SET, state: "running", stage: "reading" });
  });

  it("a scrub id, which is never on the feed, is refused in words rather than followed into silence", async () => {
    const a = createHttpAssurance(rootWith({ getLiveActivity: vi.fn().mockResolvedValue(live([])) }));
    await expect(a.readOperation("scrub-op-1", 0)).rejects.toThrow(/scrub/i);
  });
});

describe("the terminal and the browser follow the same stages in the same order", () => {
  // One recorded engine feed, written by the Go test that holds the CLI's half
  // (core/cmd/retnd/recoverfollow_test.go). Both halves are held against it.
  const asLines = (op: { events: Array<{ event: string; stage?: string; stage_label?: string; result?: string }> }, opId: string): SetActivityEvent[] =>
    op.events.map((e, i) => ({
      sequence: i + 1, at: T, level: e.result === "warn" ? "warn" : "info", event: e.event, scope: "set", message: e.event,
      ...(e.result ? { result: e.result as SetActivityEvent["result"] } : {}),
      fields: { op_id: opId, ...(e.stage ? { stage: e.stage } : {}), ...(e.stage_label ? { stage_label: e.stage_label } : {}) }
    }));

  it("the activity strip renders a recovery's stage lines as the recorded stages, in order", () => {
    const lines = asLines(recorded.recover, "rec-1");
    const rendered = lines.filter((l) => l.event === "recovery_stage").map((l) => activityLine(l).text);
    expect(rendered).toHaveLength(recorded.recover.stages.length);
    // Held against the stage ids, not against the feed's own labels, so a
    // label the engine and this build disagree on would show here too.
    recorded.recover.stages.forEach((stage, i) => {
      expect(rendered[i]).toBe("recovery stage: " + stageLabel(stage));
    });
  });

  it("the page's follow of a recovery test reports the recorded stage ids in order", async () => {
    const lines = asLines(recorded.recovery_test, "op-1");
    const a = createHttpAssurance(rootWith({
      startRecoveryTest: vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-1", coalesced: false }),
      getLiveActivity: vi.fn().mockResolvedValue(live(lines))
    }));
    await a.startRecoveryTest(SET);
    const read = await a.readOperation("op-1", 0);
    const stages = read.events.filter((e) => e.kind === "stage").map((e) => e.stage);
    expect(stages).toEqual(recorded.recovery_test.stages);
  });

  it("the page's follow of a recovery reports the recorded stage ids in order", async () => {
    const lines = asLines(recorded.recover, "rec-1");
    const a = createHttpAssurance(rootWith({ getLiveActivity: vi.fn().mockResolvedValue(live(lines)) }));
    const read = await a.readOperation("rec-1", 0);
    expect(read.operation.kind).toBe("recovery");
    const stages = read.events.filter((e) => e.kind === "stage").map((e) => e.stage);
    expect(stages).toEqual(recorded.recover.stages);
    expect(stages).toEqual(["resolving_copy", "reading", "verifying", "writing_target", "cleaning_temporary_files", "complete"]);
    expect(read.operation.state).toBe("complete");
  });

  it("control: a feed with two stages swapped is NOT reported as the recorded order", async () => {
    const lines = asLines(recorded.recover, "rec-1");
    const swapped = lines.map((l) => ({ ...l }));
    const [i, j] = [2, 3];
    [swapped[i].fields, swapped[j].fields] = [swapped[j].fields, swapped[i].fields];
    const a = createHttpAssurance(rootWith({ getLiveActivity: vi.fn().mockResolvedValue(live(swapped)) }));
    const stages = (await a.readOperation("rec-1", 0)).events.filter((e) => e.kind === "stage").map((e) => e.stage);
    expect(stages).not.toEqual(recorded.recover.stages);
  });
});

describe("dedicated renderings of the events the controls produce", () => {
  const f = (event: string, fields: Record<string, string>, result?: SetActivityEvent["result"], level: SetActivityEvent["level"] = "info") =>
    activityLine(ev(1, event, fields, result, level));
  const NOT_FALLBACK = /(^|\s)(op_id|trigger|scope|selected|stage|stage_label|backup_set|artifact|medium|class|reason|validator)=/;

  it("scrub_pass: a clean pass is a clean pass, with its counts in words", () => {
    const l = f("scrub_pass", { trigger: "manual", scope: "set " + SET, selected: "3", checked: "3", passed: "3", failed: "0", could_not_complete: "0", requires_restore: "0", skipped_held: "0", skipped_leased: "0", bytes_read: "2048" }, "success");
    expect(l.text).toMatch(/^scrub pass finished/);
    expect(l.text).toContain("3 checked");
    expect(l.text).toContain("3 passed");
    expect(l.text).not.toMatch(NOT_FALLBACK);
    expect(l.tone).toBe("ok");
  });

  it("scrub_pass: a pass that found a copy wrong says so and is not green", () => {
    const l = f("scrub_pass", { trigger: "schedule", scope: "all due", selected: "3", checked: "3", passed: "2", failed: "1", could_not_complete: "0", requires_restore: "0", skipped_held: "0", skipped_leased: "0", bytes_read: "0" }, "warn", "warn");
    expect(l.text).toMatch(/found problems/);
    expect(l.text).toContain("1 failed");
    expect(l.tone).toBe("warn");
  });

  it("scrub_pass: a pass that only skipped copies says skipped, which is not a pass either", () => {
    const l = f("scrub_pass", { trigger: "manual", scope: "set " + SET, selected: "1", checked: "0", passed: "0", failed: "0", could_not_complete: "0", requires_restore: "0", skipped_held: "1", skipped_leased: "0", bytes_read: "0" }, "warn", "warn");
    expect(l.text).toMatch(/skipped/);
    expect(l.text).not.toMatch(/^scrub pass finished: /);
    expect(l.tone).toBe("warn");
  });

  it("scrub_failure: names the copy, the medium, the check and the reason", () => {
    const l = f("scrub_failure", { artifact: SET + "/a.dump", medium: "offsite", class: "content", reason: "sha256 differs from the one recorded at ingest" }, "warn", "warn");
    expect(l.text).toContain("a.dump");
    expect(l.text).toContain("offsite");
    expect(l.text).toContain("sha256 differs from the one recorded at ingest");
    expect(l.text).not.toMatch(NOT_FALLBACK);
    expect(l.tone).toBe("warn");
  });

  it("recovery_stage: the engine's label, for a recovery and for a recovery test", () => {
    const rec = f("recovery_stage", { op_id: "r", stage: "writing_target", stage_label: "Writing target" });
    expect(rec.text).toBe("recovery stage: Writing target");
    const test = activityLine({ ...ev(1, "recovery_stage", { op_id: "t", stage: "validating", stage_label: "Validating" }), action: "recovery_test", actionId: "t" });
    expect(test.text).toBe("recovery test stage: Validating");
    const unknown = f("recovery_stage", { op_id: "r", stage: "brand_new_stage", stage_label: "" });
    expect(unknown.text).toContain("brand_new_stage");
    expect(rec.text).not.toMatch(NOT_FALLBACK);
  });

  it("recovery_start and recovery_end: success is green, a refusal is amber, a failure is red", () => {
    expect(f("recovery_start", { op_id: "r", backup_set: SET, artifact: SET + "/a.dump" }).text).toBe("recovery started: a.dump");
    const ok = f("recovery_end", { op_id: "r", artifact: SET + "/a.dump", bytes: "4800000", duration: "1.5s" }, "success");
    expect(ok.text).toMatch(/^recovery finished: a\.dump/);
    expect(ok.tone).toBe("ok");
    const warn = f("recovery_end", { op_id: "r", artifact: SET + "/a.dump", bytes: "0", duration: "1s", reason: "RECOVERY_COST_NOT_ACKNOWLEDGED" }, "warn", "warn");
    expect(warn.text).toMatch(/^recovery stopped: a\.dump/);
    expect(warn.text).toContain("RECOVERY_COST_NOT_ACKNOWLEDGED");
    expect(warn.tone).toBe("warn");
    const bad = f("recovery_end", { op_id: "r", artifact: SET + "/a.dump", bytes: "0", duration: "1s", reason: "the recovery failed" }, "error", "error");
    expect(bad.text).toMatch(/^recovery failed: a\.dump/);
    expect(bad.tone).toBe("error");
    expect(ok.text).not.toMatch(NOT_FALLBACK);
  });

  it("recovery_test_start and recovery_test_end: passed, did not complete (a cancel), failed", () => {
    expect(f("recovery_test_start", { op_id: "t", artifact: SET + "/a.dump", validator: "pg-check" }).text).toBe("recovery test started: a.dump (validator pg-check)");
    const pass = f("recovery_test_end", { op_id: "t", artifact: SET + "/a.dump", validator: "pg-check", duration: "9s" }, "success");
    expect(pass.text).toMatch(/^recovery test passed: a\.dump/);
    expect(pass.tone).toBe("ok");
    const cancelled = f("recovery_test_end", { op_id: "t", artifact: SET + "/a.dump", validator: "pg-check", duration: "2s", reason: "cancelled before it finished; no result was recorded" }, "warn", "warn");
    expect(cancelled.text).toMatch(/^recovery test did not complete: a\.dump/);
    expect(cancelled.text).toContain("cancelled before it finished");
    expect(cancelled.tone).toBe("warn");
    const failed = f("recovery_test_end", { op_id: "t", artifact: SET + "/a.dump", validator: "pg-check", duration: "9s", reason: "the validator rejected the bytes" }, "error", "error");
    expect(failed.text).toMatch(/^recovery test failed: a\.dump/);
    expect(failed.tone).toBe("error");
    expect(failed.text).not.toMatch(NOT_FALLBACK);
  });

  it("control: an event this build has never heard of still falls back to message plus fields", () => {
    expect(f("brand_new_event", { x: "1" }).text).toBe("brand_new_event x=1");
  });

  it("an unfinished recovery test or recovery is named in words, not by its action id", () => {
    render(<UnfinishedActionsNotice actions={[
      { action: "recovery_test", actionId: "t", startedAt: T, sequence: 1 },
      { action: "recovery", actionId: "r", startedAt: T, sequence: 2 }
    ]} />);
    expect(screen.getByText(/^Recovery test started/)).toBeTruthy();
    expect(screen.getByText(/^Recovery started/)).toBeTruthy();
    expect(document.body.textContent).not.toContain("recovery_test started");
    cleanup();
  });
});

describe("every activity surface draws the new events with their dedicated lines", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  const lines = (): SetActivityEvent[] => [
    ev(1, "scrub_failure", { artifact: SET + "/a.dump", medium: "offsite", class: "content", reason: "sha256 differs from the one recorded at ingest" }, "warn", "warn"),
    ev(2, "scrub_pass", { trigger: "manual", scope: "set " + SET, selected: "2", checked: "2", passed: "1", failed: "1", could_not_complete: "0", requires_restore: "0", skipped_held: "0", skipped_leased: "0", bytes_read: "0" }, "warn", "warn"),
    { ...ev(3, "recovery_stage", { op_id: "t", stage: "reading", stage_label: "Reading" }), action: "recovery_test", actionId: "t" },
    ev(4, "recovery_test_end", { op_id: "t", artifact: SET + "/a.dump", reason: "cancelled before it finished; no result was recorded" }, "warn", "warn")
  ];
  const wanted = [
    "scrub did not pass: a.dump on offsite (content check): sha256 differs from the one recorded at ingest",
    "scrub pass found problems",
    "recovery test stage: Reading",
    "recovery test did not complete: a.dump: cancelled before it finished"
  ];
  const setOf = async () => (await createMockApi("default").listSets()).find((x) => x.id === SET)!;
  const activity = (events: SetActivityEvent[]) => ({
    setId: SET, active: false, stage: null, artifact: null, artifactsCompleted: 0, artifactsTotal: null, progressBasis: "unknown" as const,
    bytesTransferred: null, bytesTotal: null, bytesPerSecond: null, failures: 0, outcome: null, startedAt: null, finishedAt: null,
    events, truncated: false, dropped: false, oldestSequence: 1, latestSequence: top(events)
  });

  it("the dashboard strip", async () => {
    const set = await setOf();
    const api = rootWith({ getLiveActivity: vi.fn().mockResolvedValue({ observedAt: T, epoch: "e", pollAfterMs: 60_000, deployment: null, sets: [activity(lines())] }) });
    render(<MemoryRouter><ApiProvider api={api}><DashboardActivity sets={[set]} /></ApiProvider></MemoryRouter>);
    const log = await screen.findByRole("log");
    for (const w of wanted) expect(log.textContent).toContain(w);
    expect(log.textContent).not.toMatch(/(trigger|scope|op_id|stage_label)=/);
  });

  it("the set page's terminal", async () => {
    const set = await setOf();
    const held = new Map([[SET, activity(lines())]]);
    render(
      <MemoryRouter>
        <ApiProvider api={rootWith()}>
          <SetActivityPanel set={set} feed={{ held, reading: null, pollAfterMs: 60_000, error: null, refresh: () => {} } as never} />
        </ApiProvider>
      </MemoryRouter>
    );
    const log = await screen.findByRole("log");
    for (const w of wanted) expect(log.textContent).toContain(w);
  });

  it("the copied and saved text, which the strips and the docked terminal share", () => {
    const text = logText(lines());
    for (const w of wanted) expect(text).toContain(w);
  });
});

describe("Scrub now, Cancel and Show validator output on the set page", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  function renderSection(over: Partial<RetndApi> = {}, readOnly = false) {
    const root = rootWith({
      getLiveActivity: vi.fn().mockResolvedValue(emptyLive()),
      startScrub: vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "scrub-op-1" }),
      cancelAssuranceOperation: vi.fn().mockResolvedValue({ operationId: "scrub-op-1", kind: "scrub", backupSetId: SET, status: "cancelling" }),
      getRecoveryTestOutput: vi.fn().mockResolvedValue(output()),
      ...over
    } as never);
    render(
      <MemoryRouter>
        <ApiProvider api={withHttpAssurance(root)}>
          <AssuranceSection setId={SET} readOnly={readOnly} />
        </ApiProvider>
      </MemoryRouter>
    );
    return root as RetndApi & { startScrub: ReturnType<typeof vi.fn>; cancelAssuranceOperation: ReturnType<typeof vi.fn>; getRecoveryTestOutput: ReturnType<typeof vi.fn> };
  }

  it("Scrub now is drawn, calls the route, and the terminal twin is printed beside it", async () => {
    const root = renderSection();
    const scrub = await screen.findByRole("button", { name: "Scrub now" });
    expect(screen.queryByTestId("no-run-route")).toBeNull();
    expect(screen.getByText("retnd scrub " + SET, { exact: true })).toBeTruthy();
    await userEvent.click(scrub);
    await waitFor(() => expect(root.startScrub).toHaveBeenCalledWith("production", "postgres-primary"));
    expect(await screen.findByTestId("operation-progress")).toHaveAttribute("data-operation-id", "scrub-op-1");
  });

  it("a running scrub can be cancelled, and the cancel's terminal twin names its operation", async () => {
    const root = renderSection();
    await userEvent.click(await screen.findByRole("button", { name: "Scrub now" }));
    expect(await screen.findByText("retnd assurance cancel scrub-op-1", { exact: true })).toBeTruthy();
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(root.cancelAssuranceOperation).toHaveBeenCalledWith("scrub-op-1"));
    expect(await screen.findByTestId("operation-progress")).toHaveAttribute("data-cancelling", "true");
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
  });

  it("a cancel the service refuses is shown in the service's words and does not escape as an unhandled rejection", async () => {
    // vitest fails the run on an unhandled rejection, so a handler that
    // lets the refusal escape fails here without an assertion of its own.
    renderSection({ cancelAssuranceOperation: vi.fn().mockRejectedValue(refusal("OPERATION_NOT_FOUND", "no running operation has that id", 404)) } as never);
    await userEvent.click(await screen.findByRole("button", { name: "Scrub now" }));
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect((await screen.findByRole("alert")).textContent).toContain("no running operation has that id");
    await new Promise((r) => setTimeout(r, 20));
  });

  it("a scrub the service refuses (409) is shown in the service's words", async () => {
    renderSection({ startScrub: vi.fn().mockRejectedValue(refusal("OPERATION_ALREADY_RUNNING", "a scrub of this backup set is already running", 409)) } as never);
    await userEvent.click(await screen.findByRole("button", { name: "Scrub now" }));
    expect((await screen.findByRole("alert")).textContent).toContain("already running");
    expect(screen.queryByTestId("operation-progress")).toBeNull();
  });

  it("read-only sessions get neither Scrub now nor Cancel enabled", async () => {
    renderSection({}, true);
    expect(await screen.findByRole("button", { name: "Scrub now" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Run recovery test" })).toBeDisabled();
  });

  it("Show validator output reads the route, draws the log, and prints the CLI twin", async () => {
    const root = renderSection();
    expect(await screen.findByText("retnd recovery-test " + SET + " --show-output", { exact: true })).toBeTruthy();
    await userEvent.click(await screen.findByRole("button", { name: "Show validator output" }));
    await waitFor(() => expect(root.getRecoveryTestOutput).toHaveBeenCalledWith("production", "postgres-primary"));
    expect((await screen.findByTestId("validator-log")).textContent).toContain("validate: ok");
  });

  it("before any test has recorded output, it says so and draws no log", async () => {
    renderSection({ getRecoveryTestOutput: vi.fn().mockResolvedValue(output({ recorded: false, operationId: "", artifactId: "", medium: "", state: "" as never, recordedAt: "", records: [] })) } as never);
    await userEvent.click(await screen.findByRole("button", { name: "Show validator output" }));
    expect((await screen.findByRole("status")).textContent).toContain("No recovery test has run for this set yet.");
    expect(screen.queryByTestId("validator-log")).toBeNull();
  });

  it("joining a test that was already running is said, not hidden", async () => {
    renderSection({ startRecoveryTest: vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-running", coalesced: true }) });
    await userEvent.click(await screen.findByRole("button", { name: "Run recovery test" }));
    expect((await screen.findByTestId("operation-joined")).textContent).toMatch(/already running/i);
  });

  it("an operation's detail link is offered, and the page it opens is the same panel", async () => {
    renderSection();
    await userEvent.click(await screen.findByRole("button", { name: "Scrub now" }));
    const panel = await screen.findByTestId("operation-progress");
    expect(within(panel).getByRole("link", { name: "Operation detail" }).getAttribute("href")).toBe("/assurance/operations/scrub-op-1");
  });
});

describe("the new controls' tooltips, preference explicitly ON", () => {
  beforeEach(() => {
    resetGraphForTests();
    // Explicitly ON, and the one-time opt-out question NOT yet asked, so the
    // x on a pop-up raises it.
    localStorage.setItem(TOOLTIPS_KEY, "on");
    setTooltipsEnabled(true);
  });
  afterEach(() => { cleanup(); localStorage.clear(); });

  const mount = () => render(
    <MemoryRouter>
      <ApiProvider api={withHttpAssurance(rootWith({ getLiveActivity: vi.fn().mockResolvedValue(emptyLive()) }))}>
        <AssuranceSection setId={SET} readOnly={false} />
        <TooltipOptOutDialog />
      </ApiProvider>
    </MemoryRouter>
  );
  const popFor = (id: keyof typeof TOOLTIPS) => {
    const body = Array.from(document.querySelectorAll<HTMLElement>(".tooltip__body")).find((n) => n.innerHTML === TOOLTIPS[id].html);
    if (!body) throw new Error("no tooltip body for " + id);
    return body.closest(".tooltip__pop") as HTMLElement;
  };

  it("has an entry for the validator control, the validator log and the operation detail link, each with copy", () => {
    for (const id of ["assurance.validator.show", "assurance.validator.log", "assurance.operation.detail"] as const) {
      expect(TOOLTIPS[id], id).toBeTruthy();
      expect(TOOLTIPS[id].html.length, id).toBeGreaterThan(30);
      const tags = [...TOOLTIPS[id].html.matchAll(/<\/?([a-z]+)/gi)].map((m) => m[1].toLowerCase());
      for (const t of tags) expect(["strong", "em", "code", "br"], id).toContain(t);
    }
  });

  it("hovering Show validator output shows its registry copy, and the control keeps its name", async () => {
    mount();
    const button = await screen.findByRole("button", { name: "Show validator output" });
    await userEvent.hover(button);
    expect(popFor("assurance.validator.show")).toBeVisible();
    expect(button.getAttribute("aria-describedby")).toBeTruthy();
  });

  it("taking the opt-out from the new control's x silences Scrub now's tooltip too", async () => {
    mount();
    await userEvent.hover(await screen.findByRole("button", { name: "Show validator output" }));
    await userEvent.click(screen.getByRole("button", { name: /^Close help for/ }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, turn tooltips off" }));
    await userEvent.hover(await screen.findByRole("button", { name: "Scrub now" }));
    expect(popFor("assurance.scrub.now")).not.toBeVisible();
  });
});
