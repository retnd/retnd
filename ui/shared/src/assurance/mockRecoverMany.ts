/**
 * The in-process stand-in for POST /recoveries (#1106, #1107), used by the
 * development build and the browser specs.
 *
 * It classifies rows the way core/service/recovermany.go does (a restore
 * point is COMMITTED, REMOTE_DELETE_PENDING, COMPLETE or REMOTE_RETAINED;
 * quarantined and lost rows are listed and never included; an archived copy
 * needs a thaw), refuses for cost and for space with the numbers in the
 * sentence, and then "recovers" file by file with a pause between files so a
 * spec can watch the running total and press Cancel. The digests it reports
 * are made up from the file name: it reads no bytes, and a spec must never
 * treat them as evidence about anything but the screen.
 */
import { RetndError } from "@shared/api/contracts";
import type { ApiErrorCode } from "@shared/api/contracts";
import type {
  RecoverManyItem, RecoverManyPlan, RecoverManyProgress, RecoverManyRequest, RecoverManyResult, RecoverManyTotals
} from "@shared/api/contracts";
import type { BackupArtifact } from "@shared/types/backup";

const RESTORE_POINTS = new Set(["COMMITTED", "REMOTE_DELETE_PENDING", "COMPLETE", "REMOTE_RETAINED"]);
const RESERVE = 1_000_000_000;

const refuse = (code: string, message: string, status: number) =>
  new RetndError({ code: code as ApiErrorCode, message, status, correlationId: "cid_mock" + code.toLowerCase() });

/** Four extra backups for the postgres set, so a set recover has history to
 *  show: three restore points and one the validator rejected. They exist only
 *  here; the Backups list is untouched. */
export function historyRows(base: BackupArtifact[]): BackupArtifact[] {
  const first = base[0];
  if (!first) return base;
  const make = (day: string, over: Partial<BackupArtifact>): BackupArtifact => ({
    ...first,
    id: first.setId + "/postgres-prod-" + day + ".dump.zst",
    filename: "postgres-prod-" + day + ".dump.zst",
    producedAt: "2026-08-" + day.slice(6) + "T01:58:44+02:00",
    receivedAt: "2026-08-" + day.slice(6) + "T02:00:53+02:00",
    sizeBytes: 15_000_000_000 + Number(day.slice(6)) * 1_000_000,
    ...over
  });
  return [
    make("20260825", { state: "QUARANTINED", validation: "failed", quarantine: { reason: "checksum-mismatch", detail: "sha256 mismatch", detectedAt: "2026-08-25T02:05:00+02:00", remoteSourceRetained: true } }),
    make("20260826", {}),
    make("20260827", {}),
    ...base
  ];
}

function classify(a: BackupArtifact): RecoverManyItem {
  const it: RecoverManyItem = { artifactId: a.id, name: a.filename, status: "ready", sizeBytes: a.sizeBytes };
  if (a.state === "QUARANTINED_LOST") {
    return { ...it, status: "unrecoverable", code: "ARTIFACT_IRRECOVERABLE", message: "this backup was lost: its only copy is gone and nothing remains to re-ingest it from" };
  }
  if (a.quarantine || a.state === "QUARANTINED") {
    return { ...it, status: "quarantined", code: "ARTIFACT_QUARANTINED", message: a.quarantine?.detail ?? "this backup is quarantined, so it is not a restore point" };
  }
  if (!RESTORE_POINTS.has(a.state)) {
    return { ...it, status: "skipped", code: "NOT_A_RESTORE_POINT", message: "this backup is " + a.state + ", which is not a restore point" };
  }
  const live = a.placements.filter((p) => p.status === "ACTIVE");
  const local = live.find((p) => p.mediumType === "local" && p.access === "immediate");
  const remote = live.find((p) => p.access === "immediate");
  const cold = live.find((p) => p.access === "requires_restore");
  if (local) return { ...it, copy: local.medium };
  if (remote) return { ...it, copy: remote.medium, chargeable: true };
  if (cold) return { ...it, copy: cold.medium, status: "needs_thaw", chargeable: true };
  return { ...it, status: "unrecoverable", code: "RECOVERY_NOT_READABLE", message: "no copy of this backup can be read right now" };
}

