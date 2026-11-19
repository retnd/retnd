/**
 * An idle poll changes nothing a terminal draws, so it must not hand every
 * terminal a new object (review of #1181).
 *
 * Most polls are idle: every bucket answers with no events and the same
 * bounds. When each of those rebuilt the per-set windows, the docked window
 * and the state around them, `useCausl(terminalNode)` woke every terminal,
 * the whole set page and the dock's 1000-entry pipeline once a tick for
 * nothing. These pin that a reading which brings nothing keeps identity all
 * the way up, and that one which brings something only replaces what it
 * touched.
 */
import { afterEach, describe, expect, it } from "vitest";
import { graph } from "@shared/state/graph";
import { foldReading, ingestReading, mergeActivity } from "@shared/state/activityWindow";
import { resetTerminalForTests, terminalNode } from "@shared/state/terminalStore";
import type { TerminalState } from "@shared/state/terminalStore";
import type { DeploymentActivity, LiveActivity, SetActivity, SetActivityEvent } from "@shared/types/activity";

const ev = (sequence: number, setId = "a/one"): SetActivityEvent => ({
  sequence, at: "2026-09-07T14:02:00Z", level: "info", event: "commit",
  scope: "set", message: "line " + sequence, fields: { backup_set: setId }
});

const range = (from: number, to: number, setId?: string) =>
  from > to ? [] : Array.from({ length: to - from + 1 }, (_, i) => ev(from + i, setId));

/** A set's bucket. `held` is what the service says it holds, independent of
 *  what this reading hands over, which is how an idle reading looks. */
const setOf = (setId: string, events: SetActivityEvent[], held: [number, number], over: Partial<SetActivity> = {}): SetActivity => ({
  setId, active: false, stage: null, artifact: null, artifactsCompleted: 0, artifactsTotal: null, progressBasis: "unknown",
  bytesTransferred: null, bytesTotal: null, bytesPerSecond: null, failures: 0, outcome: null, startedAt: null, finishedAt: null,
  events, truncated: false, dropped: false, oldestSequence: held[0], latestSequence: held[1], ...over
});

const deployment = (events: SetActivityEvent[], held: [number, number]): DeploymentActivity => ({
  events, truncated: false, dropped: false, oldestSequence: held[0], latestSequence: held[1]
});

const reading = (sets: SetActivity[], over: Partial<LiveActivity> = {}): LiveActivity => ({
  observedAt: "2026-09-07T14:02:41Z", epoch: "p1", pollAfterMs: 1000, sets, deployment: deployment([], [0, 0]), ...over
});

const start = (): TerminalState => graph.read(terminalNode);

afterEach(() => resetTerminalForTests());

/** Two sets and a deployment bucket that have said something, then gone quiet. */
function filled(): TerminalState {
  return ingestReading(
    start(),
    reading(
      [setOf("a/one", range(1, 4, "a/one"), [1, 4]), setOf("b/two", range(5, 6, "b/two"), [5, 6])],
      { deployment: deployment([ev(7)], [7, 7]) }
    )
  );
}

const idle = (over: Partial<LiveActivity> = {}) =>
  reading([setOf("a/one", [], [1, 4]), setOf("b/two", [], [5, 6])], {
    observedAt: "2026-09-07T14:02:42Z", deployment: deployment([], [7, 7]), ...over
  });

describe("an idle reading keeps identity", () => {
  it("returns the same state object when no bucket brings anything", () => {
    const before = filled();
    expect(ingestReading(before, idle())).toBe(before);
  });

  it("keeps it across many idle readings, so nothing re-renders for a quiet minute", () => {
    const before = filled();
    let s = before;
    for (let i = 0; i < 20; i++) s = ingestReading(s, idle({ observedAt: "2026-09-07T14:03:" + String(i).padStart(2, "0") + "Z" }));
    expect(s).toBe(before);
  });

  it("returns the previous window from mergeActivity when the bucket brings nothing and says the same", () => {
    const held = mergeActivity(undefined, setOf("a/one", range(1, 4), [1, 4]));
    expect(mergeActivity(held, setOf("a/one", [], [1, 4]))).toBe(held);
  });

  it("returns the previous docked window from foldReading on an idle reading", () => {
    const before = filled();
    expect(foldReading(before.combined, idle())).toBe(before.combined);
  });
});

describe("a reading that changes something replaces only that", () => {
  it("keeps the quiet set's window when the other set brings a line", () => {
    const before = filled();
    const after = ingestReading(before, reading(
      [setOf("a/one", [ev(8, "a/one")], [1, 8]), setOf("b/two", [], [5, 6])],
      { deployment: deployment([], [7, 7]) }
    ));
    expect(after).not.toBe(before);
    expect(after.sets["b/two"]).toBe(before.sets["b/two"]);
    expect(after.sets["a/one"]).not.toBe(before.sets["a/one"]);
    expect(after.sets["a/one"].events.map((e) => e.sequence)).toEqual([1, 2, 3, 4, 8]);
    expect(after.combined?.events.map((e) => e.sequence)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
  });

  it("is a new window when a scalar moves with no new lines (a progress bar advancing)", () => {
    const before = filled();
    const after = ingestReading(before, reading(
      [setOf("a/one", [], [1, 4], { active: true, artifactsCompleted: 1 }), setOf("b/two", [], [5, 6])],
      { deployment: deployment([], [7, 7]) }
    ));
    expect(after).not.toBe(before);
    expect(after.sets["a/one"].active).toBe(true);
    expect(after.sets["b/two"]).toBe(before.sets["b/two"]);
  });

  it("is a new state when the service asks for a different cadence", () => {
    const before = filled();
    const after = ingestReading(before, idle({ pollAfterMs: 250 }));
    expect(after).not.toBe(before);
    expect(after.pollAfterMs).toBe(250);
    expect(after.sets).toBe(before.sets);
    expect(after.combined).toBe(before.combined);
  });

  it("clears a held failure even when the reading itself is idle", () => {
    const failed: TerminalState = { ...filled(), error: { code: "unknown", message: "down" } as unknown as TerminalState["error"] };
    const after = ingestReading(failed, idle());
    expect(after).not.toBe(failed);
    expect(after.error).toBeNull();
  });

  it("keeps the set ids the dock's chips read when the same sets are configured", () => {
    const before = filled();
    const after = ingestReading(before, idle({ pollAfterMs: 250 }));
    expect(after.reading).toBe(before.reading);
    expect(after.reading?.setIds).toEqual(["a/one", "b/two"]);
  });
});

describe("appending in order gives the same window as the full merge", () => {
  it("appends lines above the held ones and trims to the buffer", () => {
    const held = mergeActivity(undefined, setOf("a/one", range(1, 4), [1, 4]), 5);
    const next = mergeActivity(held, setOf("a/one", range(5, 7), [1, 7]), 5);
    expect(next.events.map((e) => e.sequence)).toEqual([3, 4, 5, 6, 7]);
    expect(next.dropped).toBe(true);
    expect(next.oldestSequence).toBe(3);
  });

  it("still deduplicates and orders a reading that overlaps what is held", () => {
    const held = mergeActivity(undefined, setOf("a/one", [ev(1), ev(3), ev(5)], [1, 5]));
    const next = mergeActivity(held, setOf("a/one", [ev(2), ev(3), ev(6)], [1, 6]));
    expect(next.events.map((e) => e.sequence)).toEqual([1, 2, 3, 5, 6]);
  });
});
