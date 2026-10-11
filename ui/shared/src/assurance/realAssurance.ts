/**
 * The {@link AssuranceApi} a production client offers, composed from routes
 * that exist (issue #985).
 *
 * # What is real and what is not offered
 *
 * Real: each copy's achieved verification class, its time and its access
 * (the backup's placements), and the scrub schedule (GET /assurance/due).
 * From those this builds the Stored and Content claims.
 *
 * Not offered, and therefore not invented: a route to start a scrub or a
 * recovery test, to follow one, to read the recovery-test claim or to read a
 * validator's output. `offers` says so, the screens show the terminal command
 * that does exist, and every method that would need such a route rejects with
 * {@link NotOffered} rather than answering with a made-up value. The recovery
 * claim is `unknown` (or `never_configured` when the set's own policy turns
 * the test off), because "could not ask" is not a pass.
 */
import type { RetndApi } from "@shared/api/contracts";
import type { BackupArtifact } from "@shared/types/backup";
import type { AchievedClass, ArtifactAssurance, AssuranceApi, ClaimRecord } from "./types";

export class NotOffered extends Error {
  constructor(what: string) {
    super("This server has no route to " + what + " yet.");
    this.name = "NotOffered";
  }
}

const RANK: Record<string, number> = { content: 3, attested: 2, existence: 1 };

function splitSet(setId: string): [string, string] {
  const i = setId.indexOf("/");
  return [setId.slice(0, i), setId.slice(i + 1)];
}

/** The strongest class any readable-or-not copy has achieved, with the time it
 *  was achieved. Only a copy that records both counts: a class without a time
 *  is not evidence of anything. */
function strongest(a: BackupArtifact): { cls: AchievedClass; at?: string } {
  let best: { cls: AchievedClass; at?: string } = { cls: "" };
  for (const p of a.placements) {
    if (p.status === "DELETE_PENDING" || !p.verificationClass || !p.verifiedAt) continue;
    if ((RANK[p.verificationClass] ?? 0) > (RANK[best.cls] ?? 0)) best = { cls: p.verificationClass, at: p.verifiedAt };
  }
  return best;
}

/** One backup's Content claim. `overdue` and `neverAssured` come from the
 *  scrub schedule, which is the server's own arithmetic over the set's
 *  resolved cadence. */
export function contentClaim(a: BackupArtifact, due: { overdue: boolean; never: boolean } | undefined): ClaimRecord {
  const live = a.placements.filter((p) => p.status !== "DELETE_PENDING");
  // A quarantined backup failed a check the product ran itself.
  if (a.quarantine) return { status: "failed", achievedClass: "" };
  if (live.length === 0) return { status: "never_run", achievedClass: "" };
  const { cls, at } = strongest(a);
  if (cls && at) {
    return { status: due?.overdue && !due.never ? "overdue" : "pass", achievedClass: cls, at };
  }
  const readable = live.some((p) => p.access === "immediate");
  if (!readable) {
    return live.some((p) => p.access === "requires_restore" || p.access === "restoring")
      ? { status: "requires_restore", achievedClass: "" }
      : { status: "no_readable_copy", achievedClass: "" };
  }
  return { status: "never_run", achievedClass: "" };
}

export function createHttpAssurance(api: RetndApi): AssuranceApi {
  const compose = async (artifacts: BackupArtifact[]): Promise<ArtifactAssurance[]> => {
    const dueById = new Map<string, { overdue: boolean; never: boolean }>();
    try {
      const due = await api.listAssuranceDue();
      for (const i of due.items) dueById.set(i.artifactId, { overdue: i.overdue, never: !i.lastAssuredAt });
    } catch { /* no schedule: the claims below fall back to what the copies say */ }

    // The recovery claim has no route. The one thing that can be said is that a
    // set's own policy turned the test off, so ask each set once.
    const off = new Map<string, boolean>();
    await Promise.all([...new Set(artifacts.map((a) => a.setId))].map(async (setId) => {
      try {
        const [source, set] = splitSet(setId);
        off.set(setId, (await api.getBackupSetAssurance(source, set)).recoveryTest.mode === "off");
      } catch { off.set(setId, false); }
    }));

    return artifacts.map((a) => ({
      artifactId: a.id,
      copies: a.placements.filter((p) => p.status !== "DELETE_PENDING").length,
      content: contentClaim(a, dueById.get(a.id)),
      recovery: off.get(a.setId) ? { status: "never_configured", achievedClass: "" } : { status: "unknown", achievedClass: "" }
    }));
  };

  const refuse = (what: string) => () => Promise.reject(new NotOffered(what));
  return {
    offers: { operations: false, recoveryClaim: false },
    listArtifactAssurance: async (setId) => compose(await api.listArtifacts(setId)),
    getArtifactAssurance: async (id) => (await compose([await api.getArtifact(id)]))[0],
    startScrub: refuse("start a scrub from the browser"),
    startRecoveryTest: refuse("start a recovery test"),
    activeOperation: async () => null,
    readOperation: refuse("follow a scrub or a recovery test"),
    cancelOperation: refuse("cancel a scrub or a recovery test"),
    getValidatorResult: refuse("read a validator's output")
  };
}

/** The production client with the assurance seam attached. */
export function withHttpAssurance(api: RetndApi): RetndApi {
  return { ...api, assurance: createHttpAssurance(api) };
}
