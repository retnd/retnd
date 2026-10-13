/**
 * What a backup set's cards say for the four fields the server does not
 * report (issue #1134), shared by the Backup Sets card and the set's page so
 * the two cannot word a gap differently.
 *
 * A gap is said as a gap and says why. "never" for a set that has a known-good
 * backup from eleven hours ago is the one word that is wrong in every
 * deployment, so a missing last run reads "Not recorded", never "never".
 */
import { bytes, relativeAge } from "@shared/utilities/format";
import type { BackupSet } from "@shared/types/backup";
import type { SetFacts } from "./verificationModel";

export function lastRunText(set: BackupSet): string {
  return set.lastRunAt === null ? "Not recorded" : relativeAge(set.lastRunAt);
}

export function retainedText(set: BackupSet, facts: SetFacts | null): string {
  if (set.retainedCount !== null && set.retainedBytes !== null) return set.retainedCount + " · " + bytes(set.retainedBytes);
  if (facts && facts.backups > 0) return facts.backups + (facts.backups === 1 ? " backup" : " backups") + ", size not reported";
  return "Not reported";
}

export function expectedEveryText(set: BackupSet, prefix = ""): string {
  if (set.expectedIntervalHours !== null) return prefix + set.expectedIntervalHours + "h";
  const secs = set.staleAfterSeconds;
  if (secs && secs > 0) {
    const hours = secs / 3600;
    const words = hours >= 1 ? (Number.isInteger(hours) ? hours + "h" : hours.toFixed(1) + "h") : Math.round(secs / 60) + "m";
    return "Not reported (stale after " + words + ")";
  }
  return "Not reported";
}
