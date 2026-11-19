/**
 * A run whose lines are drawn by the set terminal does not need its own copy
 * of them (review of #1181). The Assurance card links to the terminal instead
 * of drawing a log, so holding every line of a long recovery test in the
 * card's state was memory nothing read.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { useOperation } from "@shared/assurance/useOperation";
import type { AssuranceApi, Operation, OperationEvent } from "@shared/assurance/types";

const op = (state: Operation["state"] = "running"): Operation =>
  ({ id: "op-1", kind: "recovery_test", state, stage: "reading", bytes: 0, totalBytes: 0 } as unknown as Operation);
const line = (seq: number): OperationEvent => ({ seq, text: "line " + seq } as unknown as OperationEvent);

function apiWith(events: OperationEvent[]) {
  return {
    readOperation: vi.fn().mockResolvedValue({ operation: op("complete"), events, nextCursor: events.length })
  } as unknown as AssuranceApi;
}

afterEach(() => cleanup());

describe("useOperation's retained lines", () => {
  it("keeps the lines by default, for the detail page that draws them", async () => {
    const api = apiWith([line(1), line(2)]);
    const { result } = renderHook(() => useOperation(api, "op-1"));
    await act(async () => {});
    expect(result.current.events.map((e) => e.seq)).toEqual([1, 2]);
  });

  it("keeps none when the caller says the lines are drawn elsewhere, and still follows the run", async () => {
    const api = apiWith([line(1), line(2)]);
    const { result } = renderHook(() => useOperation(api, "op-1", undefined, { retainEvents: false }));
    await act(async () => {});
    expect(result.current.events).toEqual([]);
    expect(result.current.operation?.state).toBe("complete");
  });
});
