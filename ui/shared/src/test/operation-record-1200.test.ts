/**
 * Issue #1200: the production assurance seam reads a run back from the
 * durable activity record when the live tail no longer holds it, and says
 * what the record says: the lines in the panel's words, the stage reached and
 * how it ended.
 */
import { describe, expect, it, vi } from "vitest";
import { createMockApi } from "@shared/api/mock";
import { RetndError } from "@shared/api/contracts";
import type { AssuranceOperationRecord, RetndApi } from "@shared/api/contracts";
import { createHttpAssurance, feedFromRecord } from "@shared/assurance/realAssurance";
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
