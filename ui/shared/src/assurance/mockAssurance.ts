/**
 * The in-process {@link AssuranceApi} the development build and the browser
 * specs run against (issue #985).
 *
 * # It models durability, because the UI's hardest promises are about it
 *
 * A reload mid-recovery must restore the stage and the bytes so far and start
 * no second run, and a dropped connection must replay from its cursor. Both
 * are statements about state that outlives the page, so this mock keeps its
 * state in sessionStorage and derives everything else from the clock: an
 * operation is only (id, kind, set, startedAt), and its stage, bytes and
 * event list are pure functions of how long ago it started. That is also
 * what the real service does with its durable feed, and it means a reload
 * cannot "forget" or "advance" anything the way an in-memory timer would.
 *
 * The scenarios are the closed list in api/mock.ts. They only choose which
 * fixture or which outcome a case needs; there is no assurance behaviour
 * here the typed interface does not describe.
 */
import { RetndError } from "@shared/api/contracts";
import type { BackupArtifact } from "@shared/types/backup";
import type { ApiErrorCode, RecoveryMint, RecoveryMintRequest, RecoveryPlacement, RecoveryTargetSpec } from "@shared/api/contracts";
import type {
  ArtifactAssurance, AssuranceApi, ClaimRecord, Operation, OperationEvent,
  OperationFeed, OperationKind, OperationState, ValidatorResult
} from "./types";
import { stageLabel } from "./stages";
import type { SetActivityEvent, UnfinishedAction } from "@shared/types/activity";

const DAY = 86_400_000;
const KEY = "retnd.mock.assurance";
const DROP = "retnd.mock.assurance.drop";

interface StoredOp { id: string; kind: OperationKind; setId: string; startedAt: number; cancelledAt?: number }
interface Store {
  starts: number;
  grants: number;
  ops: Record<string, StoredOp>;
  /** artifact id -> when its provider restore was requested. */
  thaws: Record<string, number>;
}

let memory: Store | null = null;
function load(): Store {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (raw) return JSON.parse(raw) as Store;
  } catch { /* private window: fall through to memory */ }
  memory ??= fresh();
  return memory;
}
function save(s: Store) {
  memory = s;
  try { sessionStorage.setItem(KEY, JSON.stringify(s)); } catch { /* memory holds it */ }
}
function fresh(): Store { return { starts: 0, grants: 0, ops: {}, thaws: {} }; }

const rec = (status: ClaimRecord["status"], cls: ClaimRecord["achievedClass"], ago?: number, now = Date.now()): ClaimRecord => ({
  status, achievedClass: cls, at: ago === undefined ? undefined : new Date(now - ago).toISOString()
});

/** Set id -> the set's latest backup, whose claims the set page shows. */
const SET_LATEST: Record<string, string> = {
  "production/postgres-primary": "production/postgres-primary/postgres-prod-20260828.dump.zst",
  "production/billing-mysql": "production/billing-mysql/billing-20260827.sql.gz",
  "production/auth-config": "production/auth-config/auth-config-20260826.tar.zst",
  "media/weekly-archive": "media/weekly-archive/media-week34.tar"
};

function baseTable(now: number): ArtifactAssurance[] {
  // Only the backups the Backups page lists, in the shapes their placements
  // have in the fixtures (api/mock.ts): two copies of one dump, a single copy
  // on an archive class, a backup with no copy yet, and a copy nobody can
  // reach. Every state below is one a reader can reach by looking.
  return [
    { artifactId: "production/postgres-primary/postgres-prod-20260828.dump.zst", copies: 2, copyContent: ["content", "existence"], content: rec("pass", "content", 3 * DAY, now), recovery: rec("pass", "content", 12 * DAY, now) },
    { artifactId: "production/billing-mysql/billing-20260827.sql.gz", copies: 1, content: rec("requires_restore", ""), recovery: rec("could_not_complete", "", 1 * DAY, now) },
    { artifactId: "media/weekly-archive/media-week34.tar", copies: 0, content: rec("never_run", ""), recovery: rec("never_configured", "") },
    { artifactId: "production/legacy-redis/redis-20260812.rdb.zst", copies: 1, content: rec("no_readable_copy", ""), recovery: rec("never_configured", "") }
  ];
}

