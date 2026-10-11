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
import type {
  ArchivalCost, ArtifactAssurance, AssuranceApi, AssuranceSettings, ClaimRecord, Operation, OperationEvent,
  OperationFeed, OperationKind, OperationState, Recovery, RecoveryRequest, SetAssurance, SetAssuranceMode, ValidatorResult
} from "./types";
import type { SetActivityEvent } from "@shared/types/activity";

const DAY = 86_400_000;
const KEY = "retnd.mock.assurance";
const DROP = "retnd.mock.assurance.drop";

interface StoredOp { id: string; kind: OperationKind; setId: string; startedAt: number; cancelledAt?: number }
interface StoredRecovery { id: string; artifactId: string; target: "browser" | "staging" | "destination"; startedAt: number; cancelledAt?: number }
interface Store {
  starts: number;
  grants: number;
  ops: Record<string, StoredOp>;
  recoveries: Record<string, StoredRecovery>;
  modes: Record<string, { mode: SetAssuranceMode; scrubEvery?: string; validator?: string }>;
  scrubEvery: string;
}

let memory: Store | null = null;
function load(): Store {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (raw) return JSON.parse(raw) as Store;
  } catch { /* private window: fall through to memory */ }
  memory ??= { starts: 0, grants: 0, ops: {}, recoveries: {}, modes: {}, scrubEvery: "7d" };
  return memory;
}
function save(s: Store) {
  memory = s;
  try { sessionStorage.setItem(KEY, JSON.stringify(s)); } catch { /* memory holds it */ }
}
function fresh(): Store { return { starts: 0, grants: 0, ops: {}, recoveries: {}, modes: {}, scrubEvery: "7d" }; }

const rec = (status: ClaimRecord["status"], cls: ClaimRecord["achievedClass"], ago?: number, now = Date.now()): ClaimRecord => ({
  status, achievedClass: cls, at: ago === undefined ? undefined : new Date(now - ago).toISOString()
});

/** Artifact id -> the set it belongs to, and which one is that set's latest. */
const SET_LATEST: Record<string, string> = {
  "production/postgres-primary": "production/postgres-primary/postgres-prod-20260828.dump.zst",
  "production/billing-mysql": "production/billing-mysql/billing-20260827.sql.gz",
  "production/auth-config": "production/auth-config/auth-config-20260826.tar.zst",
  "media/weekly-archive": "media/weekly-archive/media-week34.tar"
};
const COST: Record<string, ArchivalCost> = {
  "media/weekly-archive/media-week34.tar": {
    provider: "s3-cold", tier: "Bulk", estimate: "about 12 hours to thaw; egress for 48 GB is charged by the provider"
  }
};

function baseTable(now: number): ArtifactAssurance[] {
  // Only the four backups the Backups page lists (the other two fixtures are
  // quarantined and have their own page), so every state below is one a
  // reader can reach by looking.
  return [
    { artifactId: "production/postgres-primary/postgres-prod-20260828.dump.zst", copies: 2, content: rec("pass", "content", 3 * DAY, now), recovery: rec("pass", "content", 12 * DAY, now) },
    { artifactId: "production/billing-mysql/billing-20260827.sql.gz", copies: 1, content: rec("overdue", "content", 41 * DAY, now), recovery: rec("never_run", "") },
    { artifactId: "media/weekly-archive/media-week34.tar", copies: 1, content: rec("requires_restore", ""), recovery: rec("could_not_complete", "", 1 * DAY, now) },
    { artifactId: "production/legacy-redis/redis-20260812.rdb.zst", copies: 1, content: rec("no_readable_copy", ""), recovery: rec("never_configured", "") }
  ];
}

/** The stage plan of an operation: name, seconds, whether bytes move. */
const PLAN: Record<OperationKind, Array<{ stage: string; secs: number; bytes: boolean; label: string }>> = {
  scrub: [
    { stage: "enumerate", secs: 1, bytes: false, label: "Reading the copy list from the journal" },
    { stage: "read", secs: 2, bytes: true, label: "Reading copies" },
    { stage: "verify", secs: 1, bytes: false, label: "Comparing digests" }
  ],
  recovery_test: [
    { stage: "recover", secs: 1, bytes: false, label: "Recovering to an isolated staging path" },
    { stage: "read", secs: 6, bytes: true, label: "Re-reading the recovered bytes" },
    { stage: "digest", secs: 14, bytes: true, label: "Checking the digest against the recorded one" },
    { stage: "validator", secs: 3, bytes: false, label: "Running the validator" }
  ]
};
const TOTAL_BYTES = 4_800_000_000;
const total = (k: OperationKind) => PLAN[k].reduce((n, p) => n + p.secs, 0);

