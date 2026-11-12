/**
 * Agent tokens, confirmations and the audit as the UI holds them (EPIC U,
 * issue #1351; design docs/design/1350-agents.html).
 *
 * # What is not here
 *
 * No field holds a token's hash, and the token itself has exactly one home:
 * `MintedAgentToken.secret`, which the mint dialog reads once and the card's
 * state never sees. A token row carries the eight-character prefix and
 * nothing more of the secret.
 *
 * No command is composed in the browser. Every `command` below is a string
 * the engine's response carried (the recorded command of a request, or the
 * twin the engine echoes for the act), and a response that carries none draws
 * none, so there is no function in this module that builds one.
 */

export type AgentScope = "read" | "operate" | "configure";
export type AgentTokenStatus = "active" | "expired" | "revoked";

export interface AgentToken {
  id: string;
  name: string;
  /** The first eight characters of the token. Identifies it and is not it. */
  prefix: string;
  scope: AgentScope;
  owner: string;
  createdAt: string;
  expiresAt: string;
  lastUsedAt: string | null;
  revokedAt: string | null;
  /** Derived by the engine from the row on this request, never by the browser. */
  status: AgentTokenStatus;
  /** The `retnd agent token revoke` twin the response carried, or null. */
  revokeCommand: string | null;
}

/** The one answer that carries the token. */
export interface MintedAgentToken {
  token: AgentToken;
  secret: string;
  /** The `retnd agent token create` twin of the act, without the secret. */
  command: string | null;
}

export interface MintAgentTokenRequest {
  name: string;
  scope: AgentScope;
  expiresIn: string;
}

/** One tool of the engine's catalogue, as GET /agent/tools lists it. */
export interface AgentTool {
  name: string;
  scope: AgentScope;
}

export interface AgentCatalogue {
  tools: AgentTool[];
  /** Whether the streamable HTTP transport is on, or null when the response
   *  does not say (the card then draws no transport line). */
  httpTransport: "on" | "off" | null;
}

export interface AgentConfirmation {
  id: string;
  tokenId: string;
  tokenName: string;
  tool: string;
  /** The catalogue's static sentence for the tool. */
  description: string;
  /** The recorded retnd command the request will run. */
  command: string;
  approveCommand: string | null;
  denyCommand: string | null;
  backupSet: string | null;
  createdAt: string;
  expiresAt: string;
}

/** What the replay of an approved request answered. */
export interface AgentOutcome {
  status: number;
  code: string | null;
  message: string | null;
  correlationId: string | null;
  /** The exit code the CLI would have used for the same call. */
  exitCode: number | null;
}

export interface AgentDecision {
  state: "approved" | "denied";
  outcome: AgentOutcome | null;
}

export interface AgentAuditRow {
  seq: number;
  at: string;
  tokenId: string;
  tokenName: string;
  tool: string;
  /** The scope the tool requires, not the token's own. */
  scope: AgentScope | null;
  transport: string;
  /** The arguments after projection, as text an agent supplied. */
  arguments: string | null;
  status: number;
  code: string | null;
  correlationId: string;
}

export interface AgentAuditFilter {
  tokenId?: string;
  since?: string;
  cursor?: string;
}

export interface AgentAuditPage {
  rows: AgentAuditRow[];
  nextCursor: string | null;
  /** The twin of the query the engine answered, or null. */
  command: string | null;
}
