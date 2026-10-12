/**
 * The {@link AssuranceApi} a production client offers, composed from routes
 * that exist (issue #985).
 *
 * # What is real
 *
 * Each copy's achieved verification class, its time and its access (the
 * backup's placements), the copy's own scrub and recovery-test record (also
 * on the placement), the scrub schedule (GET /assurance/due), and the four
 * operator routes: start a scrub (POST /backup-sets/{source}/{set}/scrub),
 * start a recovery test (POST .../recovery-test), cancel either (POST
 * /assurance/operations/{op}/cancel) and read the newest test's validator
 * output (GET .../recovery-test/output). From those this builds the Stored,
 * Content and Recovery claims and the controls.
 *
 * # Following an operation
 *
 * A recovery test (and a recovery) answers with an operation id that every
 * one of its lines carries as `op_id` on the live activity feed, so this
 * page follows it by reading that feed. The feed is a bounded in-memory
 * tail, not a record, and this says so: a run whose lines have fallen off it
 * is reported as `untracked`, which is not a verdict, and the chip is where
 * the result lands.
 *
 * A SCRUB is different, and the difference is the server's on purpose: its
 * operation id is on no feed line, it exists to be cancelled. So a scrub is
 * followed by what it leaves behind, read from the feed after the point the
 * page started it: this set's manual `scrub_pass` line ends it, and each
 * `scrub_failure` line naming one of the set's copies is a line of it. A
 * scrub that selected nothing writes no line at all, so silence is not a
 * verdict either: after {@link SCRUB_QUIET_MS} of it the page says it can no
 * longer see the run, and a restart of the serving process (a new feed epoch)
 * says the same at once.
 */
