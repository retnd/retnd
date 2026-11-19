/**
 * Issue #1202, the review of #1214: the run orchestration behind the Backups
 * row menu is a hook with its own state, so two pages never share a counter,
 * and a request that finishes after the page is gone writes nowhere.
 */
import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useRowMenuRuns } from "./useRowMenuRuns";
import type { BackupArtifact } from "@shared/types/backup";

const A = { id: "production/pg/dump.zst", setId: "production/pg", setName: "PG", filename: "dump.zst" } as BackupArtifact;

function fakeApi(startScrub: () => Promise<{ id: string }>) {
  return { assurance: { startScrub }, retryFailedIngestion: vi.fn(async () => {}) } as never;
}

describe("#1202 useRowMenuRuns", () => {
  it("#1202 a scrub is listed while it starts and carries its operation once it has", async () => {
    let finish!: (o: { id: string }) => void;
    const api = fakeApi(() => new Promise((r) => { finish = r; }));
    const { result } = renderHook(() => useRowMenuRuns({ api, gate: {} as never, onRetried: () => {} }));
    let p!: Promise<void>;
    act(() => { p = result.current.scrub(A); });
    expect(result.current.runs).toHaveLength(1);
    expect(result.current.runs[0]).toMatchObject({ kind: "scrub", starting: true });
    await act(async () => { finish({ id: "op1" }); await p; });
    expect(result.current.runs[0]).toMatchObject({ starting: false, opId: "op1" });
  });

  it("#1202 keys do not come from a counter shared between instances", () => {
    const api = fakeApi(() => new Promise(() => {}));
    const one = renderHook(() => useRowMenuRuns({ api, gate: {} as never, onRetried: () => {} }));
    act(() => { void one.result.current.scrub(A); });
    const two = renderHook(() => useRowMenuRuns({ api, gate: {} as never, onRetried: () => {} }));
    act(() => { void two.result.current.scrub(A); });
    expect(two.result.current.runs[0].key).toBe(one.result.current.runs[0].key);
  });

  it("#1202 an answer that arrives after unmount changes nothing and does not reload", async () => {
    let finish!: (o: { id: string }) => void;
    const api = fakeApi(() => new Promise((r) => { finish = r; }));
    const onRetried = vi.fn();
    const { result, unmount } = renderHook(() => useRowMenuRuns({ api, gate: {} as never, onRetried }));
    let p!: Promise<void>;
    act(() => { p = result.current.scrub(A); });
    unmount();
    await act(async () => { finish({ id: "op1" }); await p; });
    await act(async () => { await result.current.retry(A); });
    expect(onRetried).not.toHaveBeenCalled();
  });
});
