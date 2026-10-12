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
 * `recovery_state`), so a failed copy is shown as failed. The routes to start
 * a scrub, to cancel a scrub or a recovery test and to read a validator's
 * output exist too (#1059), and `offers.operations` says whether the
 * connected server has them: a server that predates them gets the terminal
 * commands instead of a button that could only fail.
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

/** What one copy has on record for the Content claim. `unchecked` is a copy
 *  nobody has read back (no class, no time), which is not the same fact as a
 *  copy whose check came back wrong. */
export type CopyContent = "content" | "attested" | "existence" | "could_not_complete" | "failed" | "unchecked";

export interface ArtifactAssurance {
  artifactId: string;
  /** How many copies are recorded. Zero is "no copy yet", not an error. */
  copies: number;
  /** Each live copy's own Content record, one entry per copy. The `content`
   *  claim below keeps the strongest class across copies (that is how FR-68
   *  resolves it), so on its own it would read "Content verified" for a
   *  backup whose other copies nobody has looked at. This is what lets the
   *  chip say how many copies the claim covers and what the weakest is
   *  (FR-72, D3). Absent means the server did not itemise them. */
  copyContent?: CopyContent[];
  content: ClaimRecord;
  recovery: ClaimRecord;
  /** Every copy whose last scrub or recovery test failed or could not
   *  complete. Empty or absent means no copy has such a record. */
  copyFailures?: CopyFailure[];
}

export type OperationKind = "scrub" | "recovery_test" | "recovery";
/** `untracked` is a run this page started and can no longer see: the live
 *  feed that carried its lines has dropped them. It is NOT a verdict. The
 *  chip the run feeds is where the result lands. */
export type OperationState = "running" | "complete" | "failed" | "cancelled" | "untracked";

export interface OperationEvent {
  /** Strictly increasing per operation; the key reconnects deduplicate on. */
  seq: number;
  at: string;
  stage: string;
  text: string;
  /** What the line is. `stage` lines are the named steps the terminal's
   *  --follow prints too, so the two can be held against one list. */
  kind?: "start" | "stage" | "end" | "scrub";
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
  /** This page asked for the run to stop and the run has not ended yet.
   *  A request, not a fact: the end line is the fact. */
  cancelling?: boolean;
  /** Starting did not start a second run: it joined one already running for
   *  the set, and `id` is that run's. */
  joined?: boolean;
}

export interface OperationFeed {
  operation: Operation;
  /** Events strictly after the cursor that was asked for. */
  events: OperationEvent[];
  nextCursor: number;
}

/** What the newest recovery test of a set printed, as the server keeps it
 *  (GET .../recovery-test/output). The engine's own record of the run, the
 *  verdict and the script's output are separate things: nothing here lets
 *  the output stand for the verdict. */
export interface ValidatorResult {
  artifactId: string;
  /** "local" or the storage medium the tested copy was on. */
  medium: string;
  operationId: string;
  /** The engine's recorded verdict for that run. */
  state: "passed" | "failed" | "refused_for_cost" | "could_not_complete";
  recordedAt: string;
  /** Whether the runner dropped output to stay inside its bounds. */
  truncated: boolean;
  /** Untrusted terminal input, in order. Rendered inert (ValidatorLog.tsx).
   *  Branch on `kind`, never on `text`. */
  records: Array<{ kind: "output" | "truncation"; text: string }>;
}

/** Which parts of the seam the connected server really offers. A part it
 *  does not offer is not mocked in production: the screen says so and shows
 *  the terminal command that does exist. */
export interface AssuranceOffers {
  /** Start a scrub from the browser, cancel a scrub or a recovery test, and
   *  read a validator's output. A server without those routes says false and
   *  the screens show the terminal commands instead. */
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

  // startScrub, cancelOperation and getValidatorResult are real only when
  // `offers.operations` is true; startRecoveryTest, activeOperation and
  // readOperation when `offers.recoveryTest` is.
  startScrub(setId: string): Promise<Operation>;
  startRecoveryTest(setId: string): Promise<Operation>;
  /** The set's operation that is still running, if one is: what a reload
   *  asks, instead of starting another. */
  activeOperation(setId: string): Promise<Operation | null>;
  readOperation(operationId: string, cursor: number): Promise<OperationFeed>;
  cancelOperation(operationId: string): Promise<void>;
  /** The set's most recent recovery test's recorded output, or null when no
   *  test has recorded any. */
  getValidatorResult(setId: string): Promise<ValidatorResult | null>;
}
