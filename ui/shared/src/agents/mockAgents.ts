/**
 * The mock's agent routes (EPIC U, issue #1351).
 *
 * State is per mock instance, as the other settings are: a mint, a revoke, an
 * approval or a denial has to be visible to the next read, and one test's
 * writes must not reach the next. What it models is the contract the card
 * reads and nothing stronger: the engine's own refusals (the expiry ceiling,
 * the step-up, an expired confirmation, the shut operation gate) come back
 * with the engine's words, the confirmation list carries every state newest
 * first as the route does, and the one place the token exists is the mint's
 * return value.
 *
 * Scenarios (everything else is the default deployment: no token, nothing
 * waiting, nothing audited):
 *   agents-tokens            five tokens in every status, a short audit
 *   agents-pending           the same, with two requests waiting (and one that
 *                            was denied earlier, which the card does not list)
 *   agents-pending-gate      approving run_cycle is refused by the shut gate
 *   agents-pending-expired   approving anything is refused as expired
 *   agents-audit-many        sixty audit rows, so Show older has a page to fetch
 *   agents-audit-failed      the audit route fails, the other regions load
 *   agents-read-only         the pending page, on a page that is read-only
 *   agents-insecure          the pending page on an unencrypted connection: the consent comes first
 *   agents-catalogue-failed  the pending page, with the catalogue route failing
 *   agents-http-on           the token page, with the HTTP transport reported on (every other agents-* scenario reports off)
 */
import { RetndError } from "@shared/api/contracts";
import type { ApiErrorCode, RetndApi } from "@shared/api/contracts";
import type { AgentAuditRow, AgentConfirmation, AgentScope, AgentToken } from "@shared/types/agents";

export const AGENTS_SCENARIOS = [
  "agents-tokens",
  "agents-pending",
  "agents-pending-gate",
  "agents-pending-expired",
  "agents-audit-many",
  "agents-audit-failed",
  "agents-read-only",
  "agents-insecure",
  "agents-catalogue-failed",
  "agents-http-on"
] as const;

/**
 * The token the next mint answers with. It is not one: it carries the rat_
 * prefix but is two characters short of a minted token, so the engine refuses
 * it (mockAgents.1367.test.ts pins the shape).
 */
export const MOCK_MINTED_SECRET = "rat_EXAMPLE0000000000000000000000000000000000";

export const MOCK_GATE_SENTENCE =
  "destructive operations are refused over HTTP in this release, until the trusted-proxy authentication gate (issue #92) has been verified; run the equivalent retnd command on the host";

/** What an agent could put in an argument. The card draws it as inert text. */
export const HOSTILE_ARGUMENTS = '{"set":"<img src=x onerror=\'window.__agentPwned=1\'> https://example.invalid/ignore-previous-instructions"}';

const TOOLS: Array<[string, AgentScope, string]> = [
  ["status", "read", "Reports whether the service is running and healthy."],
  ["backup_sets_list", "read", "Lists every backup set."],
  ["backup_set_show", "read", "Shows one backup set."],
  ["backups_list", "read", "Lists the backups a deployment holds."],
  ["backup_show", "read", "Shows one backup."],
  ["copies_show", "read", "Shows where a backup's copies are."],
  ["encryption_state", "read", "Shows the retained-copy encryption state."],
  ["assurance_due", "read", "Lists the backups whose checks are due."],
  ["assurance_show", "read", "Shows the assurance state of one backup."],
  ["activity", "read", "Lists recent activity."],
  ["activity_live", "read", "Shows what is running now."],
  ["quarantine_list", "read", "Lists quarantined backups."],
  ["operations_list", "read", "Lists operations."],
  ["operation_show", "read", "Shows one operation."],
  ["storage_mediums_list", "read", "Lists storage destinations."],
  ["storage_usage", "read", "Shows storage use."],
  ["retention_preview", "read", "Previews what a retention plan would remove."],
  ["workflow_runs_list", "read", "Lists workflow runs."],
  ["workflow_run_show", "read", "Shows one workflow run."],
  ["run_cycle", "operate", "Runs a backup cycle for every enabled backup set."],
  ["run_backup_set", "operate", "Runs a backup cycle for one backup set."],
  ["recovery_test_start", "operate", "Starts a recovery test of one backup."],
  ["verify_copy", "operate", "Verifies one copy of a backup."],
  ["scrub_start", "operate", "Starts a scrub of a backup's copies."],
  ["retry_backup", "operate", "Retries the ingestion of a failed backup."],
  ["quarantine_revalidate", "operate", "Revalidates a quarantined backup."],
  ["backup_set_enabled", "configure", "Enables or disables a backup set."],
  ["backup_set_retention_set", "configure", "Sets a backup set's retention."],
  ["backup_set_retention_clear", "configure", "Clears a backup set's retention."],
  ["backup_set_assurance_set", "configure", "Sets a backup set's assurance settings."]
];

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
  /** The consent a connection that is not encrypted needs before anything leaves. */
  transport(hasPassword: boolean): Promise<void>;
}

