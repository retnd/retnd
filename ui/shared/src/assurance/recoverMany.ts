/**
 * Recover several backups to ONE destination, exactly as if each had been
 * recovered on its own (issue #1107), over the service's multi-file call
 * (issue #1106, `POST /recoveries`; `RetndApi.recoverMany`).
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
  kind: "staging" | "storage" | "source";
  /** A folder, a destination id, or for the source host (#1191) the new
   *  retnd-recovered-* folder. */
  where: string;
  /** source: the yes to writing onto the host, and the password again where
   *  the engine re-checks it. */
  acknowledged?: boolean;
  stepUpPassword?: string;
}

function stateOf(i: RecoverManyItem): ItemState {
  switch (i.status) {
    case "recovered": return "recovered";
    case "failed": return "failed";
    case "needs_thaw": return "restoring";
    // A skipped row is either one the run never started (cancelled) or one
    // that is not a restore point and was never going to be.
    case "skipped": return i.code === "RECOVERY_INTERRUPTED" ? "cancelled" : "excluded";
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
  /** The result, when the run got as far as one (a cancelled run has one too). */
  result: RecoverManyResult | null;
  /** True when this side asked the run to stop. */
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
        artifacts: ids,
        target: target.kind === "staging" ? { kind: "staging", path: target.where }
          : target.kind === "source" ? { kind: "source", path: target.where, acknowledged: target.acknowledged === true }
          : { kind: "storage", medium: target.where },
        acknowledged: acknowledged ? true : undefined,
        ...(target.stepUpPassword ? { stepUpPassword: target.stepUpPassword } : {})
      },
      {
        signal,
        onProgress: (e) => {
          if (e.kind === "plan") {
            for (const i of e.plan.items) if (i.status !== "ready" && i.status !== "needs_thaw") hooks.update(i.artifactId, patchOf(i));
          } else if (e.kind === "start") {
            hooks.update(e.item.artifactId, { state: "running" });
          } else {
            hooks.update(e.item.artifactId, patchOf(e.item));
            hooks.progress(e.doneBytes, e.totalBytes);
          }
        }
      }
    );
    // The result is the authority: the summary and the manifest on disk are
    // built from these same rows.
    for (const i of result.items) hooks.update(i.artifactId, patchOf(i));
    return { result, aborted: signal.aborted };
  } catch (e) {
    if (signal.aborted) return { result: null, aborted: true };
    return { refusal: recoveryRefusal(e, "").message, result: null, aborted: false };
  }
}