/** The stage plan of an operation: engine stage id, seconds, whether bytes
 *  move. Ids and order are the engine's (stages.ts mirrors obs.RecoveryStages
 *  and the scrub events), so the browser and `--follow` cannot name a stage
 *  differently. A recovery test re-reads the staged copy, checks its digest,
 *  and only then runs the operator's script; the engine records both claims,
 *  the script records neither. */
const PLAN: Record<OperationKind, Array<{ stage: string; secs: number; bytes: boolean }>> = {
  scrub: [
    { stage: "reading", secs: 2, bytes: true },
    { stage: "verifying", secs: 1, bytes: true },
    { stage: "complete", secs: 1, bytes: false }
  ],
  recovery_test: [
    { stage: "resolving_copy", secs: 1, bytes: false },
    { stage: "reading", secs: 6, bytes: true },
    { stage: "verifying", secs: 14, bytes: true },
    { stage: "validating", secs: 3, bytes: false },
    { stage: "cleaning_temporary_files", secs: 1, bytes: false }
  ],
  // The browser never starts a recovery it can follow here (a download is the
  // browser's own), so this exists to keep the closed list total.
  recovery: [
    { stage: "resolving_copy", secs: 1, bytes: false },
    { stage: "reading", secs: 4, bytes: true },
    { stage: "verifying", secs: 2, bytes: true },
    { stage: "writing_target", secs: 2, bytes: true },
    { stage: "cleaning_temporary_files", secs: 1, bytes: false }
  ]
};
const TOTAL_BYTES = 4_800_000_000;
/** What the journal's bounded reason reads like for a copy whose bytes changed. */
const FAIL_REASON = "sha256 differs from the one recorded at ingest";
const total = (k: OperationKind) => PLAN[k].reduce((n, p) => n + p.secs, 0);
const FINAL: Record<OperationKind, string> = { scrub: "complete", recovery_test: "complete", recovery: "complete" };

function eventsOf(op: StoredOp, upTo: number): OperationEvent[] {
  const out: OperationEvent[] = [];
  let offset = 0;
  let seq = 0;
  const at = (s: number) => new Date(op.startedAt + s * 1000).toISOString();
  for (const p of PLAN[op.kind]) {
    if (offset > upTo) break;
    out.push({ seq: ++seq, at: at(offset), stage: p.stage, text: stageLabel(p.stage) });
    if (p.bytes) {
      for (let s = 1; s < p.secs; s++) {
        if (offset + s > upTo) break;
        const pct = Math.round(((offset + s) / total(op.kind)) * 100);
        out.push({ seq: ++seq, at: at(offset + s), stage: p.stage, text: stageLabel(p.stage) + ": " + pct + "% of the way through" });
      }
    }
    offset += p.secs;
  }
  return out;
}

function view(op: StoredOp, scrubFails: boolean, now: number): { operation: Operation; events: OperationEvent[] } {
  const elapsedMs = (op.cancelledAt ?? now) - op.startedAt;
  const elapsed = Math.floor(elapsedMs / 1000);
  const done = !op.cancelledAt && elapsed >= total(op.kind);
  let stage = PLAN[op.kind][PLAN[op.kind].length - 1].stage;
  let off = 0;
  for (const p of PLAN[op.kind]) {
    if (elapsed < off + p.secs) { stage = p.stage; break; }
    off += p.secs;
  }
  const events = eventsOf(op, Math.min(elapsed, total(op.kind)));
  let state: OperationState = "running";
  if (op.cancelledAt) {
    state = "cancelled";
    events.push({ seq: events.length + 1, at: new Date(op.cancelledAt).toISOString(), stage, text: "Cancelled by the operator" });
  } else if (done) {
    state = op.kind === "scrub" && scrubFails ? "failed" : "complete";
    events.push({
      seq: events.length + 1, at: new Date(op.startedAt + total(op.kind) * 1000).toISOString(), stage: FINAL[op.kind],
      text: state === "failed" ? "Scrub finished: a digest did not match" : stageLabel(FINAL[op.kind])
    });
  }
  const frac = state === "complete" || state === "failed" ? 1 : Math.min(1, elapsed / total(op.kind));
  return {
    operation: {
      id: op.id, kind: op.kind, setId: op.setId, state, stage: state === "running" ? stage : FINAL[op.kind],
      bytes: Math.round(TOTAL_BYTES * frac), totalBytes: TOTAL_BYTES, startedAt: new Date(op.startedAt).toISOString()
    },
    events
  };
}

