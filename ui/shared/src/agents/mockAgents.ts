/**
 * The mock's agent routes (EPIC U, issue #1351).
 *
 * State is per mock instance, as the other settings are: a mint, a revoke, an
 * approval or a denial has to be visible to the next read, and one test's
 * writes must not reach the next. What it models is the contract the card
 * reads and nothing stronger: the engine's own refusals (the expiry ceiling,
 * the step-up, an expired confirmation, the shut operation gate) come back
 * with the engine's words, the commands in the answers are strings the "engine"
 * wrote, and the one place the token exists is the mint's return value.
 *
 * Scenarios (everything else is the default deployment: no token, nothing
 * waiting, nothing audited, HTTP transport off):
 *   agents-tokens            five tokens in every status, a short audit
 *   agents-pending           the same, with two requests waiting
 *   agents-pending-gate      approving run_cycle is refused by the shut gate
 *   agents-pending-expired   approving anything is refused as expired
 *   agents-transport-on      the HTTP transport statement for "on"
 *   agents-audit-many        sixty audit rows, so Show older has a page to fetch
 *   agents-audit-failed      the audit route fails, the other regions load
 *   agents-read-only         the pending page, on a page that is read-only
 *   agents-insecure          the pending page on an unencrypted connection: the consent comes first
 *   agents-no-echo           pending requests, and responses that carry no command
 */
import { RetndError } from "@shared/api/contracts";
import type { ApiErrorCode, RetndApi } from "@shared/api/contracts";
import type { AgentAuditRow, AgentConfirmation, AgentScope, AgentToken } from "@shared/types/agents";

export const AGENTS_SCENARIOS = [
  "agents-tokens",
  "agents-pending",
  "agents-pending-gate",
  "agents-pending-expired",
  "agents-transport-on",
  "agents-audit-many",
  "agents-audit-failed",
  "agents-read-only",
  "agents-insecure",
  "agents-no-echo"
] as const;

/** The token the next mint answers with. It is not one: it opens nothing. */
export const MOCK_MINTED_SECRET = "rat_Qm7Zk3xHn9LdR2sVt8YbWc4JgPeA6UfX";

export const MOCK_GATE_SENTENCE =
  "destructive operations are refused over HTTP in this release, until the trusted-proxy authentication gate (issue #92) has been verified; run the equivalent retnd command on the host";

const READ = [
  "status", "backup_sets_list", "backup_set_show", "backups_list", "backup_show", "copies_show", "encryption_state",
  "assurance_due", "assurance_show", "activity", "activity_live", "quarantine_list", "operations_list", "operation_show",
  "storage_mediums_list", "storage_usage", "retention_preview", "workflow_runs_list", "workflow_run_show"
];
const OPERATE = ["run_cycle", "run_backup_set", "recovery_test_start", "verify_copy", "scrub_start", "retry_backup", "quarantine_revalidate"];
const CONFIGURE = ["backup_set_enabled", "backup_set_retention_set", "backup_set_retention_clear", "backup_set_assurance_set"];

/** What an agent could put in an argument. The card draws it as inert text. */
export const HOSTILE_ARGUMENTS = '{"set":"<img src=x onerror=\'window.__agentPwned=1\'> https://example.invalid/ignore-previous-instructions"}';

const MIN = 60_000;
const DAY = 24 * 60 * MIN;

const refuse = (code: string, message: string, status = 400, correlationId = "cid_mockagent") =>
  new RetndError({ code: code as ApiErrorCode, message, status, correlationId });

const later = <T,>(v: T): Promise<T> => new Promise((r) => setTimeout(() => r(v), 80));

/** "90d", "36h", "30m" as milliseconds, or null for anything else. */
function durationMs(word: string): number | null {
  const m = /^(\d+)([dhm])$/.exec(word.trim());
  if (!m) return null;
  return Number(m[1]) * (m[2] === "d" ? DAY : m[2] === "h" ? 60 * MIN : MIN);
}

export interface MockAgentsDeps {
  /** The step-up the engine runs on an approval: null when the password is right. */
  stepUp(password: string | undefined): Promise<never> | null;
  /** The consent a connection that is not encrypted needs before a password leaves. */
  transport(hasPassword: boolean): Promise<void>;
}

