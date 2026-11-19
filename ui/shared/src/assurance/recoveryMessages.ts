/**
 * What each recovery refusal means, in words that say what to do next
 * (issue #985, #976).
 *
 * The service's own sentence is kept whenever there is one (api/failure.ts
 * shows what the service said, because a reason nobody anticipated beats one
 * this build invented). This table only adds the next step for the refusals
 * a recovery produces, and says whether asking again can help: a used or
 * expired grant is fixed by minting a new one, a full staging filesystem is
 * not fixed by anything the page can do.
 */
import { apiErrorOf, describeFailure } from "@shared/api/failure";
import { snapshotRestoreCommand } from "./assuranceCommands";

export interface RecoveryRefusal {
  /** The service's sentence, or a plain one when it said nothing usable. */
  message: string;
  /** What to do about it. */
  next?: string;
  /** A terminal command that does what the page cannot. */
  command?: string;
  /** Whether asking for a fresh grant can succeed without anything else changing. */
  retryable: boolean;
  /** The service's code, published on the element for a test and for support. */
  code: string;
}

const NEXT: Record<string, { next: string; retryable: boolean }> = {
  RECOVERY_GRANT_USED: { next: "A grant works once. Ask for a new one.", retryable: true },
  RECOVERY_GRANT_EXPIRED: { next: "A grant lasts a few minutes. Ask for a new one.", retryable: true },
  RECOVERY_GRANT_UNKNOWN: { next: "The host has no such grant. Ask for a new one.", retryable: true },
  RECOVERY_GRANT_SESSION_ENDED: { next: "Sign in again, then ask for a new grant. A grant dies with the session that asked for it.", retryable: true },
  RECOVERY_TOO_MANY_GRANTS: { next: "Use or let some of the waiting recoveries expire, then try again.", retryable: true },
  RECOVERY_BUSY: { next: "The same grant is still being read. Wait for it to finish.", retryable: false },
  RECOVERY_READS_BUSY: { next: "Nothing was read and the grant is not spent. Try again in a moment.", retryable: true },
  RECOVERY_DESTINATION_BUSY: { next: "Another read against the same destination is running. Try again in a moment.", retryable: true },
  RECOVERY_DOWNLOADS_BUSY: { next: "The host streams only so many downloads at once. Try again in a moment, or recover to staging instead.", retryable: true },
  RECOVERY_NO_SPACE: { next: "Nothing was written. Free space under the recovery staging root, or recover to another destination.", retryable: false },
  RECOVERY_BELOW_RESERVE: { next: "Nothing was written: the copy would leave less than the reserve free. Free space, or recover to another destination.", retryable: false },
  RECOVERY_TARGET_EXISTS: { next: "A recovery never replaces anything. Pick another name or key.", retryable: false },
  RECOVERY_TARGET_INVALID: { next: "The name has to stay inside the staging root, and a destination has to be one this configuration declares.", retryable: false },
  RECOVERY_LOCAL_COPY_CORRUPT: { next: "The copy on this host failed its digest, so nothing was delivered. Try another copy if the backup has one.", retryable: false },
  RECOVERY_REMOTE_COPY_CORRUPT: { next: "The destination returned bytes that do not match what was recorded, so nothing was delivered. Try another copy if the backup has one.", retryable: false },
  RECOVERY_NOT_READABLE: { next: "The copy cannot be read right now. If it is archived, wait for the provider restore to finish.", retryable: false },
  RECOVERY_NO_COPY: { next: "This backup has no copy to recover yet.", retryable: false },
  RECOVERY_COPY_NOT_FOUND: { next: "The copy asked for is not one of this backup's copies.", retryable: false },
  RECOVERY_NO_DIGEST: { next: "No digest was recorded for this backup, so a read could not be checked and nothing was started.", retryable: false },
  RECOVERY_INTERRUPTED: { next: "The host stopped while reading. Nothing was delivered. Try again.", retryable: true },
  RECOVERY_UNAVAILABLE: { next: "This host cannot recover backups right now.", retryable: false },
  RECOVERY_FAILED: { next: "Nothing was delivered. The host's log has the reason.", retryable: true },
  // Issue #1191 and #1193's review: the source host.
  RECOVERY_SOURCE_HOST_UNAVAILABLE: { next: "This set has no source host to send the backup back to. Pick another destination.", retryable: false },
  RECOVERY_SOURCE_HOST_READ_ONLY: { next: "The account this set pulls with cannot write on its source, or the set is declared read_only. Pick another destination, or give that account write access.", retryable: false },
  RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED: { next: "The destination changed after it was acknowledged, or it was not acknowledged. Check where it goes and tick the box again.", retryable: true },
  RECOVERY_SOURCE_HOST_UNREACHABLE: { next: "retnd could not reach the source host, and nothing was written there. Try again in a moment.", retryable: true },
  RECOVERY_SOURCE_HOST_NO_SPACE: { next: "The source host does not have room for this and still keep its reserve. Nothing was read. Free space there, recover fewer backups, or pick another destination.", retryable: false },
  RECOVERY_SOURCE_HOST_PROBE_LEFT_FILE: { next: "A write check left its probe file on the source host, so retnd does not treat that host as writable. Delete the file the message names, then try again.", retryable: false }
};

export function recoveryRefusal(e: unknown, setId: string): RecoveryRefusal {
  const code = apiErrorOf(e)?.code ?? "unknown";
  const failure = describeFailure(e, "The recovery could not be started.");
  if (code === "RECOVERY_USE_SNAPSHOT_RESTORE") {
    return {
      code, retryable: false,
      message: failure.message,
      next: "This backup set keeps restore points, not files. Restore from the terminal.",
      command: snapshotRestoreCommand(setId)
    };
  }
  const known = NEXT[code];
  return {
    code,
    message: failure.message,
    next: known?.next ?? failure.remediation,
    retryable: known?.retryable ?? false
  };
}
