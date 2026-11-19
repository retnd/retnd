/**
 * Review of #1184: the set page has two places that follow a recovery test,
 * the Assurance section and the Validation pane, and both attach to the run
 * that is going when the page opens. One operation is followed once,
 * whoever asks: two followers of the same id share one poll.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { useOperation } from "@shared/assurance/useOperation";
import type { AssuranceApi, Operation, OperationEvent } from "@shared/assurance/types";

const op = (state: Operation["state"]): Operation =>
  ({ id: "op-1", kind: "recovery_test", state, stage: "reading", bytes: 0, totalBytes: 0 } as unknown as Operation);
const line = (seq: number): OperationEvent => ({ seq, text: "line " + seq } as unknown as OperationEvent);

afterEach(() => cleanup());

describe("one follower per operation", () => {
  it("two hooks on one operation read it once a tick, and both see the reading", async () => {
    const readOperation = vi.fn().mockResolvedValue({ operation: op("running"), events: [line(1)], nextCursor: 1 });
    const api = { readOperation } as unknown as AssuranceApi;
    const { result } = renderHook(() => ({
      a: useOperation(api, "op-1"),
      b: useOperation(api, "op-1", undefined, { retainEvents: false })
    }));
    await act(async () => {});
    expect(readOperation).toHaveBeenCalledTimes(1);
    expect(result.current.a.operation?.state).toBe("running");
    expect(result.current.b.operation?.state).toBe("running");
    // The one that keeps lines has them; the one that doesn't, doesn't.
    expect(result.current.a.events.map((e) => e.seq)).toEqual([1]);
    expect(result.current.b.events).toEqual([]);
  });

  it("each follower still hears the end, once", async () => {
    const readOperation = vi.fn().mockResolvedValue({ operation: op("complete"), events: [], nextCursor: 0 });
    const api = { readOperation } as unknown as AssuranceApi;
    const endA = vi.fn();
    const endB = vi.fn();
    renderHook(() => ({ a: useOperation(api, "op-2", endA), b: useOperation(api, "op-2", endB) }));
    await act(async () => {});
    expect(readOperation).toHaveBeenCalledTimes(1);
    expect(endA).toHaveBeenCalledTimes(1);
    expect(endB).toHaveBeenCalledTimes(1);
  });
});