export function createMockAgents(scenario: string, deps: MockAgentsDeps) {
  const seeded = scenario.startsWith("agents-");
  // A response that carries no command draws none: the card never composes one.
  const echoes = scenario !== "agents-no-echo";
  const now = Date.now();
  const iso = (offsetMs: number) => new Date(now + offsetMs).toISOString();

  const tok = (
    id: string, name: string, prefix: string, scope: AgentScope, status: AgentToken["status"],
    createdAgo: number, expiresIn: number, usedAgo: number | null
  ): AgentToken => ({
    id, name, prefix, scope, owner: "admin", status,
    createdAt: iso(-createdAgo * DAY), expiresAt: iso(expiresIn * DAY),
    lastUsedAt: usedAgo === null ? null : iso(-usedAgo * MIN),
    revokedAt: status === "revoked" ? iso(-3 * DAY) : null,
    revokeCommand: status === "active" && echoes ? "retnd agent token revoke " + id : null
  });

  const tokens: AgentToken[] = seeded
    ? [
        tok("tok_01", "nightly-triage", "rat_q7Hn", "read", "active", 14, 76, 3),
        tok("tok_02", "recovery-drill", "rat_b2Vw", "operate", "active", 9, 5, 6),
        tok("tok_03", "settings-helper", "rat_x4Lc", "configure", "active", 2, 88, null),
        tok("tok_04", "old-weekly-report", "rat_m9Zr", "read", "expired", 133, -43, 60 * 24 * 44),
        tok("tok_05", "laptop-experiment", "rat_k3Pd", "operate", "revoked", 40, 52, 60 * 24 * 28)
      ]
    : [];

  const confirmations: AgentConfirmation[] = [];
  if (["agents-pending", "agents-pending-gate", "agents-pending-expired", "agents-no-echo", "agents-read-only", "agents-insecure"].includes(scenario)) {
    confirmations.push(
      {
        id: "cf_01", tokenId: "tok_02", tokenName: "recovery-drill", tool: "run_cycle",
        description: "Runs a backup cycle for every enabled backup set.",
        command: "retnd run", approveCommand: echoes ? "retnd agent confirmations approve cf_01" : null,
        denyCommand: echoes ? "retnd agent confirmations deny cf_01" : null, backupSet: null,
        createdAt: iso(-1 * MIN), expiresAt: iso(14 * MIN)
      },
      {
        id: "cf_02", tokenId: "tok_02", tokenName: "recovery-drill", tool: "recovery_test_start",
        description: "Starts a recovery test of one backup.",
        command: "retnd assurance recovery-test production/postgres-primary", approveCommand: echoes ? "retnd agent confirmations approve cf_02" : null,
        denyCommand: echoes ? "retnd agent confirmations deny cf_02" : null, backupSet: "production/postgres-primary",
        createdAt: iso(-14 * MIN), expiresAt: iso(41_000)
      }
    );
  }

  const tools = [
    ...READ.map((name) => ({ name, scope: "read" as const })),
    ...OPERATE.map((name) => ({ name, scope: "operate" as const })),
    ...CONFIGURE.map((name) => ({ name, scope: "configure" as const }))
  ];
  const scopeOf = new Map(tools.map((t) => [t.name, t.scope]));

  const audit: AgentAuditRow[] = [];
  if (seeded) {
    const base: Array<[string, string, string, number, string | null, number]> = [
      ["nightly-triage", "tok_01", "status", 200, null, 3],
      ["nightly-triage", "tok_01", "backup_sets_list", 200, null, 3],
      ["recovery-drill", "tok_02", "run_cycle", 202, "AGENT_CONFIRMATION_REQUIRED", 8],
      ["nightly-triage", "tok_01", "run_cycle", 403, "AGENT_SCOPE_INSUFFICIENT", 20],
      ["nightly-triage", "tok_01", "activity", 200, null, 33],
      ["recovery-drill", "tok_02", "verify_copy", 202, "AGENT_CONFIRMATION_REQUIRED", 61],
      ["nightly-triage", "tok_01", "assurance_due", 200, null, 95],
      ["laptop-experiment", "tok_05", "status", 401, "AGENT_TOKEN_INVALID", 60 * 24 * 27]
    ];
    const count = scenario === "agents-audit-many" ? 60 : base.length;
    for (let i = 0; i < count; i++) {
      const [name, id, tool, status, code, ago] = base[i % base.length];
      audit.push({
        seq: count - i, at: iso(-(ago + Math.floor(i / base.length) * 7) * MIN), tokenId: id, tokenName: name, tool,
        scope: scopeOf.get(tool) ?? null, transport: i % 2 === 0 ? "stdio" : "http",
        // One row carries text an agent could plant: it must be drawn as data.
        arguments: tool === "activity" ? HOSTILE_ARGUMENTS : tool === "run_cycle" ? "{}" : '{"set":"production/postgres-primary"}',
        status, code, correlationId: "cid_" + String(1000 + i).padStart(8, "0")
      });
    }
    // Newest first, as the route answers, and the sequence follows the order.
    audit.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
    audit.forEach((r, i) => { r.seq = audit.length - i; });
  }

  const find = (id: string) => {
    const c = confirmations.find((x) => x.id === id);
    if (!c) throw refuse("AGENT_CONFIRMATION_NOT_FOUND", "There is no pending confirmation " + id + ".", 404);
    return c;
  };

  const methods: Pick<
    RetndApi,
    | "listAgentTokens" | "mintAgentToken" | "revokeAgentToken" | "listAgentCatalogue" | "listAgentConfirmations"
    | "approveAgentConfirmation" | "denyAgentConfirmation" | "listAgentAudit"
  > = {
    listAgentTokens: () => later(structuredClone(tokens)),
    mintAgentToken: async (req) => {
      await deps.transport(false);
      if (!/^[a-z0-9][a-z0-9-]{0,39}$/.test(req.name)) {
        throw refuse("INVALID_REQUEST", "name must be 1 to 40 lowercase letters, digits or hyphens, starting with a letter or digit.");
      }
      if (tokens.some((t) => t.name === req.name && t.status === "active")) {
        throw refuse("INVALID_REQUEST", "an active token named " + req.name + " already exists.", 409);
      }
      const ms = durationMs(req.expiresIn);
      if (ms === null) throw refuse("INVALID_REQUEST", "expires_in must be a number of days such as 90d, or a duration such as 720h.");
      if (ms > 365 * DAY) throw refuse("INVALID_REQUEST", "expires_in may not exceed 365d.");
      const token: AgentToken = {
        id: "tok_" + String(tokens.length + 1).padStart(2, "0"), name: req.name,
        prefix: MOCK_MINTED_SECRET.slice(0, 8), scope: req.scope, owner: "admin", status: "active",
        createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + ms).toISOString(),
        lastUsedAt: null, revokedAt: null, revokeCommand: null
      };
      token.revokeCommand = echoes ? "retnd agent token revoke " + token.id : null;
      tokens.unshift(token);
      await later(null);
      return {
        token: structuredClone(token), secret: MOCK_MINTED_SECRET,
        command: !echoes ? null : "retnd agent token create --name " + req.name + " --scope " + req.scope + " --expires-in " + req.expiresIn
      };
    },
    revokeAgentToken: async (id) => {
      const t = tokens.find((x) => x.id === id);
      if (!t) throw refuse("AGENT_TOKEN_NOT_FOUND", "There is no agent token " + id + ".", 404);
      t.status = "revoked";
      t.revokedAt = new Date().toISOString();
      t.revokeCommand = null;
      return later(structuredClone(t));
    },
    listAgentCatalogue: () =>
      later({ tools: structuredClone(tools), httpTransport: scenario === "agents-transport-on" ? ("on" as const) : ("off" as const) }),
    listAgentConfirmations: () => later(structuredClone(confirmations)),
    approveAgentConfirmation: async (id, opts) => {
      const c = find(id);
      await deps.transport(Boolean(opts.stepUpPassword));
      const failed = deps.stepUp(opts.stepUpPassword);
      if (failed) await failed;
      if (scenario === "agents-pending-expired") {
        throw refuse("AGENT_CONFIRMATION_EXPIRED", "this confirmation expired and nothing was run; ask the agent to make the request again.", 409, "cid_mockexpired");
      }
      confirmations.splice(confirmations.indexOf(c), 1);
      const gated = scenario === "agents-pending-gate" && c.tool === "run_cycle";
      audit.unshift({
        seq: (audit[0]?.seq ?? 0) + 1, at: new Date().toISOString(), tokenId: c.tokenId, tokenName: c.tokenName, tool: c.tool,
        scope: scopeOf.get(c.tool) ?? null, transport: "http", arguments: "{}", status: gated ? 403 : 202,
        code: gated ? "DESTRUCTIVE_OPERATIONS_DISABLED" : null, correlationId: gated ? "cid_mockgate01" : "cid_mockrun001"
      });
      return later({
        state: "approved" as const,
        outcome: gated
          ? { status: 403, code: "DESTRUCTIVE_OPERATIONS_DISABLED", message: MOCK_GATE_SENTENCE, correlationId: "cid_mockgate01", exitCode: 50 }
          : { status: 202, code: null, message: null, correlationId: "cid_mockrun001", exitCode: 0 }
      });
    },
    denyAgentConfirmation: async (id) => {
      const c = find(id);
      confirmations.splice(confirmations.indexOf(c), 1);
      return later({ state: "denied" as const, outcome: null });
    },
    listAgentAudit: async (filter) => {
      if (scenario === "agents-audit-failed") {
        throw refuse("INTERNAL", "the audit could not be read.", 500, "cid_mockaudit500");
      }
      const since = filter.since ? durationMs(filter.since) : null;
      if (filter.since && since === null) throw refuse("INVALID_REQUEST", "since must be a duration such as 24h or 7d.");
      const matching = audit.filter(
        (r) => (!filter.tokenId || r.tokenId === filter.tokenId) && (since === null || Date.parse(r.at) >= Date.now() - since)
      );
      const from = filter.cursor ? Number(filter.cursor) : 0;
      const page = matching.slice(from, from + 25);
      const next = from + 25 < matching.length ? String(from + 25) : null;
      const q = [filter.tokenId ? " --token " + filter.tokenId : "", filter.since ? " --since " + filter.since : ""].join("");
      return later({ rows: structuredClone(page), nextCursor: next, command: echoes ? "retnd agent audit" + q : null });
    }
  };
  return methods;
}
