import { describe, expect, it } from "vitest";
import { RECOVERY_STAGES, RECOVERY_TEST_STAGES, stageProgress } from "@shared/assurance/stages";

describe("stageProgress (issue #1176)", () => {
  it("walks a recovery test from 0 to 100 through its own stages, in the engine's order", () => {
    const pct = RECOVERY_TEST_STAGES.map((s) => stageProgress("recovery_test", s)!.pct);
    expect(pct).toEqual([0, 20, 40, 60, 80, 100]);
    expect(RECOVERY_TEST_STAGES.map((s) => stageProgress("recovery_test", s)!.step)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(stageProgress("recovery_test", "reading")).toEqual({ step: 2, of: 6, pct: 20 });
  });

  it("counts a recovery against the full list, which includes the restore a test never does", () => {
    expect(stageProgress("recovery", "complete")).toEqual({ step: RECOVERY_STAGES.length, of: RECOVERY_STAGES.length, pct: 100 });
    expect(stageProgress("recovery", "restoring_archive")?.step).toBe(2);
  });

  it("claims no position for a stage this build does not name, or for a scrub", () => {
    expect(stageProgress("recovery_test", "something_new")).toBeNull();
    expect(stageProgress("recovery_test", "restoring_archive")).toBeNull();
    expect(stageProgress("scrub", "scrub")).toBeNull();
  });
});