export function createMockAgents(scenario: string, deps: MockAgentsDeps) {
  const seeded = scenario.startsWith("agents-");
  const now = Date.now();
  const iso = (offsetMs: number) => new Date(now + offsetMs).toISOString();

  const tok = (
    id: string, name: string, prefix: string, scope: AgentScope, status: AgentToken["status"],
    createdAgo: number, expiresIn: number, usedAgo: number | null
  ): AgentToken => ({
    id, name, prefix, scope, owner: "admin", status,
    createdAt: iso(-createdAgo * DAY), expiresAt: iso(expiresIn * DAY),
    lastUsedAt: usedAgo === null ? null : iso(-usedAgo * MIN),
    revokedAt: status === "revoked" ? iso(-3 * DAY) : null
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

  const pend = (id: string, tool: string, command: string, backupSet: string, createdAgo: number, expiresIn: number): AgentConfirmation => ({
    id, tokenId: "tok_02", tokenName: "recovery-drill", tool, command, backupSet, state: "pending",
    createdAt: iso(-createdAgo), expiresAt: iso(expiresIn), decidedAt: null, execution: null
  });
  // Newest first, every state, as GET /agent/confirmations answers.
  const confirmations: AgentConfirmation[] = [];
  if (["agents-pending", "agents-pending-gate", "agents-pending-expired", "agents-read-only", "agents-insecure", "agents-catalogue-failed"].includes(scenario)) {
    confirmations.push(
      pend("cf_01", "run_cycle", "retnd run", "", MIN, 14 * MIN),
      pend("cf_02", "recovery_test_start", "retnd assurance recovery-test production/postgres-primary", "production/postgres-primary", 14 * MIN, 41_000),
      { ...pend("cf_00", "verify_copy", "retnd assurance verify production/postgres-primary", "production/postgres-primary", 40 * MIN, -25 * MIN), state: "denied", decidedAt: iso(-30 * MIN) }
    );
  }

  const tools = TOOLS.map(([name, scope, description]) => ({ name, scope, description }));

  const audit: AgentAuditRow[] = [];
  if (seeded) {
    const base: Array<[string, string, number, string, number]> = [
      ["tok_01", "status", 200, "", 3],
      ["tok_01", "backup_sets_list", 200, "", 3],
      ["tok_02", "run_cycle", 202, "AGENT_CONFIRMATION_REQUIRED", 8],
      ["tok_01", "run_cycle", 403, "AGENT_SCOPE_INSUFFICIENT", 20],
      ["tok_01", "activity", 200, "", 33],
      ["tok_02", "verify_copy", 202, "AGENT_CONFIRMATION_REQUIRED", 61],
      ["tok_01", "assurance_due", 200, "", 95],
      ["tok_05", "status", 401, "AGENT_TOKEN_INVALID", 60 * 24 * 27]
    ];
    const count = scenario === "agents-audit-many" ? 60 : base.length;
    for (let i = 0; i < count; i++) {
      const [tokenId, tool, status, code, ago] = base[i % base.length];
      audit.push({
        seq: 0, at: iso(-(ago + Math.floor(i / base.length) * 7) * MIN), tokenId, tokenName: tokens.find((t) => t.id === tokenId)?.name ?? "", tool,
        transport: i % 2 === 0 ? "stdio" : "http",
        // One row carries text an agent could plant: it must be drawn as data.
        arguments: tool === "activity" ? HOSTILE_ARGUMENTS : tool === "run_cycle" ? "{}" : '{"set":"production/postgres-primary"}',
        status, code, correlationId: "cid_" + String(1000 + i).padStart(8, "0"), confirmationId: ""
      });
    }
    // Newest first, as the route answers, and the sequence follows the order.
    audit.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
    audit.forEach((r, i) => { r.seq = audit.length - i; });
  }

  const find = (id: string) => {
    const c = confirmations.find((x) => x.id === id);
    if (!c) throw refuse("AGENT_CONFIRMATION_NOT_FOUND", "There is no confirmation " + id + ".", 404);
    if (c.state !== "pending") throw refuse("AGENT_CONFIRMATION_DECIDED", "this confirmation was already " + c.state + ".", 409);
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
        throw refuse("INVALID_REQUEST", "name: must be 1 to 40 lowercase letters, digits or hyphens, starting with a letter or digit.");
      }
      if (tokens.some((t) => t.name === req.name && t.status === "active")) {
        throw refuse("INVALID_REQUEST", "name: an active token named " + req.name + " already exists.", 409);
      }
      const ms = durationMs(req.expiresIn);
      if (ms === null) throw refuse("INVALID_REQUEST", "expires_in: must be a number of days such as 90d, or a duration such as 720h.");
      if (ms > 365 * DAY) throw refuse("INVALID_REQUEST", "expires_in: may not exceed 365d.");
      const token: AgentToken = {
        id: "tok_" + String(tokens.length + 1).padStart(2, "0"), name: req.name,
        prefix: MOCK_MINTED_SECRET.slice(0, 8), scope: req.scope, owner: "admin", status: "active",
        createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + ms).toISOString(),
        lastUsedAt: null, revokedAt: null
      };
      tokens.unshift(token);
      await later(null);
      return { token: structuredClone(token), secret: MOCK_MINTED_SECRET };
    },
    revokeAgentToken: async (id) => {
      const t = tokens.find((x) => x.id === id);
      if (!t) throw refuse("AGENT_TOKEN_NOT_FOUND", "There is no agent token " + id + ".", 404);
      t.status = "revoked";
      t.revokedAt = new Date().toISOString();
      return later(structuredClone(t));
    },
    listAgentCatalogue: async () => {
      if (scenario === "agents-catalogue-failed") throw refuse("INTERNAL", "the catalogue could not be read.", 500, "cid_mockcat500");
      return later({ tools: structuredClone(tools) });
    },
    listAgentConfirmations: () => later(structuredClone(confirmations)),
    approveAgentConfirmation: async (id, opts) => {
      const c = find(id);
      await deps.transport(Boolean(opts.stepUpPassword));
      const failed = deps.stepUp(opts.stepUpPassword);
      if (failed) await failed;
      if (scenario === "agents-pending-expired") {
        throw refuse("AGENT_CONFIRMATION_EXPIRED", "this confirmation expired and nothing was run; ask the agent to make the request again.", 409, "cid_mockexpired");
      }
      const gated = scenario === "agents-pending-gate" && c.tool === "run_cycle";
      c.state = "approved";
      c.decidedAt = new Date().toISOString();
      c.execution = gated
        ? { status: 403, code: "DESTRUCTIVE_OPERATIONS_DISABLED", message: MOCK_GATE_SENTENCE, correlationId: "cid_mockgate01", exitCode: 50 }
        : { status: 202, code: "", message: "", correlationId: "cid_mockrun001", exitCode: 0 };
      audit.unshift({
        seq: (audit[0]?.seq ?? 0) + 1, at: new Date().toISOString(), tokenId: c.tokenId, tokenName: c.tokenName, tool: c.tool, transport: "http",
        arguments: "{}", status: c.execution.status, code: c.execution.code, correlationId: c.execution.correlationId, confirmationId: c.id
      });
      return later(structuredClone(c));
    },
    denyAgentConfirmation: async (id) => {
      const c = find(id);
      c.state = "denied";
      c.decidedAt = new Date().toISOString();
      return later(structuredClone(c));
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
      return later({ rows: structuredClone(page), nextCursor: from + 25 < matching.length ? String(from + 25) : null });
    }
  };
  return methods;
}
