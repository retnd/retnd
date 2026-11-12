/**
 * The wire side of the agent routes the card reads (EPIC U, issue #1351).
 *
 * The token routes (GET and POST /agent/tokens, POST /agent/tokens/{id}/revoke)
 * are in the generated contract (api/generated/contract.ts, #1345) and are
 * mapped from those types. The catalogue, confirmation and audit routes are
 * #1346, #1348 and #1349, which had not pushed their contract when this card
 * was written, so their shapes are declared here from the spec's names table
 * (docs/EPIC-U-mcp-server.md section 12) and the design note's assumptions 2,
 * 5, 6, 8 and 9. When those branches land, the interfaces below are replaced
 * by the generated ones and nothing else in the card changes: the rest of the
 * UI reads the domain types in types/agents.ts only.
 */
import type { WireAgentToken, WireMintedAgentToken } from "@shared/api/generated/contract";
import type {
  AgentAuditPage, AgentAuditRow, AgentCatalogue, AgentConfirmation, AgentDecision, AgentOutcome,
  AgentScope, AgentToken, MintedAgentToken
} from "@shared/types/agents";

/** A token row, plus the revoke twin the engine's response may carry. */
export type WireAgentTokenRow = WireAgentToken & { revoke_command?: string };
export type WireMintedAgentTokenRow = WireMintedAgentToken & { command?: string };

export interface WireAgentTools {
  tools: Array<{ name: string; scope: AgentScope }>;
  http_transport?: "on" | "off";
}

export interface WireAgentConfirmation {
  id: string;
  token_id: string;
  token_name: string;
  tool: string;
  description: string;
  command: string;
  approve_command?: string;
  deny_command?: string;
  backup_set?: string;
  created_at: string;
  expires_at: string;
  state?: string;
}

export interface WireAgentOutcome {
  status: number;
  code?: string;
  message?: string;
  correlation_id?: string;
  exit_code?: number;
}

export interface WireAgentDecision {
  confirmation: WireAgentConfirmation;
  result?: WireAgentOutcome;
}

export interface WireAgentAuditRow {
  seq: number;
  at: string;
  token_id: string;
  token_name: string;
  tool: string;
  scope?: AgentScope;
  transport: string;
  arguments?: string;
  status: number;
  code?: string;
  correlation_id: string;
}

export interface WireAgentAuditPage {
  rows: WireAgentAuditRow[];
  next_cursor?: string;
  command?: string;
}

export const fromWireAgentToken = (w: WireAgentTokenRow): AgentToken => ({
  id: w.id,
  name: w.name,
  prefix: w.prefix,
  scope: w.scope,
  owner: w.owner,
  createdAt: w.created_at,
  expiresAt: w.expires_at,
  lastUsedAt: w.last_used_at,
  revokedAt: w.revoked_at,
  status: w.status,
  revokeCommand: w.revoke_command ?? null
});

export const fromWireMintedAgentToken = (w: WireMintedAgentTokenRow): MintedAgentToken => ({
  token: fromWireAgentToken(w.agent_token),
  secret: w.token,
  command: w.command ?? null
});

export const fromWireAgentTools = (w: WireAgentTools): AgentCatalogue => ({
  tools: (w.tools ?? []).map((t) => ({ name: t.name, scope: t.scope })),
  httpTransport: w.http_transport ?? null
});

export const fromWireAgentConfirmation = (w: WireAgentConfirmation): AgentConfirmation => ({
  id: w.id,
  tokenId: w.token_id,
  tokenName: w.token_name,
  tool: w.tool,
  description: w.description,
  command: w.command,
  approveCommand: w.approve_command ?? null,
  denyCommand: w.deny_command ?? null,
  backupSet: w.backup_set ?? null,
  createdAt: w.created_at,
  expiresAt: w.expires_at
});

const fromWireOutcome = (w: WireAgentOutcome): AgentOutcome => ({
  status: w.status,
  code: w.code ?? null,
  message: w.message ?? null,
  correlationId: w.correlation_id ?? null,
  exitCode: w.exit_code ?? null
});

export const fromWireAgentDecision = (w: WireAgentDecision, state: "approved" | "denied"): AgentDecision => ({
  state,
  outcome: w.result ? fromWireOutcome(w.result) : null
});

const fromWireAuditRow = (w: WireAgentAuditRow): AgentAuditRow => ({
  seq: w.seq,
  at: w.at,
  tokenId: w.token_id,
  tokenName: w.token_name,
  tool: w.tool,
  scope: w.scope ?? null,
  transport: w.transport,
  arguments: w.arguments ?? null,
  status: w.status,
  code: w.code ?? null,
  correlationId: w.correlation_id
});

export const fromWireAgentAudit = (w: WireAgentAuditPage): AgentAuditPage => ({
  rows: (w.rows ?? []).map(fromWireAuditRow),
  nextCursor: w.next_cursor ?? null,
  command: w.command ?? null
});
