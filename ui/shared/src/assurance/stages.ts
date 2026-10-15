/**
 * The named stages of a recovery and a recovery test, as the engine reports
 * them (issue #985).
 *
 * This is a copy of `obs.RecoveryStages` (core/internal/obs/assurance_events.go),
 * which is the single list the CLI's --follow and the browser both render
 * from. A copy because the browser cannot import Go; kept honest by
 * stages.test.ts, which parses the Go file and fails when the two differ in
 * a name, a label or an order.
 */
import type { Operation } from "./types";

export interface Stage { readonly id: string; readonly label: string }

export const RECOVERY_STAGES = [
  { id: "resolving_copy", label: "Resolving copy" },
  { id: "restoring_archive", label: "Restoring archive" },
  { id: "reading", label: "Reading" },
  { id: "verifying", label: "Verifying" },
  { id: "decrypting", label: "Decrypting" },
  { id: "writing_target", label: "Writing target" },
  { id: "validating", label: "Validating" },
  { id: "cleaning_temporary_files", label: "Cleaning temporary files" },
  { id: "complete", label: "Complete" }
] as const satisfies readonly Stage[];

/** The recovery's stage ids, in order, worked out once. */
export const RECOVERY_STAGE_IDS: readonly string[] = RECOVERY_STAGES.map((s) => s.id);

/** The label for a stage or event id, or the id itself for one this build
 *  does not name. An unknown stage is shown as received, never hidden. */
export function stageLabel(id: string): string {
  return RECOVERY_STAGES.find((s) => s.id === id)?.label ?? id;
}

/** The stages a recovery test goes through, in the engine's order: it
 *  re-reads the staged copy, checks the digest, runs the operator's script
 *  and cleans up. Restoring an archive is never among them, because a
 *  scheduled test never thaws. */
export const RECOVERY_TEST_STAGES = [
  "resolving_copy", "reading", "verifying", "validating", "cleaning_temporary_files", "complete"
] as const;

/** The stage every list ends on. It is the outcome, not a piece of work, so
 *  it is not counted as a stage the run goes through. */
const OUTCOME_STAGE = "complete";

/** How far along a run is, said as a position among its working stages. */
export interface StageProgress {
  /** One-based position of the working stage the run is in, or null once it
   *  has reached the outcome. */
  stage: number | null;
  /** How many working stages the run goes through (the outcome excluded). */
  of: number;
  /** Whole percent, 0 when the first stage is entered and 100 at the outcome. */
  pct: number;
}

/** The kinds a stage position can be asked for: the operations the panel
 *  follows, plus a full recovery. */
export type StagedKind = Operation["kind"] | "recovery";

const STAGE_LISTS: Record<StagedKind, readonly string[] | null> = {
  recovery_test: RECOVERY_TEST_STAGES,
  recovery: RECOVERY_STAGE_IDS,
  scrub: null
};

/**
 * Progress for a run that reports stages and no byte counts.
 *
 * The engine names the stage a recovery test is in on the feed but sends no
 * running byte total, so the honest bar is a position in the engine's own
 * list: entering the first stage is 0% and the outcome (`complete`) is 100%.
 * The count says "Stage k of 5" over the stages that do work; reaching the
 * outcome is not a sixth stage, and "Step 6 of 6" beside "finished" read as
 * one more thing still to do. A stage this build does not name, or a kind
 * with no stage list (a scrub), has no position, and the caller keeps the
 * indeterminate bar rather than invent one.
 */
export function stageProgress(kind: StagedKind, stage: string): StageProgress | null {
  const list = STAGE_LISTS[kind];
  if (!list) return null;
  const i = list.indexOf(stage);
  if (i < 0) return null;
  const working = list.length - 1;
  return { stage: stage === OUTCOME_STAGE ? null : i + 1, of: working, pct: Math.round((i / working) * 100) };
}