import type { RetndApi } from "@shared/api/contracts";
import type { LiveActivity, SetActivityEvent } from "@shared/types/activity";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";
import { scrubFailureText, scrubPassText, stageWords } from "./feedText";
import type {
  AchievedClass, ArtifactAssurance, AssuranceApi, ClaimRecord, CopyFailure, Operation, OperationEvent, OperationFeed,
  OperationKind, ValidatorResult
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

/** How long a scrub may leave nothing on the feed before the page stops
 *  claiming it can see it. A scrub that selects nothing writes no line, and a
 *  content read of a large copy can be quiet for minutes, so this is long
 *  enough not to cry wolf and short enough not to spin for ever. */
export const SCRUB_QUIET_MS = 5 * 60_000;

/** How long after a cancel was accepted a scrub that writes no line is taken
 *  to have stopped. A cancelled pass that had selected copies still writes its
 *  counts, so this only covers the pass with nothing to say. */
export const CANCEL_GRACE_MS = 10_000;

/** Thrown for an operation id that is not on the live feed. */
export class NotFollowable extends Error {
  constructor() {
    super(
      "The live feed holds no operation with this id, so this page cannot follow it. A scrub never appears on the feed " +
      "(its id exists only to cancel it), and a finished run's lines fall off the feed. Its result is on the chip it feeds."
    );
    this.name = "NotFollowable";
  }
}

interface Followed {
  setId: string;
  kind: OperationKind;
  startedAt: number;
  sawStart: boolean;
  /** Scrub only: the feed sequence at the start; nothing at or below it is this scrub's. */
  floor: number;
  /** Scrub only: the feed epoch at the start, so a restart of the serving process is noticed. */
  epoch: string;
  /** When a cancel the service accepted was asked for, or 0. */
  cancelRequestedAt: number;
  /** Scrub only: when a line of this scrub was last seen. */
  lastLineAt: number;
  /** Starting joined a run that was already going. */
  joined: boolean;
}

function newFollowed(setId: string, kind: OperationKind, startedAt: number, over: Partial<Followed> = {}): Followed {
  return { setId, kind, startedAt, sawStart: false, floor: 0, epoch: "", cancelRequestedAt: 0, lastLineAt: 0, joined: false, ...over };
}

/** The highest sequence anything on a reading carries. */
function headOf(live: LiveActivity): number {
  let head = live.deployment?.latestSequence ?? 0;
  for (const s of live.sets) head = Math.max(head, s.latestSequence);
  return head;
}

/** Which kind of run a set of its lines (or its open action) belongs to. */
function kindOf(lines: SetActivityEvent[], openAction?: string): OperationKind | null {
  if (lines.some((e) => e.event === "recovery_test_start" || e.event === "recovery_test_end") || openAction === "recovery_test") return "recovery_test";
  if (lines.some((e) => e.event === "recovery_start" || e.event === "recovery_end") || openAction === "recovery") return "recovery";
  if (lines.some((e) => e.event === "recovery_stage")) return lines.some((e) => e.action === "recovery_test") ? "recovery_test" : "recovery";
  return null;
}

const KIND_WORDS = { scrub: "Scrub", recovery_test: "Recovery test", recovery: "Recovery" } as const;

/** What one line of a recovery or recovery test says in the operation panel,
 *  in the words the terminal's `--follow` uses (the engine's own stage label
 *  wins over this build's). */
function runLineText(kind: OperationKind, e: SetActivityEvent, cancelled: boolean): string {
  const word = KIND_WORDS[kind];
  if (e.event === "recovery_test_start" || e.event === "recovery_start") return word + " started";
  if (e.event === "recovery_test_end" || e.event === "recovery_end") {
    if (e.result === "success") return word + " finished";
    if (cancelled) return word + " cancelled";
    const reason = e.fields.reason;
    // An error is a verdict that came back wrong; a warning proved nothing
    // either way (a refusal, or a run that could not complete).
    return word + (e.result === "warn" ? " did not complete" : " did not pass") + (reason ? ": " + reason : "");
  }
  return e.fields.stage ? stageWords(e.fields) : e.message;
}

const isStart = (e: SetActivityEvent) => e.event === "recovery_test_start" || e.event === "recovery_start";
const isEnd = (e: SetActivityEvent) => e.event === "recovery_test_end" || e.event === "recovery_end";

async function readRun(api: RetndApi, operationId: string, mine: Followed, cursor: number): Promise<OperationFeed> {
  const live = await api.getLiveActivity({ setId: mine.setId, since: cursor });
  const set = live.sets.find((s) => s.setId === mine.setId);
  const lines = (set?.events ?? []).filter((e) => e.fields.op_id === operationId);
  const end = lines.find(isEnd);
  const cancelled = !!end && end.result === "warn" && mine.cancelRequestedAt > 0;
  const events: OperationEvent[] = lines.map((e) => ({
    seq: e.sequence, at: e.at, stage: e.fields.stage ?? e.event,
    kind: isStart(e) ? "start" : isEnd(e) ? "end" : "stage",
    text: runLineText(mine.kind, e, cancelled && e === end)
  }));
  const last = lines[lines.length - 1];
  const stillOpen = !!set?.unfinishedActions?.some((x) => x.actionId === operationId);
  if (lines.some(isStart) || stillOpen) mine.sawStart = true;

  let state: Operation["state"] = "running";
  if (end) state = end.result === "success" ? "complete" : cancelled ? "cancelled" : "failed";
  else if (!stillOpen && mine.sawStart) state = "untracked";
  else if (!mine.sawStart && Date.now() - mine.startedAt > START_GRACE_MS) state = "untracked";

  const stageLine = [...lines].reverse().find((e) => e.fields.stage);
  const done = end ? Number(end.fields.bytes) : 0;
  const operation: Operation = {
    id: operationId, kind: mine.kind, setId: mine.setId, state,
    stage: end?.result === "success" ? "complete" : stageLine?.fields.stage ?? "resolving_copy",
    bytes: Number.isFinite(done) && done > 0 ? done : 0, totalBytes: Number.isFinite(done) && done > 0 ? done : 0,
    startedAt: new Date(mine.startedAt).toISOString(),
    ...(state === "running" && mine.cancelRequestedAt > 0 ? { cancelling: true } : {}),
    ...(mine.joined ? { joined: true } : {})
  };
  return { operation, events, nextCursor: Math.max(cursor, last?.sequence ?? 0) };
}

async function readScrub(api: RetndApi, operationId: string, mine: Followed, cursor: number): Promise<OperationFeed> {
  const live = await api.getLiveActivity({ since: Math.max(cursor, mine.floor) });
  const all = [...live.sets.flatMap((s) => s.events), ...(live.deployment?.events ?? [])];
  const prefix = mine.setId + "/";
  // This set's own lines and nothing else. A pass names its scope in words
  // ("set a/b"), a failure names the copy's artifact, and the trigger keeps a
  // scheduled pass or a CLI scrub from being taken for this one.
  const mineLines = all
    .filter((e) => e.sequence > Math.max(cursor, mine.floor))
    .filter((e) =>
      (e.event === "scrub_pass" && e.fields.trigger === "manual" && e.fields.scope === "set " + mine.setId) ||
      (e.event === "scrub_failure" && (e.fields.artifact ?? "").startsWith(prefix)))
    .sort((a, b) => a.sequence - b.sequence);
  const events: OperationEvent[] = mineLines.map((e) => ({
    seq: e.sequence, at: e.at, stage: e.event, kind: "scrub",
    text: e.event === "scrub_pass" ? scrubPassText(e) : scrubFailureText(e)
  }));
  const now = Date.now();
  if (mineLines.length > 0) mine.lastLineAt = now;
  const pass = [...mineLines].reverse().find((e) => e.event === "scrub_pass");
  const cancelRequested = mine.cancelRequestedAt > 0;
  const quietSince = Math.max(mine.startedAt, mine.lastLineAt);

  let state: Operation["state"] = "running";
  if (pass) {
    const bad = Number(pass.fields.failed) > 0 || Number(pass.fields.could_not_complete) > 0;
    state = cancelRequested ? "cancelled" : bad ? "failed" : "complete";
  } else if (cancelRequested && now - mine.cancelRequestedAt > CANCEL_GRACE_MS) {
    state = "cancelled";
  } else if ((mine.epoch && live.epoch !== mine.epoch) || now - quietSince > SCRUB_QUIET_MS) {
    // The serving process restarted, or the feed has been silent for longer
    // than a run with nothing to report would be. Neither is a verdict.
    state = "untracked";
  }
  const operation: Operation = {
    id: operationId, kind: "scrub", setId: mine.setId, state, stage: state === "running" ? "scrub" : "complete",
    bytes: 0, totalBytes: 0, startedAt: new Date(mine.startedAt).toISOString(),
    ...(state === "running" && cancelRequested ? { cancelling: true } : {})
  };
  const nextCursor = mineLines.length > 0 ? mineLines[mineLines.length - 1].sequence : cursor;
  return { operation, events, nextCursor: Math.max(cursor, nextCursor) };
}

export function createHttpAssurance(api: RetndApi): AssuranceApi {
  /** Operations this page started, reattached to, or found on the feed. The
   *  feed is read per set, and an id alone does not say which. */
  const followed = new Map<string, Followed>();

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

  const startedOperation = (id: string, kind: OperationKind, setId: string, at: number, extra: Partial<Operation> = {}): Operation => ({
    id, kind, setId, state: "running", stage: kind === "scrub" ? "scrub" : "resolving_copy", bytes: 0, totalBytes: 0,
    startedAt: new Date(at).toISOString(), ...extra
  });

  const startRecoveryTest = async (setId: string): Promise<Operation> => {
    const [source, set] = splitSet(setId);
    const started = await api.startRecoveryTest(source, set);
    const startedAt = Date.now();
    // Joining a test that is already running follows that one: its lines are
    // already on the feed, so the start-grace clock does not apply to it.
    followed.set(started.operationId, newFollowed(setId, "recovery_test", startedAt, { sawStart: started.coalesced, joined: started.coalesced }));
    return startedOperation(started.operationId, "recovery_test", setId, startedAt, started.coalesced ? { joined: true } : {});
  };

  const startScrub = async (setId: string): Promise<Operation> => {
    const [source, set] = splitSet(setId);
    // Where the feed stands now, so only what the scrub writes AFTER this is
    // taken for its. Best effort: a feed that cannot be read here leaves the
    // floor at zero, and the set and trigger filters below still apply.
    let head = 0;
    let epoch = "";
    try {
      const before = await api.getLiveActivity();
      head = headOf(before);
      epoch = before.epoch;
    } catch { /* the floor stays at zero */ }
    const started = await api.startScrub(source, set);
    const startedAt = Date.now();
    followed.set(started.operationId, newFollowed(setId, "scrub", startedAt, { sawStart: true, floor: head, epoch }));
    return startedOperation(started.operationId, "scrub", setId, startedAt);
  };

  const activeOperation = async (setId: string): Promise<Operation | null> => {
    const live = await api.getLiveActivity({ setId });
    const open = live.sets.find((s) => s.setId === setId)?.unfinishedActions?.find((x) => x.action === "recovery_test");
    if (!open) return null;
    const at = Date.parse(open.startedAt);
    const startedAt = Number.isNaN(at) ? Date.now() : at;
    followed.set(open.actionId, newFollowed(setId, "recovery_test", startedAt, { sawStart: true }));
    return startedOperation(open.actionId, "recovery_test", setId, startedAt);
  };

  /** Finds an operation this page did not start (a reload, or a link to its
   *  detail) on the live feed by its `op_id`. */
  const attach = async (operationId: string): Promise<Followed> => {
    const live = await api.getLiveActivity();
    for (const set of live.sets) {
      const lines = set.events.filter((e) => e.fields.op_id === operationId);
      const open = set.unfinishedActions?.find((x) => x.actionId === operationId);
      if (lines.length === 0 && !open) continue;
      const kind = kindOf(lines, open?.action);
      if (!kind) continue;
      const at = Date.parse(lines[0]?.at ?? open?.startedAt ?? "");
      const mine = newFollowed(set.setId, kind, Number.isNaN(at) ? Date.now() : at, { sawStart: true });
      followed.set(operationId, mine);
      return mine;
    }
    throw new NotFollowable();
  };

  const readOperation = async (operationId: string, cursor: number): Promise<OperationFeed> => {
    const mine = followed.get(operationId) ?? (await attach(operationId));
    return mine.kind === "scrub" ? readScrub(api, operationId, mine, cursor) : readRun(api, operationId, mine, cursor);
  };

  const getValidatorResult = async (setId: string): Promise<ValidatorResult | null> => {
    const [source, set] = splitSet(setId);
    const out = await api.getRecoveryTestOutput(source, set);
    if (!out.recorded) return null;
    return {
      artifactId: out.artifactId, medium: out.medium, operationId: out.operationId, state: out.state,
      recordedAt: out.recordedAt, truncated: out.truncated, records: out.records
    };
  };

  return {
    offers: { operations: true, recoveryTest: true, recoveryClaim: true },
    listArtifactAssurance: async (setId) => compose(await api.listArtifacts(setId)),
    getArtifactAssurance: async (id) => (await compose([await api.getArtifact(id)]))[0],
    startScrub,
    startRecoveryTest,
    activeOperation,
    readOperation,
    cancelOperation: async (operationId) => {
      await api.cancelAssuranceOperation(operationId);
      // Remembered only once the service accepted it: a cancel it refused
      // (404, the run just ended) must not turn a later warn into "Cancelled".
      const mine = followed.get(operationId);
      if (mine) mine.cancelRequestedAt = Date.now();
    },
    getValidatorResult
  };
}

/** The production client with the assurance seam attached. */
export function withHttpAssurance(api: RetndApi): RetndApi {
  return { ...api, assurance: createHttpAssurance(api) };
}
