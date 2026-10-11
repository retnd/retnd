/**
 * What the recovery and assurance surfaces know about a backup (issue #985,
 * EPIC Q).
 *
 * # Three claims, three homes
 *
 * `Stored`, `Content verified` and `Recovery tested` are separately durable,
 * separately aged and separately rendered, so each has its own field here and
 * nothing in this file rolls them up. There is no "overall" status and no
 * helper that would compute one: a roll-up is exactly where a checksum pass
 * and a validator failure get averaged into a tick.
 *
 * # Status is a closed enumeration and the zero value is `unknown`
 *
 * Five different reasons produce a not-a-pass (never configured, never run,
 * overdue, refused for cost, could not complete) and a sixth is a verdict
 * that came back wrong (failed). They are six values, not one, and
 * `unknown` is what anything this build does not recognise becomes (see
 * claims.ts), so an un-scrubbed, rebuilt or newer-than-this-build record can
 * never default to good.
 *
 * # Where these shapes come from
 *
 * The settings, per-set policy and schedule (#982), the recovery mint and
 * redeem (#976), the copies' verification records (#978) and the recovery
 * test's start route and verdicts (#980) are real routes and the screens call
 * them through the ordinary client. A copy's own scrub and recovery-test
 * record rides on the placement (`assurance_state`, `last_failure_*`,
 * `recovery_state`), so a failed copy is shown as failed. What the server does
 * NOT offer is a route to start a scrub, to cancel anything, or to read a
 * validator's output: the scrub runs from the schedule and `retnd scrub`.
 * {@link AssuranceApi} is the one seam for the claims and for those
 * operations, and `offers` says which of them the connected server really
 * has, so a surface never fabricates one.
 */

export type ClaimStatus =
  | "unknown"
  | "never_configured"
  | "never_run"
  | "pass"
  | "overdue"
  | "refused_for_cost"
  | "could_not_complete"
  | "failed"
  | "requires_restore"
  | "no_readable_copy"
  /** The recovery test itself concluded, but the staged copy it made could
   *  not be removed. A pass nobody should read as clean, and not a failure of
   *  the copy either, so it is its own state. */
  | "cleanup_failed";

/** The strongest verification class a copy has actually ACHIEVED, never the
 *  one configured (placements.verification_class). Only `content` is content
 *  verification. */
export type AchievedClass = "" | "content" | "attested" | "existence";

export interface ClaimRecord {
  status: ClaimStatus;
  achievedClass: AchievedClass;
  /** RFC 3339. A pass without a timestamp is not a pass (claims.ts). */
  at?: string;
}

/** One copy whose last check did not pass, said about that copy and not
 *  folded into a claim: which copy, what ran, what the journal recorded and
 *  how many times in a row it has gone wrong. */
export interface CopyFailure {
  /** "local" or the storage medium's id. */
  medium: string;
  kind: "scrub" | "recovery_test";
  /** The recorded verdict: failed or could_not_complete. */
  state: "failed" | "could_not_complete";
  /** The journal's bounded plain words. Empty when none was recorded. */
  reason: string;
  /** Scrub verdicts in a row that were not a pass. Zero for a recovery test,
   *  which keeps no streak. */
  consecutiveFailures: number;
  /** RFC 3339, when it last failed or could not complete. */
  at?: string;
}

export interface ArtifactAssurance {
  artifactId: string;
  /** How many copies are recorded. Zero is "no copy yet", not an error. */
  copies: number;
  content: ClaimRecord;
  recovery: ClaimRecord;
  /** Every copy whose last scrub or recovery test failed or could not
   *  complete. Empty or absent means no copy has such a record. */
  copyFailures?: CopyFailure[];
}

export type OperationKind = "scrub" | "recovery_test";
/** `untracked` is a run this page started and can no longer see: the live
 *  feed that carried its lines has dropped them. It is NOT a verdict. The
 *  Recovery chip is where the result lands. */
export type OperationState = "running" | "complete" | "failed" | "cancelled" | "untracked";

export interface OperationEvent {
  /** Strictly increasing per operation; the key reconnects deduplicate on. */
  seq: number;
  at: string;
  stage: string;
  text: string;
}

export interface Operation {
  id: string;
  kind: OperationKind;
  setId: string;
  state: OperationState;
  /** The current named stage. */
  stage: string;
  bytes: number;
  totalBytes: number;
  startedAt: string;
}

export interface OperationFeed {
  operation: Operation;
  /** Events strictly after the cursor that was asked for. */
  events: OperationEvent[];
  nextCursor: number;
}

export interface ValidatorResult {
  /** What the engine recorded about the run, none of which the script
   *  chooses: the artifact and copy it recovered, the validator script's
   *  file name and the hash of the bytes that actually ran. */
  artifactId?: string;
  copy?: string;
  validatorName?: string;
  validatorSha256?: string;
  /** How the script ended. Its exit status is one input to the engine's
   *  verdict and never the verdict itself. */
  exitStatus: number;
  durationMs: number;
  /** Untrusted terminal input. Rendered inert (ValidatorLog.tsx). */
  output: string;
  /** Bytes dropped before `output`, or 0. Carried beside the content, never
   *  inside it, so the marker cannot be forged by the content. */
  truncatedBytes: number;
}

/** Which parts of the seam the connected server really offers. A part it
 *  does not offer is not mocked in production: the screen says so and shows
 *  the terminal command that does exist. */
export interface AssuranceOffers {
  /** Start a scrub from the browser, follow it and cancel it. No such route
   *  exists in the contract today, so a production client says false. */
  operations: boolean;
  /** Start a recovery test now (POST /backup-sets/{source}/{set}/recovery-test)
   *  and follow it on the live feed. Cancelling one is not part of this. */
  recoveryTest: boolean;
  /** The recovery-test claim is reported per copy, on the placement. */
  recoveryClaim: boolean;
}

export interface AssuranceApi {
  offers: AssuranceOffers;
  /** The three claims of every backup (or of one set's), composed from what
   *  the server does record: each copy's achieved verification class and
   *  time, its access, and the scrub schedule. */
  listArtifactAssurance(setId?: string): Promise<ArtifactAssurance[]>;
  getArtifactAssurance(artifactId: string): Promise<ArtifactAssurance>;

  // startScrub, cancelOperation and getValidatorResult exist only when
  // `offers.operations` is true; startRecoveryTest, activeOperation and
  // readOperation when `offers.recoveryTest` is.
  startScrub(setId: string): Promise<Operation>;
  startRecoveryTest(setId: string): Promise<Operation>;
  /** The set's operation that is still running, if one is: what a reload
   *  asks, instead of starting another. */
  activeOperation(setId: string): Promise<Operation | null>;
  readOperation(operationId: string, cursor: number): Promise<OperationFeed>;
  cancelOperation(operationId: string): Promise<void>;
  /** The set's most recent recovery test's validator, or null when none has run. */
  getValidatorResult(setId: string): Promise<ValidatorResult | null>;
}
