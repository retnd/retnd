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

/** A scrub pass is not a staged operation: it is a start line, one line per
 *  copy that did not pass, and an end line (obs.EventScrubStart and friends). */
export const SCRUB_EVENTS: readonly Stage[] = [
  { id: "scrub_start", label: "Scrub started" },
  { id: "scrub_copy", label: "Copy not passed" },
  { id: "scrub_end", label: "Scrub finished" }
];

/** The label for a stage or event id, or the id itself for one this build
 *  does not name. An unknown stage is shown as received, never hidden. */
export function stageLabel(id: string): string {
  return [...RECOVERY_STAGES, ...SCRUB_EVENTS].find((s) => s.id === id)?.label ?? id;
}

/** The stages a recovery test goes through, in the engine's order: it
 *  re-reads the staged copy, checks the digest, runs the operator's script
 *  and cleans up. Restoring an archive is never among them, because a
 *  scheduled test never thaws. */
export const RECOVERY_TEST_STAGES: readonly string[] = [
  "resolving_copy", "reading", "verifying", "validating", "cleaning_temporary_files", "complete"
];