let activeScenario = "default";

/**
 * The docked terminal's view of the same operations, in the engine's own
 * event names and fields (obs/assurance_events.go and the scrub events), so
 * the activity surfaces draw them with their dedicated lines and the
 * production client follows them exactly as it follows the real feed.
 *
 * A recovery test is a start, one stage line per stage it has entered, and
 * an end, each carrying the operation's id as `op_id`. A scrub is NOT
 * followable by id (the server keeps its id for cancel), so it writes only
 * what the engine writes: one `scrub_failure` per copy that did not pass and
 * one `scrub_pass`. The sequence is stable across polls, so the activity
 * strip's merge-by-sequence leaves no duplicate after a reconnect.
 * Per-copy progress is live-only and never reaches the durable record; this
 * is the live channel.
 */
export function assuranceLiveEvents(setId: string): SetActivityEvent[] {
  const out: SetActivityEvent[] = [];
  const now = Date.now();
  const artifact = SET_LATEST[setId] ?? setId + "/latest";
  const ops = Object.values(load().ops).filter((o) => o.setId === setId);
  ops.forEach((op, i) => {
    const base = 100_000 + i * 1_000;
    const v = view(op, activeScenario === "assurance-scrub-fails", now);
    const at = (s: number) => new Date(op.startedAt + s * 1000).toISOString();
    let seq = 0;
    const push = (e: Omit<SetActivityEvent, "sequence" | "scope">) => { out.push({ ...e, sequence: base + ++seq, scope: "set" }); };
    const cancelled = v.operation.state === "cancelled";
    const endAt = op.cancelledAt ? new Date(op.cancelledAt).toISOString() : at(total(op.kind));
    if (op.kind === "scrub") {
      if (v.operation.state === "failed") {
        push({
          at: endAt, level: "warn", event: "scrub_failure", result: "warn", message: "scrub found a copy wrong or could not check it",
          fields: { artifact, medium: "local", class: "content", reason: FAIL_REASON }
        });
      }
      if (v.operation.state === "complete" || v.operation.state === "failed" || cancelled) {
        const failed = v.operation.state === "failed" ? 1 : 0;
        push({
          at: endAt, level: failed ? "warn" : "info", event: "scrub_pass", result: failed ? "warn" : "success", message: "scrub pass complete",
          fields: {
            trigger: "manual", scope: "set " + setId, selected: "1", checked: cancelled ? "0" : "1", passed: cancelled || failed ? "0" : "1",
            failed: String(failed), could_not_complete: "0", requires_restore: "0", skipped_held: "0", skipped_leased: "0", bytes_read: "0"
          }
        });
      }
      return;
    }
    const start = op.kind === "recovery" ? "recovery_start" : "recovery_test_start";
    const end = op.kind === "recovery" ? "recovery_end" : "recovery_test_end";
    const action = op.kind;
    push({
      at: at(0), level: "info", event: start, action, actionId: op.id, message: op.kind === "recovery" ? "recovery starting" : "recovery test starting",
      fields: { op_id: op.id, backup_set: setId, artifact, ...(op.kind === "recovery_test" ? { validator: "pg-restore-check" } : {}) }
    });
    let offset = 0;
    const elapsed = Math.floor(((op.cancelledAt ?? now) - op.startedAt) / 1000);
    for (const p of PLAN[op.kind]) {
      if (offset > elapsed) break;
      push({
        at: at(offset), level: "info", event: "recovery_stage", action, actionId: op.id, message: "recovery stage",
        fields: { op_id: op.id, backup_set: setId, stage: p.stage, stage_label: stageLabel(p.stage) }
      });
      offset += p.secs;
    }
    if (v.operation.state === "complete" || v.operation.state === "failed" || cancelled) {
      const failed = v.operation.state === "failed";
      if (!failed && !cancelled) {
        push({
          at: endAt, level: "info", event: "recovery_stage", action, actionId: op.id, message: "recovery stage",
          fields: { op_id: op.id, backup_set: setId, stage: "complete", stage_label: stageLabel("complete") }
        });
      }
      push({
        at: endAt, level: cancelled ? "warn" : failed ? "error" : "info", event: end, action, actionId: op.id,
        result: cancelled ? "warn" : failed ? "error" : "success", message: "recovery test finished",
        fields: {
          op_id: op.id, backup_set: setId, artifact, duration: total(op.kind) + "s",
          ...(op.kind === "recovery_test" ? { validator: "pg-restore-check" } : {}),
          ...(cancelled ? { reason: "cancelled before it finished; no result was recorded" } : {})
        }
      });
    }
  });
  return out;
}

