/**
 * The {@link AssuranceApi} a production client offers, composed from routes
 * that exist (issue #985).
 *
 * # What is real
 *
 * Each copy's achieved verification class, its time and its access (the
 * backup's placements), the copy's own scrub and recovery-test record (also
 * on the placement), the scrub schedule (GET /assurance/due), and the route
 * that starts a recovery test now (POST /backup-sets/{source}/{set}/recovery-test).
 * From those this builds the Stored, Content and Recovery claims, and the
 * Run recovery test control.
 *
 * # What is not offered, and therefore not invented
 *
 * A route to start a scrub, to cancel anything, or to read a validator's
 * output. `offers` says so, the screens show the terminal command that does
 * exist, and every method that would need such a route rejects with
 * {@link NotOffered} rather than answering with a made-up value.
 *
 * # Following a recovery test
 *
 * A recovery test answers 202 with an operation id and runs in the
 * background. Its start, stage and end lines carry that id as `op_id` on the
 * live activity feed, so this page follows it by reading that feed for the
 * set. The feed is a bounded in-memory tail, not a record, and this says so:
 * a run whose lines have fallen off it is reported as `untracked`, which is
 * not a verdict, and the Recovery chip is where the result lands.
 */
import type { RetndApi } from "@shared/api/contracts";
import type { SetActivityEvent } from "@shared/types/activity";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";
import { stageLabel } from "./stages";
import type {
  AchievedClass, ArtifactAssurance, AssuranceApi, ClaimRecord, CopyFailure, Operation, OperationEvent, OperationFeed
} from "./types";

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

const liveCopies = (a: BackupArtifact): BackupPlacement[] => a.placements.filter((p) => p.status !== "DELETE_PENDING");

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
 *  scrub schedule, which is the server's own arithmetic over the copies' own
 *  due times.
 *
 *  A copy whose last scrub FAILED is a failure of the claim, whatever another
 *  copy achieved: a failed scrub clears that copy's class and time, so without
 *  reading its recorded state it would be indistinguishable from a copy nobody
 *  had looked at ("Never run"). */
export function contentClaim(a: BackupArtifact, due: { overdue: boolean; never: boolean } | undefined): ClaimRecord {
  const live = liveCopies(a);
  // A quarantined backup failed a check the product ran itself.
  if (a.quarantine) return { status: "failed", achievedClass: "" };
  if (live.length === 0) return { status: "never_run", achievedClass: "" };
  const failed = live.filter((p) => p.assuranceState === "failed");
  if (failed.length > 0) return { status: "failed", achievedClass: "", at: newest(failed.map((p) => p.lastFailureAt ?? p.lastCheckedAt)) };
  const { cls, at } = strongest(a);
  if (cls && at) {
    return { status: due?.overdue && !due.never ? "overdue" : "pass", achievedClass: cls, at };
  }
  const incomplete = live.filter((p) => p.assuranceState === "could_not_complete");
  if (incomplete.length > 0) return { status: "could_not_complete", achievedClass: "", at: newest(incomplete.map((p) => p.lastFailureAt ?? p.lastCheckedAt)) };
  const readable = live.some((p) => p.access === "immediate");
  if (!readable) {
    return live.some((p) => p.access === "requires_restore" || p.access === "restoring")
      ? { status: "requires_restore", achievedClass: "" }
      : { status: "no_readable_copy", achievedClass: "" };
  }
  return { status: "never_run", achievedClass: "" };
}

/** The newest of some optional times, or undefined when there are none. */
function newest(times: Array<string | null | undefined>): string | undefined {
  const real = times.filter((t): t is string => !!t && !Number.isNaN(Date.parse(t)));
  return real.sort((x, y) => Date.parse(y) - Date.parse(x))[0];
}

/** One backup's Recovery claim, from its copies' own recovery records.
 *
 *  Worst first, because a failure on any copy is a fact about the backup:
 *  failed, then could-not-complete, then a pass (the newest one) or the other
 *  non-passing states. `policyOff` is the set's own policy turning the test
 *  off; `reported` says the server records the claim at all, so an empty
 *  record can mean "a test is owed and none has run" and not just "could not
 *  ask". */