function eventsOf(op: StoredOp, upTo: number): OperationEvent[] {
  const out: OperationEvent[] = [];
  let offset = 0;
  let seq = 0;
  const at = (s: number) => new Date(op.startedAt + s * 1000).toISOString();
  for (const p of PLAN[op.kind]) {
    if (offset > upTo) break;
    out.push({ seq: ++seq, at: at(offset), stage: p.stage, text: "stage " + p.stage + ": " + p.label });
    if (p.bytes) {
      for (let s = 1; s < p.secs; s++) {
        if (offset + s > upTo) break;
        const pct = Math.round(((offset + s) / total(op.kind)) * 100);
        out.push({ seq: ++seq, at: at(offset + s), stage: p.stage, text: p.stage + " " + pct + "% of the way through" });
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
    events.push({ seq: events.length + 1, at: new Date(op.cancelledAt).toISOString(), stage, text: "cancelled by the operator" });
  } else if (done) {
    state = op.kind === "scrub" && scrubFails ? "failed" : "complete";
    events.push({
      seq: events.length + 1, at: new Date(op.startedAt + total(op.kind) * 1000).toISOString(), stage: "done",
      text: state === "failed" ? "scrub finished: a digest did not match" : op.kind + " finished"
    });
  }
  const frac = state === "complete" || state === "failed" ? 1 : Math.min(1, elapsed / total(op.kind));
  return {
    operation: {
      id: op.id, kind: op.kind, setId: op.setId, state, stage: state === "running" ? stage : "done",
      bytes: Math.round(TOTAL_BYTES * frac), totalBytes: TOTAL_BYTES, startedAt: new Date(op.startedAt).toISOString()
    },
    events
  };
}

let activeScenario = "default";

/**
 * The docked terminal's view of the same operations: one live line per
 * named stage event, with a sequence that is stable across polls, so the
 * activity strip's merge-by-sequence leaves no duplicate after a reconnect.
 * Per-copy progress is live-only and never reaches the durable record; this
 * is the live channel.
 */
export function assuranceLiveEvents(setId: string): SetActivityEvent[] {
  const out: SetActivityEvent[] = [];
  const ops = Object.values(load().ops).filter((o) => o.setId === setId);
  ops.forEach((op, i) => {
    const v = view(op, activeScenario === "assurance-scrub-fails", Date.now());
    v.events.forEach((e) => {
      const last = e.stage === "done" || e.text.startsWith("cancelled");
      const failed = v.operation.state === "failed" && e.stage === "done";
      out.push({
        sequence: 100_000 + i * 1_000 + e.seq,
        at: e.at,
        level: failed ? "error" : "info",
        event: "assurance_stage",
        scope: "set",
        message: (op.kind === "scrub" ? "scrub " : "recovery test ") + e.text,
        ...(last ? { result: failed ? ("error" as const) : e.text.startsWith("cancelled") ? ("warn" as const) : ("success" as const) } : {}),
        fields: { stage: e.stage, operation: op.id }
      });
    });
  });
  return out;
}

export function createMockAssurance(scenario: string): AssuranceApi {
  activeScenario = scenario;
  const scrubFails = scenario === "assurance-scrub-fails";
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
        a.artifactId.endsWith("20260828.dump.zst") ? { ...a, content: rec("refused_for_cost", "existence", 9 * DAY, now) }
        : a.artifactId.endsWith("20260827.sql.gz") ? { ...a, content: rec("failed", "", 2 * DAY, now), recovery: rec("never_configured", "") }
        : a);
    }
    if (scenario === "assurance-existence") {
      t = t.map((a) => a.artifactId.endsWith("20260828.dump.zst") ? { ...a, content: rec("pass", "existence", 3 * DAY, now) } : a);
    }
    // Finished operations write their verdict onto the set's latest backup,
    // which is what makes a failing scrub REPLACE the chip rather than sit
    // beside it.
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
        if (op.kind === "scrub") return { ...a, content: { status: v.state === "failed" ? "failed" : "pass", achievedClass: v.state === "failed" ? "" : "content", at } };
        return { ...a, recovery: { status: "pass", achievedClass: "content", at } };
      });
    }
    return t;
  };

  const setOf = (artifactId: string) => artifactId.split("/").slice(0, 2).join("/");

  const operationsOf = (setId: string) => Object.values(load().ops).filter((o) => o.setId === setId);

  const recoveryView = (r: StoredRecovery, now: number): Recovery => {
    const total = 48_000_000;
    const slow = scenario === "assurance-download-slow";
    const fails = scenario === "assurance-download-fails";
    const dur = slow ? 120_000 : 3_000;
    const elapsed = (r.cancelledAt ?? now) - r.startedAt;
    let state: Recovery["state"] = "running";
    let bytes = Math.min(total, Math.round((elapsed / dur) * total));
    if (r.cancelledAt) state = "cancelled";
    else if (fails && elapsed >= 2_000) { state = "failed"; bytes = Math.round(total * 0.4); }
    else if (elapsed >= dur) state = "complete";
    return { id: r.id, artifactId: r.artifactId, target: r.target, state, bytes, totalBytes: total, streamUrl: "" };
  };

  const latestFor = (setId: string, now: number) => table(now).find((a) => a.artifactId === SET_LATEST[setId]);

  const api: AssuranceApi = {
    listArtifactAssurance: async (setId) => table(Date.now()).filter((a) => !setId || setOf(a.artifactId) === setId),
    getArtifactAssurance: async (id) => {
      const a = table(Date.now()).find((x) => x.artifactId === id);
      if (!a) throw new Error("no such artifact");
      return a;
    },
    getArchivalCost: async (id) => COST[id] ?? null,
    getSetAssurance: async (setId) => {
      const s = load();
      const m = s.modes[setId] ?? { mode: "inherit" as const };
      return {
        setId, mode: m.mode, resolvedScrubEvery: m.mode === "custom" && m.scrubEvery ? m.scrubEvery : s.scrubEvery,
        customScrubEvery: m.scrubEvery, customValidator: m.validator, latest: latestFor(setId, Date.now())
      } satisfies SetAssurance;
    },
    putSetAssurance: async (setId, mode, custom) => {
      const s = load();
      s.modes[setId] = { mode, scrubEvery: custom?.scrubEvery, validator: custom?.validator };
      save(s);
      return api.getSetAssurance(setId);
    },
    getSettings: async (): Promise<AssuranceSettings> => ({
      scrubEvery: load().scrubEvery,
      host: { stagingRoot: "/data/recover-staging", stagingTtl: "24h", concurrency: "1 full read, 1 per destination", downloadMemoryBudget: "64 MiB" }
    }),
    putSettings: async (scrubEvery) => {
      if (!/^\d+[dh]$/.test(scrubEvery)) throw new Error("Use a number and a unit, like 7d or 12h.");
      const s = load();
      s.scrubEvery = scrubEvery;
      save(s);
      return api.getSettings();
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
        exitStatus: 3,
        durationMs: 4200,
        truncatedBytes: 41_873,
        output: [
          "validate: checking page 14 of 220",
          "validate: header \u001b]0;pwned\u0007 set the title?",
          "validate: clear \u001b[2J then home \u001b[H",
          "[output truncated]",
          "validate: error: index mismatch"
        ].join("\n")
      };
    },

    createRecovery: async (artifactId, req: RecoveryRequest) => {
      if (COST[artifactId] && !req.acknowledgeCost) {
        throw new Error("This copy is archived: recovering it costs money, and no acknowledgement was sent.");
      }
      const s = load();
      s.grants += 1;
      const id = "rec_" + s.grants + "_" + Date.now().toString(36);
      s.recoveries[id] = { id, artifactId, target: req.target, startedAt: Date.now() };
      save(s);
      return recoveryView(s.recoveries[id], Date.now());
    },
    activeRecovery: async (artifactId) => {
      const now = Date.now();
      for (const r of Object.values(load().recoveries)) {
        if (r.artifactId !== artifactId) continue;
        const v = recoveryView(r, now);
        if (v.state === "running") return v;
      }
      return null;
    },
    getRecovery: async (id) => {
      const r = load().recoveries[id];
      if (!r) throw new Error("no such recovery");
      return recoveryView(r, Date.now());
    },
    cancelRecovery: async (id) => {
      const s = load();
      if (s.recoveries[id] && !s.recoveries[id].cancelledAt) { s.recoveries[id].cancelledAt = Date.now(); save(s); }
    }
  };

  async function start(kind: OperationKind, setId: string): Promise<Operation> {
    const s = load();
    // The service refuses a second run of the same kind on a set that has
    // one running, so a double press cannot start two.
    const now = Date.now();
    for (const op of Object.values(s.ops)) {
      if (op.setId === setId && op.kind === kind && view(op, scrubFails, now).operation.state === "running") {
        return view(op, scrubFails, now).operation;
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