/**
 * What finished operations have written onto a backup's copies, in the
 * placement fields a real server serves (assurance_state, last_failure_*,
 * recovery_state, verification_class). The production composer builds its
 * claims from these and from nothing else, so a mock server that did not
 * write them would show a scrub that changed nothing.
 */
export function overlayAssurance(a: BackupArtifact): BackupArtifact {
  if (SET_LATEST[a.setId] !== a.id) return a;
  const now = Date.now();
  let placements = a.placements;
  for (const op of Object.values(load().ops).filter((o) => o.setId === a.setId && !o.cancelledAt)) {
    const v = view(op, activeScenario === "assurance-scrub-fails", now).operation;
    if (v.state !== "complete" && v.state !== "failed") continue;
    const at = new Date(op.startedAt + total(op.kind) * 1000).toISOString();
    placements = placements.map((p) => {
      if (p.medium !== "local") return p;
      if (op.kind === "scrub") {
        return v.state === "failed"
          ? { ...p, verificationClass: null, verifiedAt: null, assuranceState: "failed" as const, lastCheckedAt: at, lastFailureAt: at, lastFailureReason: FAIL_REASON, consecutiveFailures: 1 }
          : { ...p, verificationClass: "content" as const, verifiedAt: at, assuranceState: "passed" as const, lastCheckedAt: at };
      }
      return { ...p, recoveryState: "passed" as const, recoveryTestedAt: at };
    });
  }
  return placements === a.placements ? a : { ...a, placements };
}

/** The recovery tests still running, as the engine's unfinished-action list
 *  reports them: a start with no end. A scrub has no start line, so it is
 *  never here. */
export function assuranceUnfinished(setId: string): UnfinishedAction[] {
  const now = Date.now();
  return Object.values(load().ops)
    .filter((o) => o.setId === setId && o.kind !== "scrub" && view(o, activeScenario === "assurance-scrub-fails", now).operation.state === "running")
    .map((o, i) => ({ action: o.kind, actionId: o.id, startedAt: new Date(o.startedAt).toISOString(), sequence: 100_000 + i * 1_000 + 1 }));
}

