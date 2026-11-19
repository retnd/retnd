/**
 * The destructive gate, as a screen has to talk about it (issue #1157).
 *
 * On a gateway profile (UGOS) the gate is shut, so starting a run, restoring
 * a snapshot and applying a retention plan are refused over HTTP with the
 * code below (issue #1163 opened it for local-account profiles, and #1164
 * took verify, hold and release-hold off it altogether, so those three are
 * never refused). The refusal is not transient. Pressing the control again
 * gets the same answer, and nothing an operator can set opens the gate: what
 * lifts it is issue #92 shipping a verified one. So a screen that meets this refusal
 * does not say "could not start" and offer a retry. It says that retrying
 * will not help, that nothing was changed, and what to type on the host.
 *
 * The code and the status are the engine's and have not changed. This module
 * only decides what an operator is told.
 */
import { apiErrorOf } from "@shared/api/failure";

/** The wire code the engine answers a gated route with while the gate is
 *  shut (apps/common/webhost/auth.go). */
export const DESTRUCTIVE_GATE_CODE = "DESTRUCTIVE_OPERATIONS_DISABLED";

/** Whether this failure is the shut gate's refusal, by the engine's own code
 *  and never by a status, since a 403 is also what a CSRF failure answers. */
export function isGateRefusal(e: unknown): boolean {
  return apiErrorOf(e)?.code === DESTRUCTIVE_GATE_CODE;
}

/** What a screen shows for a refused gated act. */
export interface GateRefusalNotice {
  /** One sentence naming what was refused and that nothing happened. */
  headline: string;
  /** Why retrying is pointless, in the engine's terms. */
  why: string;
  /** The `retnd` command that does the same act from a shell on the host. */
  command: string;
  /** The id the refusal carried, when it carried one. */
  correlationId?: string;
}

/**
 * Builds the notice for one refused act.
 *
 * `outcome` is what did not happen, as a clause ("The verification was not
 * started"). `nothingChanged` says what stayed as it was, because each act
 * has a different answer: a refused retention apply deleted nothing, a
 * refused restore wrote nothing. `reason` is the gate's own reason, read
 * from the capability field when this build reports one (issue #1165), and
 * falls back to the sentence that has been true of every shipped build.
 */
export function gateRefusalNotice(args: {
  outcome: string;
  nothingChanged: string;
  command: string;
  reason?: string;
  correlationId?: string;
}): GateRefusalNotice {
  return {
    headline: args.outcome + ": this release refuses it over HTTP.",
    why:
      "Retrying will not help. " +
      (args.reason && args.reason !== ""
        ? "This deployment reports its destructive gate as shut (" + args.reason + ")."
        : "Destructive operations are refused over HTTP on this deployment's profile, until the trusted-proxy authentication gate (issue #92) has been verified.") +
      " " +
      args.nothingChanged,
    command: args.command,
    correlationId: args.correlationId
  };
}
