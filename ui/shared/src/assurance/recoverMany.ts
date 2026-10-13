/**
 * Recover several backups to ONE destination, exactly as if each had been
 * recovered on its own (issue #1107), over the service's multi-file call
 * (issue #1106, `POST /recoveries`).
 *
 * The service does the work: every file is read in full and matched to the
 * digest recorded at ingest before anything is placed, a failing file is
 * reported and the rest carry on, and a manifest is written beside the files.
 * This module only folds the stream of progress lines into one row per backup,
 * so the dialog can draw them and the summary can count them.
 *
 * Files land under `<target>/<source>/<set>/`, so two sets that both hold a
 * `daily.tar` do not meet.
 */
import type { RetndApi, RecoverManyItem, RecoverManyResult } from "@shared/api/contracts";
import { recoveryRefusal } from "./recoveryMessages";

export type ItemState = "queued" | "running" | "recovered" | "failed" | "restoring" | "cancelled" | "excluded";

export interface RunItem {
  id: string;
  filename: string;
  state: ItemState;
  /** Where the host placed it. */
  placed?: string;
  bytes?: number;
  sha256?: string;
  /** The reason, for a failure or an exclusion. */
  reason?: string;
}

export interface RunTarget {
  kind: "staging" | "storage";
  where: string;
}

function stateOf(i: RecoverManyItem): ItemState {
  switch (i.status) {
    case "recovered": return "recovered";
    case "failed": return "failed";
    case "needs_thaw": return "restoring";
    case "skipped": return "cancelled";
    case "ready": return "queued";
    default: return "excluded";
  }
}

function patchOf(i: RecoverManyItem): Partial<RunItem> {
  return {
    state: stateOf(i),
    ...(i.path || i.key ? { placed: i.path ?? i.key } : {}),
    ...(i.bytes !== undefined ? { bytes: i.bytes } : {}),
    ...(i.sha256 ? { sha256: i.sha256 } : {}),
    ...(i.message ? { reason: i.message } : {})
  };
}

export interface RunOutcome {
  /** The service's refusal, when it refused before reading a byte. */
  refusal?: string;
  /** The result line, when the stream got as far as one. */
  result: RecoverManyResult | null;
  /** True when this side closed the connection. */
  aborted: boolean;
}

export async function runRecoverMany(
  api: RetndApi,
  ids: string[],
  target: RunTarget,
  acknowledged: boolean,
  signal: AbortSignal,
  hooks: { update(id: string, patch: Partial<RunItem>): void; progress(doneBytes: number, totalBytes: number): void }
): Promise<RunOutcome> {
  try {
    const result = await api.recoverMany(
      {
        artifactIds: ids,
        target: target.kind === "staging" ? { kind: "staging", path: target.where } : { kind: "storage", medium: target.where },
        acknowledged: acknowledged ? true : undefined
      },
      (e) => {
        if (e.event === "plan") {
          for (const i of e.plan.items) if (i.status !== "ready") hooks.update(i.artifactId, patchOf(i));
        } else if (e.event === "start") {
          hooks.update(e.item.artifactId, { state: "running" });
        } else if (e.event === "item") {
          hooks.update(e.item.artifactId, patchOf(e.item));
          hooks.progress(e.doneBytes, e.totalBytes);
        } else {
          // The result is the authority: the summary and the manifest on disk
          // are built from these same rows.
          for (const i of e.result.items) hooks.update(i.artifactId, patchOf(i));
        }
      },
      signal
    );
    return { result, aborted: signal.aborted };
  } catch (e) {
    if (signal.aborted) return { result: null, aborted: true };
    return { refusal: recoveryRefusal(e, "").message, result: null, aborted: false };
  }
}