export function createMockAssurance(scenario: string): AssuranceApi {
  activeScenario = scenario;
  const scrubFails = scenario === "assurance-scrub-fails";
  const cleanupFails = scenario === "assurance-cleanup-fails";
  // A new tab session of the mock starts clean the first time a scenario is
  // opened; a reload keeps what the page left behind.
  try {
    const marker = sessionStorage.getItem(KEY + ".scenario");
    if (marker !== scenario) { save(fresh()); sessionStorage.setItem(KEY + ".scenario", scenario); }
  } catch { /* memory */ }

  const table = (now: number): ArtifactAssurance[] => {
    let t = baseTable(now);
    if (scenario === "assurance-rebuilt") {
      t = t.map((a) => ({ ...a, content: rec("unknown", ""), recovery: rec("unknown", "") }));
    }
    if (scenario === "assurance-split") {
      t = t.map((a) => a.artifactId.endsWith("20260828.dump.zst") ? { ...a, recovery: rec("failed", "", 3 * DAY, now) } : a);
    }
    if (scenario === "assurance-more") {
      t = t.map((a) =>
        a.artifactId.endsWith("20260828.dump.zst") ? { ...a, content: rec("refused_for_cost", "existence", 9 * DAY, now), recovery: rec("cleanup_failed", "content", 2 * DAY, now) }
        : a.artifactId.endsWith("20260827.sql.gz") ? {
          ...a, content: rec("failed", "", 2 * DAY, now), recovery: rec("never_configured", ""),
          copyFailures: [{ medium: "local", kind: "scrub", state: "failed", reason: FAIL_REASON, consecutiveFailures: 3, at: new Date(now - 2 * DAY).toISOString() }]
        }
        : a.artifactId.endsWith("20260812.rdb.zst") ? { ...a, content: rec("overdue", "content", 41 * DAY, now), recovery: rec("never_run", "") }
        : a);
    }
    if (scenario === "assurance-existence") {
      t = t.map((a) => a.artifactId.endsWith("20260828.dump.zst") ? { ...a, content: rec("pass", "existence", 3 * DAY, now) } : a);
    }
    if (scenario === "assurance-recovery-overdue") {
      // Only the recovery claim is overdue. Content is fresh, so the
      // dashboard has to count this set as due from the other claim alone.
      t = t.map((a) => a.artifactId.endsWith("20260828.dump.zst") ? { ...a, recovery: rec("overdue", "content", 70 * DAY, now) } : a);
    }
    // Finished operations write their verdict onto the set's latest backup,
    // which is what makes a failing scrub REPLACE the chip rather than sit
    // beside it. This is the mock's stand-in for the engine recording the
    // claim: nothing the validator script prints can reach these fields.
    const s = load();
    for (const op of Object.values(s.ops)) {
      if (op.cancelledAt) continue;
      const v = view(op, scrubFails, now).operation;
      if (v.state !== "complete" && v.state !== "failed") continue;
      const endedAt = op.startedAt + total(op.kind) * 1000;
      const target = SET_LATEST[op.setId];
      t = t.map((a) => {
        if (a.artifactId !== target) return a;
        const at = new Date(endedAt).toISOString();
        if (op.kind === "scrub") {
          return {
            ...a,
            content: { status: v.state === "failed" ? "failed" : "pass", achievedClass: v.state === "failed" ? "" : "content", at },
            copyFailures: v.state === "failed"
              ? [{ medium: "local", kind: "scrub", state: "failed", reason: FAIL_REASON, consecutiveFailures: 1, at }]
              : []
          };
        }
        return { ...a, recovery: { status: cleanupFails ? "cleanup_failed" : "pass", achievedClass: "content", at } };
      });
    }
    return t;
  };

  const setOf = (artifactId: string) => artifactId.split("/").slice(0, 2).join("/");
  const operationsOf = (setId: string) => Object.values(load().ops).filter((o) => o.setId === setId);

  const api: AssuranceApi = {
    // assurance-no-operations is the production composition: no route starts
    // a scrub or cancels anything, a recovery test can be started and its
    // claim is reported per copy.
    offers: { operations: scenario !== "assurance-no-operations", recoveryTest: true, recoveryClaim: true },
    listArtifactAssurance: async (setId) => table(Date.now()).filter((a) => !setId || setOf(a.artifactId) === setId),
    getArtifactAssurance: async (id) => {
      const a = table(Date.now()).find((x) => x.artifactId === id);
      if (!a) throw new Error("no such artifact");
      return a;
    },

    startScrub: (setId) => start("scrub", setId),
    startRecoveryTest: (setId) => start("recovery_test", setId),
    activeOperation: async (setId) => {
      const now = Date.now();
      for (const op of operationsOf(setId)) {
        const v = view(op, scrubFails, now).operation;
        if (v.state === "running") return v;
      }
      return null;
    },
    readOperation: async (id, cursor): Promise<OperationFeed> => {
      const op = load().ops[id];
      if (!op) throw new Error("no such operation");
      const v = view(op, scrubFails, Date.now());
      // A dropped connection: the next read comes back from the start, the
      // way a reconnecting reader that lost its cursor would be answered.
      // The reader deduplicates by seq, which is what is under test.
      let from = cursor;
      try {
        if (sessionStorage.getItem(DROP) === "1") { sessionStorage.removeItem(DROP); from = 0; }
      } catch { /* none */ }
      return { operation: v.operation, events: v.events.filter((e) => e.seq > from), nextCursor: v.events.length };
    },
    cancelOperation: async (id) => {
      const s = load();
      if (s.ops[id] && !s.ops[id].cancelledAt) { s.ops[id].cancelledAt = Date.now(); save(s); }
    },
    getValidatorResult: async (): Promise<ValidatorResult | null> => {
      if (scenario !== "assurance-validator-output") return null;
      return {
        artifactId: "production/postgres-primary/postgres-prod-20260828.dump.zst",
        medium: "local",
        operationId: "aop_validator_fixture",
        state: "failed",
        recordedAt: new Date(Date.now() - 3 * DAY).toISOString(),
        truncated: true,
        // Output a hostile script could print: a window-title sequence, a
        // screen clear, the runner's marker words (which are only output
        // when they are not the truncation record), and an honest failure.
        records: [
          { kind: "output", text: "validate: checking page 14 of 220" },
          { kind: "output", text: "validate: header \u001b]0;pwned\u0007 set the title?" },
          { kind: "output", text: "validate: clear \u001b[2J then home \u001b[H" },
          { kind: "output", text: "[output truncated]" },
          { kind: "output", text: "validate: error: index mismatch" },
          { kind: "truncation", text: "output truncated: 41873 bytes were dropped before this point" }
        ]
      };
    }
  };

  async function start(kind: OperationKind, setId: string): Promise<Operation> {
    const s = load();
    // The service refuses a second run of the same kind on a set that has
    // one running, so a double press cannot start two.
    const now = Date.now();
    for (const op of Object.values(s.ops)) {
      if (op.setId === setId && op.kind === kind && view(op, scrubFails, now).operation.state === "running") {
        // Joined, not started: the service answers a second request with the
        // running one and says so.
        return { ...view(op, scrubFails, now).operation, joined: true };
      }
    }
    s.starts += 1;
    const id = "aop_" + s.starts + "_" + now.toString(36);
    s.ops[id] = { id, kind, setId, startedAt: now };
    save(s);
    return view(s.ops[id], scrubFails, now).operation;
  }

  return api;
}

