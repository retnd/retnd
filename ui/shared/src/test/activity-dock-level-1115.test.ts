/**
 * Issue #1115: the logic behind the dock's level toggles. The browser
 * behaviour is in e2e/1115-dock-level-filter.spec.ts; this pins the rules
 * that do not need a layout: what counts, what a level keeps, and that a new
 * line meets an active filter the same way an old one does.
 */
import { describe, expect, it } from "vitest";
import { countLevels, filterByLevel, levelStatus } from "@shared/components/ActivityDock";
import type { DockEntry } from "@shared/components/ActivityDock";
import type { SetActivityEvent } from "@shared/types/activity";
import type { BrowserNotice } from "@shared/state/browserNotices";

function ev(sequence: number, level: SetActivityEvent["level"]): DockEntry {
  return {
    kind: "event",
    event: { sequence, at: "2026-09-07T14:02:00Z", level, event: "commit", scope: "set", message: "m" + sequence, fields: {} }
  } as DockEntry;
}
const restart: DockEntry = { kind: "restart", at: "2026-09-07T14:03:00Z" };

describe("#1115 counting", () => {
  it("counts every warning and error, and nothing else", () => {
    const all = [ev(1, "info"), ev(2, "warn"), ev(3, "error"), ev(4, "warn"), restart];
    expect(countLevels(all)).toEqual({ errors: 1, warnings: 2 });
  });
  it("counts a refusal this browser wrote as the warning it is", () => {
    const notice = { kind: "notice", notice: { at: "2026-09-07T14:00:00Z", what: "x", status: 403 } } as unknown as DockEntry;
    expect(countLevels([notice]).warnings).toBe(1);
    expect(notice).toBeTruthy();
    void ({} as BrowserNotice);
  });
});

describe("#1115 filtering", () => {
  const all = [ev(1, "info"), ev(2, "warn"), ev(3, "error"), ev(4, "info"), ev(5, "warn"), restart];
  it("no level keeps everything, restart rules included", () => {
    expect(filterByLevel(all, null)).toHaveLength(all.length);
  });
  it("warn keeps only warnings", () => {
    const kept = filterByLevel(all, "warn");
    expect(kept.map((e) => (e.kind === "event" ? e.event.sequence : -1))).toEqual([2, 5]);
  });
  it("error keeps only errors", () => {
    expect(filterByLevel(all, "error")).toHaveLength(1);
  });
  it("a new line meets the filter like an old one: a match appears, a non-match does not, the counts include both", () => {
    const next = [...all, ev(6, "warn"), ev(7, "info")];
    expect(filterByLevel(next, "warn")).toHaveLength(3);
    expect(countLevels(next)).toEqual({ errors: 1, warnings: 3 });
  });
});

describe("#1115 the announcement", () => {
  it("says N of M and the level", () => {
    expect(levelStatus("warn", 3, 41)).toBe("Showing 3 of 41 lines, warnings only");
    expect(levelStatus("error", 1, 41)).toBe("Showing 1 of 41 lines, errors only");
    expect(levelStatus(null, 41, 41)).toBe("Showing all 41 lines");
  });
});
