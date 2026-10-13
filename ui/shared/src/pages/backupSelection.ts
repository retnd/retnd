/**
 * What a selection on the Backups list may contain (issue #1107).
 *
 * A row is selectable when a recovery of it can start: it has a copy that is
 * readable now, or one that can be asked for from an archive class (which
 * costs money and is acknowledged once for the whole selection). A backup
 * with no copy, or whose every copy is out of reach, cannot be recovered, so
 * it is not selectable and the reason is shown, never skipped silently.
 */
import type { BackupArtifact } from "@shared/types/backup";
import { planOf } from "@shared/assurance/copyPlan";

export function unselectableReason(a: BackupArtifact): string | null {
  const plan = planOf(a);
  if (plan.none) return "No copy of this backup is recorded yet, so there is nothing to recover.";
  if (plan.unreachable) return "This deployment cannot reach the place holding this backup's copy right now.";
  return null;
}

/** The calendar day a backup was received, read in the offset the record
 *  carries, so a filter by day means the same day on every machine. */
export function receivedDay(a: BackupArtifact): string {
  return a.receivedAt.slice(0, 10);
}

/** Inclusive on both ends; an empty bound is no bound. */
export function inReceivedRange(a: BackupArtifact, after: string, before: string): boolean {
  const day = receivedDay(a);
  return (!after || day >= after) && (!before || day <= before);
}
