/**
 * The shared terminal state (issue #1177): one causl node every terminal
 * reads, written by one reader.
 *
 * What these pin is the part a page-level test cannot see from outside: that
 * several readers cost one reading, that a reader arriving late finds the
 * window filled, that the window is bounded, and that a reconnect which
 * replays lines adds nothing and a restart freezes rather than overwrites.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { ApiProvider } from "@shared/api/ApiContext";
import { useTerminalFeed } from "@shared/pages/useActivityFeed";
import { graph } from "@shared/state/graph";
import {
  acquireTerminalFeed, ingestReading, MAX_GENERATIONS, resetTerminalForTests, terminalNode
} from "@shared/state/terminalStore";
import type { TerminalState } from "@shared/state/terminalStore";
import type { RetndApi } from "@shared/api/contracts";
import type { LiveActivity, SetActivity, SetActivityEvent } from "@shared/types/activity";

const ev = (sequence: number, setId = "a/one"): SetActivityEvent => ({
  sequence, at: "2026-09-07T14:02:" + String(sequence % 60).padStart(2, "0") + "Z", level: "info", event: "commit",
  scope: "set", message: "line " + sequence, fields: { backup_set: setId }
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

const state = (): TerminalState => graph.read(terminalNode);
const range = (from: number, to: number, setId?: string) => Array.from({ length: to - from + 1 }, (_, i) => ev(from + i, setId));

afterEach(() => {
  resetTerminalForTests();
  vi.useRealTimers();
});

function Terminal({ id, setId }: { id: string; setId: string }) {
  const { state: s } = useTerminalFeed();
  return <span data-testid={id}>{s.sets[setId]?.events.length ?? 0}</span>;
}

describe("one reader, however many terminals", () => {
  it("asks for one reading per poll with three terminals mounted, and all three change together", async () => {
    vi.useFakeTimers();
    const getLiveActivity = vi.fn().mockResolvedValue(reading([setOf("a/one", range(1, 3))]));
    const api = { getLiveActivity } as unknown as RetndApi;
    render(
      <ApiProvider api={api}>
        <Terminal id="set" setId="a/one" />
        <Terminal id="dock" setId="a/one" />
        <Terminal id="global" setId="a/one" />
      </ApiProvider>
    );
    await act(async () => {});
    expect(getLiveActivity).toHaveBeenCalledTimes(1);
    for (const id of ["set", "dock", "global"]) expect(screen.getByTestId(id).textContent).toBe("3");

    await act(async () => { vi.advanceTimersByTime(1100); });
    expect(getLiveActivity).toHaveBeenCalledTimes(2);
  });

  it("gives a terminal mounted later what is already held, without a reading of its own", async () => {
    const getLiveActivity = vi.fn().mockResolvedValue(reading([setOf("a/one", range(1, 4))]));
    const api = { getLiveActivity } as unknown as RetndApi;
    const release = acquireTerminalFeed(api);
    await act(async () => {});
    expect(state().sets["a/one"].events).toHaveLength(4);
    const asked = getLiveActivity.mock.calls.length;

    render(<ApiProvider api={api}><Terminal id="late" setId="a/one" /></ApiProvider>);
    expect(screen.getByTestId("late").textContent).toBe("4");
    await act(async () => {});
    expect(getLiveActivity.mock.calls.length).toBe(asked);
    release();
  });

  it("stops reading when the last terminal goes, and starts from nothing for the next one", async () => {
    vi.useFakeTimers();
    const getLiveActivity = vi.fn().mockResolvedValue(reading([setOf("a/one", range(1, 2))]));
    const api = { getLiveActivity } as unknown as RetndApi;
    const view = render(<ApiProvider api={api}><Terminal id="t" setId="a/one" /></ApiProvider>);
    await act(async () => {});
    view.unmount();
    const asked = getLiveActivity.mock.calls.length;
    await act(async () => { vi.advanceTimersByTime(5000); });
    expect(getLiveActivity.mock.calls.length).toBe(asked);

    // a window nothing was updating is not handed to the next terminal
    getLiveActivity.mockReturnValue(new Promise(() => {}));
    render(<ApiProvider api={api}><Terminal id="t2" setId="a/one" /></ApiProvider>);
    expect(screen.getByTestId("t2").textContent).toBe("0");
  });
});

describe("the window is bounded", () => {
  it("keeps 300 lines per set however many arrive, and 1000 in the docked window", () => {
    let s = state();
    for (let from = 1; from <= 5000; from += 200) {
      s = ingestReading(s, reading([setOf("a/one", range(from, from + 199))]));
    }
    expect(s.sets["a/one"].events).toHaveLength(300);
    expect(s.sets["a/one"].events[299].sequence).toBe(5000);
    expect(s.combined?.events).toHaveLength(1000);
    expect(s.combined?.dropped).toBe(true);
  });

  it("keeps only the newest few dead processes", () => {
    let s = state();
    for (let p = 0; p < MAX_GENERATIONS + 3; p++) {
      s = ingestReading(s, reading([setOf("a/one", range(1, 3))], { epoch: "p" + p }));
    }
    expect(s.generations).toHaveLength(MAX_GENERATIONS);
  });
});

describe("a reconnect keeps its place", () => {
  it("adds nothing when a retried reading replays lines already held", () => {
    let s = ingestReading(state(), reading([setOf("a/one", range(1, 5))]));
    const cursor = s.cursor;
    s = ingestReading(s, reading([setOf("a/one", range(3, 5))]));
    expect(s.sets["a/one"].events.map((e) => e.sequence)).toEqual([1, 2, 3, 4, 5]);
    expect(s.combined?.events.map((e) => e.sequence)).toEqual([1, 2, 3, 4, 5]);
    expect(s.cursor).toBe(cursor);
  });

  it("keeps the window and the cursor through a failed poll, and says it failed", async () => {
    const getLiveActivity = vi.fn().mockResolvedValueOnce(reading([setOf("a/one", range(1, 3))]));
    const api = { getLiveActivity } as unknown as RetndApi;
    const release = acquireTerminalFeed(api);
    await act(async () => {});
    getLiveActivity.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    await act(async () => { (await import("@shared/state/terminalStore")).refreshTerminalFeed(); });
    expect(state().error).not.toBeNull();
    expect(state().sets["a/one"].events).toHaveLength(3);
    expect(state().cursor).toBe(3);
    release();
  });
});

describe("a restart", () => {
  it("freezes the dead process's window, drops the per-set ones and asks from the start", () => {
    let s = ingestReading(state(), reading([setOf("a/one", range(1, 6))]));
    s = ingestReading(s, reading([setOf("a/one", range(1, 2))], { epoch: "p2", observedAt: "2026-09-07T15:00:00Z" }));
    expect(s.generations).toHaveLength(1);
    expect(s.generations[0].at).toBe("2026-09-07T15:00:00Z");
    expect(s.generations[0].window.events).toHaveLength(6);
    expect(s.sets["a/one"].events.map((e) => e.sequence)).toEqual([1, 2]);
    expect(s.cursor).toBe(0);
    expect(s.epoch).toBe("p2");
  });
});
