/**
 * Issue #1200: the production assurance seam reads a run back from the
 * durable activity record when the live tail no longer holds it, and says
 * what the record says: the lines in the panel's words, the stage reached and
 * how it ended.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { httpApi } from "@shared/api/client";
import { createMockApi } from "@shared/api/mock";
import { RetndError } from "@shared/api/contracts";
import type { AssuranceOperationRecord, RetndApi } from "@shared/api/contracts";
import { NotOnRecord, createHttpAssurance, feedFromRecord } from "@shared/assurance/realAssurance";
import type { SetActivityEvent } from "@shared/types/activity";

const SET = "production/postgres-primary";
const T = "2026-10-14T09:00:00Z";
const line = (sequence: number, event: string, fields: Record<string, string>, result?: SetActivityEvent["result"]): SetActivityEvent => ({
  sequence, at: T, level: "info", event, scope: "set", message: event, fields: { op_id: "op-1", ...fields },
  action: "recovery_test", actionId: "op-1", ...(result ? { result } : {})
});
const record = (state: AssuranceOperationRecord["state"], stage: string, end?: SetActivityEvent): AssuranceOperationRecord => ({
  operationId: "op-1", kind: "recovery_test", backupSetId: SET, state, stage, reason: end?.fields.reason ?? "", startedAt: T, endedAt: end ? T : "",
  events: [line(10, "recovery_test_start", {}), line(11, "recovery_stage", { stage, stage_label: "" }), ...(end ? [end] : [])]
});
const rootWith = (over: Partial<RetndApi>) => ({ ...createMockApi("assurance-unavailable"), ...over }) as RetndApi;
const emptyLive = { observedAt: T, epoch: "e", pollAfterMs: 1000, deployment: null, sets: [] };

describe("#1200 a run read from the durable record", () => {
  it("a failed run keeps the stage it reached and says why in the end line", () => {
    const feed = feedFromRecord(record("failed", "verifying", line(12, "recovery_test_end", { reason: "sha256 differs" }, "error")), 0);
    expect(feed.operation).toMatchObject({ state: "failed", stage: "verifying", setId: SET });
    expect(feed.events.map((e) => e.text)).toEqual(["Recovery test started", "Verifying", "Recovery test did not pass: sha256 differs"]);
    expect(feed.nextCursor).toBe(12);
  });

  it("a cancelled run reads as cancelled, from the record's own state", () => {
    const feed = feedFromRecord(record("cancelled", "reading", line(12, "recovery_test_end", { reason: "cancelled before it finished; no result was recorded" }, "warn")), 0);
    expect(feed.operation.state).toBe("cancelled");
    expect(feed.events[2].text).toBe("Recovery test cancelled");
  });

  it("a run with no end that nothing runs any more is unfinished, not untracked", () => {
    expect(feedFromRecord(record("unfinished", "reading"), 0).operation.state).toBe("unfinished");
  });

  it("only lines above the cursor are handed over", () => {
    const feed = feedFromRecord(record("passed", "complete", line(12, "recovery_test_end", {}, "success")), 11);
    expect(feed.events.map((e) => e.seq)).toEqual([12]);
  });

  it("a cold open of an id the live feed does not hold asks the record by that id", async () => {
    const getAssuranceOperation = vi.fn().mockResolvedValue(record("passed", "complete", line(12, "recovery_test_end", {}, "success")));
    const a = createHttpAssurance(rootWith({ getLiveActivity: vi.fn().mockResolvedValue(emptyLive), getAssuranceOperation }));
    const read = await a.readOperation("op-1", 0);
    expect(getAssuranceOperation).toHaveBeenCalledWith("op-1");
    expect(read.operation).toMatchObject({ state: "complete", stage: "complete" });
    expect(read.events).toHaveLength(3);
  });

  it("a record that holds nothing is NotOnRecord, said in words", async () => {
    const getAssuranceOperation = vi.fn().mockRejectedValue(new RetndError({ code: "OPERATION_NOT_FOUND", message: "x", status: 404, correlationId: "c" }));
    const a = createHttpAssurance(rootWith({ getLiveActivity: vi.fn().mockResolvedValue(emptyLive), getAssuranceOperation }));
    await expect(a.readOperation("op-1", 0)).rejects.toMatchObject({ name: "NotOnRecord" });
  });

  it("a refusal other than not-found is not taken for an empty record", async () => {
    const getAssuranceOperation = vi.fn().mockRejectedValue(new RetndError({ code: "INTERNAL", message: "boom", status: 500, correlationId: "c" }));
    const a = createHttpAssurance(rootWith({ getLiveActivity: vi.fn().mockResolvedValue(emptyLive), getAssuranceOperation }));
    await expect(a.readOperation("op-1", 0)).rejects.toMatchObject({ name: "RetndError" });
  });
});

describe("#1200 review: the record's words are not guessed", () => {
  afterEach(() => { vi.unstubAllGlobals(); });
  const body = (over: Record<string, unknown>) => ({
    operation_id: "op-1", kind: "recovery_test", backup_set_id: SET, state: "passed", stage: "complete", reason: "",
    started_at: T, ended_at: T, events: [], ...over
  });
  const serve = (b: unknown) => vi.stubGlobal("fetch", vi.fn(async () => ({
    ok: true, status: 200, headers: { get: () => null }, json: async () => b, text: async () => JSON.stringify(b)
  })));

  it("a state this build does not know is refused out loud, not drawn as unfinished", async () => {
    serve(body({ state: "paused" }));
    await expect(httpApi.getAssuranceOperation("op-1")).rejects.toThrow(/state "paused".*does not know/);
  });

  it("a kind this build does not know is refused out loud, not drawn as a recovery test", async () => {
    serve(body({ kind: "migration" }));
    await expect(httpApi.getAssuranceOperation("op-1")).rejects.toThrow(/kind "migration".*does not know/);
  });

  it("the known states and kinds read through as they are", async () => {
    serve(body({ kind: "scrub", state: "did_not_complete" }));
    await expect(httpApi.getAssuranceOperation("op-1")).resolves.toMatchObject({ kind: "scrub", state: "did_not_complete" });
  });

  it("an id with nothing on record is a NotOnRecord instance carrying the server's words", async () => {
    const words = "nothing is on record for this operation id (a run's lines are kept for 7 days)";
    const getAssuranceOperation = vi.fn().mockRejectedValue(new RetndError({ code: "OPERATION_NOT_FOUND", message: words, status: 404, correlationId: "c" }));
    const a = createHttpAssurance(rootWith({ getLiveActivity: vi.fn().mockResolvedValue(emptyLive), getAssuranceOperation }));
    const err = await a.readOperation("op-1", 0).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(NotOnRecord);
    expect((err as Error).message).toBe(words);
  });

  it("a running scrub with no line yet is drawn as running, with its start line when it has one", () => {
    const start = line(20, "scrub_start", { trigger: "manual", scope: "set " + SET });
    const rec: AssuranceOperationRecord = {
      operationId: "op-1", kind: "scrub", backupSetId: SET, state: "running", stage: "", reason: "", startedAt: T, endedAt: "", events: [start]
    };
    const feed = feedFromRecord(rec, 0);
    expect(feed.operation).toMatchObject({ kind: "scrub", state: "running" });
    expect(feed.events.map((e) => e.text)).toEqual(["scrub started (manual, set " + SET + ")"]);
    expect(feedFromRecord({ ...rec, events: [] }, 0).events).toEqual([]);
  });
});