function totalsOf(items: RecoverManyItem[]): RecoverManyTotals {
  const n = (s: RecoverManyItem["status"]) => items.filter((i) => i.status === s).length;
  return {
    count: items.length, ready: n("ready"), recovered: n("recovered"), failed: n("failed"), skipped: n("skipped"),
    needsThaw: n("needs_thaw"), quarantined: n("quarantined"), unrecoverable: n("unrecoverable"),
    bytes: items.reduce((t, i) => t + (i.status === "ready" ? (i.sizeBytes ?? 0) : i.status === "recovered" ? (i.bytes ?? 0) : 0), 0)
  };
}

function digestOf(name: string): string {
  let h = 2166136261;
  let out = "";
  for (let r = 0; r < 8; r++) {
    for (let i = 0; i < name.length; i++) { h ^= name.charCodeAt(i) + r; h = Math.imul(h, 16777619) >>> 0; }
    out += h.toString(16).padStart(8, "0");
  }
  return out;
}

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve) => {
    if (signal?.aborted) { resolve(); return; }
    const t = setTimeout(done, ms);
    function done() { signal?.removeEventListener("abort", done); clearTimeout(t); resolve(); }
    signal?.addEventListener("abort", done);
  });

export function createMockRecoverMany(scenario: string, rowsFor: (setId: string) => BackupArtifact[]) {
  const rows = (req: RecoverManyRequest): BackupArtifact[] => {
    if (req.set) {
      const base = rowsFor(req.set);
      const all = scenario.startsWith("assurance-set-") && req.set === "production/postgres-primary" ? historyRows(base) : base;
      let out = all;
      if (req.since) out = out.filter((a) => a.receivedAt >= req.since!);
      if (req.newestOnly) {
        const good = out.filter((a) => RESTORE_POINTS.has(a.state) && !a.quarantine);
        const newest = good.reduce<BackupArtifact | undefined>((m, a) => (!m || a.receivedAt > m.receivedAt ? a : m), undefined);
        out = out.filter((a) => !RESTORE_POINTS.has(a.state) || a === newest);
      }
      return out;
    }
    const wanted = new Set(req.artifacts ?? []);
    const sets = new Set([...wanted].map((id) => id.split("/").slice(0, 2).join("/")));
    const all = [...sets].flatMap((s) => {
      const base = rowsFor(s);
      return scenario.startsWith("assurance-set-") && s === "production/postgres-primary" ? historyRows(base) : base;
    });
    return all.filter((a) => wanted.has(a.id));
  };

  const folderOf = (req: RecoverManyRequest, items: RecoverManyItem[]) => {
    const base = req.target.kind === "storage" ? (req.target.key || "recovered") : (req.target.path ?? "");
    const sets = new Set(items.map((i) => i.artifactId.split("/").slice(0, 2).join("/")));
    return sets.size === 1 ? base + "/" + [...sets][0] : base;
  };

  const plan = async (req: RecoverManyRequest): Promise<RecoverManyPlan> => {
    if (scenario === "assurance-snapshot-set") {
      throw refuse("RECOVERY_USE_SNAPSHOT_RESTORE", "This backup set is stored by the incremental engine, whose whole-set restore is a snapshot restore and not a copy of files: use `retnd snapshot restore " + (req.set ?? "") + "` to extract one.", 409);
    }
    if (req.target.kind === "download" || !req.target.kind) {
      throw refuse("RECOVERY_TARGET_INVALID", "Several backups cannot go to one browser download. Recover them into a folder under the staging root, or onto another storage destination.", 400);
    }
    const found = rows(req);
    if (found.length === 0) throw refuse("RECOVERY_NO_COPY", "There is no backup in " + (req.set ?? "this request") + " to recover.", 409);
    const items = found.map(classify).sort((a, b) => a.name.localeCompare(b.name));
    const totals = totalsOf(items);
    const charge = items.filter((i) => i.chargeable && (i.status === "ready" || i.status === "needs_thaw"));
    const chargeableBytes = charge.reduce((t, i) => t + (i.sizeBytes ?? 0), 0);
    const freeBytes = scenario === "assurance-set-nospace" ? 40_000_000_000 : 2_000_000_000_000;
    const p: RecoverManyPlan = {
      ...(req.set ? { set: req.set } : {}),
      target: req.target, folder: folderOf(req, items), items, totals,
      chargeableCount: charge.length, chargeableBytes, unknownSizes: 0,
      ...(req.target.kind === "staging" ? { freeBytes } : {}),
      reserveBytes: RESERVE
    };
    if (charge.length > 0 && !req.acknowledged) {
      p.refusal = {
        code: "RECOVERY_COST_NOT_ACKNOWLEDGED",
        message: "reading " + charge.length + " of these " + totals.count + " files is billed (" + chargeableBytes + " bytes in all, and any archived copy needs a provider restore first); pass the acknowledgement once for the whole set, nothing was started"
      };
    } else if (req.target.kind === "staging" && totals.ready > 0 && totals.bytes > freeBytes) {
      p.refusal = {
        code: "RECOVERY_NO_SPACE",
        message: "the files need " + totals.bytes + " bytes (" + totals.ready + " files) and the staging disk has " + freeBytes + " bytes free, so nothing was read"
      };
    }
    return p;
  };

  return {
    planRecoverMany: plan,
    recoverMany: async (req: RecoverManyRequest, opts?: { onProgress?: (p: RecoverManyProgress) => void; signal?: AbortSignal }): Promise<RecoverManyResult> => {
      const p = await plan(req);
      if (p.refusal) throw refuse(p.refusal.code, p.refusal.message, p.refusal.code === "RECOVERY_NO_SPACE" ? 507 : 409);
      const emit = opts?.onProgress;
      emit?.({ kind: "plan", plan: p, totalBytes: p.totals.bytes });
      const items = p.items.map((i) => ({ ...i }));
      const actionable = items.filter((i) => i.status === "ready" || i.status === "needs_thaw").length;
      const perFile = scenario === "assurance-set-slow" ? 2500 : 700;
      let index = 0, done = 0, ready = 0, cancelled = false;
      for (const it of items) {
        if (it.status !== "ready" && it.status !== "needs_thaw") continue;
        index++;
        if (cancelled || opts?.signal?.aborted) {
          cancelled = true;
          Object.assign(it, { status: "skipped", code: "RECOVERY_INTERRUPTED", message: "the run was cancelled before this file was started" });
          continue;
        }
        emit?.({ kind: "start", index, total: actionable, item: { ...it }, doneBytes: done, totalBytes: p.totals.bytes });
        await sleep(perFile, opts?.signal);
        if (opts?.signal?.aborted) {
          cancelled = true;
          Object.assign(it, { status: "skipped", code: "RECOVERY_INTERRUPTED", message: "the run was cancelled while this file was being read; nothing of it was kept" });
          continue;
        }
        if (it.status === "needs_thaw") {
          Object.assign(it, { restoreOperationId: "op_restore_mock_" + index, message: "a provider restore was started; run this again once the copy is readable" });
        } else {
          ready++;
          if (scenario === "assurance-set-corrupt" && ready === 2) {
            Object.assign(it, { status: "failed", code: "RECOVERY_LOCAL_COPY_CORRUPT", message: "the copy on this host's disk does not match the checksum recorded when this backup was ingested (digest mismatch), so none of it was delivered" });
          } else {
            const folder = p.folder;
            const rel = (req.target.kind === "staging" ? folder + "/rcv-" + digestOf(it.name).slice(0, 8) + "-" + it.name : folder + "/" + it.name);
            Object.assign(it, {
              status: "recovered", bytes: it.sizeBytes ?? 0, sha256: digestOf(it.name), file: rel,
              ...(req.target.kind === "storage" ? { key: rel } : { path: "/data/recover-staging/" + rel })
            });
            done += it.sizeBytes ?? 0;
          }
        }
        emit?.({ kind: "item", index, total: actionable, item: { ...it }, doneBytes: done, totalBytes: p.totals.bytes });
      }
      // A cancelled run takes a moment to discard the file in flight and write
      // its manifest, and the page says it is cancelling meanwhile.
      if (cancelled) await sleep(600);
      const totals = totalsOf(items);
      totals.bytes = done;
      const manifest = p.folder + "/rcv-" + digestOf(p.folder).slice(0, 8) + "-recovery-manifest.json";
      return {
        operationId: "op_recover_many_mock", ...(req.set ? { set: req.set } : {}), target: req.target, folder: p.folder,
        items, totals, cancelled,
        ...(req.target.kind === "storage" ? { manifestKey: manifest } : { manifestPath: "/data/recover-staging/" + manifest }),
        manifestSha256: digestOf(manifest)
      };
    }
  };
}
