/**
 * The wire side of the agent routes the card reads (EPIC U, issue #1351).
 *
 * The token routes (#1345) and the confirmation routes (#1349) are mapped from
 * the generated contract (api/generated/contract.ts). GET /agent/tools (#1346)
 * and GET /agent/audit (#1348) are on sibling branches whose generated
 * contract this branch has not merged, so their response shapes are declared
 * below exactly as those branches generate them (WireAgentToolsResponse and
 * WireListAgentAuditResponse). When the chain is merged, the two interfaces
 * are replaced by the generated ones and nothing else in the card changes: the
 * rest of the UI reads the domain types in types/agents.ts only.
 */
import type { WireAgentConfirmation, WireAgentExecution, WireAgentToken, WireMintedAgentToken } from "@shared/api/generated/contract";
import type {
  AgentAuditPage, AgentAuditRow, AgentCatalogue, AgentConfirmation, AgentExecution, AgentScope, AgentToken, MintedAgentToken
} from "@shared/types/agents";

/** As generated on feat/1346-catalogue (the fields the card reads). */
export interface WireAgentToolsResponse {
  tools: Array<{ name: string; scope: AgentScope; description: string }>;
}

/** As generated on feat/1348-audit. */
export interface WireListAgentAuditResponse {
  entries: Array<{
    arguments: string;
    at: string;
    code: string;
    confirmation_id: string;
    correlation_id: string;
    seq: number;
    status: number;
    token_id: string;
    /**
     * Q15 (#1365): the name, joined at read time by the engine, empty for a row
     * with no token. Optional here only until #1365's generated binding is merged
     * in (TODO at integration: delete `?` and this note).
     */
    token_name?: string;
    tool: string;
    transport: string;
  }>;
  next_cursor?: string;
}

/**
 * Which form field a mint refusal belongs to (Q16, #1367). The engine's contract
 * is that every mint INVALID_REQUEST message starts with the request's JSON
 * field name and a colon; anything else is the form's own error.
 */
export function mintRefusalField(message: string): "name" | "expires_in" | "scope" | null {
  const m = /^(name|expires_in|scope):/.exec(message);
  return m ? (m[1] as "name" | "expires_in" | "scope") : null;
}

export const fromWireAgentToken = (w: WireAgentToken): AgentToken => ({
  id: w.id,
  name: w.name,
  prefix: w.prefix,
  scope: w.scope,
  owner: w.owner,
  createdAt: w.created_at,
  expiresAt: w.expires_at,
  lastUsedAt: w.last_used_at,
  revokedAt: w.revoked_at,
  status: w.status
});

export const fromWireMintedAgentToken = (w: WireMintedAgentToken): MintedAgentToken => ({
  token: fromWireAgentToken(w.agent_token),
  secret: w.token
});

export const fromWireAgentTools = (w: WireAgentToolsResponse): AgentCatalogue => ({
  tools: (w.tools ?? []).map((t) => ({ name: t.name, scope: t.scope, description: t.description }))
});

const fromWireExecution = (w: WireAgentExecution): AgentExecution => ({
  status: w.status,
  code: w.code,
  message: w.message,
  correlationId: w.correlation_id,
  exitCode: w.exit_code
});

export const fromWireAgentConfirmation = (w: WireAgentConfirmation): AgentConfirmation => ({
  id: w.id,
  tokenId: w.token_id,
  tokenName: w.token_name,
  tool: w.tool,
  command: w.command,
  backupSet: w.backup_set === "" ? null : w.backup_set,
  state: w.state,
  createdAt: w.created_at,
  expiresAt: w.expires_at,
  decidedAt: w.decided_at,
  execution: w.execution ? fromWireExecution(w.execution) : null
});

const fromWireAuditRow = (w: WireListAgentAuditResponse["entries"][number]): AgentAuditRow => ({
  seq: w.seq,
  at: w.at,
  tokenId: w.token_id,
  tokenName: w.token_name ?? "",
  tool: w.tool,
  transport: w.transport,
  arguments: w.arguments,
  status: w.status,
  code: w.code,
  correlationId: w.correlation_id,
  confirmationId: w.confirmation_id
});

export const fromWireAgentAudit = (w: WireListAgentAuditResponse): AgentAuditPage => ({
  rows: (w.entries ?? []).map(fromWireAuditRow),
  nextCursor: w.next_cursor ?? null
});
