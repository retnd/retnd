/**
 * Retry ingestion's words, shared by the backup's own Recovery card and the
 * Backups row menu (#662, #1202), so the two places that offer the same
 * request say the same thing about its answer.
 */
import { RetndError, RequestFailure } from "@shared/api/contracts";
import type { ApiErrorCode } from "@shared/api/contracts";
import type { BackupArtifact } from "@shared/types/backup";

/** Which backups get the control: FAILED, the state whose two exits are both
 *  operator-only (BackupDetailPage explains why it is read off the state and
 *  not the verdicts). A quarantined backup is never FAILED, so the Quarantine
 *  page's verbs and this one never overlap. */
export function isRetryable(a: Pick<BackupArtifact, "state">): boolean {
  return a.state === "FAILED";
}

/** What an accepted retry means: accepted, not recovered. */
export const RETRY_ACCEPTED =
  "Re-attempted: the service accepted the request and has not carried it out yet, so what this page " +
  "shows below is the state the next attempt leaves. If its local copy was already good and the retry " +
  "keeps meeting a final-name collision, that retry verifies the copy against the remote object and " +
  "trusts it in place.";

/**
 * What this card says when the retry did not go through.
 *
 * It rendered `e.message` bare, which fails the operator in the exact way
 * #662 complains about the docked terminal: a control that refuses and
 * explains nothing. Three things are added and none of them is invented.
 *
 * The correlation id, whenever the response carried one. It is the only
 * token that ties what an operator saw to what the engine logged, and the
 * position #662 was filed from — a NAS with no shell — is exactly the
 * position where quoting that id into an issue is all somebody can do.
 *
 * A next step for the two refusals whose own message cannot carry one.
 * ARTIFACT_NOT_FAILED means the row moved while this page was open (a
 * cycle, another browser, or the operator's own earlier press): the page
 * is stale rather than wrong, and reloading is the whole remedy. An
 * authentication or CSRF refusal is about the session and not about the
 * backup at all, so it must not read as a verdict on the backup.
 *
 * And the two non-refusals RequestFailure labels, kept apart, because
 * "nothing came back" leaves it genuinely unknown whether the retry was
 * carried out, and telling somebody to press again when it may already
 * have run is advice with a cost.
 *
 * # There is deliberately no destructive-gate branch
 *
 * POST /api/v1/backups/{source}/{set}/{name}/retry carries requireCSRF
 * and NOT requireDestructiveGate (router.go:425), the generated contract
 * records `destructiveGate: false` for `retryFailedIngestion`, and
 * webhost's own contract walk asserts that a closed production gate must
 * NOT refuse an operation declared ungated. So this button does not 403
 * on the shipped NotYetImplementedGate, DESTRUCTIVE_OPERATIONS_DISABLED
 * is not among the codes this route can emit, and a branch explaining
 * issue #92 here would be this page inventing a refusal the server never
 * sends — the same species of untruth as the fixture that used to gate
 * the card above.
 */
export function retryRefusalSentence(e: unknown): string {
  if (e instanceof RetndError) {
    const step = RETRY_REFUSAL_NEXT_STEP[e.api.code] ?? "";
    return [e.api.message, step, correlationSuffix(e.api.correlationId)].filter((part) => part !== "").join(" ");
  }
  if (e instanceof RequestFailure) {
    const said =
      e.kind === "no-response"
        ? "The backup service did not answer, so whether the re-attempt was started is unknown. Reload this page" +
          " before pressing again: if the backup is no longer FAILED, it did start."
        : "The backup service answered with something this build could not read, so what happened to the" +
          " re-attempt is unknown. Reload this page to see the backup's current state.";
    return [said, correlationSuffix(e.correlationId)].filter((part) => part !== "").join(" ");
  }
  // Not a refusal and not a request failure, so it did not come out of the
  // request at all (RequestFailure's own doc makes that distinction the
  // point of the type): it came out of what was chained onto it, and there
  // is nothing honest to add to it.
  return e instanceof Error ? e.message : String(e);
}

/** The refusals whose own sentence cannot say what to do next, because
 *  what to do next is about this page or this session rather than about
 *  the backup. Every other code is reported in the engine's own words
 *  alone, which is the house rule: a paraphrase kept in a frontend
 *  eventually says something the engine does not. */
const RETRY_REFUSAL_NEXT_STEP: Partial<Record<ApiErrorCode, string>> = {
  ARTIFACT_NOT_FAILED:
    "This backup is no longer FAILED, so this page is showing a state it has already left. Reload it to see" +
    " where the backup is now.",
  UNAUTHENTICATED: "This is about the session, not about the backup. Sign in again and the re-attempt is still available.",
  CSRF_TOKEN_MISSING: "This is about the session, not about the backup. Reload the page and press again.",
  CSRF_TOKEN_MISMATCH: "This is about the session, not about the backup. Reload the page and press again."
};

/** The correlation id as a clause, or nothing at all when the response
 *  carried none. Never a placeholder: an id an operator quotes has to be
 *  one the engine's log actually contains (the rule fromWireTransition
 *  states for the same field one file over). */
function correlationSuffix(correlationId: string | undefined): string {
  return correlationId ? "Correlation id " + correlationId + "." : "";
}