export function recoveryClaim(a: BackupArtifact, policyOff: boolean, reported: boolean): ClaimRecord {
  const live = liveCopies(a);
  const states = live.map((p) => p.recoveryState).filter((s): s is NonNullable<BackupPlacement["recoveryState"]> => !!s);
  const when = (state: string) => newest(live.filter((p) => p.recoveryState === state).map((p) => p.recoveryTestedAt));
  if (states.includes("failed")) return { status: "failed", achievedClass: "", at: when("failed") };
  if (states.includes("could_not_complete")) return { status: "could_not_complete", achievedClass: "", at: when("could_not_complete") };
  if (states.includes("passed")) return { status: "pass", achievedClass: "content", at: when("passed") };
  if (states.includes("overdue")) return { status: "overdue", achievedClass: "", at: when("overdue") };
  if (states.includes("refused_for_cost")) return { status: "refused_for_cost", achievedClass: "", at: when("refused_for_cost") };
  if (policyOff) return { status: "never_configured", achievedClass: "" };
  if (states.includes("never_run") || (reported && live.length > 0)) return { status: "never_run", achievedClass: "" };
  if (states.includes("not_configured")) return { status: "never_configured", achievedClass: "" };
  return { status: "unknown", achievedClass: "" };
}

/** Every copy whose last check did not pass, said about that copy. */
export function copyFailures(a: BackupArtifact): CopyFailure[] {
  const out: CopyFailure[] = [];
  for (const p of liveCopies(a)) {
    if (p.assuranceState === "failed" || p.assuranceState === "could_not_complete") {
      out.push({
        medium: p.medium, kind: "scrub", state: p.assuranceState, reason: p.lastFailureReason ?? "",
        consecutiveFailures: p.consecutiveFailures ?? 0, at: p.lastFailureAt ?? p.lastCheckedAt ?? undefined
      });
    }
    if (p.recoveryState === "failed" || p.recoveryState === "could_not_complete") {
      out.push({
        medium: p.medium, kind: "recovery_test", state: p.recoveryState, reason: p.recoveryFailureReason ?? "",
        consecutiveFailures: 0, at: p.recoveryTestedAt ?? undefined
      });
    }
  }
  return out;
}

/** How long after starting a test its first line may take to show up on the
 *  feed before the page stops saying it is simply waiting. */
export const START_GRACE_MS = 20_000;

const startLine = (e: SetActivityEvent) => e.event === "recovery_test_start";
const endLine = (e: SetActivityEvent) => e.event === "recovery_test_end";

/** What one line of a recovery test says, in the words the terminal's
 *  `--follow` uses (the engine's own stage label wins over this build's). */
function lineText(e: SetActivityEvent): string {
  if (startLine(e)) return "Recovery test started";
  if (endLine(e)) {
    if (e.result === "success") return "Recovery test finished";
    const reason = e.fields.reason;
    return "Recovery test did not pass" + (reason ? ": " + reason : "");
  }
  const stage = e.fields.stage ?? "";
  return e.fields.stage_label || stageLabel(stage) || e.message;
}

