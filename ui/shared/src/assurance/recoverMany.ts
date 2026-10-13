/**
 * Recover several backups to ONE destination, exactly as if each had been
 * recovered on its own (issue #1107).
 *
 * # What this is, and what it is not
 *
 * It is the existing single recovery (mint a grant, redeem it) run once per
 * backup, one after another. Every file therefore goes through the same
 * checks and ends in the same outcomes as a single recovery: the host reads
 * the copy in full, compares it to the digest recorded at ingest, and
 * writes nothing unless they match. One file failing does not stop the
 * others; each failure keeps its own reason.
 *
 * This is the seam where the service's own multi-file recovery (issue
 * #1106) plugs in: that call takes a list of backups and one destination
 * and records one operation. Until a page can call it, this loop is the
 * truthful implementation of the same contract from the browser, and it is
 * the only place the page knows how a selection becomes recoveries.
 *
 * # Where the files go
 *
 * Under staging, each backup lands at `<name>/<backup id>`: the staging
 * area accepts a path under its root and the backup id (source/set/file)
 * is unique, so two sets that both hold a `daily.tar` do not meet. For a
 * configured destination every backup is written to that destination.
 */
import type { RetndApi } from "@shared/api/contracts";
import { recoveryRefusal } from "./recoveryMessages";

export type ItemState = "queued" | "running" | "recovered" | "failed" | "restoring" | "cancelled";

export interface RunItem {
  id: string;
  filename: string;
  state: ItemState;
  /** Where the host placed it. */
  placed?: string;
  bytes?: number;
  sha256?: string;
  /** The reason, for a failure. */
  reason?: string;
}

export interface RunTarget {
  kind: "staging" | "storage";
  where: string;
}

export async function recoverMany(
  api: RetndApi,
  ids: string[],
  target: RunTarget,
  acknowledged: boolean,
  hooks: { update(id: string, patch: Partial<RunItem>): void; cancelled(): boolean }
): Promise<void> {
  for (const id of ids) {
    if (hooks.cancelled()) {
      hooks.update(id, { state: "cancelled" });
      continue;
    }
    hooks.update(id, { state: "running" });
    try {
      const spec = target.kind === "staging" ? { kind: "staging" as const, path: target.where + "/" + id } : { kind: "storage" as const, medium: target.where };
      const m = await api.mintRecovery(id, { target: spec, acknowledged: acknowledged ? true : undefined });
      if (m.kind === "restore") {
        hooks.update(id, { state: "restoring" });
        continue;
      }
      const placement = await api.redeemRecovery(m.grant);
      hooks.update(id, { state: "recovered", placed: placement.path ?? placement.key ?? "", bytes: placement.bytes, sha256: placement.sha256 });
    } catch (e) {
      hooks.update(id, { state: "failed", reason: recoveryRefusal(e, id.split("/").slice(0, 2).join("/")).message });
    }
  }
}
