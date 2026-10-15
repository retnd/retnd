/**
 * "Download in this browser" for a selection of backups (issue #1179).
 *
 * # Why this is many downloads and not one
 *
 * One archive of the selection would be a second format the host's digest
 * check does not cover, so a selection is downloaded the way a single backup
 * is: a grant per backup, and the browser follows each grant's link itself.
 * Every file is checked against the digest the journal recorded as the host
 * sends it, exactly as a single download is, and the page can no more see the
 * browser's transfer here than it can there.
 *
 * # What is left out, and said so
 *
 * A copy that has to be restored by the provider first is not downloaded: a
 * grant lasts minutes and a restore takes hours (the same reason the single
 * dialog switches the radio off for one). It is listed with that reason. A
 * grant the host refuses is listed with the host's own sentence. Neither stops
 * the rest.
 *
 * Browsers may ask the person to allow several downloads from one page, and
 * may throttle a burst, so the links are followed a beat apart.
 */
import type { RetndApi } from "@shared/api/contracts";
import type { BackupArtifact } from "@shared/types/backup";
import { planOf } from "./copyPlan";
import { recoveryRefusal } from "./recoveryMessages";

export interface BulkDownloadOutcome {
  /** The backups whose download was handed to the browser, in order. */
  handed: Array<{ id: string; filename: string }>;
  /** The backups that were not, each with the reason in words. */
  skipped: Array<{ id: string; filename: string; reason: string }>;
}

/** How long to wait between two links. */
export const DOWNLOAD_BEAT_MS = 350;

function follow(url: string) {
  const a = document.createElement("a");
  a.href = url;
  a.rel = "noopener";
  a.download = "";
  a.click();
}

export async function downloadMany(
  api: RetndApi,
  files: BackupArtifact[],
  hooks: { onHanded?(done: number): void } = {}
): Promise<BulkDownloadOutcome> {
  const out: BulkDownloadOutcome = { handed: [], skipped: [] };
  for (const a of files) {
    const plan = planOf(a);
    if (plan.archived) {
      out.skipped.push({
        id: a.id, filename: a.filename,
        reason: "archived: the provider has to restore it first, which takes hours, and a download link lasts minutes"
      });
      continue;
    }
    try {
      const m = await api.mintRecovery(a.id, { target: { kind: "download" } });
      if (m.kind !== "grant") {
        out.skipped.push({ id: a.id, filename: a.filename, reason: "the host started a provider restore instead of a download" });
        continue;
      }
      follow(m.downloadUrl);
      out.handed.push({ id: a.id, filename: a.filename });
      hooks.onHanded?.(out.handed.length);
      await new Promise((r) => setTimeout(r, DOWNLOAD_BEAT_MS));
    } catch (e) {
      out.skipped.push({ id: a.id, filename: a.filename, reason: recoveryRefusal(e, a.id.split("/").slice(0, 2).join("/")).message });
    }
  }
  return out;
}
