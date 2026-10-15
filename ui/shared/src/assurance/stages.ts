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
export interface Stage { id: string; label: string }

export const RECOVERY_STAGES: readonly Stage[] = [
  { id: "resolving_copy", label: "Resolving copy" },
  { id: "restoring_archive", label: "Restoring archive" },
  { id: "reading", label: "Reading" },
  { id: "verifying", label: "Verifying" },
  { id: "decrypting", label: "Decrypting" },
  { id: "writing_target", label: "Writing target" },
  { id: "validating", label: "Validating" },
  { id: "cleaning_temporary_files", label: "Cleaning temporary files" },
  { id: "complete", label: "Complete" }
];

/** The label for a stage or event id, or the id itself for one this build
 *  does not name. An unknown stage is shown as received, never hidden. */
export function stageLabel(id: string): string {
  return RECOVERY_STAGES.find((s) => s.id === id)?.label ?? id;
}

/** The stages a recovery test goes through, in the engine's order: it
 *  re-reads the staged copy, checks the digest, runs the operator's script
 *  and cleans up. Restoring an archive is never among them, because a
 *  scheduled test never thaws. */
export const RECOVERY_TEST_STAGES: readonly string[] = [
  "resolving_copy", "reading", "verifying", "validating", "cleaning_temporary_files", "complete"
];

/** How far along a run is, said as a position in its own stage list. */
export interface StageProgress {
  /** One-based position of the stage the run is in. */
  step: number;
  /** How many stages the run goes through. */
  of: number;
  /** Whole percent, 0 when the first stage is entered and 100 at the last. */
  pct: number;
}

/**
 * Progress for a run that reports stages and no byte counts.
 *
 * The engine names the stage a recovery test is in on the feed but sends no
 * running byte total, so the honest bar is a position in the engine's own
 * list: entering the first stage is 0% and the last (`complete`) is 100%. A
 * stage this build does not name, or a kind with no stage list (a scrub),
 * has no position, and the caller keeps the indeterminate bar rather than
 * invent one.
 */
export function stageProgress(kind: "scrub" | "recovery_test" | "recovery", stage: string): StageProgress | null {
  const list: readonly string[] | null =
    kind === "recovery_test" ? RECOVERY_TEST_STAGES : kind === "recovery" ? RECOVERY_STAGES.map((s) => s.id) : null;
  if (!list) return null;
  const i = list.indexOf(stage);
  if (i < 0) return null;
  return { step: i + 1, of: list.length, pct: Math.round((i / (list.length - 1)) * 100) };
}