// ---------------------------------------------------------------------------
// Recovery (#976): the mint and the redeem, and the one provider restore.
// ---------------------------------------------------------------------------

/** The fixture backup whose only copy is on an archive class. */
export const ARCHIVED_FIXTURE = "production/billing-mysql/billing-20260827.sql.gz";
/** How long the mock's provider restore takes. Real ones take hours; the
 *  fixture takes seconds so a spec can watch the whole flow. */
const THAW_MS = 2500;
/** A spec that needs the restore to stay in flight sets this key. */
const thawMs = () => {
  try { return Number(sessionStorage.getItem(KEY + ".thawMs")) || THAW_MS; } catch { return THAW_MS; }
};

const refuse = (code: string, message: string, status: number) =>
  new RetndError({ code: code as ApiErrorCode, message, status, correlationId: "cid_mock" + code.toLowerCase() });

/** What a provider restore has done to the archived fixture's copy so far, or
 *  null when none was requested. The overlay api/mock.ts applies to the copy's
 *  `access`, derived from the clock like everything else here. */
export function mockThawAccess(artifactId: string, now: number = Date.now()): "restoring" | "immediate" | null {
  const at = load().thaws[artifactId];
  if (at === undefined) return null;
  return now - at >= thawMs() ? "immediate" : "restoring";
}

export function overlayThaw(a: BackupArtifact): BackupArtifact {
  const access = mockThawAccess(a.id);
  if (!access) return a;
  return { ...a, placements: a.placements.map((p) => (p.access === "requires_restore" || p.access === "restoring" ? { ...p, access } : p)) };
}

/** What each grant the mock minted is for, so a redeem answers for the right
 *  target. Memory only: a grant is single-use and short-lived by design. */
const GRANTS = new Map<string, { artifactId: string; copy: string; target: RecoveryTargetSpec }>();

