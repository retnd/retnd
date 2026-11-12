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
 * No command is composed in the browser. The only `command` below is the one
 * the engine recorded with a request an agent made, carried by the response
 * as it is. The token and audit responses carry no command, so those
 * surfaces draw none, and there is no function in this module that builds one.
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
}

/** The one answer that carries the token. */
export interface MintedAgentToken {
  token: AgentToken;
  secret: string;
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
  /** The catalogue's static sentence for the tool. */
  description: string;
}

export interface AgentCatalogue {
  tools: AgentTool[];
}

export type AgentConfirmationState = "pending" | "approved" | "denied" | "expired";

/** How the replay of an approved request ended, in the engine's words. */
export interface AgentExecution {
  status: number;
  /** Empty for a success. */
  code: string;
  message: string;
  correlationId: string;
  /** The exit code the CLI would have used for the same answer. */
  exitCode: number;
}

export interface AgentConfirmation {
  id: string;
  tokenId: string;
  tokenName: string;
  tool: string;
  /** The recorded retnd command the request will run. */
  command: string;
  backupSet: string | null;
  state: AgentConfirmationState;
  createdAt: string;
  expiresAt: string;
  decidedAt: string | null;
  /** Present once an approved request has finished replaying. */
  execution: AgentExecution | null;
}

export interface AgentAuditRow {
  seq: number;
  at: string;
  /** Empty for a refusal of a token that matched no row. */
  tokenId: string;
  /** Empty for a refusal made before any tool was named. */
  tool: string;
  transport: string;
  /** The arguments after projection, as JSON text an agent supplied. */
  arguments: string;
  status: number;
  /** Empty for a success. */
  code: string;
  correlationId: string;
  confirmationId: string;
}

export interface AgentAuditFilter {
  tokenId?: string;
  since?: string;
  cursor?: string;
}

export interface AgentAuditPage {
  rows: AgentAuditRow[];
  nextCursor: string | null;
}