export function createHttpAssurance(api: RetndApi): AssuranceApi {
  /** Operations this page started or reattached to: id -> (set, when). The
   *  feed is read per set, and an id alone does not say which. */
  const followed = new Map<string, { setId: string; startedAt: number; sawStart: boolean }>();

  const compose = async (artifacts: BackupArtifact[]): Promise<ArtifactAssurance[]> => {
    const dueById = new Map<string, { overdue: boolean; never: boolean }>();
    try {
      const due = await api.listAssuranceDue();
      for (const i of due.items) dueById.set(i.artifactId, { overdue: i.overdue, never: !i.lastAssuredAt });
    } catch { /* no schedule: the claims below fall back to what the copies say */ }

    // The one thing a copy's record cannot say is that the set's own policy
    // turned the test off, so ask each set once.
    const off = new Map<string, boolean>();
    await Promise.all([...new Set(artifacts.map((a) => a.setId))].map(async (setId) => {
      try {
        const [source, set] = splitSet(setId);
        off.set(setId, (await api.getBackupSetAssurance(source, set)).recoveryTest.mode === "off");
      } catch { off.set(setId, false); }
    }));

    return artifacts.map((a) => ({
      artifactId: a.id,
      copies: liveCopies(a).length,
      content: contentClaim(a, dueById.get(a.id)),
      recovery: recoveryClaim(a, off.get(a.setId) ?? false, true),
      copyFailures: copyFailures(a)
    }));
  };

  const refuse = (what: string) => () => Promise.reject(new NotOffered(what));

  const startRecoveryTest = async (setId: string): Promise<Operation> => {
    const [source, set] = splitSet(setId);
    const started = await api.startRecoveryTest(source, set);
    const startedAt = Date.now();
    followed.set(started.operationId, { setId, startedAt, sawStart: false });
    return {
      id: started.operationId, kind: "recovery_test", setId, state: "running",
      stage: "resolving_copy", bytes: 0, totalBytes: 0, startedAt: new Date(startedAt).toISOString()
    };
  };

  const activeOperation = async (setId: string): Promise<Operation | null> => {
    const live = await api.getLiveActivity({ setId });
    const open = live.sets.find((s) => s.setId === setId)?.unfinishedActions?.find((x) => x.action === "recovery_test");
    if (!open) return null;
    const at = Date.parse(open.startedAt);
    followed.set(open.actionId, { setId, startedAt: Number.isNaN(at) ? Date.now() : at, sawStart: true });
    return {
      id: open.actionId, kind: "recovery_test", setId, state: "running",
      stage: "resolving_copy", bytes: 0, totalBytes: 0, startedAt: open.startedAt
    };
  };

  const readOperation = async (operationId: string, cursor: number): Promise<OperationFeed> => {
    const mine = followed.get(operationId);
    if (!mine) throw new NotOffered("follow a recovery test this page did not start");
    const live = await api.getLiveActivity({ setId: mine.setId, since: cursor });
    const set = live.sets.find((s) => s.setId === mine.setId);
    const lines = (set?.events ?? []).filter((e) => e.fields.op_id === operationId);
    const events: OperationEvent[] = lines.map((e) => ({
      seq: e.sequence, at: e.at, stage: e.fields.stage ?? e.event, text: lineText(e)
    }));
    const last = lines[lines.length - 1];
    const end = lines.find(endLine);
    const stillOpen = !!set?.unfinishedActions?.some((x) => x.actionId === operationId);
    if (lines.some(startLine) || stillOpen) mine.sawStart = true;
    const seenStart = mine.sawStart;

    let state: Operation["state"] = "running";
    if (end) state = end.result === "success" ? "complete" : "failed";
    else if (!stillOpen && seenStart) state = "untracked";
    else if (!seenStart && Date.now() - mine.startedAt > START_GRACE_MS) state = "untracked";

    const stageLine = [...lines].reverse().find((e) => e.fields.stage);
    const operation: Operation = {
      id: operationId, kind: "recovery_test", setId: mine.setId, state,
      stage: end ? "complete" : stageLine?.fields.stage ?? "resolving_copy",
      bytes: 0, totalBytes: 0, startedAt: new Date(mine.startedAt).toISOString()
    };
    return { operation, events, nextCursor: Math.max(cursor, last?.sequence ?? 0) };
  };

  return {
    offers: { operations: false, recoveryTest: true, recoveryClaim: true },
    listArtifactAssurance: async (setId) => compose(await api.listArtifacts(setId)),
    getArtifactAssurance: async (id) => (await compose([await api.getArtifact(id)]))[0],
    startScrub: refuse("start a scrub from the browser"),
    startRecoveryTest,
    activeOperation,
    readOperation,
    cancelOperation: refuse("cancel a scrub or a recovery test"),
    getValidatorResult: refuse("read a validator's output")
  };
}

/** The production client with the assurance seam attached. */
export function withHttpAssurance(api: RetndApi): RetndApi {
  return { ...api, assurance: createHttpAssurance(api) };
}
