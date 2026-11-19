/**
 * What a backup's copies allow a recovery to do (issue #985), read off their
 * placements. Nothing here is a price or a time: the placements carry neither.
 */
import type { BackupArtifact } from "@shared/types/backup";

/** How often the dialog re-reads the copy while a provider restore runs. */
export const THAW_POLL_MS = 2000;

/** What the backup's copies allow, read off their placements. */
export interface CopyPlan {
  /** No copy is recorded at all. */
  none: boolean;
  /** At least one copy can be read now, so a recovery needs no restore. */
  readable: boolean;
  /** No copy can be read now and one needs (or is having) a provider restore. */
  archived: boolean;
  /** The provider is restoring a copy right now. */
  restoring: boolean;
  /** Copies exist and none can be reached or restored. */
  unreachable: boolean;
  /** Where the archived copy is, for the sentence that names it. */
  where?: { medium: string; storageClass: string };
}

export function planOf(a: BackupArtifact): CopyPlan {
  const live = a.placements.filter((p) => p.status !== "DELETE_PENDING");
  const readable = live.some((p) => p.access === "immediate");
  const cold = live.find((p) => p.access === "requires_restore" || p.access === "restoring");
  return {
    none: live.length === 0,
    readable,
    archived: !readable && !!cold,
    restoring: !readable && live.some((p) => p.access === "restoring"),
    unreachable: live.length > 0 && !readable && !cold,
    where: cold ? { medium: cold.medium, storageClass: cold.storageClass } : undefined
  };
}