export function createMockRecovery(scenario: string) {
  const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
  return {
    mintRecovery: async (artifactId: string, req: RecoveryMintRequest): Promise<RecoveryMint> => {
      if (scenario === "assurance-snapshot-set") {
        throw refuse("RECOVERY_USE_SNAPSHOT_RESTORE", "This backup set is stored by the snapshot engine, so its backups are restore points. Use retnd snapshot restore.", 409);
      }
      if (scenario === "assurance-too-many-grants") {
        throw refuse("RECOVERY_TOO_MANY_GRANTS", "Too many recoveries are waiting to be collected. Use or let some expire first.", 429);
      }
      const s = load();
      if (artifactId === ARCHIVED_FIXTURE) {
        if (!req.acknowledged) {
          throw refuse(
            "RECOVERY_COST_NOT_ACKNOWLEDGED",
            "The only copy is in DEEP_ARCHIVE on offsite_cold. Reading it needs the provider to restore it first, which is billed and takes hours. Nothing was started.",
            409
          );
        }
        const state = mockThawAccess(artifactId);
        if (state === null) {
          s.thaws[artifactId] = Date.now();
          save(s);
          return {
            kind: "restore", artifactId, copy: "offsite_cold", operationId: "op_restore_mock",
            wait: "up to 48 hours for DEEP_ARCHIVE (the class's published time, not an estimate for this copy)",
            billing: "The provider bills for this restore. retnd holds no price list, so it shows no amount."
          };
        }
        if (state === "restoring") {
          throw refuse("RECOVERY_NOT_READABLE", "The provider is still restoring this copy. Ask again once it is readable.", 409);
        }
      }
      s.grants += 1;
      save(s);
      const grant = "mockgrant" + s.grants;
      GRANTS.set(grant, { artifactId, copy: artifactId === ARCHIVED_FIXTURE ? "offsite_cold" : "local", target: req.target });
      return {
        kind: "grant", artifactId, copy: artifactId === ARCHIVED_FIXTURE ? "offsite_cold" : "local", grant,
        downloadUrl: "/api/v1/recoveries/" + grant,
        expiresAt: new Date(Date.now() + 5 * 60_000).toISOString(),
        target: req.target, sizeBytes: 15_246_903_296
      };
    },
    redeemRecovery: async (grant: string): Promise<RecoveryPlacement> => {
      // The host re-reads the copy and matches it to the recorded digest
      // before it writes anything. The mock holds the request open for that
      // long so the "checking on the host" phase can be seen.
      await sleep(scenario === "assurance-download-slow" ? 6000 : 1200);
      if (scenario === "assurance-grant-expired") throw refuse("RECOVERY_GRANT_EXPIRED", "This grant expired before it was used. Ask for a new one.", 410);
      if (scenario === "assurance-grant-used") throw refuse("RECOVERY_GRANT_USED", "This grant was already used. Ask for a new one.", 410);
      if (scenario === "assurance-session-ended") throw refuse("RECOVERY_GRANT_SESSION_ENDED", "The session that minted this grant has ended.", 403);
      if (scenario === "assurance-staging-full") throw refuse("RECOVERY_NO_SPACE", "The staging filesystem does not have room for this copy plus its reserve. Nothing was written and no partial file remains.", 507);
      if (scenario === "assurance-reads-busy") throw refuse("RECOVERY_READS_BUSY", "This host runs only so many full reads at once and all of them are taken. The grant is not spent. Try again shortly.", 429);
      if (scenario === "assurance-download-fails" || scenario === "assurance-corrupt-copy") {
        throw refuse("RECOVERY_LOCAL_COPY_CORRUPT", "The copy on this host's disk failed verification against the digest the journal recorded. Nothing was delivered.", 500);
      }
      const g = GRANTS.get(grant);
      if (!g) throw refuse("RECOVERY_GRANT_UNKNOWN", "No such grant.", 404);
      GRANTS.delete(grant);
      const placed = g.target.kind === "storage"
        ? { key: g.target.key || "recovered/" + g.artifactId }
        : { path: "/data/recover-staging/" + (g.target.path || "recovered") + ".k3j9" };
      return {
        artifactId: g.artifactId, copy: g.copy, target: g.target, ...placed,
        bytes: 15_246_903_296, sha256: "9f2c61b7a5e84d03c1b7f60a29e5d8374bc01e6a52d93f8087ab4c6e1d25f390"
      };
    }
  };
}
