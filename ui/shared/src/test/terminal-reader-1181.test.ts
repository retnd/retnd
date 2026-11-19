/**
 * The one terminal reader's guards (review of #1181).
 *
 * Each of these is a race the reader's comments promise to handle: an answer
 * landing after the reader stopped, a second api arriving while two terminals
 * are mounted, a refresh asked for while a poll is in flight, a NAS that stops
 * answering and a session that ends. They use deferred promises so the order
 * of events is the test's, not the scheduler's.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { graph } from "@shared/state/graph";
import { ingestReading } from "@shared/state/activityWindow";
import {
  acquireTerminalFeed, backoffDelay, EVICT_AFTER, MAX_BACKOFF_MS, refreshTerminalFeed, resetTerminalForTests, terminalNode
} from "@shared/state/terminalStore";
import type { TerminalState } from "@shared/state/terminalStore";
import { RetndError } from "@shared/api/contracts";
import type { ApiError, RetndApi } from "@shared/api/contracts";
import type { LiveActivity, SetActivity, SetActivityEvent } from "@shared/types/activity";

vi.mock("@shared/api/sessionLoss", () => ({ reportSessionLost: vi.fn() }));
import { reportSessionLost } from "@shared/api/sessionLoss";

const ev = (sequence: number, setId = "a/one"): SetActivityEvent => ({
  sequence, at: "2026-09-07T14:02:00Z", level: "info", event: "commit", scope: "set", message: "line " + sequence,
  fields: { backup_set: setId }
});

const setOf = (setId: string, events: SetActivityEvent[]): SetActivity => ({
  setId, active: false, stage: null, artifact: null, artifactsCompleted: 0, artifactsTotal: null, progressBasis: "unknown",
  bytesTransferred: null, bytesTotal: null, bytesPerSecond: null, failures: 0, outcome: null, startedAt: null, finishedAt: null,
  events, truncated: false, dropped: false,
  oldestSequence: events.length ? events[0].sequence : 0, latestSequence: events.length ? events[events.length - 1].sequence : 0
});

const reading = (sets: SetActivity[], over: Partial<LiveActivity> = {}): LiveActivity => ({
  observedAt: "2026-09-07T14:02:41Z", epoch: "p1", pollAfterMs: 1000, sets, deployment: null, ...over
});

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const state = (): TerminalState => graph.read(terminalNode);
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); };
const apiOf = (getLiveActivity: ReturnType<typeof vi.fn>) => ({ getLiveActivity } as unknown as RetndApi);

afterEach(() => {
  resetTerminalForTests();
  vi.useRealTimers();
  vi.mocked(reportSessionLost).mockClear();
});

describe("an answer that arrives too late", () => {
  it("is not written once the last terminal has gone", async () => {
    const answer = deferred<LiveActivity>();
    const getLiveActivity = vi.fn().mockReturnValue(answer.promise);
    const release = acquireTerminalFeed(apiOf(getLiveActivity));
    release();
    answer.resolve(reading([setOf("a/one", [ev(1)])]));
    await flush();
    expect(state().sets).toEqual({});
  });

  it("from the old api is not written into the new api's state", async () => {
    const old = deferred<LiveActivity>();
    const fresh = deferred<LiveActivity>();
    acquireTerminalFeed(apiOf(vi.fn().mockReturnValue(old.promise)));
    acquireTerminalFeed(apiOf(vi.fn().mockReturnValue(fresh.promise)));
    fresh.resolve(reading([setOf("b/two", [ev(1, "b/two")])], { epoch: "new" }));
    await flush();
    old.resolve(reading([setOf("a/one", [ev(9)])], { epoch: "old" }));
    await flush();
    expect(Object.keys(state().sets)).toEqual(["b/two"]);
    expect(state().epoch).toBe("new");
  });
});

describe("an api swap with terminals already mounted", () => {
  it("keeps reading for the old holders after the new holder lets go", async () => {
    vi.useFakeTimers();
    const first = vi.fn().mockResolvedValue(reading([setOf("a/one", [ev(1)])]));
    const second = vi.fn().mockResolvedValue(reading([setOf("a/one", [ev(1)])]));
    const a = acquireTerminalFeed(apiOf(first));
    acquireTerminalFeed(apiOf(first));
    await flush();
    const c = acquireTerminalFeed(apiOf(second));
    await flush();
    c();
    // Two terminals are still mounted, so the reader keeps going.
    const before = second.mock.calls.length;
    await vi.advanceTimersByTimeAsync(1100);
    expect(second.mock.calls.length).toBeGreaterThan(before);
    a();
  });

  it("stops only when every holder, old and new, has let go", async () => {
    vi.useFakeTimers();
    const first = vi.fn().mockResolvedValue(reading([]));
    const second = vi.fn().mockResolvedValue(reading([]));
    const a = acquireTerminalFeed(apiOf(first));
    const b = acquireTerminalFeed(apiOf(second));
    await flush();
    a();
    b();
    const asked = second.mock.calls.length;
    await vi.advanceTimersByTimeAsync(5000);
    expect(second.mock.calls.length).toBe(asked);
  });
});

describe("a refresh while a poll is in flight", () => {
  it("runs exactly one more reading once the first answers, not two at once", async () => {
    const one = deferred<LiveActivity>();
    const getLiveActivity = vi.fn().mockReturnValueOnce(one.promise).mockResolvedValue(reading([setOf("a/one", [ev(2)])]));
    const release = acquireTerminalFeed(apiOf(getLiveActivity));
    refreshTerminalFeed();
    refreshTerminalFeed();
    expect(getLiveActivity).toHaveBeenCalledTimes(1);
    one.resolve(reading([setOf("a/one", [ev(1)])]));
    await flush();
    expect(getLiveActivity).toHaveBeenCalledTimes(2);
    expect(getLiveActivity).toHaveBeenLastCalledWith({ since: 1, limit: 200 });
    release();
  });
});

describe("a service that stops answering", () => {
  it("backs off exponentially up to a cap, with jitter either side", () => {
    expect(backoffDelay(1000, 0)).toBe(1000);
    expect(backoffDelay(1000, 1, () => 0.5)).toBe(2000);
    expect(backoffDelay(1000, 3, () => 0.5)).toBe(8000);
    expect(backoffDelay(1000, 20, () => 0.5)).toBe(MAX_BACKOFF_MS);
    expect(backoffDelay(1000, 2, () => 0)).toBe(3200);
    expect(backoffDelay(1000, 2, () => 1)).toBe(4800);
  });

  it("waits longer after each failure and goes back to the service's cadence on success", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0.5);
    const getLiveActivity = vi.fn()
      .mockResolvedValueOnce(reading([]))
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValue(reading([]));
    const release = acquireTerminalFeed(apiOf(getLiveActivity));
    await flush();
    await vi.advanceTimersByTimeAsync(1000); // first failure
    expect(getLiveActivity).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1999);
    expect(getLiveActivity).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1); // second failure, after 2 s
    expect(getLiveActivity).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(3999);
    expect(getLiveActivity).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(1); // success, after 4 s
    expect(getLiveActivity).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(1000); // back on the service's cadence
    expect(getLiveActivity).toHaveBeenCalledTimes(5);
    release();
  });

  it("stops polling when the session is gone, and starts again for the next terminal", async () => {
    vi.useFakeTimers();
    const getLiveActivity = vi.fn().mockRejectedValue(new RetndError({ code: "UNAUTHENTICATED", message: "signed out" } as ApiError));
    const api = apiOf(getLiveActivity);
    const release = acquireTerminalFeed(api);
    await flush();
    expect(reportSessionLost).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(getLiveActivity).toHaveBeenCalledTimes(1);
    expect(reportSessionLost).toHaveBeenCalledTimes(1);

    getLiveActivity.mockResolvedValue(reading([]));
    const again = acquireTerminalFeed(api);
    await flush();
    expect(getLiveActivity).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1000);
    expect(getLiveActivity).toHaveBeenCalledTimes(3);
    release();
    again();
  });
});

describe("a set that was removed", () => {
  it("keeps its window through a reading or two, then drops it", () => {
    let s = ingestReading(state(), reading([setOf("a/one", [ev(1)]), setOf("b/two", [ev(2, "b/two")])]));
    for (let i = 1; i < EVICT_AFTER; i++) {
      s = ingestReading(s, reading([setOf("a/one", [])], { observedAt: "t" + i }));
      expect(s.sets["b/two"]).toBeDefined();
    }
    s = ingestReading(s, reading([setOf("a/one", [])]));
    expect(s.sets["b/two"]).toBeUndefined();
    expect(s.absent).toEqual({});
    expect(s.sets["a/one"]).toBeDefined();
  });

  it("forgets the misses once the set appears again", () => {
    let s = ingestReading(state(), reading([setOf("a/one", [ev(1)]), setOf("b/two", [ev(2, "b/two")])]));
    s = ingestReading(s, reading([setOf("a/one", [])]));
    expect(s.absent).toEqual({ "b/two": 1 });
    s = ingestReading(s, reading([setOf("a/one", []), setOf("b/two", [])]));
    expect(s.absent).toEqual({});
  });
});
