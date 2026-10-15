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
 * # Why the person opens each link (the review of #1185)
 *
 * This used to click a hidden link for every backup, a beat apart. Two things
 * made that dishonest. The engine streams only so many downloads at once
 * (budget.DefaultMaxDownloadStreams, four unless the platform sets fewer) and
 * a grant takes its slot when the browser first follows it, so the one past
 * the ceiling failed. And a click the page makes after an await is not one
 * the person made, so a browser holds the second and later ones behind its
 * "allow several downloads" prompt while a grant that lasts two minutes runs
 * out. Meanwhile the page said "N downloads handed to your browser".
 *
 * So the page mints the grants and shows them as real links, one per backup,
 * that the person opens: every download is one they asked for, and what the
 * page counts is what it saw (a link opened), never a file arriving. A
 * selection is offered this way only up to the engine's ceiling.
 *
 * # What is left out, and said so
 *
 * A copy that has to be restored by the provider first is not downloaded: a
 * grant lasts minutes and a restore takes hours (the same reason the single
 * dialog switches the radio off for one). It is listed with that reason. A
 * grant the host refuses is listed with the host's own sentence. Neither stops
 * the rest.
 */
import type { AssuranceSettings, RetndApi } from "@shared/api/contracts";
import type { BackupArtifact } from "@shared/types/backup";
import { planOf } from "./copyPlan";
import { recoveryRefusal } from "./recoveryMessages";

/**
 * The engine's own default ceiling on downloads streamed at once
 * (core/internal/recovery/budget/budget.go, DefaultMaxDownloadStreams). Used
 * only when the engine did not report its ceiling (an older engine), because
 * the engine runs at its default unless a platform sets a lower one.
 */
export const DEFAULT_DOWNLOAD_STREAMS = 4;

/** The ceiling this page holds a selection to: the engine's, as it reports it. */
export function downloadCeilingOf(settings: Pick<AssuranceSettings, "maxDownloadStreams"> | null): number {
  const n = settings?.maxDownloadStreams ?? 0;
  return n > 0 ? n : DEFAULT_DOWNLOAD_STREAMS;
}

/** The backups of a selection that a browser download can take. */
export function downloadable(files: BackupArtifact[]): BackupArtifact[] {
  return files.filter((a) => !planOf(a).archived);
}

/** One minted link: single use, until `expiresAt`. */
export interface DownloadLink {
  id: string;
  filename: string;
  url: string;
  expiresAt: string;
}

export interface MintedDownloads {
  links: DownloadLink[];
  /** The backups that got no link, each with the reason in words. */
  skipped: Array<{ id: string; filename: string; reason: string }>;
}

/**
 * Mints one download grant per backup, in order, and follows none of them.
 * Stops asking the host once `signal` is aborted (the page went away, or a
 * newer run replaced this one) and answers with what it had.
 */
export async function mintDownloadLinks(api: RetndApi, files: BackupArtifact[], signal: AbortSignal): Promise<MintedDownloads> {
  const out: MintedDownloads = { links: [], skipped: [] };
  for (const a of files) {
    if (signal.aborted) break;
    if (planOf(a).archived) {
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
      out.links.push({ id: a.id, filename: a.filename, url: m.downloadUrl, expiresAt: m.expiresAt });
    } catch (e) {
      out.skipped.push({ id: a.id, filename: a.filename, reason: recoveryRefusal(e, a.id.split("/").slice(0, 2).join("/")).message });
    }
  }
  return out;
}
