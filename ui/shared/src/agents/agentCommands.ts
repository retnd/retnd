/**
 * The three `retnd` commands the token and audit surfaces echo (Q13, #1367).
 *
 * The token and audit responses carry no `command` (only a confirmation does,
 * and the card prints the engine's), so the browser composes these three fixed
 * twins, the way the assurance and encryption screens compose theirs. They
 * are held to the CLI by agentCommands.1367.test.ts against what
 * core/cliecho/routes.go builds for the same inputs. None of them carries a
 * secret: a name, a scope, an expiry, a token id.
 */
import { shellQuote } from "@shared/assurance/assuranceCommands";

/** `retnd agent token create NAME [--scope S] [--expires-in D]`; an empty flag value is left out, as cliecho leaves it out. */
export function mintCommand(r: { name: string; scope: string; expiresIn: string }): string {
  return ["retnd agent token create", shellQuote(r.name), ...(r.scope ? ["--scope " + shellQuote(r.scope)] : []), ...(r.expiresIn ? ["--expires-in " + shellQuote(r.expiresIn)] : [])].join(" ");
}

/** `retnd agent token revoke ID`. */
export function revokeCommand(tokenId: string): string {
  return "retnd agent token revoke " + shellQuote(tokenId);
}

/** `retnd agent audit [--token ID] [--since D]`; only the filters that are set. */
export function auditCommand(f: { tokenId?: string; since?: string }): string {
  return ["retnd agent audit", ...(f.tokenId ? ["--token " + shellQuote(f.tokenId)] : []), ...(f.since ? ["--since " + shellQuote(f.since)] : [])].join(" ");
}
