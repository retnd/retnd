/**
 * Issue #1592: the bulk recovery dialog once it has gone.
 *
 * Two things, and the second is a decision rather than a guard.
 *
 * Nothing the dialog started may set its state after it unmounts. It streams
 * a line per file into setters, and the run's outcome lands in a .then()
 * that sets four more, so a dialog that went away mid-run took every one of
 * those as a state update on an unmounted component. In vitest that is the
 * hole #1589 was about: once jsdom is torn down React's read of
 * `window.event` throws inside the callback and the run fails on an
 * unhandled rejection. test/teardownProbe.ts says why each case holds the
 * request open, unmounts, and only then lets it answer.
 *
 * And closing the dialog closes the recovery's connection. The engine treats
 * a closed connection as a cancel (webhost/handlers_recoverymany.go): the file
 * in flight is discarded, the rest are recorded as not started, and the
 * manifest says the run was cancelled. A closed tab, a reload and Ctrl-C in
 * `retnd recover` all end a run that way already. Leaving the dialog any
 * other way used to leave the run going with nothing on screen to show its
 * progress, its outcome or a way to stop it, so it now ends the same way.
 * Strict mode's rehearsal of an unmount must not count as one, or every run
 * in development would cancel itself on start.
 */
import { StrictMode } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import { RetndError } from "@shared/api/contracts";
import type { RecoverManyItem, RecoverManyProgress, RecoverManyResult, RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { BackupsRecoverRun } from "./BackupsRecoverRun";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
});

const FILES = [
  { id: "production/postgres-primary/a.dump", filename: "a.dump" },
  { id: "production/postgres-primary/b.dump", filename: "b.dump" }
];

const item = (id: string, over: Partial<RecoverManyItem> = {}): RecoverManyItem =>
  ({ artifactId: id, name: id.split("/").pop() ?? id, status: "ready", sizeBytes: 10, ...over });

const RESULT: RecoverManyResult = {
  operationId: "op_1592",
  target: { kind: "staging", path: "r1592" },
  folder: "r1592",
  items: FILES.map((f) => item(f.id, { status: "recovered", bytes: 10, sha256: "ab" })),
  totals: { count: 2, ready: 0, recovered: 2, failed: 0, skipped: 0, needsThaw: 0, quarantined: 0, unrecoverable: 0, bytes: 20 },
  cancelled: false,
  manifestPath: "/data/recover-staging/r1592/manifest.json"
};

type Opts = Parameters<RetndApi["recoverMany"]>[1];

/** The mock with its recovery replaced by one this file answers. `calls`
 *  holds the options each run was started with: the progress callback and
 *  the signal that closes the connection. */
function harness() {
  const answer = held<RecoverManyResult>();
  const calls: NonNullable<Opts>[] = [];
  const api: RetndApi = {
    ...createMockApi("default"),
    recoverMany: (_req, opts) => {
      calls.push(opts ?? {});
      return answer.promise;
    }
  };
  return { api, answer, calls };
}

/** A line delivered the way the client delivers one: from inside its read
 *  loop, a promise nobody else holds. */
function line(opts: NonNullable<Opts>, ev: RecoverManyProgress) {
  void Promise.resolve().then(() => opts.onProgress?.(ev));
}

describe("BackupsRecoverRun after unmount (#1592)", () => {
  it("ignores a progress line that arrives after the dialog has gone", async () => {
    const { api, calls } = harness();
    const { unmount } = render(
      <ApiProvider api={api}>
        <BackupsRecoverRun files={FILES} target={{ kind: "staging", where: "r1592" }} acknowledged={false} onClose={() => {}} />
      </ApiProvider>
    );
    expect(calls).toHaveLength(1);
    unmount();
    probe.tearDown();
    line(calls[0], { kind: "start", index: 1, total: 2, item: item(FILES[0].id), doneBytes: 0, totalBytes: 20 });
    line(calls[0], { kind: "item", index: 1, total: 2, item: item(FILES[0].id, { status: "recovered", bytes: 10 }), doneBytes: 10, totalBytes: 20 });
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores the run's outcome when it arrives after the dialog has gone", async () => {
    const { api, answer } = harness();
    const { unmount } = render(
      <ApiProvider api={api}>
        <BackupsRecoverRun files={FILES} target={{ kind: "staging", where: "r1592" }} acknowledged={false} onClose={() => {}} />
      </ApiProvider>
    );
    unmount();
    probe.tearDown();
    answer.resolve(RESULT);
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a refusal that arrives after the dialog has gone", async () => {
    const { api, answer } = harness();
    const { unmount } = render(
      <ApiProvider api={api}>
        <BackupsRecoverRun files={FILES} target={{ kind: "staging", where: "r1592" }} acknowledged={false} onClose={() => {}} />
      </ApiProvider>
    );
    unmount();
    probe.tearDown();
    answer.reject(new RetndError({ code: "RECOVERY_NO_SPACE", message: "no space", status: 507, correlationId: "cid_1592" }));
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  // The source host's refusals go back to the bar that asked for the tick or
  // the password, through the page's own setters. A page that has gone has
  // no bar to route them to.
  it("does not hand a source-host refusal back once the dialog has gone", async () => {
    const { api, answer } = harness();
    const onSourceRefused = vi.fn();
    const { unmount } = render(
      <ApiProvider api={api}>
        <BackupsRecoverRun
          files={FILES}
          target={{ kind: "source", where: "retnd-recovered-1592", acknowledgedDestination: "nas" }}
          acknowledged={false}
          onClose={() => {}}
          onSourceRefused={onSourceRefused}
        />
      </ApiProvider>
    );
    unmount();
    probe.tearDown();
    answer.reject(new RetndError({ code: "RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED", message: "tick it", status: 409, correlationId: "cid_1592s" }));
    await probe.settle();

    expect(onSourceRefused).not.toHaveBeenCalled();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("closes the recovery's connection when the dialog goes, so the engine cancels the run", async () => {
    const { api, calls } = harness();
    const { unmount } = render(
      <ApiProvider api={api}>
        <BackupsRecoverRun files={FILES} target={{ kind: "staging", where: "r1592" }} acknowledged={false} onClose={() => {}} />
      </ApiProvider>
    );
    const signal = calls[0].signal!;
    expect(signal.aborted).toBe(false);
    unmount();
    await probe.settle();

    expect(signal.aborted).toBe(true);
  });

  // The control, and the case for strict mode. Its rehearsal unmounts and
  // remounts every effect once on mount; the run must start once, keep its
  // connection, and draw its lines and its summary as it always did.
  it("keeps the run going through strict mode's rehearsal and still shows what it did", async () => {
    const { api, answer, calls } = harness();
    render(
      <StrictMode>
        <ApiProvider api={api}>
          <BackupsRecoverRun files={FILES} target={{ kind: "staging", where: "r1592" }} acknowledged={false} onClose={() => {}} />
        </ApiProvider>
      </StrictMode>
    );
    await probe.settle();
    expect(calls).toHaveLength(1);
    expect(calls[0].signal!.aborted).toBe(false);

    line(calls[0], { kind: "start", index: 1, total: 2, item: item(FILES[0].id), doneBytes: 0, totalBytes: 20 });
    expect(await screen.findByText("Recovering")).toBeInTheDocument();
    answer.resolve(RESULT);
    expect(await screen.findByTestId("run-summary")).toHaveTextContent("2 recovered, 0 failed");
    expect(calls[0].signal!.aborted).toBe(false);
    expect(probe.rejections).toEqual([]);
  });
});
