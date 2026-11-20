/**
 * Issue #1593: useSnapshotOperation's answers once the screen has gone.
 *
 * The watch effect's cleanup clears its interval, which stops the NEXT poll
 * and does nothing about the one already out: its answer still called
 * setOperation on a screen that had unmounted. The submit's own answer had
 * no check either, and a restore or a hold is exactly the request an
 * operator presses and then walks away from. test/teardownProbe.ts says why
 * each case holds the request open, unmounts, and only then lets it answer.
 *
 * Only setInterval is faked, so the poll fires when a case says and the
 * probe's settle() still waits on a real clock.
 */
import { act, cleanup, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi, SnapshotOperationResult } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { graph, resetGraphForTests } from "@shared/state/graph";
import { versionNode } from "@shared/state/appNodes";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import type { Operation, VersionInfo } from "@shared/types/operation";
import { useSnapshotOperation } from "./useSnapshotOperation";

const VERSION: VersionInfo = {
  api: "v1", service: "1.3.0", buildCommit: "9f4c1ab", goVersion: "go1.27.0",
  engine: "1.68.2", configRevision: "cfg_9f4c1ab", ready: true, compatible: true
};

function operation(status: Operation["status"]): Operation {
  return {
    id: "op_1593", setId: "production/postgres-primary", setName: "production/postgres-primary",
    kind: "transfer", label: "restore snapshot", status, progress: null, nonDestructive: false,
    startedAt: "2026-09-13T04:00:00+02:00", cycle: null
  };
}

function harness(getOperation: RetndApi["getOperation"]) {
  const api: RetndApi = { ...createMockApi(), getOperation };
  act(() => {
    graph.commit("test/seed", (tx) => tx.set(versionNode, { data: VERSION, error: null, loading: false }));
  });
  return renderHook(() => useSnapshotOperation("Could not start the restore."), {
    wrapper: ({ children }: { children: ReactNode }) => <ApiProvider api={api}>{children}</ApiProvider>
  });
}

let probe: TeardownProbe;
beforeEach(() => {
  probe = teardownProbe();
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
});
afterEach(() => {
  probe.dispose();
  cleanup();
  vi.useRealTimers();
  resetGraphForTests();
});

describe("useSnapshotOperation after unmount (#1593)", () => {
  it("ignores a poll that answers after the screen has gone", async () => {
    const poll = held<Operation>();
    const hook = harness(() => poll.promise);
    await act(async () => {
      hook.result.current.submit("run-a", () => Promise.resolve({ operation: operation("running"), snapshots: [] }));
    });
    act(() => { vi.advanceTimersByTime(2_000); });

    hook.unmount();
    probe.tearDown();
    poll.resolve(operation("completed"));
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a submit that answers after the screen has gone, and does not call onDone", async () => {
    const answer = held<SnapshotOperationResult>();
    const onDone = vi.fn();
    const hook = harness(() => new Promise<Operation>(() => {}));
    act(() => { hook.result.current.submit("run-a", () => answer.promise, onDone); });

    hook.unmount();
    probe.tearDown();
    answer.resolve({ operation: operation("running"), snapshots: [] });
    await probe.settle();

    expect(onDone).not.toHaveBeenCalled();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a submit that fails after the screen has gone", async () => {
    const answer = held<SnapshotOperationResult>();
    const hook = harness(() => new Promise<Operation>(() => {}));
    act(() => { hook.result.current.submit("run-a", () => answer.promise); });

    hook.unmount();
    probe.tearDown();
    answer.reject(new Error("the engine went away"));
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  // The same hole while the screen is still there: dismissing the outcome
  // clears the operation, and a poll that was already out must not put it
  // back on screen.
  it("does not bring back an operation the operator dismissed while a poll was out", async () => {
    const poll = held<Operation>();
    const hook = harness(() => poll.promise);
    await act(async () => {
      hook.result.current.submit("run-a", () => Promise.resolve({ operation: operation("running"), snapshots: [] }));
    });
    act(() => { vi.advanceTimersByTime(2_000); });
    act(() => { hook.result.current.dismiss(); });

    await act(async () => {
      poll.resolve(operation("running"));
      await probe.settle();
    });
    expect(hook.result.current.operation).toBeNull();
  });

  // The control: a mounted screen still takes every reading.
  it("still shows a poll that answers while the screen is there", async () => {
    const poll = held<Operation>();
    const hook = harness(() => poll.promise);
    await act(async () => {
      hook.result.current.submit("run-a", () => Promise.resolve({ operation: operation("running"), snapshots: [] }));
    });
    act(() => { vi.advanceTimersByTime(2_000); });
    await act(async () => {
      poll.resolve(operation("completed"));
      await probe.settle();
    });
    expect(hook.result.current.operation?.status).toBe("completed");
  });
});
