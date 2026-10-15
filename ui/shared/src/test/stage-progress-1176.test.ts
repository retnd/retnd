import { describe, expect, it } from "vitest";
import { RECOVERY_STAGE_IDS, RECOVERY_TEST_STAGES, stageProgress } from "@shared/assurance/stages";

describe("stageProgress (issue #1176)", () => {
  it("walks a recovery test from 0 to 100 through its own stages, in the engine's order", () => {
    const pct = RECOVERY_TEST_STAGES.map((s) => stageProgress("recovery_test", s)!.pct);
    expect(pct).toEqual([0, 20, 40, 60, 80, 100]);
    expect(stageProgress("recovery_test", "reading")).toEqual({ stage: 2, of: 5, pct: 20 });
  });

  it("counts only the stages that do work, so the count reads Stage k of 5 and the outcome is not a stage", () => {
    expect(RECOVERY_TEST_STAGES.map((s) => stageProgress("recovery_test", s)!.stage)).toEqual([1, 2, 3, 4, 5, null]);
    expect(stageProgress("recovery_test", "resolving_copy")).toEqual({ stage: 1, of: 5, pct: 0 });
    expect(stageProgress("recovery_test", "complete")).toEqual({ stage: null, of: 5, pct: 100 });
  });

  it("counts a recovery against the full list, which includes the restore a test never does", () => {
    const working = RECOVERY_STAGE_IDS.length - 1;
    expect(stageProgress("recovery", "complete")).toEqual({ stage: null, of: working, pct: 100 });
    expect(stageProgress("recovery", "restoring_archive")?.stage).toBe(2);
  });

  it("claims no position for a stage this build does not name, or for a scrub", () => {
    expect(stageProgress("recovery_test", "something_new")).toBeNull();
    expect(stageProgress("recovery_test", "restoring_archive")).toBeNull();
    expect(stageProgress("scrub", "scrub")).toBeNull();
  });
});
